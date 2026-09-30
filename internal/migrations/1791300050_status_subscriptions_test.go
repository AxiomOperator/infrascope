//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusSubscriptionsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	pages := mustCollection(t, app, "status_pages")
	assert.NotNil(t, pages.Fields.GetByName("allowSubscriptions"))

	subscribers := mustCollection(t, app, "status_subscribers")
	page, ok := subscribers.Fields.GetByName("page").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, pages.Id, page.CollectionId)
	assert.True(t, page.CascadeDelete)
	assert.Equal(t, 254, subscribers.Fields.GetByName("email").(*core.TextField).Max)
	assert.True(t, subscribers.Fields.GetByName("tokenHash").GetHidden())
	for _, rule := range []*string{subscribers.ListRule, subscribers.ViewRule, subscribers.CreateRule, subscribers.UpdateRule, subscribers.DeleteRule} {
		assert.Nil(t, rule, "subscribers are superuser-only")
	}
	for _, name := range []string{"confirmed", "confirmedAt", "createdAt", "lastSentAt"} {
		assert.NotNil(t, subscribers.Fields.GetByName(name), name)
	}

	// Duplicate addresses per page are rejected; empty token hashes are not unique.
	users := mustCollection(t, app, "users")
	user := core.NewRecord(users)
	user.Set("email", "owner@example.com")
	user.Set("password", "testtesttest")
	require.NoError(t, app.Save(user))
	pageRecord := core.NewRecord(pages)
	pageRecord.Set("user", user.Id)
	pageRecord.Set("slug", "page")
	pageRecord.Set("title", "Page")
	require.NoError(t, app.Save(pageRecord))
	add := func(email string) error {
		record := core.NewRecord(subscribers)
		record.Set("page", pageRecord.Id)
		record.Set("email", email)
		return app.Save(record)
	}
	require.NoError(t, add("a@example.com"))
	require.NoError(t, add("b@example.com"))
	assert.Error(t, add("a@example.com"))

	migration := findMigration(t, "_status_subscriptions.go")
	require.NoError(t, migration.Down(app))
	_, err = app.FindCollectionByNameOrId("status_subscribers")
	assert.Error(t, err)
	assert.Nil(t, mustCollection(t, app, "status_pages").Fields.GetByName("allowSubscriptions"))
	require.NoError(t, migration.Up(app))
	mustCollection(t, app, "status_subscribers")
}
