//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushSubscriptionsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	users := mustCollection(t, app, "users")
	subscriptions := mustCollection(t, app, "push_subscriptions")
	user, ok := subscriptions.Fields.GetByName("user").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, users.Id, user.CollectionId)
	assert.True(t, user.CascadeDelete)
	assert.Equal(t, 2048, subscriptions.Fields.GetByName("endpoint").(*core.TextField).Max)
	assert.Equal(t, 300, subscriptions.Fields.GetByName("userAgent").(*core.TextField).Max)
	assert.Equal(t, 100, subscriptions.Fields.GetByName("name").(*core.TextField).Max)
	assert.True(t, subscriptions.Fields.GetByName("p256dh").GetHidden())
	assert.True(t, subscriptions.Fields.GetByName("auth").GetHidden())
	for _, name := range []string{"createdAt", "lastSuccessAt", "failures"} {
		assert.NotNil(t, subscriptions.Fields.GetByName(name), name)
	}
	ownerRule := "@request.auth.id != \"\" && user = @request.auth.id"
	for _, rule := range []*string{subscriptions.ListRule, subscriptions.ViewRule, subscriptions.DeleteRule} {
		require.NotNil(t, rule)
		assert.Equal(t, ownerRule, *rule)
	}
	assert.Nil(t, subscriptions.CreateRule, "created through the hub API")
	assert.Nil(t, subscriptions.UpdateRule)

	owner := core.NewRecord(users)
	owner.Set("email", "owner@example.com")
	owner.Set("password", "testtesttest")
	require.NoError(t, app.Save(owner))
	add := func(endpoint string) error {
		record := core.NewRecord(subscriptions)
		record.Set("user", owner.Id)
		record.Set("endpoint", endpoint)
		record.Set("p256dh", "key")
		record.Set("auth", "secret")
		return app.Save(record)
	}
	require.NoError(t, add("https://push.example.com/a"))
	require.NoError(t, add("https://push.example.com/b"))
	assert.Error(t, add("https://push.example.com/a"), "endpoints are unique")

	// Deleting the user removes their subscriptions.
	require.NoError(t, app.Delete(owner))
	total, err := app.CountRecords("push_subscriptions")
	require.NoError(t, err)
	assert.Zero(t, total)

	migration := findMigration(t, "_push_subscriptions.go")
	require.NoError(t, migration.Down(app))
	_, err = app.FindCollectionByNameOrId("push_subscriptions")
	assert.Error(t, err)
	require.NoError(t, migration.Up(app))
	mustCollection(t, app, "push_subscriptions")
}
