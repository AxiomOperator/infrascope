package hub

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/hub/expirymap"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/search"
)

const (
	// statusPageCacheTTL is how long a public status page response is cached.
	statusPageCacheTTL = 30 * time.Second
	// statusPageRateLimit is how many status page requests a client IP may send per statusPageRateWindow.
	statusPageRateLimit  = 60
	statusPageRateWindow = time.Minute
	// statusPageDays is the number of daily uptime buckets of each monitor.
	statusPageDays = 90
	// statusPageMaintenanceHorizon is how far ahead scheduled maintenance is listed.
	statusPageMaintenanceHorizon = 7 * 24 * time.Hour
)

// Day states of PublicStatusPageDay.
const (
	dayUp    = "up"
	dayDown  = "down"
	dayMaint = "maint"
	dayNone  = "none"
)

// Overall states of a status page.
const overallDegraded = "degraded"

// statusPages serves public status pages with a per-slug response cache and
// a per-client-IP rate limit.
type statusPages struct {
	cache  *expirymap.ExpiryMap[string]
	limits *windowLimiter
	now    func() time.Time
}

func newStatusPages() *statusPages {
	return &statusPages{
		cache:  expirymap.New[string](time.Minute),
		limits: newWindowLimiter(statusPageRateLimit, statusPageRateWindow),
		now:    time.Now,
	}
}

// publicStatusPage is the response of GET /api/beszel/status-pages/{slug}.
// It must never contain record ids, system data, errors or credentials.
type publicStatusPage struct {
	Title             string                    `json:"title"`
	Description       string                    `json:"description"`
	Updated           int64                     `json:"updated"`
	Overall           string                    `json:"overall"`
	ShowResponseTimes bool                      `json:"showResponseTimes"`
	Monitors          []publicStatusMonitor     `json:"monitors"`
	Maintenance       []publicStatusMaintenance `json:"maintenance"`
}

type publicStatusMonitor struct {
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Uptime uptime.Uptime     `json:"uptime"`
	Days   []publicStatusDay `json:"days"`
	// Res is the current response time in milliseconds.
	Res    float64 `json:"res,omitempty"`
	Target string  `json:"target,omitempty"`
}

type publicStatusDay struct {
	// D is the UTC date, YYYY-MM-DD.
	D  string   `json:"d"`
	Up *float64 `json:"up"`
	St string   `json:"st"`
}

type publicStatusMaintenance struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Active      bool   `json:"active"`
}

// handleStatusPage serves a status page. Pages that are not public are only
// served to their owner and superusers (as previews) and are never cached;
// to everyone else they do not exist.
//
// Requests are limited per client IP as reported by e.RealIP (see handlePush
// for the reverse proxy settings).
func (h *Hub) handleStatusPage(e *core.RequestEvent) error {
	if ok, retryAfter := h.statusPages.limits.allow(e.RealIP()); !ok {
		seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
		e.Response.Header().Set("Retry-After", strconv.Itoa(seconds))
		return e.TooManyRequestsError("Too many requests.", nil)
	}
	slug := e.Request.PathValue("slug")
	if body, ok := h.statusPages.cache.GetOk(slug); ok {
		return writeStatusPage(e, body, true)
	}

	page, err := e.App.FindFirstRecordByData("status_pages", "slug", slug)
	if err != nil {
		return statusPageNotFound(e)
	}
	public := page.GetBool("public")
	if !public && !canPreviewStatusPage(e, page) {
		return statusPageNotFound(e)
	}
	data, err := buildStatusPage(e.App, page, h.statusPages.now())
	if err != nil {
		return e.InternalServerError("", err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return e.InternalServerError("", err)
	}
	body := string(encoded)
	if public {
		h.statusPages.cache.Set(slug, body, statusPageCacheTTL)
	}
	return writeStatusPage(e, body, public)
}

func statusPageNotFound(e *core.RequestEvent) error {
	return e.NotFoundError("Status page not found.", nil)
}

func writeStatusPage(e *core.RequestEvent, body string, public bool) error {
	if public {
		e.Response.Header().Set("Cache-Control", "public, max-age=30")
	} else {
		e.Response.Header().Set("Cache-Control", "private, no-store")
	}
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(http.StatusOK)
	_, err := e.Response.Write([]byte(body))
	return err
}

// canPreviewStatusPage reports whether the requester owns the page or is a superuser.
func canPreviewStatusPage(e *core.RequestEvent, page *core.Record) bool {
	if e.Auth == nil {
		return false
	}
	return e.HasSuperuserAuth() || (e.Auth.Collection().Name == "users" && e.Auth.Id == page.GetString("user"))
}

// buildStatusPage builds the public data of a status page at now.
func buildStatusPage(app core.App, page *core.Record, now time.Time) (*publicStatusPage, error) {
	monitors, err := statusPageMonitors(app, page)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(monitors))
	for i, record := range monitors {
		ids[i] = record.Id
	}
	days, err := dailyUptime(app, ids, now)
	if err != nil {
		return nil, err
	}
	maintenance, err := statusPageMaintenance(app, page.GetString("user"), ids, now)
	if err != nil {
		return nil, err
	}

	showTargets := page.GetBool("showTargets")
	showResponseTimes := page.GetBool("showResponseTimes")
	result := &publicStatusPage{
		Title:             page.GetString("title"),
		Description:       page.GetString("description"),
		Updated:           now.UnixMilli(),
		ShowResponseTimes: showResponseTimes,
		Monitors:          make([]publicStatusMonitor, 0, len(monitors)),
		Maintenance:       maintenance,
	}
	for i, record := range monitors {
		status := record.GetString("status")
		if !record.GetBool("enabled") {
			status = uptime.StatusPaused
		} else if status == "" {
			status = uptime.StatusUnknown
		}
		item := publicStatusMonitor{
			Name:   record.GetString("name"),
			Status: status,
			Days:   days[record.Id],
		}
		target := record.GetString("target")
		if item.Name == "" {
			if showTargets && target != "" {
				item.Name = target
			} else {
				item.Name = "Monitor " + strconv.Itoa(i+1)
			}
		}
		if showTargets {
			item.Target = target
		}
		_ = record.UnmarshalJSONField("uptime", &item.Uptime)
		// res is stored in microseconds.
		if res := record.GetFloat("res"); showResponseTimes && res > 0 && status != uptime.StatusDown && status != uptime.StatusPaused {
			item.Res = math.Round(res/10) / 100
		}
		result.Monitors = append(result.Monitors, item)
	}
	result.Overall = overallStatus(result.Monitors)
	return result, nil
}

// statusPageMonitors returns the monitors of a page, in page order, that
// still exist and that the page owner can still view.
func statusPageMonitors(app core.App, page *core.Record) ([]*core.Record, error) {
	ids := page.GetStringSlice("monitors")
	if len(ids) == 0 {
		return nil, nil
	}
	owner, err := app.FindRecordById("users", page.GetString("user"))
	if err != nil {
		return nil, nil
	}
	collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
	if err != nil {
		return nil, err
	}
	if collection.ViewRule == nil {
		return nil, nil
	}
	query := app.RecordQuery(collection).AndWhere(dbx.In(collection.Name+".id", toAny(ids)...))
	if *collection.ViewRule != "" {
		// Evaluate the view rule for the owner, as a request of the owner would.
		info := &core.RequestInfo{
			Context: core.RequestInfoContextDefault,
			Method:  http.MethodGet,
			Auth:    owner,
			Query:   map[string]string{},
			Headers: map[string]string{},
			Body:    map[string]any{},
		}
		resolver := core.NewRecordFieldResolver(app, collection, info, true)
		expr, err := search.FilterData(*collection.ViewRule).BuildExpr(resolver)
		if err != nil {
			return nil, err
		}
		if err := resolver.UpdateQuery(query); err != nil {
			return nil, err
		}
		query.AndWhere(expr)
	}
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}
	byID := make(map[string]*core.Record, len(records))
	for _, record := range records {
		byID[record.Id] = record
	}
	ordered := make([]*core.Record, 0, len(records))
	for _, id := range ids {
		if record, ok := byID[id]; ok {
			ordered = append(ordered, record)
			delete(byID, id)
		}
	}
	return ordered, nil
}

func toAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

// dailyUptime returns statusPageDays daily buckets per monitor, for the UTC
// days ending with the day of now, oldest first. Up and down time form the
// denominator of a day's uptime; maintenance, unknown and paused time do not.
func dailyUptime(app core.App, monitorIDs []string, now time.Time) (map[string][]publicStatusDay, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	first := today.AddDate(0, 0, -(statusPageDays - 1))
	since, nowMs := first.UnixMilli(), now.UnixMilli()
	const dayMs = int64(24 * time.Hour / time.Millisecond)

	type totals struct{ up, down, maint int64 }
	perMonitor := make(map[string]*[statusPageDays]totals, len(monitorIDs))
	for _, id := range monitorIDs {
		perMonitor[id] = &[statusPageDays]totals{}
	}
	if len(monitorIDs) > 0 {
		var rows []struct {
			Monitor string `db:"monitor"`
			Status  string `db:"status"`
			Start   int64  `db:"start"`
			End     int64  `db:"end"`
		}
		err := app.DB().Select("monitor", "status", "start", "end").From("monitor_events").
			Where(dbx.In("monitor", toAny(monitorIDs)...)).
			AndWhere(dbx.NewExp("([[end]] = 0 OR [[end]] > {:since}) AND [[start]] < {:now}", dbx.Params{"since": since, "now": nowMs})).
			All(&rows)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			days := perMonitor[row.Monitor]
			if days == nil {
				continue
			}
			start, end := max(row.Start, since), row.End
			if end == 0 || end > nowMs {
				end = nowMs
			}
			for day := (start - since) / dayMs; day < statusPageDays && start < end; day++ {
				dayEnd := since + (day+1)*dayMs
				overlap := min(end, dayEnd) - start
				switch row.Status {
				case uptime.StatusUp:
					days[day].up += overlap
				case uptime.StatusDown:
					days[day].down += overlap
				case uptime.StatusMaintenance:
					days[day].maint += overlap
				}
				start = dayEnd
			}
		}
	}

	result := make(map[string][]publicStatusDay, len(monitorIDs))
	for id, days := range perMonitor {
		buckets := make([]publicStatusDay, statusPageDays)
		for i, t := range days {
			bucket := publicStatusDay{D: first.AddDate(0, 0, i).Format(time.DateOnly), St: dayNone}
			if total := t.up + t.down; total > 0 {
				pct := math.Round(float64(t.up)/float64(total)*100_000) / 1000
				bucket.Up = &pct
			}
			switch {
			case t.down > 0:
				bucket.St = dayDown
			case t.maint > 0:
				bucket.St = dayMaint
			case t.up > 0:
				bucket.St = dayUp
			}
			buckets[i] = bucket
		}
		result[id] = buckets
	}
	return result, nil
}

// overallStatus summarizes the monitor statuses of a page.
func overallStatus(monitors []publicStatusMonitor) string {
	var active, up, down, pending, maintenance int
	for _, m := range monitors {
		switch m.Status {
		case uptime.StatusPaused:
			continue
		case uptime.StatusUp:
			up++
		case uptime.StatusDown:
			down++
		case uptime.StatusPending:
			pending++
		case uptime.StatusMaintenance:
			maintenance++
		}
		active++
	}
	switch {
	case maintenance > 0 && down == 0:
		return uptime.StatusMaintenance
	case down > 0 && down == active:
		return uptime.StatusDown
	case down > 0 || pending > 0:
		return overallDegraded
	case active > 0 && up == active:
		return uptime.StatusUp
	}
	return uptime.StatusUnknown
}

// statusPageMaintenance returns the page owner's maintenance windows shown on
// status pages that list any of the monitors and are active at now or start
// within statusPageMaintenanceHorizon, ordered by start. Windows created by
// other users of a shared monitor are left out so their notes aren't published.
func statusPageMaintenance(app core.App, ownerID string, monitorIDs []string, now time.Time) ([]publicStatusMaintenance, error) {
	result := []publicStatusMaintenance{}
	if len(monitorIDs) == 0 {
		return result, nil
	}
	records, err := app.FindAllRecords(maintenanceCollection, dbx.HashExp{"showOnStatusPages": true, "user": ownerID})
	if err != nil {
		return nil, err
	}
	type window struct {
		item       publicStatusMaintenance
		start, end time.Time
	}
	var windows []window
	for _, record := range records {
		if !slices.ContainsFunc(record.GetStringSlice("monitors"), func(id string) bool { return slices.Contains(monitorIDs, id) }) {
			continue
		}
		windowType := record.GetString("type")
		start, end, ok := nextOccurrence(windowType,
			record.GetDateTime("start").Time(), record.GetDateTime("end").Time(), now)
		if !ok || start.After(now.Add(statusPageMaintenanceHorizon)) {
			continue
		}
		windows = append(windows, window{
			item: publicStatusMaintenance{
				Title:       record.GetString("title"),
				Description: record.GetString("description"),
				Start:       formatISO(start),
				End:         formatISO(end),
				Active:      !now.Before(start) && now.Before(end),
			},
			start: start, end: end,
		})
	}
	slices.SortStableFunc(windows, func(a, b window) int { return a.start.Compare(b.start) })
	for _, w := range windows {
		result = append(result, w.item)
	}
	return result, nil
}

// nextOccurrence returns the occurrence of a maintenance window that is
// active at now, or else the next one. ok is false when the window has no
// current or future occurrence. Daily windows follow alerts.WindowActive:
// only the UTC times of day (in minutes) of start and end count, and an end
// before the start crosses midnight.
func nextOccurrence(windowType string, start, end, now time.Time) (time.Time, time.Time, bool) {
	if windowType != alerts.WindowDaily {
		return start, end, end.After(now) && end.After(start)
	}
	startMinutes, endMinutes := minuteOfDayUTC(start), minuteOfDayUTC(end)
	if startMinutes == endMinutes {
		return time.Time{}, time.Time{}, false
	}
	length := time.Duration(endMinutes-startMinutes) * time.Minute
	if length < 0 {
		length += 24 * time.Hour
	}
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for offset := -1; offset <= 1; offset++ {
		occurrence := midnight.AddDate(0, 0, offset).Add(time.Duration(startMinutes) * time.Minute)
		if occurrence.Add(length).After(now) {
			return occurrence, occurrence.Add(length), true
		}
	}
	return time.Time{}, time.Time{}, false
}

func minuteOfDayUTC(t time.Time) int {
	hour, minute, _ := t.UTC().Clock()
	return hour*60 + minute
}

func formatISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
