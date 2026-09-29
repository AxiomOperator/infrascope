package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const (
	systemEventsCollectionId = "sys_events_001"
	systemsCollectionId      = "2hz5ncl8tizk5nx"
)

// Adds system_events, the status history of systems (one row per period
// with one status, like monitor_events), and status_pages.systems, the
// ordered systems shown on a status page. Access rules of system_events are
// set by the hub on startup.
func init() {
	m.Register(func(app core.App) error {
		events := core.NewBaseCollection("system_events", systemEventsCollectionId)
		events.Fields.Add(
			&core.RelationField{Id: "se_system", Name: "system", CollectionId: systemsCollectionId, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.SelectField{Id: "se_status", Name: "status", MaxSelect: 1, Required: true, Values: []string{"up", "down", "paused", "pending"}},
			&core.NumberField{Id: "se_start", Name: "start", Help: "Unix timestamp in milliseconds"},
			&core.NumberField{Id: "se_end", Name: "end", Help: "Unix timestamp in milliseconds; 0 while open"},
		)
		events.AddIndex("idx_se_system_start", false, "system, start", "")
		if err := app.Save(events); err != nil {
			return err
		}

		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.Add(&core.RelationField{Id: "sp_systems", Name: "systems", CollectionId: systemsCollectionId, MaxSelect: 100})
		return app.Save(pages)
	}, func(app core.App) error {
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.RemoveByName("systems")
		if err := app.Save(pages); err != nil {
			return err
		}
		events, err := app.FindCollectionByNameOrId(systemEventsCollectionId)
		if err != nil {
			return nil
		}
		return app.Delete(events)
	})
}
