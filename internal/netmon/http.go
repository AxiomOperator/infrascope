package netmon

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

const (
	// maxHTTPBodyRead bounds the body read for keyword and JSON checks.
	maxHTTPBodyRead = 1 << 20
	// maxHTTPBodyDrain is read from unchecked bodies so connections can be reused.
	maxHTTPBodyDrain     = 4 << 10
	defaultHTTPRedirects = 10
)

// Fixed failure messages. Response bodies may contain secrets, so check
// errors never include body content.
var (
	errKeywordNotFound = errors.New("keyword not found")
	errKeywordFound    = errors.New("keyword found")
	errJSONPathMissing = errors.New("json path not found")
	errJSONMismatch    = errors.New("json value mismatch")
	errInvalidJSON     = errors.New("invalid json response")
)

// httpProber performs HTTP checks, sharing clients between monitors with the
// same TLS and redirect options.
type httpProber struct {
	userAgent string
	// clients caches *http.Client by httpClientKey. The probe timeout is applied
	// through the request context, so it is not part of the key.
	clients      sync.Map
	insecureOnce sync.Once
	insecure     *http.Transport
}

type httpClientKey struct {
	ignoreTLS    bool
	maxRedirects int8
}

func newHTTPProber(userAgent string) *httpProber {
	return &httpProber{userAgent: userAgent}
}

// client returns the shared client for the given options. Verified requests
// use http.DefaultTransport; requests ignoring TLS errors share one transport.
func (p *httpProber) client(opts *monitor.HTTPOptions) *http.Client {
	key := httpClientKey{ignoreTLS: opts.IgnoreTLS, maxRedirects: opts.MaxRedirects}
	if client, ok := p.clients.Load(key); ok {
		return client.(*http.Client)
	}
	client := &http.Client{CheckRedirect: checkRedirect(key.maxRedirects)}
	if key.ignoreTLS {
		p.insecureOnce.Do(func() {
			p.insecure = http.DefaultTransport.(*http.Transport).Clone()
			p.insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		})
		client.Transport = p.insecure
	}
	actual, _ := p.clients.LoadOrStore(key, client)
	return actual.(*http.Client)
}

// checkRedirect follows up to maxRedirects redirects (0 means the default of
// 10). With -1 the redirect response itself is evaluated.
func checkRedirect(maxRedirects int8) func(*http.Request, []*http.Request) error {
	if maxRedirects < 0 {
		return func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	limit := int(maxRedirects)
	if limit == 0 {
		limit = defaultHTTPRedirects
	}
	return func(_ *http.Request, via []*http.Request) error {
		if len(via) > limit {
			return fmt.Errorf("stopped after %d redirects", limit)
		}
		return nil
	}
}

// probe sends the configured request and evaluates the response. The response
// time covers the request until response headers arrive.
func (p *httpProber) probe(ctx context.Context, config monitor.Config) Outcome {
	ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
	defer cancel()
	opts := config.HTTP
	if opts == nil {
		opts = &monitor.HTTPOptions{}
	}
	req, err := p.newRequest(ctx, config.Target, opts)
	if err != nil {
		return outcomeOf(-1, err)
	}
	start := time.Now()
	resp, err := p.client(opts).Do(req)
	if err != nil {
		out := outcomeOf(-1, err)
		// A redirect error comes with the last response, whose body is closed.
		if resp != nil {
			out.StatusCode = uint16(resp.StatusCode)
		}
		return out
	}
	defer resp.Body.Close()
	out := Outcome{ResponseUs: time.Since(start).Microseconds(), StatusCode: uint16(resp.StatusCode)}
	if !opts.AcceptsStatus(resp.StatusCode) {
		_, _ = io.CopyN(io.Discard, resp.Body, maxHTTPBodyDrain)
		return out.fail(fmt.Errorf("unexpected status %d", resp.StatusCode))
	}
	if opts.Keyword == "" && opts.JSONPath == "" {
		_, _ = io.CopyN(io.Discard, resp.Body, maxHTTPBodyDrain)
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyRead))
	if err != nil {
		return out.fail(err)
	}
	if opts.Keyword != "" {
		found := bytes.Contains(body, []byte(opts.Keyword))
		out.Keyword = &found
		if found == opts.KeywordInvert {
			if found {
				return out.fail(errKeywordFound)
			}
			return out.fail(errKeywordNotFound)
		}
	}
	if opts.JSONPath != "" {
		if err := checkJSONPath(body, opts.JSONPath, opts.JSONExpected); err != nil {
			return out.fail(err)
		}
	}
	return out
}

func (p *httpProber) newRequest(ctx context.Context, target string, opts *monitor.HTTPOptions) (*http.Request, error) {
	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if opts.Body != "" {
		body = strings.NewReader(opts.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent)
	// Configured headers replace defaults; repeated names add values.
	var configured map[string]bool
	for _, header := range opts.Headers {
		name := http.CanonicalHeaderKey(header[0])
		if name == "Host" {
			req.Host = header[1]
			continue
		}
		if !configured[name] {
			if configured == nil {
				configured = make(map[string]bool, len(opts.Headers))
			}
			configured[name] = true
			req.Header.Del(name)
		}
		req.Header.Add(name, header[1])
	}
	if opts.BasicUser != "" || opts.BasicPass != "" {
		req.SetBasicAuth(opts.BasicUser, opts.BasicPass)
	}
	return req, nil
}

// fail marks the outcome as failed, keeping the status code.
func (out Outcome) fail(err error) Outcome {
	out.ResponseUs = -1
	out.Err = err
	return out
}

// checkJSONPath compares the value at path in a JSON document with expected.
func checkJSONPath(body []byte, path, expected string) error {
	segments, err := monitor.ParseJSONPath(path)
	if err != nil {
		return errors.New("invalid json path")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return errInvalidJSON
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errInvalidJSON
	}
	value, ok := lookupJSONPath(document, segments)
	if !ok {
		return errJSONPathMissing
	}
	if formatJSONValue(value) != expected {
		return errJSONMismatch
	}
	return nil
}

// lookupJSONPath walks a document decoded by encoding/json.
func lookupJSONPath(value any, segments []monitor.JSONPathSegment) (any, bool) {
	for _, segment := range segments {
		if segment.IsIndex {
			array, ok := value.([]any)
			if !ok || segment.Index >= len(array) {
				return nil, false
			}
			value = array[segment.Index]
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if value, ok = object[segment.Key]; !ok {
			return nil, false
		}
	}
	return value, true
}

// formatJSONValue formats a JSON value for comparison: integers without a
// fractional part, bools as true/false, null as "null", strings unquoted and
// objects or arrays as compact JSON.
func formatJSONValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(v)
	case string:
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if f, err := v.Float64(); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return v.String()
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}
