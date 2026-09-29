package hub

import (
	"fmt"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// monitorSecretsBox returns the box sealing monitor secrets. It creates the
// hub key when missing, like GetSSHKey.
func (h *Hub) monitorSecretsBox() (*monitorsecrets.Box, error) {
	if _, err := h.GetSSHKey(""); err != nil {
		return nil, err
	}
	return monitorsecrets.ForDataDir(h.DataDir())
}

// sealMonitorSecrets seals a record's plaintext httpSecrets before it is
// written. Without a key it fails closed: new plaintext is rejected, while a
// plaintext value already stored (from before encryption) may be kept.
func (h *Hub) sealMonitorSecrets(record *core.Record) error {
	raw := record.GetString("httpSecrets")
	if raw == "" || raw == "null" || monitorsecrets.IsSealedJSON(raw) {
		return nil
	}
	box, err := h.monitorSecretsBox()
	if err == nil {
		var sealed string
		if sealed, err = box.SealJSON(raw); err == nil {
			record.Set("httpSecrets", sealed)
			return nil
		}
	}
	h.Logger().Error("Failed to encrypt monitor secrets", "monitor", record.Id, "err", err)
	if !record.IsNew() && raw == record.Original().GetString("httpSecrets") {
		return nil
	}
	return fmt.Errorf("monitor secrets cannot be stored securely: %w", err)
}

// sealStoredMonitorSecrets seals plaintext httpSecrets stored before
// encryption. It is idempotent and skips rows changed concurrently.
func (h *Hub) sealStoredMonitorSecrets() error {
	var rows []struct {
		ID      string `db:"id"`
		Secrets string `db:"httpSecrets"`
	}
	err := h.DB().Select("id", "httpSecrets").From("network_monitors").
		Where(dbx.NewExp("httpSecrets IS NOT NULL AND httpSecrets != '' AND httpSecrets != 'null' AND httpSecrets NOT LIKE {:prefix}",
			dbx.Params{"prefix": `"` + monitorsecrets.Prefix + "%"})).
		All(&rows)
	if err != nil || len(rows) == 0 {
		return err
	}
	box, err := h.monitorSecretsBox()
	if err != nil {
		return fmt.Errorf("monitor secrets key unavailable: %w", err)
	}
	for _, row := range rows {
		sealed, err := box.SealJSON(row.Secrets)
		if err != nil {
			return err
		}
		if _, err := h.DB().Update("network_monitors", dbx.Params{"httpSecrets": sealed},
			dbx.HashExp{"id": row.ID, "httpSecrets": row.Secrets}).Execute(); err != nil {
			return err
		}
	}
	h.Logger().Info("Encrypted stored monitor secrets", "count", len(rows))
	return nil
}
