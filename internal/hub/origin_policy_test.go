//go:build testing

package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOriginPolicyOnlyWithImplicitAuth(t *testing.T) {
	t.Setenv("AUTO_LOGIN", "")
	t.Setenv("TRUSTED_AUTH_HEADER", "")
	assert.Nil(t, newOriginPolicy("https://hub.example.com"), "PocketBase defaults apply without implicit auth")

	t.Setenv("AUTO_LOGIN", "user@example.com")
	assert.NotNil(t, newOriginPolicy(""))
	t.Setenv("AUTO_LOGIN", "")
	t.Setenv("TRUSTED_AUTH_HEADER", "X-Email")
	assert.NotNil(t, newOriginPolicy(""))
}

func TestURLOrigin(t *testing.T) {
	assert.Equal(t, "https://hub.example.com", urlOrigin("https://Hub.example.com/beszel/"))
	assert.Equal(t, "http://10.0.0.2:8090", urlOrigin("http://10.0.0.2:8090"))
	assert.Empty(t, urlOrigin(""))
	assert.Empty(t, urlOrigin("hub.example.com"))
}

// runPolicy runs the CORS and origin check handlers like the router would and
// reports whether the request reached the handler.
func runPolicy(t *testing.T, policy *originPolicy, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{}
	e.Request = req
	e.Response = rec
	if err := policy.corsHandler().Func(e); err != nil {
		t.Fatal(err)
	}
	if req.Method == http.MethodOptions {
		return rec, false
	}
	err := policy.checkOrigin(e)
	return rec, err == nil
}

func TestOriginPolicyCORS(t *testing.T) {
	t.Setenv("AUTO_LOGIN", "user@example.com")
	policy := newOriginPolicy("https://hub.example.com/")
	require.NotNil(t, policy)

	req := httptest.NewRequest(http.MethodGet, "http://hub.example.com/api/collections/systems/records", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec, _ := runPolicy(t, policy, req)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "other origins must not read responses")

	req = httptest.NewRequest(http.MethodGet, "http://hub.example.com/api/collections/systems/records", nil)
	req.Header.Set("Origin", "https://hub.example.com")
	rec, _ = runPolicy(t, policy, req)
	assert.Equal(t, "https://hub.example.com", rec.Header().Get("Access-Control-Allow-Origin"))

	// Without APP_URL no cross-origin reads are allowed (not "*").
	noURL := newOriginPolicy("")
	req = httptest.NewRequest(http.MethodGet, "http://hub.example.com/api/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec, _ = runPolicy(t, noURL, req)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestOriginPolicyCSRF(t *testing.T) {
	t.Setenv("AUTO_LOGIN", "user@example.com")
	policy := newOriginPolicy("https://hub.example.com")
	require.NotNil(t, policy)

	testCases := []struct {
		name    string
		method  string
		headers map[string]string
		allowed bool
	}{
		{"cross-site post", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"cross-origin post without fetch metadata", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, false},
		{"cross-site delete", http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same-origin post", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://10.0.0.2:8090"}, true},
		{"post from APP_URL behind a proxy", http.MethodPost, map[string]string{"Origin": "https://hub.example.com"}, true},
		{"non-browser post", http.MethodPost, nil, true},
		{"cross-site get", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://10.0.0.2:8090/api/collections/systems/records", nil)
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			_, allowed := runPolicy(t, policy, req)
			assert.Equal(t, tc.allowed, allowed)
		})
	}
}
