//go:build testing

package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findMigration(t *testing.T, suffix string) *core.Migration {
	t.Helper()
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasSuffix(migration.File, suffix) {
			return migration
		}
	}
	t.Fatalf("migration %s is not registered", suffix)
	return nil
}

// queryPlan returns the EXPLAIN QUERY PLAN details of query.
func queryPlan(t *testing.T, app core.App, query string) string {
	t.Helper()
	var rows []struct {
		Detail string `db:"detail"`
	}
	require.NoError(t, app.DB().NewQuery("EXPLAIN QUERY PLAN "+query).All(&rows))
	details := make([]string, len(rows))
	for i, row := range rows {
		details[i] = row.Detail
	}
	return strings.Join(details, "\n")
}

func TestStatsTypeCreatedIndexMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	assertIndexes := func(present bool) {
		t.Helper()
		for collectionName, indexName := range statsTypeCreatedIndexes {
			collection := mustCollection(t, app, collectionName)
			if present {
				assert.NotEmpty(t, collection.GetIndex(indexName), indexName)
			} else {
				assert.Empty(t, collection.GetIndex(indexName), indexName)
			}
		}
	}
	assertIndexes(true)

	// The retention sweep deletes by type and age; it must use the new index.
	for collectionName, indexName := range statsTypeCreatedIndexes {
		plan := queryPlan(t, app, "DELETE FROM "+collectionName+" WHERE type='1m' AND created<'2000-01-01 00:00:00.000Z'")
		assert.Contains(t, plan, indexName, collectionName)
	}

	migration := findMigration(t, "_stats_type_created_index.go")
	require.NoError(t, migration.Down(app))
	assertIndexes(false)
	require.NoError(t, migration.Up(app))
	assertIndexes(true)
}
