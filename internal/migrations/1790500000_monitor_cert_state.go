package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds the hidden network_monitors.certState field, which records the
// certificate expiry last notified, separate from the uptime engine's state.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		collection.Fields.Add(&core.JSONField{Id: "nm_cert_state", Name: "certState", Hidden: true})
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("certState")
		return app.Save(collection)
	})
}
