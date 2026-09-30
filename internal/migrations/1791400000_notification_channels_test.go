//go:build testing

package migrations

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestNotificationChannelsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	channels := mustCollection(t, app, "notification_channels")
	require.NotNil(t, channels.CreateRule)
	assert.Contains(t, *channels.CreateRule, `@request.auth.role != "readonly"`)
	assert.Contains(t, *channels.ListRule, "user = @request.auth.id")
	for _, name := range []string{"alerts", "network_monitors"} {
		collection := mustCollection(t, app, name)
		assert.NotNil(t, collection.Fields.GetByName("severity"), name)
		relation, ok := collection.Fields.GetByName("channels").(*core.RelationField)
		require.True(t, ok, name)
		assert.Equal(t, channels.Id, relation.CollectionId)
	}
	assert.NotNil(t, mustCollection(t, app, "alerts_history").Fields.GetByName("severity"))

	users := mustCollection(t, app, "users")
	settingsCollection := mustCollection(t, app, "user_settings")
	newUser := func(email, settings string) *core.Record {
		user := core.NewRecord(users)
		user.Set("email", email)
		user.Set("password", "password123")
		require.NoError(t, app.Save(user))
		record := core.NewRecord(settingsCollection)
		record.Set("user", user.Id)
		record.Set("settings", settings)
		require.NoError(t, app.Save(record))
		return user
	}
	both := newUser("both@example.com", `{"emails":["a@example.com","b@example.com"],"webhooks":["discord://token@id","generic+https://example.com/hook","discord://t2@id2"]}`)
	none := newUser("none@example.com", `{"emails":[],"webhooks":[]}`)
	newUser("legacy@example.com", `{"chartTime":"1h"}`)

	migration := findMigration(t, "_notification_channels.go")
	rerun := func() {
		t.Helper()
		require.NoError(t, migration.Down(app))
		_, err := app.FindCollectionByNameOrId("notification_channels")
		require.Error(t, err)
		assert.Nil(t, mustCollection(t, app, "alerts").Fields.GetByName("channels"))
		require.NoError(t, migration.Up(app))
	}
	rerun()

	type row struct {
		Name        string `db:"name"`
		Type        string `db:"type"`
		Config      string `db:"config"`
		Enabled     bool   `db:"enabled"`
		IsDefault   bool   `db:"isDefault"`
		MinSeverity string `db:"minSeverity"`
	}
	rowsOf := func(user string) []row {
		var rows []row
		require.NoError(t, app.DB().Select("name", "type", "config", "enabled", "isDefault", "minSeverity").
			From("notification_channels").Where(dbx.HashExp{"user": user}).OrderBy("rowid").All(&rows))
		return rows
	}
	rows := rowsOf(both.Id)
	require.Len(t, rows, 4)
	assert.Equal(t, []string{"Email", "Discord", "Webhook", "Discord 2"}, []string{rows[0].Name, rows[1].Name, rows[2].Name, rows[3].Name})
	assert.JSONEq(t, `{"addresses":["a@example.com","b@example.com"]}`, rows[0].Config)
	assert.JSONEq(t, `{"url":"generic+https://example.com/hook"}`, rows[2].Config, "without a hub key the hub seals it on startup")
	for _, r := range rows {
		assert.True(t, r.Enabled && r.IsDefault, r.Name)
		assert.Equal(t, "info", r.MinSeverity, r.Name)
	}
	assert.Equal(t, "email", rows[0].Type)
	assert.Equal(t, "shoutrrr", rows[1].Type)
	assert.Empty(t, rowsOf(none.Id))

	// With the hub key, URLs are sealed by the migration.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(key, "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.DataDir(), monitorsecrets.KeyFileName), pem.EncodeToMemory(block), 0o600))
	rerun()
	rows = rowsOf(both.Id)
	require.Len(t, rows, 4)
	assert.Contains(t, rows[1].Config, monitorsecrets.Prefix)
	assert.NotContains(t, rows[1].Config, "token")
	box, err := monitorsecrets.ForDataDirWithInfo(app.DataDir(), monitorsecrets.ChannelsInfo)
	require.NoError(t, err)
	sealed := rows[1].Config[strings.Index(rows[1].Config, monitorsecrets.Prefix):]
	sealed = sealed[:strings.Index(sealed, `"`)]
	plaintext, err := box.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, "discord://token@id", string(plaintext))
	// The monitor secrets key is a different one.
	monitorBox, err := monitorsecrets.ForDataDir(app.DataDir())
	require.NoError(t, err)
	_, err = monitorBox.Open(sealed)
	assert.Error(t, err)
}
