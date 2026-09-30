package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const statusSubscribersCollectionId = "status_subscribers_001"

// Adds email subscriptions to status pages:
//
//   - status_pages.allowSubscriptions: lets visitors of a public page subscribe
//     to email updates (double opt-in; needs SMTP and the app URL).
//   - status_subscribers: the subscribers of a page. Superuser-only rules: the
//     page owner lists and removes subscribers through the hub API, and
//     visitors subscribe, confirm and unsubscribe through public endpoints.
//     tokenHash is the SHA-256 of the confirmation token (never the token);
//     it is cleared once confirmed.
func init() {
	m.Register(func(app core.App) error {
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.Add(&core.BoolField{Id: "sp_allow_subscriptions", Name: "allowSubscriptions"})
		if err := app.Save(pages); err != nil {
			return err
		}

		subscribers := core.NewBaseCollection("status_subscribers", statusSubscribersCollectionId)
		subscribers.Fields.Add(
			&core.RelationField{Id: "ssub_page", Name: "page", CollectionId: pages.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Id: "ssub_email", Name: "email", Required: true, Max: 254},
			&core.BoolField{Id: "ssub_confirmed", Name: "confirmed"},
			&core.DateField{Id: "ssub_confirmed_at", Name: "confirmedAt"},
			&core.TextField{Id: "ssub_token_hash", Name: "tokenHash", Hidden: true, Max: 64},
			&core.DateField{Id: "ssub_created_at", Name: "createdAt"},
			&core.DateField{Id: "ssub_last_sent_at", Name: "lastSentAt"},
		)
		subscribers.AddIndex("idx_ssub_page_email", true, "page, email", "")
		subscribers.AddIndex("idx_ssub_token_hash", true, "tokenHash", "tokenHash != ''")
		subscribers.AddIndex("idx_ssub_confirmed_created", false, "confirmed, createdAt", "")
		return app.Save(subscribers)
	}, func(app core.App) error {
		if subscribers, err := app.FindCollectionByNameOrId(statusSubscribersCollectionId); err == nil {
			if err := app.Delete(subscribers); err != nil {
				return err
			}
		}
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.RemoveByName("allowSubscriptions")
		return app.Save(pages)
	})
}
