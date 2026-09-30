package hub

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Incidents
//
// An incident (incidents) belongs to a user and reports an outage of some
// monitors and systems on the status pages it lists (statusPages). Its
// timeline is a list of incident_updates; creating an update also sets the
// incident's status. resolvedAt is server-managed: set when the status
// becomes resolved and cleared when it is reopened.
//
// Status pages with autoIncidents open an incident automatically (auto) when
// one of their monitors or systems goes down: a monitor on a confirmed down
// transition of the uptime engine (which already holds back transitions
// during maintenance and behind a down parent), a system when its status
// becomes down while no monitor it depends on is down, or when such a parent
// recovers and the system is still down. There is one open automatic incident
// per page owner and component (autoKey); it is resolved with an update when
// the component recovers. Manual incidents are never changed automatically.
const (
	incidentsCollection       = "incidents"
	incidentUpdatesCollection = "incident_updates"

	incidentResolved      = "resolved"
	incidentInvestigating = "investigating"

	impactMinor    = "minor"
	impactMajor    = "major"
	impactCritical = "critical"

	// incidentRecentDays is how long resolved incidents stay on status pages.
	incidentRecentDays = 14
	// Limits of the incidents and updates published on a status page.
	maxPublicIncidents       = 20
	maxPublicIncidentUpdates = 50
)

// bindIncidentHooks validates incident requests and keeps the server-managed
// incident fields current.
func bindIncidentHooks(app core.App) {
	checkReferences := func(e *core.RecordRequestEvent) error {
		// Only the server opens automatic incidents.
		if original := e.Record.Original(); original != nil && !original.IsNew() {
			e.Record.Set("auto", original.GetBool("auto"))
			e.Record.Set("autoKey", original.GetString("autoKey"))
		} else {
			e.Record.Set("auto", false)
			e.Record.Set("autoKey", "")
		}
		if err := checkReferencedMonitors(e); err != nil {
			return err
		}
		if err := checkReferencedSystems(e); err != nil {
			return err
		}
		if err := checkReferencedStatusPages(e); err != nil {
			return err
		}
		return e.Next()
	}
	app.OnRecordCreateRequest(incidentsCollection).BindFunc(checkReferences)
	app.OnRecordUpdateRequest(incidentsCollection).BindFunc(checkReferences)

	// Model-level, so automatic incidents and all updates follow the same rules.
	setDates := func(e *core.RecordEvent) error {
		prepareIncident(e.Record, time.Now())
		return e.Next()
	}
	app.OnRecordCreate(incidentsCollection).BindFunc(setDates)
	app.OnRecordUpdate(incidentsCollection).BindFunc(setDates)

	app.OnRecordCreateRequest(incidentUpdatesCollection).BindFunc(func(e *core.RecordRequestEvent) error {
		author := ""
		if e.Auth != nil && e.Auth.Collection().Name == "users" {
			author = e.Auth.Id
		}
		e.Record.Set("author", author)
		return e.Next()
	})
	app.OnRecordCreate(incidentUpdatesCollection).BindFunc(func(e *core.RecordEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		incident, err := e.App.FindRecordById(incidentsCollection, e.Record.GetString("incident"))
		if err != nil {
			return err
		}
		incident.Set("status", e.Record.GetString("status"))
		return e.App.Save(incident)
	})
}

// prepareIncident sets startedAt (when missing) and resolvedAt from the status.
func prepareIncident(record *core.Record, now time.Time) {
	if record.GetDateTime("startedAt").IsZero() {
		record.Set("startedAt", now)
	}
	if record.GetString("status") != incidentResolved {
		record.Set("resolvedAt", "")
	} else if record.GetDateTime("resolvedAt").IsZero() {
		record.Set("resolvedAt", now)
	}
}

// checkReferencedStatusPages returns an error unless the requester owns every
// status page of the submitted incident.
func checkReferencedStatusPages(e *core.RecordRequestEvent) error {
	if e.HasSuperuserAuth() {
		return nil
	}
	ids := e.Record.GetStringSlice("statusPages")
	if len(ids) == 0 {
		return nil
	}
	records, err := e.App.FindRecordsByIds("status_pages", ids)
	if err != nil {
		return err
	}
	if len(records) != len(ids) {
		return e.BadRequestError("You do not have access to all selected status pages", nil)
	}
	for _, record := range records {
		if e.Auth == nil || record.GetString("user") != e.Auth.Id {
			return e.BadRequestError("You do not have access to all selected status pages", nil)
		}
	}
	return nil
}

// incidentComponent is a monitor or system that automatic incidents follow.
type incidentComponent struct {
	// field is the relation field of status pages and incidents, "monitors" or "systems".
	field string
	id    string
}

func (c incidentComponent) collection() string {
	if c.field == "systems" {
		return "systems"
	}
	return "network_monitors"
}

// key is the autoKey of the component's automatic incidents.
func (c incidentComponent) key() string {
	if c.field == "systems" {
		return "system:" + c.id
	}
	return "monitor:" + c.id
}

// handleMonitorTransitions delivers the uptime engine's status changes to
// automatic incidents, and those of monitors with the notify option to the
// alert manager.
func (h *Hub) handleMonitorTransitions(transitions []uptime.Transition) {
	notify := make([]uptime.Transition, 0, len(transitions))
	for _, transition := range transitions {
		if transition.Notify {
			notify = append(notify, transition)
		}
	}
	if len(notify) > 0 {
		h.AlertManager.HandleMonitorTransitions(notify)
	}
	h.handleIncidentTransitions(transitions)
	h.statusSubscriptions.monitorTransitions(transitions)
}

// handleIncidentTransitions opens and resolves the automatic incidents of
// monitors whose confirmed status changed. Errors are logged.
func (h *Hub) handleIncidentTransitions(transitions []uptime.Transition) {
	for _, transition := range transitions {
		component := incidentComponent{field: "monitors", id: transition.MonitorID}
		var err error
		switch transition.Status {
		case uptime.StatusDown:
			err = openAutoIncidents(h, component, transition.At)
		case uptime.StatusUp:
			err = resolveAutoIncidents(h, component)
		}
		if err != nil {
			h.Logger().Error("Failed to update automatic incidents", "monitor", transition.MonitorID, "err", err)
		}
	}
}

// bindSystemIncidentEvents opens and resolves the automatic incidents of
// systems whose status changed.
func bindSystemIncidentEvents(h *Hub) {
	h.OnRecordAfterUpdateSuccess("systems").BindFunc(func(e *core.RecordEvent) error {
		original := e.Record.Original()
		status := e.Record.GetString("status")
		if original == nil || original.GetString("status") == status {
			return e.Next()
		}
		component := incidentComponent{field: "systems", id: e.Record.Id}
		var err error
		switch status {
		case uptime.StatusDown:
			if h.systemSuppressedBy(e.Record.GetStringSlice("dependsOn")) == "" {
				err = openAutoIncidents(e.App, component, time.Now())
			}
		case uptime.StatusUp:
			err = resolveAutoIncidents(e.App, component)
		}
		if err != nil {
			e.App.Logger().Error("Failed to update automatic incidents", "system", e.Record.Id, "err", err)
		}
		return e.Next()
	})
}

// openAutoIncidents opens an automatic incident of a component that went down
// at for each owner of status pages with autoIncidents that show it, unless
// the owner already has an open automatic incident of the component.
func openAutoIncidents(app core.App, component incidentComponent, at time.Time) error {
	pages, err := app.FindAllRecords("status_pages", dbx.HashExp{"autoIncidents": true})
	if err != nil {
		return err
	}
	type ownerIncident struct {
		pages  []string
		name   string
		impact string
	}
	owners := map[string]*ownerIncident{}
	var order []string
	for _, page := range pages {
		if !slices.Contains(page.GetStringSlice(component.field), component.id) {
			continue
		}
		// Only components the owner can still view appear on the page.
		records, err := viewableByOwner(app, page, component.collection(), component.field)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(records, func(r *core.Record) bool { return r.Id == component.id })
		if index < 0 {
			continue
		}
		owner := page.GetString("user")
		item := owners[owner]
		if item == nil {
			item = &ownerIncident{name: publicComponentName(page, component, records[index], index), impact: impactMinor}
			owners[owner] = item
			order = append(order, owner)
		}
		item.pages = append(item.pages, page.Id)
		if item.impact != impactMajor {
			overall, err := componentsOverall(app, page)
			if err != nil {
				return err
			}
			if overall == uptime.StatusDown {
				item.impact = impactMajor
			}
		}
	}
	for _, owner := range order {
		item := owners[owner]
		open, err := openAutoIncident(app, owner, component.key())
		if err != nil {
			return err
		}
		if open != nil {
			continue
		}
		err = app.RunInTransaction(func(tx core.App) error {
			incidents, err := tx.FindCachedCollectionByNameOrId(incidentsCollection)
			if err != nil {
				return err
			}
			incident := core.NewRecord(incidents)
			incident.Set("user", owner)
			incident.Set("title", truncateTitle(item.name+" is down"))
			incident.Set("status", incidentInvestigating)
			incident.Set("impact", item.impact)
			incident.Set(component.field, []string{component.id})
			incident.Set("statusPages", item.pages)
			incident.Set("auto", true)
			incident.Set("autoKey", component.key())
			incident.Set("startedAt", at)
			if err := tx.Save(incident); err != nil {
				return err
			}
			return addIncidentUpdate(tx, incident.Id, incidentInvestigating,
				fmt.Sprintf("%s is down. We are investigating.", item.name))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveAutoIncidents resolves the open automatic incidents of a component
// that recovered.
func resolveAutoIncidents(app core.App, component incidentComponent) error {
	records, err := app.FindAllRecords(incidentsCollection,
		dbx.HashExp{"autoKey": component.key(), "auto": true},
		dbx.NewExp("status != {:resolved}", dbx.Params{"resolved": incidentResolved}))
	if err != nil {
		return err
	}
	for _, record := range records {
		name := autoIncidentName(record.GetString("title"))
		if err := addIncidentUpdate(app, record.Id, incidentResolved, fmt.Sprintf("%s has recovered.", name)); err != nil {
			return err
		}
	}
	return nil
}

// openAutoIncident returns the owner's open automatic incident with key, or nil.
func openAutoIncident(app core.App, owner, key string) (*core.Record, error) {
	records, err := app.FindAllRecords(incidentsCollection,
		dbx.HashExp{"user": owner, "autoKey": key, "auto": true},
		dbx.NewExp("status != {:resolved}", dbx.Params{"resolved": incidentResolved}))
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return records[0], nil
}

// addIncidentUpdate adds a server-authored update, which also sets the incident's status.
func addIncidentUpdate(app core.App, incidentID, status, message string) error {
	collection, err := app.FindCachedCollectionByNameOrId(incidentUpdatesCollection)
	if err != nil {
		return err
	}
	update := core.NewRecord(collection)
	update.Set("incident", incidentID)
	update.Set("status", status)
	update.Set("message", message)
	return app.Save(update)
}

// autoIncidentName returns the component name of an automatic incident title.
func autoIncidentName(title string) string {
	const suffix = " is down"
	if len(title) > len(suffix) && title[len(title)-len(suffix):] == suffix {
		return title[:len(title)-len(suffix)]
	}
	return title
}

func truncateTitle(title string) string {
	runes := []rune(title)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return title
}

// publicComponentName returns the name a status page shows for a component
// (see buildStatusPage and statusPageSystems): targets are only used when the
// page shows them.
func publicComponentName(page *core.Record, component incidentComponent, record *core.Record, index int) string {
	if name := record.GetString("name"); name != "" {
		return name
	}
	if component.field == "systems" {
		return "Server " + strconv.Itoa(index+1)
	}
	if target := record.GetString("target"); target != "" && page.GetBool("showTargets") {
		return target
	}
	return "Monitor " + strconv.Itoa(index+1)
}

// componentsOverall returns the overall status of a page's components (see
// buildStatusPage), without incidents.
func componentsOverall(app core.App, page *core.Record) (string, error) {
	monitors, err := viewableByOwner(app, page, "network_monitors", "monitors")
	if err != nil {
		return "", err
	}
	systems, err := viewableByOwner(app, page, "systems", "systems")
	if err != nil {
		return "", err
	}
	statuses := make([]string, 0, len(monitors)+len(systems))
	for _, record := range systems {
		if status := record.GetString("status"); status != uptime.StatusPending {
			statuses = append(statuses, status)
		}
	}
	for _, record := range monitors {
		status := record.GetString("status")
		if !record.GetBool("enabled") {
			status = uptime.StatusPaused
		}
		statuses = append(statuses, status)
	}
	return overallStatus(statuses), nil
}

// publicStatusIncidents are the incidents of a status page.
type publicStatusIncidents struct {
	// Active are the unresolved incidents, most recently started first.
	Active []publicStatusIncident `json:"active"`
	// Recent are the incidents resolved in the last incidentRecentDays days,
	// most recently resolved first.
	Recent []publicStatusIncident `json:"recent"`
}

// publicStatusIncident is an incident on a status page. Titles and messages
// are published as written by the owner; ids and authors never are.
type publicStatusIncident struct {
	Title      string `json:"title"`
	Status     string `json:"status"`
	Impact     string `json:"impact"`
	StartedAt  string `json:"startedAt"`
	ResolvedAt string `json:"resolvedAt,omitempty"`
	// Updates are newest first.
	Updates []publicStatusIncidentUpdate `json:"updates"`
	// id is the record id, for feeds; never published.
	id string
}

type publicStatusIncidentUpdate struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Created string `json:"created"`
}

// statusPageIncidents returns the page owner's incidents that list the page:
// unresolved ones and those resolved within incidentRecentDays of now.
func statusPageIncidents(app core.App, page *core.Record, now time.Time) (publicStatusIncidents, error) {
	return statusPageIncidentsSince(app, page, now, incidentRecentDays)
}

// statusPageIncidentsSince is statusPageIncidents for incidents resolved
// within the given number of days of now.
func statusPageIncidentsSince(app core.App, page *core.Record, now time.Time, days int) (publicStatusIncidents, error) {
	result := publicStatusIncidents{Active: []publicStatusIncident{}, Recent: []publicStatusIncident{}}
	since, err := types.ParseDateTime(now.AddDate(0, 0, -days))
	if err != nil {
		return result, err
	}
	records, err := app.FindAllRecords(incidentsCollection,
		dbx.HashExp{"user": page.GetString("user")},
		dbx.NewExp("(status != {:resolved} OR resolvedAt >= {:since}) AND statusPages LIKE {:page}", dbx.Params{
			"resolved": incidentResolved, "since": since.String(), "page": "%\"" + page.Id + "\"%",
		}))
	if err != nil {
		return result, err
	}
	var active, recent []*core.Record
	for _, record := range records {
		if !slices.Contains(record.GetStringSlice("statusPages"), page.Id) {
			continue
		}
		if record.GetString("status") == incidentResolved {
			recent = append(recent, record)
		} else {
			active = append(active, record)
		}
	}
	sortByDateDesc := func(field string) func(a, b *core.Record) int {
		return func(a, b *core.Record) int {
			return b.GetDateTime(field).Time().Compare(a.GetDateTime(field).Time())
		}
	}
	slices.SortStableFunc(active, sortByDateDesc("startedAt"))
	slices.SortStableFunc(recent, sortByDateDesc("resolvedAt"))
	active = active[:min(len(active), maxPublicIncidents)]
	recent = recent[:min(len(recent), maxPublicIncidents)]

	ids := make([]any, 0, len(active)+len(recent))
	for _, record := range slices.Concat(active, recent) {
		ids = append(ids, record.Id)
	}
	updates := map[string][]publicStatusIncidentUpdate{}
	if len(ids) > 0 {
		var rows []*core.Record
		collection, err := app.FindCachedCollectionByNameOrId(incidentUpdatesCollection)
		if err != nil {
			return result, err
		}
		err = app.RecordQuery(collection).Where(dbx.In("incident", ids...)).
			OrderBy("created DESC", "rowid DESC").All(&rows)
		if err != nil {
			return result, err
		}
		for _, row := range rows {
			incident := row.GetString("incident")
			if len(updates[incident]) >= maxPublicIncidentUpdates {
				continue
			}
			updates[incident] = append(updates[incident], publicStatusIncidentUpdate{
				Status:  row.GetString("status"),
				Message: row.GetString("message"),
				Created: formatISO(row.GetDateTime("created").Time()),
			})
		}
	}
	toPublic := func(record *core.Record) publicStatusIncident {
		item := publicStatusIncident{
			Title:     record.GetString("title"),
			Status:    record.GetString("status"),
			Impact:    record.GetString("impact"),
			StartedAt: formatISO(record.GetDateTime("startedAt").Time()),
			Updates:   updates[record.Id],
			id:        record.Id,
		}
		if item.Updates == nil {
			item.Updates = []publicStatusIncidentUpdate{}
		}
		if resolved := record.GetDateTime("resolvedAt"); !resolved.IsZero() {
			item.ResolvedAt = formatISO(resolved.Time())
		}
		return item
	}
	for _, record := range active {
		result.Active = append(result.Active, toPublic(record))
	}
	for _, record := range recent {
		result.Recent = append(result.Recent, toPublic(record))
	}
	return result, nil
}

// overallWithIncidents raises the overall status of a page to at least
// degraded while an active incident has major or critical impact. Incidents
// with minor or no impact are informational and leave it unchanged; a page
// that is already degraded or down stays so.
func overallWithIncidents(overall string, active []publicStatusIncident) string {
	if overall == overallDegraded || overall == uptime.StatusDown {
		return overall
	}
	for _, incident := range active {
		if incident.Impact == impactMajor || incident.Impact == impactCritical {
			return overallDegraded
		}
	}
	return overall
}
