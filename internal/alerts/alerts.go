// Package alerts handles alert management and delivery.
package alerts

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type hubLike interface {
	core.App
	MakeLink(parts ...string) string
}

type AlertManager struct {
	hub             hubLike
	stopOnce        sync.Once
	pendingAlerts   sync.Map
	alertsCache     *AlertsCache
	networkMonitors *networkMonitorCache
	// monitorThresholds caches monitor threshold alert configuration.
	monitorThresholds *monitorThresholdCache
	// inMaintenance reports whether a monitor is in a maintenance window; nil for never.
	inMaintenance func(monitorID string, now time.Time) bool
	// monitorSuppressed reports whether a monitor's alerts are suppressed
	// because a monitor it depends on is down; nil for never.
	monitorSuppressed func(monitorID string) bool
	// systemSuppressed reports whether a system's status alerts are
	// suppressed because a monitor it depends on is down; nil for never.
	systemSuppressed func(systemID string) bool
	// ackSecret overrides the key of acknowledgement links (tests).
	ackSecret []byte
	// browserSender delivers browser (Web Push) channels; nil disables them.
	browserSender BrowserSender
}

type AlertMessageData struct {
	UserID   string
	SystemID string
	Title    string
	Message  string
	Link     string
	LinkText string
	// HistoryID is the open alerts_history row the message notifies, if
	// any; notifications of it include a one-click acknowledgement link.
	HistoryID string
	// Severity routes the message to channels (empty: warning).
	Severity Severity
	// Channels are explicit channel ids (empty: default routing).
	Channels []string
	// Name is the system or monitor name, Value the alert's value and
	// Status "triggered", "resolved" or "reminder"; for templates.
	Name   string
	Value  string
	Status string
	// MonitorID and AlertType identify the alert (browser notification tag).
	MonitorID string
	AlertType string
	// AckLink is set on delivery from HistoryID.
	AckLink string
}

// AlertMessage is the message of an alert.
type AlertMessage = AlertMessageData

type UserNotificationSettings struct {
	Emails   []string `json:"emails"`
	Webhooks []string `json:"webhooks"`
}

type SystemAlertFsStats struct {
	DiskTotal float64 `json:"d"`
	DiskUsed  float64 `json:"du"`
}

// Values pulled from system_stats.stats that are relevant to alerts.
type SystemAlertStats struct {
	Cpu          float64                       `json:"cpu"`
	CpuBreakdown []float64                     `json:"cpub"`
	Mem          float64                       `json:"mp"`
	Disk         float64                       `json:"dp"`
	Bandwidth    [2]uint64                     `json:"b"`
	GPU          map[string]SystemAlertGPUData `json:"g"`
	Temperatures map[string]float32            `json:"t"`
	// DashboardTemp is stored by the hub with 1m records; older records lack it.
	DashboardTemp float32                       `json:"dt"`
	LoadAvg       [3]float64                    `json:"la"`
	Battery       [2]uint8                      `json:"bat"`
	Batteries     map[string]uint8              `json:"bats"`
	ExtraFs       map[string]SystemAlertFsStats `json:"efs"`
	ZfsPools      map[string]SystemAlertZfsPool `json:"z"`
}

type SystemAlertGPUData struct {
	Usage float64 `json:"u"`
}

type SystemAlertZfsPool struct {
	Raw   bool    `json:"raw,omitempty"`
	Total float64 `json:"d"`
	Used  float64 `json:"du"`
}

type SystemAlertData struct {
	systemRecord *core.Record
	alertData    CachedAlertData
	name         string
	unit         string
	val          float64
	threshold    float64
	triggered    bool
	time         time.Time
	count        uint8
	min          uint8
	mapSums      map[string]float32
	descriptor   string // override descriptor in notification body (for temp sensor, disk partition, etc)
}

// notification services that support title param
var supportsTitle = map[string]struct{}{
	"bark":       {},
	"discord":    {},
	"gotify":     {},
	"ifttt":      {},
	"join":       {},
	"lark":       {},
	"ntfy":       {},
	"opsgenie":   {},
	"pushbullet": {},
	"pushover":   {},
	"slack":      {},
	"teams":      {},
	"telegram":   {},
	"zulip":      {},
}

// NewAlertManager creates a new AlertManager instance.
func NewAlertManager(app hubLike) *AlertManager {
	am := &AlertManager{
		hub:               app,
		alertsCache:       NewAlertsCache(app),
		networkMonitors:   newNetworkMonitorCache(app),
		monitorThresholds: newMonitorThresholdCache(app),
	}
	am.bindEvents()
	return am
}

// Bind events to the alerts collection lifecycle
func (am *AlertManager) bindEvents() {
	am.bindNetworkMonitorAlertEvents()
	am.hub.OnRecordAfterUpdateSuccess("alerts").BindFunc(updateHistoryOnAlertUpdate)
	am.hub.OnRecordAfterDeleteSuccess("alerts").BindFunc(resolveHistoryOnAlertDelete)
	am.hub.OnRecordAfterDeleteSuccess("network_monitors").BindFunc(resolveDeletedMonitorHistory)
	am.hub.OnRecordAfterUpdateSuccess("network_monitors").BindFunc(am.resolveMonitorThresholdsOnUpdate)
	am.hub.OnRecordAfterUpdateSuccess("smart_devices").BindFunc(am.handleSmartDeviceAlert)
	am.hub.OnRecordAfterCreateSuccess("zfs_pools").BindFunc(am.handleZfsPoolCreateAlert)
	am.hub.OnRecordAfterUpdateSuccess("zfs_pools").BindFunc(am.handleZfsPoolAlert)
	am.hub.OnRecordAfterDeleteSuccess("zfs_pools").BindFunc(resolveZfsPoolHistoryOnDelete)
	am.hub.OnRecordCreateRequest("user_settings").BindFunc(validateReminderSettings)
	am.hub.OnRecordUpdateRequest("user_settings").BindFunc(validateReminderSettings)
	am.bindChannelEvents()

	am.hub.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Populate all alerts into cache on startup
		_ = am.alertsCache.PopulateFromDB(true)

		if err := resolveStatusAlerts(e.App); err != nil {
			e.App.Logger().Error("Failed to resolve stale status alerts", "err", err)
		}
		if err := resolveSystemdAlerts(e.App); err != nil {
			e.App.Logger().Error("Failed to resolve stale systemd alerts", "err", err)
		}
		if err := am.restorePendingStatusAlerts(); err != nil {
			e.App.Logger().Error("Failed to restore pending status alerts", "err", err)
		}
		return e.Next()
	})
}

// IsNotificationSilenced checks if a notification should be silenced based on configured quiet hours
func (am *AlertManager) IsNotificationSilenced(userID, systemID string) bool {
	return am.isNotificationSilencedAt(userID, systemID, time.Now())
}

// isNotificationSilencedAt checks if quiet hours silence notifications at now.
func (am *AlertManager) isNotificationSilencedAt(userID, systemID string, now time.Time) bool {
	// Query for quiet hours windows that match this user and system
	// Include both global windows (system is null/empty) and system-specific windows
	var filter string
	var params dbx.Params

	if systemID == "" {
		// If no systemID provided, only check global windows
		filter = "user={:user} AND system=''"
		params = dbx.Params{"user": userID}
	} else {
		// Check both global and system-specific windows
		filter = "user={:user} AND (system='' OR system={:system})"
		params = dbx.Params{
			"user":   userID,
			"system": systemID,
		}
	}

	quietHourWindows, err := am.hub.FindAllRecords("quiet_hours", dbx.NewExp(filter, params))
	if err != nil || len(quietHourWindows) == 0 {
		return false
	}

	now = now.UTC()
	for _, window := range quietHourWindows {
		start := window.GetDateTime("start").Time()
		end := window.GetDateTime("end").Time()
		if WindowActive(window.GetString("type"), start, end, now) {
			return true
		}
	}
	return false
}

// SendAlert sends an alert to the user's channels, unless quiet hours
// silence it.
func (am *AlertManager) SendAlert(data AlertMessageData) error {
	if am.silencedAt(data.UserID, data.SystemID, data.Severity, time.Now()) {
		am.hub.Logger().Info("Notification silenced", "user", data.UserID, "system", data.SystemID, "title", data.Title)
		return nil
	}
	return am.deliverAlert(data)
}

// silencedAt reports whether quiet hours silence an alert of severity at
// now. Critical alerts bypass quiet hours when the user opted in.
func (am *AlertManager) silencedAt(userID, systemID string, severity Severity, now time.Time) bool {
	if !am.isNotificationSilencedAt(userID, systemID, now) {
		return false
	}
	if severity.orDefault(SeverityWarning) == SeverityCritical {
		if prefs, ok, _ := am.loadUserPrefs(userID); ok && prefs.CriticalBypassQuietHours {
			return false
		}
	}
	return true
}

// SendShoutrrrAlert sends an alert via a Shoutrrr URL
func (am *AlertManager) SendShoutrrrAlert(notificationUrl, title, message, link, linkText string) error {
	return am.sendShoutrrrAlert(notificationUrl, title, message, link, linkText, shoutrrr.Send)
}

func (am *AlertManager) sendShoutrrrAlert(notificationUrl, title, message, link, linkText string, send func(string, string) error) error {
	// Parse the URL
	parsedURL, err := url.Parse(notificationUrl)
	if err != nil {
		return fmt.Errorf("error parsing URL: %v", err)
	}
	scheme := parsedURL.Scheme
	queryParams := parsedURL.Query()

	// Add title
	if _, ok := supportsTitle[scheme]; ok {
		queryParams.Add("title", title)
	} else if scheme == "mattermost" {
		// use markdown title for mattermost
		message = "##### " + title + "\n\n" + message
	} else if scheme == "generic" && queryParams.Has("template") {
		// add title as property if using generic with template json
		titleKey := queryParams.Get("titlekey")
		if titleKey == "" {
			titleKey = "title"
		}
		queryParams.Add("$"+titleKey, title)
	} else {
		// otherwise just add title to message
		message = title + "\n\n" + message
	}

	// Add link
	switch {
	case link == "":
	case scheme == "ntfy":
		queryParams.Add("Actions", fmt.Sprintf("view, %s, %s", linkText, link))
	case scheme == "lark":
		queryParams.Add("link", link)
	case scheme == "bark":
		queryParams.Add("url", link)
	default:
		message += "\n\n" + link
	}

	// Encode the modified query parameters back into the URL
	parsedURL.RawQuery = queryParams.Encode()
	// log.Println("URL after modification:", parsedURL.String())

	err = send(parsedURL.String(), message)

	if err == nil {
		am.hub.Logger().Info("Sent shoutrrr alert", "title", title)
	} else {
		am.hub.Logger().Error("Error sending shoutrrr alert", "err", err)
		return err
	}
	return nil
}

// setAlertTriggered updates the "triggered" status of an alert record in the database
func (am *AlertManager) setAlertTriggered(alert CachedAlertData, triggered bool) error {
	alertRecord, err := am.hub.FindRecordById("alerts", alert.Id)
	if err != nil {
		return err
	}
	alertRecord.Set("triggered", triggered)
	return am.hub.Save(alertRecord)
}
