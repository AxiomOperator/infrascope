package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const (
	incidentsCollectionId       = "incidents_001"
	incidentUpdatesCollectionId = "incident_upd_001"
)

// incidentStatuses are the statuses of incidents and their updates.
var incidentStatuses = []string{"investigating", "identified", "monitoring", "resolved"}

// Adds incidents, user-owned outage reports with a timeline of updates that
// status pages publish, and status_pages.autoIncidents, which opts a page
// into incidents created automatically when its components go down.
//
//   - incidents: title, status, impact, the affected monitors and systems,
//     the status pages that show it, auto (created automatically), autoKey
//     (hidden, server-managed: the component of an automatic incident, e.g.
//     "monitor:<id>"), startedAt and resolvedAt (server-managed from status).
//   - incident_updates: the timeline of an incident. Creating one also sets
//     the incident's status. Deleted with the incident.
//
// Access rules are set by the hub on startup.
func init() {
	m.Register(func(app core.App) error {
		incidents := core.NewBaseCollection("incidents", incidentsCollectionId)
		incidents.Fields.Add(
			&core.RelationField{Id: "inc_user", Name: "user", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Id: "inc_title", Name: "title", Required: true, Max: 200},
			&core.SelectField{Id: "inc_status", Name: "status", MaxSelect: 1, Required: true, Values: incidentStatuses},
			&core.SelectField{Id: "inc_impact", Name: "impact", MaxSelect: 1, Required: true, Values: []string{"none", "minor", "major", "critical"}},
			&core.RelationField{Id: "inc_monitors", Name: "monitors", CollectionId: networkMonitorsCollectionId, MaxSelect: 100},
			&core.RelationField{Id: "inc_systems", Name: "systems", CollectionId: systemsCollectionId, MaxSelect: 100},
			&core.RelationField{Id: "inc_status_pages", Name: "statusPages", CollectionId: statusPagesCollectionId, MaxSelect: 50},
			&core.BoolField{Id: "inc_auto", Name: "auto"},
			&core.TextField{Id: "inc_auto_key", Name: "autoKey", Max: 100, Hidden: true},
			&core.DateField{Id: "inc_started_at", Name: "startedAt"},
			&core.DateField{Id: "inc_resolved_at", Name: "resolvedAt"},
			&core.AutodateField{Id: "inc_created", Name: "created", OnCreate: true},
			&core.AutodateField{Id: "inc_updated", Name: "updated", OnCreate: true, OnUpdate: true},
		)
		incidents.AddIndex("idx_inc_user_resolved", false, "user, resolvedAt", "")
		incidents.AddIndex("idx_inc_auto_key", false, "autoKey", "autoKey != ''")
		if err := app.Save(incidents); err != nil {
			return err
		}

		updates := core.NewBaseCollection("incident_updates", incidentUpdatesCollectionId)
		updates.Fields.Add(
			&core.RelationField{Id: "incu_incident", Name: "incident", CollectionId: incidentsCollectionId, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.SelectField{Id: "incu_status", Name: "status", MaxSelect: 1, Required: true, Values: incidentStatuses},
			&core.TextField{Id: "incu_message", Name: "message", Required: true, Max: 5000},
			&core.RelationField{Id: "incu_author", Name: "author", CollectionId: "_pb_users_auth_", MaxSelect: 1},
			&core.AutodateField{Id: "incu_created", Name: "created", OnCreate: true},
		)
		updates.AddIndex("idx_incu_incident_created", false, "incident, created", "")
		if err := app.Save(updates); err != nil {
			return err
		}

		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.Add(&core.BoolField{Id: "sp_auto_incidents", Name: "autoIncidents"})
		return app.Save(pages)
	}, func(app core.App) error {
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.RemoveByName("autoIncidents")
		if err := app.Save(pages); err != nil {
			return err
		}
		for _, id := range []string{incidentUpdatesCollectionId, incidentsCollectionId} {
			collection, err := app.FindCollectionByNameOrId(id)
			if err != nil {
				continue
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
