package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Renames the default app and email sender name from Beszel to InfraScope.
// Names an admin has customised are left unchanged.
func init() {
	m.Register(func(app core.App) error {
		return renameAppName(app, "Beszel", "InfraScope")
	}, func(app core.App) error {
		return renameAppName(app, "InfraScope", "Beszel")
	})
}

func renameAppName(app core.App, from, to string) error {
	settings := app.Settings()
	changed := false
	if settings.Meta.AppName == from {
		settings.Meta.AppName = to
		changed = true
	}
	if settings.Meta.SenderName == from {
		settings.Meta.SenderName = to
		changed = true
	}
	if !changed {
		return nil
	}
	return app.Save(settings)
}
