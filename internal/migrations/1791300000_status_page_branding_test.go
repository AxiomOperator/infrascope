//go:build testing

package migrations

import (
	"regexp"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusPageBrandingMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	pages := mustCollection(t, app, "status_pages")
	for _, name := range statusPageBrandingFields {
		assert.NotNil(t, pages.Fields.GetByName(name), name)
	}
	logo, ok := pages.Fields.GetByName("logo").(*core.FileField)
	require.True(t, ok)
	assert.Equal(t, int64(512<<10), logo.MaxSize)
	assert.Equal(t, 1, logo.MaxSelect)
	assert.NotEmpty(t, pages.GetIndex("idx_sp_custom_domain"))

	hostname := regexp.MustCompile(statusPageHostnamePattern)
	for _, valid := range []string{"status.example.com", "a.io", "xn--bcher-kva.example", "my-status.example.co.uk"} {
		assert.True(t, hostname.MatchString(valid), valid)
	}
	for _, invalid := range []string{"", "localhost", "Status.example.com", "https://status.example.com", "status.example.com:8080",
		"status.example.com/", "status.example.com.", "-a.example.com", "10.0.0.1", "a..example.com"} {
		assert.False(t, hostname.MatchString(invalid), invalid)
	}

	migration := findMigration(t, "_status_page_branding.go")
	require.NoError(t, migration.Down(app))
	pages = mustCollection(t, app, "status_pages")
	for _, name := range statusPageBrandingFields {
		assert.Nil(t, pages.Fields.GetByName(name), name)
	}
	assert.Empty(t, pages.GetIndex("idx_sp_custom_domain"))
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "status_pages").Fields.GetByName("customDomain"))
}
