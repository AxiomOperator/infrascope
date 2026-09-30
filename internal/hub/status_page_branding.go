package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// maxStatusPageGroups is the maximum number of component groups of a page.
	maxStatusPageGroups = 50
	// maxStatusPageGroupName is the maximum length of a group name, in characters.
	maxStatusPageGroupName = 100
	// statusPageLogoCacheControl is the Cache-Control header of logos. Logo
	// URLs change with the logo file, so they can be cached for a while.
	statusPageLogoCacheControl = "public, max-age=3600"
	// statusPageLogoCSP keeps scripts and external resources of SVG logos
	// from running when a logo is opened directly.
	statusPageLogoCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
)

// Component types of status page groups.
const (
	componentMonitor = "monitor"
	componentSystem  = "system"
)

// statusPageLogoTypes maps the allowed logo file extensions to their content types.
var statusPageLogoTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// statusPageGroup is a group of components as stored in status_pages.groups.
type statusPageGroup struct {
	Name       string                     `json:"name"`
	Collapsed  bool                       `json:"collapsed,omitempty"`
	Components []statusPageGroupComponent `json:"components"`
}

type statusPageGroupComponent struct {
	// Type is componentMonitor or componentSystem.
	Type string `json:"type"`
	ID   string `json:"id"`
}

// publicStatusGroup is a group of a public status page. Items reference
// entries of the page's systems and monitors by index, so no ids are published.
type publicStatusGroup struct {
	Name      string                  `json:"name"`
	Collapsed bool                    `json:"collapsed"`
	Status    string                  `json:"status"`
	Items     []publicStatusGroupItem `json:"items"`
}

type publicStatusGroupItem struct {
	// Kind is componentMonitor or componentSystem.
	Kind  string `json:"kind"`
	Index int    `json:"index"`
}

// publicStatusBranding is the branding of a public status page.
type publicStatusBranding struct {
	// Logo is the path of the logo below the hub's base path, or null.
	Logo          *string `json:"logo"`
	AccentColor   string  `json:"accentColor"`
	FooterText    string  `json:"footerText"`
	HidePoweredBy bool    `json:"hidePoweredBy"`
}

// statusPageGroups returns the stored groups of a page (nil when unset or invalid).
func statusPageGroups(page *core.Record) []statusPageGroup {
	var groups []statusPageGroup
	if err := page.UnmarshalJSONField("groups", &groups); err != nil {
		return nil
	}
	return groups
}

// buildPublicGroups returns the public groups of a page. monitorIDs and
// systemIDs are the ids of the published monitors and systems, in the order
// of result.Monitors and result.Systems. Components that are not published
// are left out, and so are groups without published components.
func buildPublicGroups(groups []statusPageGroup, result *publicStatusPage, monitorIDs, systemIDs []string) []publicStatusGroup {
	public := []publicStatusGroup{}
	for _, group := range groups {
		item := publicStatusGroup{Name: group.Name, Collapsed: group.Collapsed, Items: []publicStatusGroupItem{}}
		statuses := make([]string, 0, len(group.Components))
		for _, component := range group.Components {
			switch component.Type {
			case componentMonitor:
				if index := slices.Index(monitorIDs, component.ID); index >= 0 {
					item.Items = append(item.Items, publicStatusGroupItem{Kind: componentMonitor, Index: index})
					statuses = append(statuses, result.Monitors[index].Status)
				}
			case componentSystem:
				if index := slices.Index(systemIDs, component.ID); index >= 0 {
					item.Items = append(item.Items, publicStatusGroupItem{Kind: componentSystem, Index: index})
					statuses = append(statuses, result.Systems[index].Status)
				}
			}
		}
		if len(item.Items) == 0 {
			continue
		}
		item.Status = worstStatus(statuses)
		public = append(public, item)
	}
	return public
}

// worstStatus returns the worst of the statuses of a group's components.
// Paused and pending components (see buildStatusPage) do not count unless
// all components are paused or pending.
func worstStatus(statuses []string) string {
	rank := map[string]int{uptime.StatusUp: 1, uptime.StatusUnknown: 2, uptime.StatusMaintenance: 3, uptime.StatusDown: 4}
	worst, worstRank := "", 0
	for _, status := range statuses {
		if r := rank[status]; r > worstRank {
			worst, worstRank = status, r
		}
	}
	if worst != "" {
		return worst
	}
	if len(statuses) > 0 && !slices.ContainsFunc(statuses, func(s string) bool { return s != uptime.StatusPaused }) {
		return uptime.StatusPaused
	}
	return uptime.StatusUnknown
}

// buildPublicBranding returns the public branding of a page.
func buildPublicBranding(page *core.Record) publicStatusBranding {
	branding := publicStatusBranding{
		AccentColor:   strings.ToLower(page.GetString("accentColor")),
		FooterText:    page.GetString("footerText"),
		HidePoweredBy: page.GetBool("hidePoweredBy"),
	}
	if logo := page.GetString("logo"); logo != "" && statusPageLogoTypes[strings.ToLower(path.Ext(logo))] != "" {
		// the version changes with the file, so the logo can be cached
		sum := sha256.Sum256([]byte(page.Id + "/" + logo))
		url := "/api/beszel/status-pages/" + page.GetString("slug") + "/logo?v=" + hex.EncodeToString(sum[:6])
		branding.Logo = &url
	}
	return branding
}

// validateStatusPageGroups normalizes the groups of a submitted page and
// returns an error message when they are invalid: groups may only reference
// the page's monitors and systems, each at most once.
func validateStatusPageGroups(record *core.Record) string {
	raw := strings.TrimSpace(record.GetString("groups"))
	if raw == "" || raw == "null" {
		record.Set("groups", nil)
		return ""
	}
	var groups []statusPageGroup
	if err := record.UnmarshalJSONField("groups", &groups); err != nil {
		return "Invalid groups."
	}
	if len(groups) > maxStatusPageGroups {
		return "A status page can have up to " + strconv.Itoa(maxStatusPageGroups) + " groups."
	}
	monitors, systems := record.GetStringSlice("monitors"), record.GetStringSlice("systems")
	seen := make(map[statusPageGroupComponent]bool)
	for i := range groups {
		group := &groups[i]
		group.Name = strings.TrimSpace(group.Name)
		if group.Name == "" || utf8.RuneCountInString(group.Name) > maxStatusPageGroupName {
			return "Group names must have 1 to " + strconv.Itoa(maxStatusPageGroupName) + " characters."
		}
		if group.Components == nil {
			group.Components = []statusPageGroupComponent{}
		}
		for _, component := range group.Components {
			var ok bool
			switch component.Type {
			case componentMonitor:
				ok = slices.Contains(monitors, component.ID)
			case componentSystem:
				ok = slices.Contains(systems, component.ID)
			}
			if !ok {
				return "Groups can only contain the monitors and systems of the page."
			}
			if seen[component] {
				return "A monitor or system can only be in one group."
			}
			seen[component] = true
		}
	}
	record.Set("groups", groups)
	return ""
}

// handleStatusPageLogo serves the logo of a public status page. SVG logos
// are served with a restrictive Content-Security-Policy, so scripts in them
// never run, even when the logo is opened directly.
func (h *Hub) handleStatusPageLogo(e *core.RequestEvent) error {
	if err := h.limitStatusPage(e); err != nil {
		return err
	}
	page, err := findPublicStatusPage(e, e.Request.PathValue("slug"))
	if err != nil {
		return err
	}
	name := page.GetString("logo")
	contentType := statusPageLogoTypes[strings.ToLower(path.Ext(name))]
	if name == "" || contentType == "" {
		return e.NotFoundError("Logo not found.", nil)
	}
	fsys, err := e.App.NewFilesystem()
	if err != nil {
		return e.InternalServerError("", err)
	}
	defer fsys.Close()
	reader, err := fsys.GetReader(page.BaseFilesPath() + "/" + name)
	if err != nil {
		return e.NotFoundError("Logo not found.", nil)
	}
	defer reader.Close()
	header := e.Response.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", "inline")
	header.Set("Content-Security-Policy", statusPageLogoCSP)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", statusPageLogoCacheControl)
	if size := reader.Size(); size > 0 {
		header.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	e.Response.WriteHeader(http.StatusOK)
	_, err = io.Copy(e.Response, reader)
	return err
}

// limitStatusPage counts a public status page request of the client IP and
// returns a 429 error when the client exceeded the limit.
func (h *Hub) limitStatusPage(e *core.RequestEvent) error {
	if ok, retryAfter := h.statusPages.limits.allow(e.RealIP()); !ok {
		seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
		e.Response.Header().Set("Retry-After", strconv.Itoa(seconds))
		return e.TooManyRequestsError("Too many requests.", nil)
	}
	return nil
}

// findPublicStatusPage returns the public status page with the slug, or a
// not found error. Pages that are not public do not exist here, even for
// their owner: badges, feeds and logos are embedded without credentials.
func findPublicStatusPage(e *core.RequestEvent, slug string) (*core.Record, error) {
	page, err := e.App.FindFirstRecordByData("status_pages", "slug", slug)
	if err != nil || !page.GetBool("public") {
		return nil, statusPageNotFound(e)
	}
	return page, nil
}
