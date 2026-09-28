package migrations

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Collection ids of the uptime monitoring collections.
const (
	monitorEventsCollectionId      = "nm_events_001"
	statusPagesCollectionId        = "status_pages_001"
	monitorMaintenanceCollectionId = "nm_maint_001"
	networkMonitorsCollectionId    = "nm_monitors_001"
)

// networkMonitorUptimeFields are the fields added to network_monitors, removed again on down.
var networkMonitorUptimeFields = []string{
	"users", "name", "timeout", "retries", "retryInterval", "http", "httpSecrets", "notify",
	"certExpiryDays", "pushToken", "status", "statusChanged", "lastCheck", "lastError",
	"lastStatusCode", "recent", "uptime", "state",
}

func init() {
	m.Register(func(app core.App) error {
		if err := upNetworkMonitors(app); err != nil {
			return err
		}
		if err := setRelationRequired(app, "network_monitor_stats", "system", false); err != nil {
			return err
		}
		for _, collection := range []*core.Collection{newMonitorEventsCollection(), newStatusPagesCollection(), newMonitorMaintenanceCollection()} {
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return upAlertsHistory(app)
	}, func(app core.App) error {
		for _, id := range []string{monitorMaintenanceCollectionId, statusPagesCollectionId, monitorEventsCollectionId} {
			collection, err := app.FindCollectionByNameOrId(id)
			if err != nil {
				return err
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		if err := downAlertsHistory(app); err != nil {
			return err
		}
		// Hub and push monitors cannot exist without a system.
		hubMonitors, err := app.FindAllRecords("network_monitors", dbx.HashExp{"system": ""})
		if err != nil {
			return err
		}
		for _, record := range hubMonitors {
			if err := app.Delete(record); err != nil {
				return err
			}
		}
		if err := setRelationRequired(app, "network_monitor_stats", "system", true); err != nil {
			return err
		}
		return downNetworkMonitors(app)
	})
}

func upNetworkMonitors(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("network_monitors")
	if err != nil {
		return err
	}
	collection.Fields.GetByName("system").(*core.RelationField).Required = false
	target := collection.Fields.GetByName("target").(*core.TextField)
	target.Required = false
	target.Min = 0
	protocol := collection.Fields.GetByName("protocol").(*core.SelectField)
	protocol.Values = []string{"icmp", "tcp", "http", "dns", "push"}

	collection.Fields.Add(
		&core.RelationField{Id: "nm_users", Name: "users", CollectionId: "_pb_users_auth_", MaxSelect: 99},
		&core.TextField{Id: "nm_name", Name: "name", Max: 100},
		&core.NumberField{Id: "nm_timeout", Name: "timeout", Min: new(float64(0)), Max: new(float64(60)), OnlyInt: true},
		&core.NumberField{Id: "nm_retries", Name: "retries", Min: new(float64(0)), Max: new(float64(10)), OnlyInt: true},
		&core.NumberField{Id: "nm_retry_interval", Name: "retryInterval", Min: new(float64(0)), Max: new(float64(3600)), OnlyInt: true},
		// Non-secret HTTP options; credentials, headers and body live in httpSecrets.
		&core.JSONField{Id: "nm_http", Name: "http", MaxSize: 64 << 10},
		&core.JSONField{Id: "nm_http_secrets", Name: "httpSecrets", MaxSize: 512 << 10},
		&core.BoolField{Id: "nm_notify", Name: "notify"},
		&core.NumberField{Id: "nm_cert_expiry_days", Name: "certExpiryDays", Min: new(float64(0)), Max: new(float64(365)), OnlyInt: true},
		&core.TextField{Id: "nm_push_token", Name: "pushToken", Max: 64},
		// Server-managed status fields.
		&core.SelectField{Id: "nm_status", Name: "status", MaxSelect: 1, Values: []string{"up", "down", "pending", "maintenance", "paused", "unknown"}},
		&core.DateField{Id: "nm_status_changed", Name: "statusChanged"},
		&core.NumberField{Id: "nm_last_check", Name: "lastCheck", Help: "Unix timestamp in milliseconds"},
		&core.TextField{Id: "nm_last_error", Name: "lastError", Max: 300},
		&core.NumberField{Id: "nm_last_status_code", Name: "lastStatusCode"},
		&core.JSONField{Id: "nm_recent", Name: "recent"},
		&core.JSONField{Id: "nm_uptime", Name: "uptime"},
		&core.JSONField{Id: "nm_state", Name: "state", Hidden: true},
	)
	collection.AddIndex("idx_nm_push_token", true, "pushToken", "pushToken != ''")
	if err := app.Save(collection); err != nil {
		return err
	}
	_, err = app.DB().NewQuery("UPDATE network_monitors SET status = 'unknown'").Execute()
	return err
}

func downNetworkMonitors(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("network_monitors")
	if err != nil {
		return err
	}
	collection.RemoveIndex("idx_nm_push_token")
	for _, name := range networkMonitorUptimeFields {
		collection.Fields.RemoveByName(name)
	}
	collection.Fields.GetByName("system").(*core.RelationField).Required = true
	target := collection.Fields.GetByName("target").(*core.TextField)
	target.Required = true
	target.Min = 1
	protocol := collection.Fields.GetByName("protocol").(*core.SelectField)
	protocol.Values = []string{"icmp", "tcp", "http", "dns"}
	return app.Save(collection)
}

func upAlertsHistory(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("alerts_history")
	if err != nil {
		return err
	}
	collection.Fields.GetByName("system").(*core.RelationField).Required = false
	// name is a text field, so monitor alert names need no schema change.
	collection.Fields.Add(&core.RelationField{Id: "ah_monitor", Name: "monitor", CollectionId: networkMonitorsCollectionId, MaxSelect: 1})
	return app.Save(collection)
}

func downAlertsHistory(app core.App) error {
	if _, err := app.DB().NewQuery("DELETE FROM alerts_history WHERE system = ''").Execute(); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId("alerts_history")
	if err != nil {
		return err
	}
	collection.Fields.RemoveByName("monitor")
	collection.Fields.GetByName("system").(*core.RelationField).Required = true
	return app.Save(collection)
}

func setRelationRequired(app core.App, collectionName, fieldName string, required bool) error {
	collection, err := app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return err
	}
	collection.Fields.GetByName(fieldName).(*core.RelationField).Required = required
	return app.Save(collection)
}

func newMonitorEventsCollection() *core.Collection {
	collection := core.NewBaseCollection("monitor_events", monitorEventsCollectionId)
	collection.Fields.Add(
		&core.RelationField{Id: "me_monitor", Name: "monitor", CollectionId: networkMonitorsCollectionId, MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.SelectField{Id: "me_status", Name: "status", MaxSelect: 1, Required: true, Values: []string{"up", "down", "maintenance", "unknown", "paused"}},
		&core.NumberField{Id: "me_start", Name: "start", Help: "Unix timestamp in milliseconds"},
		&core.NumberField{Id: "me_end", Name: "end", Help: "Unix timestamp in milliseconds; 0 while open"},
		&core.TextField{Id: "me_error", Name: "error", Max: 300},
		&core.NumberField{Id: "me_status_code", Name: "statusCode"},
	)
	collection.AddIndex("idx_me_monitor_start", false, "monitor, start", "")
	return collection
}

func newStatusPagesCollection() *core.Collection {
	collection := core.NewBaseCollection("status_pages", statusPagesCollectionId)
	collection.Fields.Add(
		&core.RelationField{Id: "sp_user", Name: "user", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.TextField{Id: "sp_slug", Name: "slug", Required: true, Pattern: `^[a-z0-9][a-z0-9-]{1,62}$`},
		&core.TextField{Id: "sp_title", Name: "title", Required: true, Max: 200},
		&core.TextField{Id: "sp_description", Name: "description", Max: 2000},
		&core.RelationField{Id: "sp_monitors", Name: "monitors", CollectionId: networkMonitorsCollectionId, MaxSelect: 100},
		&core.BoolField{Id: "sp_public", Name: "public"},
		&core.BoolField{Id: "sp_show_targets", Name: "showTargets"},
		&core.BoolField{Id: "sp_show_response_times", Name: "showResponseTimes"},
		&core.AutodateField{Id: "sp_created", Name: "created", OnCreate: true},
		&core.AutodateField{Id: "sp_updated", Name: "updated", OnCreate: true, OnUpdate: true},
	)
	collection.AddIndex("idx_sp_slug", true, "slug", "")
	return collection
}

func newMonitorMaintenanceCollection() *core.Collection {
	collection := core.NewBaseCollection("monitor_maintenance", monitorMaintenanceCollectionId)
	collection.Fields.Add(
		&core.RelationField{Id: "mm_user", Name: "user", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.TextField{Id: "mm_title", Name: "title", Required: true, Max: 200},
		&core.TextField{Id: "mm_description", Name: "description", Max: 2000},
		&core.SelectField{Id: "mm_type", Name: "type", MaxSelect: 1, Required: true, Values: []string{"one-time", "daily"}},
		&core.DateField{Id: "mm_start", Name: "start", Required: true},
		&core.DateField{Id: "mm_end", Name: "end", Required: true},
		// Required, since minSelect alone does not reject an empty selection.
		&core.RelationField{Id: "mm_monitors", Name: "monitors", CollectionId: networkMonitorsCollectionId, MinSelect: 1, MaxSelect: 999, Required: true},
		&core.BoolField{Id: "mm_show_on_status_pages", Name: "showOnStatusPages"},
		&core.AutodateField{Id: "mm_created", Name: "created", OnCreate: true},
		&core.AutodateField{Id: "mm_updated", Name: "updated", OnCreate: true, OnUpdate: true},
	)
	collection.AddIndex("idx_mm_user", false, "user", "")
	return collection
}
