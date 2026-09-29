package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds systems.hostKey, the hidden SHA256 fingerprint of the agent's SSH host
// key pinned on first use, and systems.downReason, why the system last went
// down (e.g. a host key mismatch).
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		collection.Fields.Add(
			&core.TextField{Id: "sys_host_key", Name: "hostKey", Hidden: true, Max: 128},
			&core.TextField{Id: "sys_down_reason", Name: "downReason", Max: 1000},
		)
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("hostKey")
		collection.Fields.RemoveByName("downReason")
		return app.Save(collection)
	})
}
