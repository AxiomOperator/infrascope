package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// statsTypeCreatedIndexes are (type, created) indexes for the retention sweep,
// which deletes by type and age across all systems. The existing
// (system, type, created) indexes can't serve those deletes because system is
// their leading column.
var statsTypeCreatedIndexes = map[string]string{
	"system_stats":    "idx_ss_type_created",
	"container_stats": "idx_cs_type_created",
}

func init() {
	m.Register(func(app core.App) error {
		for collectionName, indexName := range statsTypeCreatedIndexes {
			collection, err := app.FindCollectionByNameOrId(collectionName)
			if err != nil {
				return err
			}
			collection.AddIndex(indexName, false, "`type`, `created`", "")
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for collectionName, indexName := range statsTypeCreatedIndexes {
			collection, err := app.FindCollectionByNameOrId(collectionName)
			if err != nil {
				return err
			}
			collection.RemoveIndex(indexName)
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
