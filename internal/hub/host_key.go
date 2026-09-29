package hub

import (
	"net/http"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// resetSystemHostKey handles POST /api/beszel/systems/{id}/reset-host-key.
// It clears the SSH host key pinned for the system, so the key the agent
// presents on the next connection is trusted and pinned (e.g. after the agent
// was reinstalled). Only users who can update the system may reset it.
func (h *Hub) resetSystemHostKey(e *core.RequestEvent) error {
	record, err := e.App.FindRecordById("systems", e.Request.PathValue("id"))
	if err != nil {
		return e.NotFoundError("", nil)
	}
	if !e.HasSuperuserAuth() {
		info, err := e.RequestInfo()
		if err != nil {
			return err
		}
		collection := record.Collection()
		if ok, _ := e.App.CanAccessRecord(record, info, collection.ViewRule); !ok {
			return e.NotFoundError("", nil)
		}
		if ok, _ := e.App.CanAccessRecord(record, info, collection.UpdateRule); !ok {
			return e.ForbiddenError("You cannot update this system.", nil)
		}
	}
	if _, err := e.App.DB().Update("systems", dbx.Params{"hostKey": ""}, dbx.HashExp{"id": record.Id}).Execute(); err != nil {
		return e.InternalServerError("Failed to reset the host key", err)
	}
	e.App.Logger().Info("Reset pinned SSH host key", "system", record.Id)
	return e.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
