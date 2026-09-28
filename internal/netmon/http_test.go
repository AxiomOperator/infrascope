//go:build testing

package netmon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func probeHTTP(t *testing.T, target string, opts *monitor.HTTPOptions) Outcome {
	t.Helper()
	return newHTTPProber(networkMonitorUserAgent).probe(t.Context(), monitor.Config{Protocol: "http", Target: target, HTTP: opts})
}

func TestHTTPProbeRequestOptions(t *testing.T) {
	type received struct {
		method, body, userAgent, host string
		headers                       http.Header
		user, pass                    string
		basicOK                       bool
	}
	requests := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		requests <- received{r.Method, string(body), r.UserAgent(), r.Host, r.Header, user, pass, ok}
	}))
	defer server.Close()

	t.Run("defaults", func(t *testing.T) {
		out := probeHTTP(t, server.URL, nil)
		require.NoError(t, out.Err)
		req := <-requests
		assert.Equal(t, http.MethodGet, req.method)
		assert.Equal(t, networkMonitorUserAgent, req.userAgent)
		assert.False(t, req.basicOK)
	})

	t.Run("configured", func(t *testing.T) {
		out := probeHTTP(t, server.URL, &monitor.HTTPOptions{
			Method:    http.MethodPost,
			Headers:   [][2]string{{"x-token", "abc"}, {"X-Multi", "1"}, {"X-Multi", "2"}, {"Content-Type", "application/json"}, {"Host", "example.test"}},
			Body:      `{"a":1}`,
			BasicUser: "user", BasicPass: "secret",
		})
		require.NoError(t, out.Err)
		req := <-requests
		assert.Equal(t, http.MethodPost, req.method)
		assert.Equal(t, `{"a":1}`, req.body)
		assert.Equal(t, "abc", req.headers.Get("X-Token"))
		assert.Equal(t, []string{"1", "2"}, req.headers.Values("X-Multi"))
		assert.Equal(t, "application/json", req.headers.Get("Content-Type"))
		assert.Equal(t, "example.test", req.host)
		assert.True(t, req.basicOK)
		assert.Equal(t, "user", req.user)
		assert.Equal(t, "secret", req.pass)
		assert.Equal(t, networkMonitorUserAgent, req.userAgent)
	})

	t.Run("user agent header overrides default", func(t *testing.T) {
		out := probeHTTP(t, server.URL, &monitor.HTTPOptions{Headers: [][2]string{{"User-Agent", "custom/1"}}})
		require.NoError(t, out.Err)
		assert.Equal(t, "custom/1", (<-requests).userAgent)
	})

	t.Run("manager user agent", func(t *testing.T) {
		pm := NewManager(testDefaultIntervalMs, WithUserAgent("Beszel-Hub/test"))
		defer pm.Stop()
		out := pm.probe(t.Context(), monitor.Config{Protocol: "http", Target: server.URL})
		require.NoError(t, out.Err)
		assert.Equal(t, "Beszel-Hub/test", (<-requests).userAgent)
	})
}

func TestHTTPProbeAcceptedCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		w.WriteHeader(code)
	}))
	defer server.Close()
	for _, tc := range []struct {
		code     int
		accepted []string
		ok       bool
	}{
		{200, nil, true},
		{204, nil, true},
		{399, nil, true},
		{400, nil, false},
		{503, nil, false},
		{503, []string{"503"}, true},
		{404, []string{"200-299", "400-404"}, true},
		{405, []string{"200-299", "400-404"}, false},
		{200, []string{"201"}, false},
	} {
		out := probeHTTP(t, server.URL+"?code="+strconv.Itoa(tc.code), &monitor.HTTPOptions{AcceptedCodes: tc.accepted})
		assert.Equal(t, uint16(tc.code), out.StatusCode, "%d %v", tc.code, tc.accepted)
		if tc.ok {
			assert.NoError(t, out.Err, "%d %v", tc.code, tc.accepted)
			assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
		} else {
			assert.EqualError(t, out.Err, "unexpected status "+strconv.Itoa(tc.code), "%d %v", tc.code, tc.accepted)
			assert.Equal(t, int64(-1), out.ResponseUs)
		}
	}
}

func TestHTTPProbeRedirects(t *testing.T) {
	mux := http.NewServeMux()
	// /hop/n redirects n more times before reaching /done.
	mux.HandleFunc("/hop/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		if n == 0 {
			http.Redirect(w, r, "/done", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/hop/"+strconv.Itoa(n-1), http.StatusFound)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "done") })
	server := httptest.NewServer(mux)
	defer server.Close()

	// /hop/0 is one redirect; /hop/n is n+1 redirects.
	out := probeHTTP(t, server.URL+"/hop/0", nil)
	require.NoError(t, out.Err, "redirects are followed by default")
	assert.Equal(t, uint16(http.StatusOK), out.StatusCode)

	out = probeHTTP(t, server.URL+"/hop/9", nil)
	require.NoError(t, out.Err, "the default allows 10 redirects")
	out = probeHTTP(t, server.URL+"/hop/10", nil)
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "stopped after 10 redirects")
	assert.Equal(t, uint16(http.StatusFound), out.StatusCode, "the last response is reported")

	out = probeHTTP(t, server.URL+"/hop/10", &monitor.HTTPOptions{MaxRedirects: 11})
	require.NoError(t, out.Err, "a configured limit allows more redirects")
}

func TestHTTPProbeNoFollowEvaluatesRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	out := probeHTTP(t, server.URL, nil)
	assert.Equal(t, uint16(http.StatusTeapot), out.StatusCode, "the redirect is followed by default")
	assert.EqualError(t, out.Err, "unexpected status 418")

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{MaxRedirects: -1})
	require.NoError(t, out.Err, "the 302 itself is evaluated and accepted")
	assert.Equal(t, uint16(http.StatusFound), out.StatusCode)

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{MaxRedirects: -1, AcceptedCodes: []string{"200"}})
	assert.EqualError(t, out.Err, "unexpected status 302")

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{MaxRedirects: 1, AcceptedCodes: []string{"418"}})
	require.NoError(t, out.Err)
}

func TestHTTPProbeRedirectLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()
	out := probeHTTP(t, server.URL, &monitor.HTTPOptions{MaxRedirects: 2})
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "stopped after 2 redirects")
	assert.Equal(t, int64(-1), out.ResponseUs)
}

func TestHTTPProbeIgnoreTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	prober := newHTTPProber(networkMonitorUserAgent)
	config := monitor.Config{Protocol: "http", Target: server.URL}

	out := prober.probe(t.Context(), config)
	require.Error(t, out.Err, "untrusted certificates fail by default")
	assert.Zero(t, out.StatusCode)

	config.HTTP = &monitor.HTTPOptions{IgnoreTLS: true}
	out = prober.probe(t.Context(), config)
	require.NoError(t, out.Err)
	assert.Equal(t, uint16(http.StatusOK), out.StatusCode)

	config.HTTP = nil
	require.Error(t, prober.probe(t.Context(), config).Err, "ignoring TLS must not leak into other clients")
}

func TestHTTPProbeKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "status: healthy")
	}))
	defer server.Close()

	out := probeHTTP(t, server.URL, &monitor.HTTPOptions{Keyword: "healthy"})
	require.NoError(t, out.Err)
	require.NotNil(t, out.Keyword)
	assert.True(t, *out.Keyword)

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{Keyword: "down"})
	assert.EqualError(t, out.Err, "keyword not found")
	require.NotNil(t, out.Keyword)
	assert.False(t, *out.Keyword)
	assert.Equal(t, uint16(http.StatusOK), out.StatusCode)
	assert.Equal(t, int64(-1), out.ResponseUs)

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{Keyword: "down", KeywordInvert: true})
	require.NoError(t, out.Err)
	assert.False(t, *out.Keyword)

	out = probeHTTP(t, server.URL, &monitor.HTTPOptions{Keyword: "healthy", KeywordInvert: true})
	assert.EqualError(t, out.Err, "keyword found")
	assert.True(t, *out.Keyword)

	out = probeHTTP(t, server.URL, nil)
	assert.Nil(t, out.Keyword)
}

func TestHTTPProbeJSONPath(t *testing.T) {
	const body = `{"status":"ok","data":{"items":[{"count":3,"ratio":1.5,"whole":2.0,"on":true,"none":null}]},"big":9007199254740993}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/invalid":
			_, _ = io.WriteString(w, `{"status":`)
		case "/trailing":
			_, _ = io.WriteString(w, `{} {}`)
		default:
			_, _ = io.WriteString(w, body)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		target, path, expected, err string
	}{
		{"/", "status", "ok", ""},
		{"/", "$.status", "ok", ""},
		{"/", "data.items[0].count", "3", ""},
		{"/", "data.items[0].ratio", "1.5", ""},
		{"/", "data.items[0].whole", "2", ""},
		{"/", "data.items[0].on", "true", ""},
		{"/", "data.items[0].none", "null", ""},
		{"/", "big", "9007199254740993", ""},
		{"/", "data.items[0]", `{"count":3,"none":null,"on":true,"ratio":1.5,"whole":2.0}`, ""},
		{"/", "status", "down", "json value mismatch"},
		{"/", "data.items[0].count", "3.0", "json value mismatch"},
		{"/", "data.items[1]", "3", "json path not found"},
		{"/", "missing", "", "json path not found"},
		{"/", "status.inner", "", "json path not found"},
		{"/", "data[0]", "", "json path not found"},
		{"/invalid", "status", "ok", "invalid json response"},
		{"/trailing", "$", "{}", "invalid json response"},
	} {
		out := probeHTTP(t, server.URL+tc.target, &monitor.HTTPOptions{JSONPath: tc.path, JSONExpected: tc.expected})
		if tc.err == "" {
			assert.NoError(t, out.Err, tc.path)
		} else {
			assert.EqualError(t, out.Err, tc.err, tc.path)
			assert.Equal(t, uint16(http.StatusOK), out.StatusCode)
		}
	}
}

func TestHTTPProbeErrorsNeverIncludeBody(t *testing.T) {
	const secret = "s3cr3t-token-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			http.Error(w, "failure "+secret, http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"token":"`+secret+`","status":"`+secret+`"} `+secret)
	}))
	defer server.Close()
	for _, opts := range []*monitor.HTTPOptions{
		nil,
		{Keyword: "absent"},
		{Keyword: secret, KeywordInvert: true},
		{JSONPath: "token", JSONExpected: "other"},
		{JSONPath: "missing"},
		{JSONPath: "status", JSONExpected: secret[:4]},
	} {
		for _, path := range []string{"/", "/error"} {
			out := probeHTTP(t, server.URL+path, opts)
			if out.Err != nil {
				assert.NotContains(t, out.Err.Error(), secret)
			}
		}
	}
	// A JSON check on a non-JSON body must not echo the body either.
	out := probeHTTP(t, server.URL+"/error", &monitor.HTTPOptions{JSONPath: "a", AcceptedCodes: []string{"503"}})
	assert.EqualError(t, out.Err, "invalid json response")
}

func TestHTTPProbeTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	start := time.Now()
	out := newHTTPProber(networkMonitorUserAgent).probe(context.Background(), monitor.Config{Protocol: "http", Target: server.URL, Timeout: 1})
	elapsed := time.Since(start)
	require.Error(t, out.Err)
	assert.GreaterOrEqual(t, elapsed, time.Second)
	assert.Less(t, elapsed, 3*time.Second)
}

func TestCheckErrStringTruncates(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	got := checkErrString(errString(long))
	assert.LessOrEqual(t, len(got), maxCheckErrLen)
	assert.True(t, strings.HasPrefix(long, got))
	assert.Equal(t, "short", checkErrString(errString("short")))
}

type errString string

func (e errString) Error() string { return string(e) }
