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
	"github.com/henrygd/beszel/internal/hub/systemevents"
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
	Systems           []publicStatusSystem      `json:"systems"`
	Monitors          []publicStatusMonitor     `json:"monitors"`
	Maintenance       []publicStatusMaintenance `json:"maintenance"`
	Incidents         publicStatusIncidents     `json:"incidents"`
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

// publicStatusSystem is a system of a status page. Only the name is
// published: never the host, port, ids or agent details.
type publicStatusSystem struct {
	Name string `json:"name"`
	// Status is "up", "down", "paused", "pending" or "unknown".
	Status string            `json:"status"`
	Uptime uptime.Uptime     `json:"uptime"`
	Days   []publicStatusDay `json:"days"`
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
	monitors, err := viewableByOwner(app, page, "network_monitors", "monitors")
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
	systems, err := statusPageSystems(app, page, now)
	if err != nil {
		return nil, err
	}
	maintenance, err := statusPageMaintenance(app, page.GetString("user"), ids, now)
	if err != nil {
		return nil, err
	}
	incidents, err := statusPageIncidents(app, page, now)
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
		Systems:           systems,
		Monitors:          make([]publicStatusMonitor, 0, len(monitors)),
		Maintenance:       maintenance,
		Incidents:         incidents,
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
	statuses := make([]string, 0, len(result.Systems)+len(result.Monitors))
	for _, system := range result.Systems {
		// A pending system is waiting on its first connection result (after
		// it was added, resumed or edited), not failing, so like a paused
		// system it does not count.
		if system.Status != uptime.StatusPending {
			statuses = append(statuses, system.Status)
		}
	}
	for _, monitor := range result.Monitors {
		statuses = append(statuses, monitor.Status)
	}
	// Active incidents with major or critical impact make the page at least
	// degraded, even while its components are up (see overallWithIncidents).
	result.Overall = overallWithIncidents(overallStatus(statuses), incidents.Active)
	return result, nil
}

// statusPageSystems returns the public data of the systems of a page, in
// page order, that still exist and that the page owner can still view.
//
// Uptime and daily buckets come from system_events: up and down time form
// the denominator, while paused and pending time (a system waiting on its
// first connection result after it was added, resumed or edited) do not.
func statusPageSystems(app core.App, page *core.Record, now time.Time) ([]publicStatusSystem, error) {
	records, err := viewableByOwner(app, page, "systems", "systems")
	if err != nil {
		return nil, err
	}
	result := make([]publicStatusSystem, 0, len(records))
	if len(records) == 0 {
		return result, nil
	}
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.Id
	}
	first, since, nowMs := statusPageRange(now)
	segments, err := systemevents.Load(app, ids, since, nowMs)
	if err != nil {
		return nil, err
	}
	bySystem := make(map[string][]uptime.Segment, len(ids))
	rows := make([]statusSegment, 0, len(segments))
	for _, segment := range segments {
		bySystem[segment.System] = append(bySystem[segment.System], uptime.Segment{Status: segment.Status, Start: segment.Start, End: segment.End})
		rows = append(rows, statusSegment{Owner: segment.System, Status: segment.Status, Start: segment.Start, End: segment.End})
	}
	days := dailyBuckets(rows, ids, first, nowMs)
	for i, record := range records {
		status := record.GetString("status")
		switch status {
		case uptime.StatusUp, uptime.StatusDown, uptime.StatusPaused, uptime.StatusPending:
		default:
			status = uptime.StatusUnknown
		}
		name := record.GetString("name")
		if name == "" {
			name = "Server " + strconv.Itoa(i+1)
		}
		result = append(result, publicStatusSystem{
			Name:   name,
			Status: status,
			Uptime: uptime.UptimeFromSegments(bySystem[record.Id], now),
			Days:   days[record.Id],
		})
	}
	return result, nil
}

// viewableByOwner returns the records of collectionName referenced by the
// relation field of a page, in page order, that still exist and that the page
// owner can still view.
func viewableByOwner(app core.App, page *core.Record, collectionName, field string) ([]*core.Record, error) {
	ids := page.GetStringSlice(field)
	if len(ids) == 0 {
		return nil, nil
	}
	owner, err := app.FindRecordById("users", page.GetString("user"))
	if err != nil {
		return nil, nil
	}
	collection, err := app.FindCachedCollectionByNameOrId(collectionName)
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

// statusPageRange returns the first UTC day of the daily buckets at now and
// the covered range [since, nowMs) in Unix milliseconds.
func statusPageRange(now time.Time) (first time.Time, since, nowMs int64) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	first = today.AddDate(0, 0, -(statusPageDays - 1))
	return first, first.UnixMilli(), now.UnixMilli()
}

// statusSegment is a status period of a monitor or system (the owner).
type statusSegment struct {
	Owner  string `db:"owner"`
	Status string `db:"status"`
	Start  int64  `db:"start"`
	End    int64  `db:"end"`
}

// dailyUptime returns statusPageDays daily buckets per monitor, for the UTC
// days ending with the day of now, oldest first. Up and down time form the
// denominator of a day's uptime; maintenance, unknown and paused time do not.
func dailyUptime(app core.App, monitorIDs []string, now time.Time) (map[string][]publicStatusDay, error) {
	first, since, nowMs := statusPageRange(now)
	var rows []statusSegment
	if len(monitorIDs) > 0 {
		err := app.DB().Select("monitor AS owner", "status", "start", "end").From("monitor_events").
			Where(dbx.In("monitor", toAny(monitorIDs)...)).
			AndWhere(dbx.NewExp("([[end]] = 0 OR [[end]] > {:since}) AND [[start]] < {:now}", dbx.Params{"since": since, "now": nowMs})).
			All(&rows)
		if err != nil {
			return nil, err
		}
	}
	return dailyBuckets(rows, monitorIDs, first, nowMs), nil
}

// dailyBuckets returns statusPageDays daily buckets per owner from first
// (UTC midnight) to nowMs. Up and down time form the denominator of a day's
// uptime; other statuses do not, and only maintenance marks a day.
func dailyBuckets(rows []statusSegment, ids []string, first time.Time, nowMs int64) map[string][]publicStatusDay {
	since := first.UnixMilli()
	const dayMs = int64(24 * time.Hour / time.Millisecond)

	type totals struct{ up, down, maint int64 }
	perOwner := make(map[string]*[statusPageDays]totals, len(ids))
	for _, id := range ids {
		perOwner[id] = &[statusPageDays]totals{}
	}
	for _, row := range rows {
		days := perOwner[row.Owner]
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

	result := make(map[string][]publicStatusDay, len(ids))
	for id, days := range perOwner {
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
	return result
}

// overallStatus summarizes the system and monitor statuses of a page.
func overallStatus(statuses []string) string {
	var active, up, down, pending, maintenance int
	for _, status := range statuses {
		switch status {
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

// bindStatusPageHooks rejects status pages that list systems the requester
// (the page owner, see the collection rules) cannot view. Monitors are
// checked by the network monitor hooks.
func bindStatusPageHooks(app core.App) {
	checkSystems := func(e *core.RecordRequestEvent) error {
		if err := checkReferencedSystems(e); err != nil {
			return err
		}
		return e.Next()
	}
	app.OnRecordCreateRequest("status_pages").BindFunc(checkSystems)
	app.OnRecordUpdateRequest("status_pages").BindFunc(checkSystems)
}

// checkReferencedSystems returns an error unless the requester can view every
// system of the submitted status page.
func checkReferencedSystems(e *core.RecordRequestEvent) error {
	if e.HasSuperuserAuth() {
		return nil
	}
	ids := e.Record.GetStringSlice("systems")
	if len(ids) == 0 {
		return nil
	}
	collection, err := e.App.FindCachedCollectionByNameOrId("systems")
	if err != nil {
		return err
	}
	info, err := e.RequestInfo()
	if err != nil {
		return err
	}
	records, err := e.App.FindRecordsByIds(collection, ids)
	if err != nil {
		return err
	}
	if len(records) != len(ids) {
		return e.BadRequestError("You do not have access to all selected systems", nil)
	}
	for _, record := range records {
		if ok, err := e.App.CanAccessRecord(record, info, collection.ViewRule); err != nil || !ok {
			return e.BadRequestError("You do not have access to all selected systems", err)
		}
	}
	return nil
}
