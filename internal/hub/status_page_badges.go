package hub

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// statusPageBadgeCacheTTL is how long a badge is cached.
	statusPageBadgeCacheTTL = time.Minute
	// maxBadgeLabel is the maximum length of a badge label, in characters.
	maxBadgeLabel = 40
)

// Badge colors (the shields.io palette).
const (
	badgeGreen       = "#4c1"
	badgeYellowGreen = "#97ca00"
	badgeYellow      = "#dfb317"
	badgeOrange      = "#fe7d37"
	badgeRed         = "#e05d44"
	badgeBlue        = "#007ec6"
	badgeGrey        = "#9f9f9f"
	badgeLabelColor  = "#555"
)

// badgeStatuses are the messages and colors of status badges.
var badgeStatuses = map[string][2]string{
	uptime.StatusUp:          {"up", badgeGreen},
	uptime.StatusDown:        {"down", badgeRed},
	overallDegraded:          {"degraded", badgeYellow},
	uptime.StatusMaintenance: {"maintenance", badgeBlue},
	uptime.StatusPending:     {"pending", badgeYellow},
	uptime.StatusPaused:      {"paused", badgeGrey},
	uptime.StatusUnknown:     {"unknown", badgeGrey},
}

// badgeOverallStatuses are the messages of overall status badges that differ
// from badgeStatuses.
var badgeOverallStatuses = map[string]string{
	uptime.StatusUp:   "operational",
	overallDegraded:   "partial outage",
	uptime.StatusDown: "major outage",
}

// handleStatusPageBadge serves an SVG badge with the status or uptime of a
// public status page (/badge.svg) or of one of its components
// (/badge/monitor/{n}.svg, /badge/system/{n}.svg, 1-based in page order).
//
// Query parameters: type (status or uptime), period (24h, 7d or 30d, for
// uptime) and label (at most maxBadgeLabel characters; defaults to the page
// title or component name).
func (h *Hub) handleStatusPageBadge(e *core.RequestEvent) error {
	if err := h.limitStatusPage(e); err != nil {
		return err
	}
	kind, file := e.Request.PathValue("kind"), e.Request.PathValue("file")
	index := 0
	if kind != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(file, ".svg"))
		if (kind != componentMonitor && kind != componentSystem) || !strings.HasSuffix(file, ".svg") || err != nil || n < 1 {
			return e.NotFoundError("Badge not found.", nil)
		}
		index = n - 1
	}
	query := e.Request.URL.Query()
	badgeType := query.Get("type")
	if badgeType == "" {
		badgeType = "status"
	}
	period := query.Get("period")
	if period == "" {
		period = "24h"
	}
	if (badgeType != "status" && badgeType != "uptime") || (period != "24h" && period != "7d" && period != "30d") {
		return e.BadRequestError("Invalid badge type or period.", nil)
	}
	label := truncateRunes(strings.TrimSpace(query.Get("label")), maxBadgeLabel)

	slug := e.Request.PathValue("slug")
	key := strings.Join([]string{"badge", slug, kind, strconv.Itoa(index), badgeType, period, label}, "|")
	if svg, ok := h.statusPages.cache.GetOk(key); ok {
		return writeBadge(e, svg)
	}
	page, err := findPublicStatusPage(e, slug)
	if err != nil {
		return err
	}
	data, err := h.publicStatusPageData(e.App, page)
	if err != nil {
		return e.InternalServerError("", err)
	}
	name, status, uptimes, ok := badgeSubject(data, kind, index)
	if !ok {
		return e.NotFoundError("Badge not found.", nil)
	}
	if label == "" {
		label = truncateRunes(name, maxBadgeLabel)
	}
	var message, color string
	if badgeType == "uptime" {
		message, color = uptimeBadge(uptimes, period)
	} else {
		message, color = statusBadge(status, kind == "")
	}
	svg := renderBadge(label, message, color)
	h.statusPages.cache.Set(key, svg, statusPageBadgeCacheTTL)
	return writeBadge(e, svg)
}

// badgeSubject returns the name, status and uptime of the page (kind "") or
// of the component with the 0-based index of the kind.
func badgeSubject(data *publicStatusPage, kind string, index int) (name, status string, uptimes []uptime.Uptime, ok bool) {
	switch kind {
	case "":
		for _, system := range data.Systems {
			uptimes = append(uptimes, system.Uptime)
		}
		for _, monitor := range data.Monitors {
			uptimes = append(uptimes, monitor.Uptime)
		}
		return data.Title, data.Overall, uptimes, true
	case componentMonitor:
		if index < len(data.Monitors) {
			monitor := data.Monitors[index]
			return monitor.Name, monitor.Status, []uptime.Uptime{monitor.Uptime}, true
		}
	case componentSystem:
		if index < len(data.Systems) {
			system := data.Systems[index]
			return system.Name, system.Status, []uptime.Uptime{system.Uptime}, true
		}
	}
	return "", "", nil, false
}

// statusBadge returns the message and color of a status badge.
func statusBadge(status string, overall bool) (string, string) {
	badge, ok := badgeStatuses[status]
	if !ok {
		badge = badgeStatuses[uptime.StatusUnknown]
	}
	if message, ok := badgeOverallStatuses[status]; ok && overall {
		badge[0] = message
	}
	return badge[0], badge[1]
}

// uptimeBadge returns the message and color of an uptime badge: the average
// uptime of the components with data in the period.
func uptimeBadge(uptimes []uptime.Uptime, period string) (string, string) {
	var sum float64
	var count int
	for _, u := range uptimes {
		value := u.D1
		switch period {
		case "7d":
			value = u.D7
		case "30d":
			value = u.D30
		}
		if value != nil {
			sum += *value
			count++
		}
	}
	if count == 0 {
		return "no data", badgeGrey
	}
	pct := sum / float64(count)
	message := strconv.FormatFloat(float64(int64(pct*100))/100, 'f', -1, 64) + "% " + period
	switch {
	case pct >= 99.9:
		return message, badgeGreen
	case pct >= 99:
		return message, badgeYellowGreen
	case pct >= 95:
		return message, badgeYellow
	case pct >= 90:
		return message, badgeOrange
	}
	return message, badgeRed
}

// publicStatusPageData returns the public data of a public page, from the
// response cache of handleStatusPage when possible.
func (h *Hub) publicStatusPageData(app core.App, page *core.Record) (*publicStatusPage, error) {
	slug := page.GetString("slug")
	if body, ok := h.statusPages.cache.GetOk(slug); ok {
		var data publicStatusPage
		if err := json.Unmarshal([]byte(body), &data); err == nil {
			return &data, nil
		}
	}
	data, err := buildStatusPage(app, page, h.statusPages.now())
	if err != nil {
		return nil, err
	}
	if encoded, err := json.Marshal(data); err == nil {
		h.statusPages.cache.Set(slug, string(encoded), statusPageCacheTTL)
	}
	return data, nil
}

func writeBadge(e *core.RequestEvent, svg string) error {
	header := e.Response.Header()
	header.Set("Content-Type", "image/svg+xml; charset=utf-8")
	header.Set("Cache-Control", "public, max-age=60")
	header.Set("Content-Security-Policy", statusPageLogoCSP)
	header.Set("X-Content-Type-Options", "nosniff")
	e.Response.WriteHeader(http.StatusOK)
	_, err := e.Response.Write([]byte(svg))
	return err
}

// textWidth estimates the width in pixels of text in 11px Verdana.
func textWidth(text string) int {
	var width float64
	for _, r := range text {
		switch {
		case strings.ContainsRune("iljI.,:;!'|", r):
			width += 3.7
		case strings.ContainsRune("frt ()[]{}-/", r):
			width += 4.8
		case strings.ContainsRune("mwMW", r):
			width += 10.5
		case r >= 'A' && r <= 'Z':
			width += 7.6
		case r >= '0' && r <= '9', r == '%':
			width += 7
		case r < 128:
			width += 6.6
		default:
			width += 8
		}
	}
	return int(width + 0.5)
}

// escapeXML returns text escaped for XML character data and attributes.
func escapeXML(text string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
}

// renderBadge returns a flat, shields.io style SVG badge.
func renderBadge(label, message, color string) string {
	labelWidth, messageWidth := textWidth(label)+10, textWidth(message)+10
	if label == "" {
		labelWidth = 0
	}
	width := labelWidth + messageWidth
	w := strconv.Itoa
	title := escapeXML(message)
	if label != "" {
		title = escapeXML(label + ": " + message)
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="` + w(width) + `" height="20" role="img" aria-label="` + title + `">`)
	b.WriteString(`<title>` + title + `</title>`)
	b.WriteString(`<linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`)
	b.WriteString(`<clipPath id="r"><rect width="` + w(width) + `" height="20" rx="3" fill="#fff"/></clipPath>`)
	b.WriteString(`<g clip-path="url(#r)">`)
	if labelWidth > 0 {
		b.WriteString(`<rect width="` + w(labelWidth) + `" height="20" fill="` + badgeLabelColor + `"/>`)
	}
	b.WriteString(`<rect x="` + w(labelWidth) + `" width="` + w(messageWidth) + `" height="20" fill="` + color + `"/>`)
	b.WriteString(`<rect width="` + w(width) + `" height="20" fill="url(#s)"/></g>`)
	b.WriteString(`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`)
	text := func(x int, value string) {
		escaped := escapeXML(value)
		b.WriteString(`<text x="` + w(x) + `" y="15" fill="#010101" fill-opacity=".3">` + escaped + `</text>`)
		b.WriteString(`<text x="` + w(x) + `" y="14">` + escaped + `</text>`)
	}
	if labelWidth > 0 {
		text(labelWidth/2, label)
	}
	text(labelWidth+messageWidth/2, message)
	b.WriteString(`</g></svg>`)
	return b.String()
}
