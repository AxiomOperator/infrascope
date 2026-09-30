// Package webpush sends browser notifications with the Web Push protocol:
// RFC 8030 (delivery), RFC 8291 (message encryption, aes128gcm of RFC 8188)
// and RFC 8292 (VAPID authentication), using only the standard library.
//
// Subscriptions are stored in the push_subscriptions collection, one per
// browser/device of a user.
package webpush

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Collection is the name of the subscriptions collection.
const Collection = "push_subscriptions"

const (
	// MaxPayloadSize bounds the JSON payload of a message; the body is
	// truncated to fit. Push services accept 4096 octets including the
	// encryption overhead.
	MaxPayloadSize = 3072
	// MaxFailures is the number of consecutive failed deliveries after which a
	// subscription is deleted.
	MaxFailures = 10
	// DefaultTTL is how long push services keep undelivered messages (seconds).
	DefaultTTL = 86400
	// defaultConcurrency limits parallel deliveries of one Send.
	defaultConcurrency = 4
	// requestTimeout bounds one delivery.
	requestTimeout = 30 * time.Second
)

var (
	// ErrDisabled is returned when the service has no VAPID key.
	ErrDisabled = errors.New("browser notifications are not available")
	// ErrNoSubscriptions is returned by Send when the user has no subscribed devices.
	ErrNoSubscriptions = errors.New("no browser subscriptions")
	// ErrSubscriptionGone is returned when the push service reports the
	// subscription expired or unsubscribed (the subscription is deleted).
	ErrSubscriptionGone = errors.New("browser subscription expired")
	// ErrNotFound is returned by SendOne for unknown or foreign subscriptions.
	ErrNotFound = errors.New("subscription not found")
)

// Message is a browser notification.
type Message struct {
	Title string
	Body  string
	// URL is opened when the notification is clicked.
	URL string
	// Tag replaces an earlier notification with the same tag on the device.
	Tag string
	// Urgent asks for immediate delivery and keeps the notification on screen.
	Urgent bool
}

// Service sends Web Push messages to the subscriptions of users.
type Service struct {
	app         core.App
	key         *ecdsa.PrivateKey
	publicKey   []byte
	client      *http.Client
	subject     func() string
	concurrency int
	ttl         int
	now         func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithHTTPClient replaces the default client, which refuses internal
// addresses. Tests use it to reach local push services.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) { s.client = client }
}

// WithSubject overrides the VAPID subject (a mailto: or https: URL).
func WithSubject(subject func() string) Option {
	return func(s *Service) { s.subject = subject }
}

// WithConcurrency sets the maximum parallel deliveries of one Send.
func WithConcurrency(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.concurrency = n
		}
	}
}

// WithTTL sets how long push services keep undelivered messages.
func WithTTL(seconds int) Option {
	return func(s *Service) {
		if seconds >= 0 {
			s.ttl = seconds
		}
	}
}

// New returns a Service using the VAPID key in the app data directory
// (KeyFileName), creating the key on first use.
func New(app core.App, opts ...Option) (*Service, error) {
	s := &Service{
		app:         app,
		client:      newGuardedClient(),
		concurrency: defaultConcurrency,
		ttl:         DefaultTTL,
		now:         time.Now,
	}
	s.subject = s.defaultSubject
	for _, opt := range opts {
		opt(s)
	}
	if s.key == nil {
		key, err := loadOrCreateKey(filepath.Join(app.DataDir(), KeyFileName))
		if err != nil {
			return nil, fmt.Errorf("load VAPID key: %w", err)
		}
		s.key = key
	}
	publicKey, err := publicKeyBytes(s.key)
	if err != nil {
		return nil, err
	}
	s.publicKey = publicKey
	return s, nil
}

// Enabled reports whether the service can send messages.
func (s *Service) Enabled() bool {
	return s != nil && s.key != nil
}

// PublicKey returns the VAPID public key (base64url, uncompressed point), the
// applicationServerKey browsers subscribe with.
func (s *Service) PublicKey() string {
	if !s.Enabled() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(s.publicKey)
}

// defaultSubject is mailto: the settings sender address, or the app URL when
// it is https.
func (s *Service) defaultSubject() string {
	meta := s.app.Settings().Meta
	if address := strings.TrimSpace(meta.SenderAddress); address != "" {
		return "mailto:" + address
	}
	if strings.HasPrefix(meta.AppURL, "https://") {
		return meta.AppURL
	}
	return "mailto:admin@localhost"
}

// Send delivers a message to every subscription of the user and returns the
// number of devices it was delivered to. The error is nil when at least one
// delivery succeeded; ErrNoSubscriptions when the user has none.
func (s *Service) Send(ctx context.Context, userID string, msg Message) (sent int, err error) {
	if !s.Enabled() {
		return 0, ErrDisabled
	}
	records, err := s.app.FindRecordsByFilter(Collection, "user = {:user}", "createdAt", 0, 0, dbx.Params{"user": userID})
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, ErrNoSubscriptions
	}
	payload, err := buildPayload(msg)
	if err != nil {
		return 0, err
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, s.concurrency)
	)
	for _, record := range records {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return sent, ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			err := s.deliver(ctx, record, payload, msg.Urgent)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				sent++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if sent == 0 {
		return 0, errors.Join(errs...)
	}
	return sent, nil
}

// SendOne delivers a message to one subscription of the user.
func (s *Service) SendOne(ctx context.Context, userID, subscriptionID string, msg Message) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	record, err := s.app.FindRecordById(Collection, subscriptionID)
	if err != nil || record.GetString("user") != userID {
		return ErrNotFound
	}
	payload, err := buildPayload(msg)
	if err != nil {
		return err
	}
	return s.deliver(ctx, record, payload, msg.Urgent)
}

// deliverError describes a failed delivery without leaking the endpoint.
type deliverError struct {
	status int
	err    error
}

func (e *deliverError) Error() string {
	if e.err != nil {
		return "push delivery failed: " + e.err.Error()
	}
	return "push service responded " + strconv.Itoa(e.status)
}

func (e *deliverError) Unwrap() error { return e.err }

// deliver sends an encrypted payload to one subscription and records the outcome.
func (s *Service) deliver(ctx context.Context, record *core.Record, payload []byte, urgent bool) error {
	endpoint := record.GetString("endpoint")
	uaPublic, authSecret, err := parseKeys(record.GetString("p256dh"), record.GetString("auth"))
	if err != nil {
		// unusable subscription; the browser will subscribe again
		_ = s.app.Delete(record)
		return err
	}
	// the dialer of the default client blocks internal addresses
	if !strings.HasPrefix(endpoint, "https://") {
		return ErrInvalidEndpoint
	}
	body, err := encrypt(payload, uaPublic, authSecret, nil, nil)
	if err != nil {
		return err
	}
	auth, err := authorization(s.key, s.publicKey, endpoint, s.subject(), s.now())
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", auth)
	request.Header.Set("Content-Encoding", "aes128gcm")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("TTL", strconv.Itoa(s.ttl))
	if urgent {
		request.Header.Set("Urgency", "high")
	} else {
		request.Header.Set("Urgency", "normal")
	}
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			// canceled by the caller, not the subscription's fault
			return err
		}
		s.recordFailure(record)
		return &deliverError{err: err}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
	switch status := response.StatusCode; {
	case status >= 200 && status < 300:
		s.recordSuccess(record)
		return nil
	case status == http.StatusNotFound || status == http.StatusGone:
		_ = s.app.Delete(record)
		return ErrSubscriptionGone
	default:
		s.recordFailure(record)
		return &deliverError{status: status}
	}
}

func (s *Service) recordSuccess(record *core.Record) {
	record.Set("lastSuccessAt", s.now().UTC())
	record.Set("failures", 0)
	if err := s.app.Save(record); err != nil {
		s.app.Logger().Debug("Failed to update push subscription", "id", record.Id, "err", err)
	}
}

func (s *Service) recordFailure(record *core.Record) {
	failures := record.GetInt("failures") + 1
	if failures >= MaxFailures {
		_ = s.app.Delete(record)
		return
	}
	record.Set("failures", failures)
	if err := s.app.Save(record); err != nil {
		s.app.Logger().Debug("Failed to update push subscription", "id", record.Id, "err", err)
	}
}

// payload is the JSON the service worker receives.
type payload struct {
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url,omitempty"`
	Tag    string `json:"tag,omitempty"`
	Urgent bool   `json:"urgent,omitempty"`
}

// buildPayload encodes a message, truncating the body so the JSON fits in
// MaxPayloadSize.
func buildPayload(msg Message) ([]byte, error) {
	p := payload{
		Title:  truncate(msg.Title, 200),
		Body:   msg.Body,
		URL:    msg.URL,
		Tag:    truncate(msg.Tag, 100),
		Urgent: msg.Urgent,
	}
	if len(p.URL) > 1024 {
		p.URL = ""
	}
	data, err := json.Marshal(p)
	if err != nil || len(data) <= MaxPayloadSize {
		return data, err
	}
	// binary search the longest body prefix that fits (JSON escaping makes
	// the encoded length of the text unpredictable)
	body := p.Body
	lo, hi := 0, len(body)
	var best []byte
	for lo <= hi {
		mid := (lo + hi) / 2
		p.Body = truncateBytes(body, mid)
		if p.Body != "" {
			p.Body += "…"
		}
		candidate, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		if len(candidate) <= MaxPayloadSize {
			best = candidate
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best == nil {
		return nil, errors.New("push message too large")
	}
	return best, nil
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// truncateBytes shortens s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
