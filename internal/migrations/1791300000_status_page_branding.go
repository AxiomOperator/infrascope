package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// statusPageHostnamePattern matches a lowercase DNS hostname with at least two
// labels and no scheme, port, path or trailing dot.
const statusPageHostnamePattern = `^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`

// statusPageBrandingFields are the fields added to status_pages.
var statusPageBrandingFields = []string{"groups", "logo", "accentColor", "footerText", "hidePoweredBy", "customDomain"}

// Adds component groups, branding and a custom domain to status pages:
//
//   - groups: named, ordered groups of the page's monitors and systems
//     (validated by the hub against the page's components).
//   - logo: a PNG, JPEG, WebP or SVG image, served publicly by the hub.
//   - accentColor (#rrggbb), footerText and hidePoweredBy.
//   - customDomain: a unique hostname that serves the page at its root.
func init() {
	m.Register(func(app core.App) error {
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.Fields.Add(
			&core.JSONField{Id: "sp_groups", Name: "groups", MaxSize: 64 << 10},
			&core.FileField{Id: "sp_logo", Name: "logo", MaxSelect: 1, MaxSize: 512 << 10,
				MimeTypes: []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}},
			&core.TextField{Id: "sp_accent_color", Name: "accentColor", Pattern: `^#[0-9a-fA-F]{6}$`},
			&core.TextField{Id: "sp_footer_text", Name: "footerText", Max: 500},
			&core.BoolField{Id: "sp_hide_powered_by", Name: "hidePoweredBy"},
			&core.TextField{Id: "sp_custom_domain", Name: "customDomain", Max: 253, Pattern: statusPageHostnamePattern},
		)
		pages.AddIndex("idx_sp_custom_domain", true, "customDomain", "customDomain != ''")
		return app.Save(pages)
	}, func(app core.App) error {
		pages, err := app.FindCollectionByNameOrId("status_pages")
		if err != nil {
			return err
		}
		pages.RemoveIndex("idx_sp_custom_domain")
		for _, name := range statusPageBrandingFields {
			pages.Fields.RemoveByName(name)
		}
		return app.Save(pages)
	})
}
