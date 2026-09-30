package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

const alertNotesCollectionId = "alert_notes_001"

// alertAckFields are the acknowledgement and reminder fields of alerts_history.
var alertAckFields = []string{"acknowledgedAt", "acknowledgedBy", "ackNote", "remindedAt", "reminderCount"}

// Adds acknowledgement, reminders and notes to alert history:
//
//   - alerts_history.acknowledgedAt, acknowledgedBy, ackNote: set by the
//     acknowledge endpoints (and signed links in notifications) only; the
//     collection has no update rule.
//   - alerts_history.remindedAt, reminderCount: server-managed, the last
//     reminder sent for an open, unacknowledged alert and how many were sent.
//   - alert_notes: notes on a history row, visible to and added by the row's
//     user only (readonly users can read but not add them).
func init() {
	m.Register(func(app core.App) error {
		history, err := app.FindCollectionByNameOrId("alerts_history")
		if err != nil {
			return err
		}
		history.Fields.Add(
			&core.DateField{Id: "ah_ack_at", Name: "acknowledgedAt"},
			&core.RelationField{Id: "ah_ack_by", Name: "acknowledgedBy", CollectionId: "_pb_users_auth_", MaxSelect: 1},
			&core.TextField{Id: "ah_ack_note", Name: "ackNote", Max: 1000},
			&core.DateField{Id: "ah_reminded_at", Name: "remindedAt"},
			&core.NumberField{Id: "ah_reminder_count", Name: "reminderCount", OnlyInt: true},
		)
		if err := app.Save(history); err != nil {
			return err
		}

		notes := core.NewBaseCollection("alert_notes", alertNotesCollectionId)
		owner := `@request.auth.id != "" && alert.user = @request.auth.id`
		notes.ListRule = types.Pointer(owner)
		notes.ViewRule = types.Pointer(owner)
		notes.CreateRule = types.Pointer(owner + ` && author = @request.auth.id && @request.auth.role != "readonly"`)
		notes.DeleteRule = types.Pointer(`@request.auth.id != "" && author = @request.auth.id && @request.auth.role != "readonly"`)
		notes.Fields.Add(
			&core.RelationField{Id: "an_alert", Name: "alert", CollectionId: history.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.RelationField{Id: "an_author", Name: "author", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Id: "an_text", Name: "text", Required: true, Max: 1000},
			&core.AutodateField{Id: "an_created", Name: "created", OnCreate: true},
		)
		notes.AddIndex("idx_an_alert", false, "alert", "")
		return app.Save(notes)
	}, func(app core.App) error {
		if notes, err := app.FindCollectionByNameOrId("alert_notes"); err == nil {
			if err := app.Delete(notes); err != nil {
				return err
			}
		}
		history, err := app.FindCollectionByNameOrId("alerts_history")
		if err != nil {
			return err
		}
		for _, name := range alertAckFields {
			history.Fields.RemoveByName(name)
		}
		return app.Save(history)
	})
}
