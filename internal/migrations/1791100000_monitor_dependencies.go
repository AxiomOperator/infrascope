package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// dependencyFields are the fields of dependency-aware alerts, added to both
// network_monitors and systems.
var dependencyFields = []string{"dependsOn", "suppressedBy"}

// Adds dependency-aware alerts to network_monitors and systems:
//
//   - dependsOn: up to five parent monitors (a router, switch or host). While
//     any of them is down, the record's down/up notifications and threshold
//     alerts are suppressed. Deleting a parent removes it from the relation.
//   - suppressedBy: server-managed, the names of the parents that are down
//     (comma separated), empty when notifications are not suppressed.
func init() {
	m.Register(func(app core.App) error {
		monitors, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		systems, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		monitors.Fields.Add(
			&core.RelationField{Id: "nm_depends_on", Name: "dependsOn", CollectionId: monitors.Id, MaxSelect: 5},
			&core.TextField{Id: "nm_suppressed_by", Name: "suppressedBy", Max: 1000},
		)
		if err := app.Save(monitors); err != nil {
			return err
		}
		systems.Fields.Add(
			&core.RelationField{Id: "sys_depends_on", Name: "dependsOn", CollectionId: monitors.Id, MaxSelect: 5},
			&core.TextField{Id: "sys_suppressed_by", Name: "suppressedBy", Max: 1000},
		)
		return app.Save(systems)
	}, func(app core.App) error {
		for _, name := range []string{"systems", "network_monitors"} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			for _, field := range dependencyFields {
				collection.Fields.RemoveByName(field)
			}
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
