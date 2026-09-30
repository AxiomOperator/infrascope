package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const pushSubscriptionsCollectionId = "push_subscriptions_001"

// Adds push_subscriptions: the Web Push (browser notification) subscriptions
// of users, one per browser/device. Owners list and remove their own
// subscriptions; they are created through the hub API, which validates the
// endpoint and keys. p256dh and auth are the browser's encryption keys and are
// hidden from the API. failures counts consecutive failed deliveries.
func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		ownerRule := "@request.auth.id != \"\" && user = @request.auth.id"
		collection := core.NewBaseCollection("push_subscriptions", pushSubscriptionsCollectionId)
		collection.ListRule = &ownerRule
		collection.ViewRule = &ownerRule
		collection.DeleteRule = &ownerRule
		collection.Fields.Add(
			&core.RelationField{Id: "psub_user", Name: "user", CollectionId: users.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Id: "psub_endpoint", Name: "endpoint", Required: true, Max: 2048},
			&core.TextField{Id: "psub_p256dh", Name: "p256dh", Required: true, Hidden: true, Max: 128},
			&core.TextField{Id: "psub_auth", Name: "auth", Required: true, Hidden: true, Max: 64},
			&core.TextField{Id: "psub_user_agent", Name: "userAgent", Max: 300},
			&core.TextField{Id: "psub_name", Name: "name", Max: 100},
			&core.AutodateField{Id: "psub_created_at", Name: "createdAt", OnCreate: true},
			&core.DateField{Id: "psub_last_success_at", Name: "lastSuccessAt"},
			&core.NumberField{Id: "psub_failures", Name: "failures", OnlyInt: true, Min: new(float64(0))},
		)
		collection.AddIndex("idx_psub_endpoint", true, "endpoint", "")
		collection.AddIndex("idx_psub_user", false, "user", "")
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(pushSubscriptionsCollectionId)
		if err != nil {
			return nil
		}
		return app.Delete(collection)
	})
}
