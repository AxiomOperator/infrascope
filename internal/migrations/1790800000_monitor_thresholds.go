package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds the monitor-level loss and latency alert thresholds and the hidden
// network_monitors.alertState field holding their server-managed state, and
// raises the size limit of httpSecrets, which is now stored encrypted.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		collection.Fields.Add(
			// Percent of failed checks over the last hour; 0 disables the alert.
			&core.NumberField{Id: "nm_loss_threshold", Name: "lossThreshold", Min: new(float64(0)), Max: new(float64(99.99))},
			// Average response time in ms over the last hour; 0 disables the alert.
			&core.NumberField{Id: "nm_latency_threshold", Name: "latencyThreshold", Min: new(float64(0)), Max: new(float64(600000))},
			&core.JSONField{Id: "nm_alert_state", Name: "alertState", Hidden: true},
		)
		if secrets, ok := collection.Fields.GetByName("httpSecrets").(*core.JSONField); ok {
			secrets.MaxSize = 1 << 20
		}
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		for _, name := range []string{"lossThreshold", "latencyThreshold", "alertState"} {
			collection.Fields.RemoveByName(name)
		}
		if secrets, ok := collection.Fields.GetByName("httpSecrets").(*core.JSONField); ok {
			secrets.MaxSize = 512 << 10
		}
		return app.Save(collection)
	})
}
