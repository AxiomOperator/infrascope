package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// dockerDiscoverySystemFields are the systems fields of Docker label discovery.
var dockerDiscoverySystemFields = []string{"autoDiscover", "autoDiscoverTraefik", "discoveryErrors"}

// dockerDiscoveryMonitorFields are the network_monitors fields of monitors
// managed by Docker label discovery.
var dockerDiscoveryMonitorFields = []string{"managedBy", "managedKey", "managedSystem", "managedFields", "managedMissingSince"}

// Adds monitor discovery from Docker container labels:
//
//   - systems.autoDiscover: create monitors from the system's container labels.
//   - systems.autoDiscoverTraefik: also create http monitors for Traefik router hosts.
//   - systems.discoveryErrors: server-managed list of label sets that failed validation.
//   - network_monitors.managedBy: "docker" for discovered monitors, empty otherwise.
//   - network_monitors.managedKey: "docker:<container>:<id>", unique per managedSystem.
//   - network_monitors.managedSystem: the system whose labels declare the monitor.
//   - network_monitors.managedFields: fields set by labels, read-only for users.
//   - network_monitors.managedMissingSince: when the declaring container disappeared.
func init() {
	m.Register(func(app core.App) error {
		systems, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		systems.Fields.Add(
			&core.BoolField{Id: "sys_auto_discover", Name: "autoDiscover"},
			&core.BoolField{Id: "sys_auto_discover_traefik", Name: "autoDiscoverTraefik"},
			&core.JSONField{Id: "sys_discovery_errors", Name: "discoveryErrors", MaxSize: 64 << 10},
		)
		if err := app.Save(systems); err != nil {
			return err
		}
		monitors, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		monitors.Fields.Add(
			&core.TextField{Id: "nm_managed_by", Name: "managedBy", Max: 32},
			&core.TextField{Id: "nm_managed_key", Name: "managedKey", Max: 400},
			&core.RelationField{Id: "nm_managed_system", Name: "managedSystem", CollectionId: systems.Id, MaxSelect: 1, CascadeDelete: true},
			&core.JSONField{Id: "nm_managed_fields", Name: "managedFields", MaxSize: 4 << 10},
			&core.DateField{Id: "nm_managed_missing_since", Name: "managedMissingSince"},
		)
		monitors.AddIndex("idx_nm_managed_key", true, "`managedSystem`, `managedKey`", "`managedKey` != ''")
		return app.Save(monitors)
	}, func(app core.App) error {
		monitors, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		monitors.RemoveIndex("idx_nm_managed_key")
		for _, name := range dockerDiscoveryMonitorFields {
			monitors.Fields.RemoveByName(name)
		}
		if err := app.Save(monitors); err != nil {
			return err
		}
		systems, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		for _, name := range dockerDiscoverySystemFields {
			systems.Fields.RemoveByName(name)
		}
		return app.Save(systems)
	})
}
