package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// monitorLocationFields are the network_monitors fields of multi-location checks.
var monitorLocationFields = []string{"locations", "quorum", "locationStatus", "locationSystems"}

// Adds multi-location checks to network_monitors:
//
//   - locations: JSON array of the runners that check the monitor, "hub" or
//     a system id. "system" stays the primary location (the first agent
//     location, or "" when the hub is the only one).
//   - quorum: how many locations must confirm the monitor down.
//   - locationStatus: server-managed status of each location.
//   - locationSystems: server-managed relation mirroring the agent
//     locations, so API rules can grant access through any location.
//
// Existing monitors get their single location from their system.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		systems, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		collection.Fields.Add(
			&core.JSONField{Id: "nm_locations", Name: "locations", MaxSize: 4 << 10},
			&core.NumberField{Id: "nm_quorum", Name: "quorum", Min: new(float64(0)), Max: new(float64(10)), OnlyInt: true},
			&core.JSONField{Id: "nm_location_status", Name: "locationStatus", MaxSize: 64 << 10},
			&core.RelationField{Id: "nm_location_systems", Name: "locationSystems", CollectionId: systems.Id, MaxSelect: 10},
		)
		if err := app.Save(collection); err != nil {
			return err
		}
		_, err = app.DB().NewQuery(`UPDATE network_monitors SET
			locations = CASE WHEN system = '' THEN '["hub"]' ELSE json_array(system) END,
			locationSystems = CASE WHEN system = '' THEN '[]' ELSE json_array(system) END,
			quorum = 1`).Execute()
		return err
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		for _, name := range monitorLocationFields {
			collection.Fields.RemoveByName(name)
		}
		// Monitors keep their primary location.
		return app.Save(collection)
	})
}
