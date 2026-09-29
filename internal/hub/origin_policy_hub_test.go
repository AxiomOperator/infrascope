//go:build testing

package hub_test

import (
	"net/http"
	"testing"

	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/pocketbase/core"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The policy replaces PocketBase's CORS middleware on the running hub.
func TestOriginPolicyOnHub(t *testing.T) {
	var hubs []*beszelTests.TestHub
	defer func() {
		for _, hub := range hubs {
			hub.Cleanup()
		}
	}()
	t.Setenv("AUTO_LOGIN", "user@test.com")
	t.Setenv("APP_URL", "https://hub.example.com")

	testAppFactory := func(t testing.TB) *pbTests.TestApp {
		hub, _ := beszelTests.NewTestHub(t.TempDir())
		hubs = append(hubs, hub)
		hub.StartHub()
		return hub.TestApp
	}
	createUser := func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
		beszelTests.CreateUser(app, "user@test.com", "password123")
	}

	scenarios := []beszelTests.ApiScenario{
		{
			Name:   "cross-site write is rejected",
			Method: http.MethodPost,
			URL:    "/api/beszel/user-alerts",
			Headers: map[string]string{
				"Origin":         "https://evil.example",
				"Sec-Fetch-Site": "cross-site",
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{"Cross-origin request blocked"},
			TestAppFactory:  testAppFactory,
			BeforeTestFunc:  createUser,
		},
		{
			Name:            "cross-origin read gets no CORS grant",
			Method:          http.MethodGet,
			URL:             "/api/beszel/getkey",
			Headers:         map[string]string{"Origin": "https://evil.example"},
			ExpectedStatus:  200,
			ExpectedContent: []string{"\"key\":"},
			TestAppFactory:  testAppFactory,
			BeforeTestFunc:  createUser,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				assert.Empty(t, res.Header.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			Name:            "APP_URL origin is allowed",
			Method:          http.MethodGet,
			URL:             "/api/beszel/getkey",
			Headers:         map[string]string{"Origin": "https://hub.example.com"},
			ExpectedStatus:  200,
			ExpectedContent: []string{"\"key\":"},
			TestAppFactory:  testAppFactory,
			BeforeTestFunc:  createUser,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				assert.Equal(t, "https://hub.example.com", res.Header.Get("Access-Control-Allow-Origin"))
			},
		},
	}
	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

// Without implicit auth, requests carry explicit tokens and PocketBase's
// defaults apply: no origin check is added.
func TestNoOriginCheckWithoutImplicitAuth(t *testing.T) {
	hub, err := beszelTests.NewTestHub(t.TempDir())
	require.NoError(t, err)
	defer hub.Cleanup()
	hub.StartHub()
	scenario := beszelTests.ApiScenario{
		Name:   "cross-site write reaches auth",
		Method: http.MethodPost,
		URL:    "/api/beszel/user-alerts",
		Headers: map[string]string{
			"Origin":         "https://evil.example",
			"Sec-Fetch-Site": "cross-site",
		},
		ExpectedStatus:  401,
		ExpectedContent: []string{"requires valid"},
		TestAppFactory:  func(testing.TB) *pbTests.TestApp { return hub.TestApp },
	}
	scenario.Test(t)
}
