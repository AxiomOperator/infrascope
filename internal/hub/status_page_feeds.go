package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// statusPageFeedDays is how many days resolved incidents stay in feeds.
	statusPageFeedDays = 30
	// statusPageFeedCacheTTL is how long a feed is cached.
	statusPageFeedCacheTTL = time.Minute
	// maxStatusPageFeedEntries is the maximum number of entries of a feed.
	maxStatusPageFeedEntries = 100
)

// incidentStatusTitles are the display names of incident statuses in feeds.
var incidentStatusTitles = map[string]string{
	"investigating": "Investigating",
	"identified":    "Identified",
	"monitoring":    "Monitoring",
	"resolved":      "Resolved",
}

// feedEntry is an incident update (or an incident without updates) in a feed.
type feedEntry struct {
	id      string
	title   string
	message string
	time    time.Time
}

type atomFeed struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle,omitempty"`
	ID       string      `xml:"id"`
	Updated  string      `xml:"updated"`
	Author   atomAuthor  `xml:"author"`
	Links    []atomLink  `xml:"link"`
	Entries  []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title     string   `xml:"title"`
	ID        string   `xml:"id"`
	Updated   string   `xml:"updated"`
	Published string   `xml:"published"`
	Link      atomLink `xml:"link"`
	Content   atomText `xml:"content"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	AtomNS  string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	AtomLink      atomLink  `xml:"atom:link"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Items         []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description string  `xml:"description"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// handleStatusPageFeed serves the incidents of a public status page (active
// ones and those resolved within statusPageFeedDays) as an Atom (feed.atom)
// or RSS 2.0 (feed.rss) feed with an entry per incident update.
func (h *Hub) handleStatusPageFeed(e *core.RequestEvent) error {
	if err := h.limitStatusPage(e); err != nil {
		return err
	}
	slug := e.Request.PathValue("slug")
	format := "atom"
	if strings.HasSuffix(e.Request.URL.Path, ".rss") {
		format = "rss"
	}
	base := statusPageBaseURL(h, e, slug)
	key := strings.Join([]string{"feed", slug, format, base}, "|")
	body, ok := h.statusPages.cache.GetOk(key)
	if !ok {
		page, err := findPublicStatusPage(e, slug)
		if err != nil {
			return err
		}
		now := h.statusPages.now()
		incidents, err := statusPageIncidentsSince(e.App, page, now, statusPageFeedDays)
		if err != nil {
			return e.InternalServerError("", err)
		}
		entries := feedEntries(page, slices.Concat(incidents.Active, incidents.Recent))
		body, err = renderFeed(format, page, base, entries, now)
		if err != nil {
			return e.InternalServerError("", err)
		}
		h.statusPages.cache.Set(key, body, statusPageFeedCacheTTL)
	}
	contentType := "application/atom+xml; charset=utf-8"
	if format == "rss" {
		contentType = "application/rss+xml; charset=utf-8"
	}
	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Cache-Control", "public, max-age=60")
	e.Response.Header().Set("X-Content-Type-Options", "nosniff")
	e.Response.WriteHeader(http.StatusOK)
	_, err := e.Response.Write([]byte(body))
	return err
}

// feedEntries returns the entries of incidents, newest first. Entry ids are
// derived from record ids with a hash, so ids are not published.
func feedEntries(page *core.Record, incidents []publicStatusIncident) []feedEntry {
	var entries []feedEntry
	for _, incident := range incidents {
		if len(incident.Updates) == 0 {
			started, _ := time.Parse(time.RFC3339, incident.StartedAt)
			entries = append(entries, feedEntry{
				id:    feedID(page.Id, incident.id),
				title: incident.Title,
				time:  started,
			})
			continue
		}
		for _, update := range incident.Updates {
			created, _ := time.Parse(time.RFC3339, update.Created)
			entries = append(entries, feedEntry{
				id:      feedID(page.Id, incident.id, update.Created, update.Status),
				title:   incident.Title + " - " + incidentStatusTitles[update.Status],
				message: update.Message,
				time:    created,
			})
		}
	}
	slices.SortStableFunc(entries, func(a, b feedEntry) int { return b.time.Compare(a.time) })
	return entries[:min(len(entries), maxStatusPageFeedEntries)]
}

// feedID returns a stable, opaque URN for the parts.
func feedID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "urn:infrascope:" + hex.EncodeToString(sum[:16])
}

// renderFeed renders the entries as an Atom or RSS feed.
func renderFeed(format string, page *core.Record, base string, entries []feedEntry, now time.Time) (string, error) {
	slug := page.GetString("slug")
	pageURL := base
	if !strings.HasSuffix(base, "/status/"+slug) {
		pageURL = strings.TrimSuffix(base, "/") + "/"
	}
	selfURL := statusPageAPIURL(base, slug) + "/feed." + format
	title := page.GetString("title")
	updated := now
	if len(entries) > 0 {
		updated = entries[0].time
	}
	var value any
	if format == "rss" {
		channel := rssChannel{
			Title:         title,
			Link:          pageURL,
			Description:   "Incidents of " + title,
			AtomLink:      atomLink{Href: selfURL, Rel: "self", Type: "application/rss+xml"},
			LastBuildDate: updated.UTC().Format(time.RFC1123Z),
		}
		for _, entry := range entries {
			description := entry.message
			if description == "" {
				description = entry.title
			}
			channel.Items = append(channel.Items, rssItem{
				Title:       entry.title,
				Link:        pageURL,
				Description: description,
				GUID:        rssGUID{IsPermaLink: "false", Value: entry.id},
				PubDate:     entry.time.UTC().Format(time.RFC1123Z),
			})
		}
		value = rssFeed{Version: "2.0", AtomNS: "http://www.w3.org/2005/Atom", Channel: channel}
	} else {
		feed := atomFeed{
			Title:    title,
			Subtitle: "Incidents of " + title,
			ID:       feedID(page.Id),
			Updated:  updated.UTC().Format(time.RFC3339),
			Author:   atomAuthor{Name: title},
			Links: []atomLink{
				{Href: pageURL, Rel: "alternate", Type: "text/html"},
				{Href: selfURL, Rel: "self", Type: "application/atom+xml"},
			},
		}
		for _, entry := range entries {
			stamp := entry.time.UTC().Format(time.RFC3339)
			feed.Entries = append(feed.Entries, atomEntry{
				Title:     entry.title,
				ID:        entry.id,
				Updated:   stamp,
				Published: stamp,
				Link:      atomLink{Href: pageURL, Rel: "alternate", Type: "text/html"},
				Content:   atomText{Type: "text", Body: entry.message},
			})
		}
		value = feed
	}
	encoded, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return xml.Header + string(encoded), nil
}

// statusPageBaseURL returns the absolute URL of a status page: the root of
// its custom domain when requested there, else /status/{slug} below the
// hub's URL (APP_URL, or the URL of the request).
func statusPageBaseURL(h *Hub, e *core.RequestEvent, slug string) string {
	if statusPageHostSlug(e) == slug {
		return "https://" + normalizeHost(e.Request.Host) + "/"
	}
	base := h.appURL
	if base == "" {
		base = h.Settings().Meta.AppURL
	}
	if u, err := url.Parse(base); base == "" || err != nil || u.Host == "" {
		scheme := "http"
		if e.Request.TLS != nil || strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + e.Request.Host
	}
	return strings.TrimSuffix(base, "/") + "/status/" + slug
}

// statusPageAPIURL returns the absolute URL of the API of a status page for
// its base URL (see statusPageBaseURL).
func statusPageAPIURL(base, slug string) string {
	root := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/status/"+slug)
	return root + "/api/beszel/status-pages/" + slug
}
