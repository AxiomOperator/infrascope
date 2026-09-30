package hub

import (
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
)

// statusPageHostKey is the request store key of the slug of the status page
// whose custom domain a request was sent to.
const statusPageHostKey = "infrascopeStatusPageHost"

// statusPageDomainsMiddlewareId is the id of statusPageDomainGuard.
const statusPageDomainsMiddlewareId = "infrascopeStatusPageDomains"

// statusPageDomains maps the custom domains of status pages to their slugs.
// It is loaded on first use and reloaded after status pages change.
type statusPageDomains struct {
	mu     sync.RWMutex
	loaded bool
	slugs  map[string]string
}

// lookup returns the slug of the status page with the custom domain of host
// (a Host header, with or without port).
func (d *statusPageDomains) lookup(app core.App, host string) (string, bool) {
	host = normalizeHost(host)
	if host == "" {
		return "", false
	}
	d.mu.RLock()
	loaded, slug := d.loaded, d.slugs[host]
	d.mu.RUnlock()
	if loaded {
		return slug, slug != ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		var rows []struct {
			Slug   string `db:"slug"`
			Domain string `db:"customDomain"`
		}
		err := app.DB().Select("slug", "customDomain").From("status_pages").
			Where(dbx.NewExp("customDomain != ''")).All(&rows)
		if err != nil {
			app.Logger().Error("Failed to load status page domains", "err", err)
			return "", false
		}
		d.slugs = make(map[string]string, len(rows))
		for _, row := range rows {
			d.slugs[normalizeHost(row.Domain)] = row.Slug
		}
		d.loaded = true
	}
	slug = d.slugs[host]
	return slug, slug != ""
}

// invalidate makes the next lookup reload the domains.
func (d *statusPageDomains) invalidate() {
	d.mu.Lock()
	d.loaded = false
	d.slugs = nil
	d.mu.Unlock()
}

// normalizeHost returns the lowercase hostname of a Host header value,
// without port or trailing dot.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// bindStatusPageDomainEvents reloads the custom domains after status pages change.
func bindStatusPageDomainEvents(h *Hub) {
	invalidate := func(e *core.RecordEvent) error {
		h.statusPages.domains.invalidate()
		return e.Next()
	}
	h.OnRecordAfterCreateSuccess("status_pages").BindFunc(invalidate)
	h.OnRecordAfterUpdateSuccess("status_pages").BindFunc(invalidate)
	h.OnRecordAfterDeleteSuccess("status_pages").BindFunc(invalidate)
}

// validateStatusPageDomain normalizes the custom domain of a submitted page
// and returns an error message when it cannot be used. The format and
// uniqueness are checked by the field and its index.
func (h *Hub) validateStatusPageDomain(record *core.Record) string {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.GetString("customDomain"))), ".")
	record.Set("customDomain", domain)
	if domain == "" {
		return ""
	}
	if strings.ContainsAny(domain, ":/?#@ ") {
		return "Enter the custom domain as a hostname, without scheme, port or path."
	}
	for _, appURL := range []string{h.appURL, h.Settings().Meta.AppURL} {
		if u, err := url.Parse(appURL); err == nil && u.Hostname() != "" && normalizeHost(u.Hostname()) == domain {
			return "The custom domain must differ from the domain of the hub."
		}
	}
	return ""
}

// statusPageHostSlug returns the slug of the status page whose custom domain
// the request was sent to, or "".
func statusPageHostSlug(e *core.RequestEvent) string {
	slug, _ := e.Get(statusPageHostKey).(string)
	return slug
}

// registerStatusPageRoutes registers the public status page routes besides
// the page itself, and the custom domain guard.
func (h *Hub) registerStatusPageRoutes(se *core.ServeEvent, api *router.RouterGroup[*core.RequestEvent]) {
	se.Router.Bind(&hook.Handler[*core.RequestEvent]{
		Id: statusPageDomainsMiddlewareId,
		// before the auth token is loaded, so custom domains never authenticate
		Priority: apis.DefaultLoadAuthTokenMiddlewarePriority - 1,
		Func:     h.statusPageDomainGuard,
	})
	api.GET("/status-pages/by-host", h.handleStatusPageByHost)
	api.GET("/status-pages/{slug}/logo", h.handleStatusPageLogo)
	api.GET("/status-pages/{slug}/badge.svg", h.handleStatusPageBadge)
	api.GET("/status-pages/{slug}/badge/{kind}/{file}", h.handleStatusPageBadge)
	api.GET("/status-pages/{slug}/feed.atom", h.handleStatusPageFeed)
	api.GET("/status-pages/{slug}/feed.rss", h.handleStatusPageFeed)
}

// statusPageDomainGuard restricts requests to the custom domain of a status
// page to that page: its root (and /status/{slug}), static assets and its
// public API. Everything else, including the login UI, the PocketBase admin
// UI and the collections API, does not exist there. Requests are never
// authenticated, so owners cannot preview private pages on custom domains.
func (h *Hub) statusPageDomainGuard(e *core.RequestEvent) error {
	slug, ok := h.statusPages.domains.lookup(e.App, e.Request.Host)
	if !ok {
		return e.Next()
	}
	e.Set(statusPageHostKey, slug)
	e.Request.Header.Del("Authorization")
	e.Request.Header.Del("Cookie")
	if !statusPageDomainPathAllowed(e.Request.Method, e.Request.URL.Path, slug) {
		return e.NotFoundError("", nil)
	}
	return e.Next()
}

// statusPageDomainPathAllowed reports whether a request to the custom domain
// of the status page with slug may reach the hub.
func statusPageDomainPathAllowed(method, urlPath, slug string) bool {
	api := "/api/beszel/status-pages/" + slug
	// the page's own API, including routes other features add below it
	if urlPath == api || strings.HasPrefix(urlPath, api+"/") {
		return true
	}
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	switch strings.TrimSuffix(urlPath, "/") {
	case "", "/status/" + slug, "/api/beszel/status-pages/by-host", "/api/health":
		return true
	}
	return strings.HasPrefix(urlPath, "/static/") || strings.HasPrefix(urlPath, "/assets/")
}

// handleStatusPageByHost returns the slug of the public status page whose
// custom domain the request was sent to.
func (h *Hub) handleStatusPageByHost(e *core.RequestEvent) error {
	slug := statusPageHostSlug(e)
	if slug == "" {
		return statusPageNotFound(e)
	}
	if _, err := findPublicStatusPage(e, slug); err != nil {
		return err
	}
	return e.JSON(http.StatusOK, map[string]string{"slug": slug})
}

// statusPageDomainHTML returns index.html for the custom domain of the status
// page with slug: the app is served at the root, the page is marked with a
// meta tag that makes the router show it at every path, and hub details are
// left out.
func statusPageDomainHTML(hub *Hub, indexHTML []byte, slug string) string {
	info := getPublicAppInfo(hub)
	info.BASE_PATH = "/"
	info.HUB_URL = ""
	info.HUB_VERSION = ""
	content, err := json.Marshal(info)
	if err != nil {
		return ""
	}
	page := strings.ReplaceAll(string(indexHTML), "./", info.BASE_PATH)
	page = strings.Replace(page, "\"{info}\"", string(content), 1)
	meta := `<meta name="infrascope-status-slug" content="` + html.EscapeString(slug) + `" />`
	return strings.Replace(page, "<head>", "<head>\n\t\t"+meta, 1)
}
