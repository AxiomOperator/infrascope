package hub

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/henrygd/beszel/internal/hub/utils"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// originPolicy restricts cross-origin access when requests are authenticated
// implicitly. With AUTO_LOGIN or TRUSTED_AUTH_HEADER, the browser (or the
// proxy in front of the hub) authenticates every request, so PocketBase's
// default CORS policy ("*") would let any website read authenticated API
// responses from a hub on the user's network, and cross-site form posts would
// act as the user (CSRF).
type originPolicy struct {
	// appOrigin is the only allowed cross-origin origin ("" allows none).
	appOrigin string
	csrf      *http.CrossOriginProtection
}

// newOriginPolicy returns the policy for the given APP_URL, or nil when
// neither AUTO_LOGIN nor TRUSTED_AUTH_HEADER is set (PocketBase defaults apply).
func newOriginPolicy(appURL string) *originPolicy {
	autoLogin, _ := utils.GetEnv("AUTO_LOGIN")
	trustedHeader, _ := utils.GetEnv("TRUSTED_AUTH_HEADER")
	if autoLogin == "" && trustedHeader == "" {
		return nil
	}
	policy := &originPolicy{appOrigin: urlOrigin(appURL), csrf: http.NewCrossOriginProtection()}
	if policy.appOrigin != "" {
		// The hub may be reached through a proxy that rewrites the Host header.
		_ = policy.csrf.AddTrustedOrigin(policy.appOrigin)
	}
	return policy
}

// urlOrigin returns the scheme://host[:port] origin of rawURL, or "".
func urlOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// corsHandler replaces PocketBase's default CORS middleware (same id).
func (p *originPolicy) corsHandler() *hook.Handler[*core.RequestEvent] {
	return apis.CORS(apis.CORSConfig{
		AllowOriginFunc: func(origin string) (bool, error) {
			return p.appOrigin != "" && strings.EqualFold(origin, p.appOrigin), nil
		},
		AllowMethods: []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete},
	})
}

// checkOrigin rejects cross-origin state-changing browser requests. Same-origin
// requests, requests from APP_URL and non-browser clients (agents, push
// monitors, scripts) are unaffected.
func (p *originPolicy) checkOrigin(e *core.RequestEvent) error {
	if err := p.csrf.Check(e.Request); err != nil {
		return e.ForbiddenError("Cross-origin request blocked.", nil)
	}
	return e.Next()
}

// register applies the policy to the router and logs what is in effect.
func (p *originPolicy) register(se *core.ServeEvent) {
	se.Router.Unbind(apis.DefaultCorsMiddlewareId)
	se.Router.Bind(p.corsHandler())
	se.Router.Bind(&hook.Handler[*core.RequestEvent]{
		Id: "beszelOriginCheck",
		// run right after CORS, before any other middleware or handler
		Priority: apis.DefaultCorsMiddlewarePriority + 1,
		Func:     p.checkOrigin,
	})
	if p.appOrigin == "" {
		slog.Warn("AUTO_LOGIN or TRUSTED_AUTH_HEADER is set without APP_URL: CORS allows no cross-origin requests and cross-origin writes are rejected. Set APP_URL to the hub's public URL.")
		return
	}
	slog.Info("AUTO_LOGIN or TRUSTED_AUTH_HEADER is set: CORS is restricted to APP_URL and cross-origin writes are rejected", "origin", p.appOrigin)
}

// registerOriginPolicy restricts CORS and adds a CSRF check when requests are
// authenticated implicitly; otherwise PocketBase's defaults are kept.
func (h *Hub) registerOriginPolicy(se *core.ServeEvent) {
	policy := newOriginPolicy(se.App.Settings().Meta.AppURL)
	if policy == nil {
		slog.Debug("CORS: using PocketBase defaults (--origins)")
		return
	}
	policy.register(se)
}
