package migrations

import (
	"slices"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// monitorCheckProtocols are the network_monitors protocols added with check options.
var monitorCheckProtocols = []string{
	"ssh", "postgres", "mysql", "redis", "smtp", "imap", "grpc", "minecraft", "a2s", "docker",
}

// Adds the monitor protocols with check options and the network_monitors
// "check" field holding their non-secret options. Credentials of postgres and
// redis checks are stored encrypted in httpSecrets.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		protocol := collection.Fields.GetByName("protocol").(*core.SelectField)
		for _, value := range monitorCheckProtocols {
			if !slices.Contains(protocol.Values, value) {
				protocol.Values = append(protocol.Values, value)
			}
		}
		collection.Fields.Add(&core.JSONField{Id: "nm_check", Name: "check", MaxSize: 16 << 10})
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		if _, err := app.DB().NewQuery("DELETE FROM network_monitors WHERE protocol IN ('ssh','postgres','mysql','redis','smtp','imap','grpc','minecraft','a2s','docker')").Execute(); err != nil {
			return err
		}
		protocol := collection.Fields.GetByName("protocol").(*core.SelectField)
		values := protocol.Values[:0:0]
		for _, value := range protocol.Values {
			if !slices.Contains(monitorCheckProtocols, value) {
				values = append(values, value)
			}
		}
		protocol.Values = values
		collection.Fields.RemoveByName("check")
		return app.Save(collection)
	})
}
