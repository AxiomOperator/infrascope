package hub

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/mailer"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/types"
	"golang.org/x/crypto/ssh"
)

// Status page subscriptions
//
// Visitors of a public status page with allowSubscriptions subscribe with
// their email address (POST /api/beszel/status-pages/{slug}/subscribe). The
// response is always the same, so it reveals nothing about which addresses
// are subscribed. A new or unconfirmed subscriber gets a confirmation email
// whose link, /api/beszel/status-subscriptions/confirm/{token}, shows a page
// that POSTs to the same URL to confirm (link scanners confirm nothing). The
// token is 32 random bytes of which only the SHA-256 is stored; unconfirmed
// subscribers are purged after unconfirmedSubscriberTTL.
//
// Every email has an unsubscribe link, /api/beszel/status-subscriptions/
// unsubscribe/{token}, with the same GET page / POST pattern and RFC 8058
// one-click headers. Its token is "<subscriberID>.<signature>", the signature
// being the HMAC-SHA256 of the id with a key derived with HKDF-SHA256 from
// the hub key (like alert acknowledgement links), so it needs no stored secret.
//
// Confirmed subscribers are emailed when an incident listing the page is
// created, updated or changes status, and, on pages without autoIncidents
// (which would open incidents for the same outages), when a component of the
// page goes down or recovers. Emails are queued and sent by a worker at most
// every subscriptionSendInterval; a component sends at most one down/recovered
// pair per componentNoticeWindow. Nothing is sent without SMTP and an app URL.
const (
	statusSubscribersCollection = "status_subscribers"

	subscribeIPLimit     = 5
	subscribeIPWindow    = time.Hour
	subscribePageLimit   = 100
	subscribePageWindow  = time.Hour
	subscribeEmailLimit  = 3
	subscribeEmailWindow = 24 * time.Hour

	maxSubscriberEmailLength = 254
	unconfirmedSubscriberTTL = 48 * time.Hour

	// subscriptionSendInterval throttles sending to 10 emails per second.
	subscriptionSendInterval = 100 * time.Millisecond
	// componentNoticeWindow is the minimum time between two down emails of a component.
	componentNoticeWindow = 15 * time.Minute
	// incidentNoticeDelay coalesces the changes of an incident (such as an
	// update and the status change it makes) into one email.
	incidentNoticeDelay = 5 * time.Second
	// maxSubscriptionOutbox bounds the queued emails.
	maxSubscriptionOutbox = 100_000

	subscriptionHKDFInfo = "infrascope/status-subscriptions/v1"

	subscribeAcceptedMessage = "If this address can be subscribed, a confirmation email is on its way."
)

// maxSubscribersPerPage caps the subscribers of a page, confirmed or not (a
// variable for tests).
var maxSubscribersPerPage = 10_000

// statusSubscriptions rate limits subscriptions and queues and sends the
// emails of status page subscribers.
type statusSubscriptions struct {
	hub          *Hub
	ipLimits     *windowLimiter
	pageLimits   *windowLimiter
	emailLimits  *windowLimiter
	now          func() time.Time
	sendInterval time.Duration

	keyMu  sync.Mutex
	secret []byte

	mu         sync.Mutex
	outbox     []*subscriptionBatch
	queued     int
	incidents  map[string]*incidentNotice
	components map[string]*componentNotice
	mailWarned bool
	stop       chan struct{}
}

// subscriptionBatch is one email to several subscribers of a page. Each
// recipient gets a copy with its own unsubscribe link.
type subscriptionBatch struct {
	pageTitle  string
	subject    string
	text       string
	recipients []string // subscriber ids
	// confirmation emails also go to unconfirmed subscribers.
	confirmation bool
}

// incidentNotice is a pending notification of an incident.
type incidentNotice struct {
	due time.Time
	// updateID is the latest update created since the notice was queued.
	updateID string
}

// componentNotice is the notification state of a monitor or system.
type componentNotice struct {
	component incidentComponent
	// current is the latest status; dirty while not yet handled.
	current string
	dirty   bool
	// sent is the last status emailed and sentDownAt when down was.
	sent       string
	sentDownAt time.Time
}

func newStatusSubscriptions(hub *Hub) *statusSubscriptions {
	return &statusSubscriptions{
		hub:          hub,
		ipLimits:     newWindowLimiter(subscribeIPLimit, subscribeIPWindow),
		pageLimits:   newWindowLimiter(subscribePageLimit, subscribePageWindow),
		emailLimits:  newWindowLimiter(subscribeEmailLimit, subscribeEmailWindow),
		now:          time.Now,
		sendInterval: subscriptionSendInterval,
		incidents:    map[string]*incidentNotice{},
		components:   map[string]*componentNotice{},
	}
}

// subscriptionMailReady reports whether subscription emails can be sent: SMTP
// is enabled with a host, and the app URL and sender address are set.
func subscriptionMailReady(app core.App) bool {
	settings := app.Settings()
	return settings.SMTP.Enabled && strings.TrimSpace(settings.SMTP.Host) != "" &&
		strings.TrimSpace(settings.Meta.AppURL) != "" && strings.TrimSpace(settings.Meta.SenderAddress) != ""
}

// mailReady is subscriptionMailReady, logging once when it is not.
func (s *statusSubscriptions) mailReady() bool {
	if subscriptionMailReady(s.hub) {
		return true
	}
	s.mu.Lock()
	warned := s.mailWarned
	s.mailWarned = true
	s.mu.Unlock()
	if !warned {
		s.hub.Logger().Warn("Status page subscription emails are not sent: configure SMTP, the sender address and the app URL")
	}
	return false
}

// subscribablePage reports whether a page's subscribers get emails.
func subscribablePage(page *core.Record) bool {
	return page.GetBool("public") && page.GetBool("allowSubscriptions")
}

// bindStatusSubscriptions registers the hooks that notify subscribers, and
// starts the sender and the purge of unconfirmed subscribers with the server.
func bindStatusSubscriptions(h *Hub) {
	bindStatusSubscriptionHooks(h)
	h.OnServe().BindFunc(func(e *core.ServeEvent) error {
		h.statusSubscriptions.start()
		e.App.Cron().MustAdd("purge unconfirmed status subscribers", "23 * * * *", func() {
			if err := purgeUnconfirmedSubscribers(e.App, time.Now()); err != nil {
				e.App.Logger().Error("Failed to purge unconfirmed status subscribers", "err", err)
			}
		})
		return e.Next()
	})
	h.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		h.statusSubscriptions.shutdown()
		return e.Next()
	})
}

// bindStatusSubscriptionHooks queues notifications of incident changes and of
// system status changes.
func bindStatusSubscriptionHooks(h *Hub) {
	subs := h.statusSubscriptions
	h.OnRecordAfterCreateSuccess(incidentsCollection).BindFunc(func(e *core.RecordEvent) error {
		subs.incidentChanged(e.Record.Id, "")
		return e.Next()
	})
	h.OnRecordAfterUpdateSuccess(incidentsCollection).BindFunc(func(e *core.RecordEvent) error {
		if original := e.Record.Original(); original != nil && original.GetString("status") != e.Record.GetString("status") {
			subs.incidentChanged(e.Record.Id, "")
		}
		return e.Next()
	})
	h.OnRecordAfterCreateSuccess(incidentUpdatesCollection).BindFunc(func(e *core.RecordEvent) error {
		subs.incidentChanged(e.Record.GetString("incident"), e.Record.Id)
		return e.Next()
	})
	h.OnRecordAfterUpdateSuccess("systems").BindFunc(func(e *core.RecordEvent) error {
		original := e.Record.Original()
		status := e.Record.GetString("status")
		if original == nil || original.GetString("status") == status {
			return e.Next()
		}
		// Systems behind a down parent do not count as down (see bindSystemIncidentEvents).
		if status == uptime.StatusDown && h.systemSuppressedBy(e.Record.GetStringSlice("dependsOn")) != "" {
			return e.Next()
		}
		subs.componentChanged(incidentComponent{field: "systems", id: e.Record.Id}, status)
		return e.Next()
	})
}

// monitorTransitions queues notifications of the confirmed status changes of monitors.
func (s *statusSubscriptions) monitorTransitions(transitions []uptime.Transition) {
	for _, transition := range transitions {
		s.componentChanged(incidentComponent{field: "monitors", id: transition.MonitorID}, transition.Status)
	}
}

// incidentChanged queues a notification of an incident, coalescing changes
// within incidentNoticeDelay. updateID is the update that was created, if any.
func (s *statusSubscriptions) incidentChanged(incidentID, updateID string) {
	if incidentID == "" || !subscriptionMailReady(s.hub) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notice := s.incidents[incidentID]
	if notice == nil {
		notice = &incidentNotice{due: s.now().Add(incidentNoticeDelay)}
		s.incidents[incidentID] = notice
	}
	if updateID != "" {
		notice.updateID = updateID
	}
}

// componentChanged records the status of a monitor or system; only "up" and
// "down" are notified.
func (s *statusSubscriptions) componentChanged(component incidentComponent, status string) {
	if status != uptime.StatusUp && status != uptime.StatusDown {
		return
	}
	if !subscriptionMailReady(s.hub) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notice := s.components[component.key()]
	if notice == nil {
		if status == uptime.StatusUp {
			// nothing was sent, so there is nothing to recover from
			return
		}
		notice = &componentNotice{component: component}
		s.components[component.key()] = notice
	}
	notice.current = status
	notice.dirty = true
}

// start runs the sender until shutdown. Only the first call starts it.
func (s *statusSubscriptions) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	s.stop = make(chan struct{})
	go s.run(s.stop)
}

func (s *statusSubscriptions) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
	}
	s.ipLimits.stop()
	s.pageLimits.stop()
	s.emailLimits.stop()
}

func (s *statusSubscriptions) run(stop <-chan struct{}) {
	ticker := time.NewTicker(s.sendInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.flush(s.now())
			s.sendNext()
		}
	}
}

// drain queues the notifications due at now and sends every queued email
// without throttling (tests).
func (s *statusSubscriptions) drain(now time.Time) {
	s.flush(now)
	for s.sendNext() {
	}
}

// flush turns the notifications due at now into queued emails.
func (s *statusSubscriptions) flush(now time.Time) {
	type dueIncident struct{ id, updateID string }
	var incidents []dueIncident
	var components []componentNotice

	s.mu.Lock()
	for id, notice := range s.incidents {
		if !now.Before(notice.due) {
			incidents = append(incidents, dueIncident{id, notice.updateID})
			delete(s.incidents, id)
		}
	}
	for key, notice := range s.components {
		if notice.dirty {
			switch {
			case notice.current == uptime.StatusDown && notice.sent != uptime.StatusDown:
				// at most one down email (and so one down/up pair) per window
				if !notice.sentDownAt.IsZero() && now.Sub(notice.sentDownAt) < componentNoticeWindow {
					continue
				}
				notice.sent, notice.sentDownAt = uptime.StatusDown, now
				components = append(components, *notice)
			case notice.current == uptime.StatusUp && notice.sent == uptime.StatusDown:
				notice.sent = uptime.StatusUp
				components = append(components, *notice)
			}
			notice.dirty = false
		}
		if notice.sent != uptime.StatusDown && now.Sub(notice.sentDownAt) >= componentNoticeWindow {
			delete(s.components, key)
		}
	}
	s.mu.Unlock()

	if len(incidents) == 0 && len(components) == 0 {
		return
	}
	if !s.mailReady() {
		return
	}
	for _, item := range incidents {
		batches, err := s.incidentBatches(item.id, item.updateID)
		if err != nil {
			s.hub.Logger().Error("Failed to notify status page subscribers of an incident", "incident", item.id, "err", err)
		}
		s.enqueue(batches...)
	}
	for _, notice := range components {
		batches, err := s.componentBatches(notice.component, notice.sent)
		if err != nil {
			s.hub.Logger().Error("Failed to notify status page subscribers of a status change", "component", notice.component.key(), "err", err)
		}
		s.enqueue(batches...)
	}
}

// enqueue appends batches to the outbox; confirmation emails go first.
func (s *statusSubscriptions) enqueue(batches ...*subscriptionBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, batch := range batches {
		if len(batch.recipients) == 0 {
			continue
		}
		if s.queued+len(batch.recipients) > maxSubscriptionOutbox {
			s.hub.Logger().Warn("Status page subscription email queue is full, dropping an email", "subject", batch.subject, "recipients", len(batch.recipients))
			continue
		}
		s.queued += len(batch.recipients)
		if batch.confirmation {
			s.outbox = slices.Insert(s.outbox, 0, batch)
		} else {
			s.outbox = append(s.outbox, batch)
		}
	}
}

// sendNext sends the next queued email and reports whether one was queued.
func (s *statusSubscriptions) sendNext() bool {
	s.mu.Lock()
	if len(s.outbox) == 0 {
		s.mu.Unlock()
		return false
	}
	batch := s.outbox[0]
	id := batch.recipients[0]
	batch.recipients = batch.recipients[1:]
	if len(batch.recipients) == 0 {
		s.outbox = s.outbox[1:]
	}
	s.queued--
	s.mu.Unlock()

	if err := s.send(batch, id); err != nil {
		s.hub.Logger().Error("Failed to send status page subscription email", "subscriber", id, "err", err)
	}
	return true
}

// send emails a batch to a subscriber that still exists (and is confirmed,
// unless it is a confirmation email).
func (s *statusSubscriptions) send(batch *subscriptionBatch, subscriberID string) error {
	subscriber, err := s.hub.FindRecordById(statusSubscribersCollection, subscriberID)
	if err != nil || (!batch.confirmation && !subscriber.GetBool("confirmed")) {
		return nil
	}
	unsubscribe, err := s.unsubscribeLink(subscriberID)
	if err != nil {
		return err
	}
	settings := s.hub.Settings()
	text := batch.text + "\n\n--\nYou receive this email because you subscribed to updates of " + batch.pageTitle +
		".\nUnsubscribe: " + unsubscribe + "\n"
	message := &mailer.Message{
		From:    mail.Address{Address: settings.Meta.SenderAddress, Name: settings.Meta.SenderName},
		To:      []mail.Address{{Address: subscriber.GetString("email")}},
		Subject: batch.subject,
		Text:    text,
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsubscribe + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
			"Auto-Submitted":        "auto-generated",
		},
	}
	if err := s.hub.NewMailClient().Send(message); err != nil {
		return err
	}
	_, err = s.hub.DB().Update(statusSubscribersCollection,
		dbx.Params{"lastSentAt": types.NowDateTime().String()}, dbx.HashExp{"id": subscriberID}).Execute()
	return err
}

// mailSubject returns a single-line subject prefixed with the page title.
func mailSubject(pageTitle, subject string) string {
	return oneLine("[" + pageTitle + "] " + subject)
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// confirmedSubscribers returns the ids of the confirmed subscribers of a page.
func confirmedSubscribers(app core.App, pageID string) ([]string, error) {
	var ids []string
	err := app.DB().Select("id").From(statusSubscribersCollection).
		Where(dbx.HashExp{"page": pageID, "confirmed": true}).OrderBy("rowid").Column(&ids)
	return ids, err
}

var incidentStatusLabels = map[string]string{
	"investigating":  "Investigating",
	"identified":     "Identified",
	"monitoring":     "Monitoring",
	incidentResolved: "Resolved",
}

func incidentStatusLabel(status string) string {
	if label, ok := incidentStatusLabels[status]; ok {
		return label
	}
	return status
}

// incidentBatches returns the emails of an incident to the subscribers of
// the subscribable pages it lists, with the message of updateID, if any.
func (s *statusSubscriptions) incidentBatches(incidentID, updateID string) ([]*subscriptionBatch, error) {
	incident, err := s.hub.FindRecordById(incidentsCollection, incidentID)
	if err != nil {
		return nil, nil
	}
	var message string
	if updateID != "" {
		if update, err := s.hub.FindRecordById(incidentUpdatesCollection, updateID); err == nil && update.GetString("incident") == incidentID {
			message = update.GetString("message")
		}
	}
	pageIDs := incident.GetStringSlice("statusPages")
	if len(pageIDs) == 0 {
		return nil, nil
	}
	pages, err := s.hub.FindRecordsByIds("status_pages", pageIDs)
	if err != nil {
		return nil, err
	}
	status := incidentStatusLabel(incident.GetString("status"))
	title := incident.GetString("title")
	var batches []*subscriptionBatch
	for _, page := range pages {
		if !subscribablePage(page) || page.GetString("user") != incident.GetString("user") {
			continue
		}
		recipients, err := confirmedSubscribers(s.hub, page.Id)
		if err != nil {
			return batches, err
		}
		text := title + "\nStatus: " + status
		if message != "" {
			text += "\n\n" + message
		}
		text += "\n\nView the status page: " + s.hub.MakeLink("status", page.GetString("slug"))
		batches = append(batches, &subscriptionBatch{
			pageTitle:  page.GetString("title"),
			subject:    mailSubject(page.GetString("title"), title+": "+status),
			text:       text,
			recipients: recipients,
		})
	}
	return batches, nil
}

// componentBatches returns the emails of a component that went down or
// recovered to the subscribers of the subscribable pages without
// autoIncidents that show it, named as each page shows it.
func (s *statusSubscriptions) componentBatches(component incidentComponent, status string) ([]*subscriptionBatch, error) {
	pages, err := s.hub.FindAllRecords("status_pages", dbx.HashExp{"allowSubscriptions": true, "public": true, "autoIncidents": false})
	if err != nil {
		return nil, err
	}
	var batches []*subscriptionBatch
	for _, page := range pages {
		if !slices.Contains(page.GetStringSlice(component.field), component.id) {
			continue
		}
		records, err := viewableByOwner(s.hub, page, component.collection(), component.field)
		if err != nil {
			return batches, err
		}
		index := slices.IndexFunc(records, func(r *core.Record) bool { return r.Id == component.id })
		if index < 0 {
			continue
		}
		recipients, err := confirmedSubscribers(s.hub, page.Id)
		if err != nil {
			return batches, err
		}
		name := publicComponentName(page, component, records[index], index)
		summary := name + " is down"
		body := name + " is down. We will email you when it recovers."
		if status == uptime.StatusUp {
			summary = name + " has recovered"
			body = name + " has recovered."
		}
		batches = append(batches, &subscriptionBatch{
			pageTitle:  page.GetString("title"),
			subject:    mailSubject(page.GetString("title"), summary),
			text:       body + "\n\nView the status page: " + s.hub.MakeLink("status", page.GetString("slug")),
			recipients: recipients,
		})
	}
	return batches, nil
}

// purgeUnconfirmedSubscribers deletes the subscribers that did not confirm
// within unconfirmedSubscriberTTL of subscribing.
func purgeUnconfirmedSubscribers(app core.App, now time.Time) error {
	cutoff, err := types.ParseDateTime(now.Add(-unconfirmedSubscriberTTL))
	if err != nil {
		return err
	}
	_, err = app.DB().Delete(statusSubscribersCollection,
		dbx.NewExp("confirmed = FALSE AND createdAt < {:cutoff}", dbx.Params{"cutoff": cutoff.String()})).Execute()
	return err
}

// Tokens

// newSubscriptionToken returns a random confirmation token and its hash.
func newSubscriptionToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashSubscriptionToken(token), nil
}

// hashSubscriptionToken returns the stored form of a confirmation token.
func hashSubscriptionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// key returns the key of unsubscribe links, derived from the hub key.
func (s *statusSubscriptions) key() ([]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.secret != nil {
		return s.secret, nil
	}
	// creates the key when missing
	if _, err := s.hub.GetSSHKey(""); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.hub.DataDir(), "id_ed25519"))
	if err != nil {
		return nil, err
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, err
	}
	var seed []byte
	switch key := raw.(type) {
	case ed25519.PrivateKey:
		seed = key.Seed()
	case *ed25519.PrivateKey:
		seed = key.Seed()
	default:
		return nil, fmt.Errorf("unsupported hub key type %T", raw)
	}
	key, err := hkdf.Key(sha256.New, seed, nil, subscriptionHKDFInfo, 32)
	if err != nil {
		return nil, err
	}
	s.secret = key
	return key, nil
}

func unsubscribeSignature(key []byte, subscriberID string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("unsubscribe:" + subscriberID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// unsubscribeToken returns the unsubscribe token of a subscriber.
func (s *statusSubscriptions) unsubscribeToken(subscriberID string) (string, error) {
	key, err := s.key()
	if err != nil {
		return "", err
	}
	return subscriberID + "." + unsubscribeSignature(key, subscriberID), nil
}

func (s *statusSubscriptions) unsubscribeLink(subscriberID string) (string, error) {
	token, err := s.unsubscribeToken(subscriberID)
	if err != nil {
		return "", err
	}
	return s.hub.MakeLink("api", "beszel", "status-subscriptions", "unsubscribe", token), nil
}

// verifyUnsubscribeToken returns the subscriber id of a valid token.
func (s *statusSubscriptions) verifyUnsubscribeToken(token string) (string, bool) {
	if len(token) > 128 {
		return "", false
	}
	id, signature, ok := strings.Cut(token, ".")
	if !ok || id == "" {
		return "", false
	}
	key, err := s.key()
	if err != nil {
		return "", false
	}
	return id, hmac.Equal([]byte(signature), []byte(unsubscribeSignature(key, id)))
}

// Handlers

// registerStatusSubscriptionRoutes registers the subscription endpoints.
func (h *Hub) registerStatusSubscriptionRoutes(apiAuth, apiNoAuth *router.RouterGroup[*core.RequestEvent]) {
	apiNoAuth.POST("/status-pages/{slug}/subscribe", h.handleStatusSubscribe)
	apiNoAuth.GET("/status-subscriptions/confirm/{token}", h.handleSubscriptionConfirm)
	apiNoAuth.POST("/status-subscriptions/confirm/{token}", h.handleSubscriptionConfirm)
	apiNoAuth.GET("/status-subscriptions/unsubscribe/{token}", h.handleSubscriptionUnsubscribe)
	apiNoAuth.POST("/status-subscriptions/unsubscribe/{token}", h.handleSubscriptionUnsubscribe)
	apiAuth.GET("/status-pages/{id}/subscribers", h.handleListSubscribers)
	apiAuth.DELETE("/status-pages/{id}/subscribers/{subscriber}", h.handleDeleteSubscriber).BindFunc(excludeReadOnlyRole)
}

func tooManyRequests(e *core.RequestEvent, retryAfter time.Duration) error {
	seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
	e.Response.Header().Set("Retry-After", strconv.Itoa(seconds))
	return e.TooManyRequestsError("Too many requests.", nil)
}

// normalizeSubscriberEmail returns the lowercase address of a plain email
// address, or false.
func normalizeSubscriberEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > maxSubscriberEmailLength {
		return "", false
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || address.Name != "" || !strings.Contains(value[strings.LastIndex(value, "@")+1:], ".") {
		return "", false
	}
	return value, true
}

// handleStatusSubscribe handles POST /api/beszel/status-pages/{slug}/subscribe
// with {"email": "..."}. Pages that are not public do not exist; otherwise
// the response is the same 202 whatever happens to the address. "website" is
// a honeypot field that people leave empty.
func (h *Hub) handleStatusSubscribe(e *core.RequestEvent) error {
	subs := h.statusSubscriptions
	if ok, retryAfter := subs.ipLimits.allow(e.RealIP()); !ok {
		return tooManyRequests(e, retryAfter)
	}
	page, err := e.App.FindFirstRecordByData("status_pages", "slug", e.Request.PathValue("slug"))
	if err != nil || !page.GetBool("public") {
		return statusPageNotFound(e)
	}
	var body struct {
		Email   string `json:"email" form:"email"`
		Website string `json:"website" form:"website"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("Invalid request body.", nil)
	}
	email, valid := normalizeSubscriberEmail(body.Email)
	if !valid {
		return e.BadRequestError("Enter a valid email address.", nil)
	}
	if ok, retryAfter := subs.pageLimits.allow(page.Id); !ok {
		return tooManyRequests(e, retryAfter)
	}
	accepted := func() error {
		return e.JSON(http.StatusAccepted, map[string]string{"message": subscribeAcceptedMessage})
	}
	if body.Website != "" || !page.GetBool("allowSubscriptions") || !subs.mailReady() {
		return accepted()
	}
	if err := subs.subscribe(e.App, page, email); err != nil {
		e.App.Logger().Error("Failed to subscribe to a status page", "page", page.Id, "err", err)
	}
	return accepted()
}

// subscribe creates or refreshes an unconfirmed subscriber of a page and
// queues its confirmation email. Confirmed subscribers are left alone, as are
// addresses that got subscribeEmailLimit confirmation emails in the window
// and new addresses of full pages.
func (s *statusSubscriptions) subscribe(app core.App, page *core.Record, email string) error {
	limitKey := page.Id + "|" + email
	if s.emailLimits.blocked(limitKey) {
		return nil
	}
	token, hash, err := newSubscriptionToken()
	if err != nil {
		return err
	}
	var subscriber *core.Record
	err = app.RunInTransaction(func(tx core.App) error {
		existing, err := tx.FindFirstRecordByFilter(statusSubscribersCollection, "page = {:page} && email = {:email}",
			dbx.Params{"page": page.Id, "email": email})
		if err == nil {
			if existing.GetBool("confirmed") {
				return nil
			}
			subscriber = existing
		} else {
			count, err := tx.CountRecords(statusSubscribersCollection, dbx.HashExp{"page": page.Id})
			if err != nil || count >= int64(maxSubscribersPerPage) {
				return err
			}
			collection, err := tx.FindCachedCollectionByNameOrId(statusSubscribersCollection)
			if err != nil {
				return err
			}
			subscriber = core.NewRecord(collection)
			subscriber.Set("page", page.Id)
			subscriber.Set("email", email)
		}
		subscriber.Set("tokenHash", hash)
		subscriber.Set("createdAt", s.now().UTC())
		return tx.Save(subscriber)
	})
	if err != nil || subscriber == nil {
		return err
	}
	s.emailLimits.add(limitKey)
	title := page.GetString("title")
	link := s.hub.MakeLink("api", "beszel", "status-subscriptions", "confirm", token)
	s.enqueue(&subscriptionBatch{
		pageTitle: title,
		subject:   mailSubject(title, "Confirm your subscription"),
		text: "Someone, hopefully you, asked to receive status updates of " + title + " at this address.\n\n" +
			"Confirm the subscription: " + link + "\n\n" +
			"The link expires in 48 hours. If you did not ask for this, ignore this email and you will not be subscribed.",
		recipients:   []string{subscriber.Id},
		confirmation: true,
	})
	return nil
}

// subscriptionPage is the HTML page of confirm and unsubscribe links. The
// arguments are HTML-escaped by renderSubscriptionPage.
const subscriptionPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer"><meta name="robots" content="noindex">
<title>%[1]s</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5">
<h1 style="font-size:1.25rem">%[1]s</h1>
<p>%[2]s</p>
%[3]s
</body></html>`

// renderSubscriptionPage writes a page with a heading, a text and either a
// button that POSTs to the same URL or a link.
func renderSubscriptionPage(e *core.RequestEvent, status int, heading, text, button, linkURL, linkText string) error {
	header := e.Response.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	var action string
	switch {
	case button != "":
		action = `<form method="post"><button type="submit" style="font:inherit;padding:.5rem 1rem;cursor:pointer">` +
			html.EscapeString(button) + `</button></form>`
	case linkURL != "":
		action = `<p><a href="` + html.EscapeString(linkURL) + `">` + html.EscapeString(linkText) + `</a></p>`
	}
	return e.HTML(status, fmt.Sprintf(subscriptionPage, html.EscapeString(heading), html.EscapeString(text), action))
}

func (h *Hub) statusPageLink(page *core.Record) string {
	link := h.MakeLink("status", page.GetString("slug"))
	if !strings.HasPrefix(link, "http") {
		return ""
	}
	return link
}

// handleSubscriptionConfirm handles /api/beszel/status-subscriptions/confirm/{token}.
// GET only shows a page whose button POSTs to the same URL, which confirms
// the subscription. Unknown, used and expired tokens get a 400 page.
func (h *Hub) handleSubscriptionConfirm(e *core.RequestEvent) error {
	invalid := func() error {
		return renderSubscriptionPage(e, http.StatusBadRequest, "Invalid link",
			"This confirmation link is invalid, was already used or has expired. Subscribe again on the status page.", "", "", "")
	}
	token := e.Request.PathValue("token")
	if token == "" || len(token) > 128 {
		return invalid()
	}
	now := h.statusSubscriptions.now()
	subscriber, err := e.App.FindFirstRecordByData(statusSubscribersCollection, "tokenHash", hashSubscriptionToken(token))
	if err != nil || subscriber.GetBool("confirmed") ||
		now.Sub(subscriber.GetDateTime("createdAt").Time()) >= unconfirmedSubscriberTTL {
		return invalid()
	}
	page, err := e.App.FindRecordById("status_pages", subscriber.GetString("page"))
	if err != nil || !subscribablePage(page) {
		return invalid()
	}
	title := page.GetString("title")
	if e.Request.Method != http.MethodPost {
		return renderSubscriptionPage(e, http.StatusOK, "Confirm subscription",
			"Receive email updates about incidents and outages of "+title+" at "+subscriber.GetString("email")+".",
			"Confirm subscription", "", "")
	}
	subscriber.Set("confirmed", true)
	subscriber.Set("confirmedAt", now.UTC())
	subscriber.Set("tokenHash", "")
	if err := e.App.Save(subscriber); err != nil {
		return e.InternalServerError("", err)
	}
	return renderSubscriptionPage(e, http.StatusOK, "Subscription confirmed",
		"You will receive email updates of "+title+". Every email has a link to unsubscribe.",
		"", h.statusPageLink(page), "View the status page")
}

// handleSubscriptionUnsubscribe handles /api/beszel/status-subscriptions/unsubscribe/{token}.
// GET only shows a page whose button POSTs to the same URL (as one-click
// unsubscribe requests of mail clients do, RFC 8058), which deletes the
// subscriber. Unsubscribing again succeeds; invalid tokens get a 400 page.
func (h *Hub) handleSubscriptionUnsubscribe(e *core.RequestEvent) error {
	id, ok := h.statusSubscriptions.verifyUnsubscribeToken(e.Request.PathValue("token"))
	if !ok {
		return renderSubscriptionPage(e, http.StatusBadRequest, "Invalid link", "This unsubscribe link is invalid.", "", "", "")
	}
	done := func() error {
		return renderSubscriptionPage(e, http.StatusOK, "Unsubscribed", "You will not receive further emails from this status page.", "", "", "")
	}
	subscriber, err := e.App.FindRecordById(statusSubscribersCollection, id)
	if err != nil {
		return done()
	}
	if e.Request.Method != http.MethodPost {
		title := ""
		if page, err := e.App.FindRecordById("status_pages", subscriber.GetString("page")); err == nil {
			title = page.GetString("title")
		}
		return renderSubscriptionPage(e, http.StatusOK, "Unsubscribe",
			"Stop email updates of "+title+" to "+subscriber.GetString("email")+".", "Unsubscribe", "", "")
	}
	if err := e.App.Delete(subscriber); err != nil {
		return e.InternalServerError("", err)
	}
	return done()
}

// ownedStatusPage returns the status page {id} of the requesting user.
func ownedStatusPage(e *core.RequestEvent) (*core.Record, error) {
	if e.Auth == nil || e.Auth.Collection().Name != "users" {
		return nil, e.ForbiddenError("Only the page owner can manage subscribers.", nil)
	}
	page, err := e.App.FindRecordById("status_pages", e.Request.PathValue("id"))
	if err != nil || page.GetString("user") != e.Auth.Id {
		return nil, statusPageNotFound(e)
	}
	return page, nil
}

// statusSubscriber is a subscriber in the owner's list.
type statusSubscriber struct {
	ID          string `json:"id" db:"id"`
	Email       string `json:"email" db:"email"`
	Confirmed   bool   `json:"confirmed" db:"confirmed"`
	CreatedAt   string `json:"createdAt" db:"createdAt"`
	ConfirmedAt string `json:"confirmedAt" db:"confirmedAt"`
	LastSentAt  string `json:"lastSentAt" db:"lastSentAt"`
}

const subscribersPerPage = 200

// handleListSubscribers handles GET /api/beszel/status-pages/{id}/subscribers?page=N
// for the page owner: counts and a page of subscribers, newest first.
func (h *Hub) handleListSubscribers(e *core.RequestEvent) error {
	page, err := ownedStatusPage(e)
	if err != nil {
		return err
	}
	number, _ := strconv.Atoi(e.Request.URL.Query().Get("page"))
	number = max(1, number)
	var counts struct {
		Total     int `db:"total"`
		Confirmed int `db:"confirmed"`
	}
	err = e.App.DB().Select("COUNT(*) AS total", "COALESCE(SUM(confirmed), 0) AS confirmed").
		From(statusSubscribersCollection).Where(dbx.HashExp{"page": page.Id}).One(&counts)
	if err != nil {
		return e.InternalServerError("", err)
	}
	items := []statusSubscriber{}
	err = e.App.DB().Select("id", "email", "confirmed", "createdAt", "confirmedAt", "lastSentAt").
		From(statusSubscribersCollection).Where(dbx.HashExp{"page": page.Id}).
		OrderBy("createdAt DESC", "rowid DESC").Offset(int64((number - 1) * subscribersPerPage)).Limit(subscribersPerPage).
		All(&items)
	if err != nil {
		return e.InternalServerError("", err)
	}
	return e.JSON(http.StatusOK, map[string]any{
		"total":     counts.Total,
		"confirmed": counts.Confirmed,
		"page":      number,
		"perPage":   subscribersPerPage,
		"items":     items,
	})
}

// handleDeleteSubscriber handles DELETE /api/beszel/status-pages/{id}/subscribers/{subscriber}
// for the page owner.
func (h *Hub) handleDeleteSubscriber(e *core.RequestEvent) error {
	page, err := ownedStatusPage(e)
	if err != nil {
		return err
	}
	subscriber, err := e.App.FindRecordById(statusSubscribersCollection, e.Request.PathValue("subscriber"))
	if err != nil || subscriber.GetString("page") != page.Id {
		return e.NotFoundError("Subscriber not found.", nil)
	}
	if err := e.App.Delete(subscriber); err != nil {
		return e.InternalServerError("", err)
	}
	return e.NoContent(http.StatusNoContent)
}
