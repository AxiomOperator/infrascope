//go:build testing

package hub

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// do sends a request to the test router.
func (env *statusPageTestEnv) do(t *testing.T, method, host, target string, auth *core.Record, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	if host != "" {
		request.Host = host
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		request.Header.Set("Authorization", authToken(t, auth))
	}
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, request)
	return response
}

func TestStatusPageGroupsValidation(t *testing.T) {
	env := newStatusPageTestEnv(t)
	bindStatusPageHooks(env.hub)
	web := env.monitor(t, map[string]any{"name": "Web"})
	api := env.monitor(t, map[string]any{"name": "API"})
	notOnPage := env.monitor(t, map[string]any{"name": "Elsewhere"})

	save := func(groups any) (int, string) {
		t.Helper()
		response := env.do(t, http.MethodPost, "", "/api/collections/status_pages/records", env.owner, map[string]any{
			"user": env.owner.Id, "slug": "groups-" + time.Now().Format("150405.000000000")[7:], "title": "Groups",
			"monitors": []string{web.Id, api.Id}, "systems": []string{env.system.Id}, "groups": groups,
		})
		return response.Code, response.Body.String()
	}
	component := func(kind, id string) map[string]any { return map[string]any{"type": kind, "id": id} }

	code, body := save([]any{
		map[string]any{"name": "  Frontend  ", "collapsed": true, "components": []any{component("monitor", web.Id), component("system", env.system.Id)}},
		map[string]any{"name": "Backend", "components": []any{component("monitor", api.Id)}},
	})
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"name":"Frontend"`)

	code, _ = save(nil)
	assert.Equal(t, http.StatusOK, code)

	longName := strings.Repeat("x", maxStatusPageGroupName+1)
	tooMany := make([]any, maxStatusPageGroups+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"name": "G", "components": []any{}}
	}
	for name, groups := range map[string]any{
		"component not on page": []any{map[string]any{"name": "A", "components": []any{component("monitor", notOnPage.Id)}}},
		"wrong type":            []any{map[string]any{"name": "A", "components": []any{component("system", web.Id)}}},
		"unknown type":          []any{map[string]any{"name": "A", "components": []any{component("container", web.Id)}}},
		"duplicate":             []any{map[string]any{"name": "A", "components": []any{component("monitor", web.Id)}}, map[string]any{"name": "B", "components": []any{component("monitor", web.Id)}}},
		"empty name":            []any{map[string]any{"name": " ", "components": []any{}}},
		"long name":             []any{map[string]any{"name": longName, "components": []any{}}},
		"too many":              tooMany,
		"not a list":            map[string]any{"name": "A"},
	} {
		code, body := save(groups)
		assert.Equal(t, http.StatusBadRequest, code, name+": "+body)
	}
}

func TestStatusPageGroupsAndBrandingDTO(t *testing.T) {
	env := newStatusPageTestEnv(t)
	web := env.monitor(t, map[string]any{"name": "Web", "status": "up"})
	api := env.monitor(t, map[string]any{"name": "API", "status": "down"})
	loose := env.monitor(t, map[string]any{"name": "Loose", "status": "up"})
	hidden := env.monitor(t, map[string]any{"name": "Gone", "status": "down"})
	groups := []statusPageGroup{
		{Name: "Frontend", Collapsed: true, Components: []statusPageGroupComponent{{componentSystem, env.system.Id}, {componentMonitor, web.Id}}},
		{Name: "Backend", Components: []statusPageGroupComponent{{componentMonitor, api.Id}}},
		// only components that are not published: left out
		{Name: "Empty", Components: []statusPageGroupComponent{{componentMonitor, hidden.Id}}},
	}
	env.page(t, "grouped", true, []string{web.Id, api.Id, loose.Id}, map[string]any{
		"systems": []string{env.system.Id}, "groups": groups,
		"accentColor": "#AB12CD", "footerText": "Contact <support>", "hidePoweredBy": true,
	})

	response := env.get(t, "grouped", nil, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	raw := response.Body.String()
	for _, secret := range []string{web.Id, api.Id, loose.Id, hidden.Id, env.system.Id, env.owner.Id, "Gone"} {
		assert.False(t, strings.Contains(raw, secret), secret)
	}
	var page publicStatusPage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Groups, 2)
	assert.Equal(t, publicStatusGroup{Name: "Frontend", Collapsed: true, Status: "up", Items: []publicStatusGroupItem{
		{Kind: componentSystem, Index: 0}, {Kind: componentMonitor, Index: 0},
	}}, page.Groups[0], "a paused system does not count")
	assert.Equal(t, publicStatusGroup{Name: "Backend", Status: "down", Items: []publicStatusGroupItem{{Kind: componentMonitor, Index: 1}}}, page.Groups[1])
	assert.Equal(t, "Web", page.Monitors[page.Groups[0].Items[1].Index].Name)

	assert.Nil(t, page.Branding.Logo)
	assert.Equal(t, "#ab12cd", page.Branding.AccentColor)
	assert.Equal(t, "Contact <support>", page.Branding.FooterText)
	assert.True(t, page.Branding.HidePoweredBy)

	// pages without groups have an empty list
	env.page(t, "plain", true, []string{web.Id}, nil)
	raw = env.get(t, "plain", nil, "").Body.String()
	assert.Contains(t, raw, `"groups":[]`)
	assert.Contains(t, raw, `"branding":{"logo":null,"accentColor":"","footerText":"","hidePoweredBy":false}`)
}

func TestWorstStatus(t *testing.T) {
	assert.Equal(t, "down", worstStatus([]string{"up", "down", "maintenance"}))
	assert.Equal(t, "maintenance", worstStatus([]string{"up", "maintenance", "paused"}))
	assert.Equal(t, "up", worstStatus([]string{"up", "paused", "pending"}))
	assert.Equal(t, "paused", worstStatus([]string{"paused", "paused"}))
	assert.Equal(t, "unknown", worstStatus([]string{"pending"}))
	assert.Equal(t, "unknown", worstStatus(nil))
}

func TestStatusPageLogo(t *testing.T) {
	env := newStatusPageTestEnv(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script><rect width="10" height="10"/></svg>`
	logo, err := filesystem.NewFileFromBytes([]byte(svg), "logo.svg")
	require.NoError(t, err)
	page := env.page(t, "logo", true, nil, map[string]any{"logo": logo})
	require.NotEmpty(t, page.GetString("logo"))
	private := env.page(t, "private-logo", false, nil, nil)
	privateLogo, err := filesystem.NewFileFromBytes([]byte(svg), "logo.svg")
	require.NoError(t, err)
	private.Set("logo", privateLogo)
	require.NoError(t, env.hub.Save(private))

	data := env.getPage(t, "logo", nil)
	require.NotNil(t, data.Branding.Logo)
	assert.True(t, strings.HasPrefix(*data.Branding.Logo, "/api/beszel/status-pages/logo/logo?v="), *data.Branding.Logo)
	assert.NotContains(t, *data.Branding.Logo, page.GetString("logo"))

	response := env.do(t, http.MethodGet, "", *data.Branding.Logo, nil, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, svg, response.Body.String())
	assert.Equal(t, "image/svg+xml", response.Header().Get("Content-Type"))
	assert.Equal(t, statusPageLogoCSP, response.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "inline", response.Header().Get("Content-Disposition"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, statusPageLogoCacheControl, response.Header().Get("Cache-Control"))

	// not public, even for the owner, or no logo
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "", "/api/beszel/status-pages/private-logo/logo", env.owner, nil).Code)
	env.page(t, "no-logo", true, nil, nil)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "", "/api/beszel/status-pages/no-logo/logo", nil, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "", "/api/beszel/status-pages/missing/logo", nil, nil).Code)

	// only images are accepted
	text, err := filesystem.NewFileFromBytes([]byte("hello"), "logo.txt")
	require.NoError(t, err)
	page.Set("logo", text)
	assert.Error(t, env.hub.Save(page))
}

func TestStatusPageBadges(t *testing.T) {
	env := newStatusPageTestEnv(t)
	web := env.monitor(t, map[string]any{"name": "Web & <API>", "uptime": map[string]any{"d1": 99.95, "d7": 98.5, "d30": 80}})
	down := env.monitor(t, map[string]any{"name": "Down", "status": "down", "uptime": map[string]any{"d1": 50}})
	env.page(t, "badges", true, []string{web.Id, down.Id}, map[string]any{"systems": []string{env.system.Id}})
	env.page(t, "private-badges", false, []string{web.Id}, nil)

	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		return env.do(t, http.MethodGet, "", target, nil, nil)
	}
	parse := func(t *testing.T, body string) {
		t.Helper()
		decoder := xml.NewDecoder(strings.NewReader(body))
		for {
			if _, err := decoder.Token(); err == io.EOF {
				return
			} else {
				require.NoError(t, err, body)
			}
		}
	}

	response := get("/api/beszel/status-pages/badges/badge.svg")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "image/svg+xml; charset=utf-8", response.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=60", response.Header().Get("Cache-Control"))
	assert.Equal(t, statusPageLogoCSP, response.Header().Get("Content-Security-Policy"))
	body := response.Body.String()
	parse(t, body)
	assert.Contains(t, body, "Status of badges")
	assert.Contains(t, body, "partial outage")
	assert.Contains(t, body, badgeYellow)

	body = get("/api/beszel/status-pages/badges/badge/monitor/1.svg").Body.String()
	parse(t, body)
	assert.Contains(t, body, "Web &amp; &lt;API&gt;")
	assert.NotContains(t, body, "<API>")
	assert.Contains(t, body, ">up<")
	assert.Contains(t, body, badgeGreen)

	body = get("/api/beszel/status-pages/badges/badge/monitor/1.svg?type=uptime&period=24h").Body.String()
	assert.Contains(t, body, "99.95% 24h")
	body = get("/api/beszel/status-pages/badges/badge/monitor/1.svg?type=uptime&period=30d").Body.String()
	assert.Contains(t, body, "80% 30d")
	assert.Contains(t, body, badgeRed)
	body = get("/api/beszel/status-pages/badges/badge/monitor/2.svg").Body.String()
	assert.Contains(t, body, ">down<")
	body = get("/api/beszel/status-pages/badges/badge/system/1.svg").Body.String()
	assert.Contains(t, body, ">paused<")
	assert.NotContains(t, body, env.system.GetString("host"))
	body = get("/api/beszel/status-pages/badges/badge/system/1.svg?type=uptime").Body.String()
	assert.Contains(t, body, "no data")

	// labels are escaped and limited
	evil := `"><script>alert(1)</script>` + strings.Repeat("y", 60)
	body = get("/api/beszel/status-pages/badges/badge.svg?label=" + urlQueryEscape(evil)).Body.String()
	parse(t, body)
	assert.NotContains(t, body, "<script>")
	assert.Contains(t, body, "&lt;script&gt;")
	assert.NotContains(t, body, strings.Repeat("y", 40))

	for target, code := range map[string]int{
		"/api/beszel/status-pages/badges/badge/monitor/3.svg":             http.StatusNotFound,
		"/api/beszel/status-pages/badges/badge/monitor/0.svg":             http.StatusNotFound,
		"/api/beszel/status-pages/badges/badge/monitor/1.png":             http.StatusNotFound,
		"/api/beszel/status-pages/badges/badge/container/1.svg":           http.StatusNotFound,
		"/api/beszel/status-pages/badges/badge.svg?type=cpu":              http.StatusBadRequest,
		"/api/beszel/status-pages/badges/badge.svg?period=1y":             http.StatusBadRequest,
		"/api/beszel/status-pages/private-badges/badge.svg":               http.StatusNotFound,
		"/api/beszel/status-pages/private-badges/badge/monitor/1.svg":     http.StatusNotFound,
		"/api/beszel/status-pages/missing/badge.svg":                      http.StatusNotFound,
		"/api/beszel/status-pages/badges/badge/monitor/1.svg?type=uptime": http.StatusOK,
	} {
		assert.Equal(t, code, get(target).Code, target)
	}
}

func urlQueryEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "%20", `"`, "%22", "<", "%3C", ">", "%3E").Replace(s)
}

func TestRenderBadge(t *testing.T) {
	svg := renderBadge("a&b", "<x>", "#4c1")
	assert.Contains(t, svg, "a&amp;b: &lt;x&gt;")
	var node struct {
		XMLName xml.Name
		Width   string `xml:"width,attr"`
	}
	require.NoError(t, xml.Unmarshal([]byte(svg), &node))
	assert.Equal(t, "svg", node.XMLName.Local)
	assert.NotEmpty(t, node.Width)
	assert.Greater(t, textWidth("WWW"), textWidth("iii"))
}

func TestStatusPageFeeds(t *testing.T) {
	env := newStatusPageTestEnv(t)
	bindIncidentHooks(env.hub)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	page := env.page(t, "feeds", true, []string{monitor.Id}, nil)
	env.page(t, "private-feeds", false, []string{monitor.Id}, nil)
	now := statusPageTestNow
	incident := func(title, status string, started, resolved time.Time) *core.Record {
		t.Helper()
		data := map[string]any{
			"user": env.owner.Id, "title": title, "status": status, "impact": "major",
			"statusPages": []string{page.Id}, "startedAt": started,
		}
		if !resolved.IsZero() {
			data["resolvedAt"] = resolved
		}
		return env.create(t, "incidents", data)
	}
	active := incident("Database <slow> & sad", "investigating", now.Add(-time.Hour), time.Time{})
	env.create(t, "incident_updates", map[string]any{"incident": active.Id, "status": "investigating", "message": "Looking <b>into</b> it ]]> \x01"})
	old := incident("Resolved 20 days ago", "resolved", now.AddDate(0, 0, -21), now.AddDate(0, 0, -20))
	ancient := incident("Resolved 40 days ago", "resolved", now.AddDate(0, 0, -41), now.AddDate(0, 0, -40))

	response := env.do(t, http.MethodGet, "", "/api/beszel/status-pages/feeds/feed.atom", nil, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "application/atom+xml; charset=utf-8", response.Header().Get("Content-Type"))
	body := response.Body.String()
	for _, secret := range []string{active.Id, old.Id, ancient.Id, page.Id, env.owner.Id, monitor.Id} {
		assert.NotContains(t, body, secret)
	}
	var atom atomFeed
	require.NoError(t, xml.Unmarshal(response.Body.Bytes(), &atom), body)
	assert.Equal(t, "Status of feeds", atom.Title)
	require.Len(t, atom.Entries, 2)
	assert.Equal(t, "Database <slow> & sad - Investigating", atom.Entries[0].Title)
	assert.Contains(t, atom.Entries[0].Content.Body, "Looking <b>into</b> it ]]>")
	assert.Equal(t, "Resolved 20 days ago", atom.Entries[1].Title)
	assert.True(t, strings.HasPrefix(atom.Entries[0].ID, "urn:infrascope:"))
	assert.NotEqual(t, atom.Entries[0].ID, atom.Entries[1].ID)
	assert.Contains(t, atom.Links[0].Href, "/status/feeds")
	assert.Contains(t, atom.Links[1].Href, "/api/beszel/status-pages/feeds/feed.atom")

	response = env.do(t, http.MethodGet, "", "/api/beszel/status-pages/feeds/feed.rss", nil, nil)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "application/rss+xml; charset=utf-8", response.Header().Get("Content-Type"))
	var rss struct {
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
				GUID  string `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	require.NoError(t, xml.Unmarshal(response.Body.Bytes(), &rss), response.Body.String())
	assert.Equal(t, "Status of feeds", rss.Channel.Title)
	require.Len(t, rss.Channel.Items, 2)
	assert.Equal(t, atom.Entries[0].ID, rss.Channel.Items[0].GUID)

	for _, target := range []string{"/api/beszel/status-pages/private-feeds/feed.atom", "/api/beszel/status-pages/missing/feed.rss"} {
		assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "", target, env.owner, nil).Code, target)
	}
}

func TestStatusPageCustomDomains(t *testing.T) {
	env := newStatusPageTestEnv(t)
	bindStatusPageHooks(env.hub)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	env.page(t, "custom", true, []string{monitor.Id}, map[string]any{"customDomain": "status.example.com"})
	env.page(t, "private-custom", false, []string{monitor.Id}, map[string]any{"customDomain": "private.example.com"})
	env.page(t, "other", true, []string{monitor.Id}, nil)

	// lookup: case-insensitive, with or without port and trailing dot
	for _, host := range []string{"status.example.com", "STATUS.Example.COM", "status.example.com:8443", "status.example.com."} {
		slug, ok := env.hub.statusPages.domains.lookup(env.hub, host)
		assert.True(t, ok, host)
		assert.Equal(t, "custom", slug, host)
	}
	for _, host := range []string{"", "example.com", "other.example.com", "status.example.com.evil.com"} {
		_, ok := env.hub.statusPages.domains.lookup(env.hub, host)
		assert.False(t, ok, host)
	}

	host := "Status.Example.com:443"
	for target, code := range map[string]int{
		"/api/beszel/status-pages/custom":           http.StatusOK,
		"/api/beszel/status-pages/custom/badge.svg": http.StatusOK,
		"/api/beszel/status-pages/custom/feed.atom": http.StatusOK,
		"/api/beszel/status-pages/by-host":          http.StatusOK,
		"/api/beszel/status-pages/other":            http.StatusNotFound,
		"/api/beszel/status-pages/other/badge.svg":  http.StatusNotFound,
		"/api/collections/users/records":            http.StatusNotFound,
		"/api/collections/status_pages/records":     http.StatusNotFound,
		"/api/collections/users/auth-methods":       http.StatusNotFound,
		"/api/beszel/info":                          http.StatusNotFound,
		"/api/beszel/first-run":                     http.StatusNotFound,
		"/_/":                                       http.StatusNotFound,
		"/settings/general":                         http.StatusNotFound,
		"/system/abc":                               http.StatusNotFound,
	} {
		response := env.do(t, http.MethodGet, host, target, nil, nil)
		assert.Equal(t, code, response.Code, target+": "+response.Body.String())
	}
	assert.JSONEq(t, `{"slug":"custom"}`, env.do(t, http.MethodGet, host, "/api/beszel/status-pages/by-host", nil, nil).Body.String())
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodPost, host, "/api/collections/users/auth-with-password", nil,
		map[string]any{"identity": "owner@example.com", "password": "x"}).Code)

	// the app host is unaffected
	assert.Equal(t, http.StatusOK, env.do(t, http.MethodGet, "hub.example.com", "/api/beszel/status-pages/other", nil, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "", "/api/beszel/status-pages/by-host", nil, nil).Code)

	// private pages are not served on their domain, not even to the owner
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "private.example.com", "/api/beszel/status-pages/private-custom", env.owner, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "private.example.com", "/api/beszel/status-pages/by-host", nil, nil).Code)
	assert.Equal(t, http.StatusOK, env.do(t, http.MethodGet, "", "/api/beszel/status-pages/private-custom", env.owner, nil).Code)

	// domains are reloaded after changes
	page, err := env.hub.FindFirstRecordByData("status_pages", "slug", "other")
	require.NoError(t, err)
	page.Set("customDomain", "other.example.com")
	require.NoError(t, env.hub.Save(page))
	slug, ok := env.hub.statusPages.domains.lookup(env.hub, "other.example.com")
	assert.True(t, ok)
	assert.Equal(t, "other", slug)
}

func TestStatusPageDomainValidation(t *testing.T) {
	env := newStatusPageTestEnv(t)
	bindStatusPageHooks(env.hub)
	env.hub.appURL = "https://hub.example.com:8090/beszel"
	env.page(t, "taken", true, nil, map[string]any{"customDomain": "taken.example.com"})

	save := func(domain string) (int, string) {
		t.Helper()
		response := env.do(t, http.MethodPost, "", "/api/collections/status_pages/records", env.owner, map[string]any{
			"user": env.owner.Id, "slug": "d-" + time.Now().Format("150405.000000000")[7:], "title": "D", "customDomain": domain,
		})
		return response.Code, response.Body.String()
	}
	code, body := save("  Status.Example.COM. ")
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"customDomain":"status.example.com"`)
	code, _ = save("")
	assert.Equal(t, http.StatusOK, code)
	for _, domain := range []string{
		"https://x.example.com", "x.example.com:8080", "x.example.com/path", "localhost", "10.0.0.1",
		"hub.example.com", "TAKEN.example.com", "under_score.example.com",
	} {
		code, body := save(domain)
		assert.Equal(t, http.StatusBadRequest, code, domain+": "+body)
	}
}

func TestStatusPageDomainPathAllowed(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/", true},
		{"GET", "/status/s", true},
		{"GET", "/status/s/", true},
		{"GET", "/status/other", false},
		{"GET", "/assets/index.js", true},
		{"GET", "/static/icon.svg", true},
		{"GET", "/api/beszel/status-pages/s", true},
		{"GET", "/api/beszel/status-pages/s/logo", true},
		{"POST", "/api/beszel/status-pages/s/subscribe", true},
		{"GET", "/api/beszel/status-pages/sx", false},
		{"GET", "/api/beszel/status-pages/by-host", true},
		{"POST", "/", false},
		{"GET", "/_/", false},
		{"GET", "/api/collections/users/records", false},
		{"GET", "/api/files/status_pages/x/y.png", false},
		{"GET", "/containers", false},
	} {
		assert.Equal(t, tc.want, statusPageDomainPathAllowed(tc.method, tc.path, "s"), tc.method+" "+tc.path)
	}
}

func TestStatusPageDomainHTML(t *testing.T) {
	env := newStatusPageTestEnv(t)
	env.hub.appURL = "https://hub.example.com/beszel"
	index := []byte("<html><head><script src=\"./assets/a.js\"></script><script>globalThis.BESZEL = \"{info}\"</script></head></html>")
	html := statusPageDomainHTML(env.hub, index, "my-page")
	assert.Contains(t, html, `<meta name="infrascope-status-slug" content="my-page" />`)
	assert.Contains(t, html, `src="/assets/a.js"`)
	assert.Contains(t, html, `"BASE_PATH":"/"`)
	assert.NotContains(t, html, "hub.example.com")
	assert.NotContains(t, html, "{info}")
}
