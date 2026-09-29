//go:build testing

package hub_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/pocketbase/core"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionRulesDefault(t *testing.T) {
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	const isUserMatchesUser = `@request.auth.id != "" && user = @request.auth.id`

	const isUserInUsers = `@request.auth.id != "" && users.id ?= @request.auth.id`
	const isUserInUsersNotReadonly = `@request.auth.id != "" && users.id ?= @request.auth.id && @request.auth.role != "readonly"`

	const isUserInSystemUsers = `@request.auth.id != "" && system.users.id ?= @request.auth.id`
	const isUserInSystemUsersNotReadonly = `@request.auth.id != "" && system.users.id ?= @request.auth.id && @request.auth.role != "readonly"`

	// users collection
	usersCollection, err := hub.FindCollectionByNameOrId("users")
	assert.NoError(t, err, "Failed to find users collection")
	assert.True(t, usersCollection.PasswordAuth.Enabled)
	assert.Equal(t, usersCollection.PasswordAuth.IdentityFields, []string{"email"})
	assert.Nil(t, usersCollection.CreateRule)
	assert.False(t, usersCollection.MFA.Enabled)

	// superusers collection
	superusersCollection, err := hub.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	assert.NoError(t, err, "Failed to find superusers collection")
	assert.True(t, superusersCollection.PasswordAuth.Enabled)
	assert.Equal(t, superusersCollection.PasswordAuth.IdentityFields, []string{"email"})
	assert.Nil(t, superusersCollection.CreateRule)
	assert.False(t, superusersCollection.MFA.Enabled)

	// alerts collection
	alertsCollection, err := hub.FindCollectionByNameOrId("alerts")
	require.NoError(t, err, "Failed to find alerts collection")
	assert.Equal(t, isUserMatchesUser, *alertsCollection.ListRule)
	assert.Nil(t, alertsCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser+` && system.users.id ?= @request.auth.id`, *alertsCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser+` && (@request.body.user:changed = false || @request.body.user = @request.auth.id) && (@request.body.system:changed = false || @request.body.system.users.id ?= @request.auth.id)`, *alertsCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *alertsCollection.DeleteRule)
	alertNames := alertsCollection.Fields.GetByName("name").(*core.SelectField).Values
	for _, name := range []string{"CPUIOWait", "CPUSteal"} {
		assert.Contains(t, alertNames, name)
	}
	for _, name := range []string{"CPUSystem", "CPUUser", "CPUIdle", "CPUOther"} {
		assert.NotContains(t, alertNames, name)
	}

	// alerts_history collection
	alertsHistoryCollection, err := hub.FindCollectionByNameOrId("alerts_history")
	require.NoError(t, err, "Failed to find alerts_history collection")
	assert.Equal(t, isUserMatchesUser, *alertsHistoryCollection.ListRule)
	assert.Nil(t, alertsHistoryCollection.ViewRule)
	assert.Nil(t, alertsHistoryCollection.CreateRule)
	assert.Nil(t, alertsHistoryCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *alertsHistoryCollection.DeleteRule)

	// containers collection
	containersCollection, err := hub.FindCollectionByNameOrId("containers")
	require.NoError(t, err, "Failed to find containers collection")
	assert.Equal(t, isUserInSystemUsers, *containersCollection.ListRule)
	assert.Nil(t, containersCollection.ViewRule)
	assert.Nil(t, containersCollection.CreateRule)
	assert.Nil(t, containersCollection.UpdateRule)
	assert.Nil(t, containersCollection.DeleteRule)

	// container_stats collection
	containerStatsCollection, err := hub.FindCollectionByNameOrId("container_stats")
	require.NoError(t, err, "Failed to find container_stats collection")
	assert.Equal(t, isUserInSystemUsers, *containerStatsCollection.ListRule)
	assert.Nil(t, containerStatsCollection.ViewRule)
	assert.Nil(t, containerStatsCollection.CreateRule)
	assert.Nil(t, containerStatsCollection.UpdateRule)
	assert.Nil(t, containerStatsCollection.DeleteRule)

	// fingerprints collection
	fingerprintsCollection, err := hub.FindCollectionByNameOrId("fingerprints")
	require.NoError(t, err, "Failed to find fingerprints collection")
	assert.Equal(t, isUserInSystemUsersNotReadonly, *fingerprintsCollection.ListRule)
	assert.Equal(t, isUserInSystemUsersNotReadonly, *fingerprintsCollection.ViewRule)
	assert.Equal(t, isUserInSystemUsersNotReadonly, *fingerprintsCollection.CreateRule)
	assert.Equal(t, isUserInSystemUsersNotReadonly, *fingerprintsCollection.UpdateRule)
	assert.Equal(t, isUserInSystemUsersNotReadonly, *fingerprintsCollection.DeleteRule)

	// network_monitors collection
	const isMonitorUser = `@request.auth.id != "" && ((system != "" && system.users.id ?= @request.auth.id) || (system = "" && users.id ?= @request.auth.id))`
	const isMonitorUserNotReadonly = isMonitorUser + ` && @request.auth.role != "readonly"`
	networkMonitorsCollection, err := hub.FindCollectionByNameOrId("network_monitors")
	require.NoError(t, err, "Failed to find network_monitors collection")
	assert.Equal(t, isMonitorUser, *networkMonitorsCollection.ListRule)
	assert.Equal(t, isMonitorUser, *networkMonitorsCollection.ViewRule)
	assert.Equal(t, isMonitorUserNotReadonly, *networkMonitorsCollection.CreateRule)
	assert.Equal(t, isMonitorUserNotReadonly+` && (@request.body.system:changed = false || @request.body.system = "" || @request.body.system.users.id ?= @request.auth.id) && (@request.body.users:changed = false || @request.body.users.id ?= @request.auth.id)`, *networkMonitorsCollection.UpdateRule)
	assert.Equal(t, isMonitorUserNotReadonly, *networkMonitorsCollection.DeleteRule)

	// network_monitor_stats and monitor_events collections
	const isMonitorDataUser = `@request.auth.id != "" && ((monitor.system != "" && monitor.system.users.id ?= @request.auth.id) || (monitor.system = "" && monitor.users.id ?= @request.auth.id))`
	networkMonitorStatsCollection, err := hub.FindCollectionByNameOrId("network_monitor_stats")
	require.NoError(t, err, "Failed to find network_monitor_stats collection")
	assert.Equal(t, isMonitorDataUser, *networkMonitorStatsCollection.ListRule)
	assert.Nil(t, networkMonitorStatsCollection.ViewRule)
	assert.Nil(t, networkMonitorStatsCollection.CreateRule)
	monitorEventsCollection, err := hub.FindCollectionByNameOrId("monitor_events")
	require.NoError(t, err, "Failed to find monitor_events collection")
	assert.Equal(t, isMonitorDataUser, *monitorEventsCollection.ListRule)
	assert.Equal(t, isMonitorDataUser, *monitorEventsCollection.ViewRule)
	assert.Nil(t, monitorEventsCollection.CreateRule)
	assert.Nil(t, monitorEventsCollection.UpdateRule)
	assert.Nil(t, monitorEventsCollection.DeleteRule)

	// status_pages and monitor_maintenance collections
	assertOwnerRules(t, hub)

	// quiet_hours collection
	quietHoursCollection, err := hub.FindCollectionByNameOrId("quiet_hours")
	require.NoError(t, err, "Failed to find quiet_hours collection")
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.ListRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.DeleteRule)

	// smart_devices collection
	smartDevicesCollection, err := hub.FindCollectionByNameOrId("smart_devices")
	require.NoError(t, err, "Failed to find smart_devices collection")
	assert.Equal(t, isUserInSystemUsers, *smartDevicesCollection.ListRule)
	assert.Equal(t, isUserInSystemUsers, *smartDevicesCollection.ViewRule)
	assert.Nil(t, smartDevicesCollection.CreateRule)
	assert.Nil(t, smartDevicesCollection.UpdateRule)
	assert.Equal(t, isUserInSystemUsersNotReadonly, *smartDevicesCollection.DeleteRule)

	// system_details collection
	systemDetailsCollection, err := hub.FindCollectionByNameOrId("system_details")
	require.NoError(t, err, "Failed to find system_details collection")
	assert.Equal(t, isUserInSystemUsers, *systemDetailsCollection.ListRule)
	assert.Equal(t, isUserInSystemUsers, *systemDetailsCollection.ViewRule)
	assert.Nil(t, systemDetailsCollection.CreateRule)
	assert.Nil(t, systemDetailsCollection.UpdateRule)
	assert.Nil(t, systemDetailsCollection.DeleteRule)

	// system_stats collection
	systemStatsCollection, err := hub.FindCollectionByNameOrId("system_stats")
	require.NoError(t, err, "Failed to find system_stats collection")
	assert.Equal(t, isUserInSystemUsers, *systemStatsCollection.ListRule)
	assert.Nil(t, systemStatsCollection.ViewRule)
	assert.Nil(t, systemStatsCollection.CreateRule)
	assert.Nil(t, systemStatsCollection.UpdateRule)
	assert.Nil(t, systemStatsCollection.DeleteRule)

	// systemd_services collection
	systemdServicesCollection, err := hub.FindCollectionByNameOrId("systemd_services")
	require.NoError(t, err, "Failed to find systemd_services collection")
	assert.Equal(t, isUserInSystemUsers, *systemdServicesCollection.ListRule)
	assert.Nil(t, systemdServicesCollection.ViewRule)
	assert.Nil(t, systemdServicesCollection.CreateRule)
	assert.Nil(t, systemdServicesCollection.UpdateRule)
	assert.Nil(t, systemdServicesCollection.DeleteRule)

	// systems collection
	systemsCollection, err := hub.FindCollectionByNameOrId("systems")
	require.NoError(t, err, "Failed to find systems collection")
	assert.Equal(t, isUserInUsers, *systemsCollection.ListRule)
	assert.Equal(t, isUserInUsers, *systemsCollection.ViewRule)
	assert.Equal(t, isUserInUsersNotReadonly, *systemsCollection.CreateRule)
	assert.Equal(t, isUserInUsersNotReadonly, *systemsCollection.UpdateRule)
	assert.Equal(t, isUserInUsersNotReadonly, *systemsCollection.DeleteRule)

	// universal_tokens collection
	universalTokensCollection, err := hub.FindCollectionByNameOrId("universal_tokens")
	require.NoError(t, err, "Failed to find universal_tokens collection")
	assert.Nil(t, universalTokensCollection.ListRule)
	assert.Nil(t, universalTokensCollection.ViewRule)
	assert.Nil(t, universalTokensCollection.CreateRule)
	assert.Nil(t, universalTokensCollection.UpdateRule)
	assert.Nil(t, universalTokensCollection.DeleteRule)

	// user_settings collection
	userSettingsCollection, err := hub.FindCollectionByNameOrId("user_settings")
	require.NoError(t, err, "Failed to find user_settings collection")
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.ListRule)
	assert.Nil(t, userSettingsCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.UpdateRule)
	assert.Nil(t, userSettingsCollection.DeleteRule)
}

func TestCollectionRulesShareAllSystems(t *testing.T) {
	t.Setenv("SHARE_ALL_SYSTEMS", "true")
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	const isUser = `@request.auth.id != ""`
	const isUserNotReadonly = `@request.auth.id != "" && @request.auth.role != "readonly"`

	const isUserMatchesUser = `@request.auth.id != "" && user = @request.auth.id`

	// alerts collection
	alertsCollection, err := hub.FindCollectionByNameOrId("alerts")
	require.NoError(t, err, "Failed to find alerts collection")
	assert.Equal(t, isUserMatchesUser, *alertsCollection.ListRule)
	assert.Nil(t, alertsCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser, *alertsCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser+` && (@request.body.user:changed = false || @request.body.user = @request.auth.id)`, *alertsCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *alertsCollection.DeleteRule)

	// alerts_history collection
	alertsHistoryCollection, err := hub.FindCollectionByNameOrId("alerts_history")
	require.NoError(t, err, "Failed to find alerts_history collection")
	assert.Equal(t, isUserMatchesUser, *alertsHistoryCollection.ListRule)
	assert.Nil(t, alertsHistoryCollection.ViewRule)
	assert.Nil(t, alertsHistoryCollection.CreateRule)
	assert.Nil(t, alertsHistoryCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *alertsHistoryCollection.DeleteRule)

	// containers collection
	containersCollection, err := hub.FindCollectionByNameOrId("containers")
	require.NoError(t, err, "Failed to find containers collection")
	assert.Equal(t, isUser, *containersCollection.ListRule)
	assert.Nil(t, containersCollection.ViewRule)
	assert.Nil(t, containersCollection.CreateRule)
	assert.Nil(t, containersCollection.UpdateRule)
	assert.Nil(t, containersCollection.DeleteRule)

	// container_stats collection
	containerStatsCollection, err := hub.FindCollectionByNameOrId("container_stats")
	require.NoError(t, err, "Failed to find container_stats collection")
	assert.Equal(t, isUser, *containerStatsCollection.ListRule)
	assert.Nil(t, containerStatsCollection.ViewRule)
	assert.Nil(t, containerStatsCollection.CreateRule)
	assert.Nil(t, containerStatsCollection.UpdateRule)
	assert.Nil(t, containerStatsCollection.DeleteRule)

	// fingerprints collection
	fingerprintsCollection, err := hub.FindCollectionByNameOrId("fingerprints")
	require.NoError(t, err, "Failed to find fingerprints collection")
	assert.Equal(t, isUserNotReadonly, *fingerprintsCollection.ListRule)
	assert.Equal(t, isUserNotReadonly, *fingerprintsCollection.ViewRule)
	assert.Equal(t, isUserNotReadonly, *fingerprintsCollection.CreateRule)
	assert.Equal(t, isUserNotReadonly, *fingerprintsCollection.UpdateRule)
	assert.Equal(t, isUserNotReadonly, *fingerprintsCollection.DeleteRule)

	// network_monitors collection
	networkMonitorsCollection, err := hub.FindCollectionByNameOrId("network_monitors")
	require.NoError(t, err, "Failed to find network_monitors collection")
	assert.Equal(t, isUser, *networkMonitorsCollection.ListRule)
	assert.Equal(t, isUser, *networkMonitorsCollection.ViewRule)
	assert.Equal(t, isUserNotReadonly, *networkMonitorsCollection.CreateRule)
	assert.Equal(t, isUserNotReadonly+` && (@request.body.users:changed = false || @request.body.users.id ?= @request.auth.id)`, *networkMonitorsCollection.UpdateRule)
	assert.Equal(t, isUserNotReadonly, *networkMonitorsCollection.DeleteRule)

	// network_monitor_stats and monitor_events collections
	networkMonitorStatsCollection, err := hub.FindCollectionByNameOrId("network_monitor_stats")
	require.NoError(t, err, "Failed to find network_monitor_stats collection")
	assert.Equal(t, isUser, *networkMonitorStatsCollection.ListRule)
	monitorEventsCollection, err := hub.FindCollectionByNameOrId("monitor_events")
	require.NoError(t, err, "Failed to find monitor_events collection")
	assert.Equal(t, isUser, *monitorEventsCollection.ListRule)
	assert.Equal(t, isUser, *monitorEventsCollection.ViewRule)

	// status_pages and monitor_maintenance collections
	assertOwnerRules(t, hub)

	// quiet_hours collection
	quietHoursCollection, err := hub.FindCollectionByNameOrId("quiet_hours")
	require.NoError(t, err, "Failed to find quiet_hours collection")
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.ListRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.UpdateRule)
	assert.Equal(t, isUserMatchesUser, *quietHoursCollection.DeleteRule)

	// smart_devices collection
	smartDevicesCollection, err := hub.FindCollectionByNameOrId("smart_devices")
	require.NoError(t, err, "Failed to find smart_devices collection")
	assert.Equal(t, isUser, *smartDevicesCollection.ListRule)
	assert.Equal(t, isUser, *smartDevicesCollection.ViewRule)
	assert.Nil(t, smartDevicesCollection.CreateRule)
	assert.Nil(t, smartDevicesCollection.UpdateRule)
	assert.Equal(t, isUserNotReadonly, *smartDevicesCollection.DeleteRule)

	// system_details collection
	systemDetailsCollection, err := hub.FindCollectionByNameOrId("system_details")
	require.NoError(t, err, "Failed to find system_details collection")
	assert.Equal(t, isUser, *systemDetailsCollection.ListRule)
	assert.Equal(t, isUser, *systemDetailsCollection.ViewRule)
	assert.Nil(t, systemDetailsCollection.CreateRule)
	assert.Nil(t, systemDetailsCollection.UpdateRule)
	assert.Nil(t, systemDetailsCollection.DeleteRule)

	// system_stats collection
	systemStatsCollection, err := hub.FindCollectionByNameOrId("system_stats")
	require.NoError(t, err, "Failed to find system_stats collection")
	assert.Equal(t, isUser, *systemStatsCollection.ListRule)
	assert.Nil(t, systemStatsCollection.ViewRule)
	assert.Nil(t, systemStatsCollection.CreateRule)
	assert.Nil(t, systemStatsCollection.UpdateRule)
	assert.Nil(t, systemStatsCollection.DeleteRule)

	// systemd_services collection
	systemdServicesCollection, err := hub.FindCollectionByNameOrId("systemd_services")
	require.NoError(t, err, "Failed to find systemd_services collection")
	assert.Equal(t, isUser, *systemdServicesCollection.ListRule)
	assert.Nil(t, systemdServicesCollection.ViewRule)
	assert.Nil(t, systemdServicesCollection.CreateRule)
	assert.Nil(t, systemdServicesCollection.UpdateRule)
	assert.Nil(t, systemdServicesCollection.DeleteRule)

	// systems collection
	systemsCollection, err := hub.FindCollectionByNameOrId("systems")
	require.NoError(t, err, "Failed to find systems collection")
	assert.Equal(t, isUser, *systemsCollection.ListRule)
	assert.Equal(t, isUser, *systemsCollection.ViewRule)
	assert.Equal(t, isUserNotReadonly, *systemsCollection.CreateRule)
	assert.Equal(t, isUserNotReadonly, *systemsCollection.UpdateRule)
	assert.Equal(t, isUserNotReadonly, *systemsCollection.DeleteRule)

	// universal_tokens collection
	universalTokensCollection, err := hub.FindCollectionByNameOrId("universal_tokens")
	require.NoError(t, err, "Failed to find universal_tokens collection")
	assert.Nil(t, universalTokensCollection.ListRule)
	assert.Nil(t, universalTokensCollection.ViewRule)
	assert.Nil(t, universalTokensCollection.CreateRule)
	assert.Nil(t, universalTokensCollection.UpdateRule)
	assert.Nil(t, universalTokensCollection.DeleteRule)

	// user_settings collection
	userSettingsCollection, err := hub.FindCollectionByNameOrId("user_settings")
	require.NoError(t, err, "Failed to find user_settings collection")
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.ListRule)
	assert.Nil(t, userSettingsCollection.ViewRule)
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.CreateRule)
	assert.Equal(t, isUserMatchesUser, *userSettingsCollection.UpdateRule)
	assert.Nil(t, userSettingsCollection.DeleteRule)
}

// assertOwnerRules checks the rules of user-owned collections, which ignore SHARE_ALL_SYSTEMS.
func assertOwnerRules(t *testing.T, app core.App) {
	t.Helper()
	const isOwner = `@request.auth.id != "" && user = @request.auth.id`
	const isOwnerNotReadonly = isOwner + ` && @request.auth.role != "readonly"`
	for _, name := range []string{"status_pages", "monitor_maintenance"} {
		collection, err := app.FindCollectionByNameOrId(name)
		require.NoError(t, err, "Failed to find %s collection", name)
		assert.Equal(t, isOwner, *collection.ListRule)
		assert.Equal(t, isOwner, *collection.ViewRule)
		assert.Equal(t, isOwnerNotReadonly, *collection.CreateRule)
		assert.Equal(t, isOwnerNotReadonly+` && (@request.body.user:changed = false || @request.body.user = @request.auth.id)`, *collection.UpdateRule)
		assert.Equal(t, isOwnerNotReadonly, *collection.DeleteRule)
	}
}

func TestDisablePasswordAuth(t *testing.T) {
	t.Setenv("DISABLE_PASSWORD_AUTH", "true")
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	usersCollection, err := hub.FindCollectionByNameOrId("users")
	assert.NoError(t, err)
	assert.False(t, usersCollection.PasswordAuth.Enabled)
}

func TestUserCreation(t *testing.T) {
	t.Setenv("USER_CREATION", "true")
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	usersCollection, err := hub.FindCollectionByNameOrId("users")
	assert.NoError(t, err)
	assert.Equal(t, "@request.context = 'oauth2'", *usersCollection.CreateRule)
}

func TestMFAOtp(t *testing.T) {
	t.Setenv("MFA_OTP", "true")
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	usersCollection, err := hub.FindCollectionByNameOrId("users")
	assert.NoError(t, err)
	assert.True(t, usersCollection.OTP.Enabled)
	assert.True(t, usersCollection.MFA.Enabled)

	superusersCollection, err := hub.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	assert.NoError(t, err)
	assert.True(t, superusersCollection.OTP.Enabled)
	assert.True(t, superusersCollection.MFA.Enabled)
}

func TestApiCollectionsAuthRules(t *testing.T) {
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()

	hub.StartHub()

	user1, _ := beszelTests.CreateUser(hub, "user1@example.com", "password")
	user1Token, _ := user1.NewAuthToken()

	user2, _ := beszelTests.CreateUser(hub, "user2@example.com", "password")
	// user2Token, _ := user2.NewAuthToken()

	userReadonly, _ := beszelTests.CreateUserWithRole(hub, "userreadonly@example.com", "password", "readonly")
	userReadonlyToken, _ := userReadonly.NewAuthToken()

	userOneSystem, _ := beszelTests.CreateRecord(hub, "systems", map[string]any{
		"name":  "system1",
		"users": []string{user1.Id},
		"host":  "127.0.0.1",
	})

	sharedSystem, _ := beszelTests.CreateRecord(hub, "systems", map[string]any{
		"name":  "system2",
		"users": []string{user1.Id, user2.Id},
		"host":  "127.0.0.2",
	})

	userTwoSystem, _ := beszelTests.CreateRecord(hub, "systems", map[string]any{
		"name":  "system3",
		"users": []string{user2.Id},
		"host":  "127.0.0.2",
	})

	userOneAlert, _ := beszelTests.CreateRecord(hub, "alerts", map[string]any{
		"name": "CPU", "system": userOneSystem.Id, "user": user1.Id, "value": 80,
	})
	userTwoAlert, _ := beszelTests.CreateRecord(hub, "alerts", map[string]any{
		"name": "CPU", "system": userTwoSystem.Id, "user": user2.Id, "value": 80,
	})

	userOneMonitor, _ := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"system": userOneSystem.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60,
	})

	userRecords, _ := hub.CountRecords("users")
	assert.EqualValues(t, 3, userRecords, "all users should be created")

	systemRecords, _ := hub.CountRecords("systems")
	assert.EqualValues(t, 3, systemRecords, "all systems should be created")

	testAppFactory := func(t testing.TB) *pbTests.TestApp {
		return hub.TestApp
	}

	scenarios := []beszelTests.ApiScenario{
		{
			Name:   "Users can only list their own alerts",
			Method: http.MethodGet,
			URL:    "/api/collections/alerts/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:     200,
			ExpectedContent:    []string{userOneAlert.Id},
			NotExpectedContent: []string{userTwoAlert.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name:   "Users cannot view another user's alert by id",
			Method: http.MethodGet,
			URL:    fmt.Sprintf("/api/collections/alerts/records/%s", userTwoAlert.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:     403,
			ExpectedContent:    []string{"Only superusers"},
			NotExpectedContent: []string{userTwoAlert.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name:   "Users can create alerts on their own systems",
			Method: http.MethodPost,
			URL:    "/api/collections/alerts/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"name":"Memory","system":%q,"user":%q,"value":80}`, userOneSystem.Id, user1.Id)),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"Memory"`},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "Users cannot create alerts on another user's system",
			Method: http.MethodPost,
			URL:    "/api/collections/alerts/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"name":"Memory","system":%q,"user":%q,"value":80}`, userTwoSystem.Id, user1.Id)),
			ExpectedStatus:  400,
			ExpectedContent: []string{"Failed to create record"},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "Users cannot move their alert to another user's system",
			Method: http.MethodPatch,
			URL:    fmt.Sprintf("/api/collections/alerts/records/%s", userOneAlert.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q}`, userTwoSystem.Id)),
			ExpectedStatus:  404,
			ExpectedContent: []string{"resource wasn't found"},
			TestAppFactory:  testAppFactory,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				alert, err := app.FindRecordById("alerts", userOneAlert.Id)
				require.NoError(t, err)
				assert.Equal(t, userOneSystem.Id, alert.GetString("system"))
			},
		},
		{
			Name:   "Users cannot give their alert to another user",
			Method: http.MethodPatch,
			URL:    fmt.Sprintf("/api/collections/alerts/records/%s", userOneAlert.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q}`, user2.Id)),
			ExpectedStatus:  404,
			ExpectedContent: []string{"resource wasn't found"},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "Users can update their alert on the same system",
			Method: http.MethodPatch,
			URL:    fmt.Sprintf("/api/collections/alerts/records/%s", userOneAlert.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q,"value":90}`, userOneSystem.Id)),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"value":90`},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "Users cannot move a network monitor to another user's system",
			Method: http.MethodPatch,
			URL:    fmt.Sprintf("/api/collections/network_monitors/records/%s", userOneMonitor.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q}`, userTwoSystem.Id)),
			ExpectedStatus:  404,
			ExpectedContent: []string{"resource wasn't found"},
			TestAppFactory:  testAppFactory,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				monitors, err := app.FindAllRecords("network_monitors")
				require.NoError(t, err)
				require.Len(t, monitors, 1)
				assert.Equal(t, userOneSystem.Id, monitors[0].GetString("system"))
			},
		},
		{
			Name:               "Unauthorized user cannot list systems",
			Method:             http.MethodGet,
			URL:                "/api/collections/systems/records",
			ExpectedStatus:     200, // https://github.com/pocketbase/pocketbase/discussions/1570
			TestAppFactory:     testAppFactory,
			ExpectedContent:    []string{`"items":[]`, `"totalItems":0`},
			NotExpectedContent: []string{userOneSystem.Id, sharedSystem.Id, userTwoSystem.Id},
		},
		{
			Name:               "Unauthorized user cannot delete a system",
			Method:             http.MethodDelete,
			URL:                fmt.Sprintf("/api/collections/systems/records/%s", userOneSystem.Id),
			ExpectedStatus:     404,
			TestAppFactory:     testAppFactory,
			ExpectedContent:    []string{"resource wasn't found"},
			NotExpectedContent: []string{userOneSystem.Id},
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 3, systemsCount, "should have 3 systems before deletion")
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 3, systemsCount, "should still have 3 systems after failed deletion")
			},
		},
		{
			Name:   "User 1 can list their own systems",
			Method: http.MethodGet,
			URL:    "/api/collections/systems/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:     200,
			ExpectedContent:    []string{userOneSystem.Id, sharedSystem.Id},
			NotExpectedContent: []string{userTwoSystem.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name:   "User 1 cannot list user 2's system",
			Method: http.MethodGet,
			URL:    "/api/collections/systems/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:     200,
			ExpectedContent:    []string{userOneSystem.Id, sharedSystem.Id},
			NotExpectedContent: []string{userTwoSystem.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name:   "User 1 can see user 2's system if SHARE_ALL_SYSTEMS is enabled",
			Method: http.MethodGet,
			URL:    "/api/collections/systems/records",
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{userOneSystem.Id, sharedSystem.Id, userTwoSystem.Id},
			TestAppFactory:  testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				t.Setenv("SHARE_ALL_SYSTEMS", "true")
				hub.SetCollectionAuthSettings()
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				t.Setenv("SHARE_ALL_SYSTEMS", "")
				hub.SetCollectionAuthSettings()
			},
		},
		{
			Name:   "User 1 can delete their own system",
			Method: http.MethodDelete,
			URL:    fmt.Sprintf("/api/collections/systems/records/%s", userOneSystem.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus: 204,
			TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 3, systemsCount, "should have 3 systems before deletion")
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount, "should have 2 systems after deletion")
			},
		},
		{
			Name:   "User 1 cannot delete user 2's system",
			Method: http.MethodDelete,
			URL:    fmt.Sprintf("/api/collections/systems/records/%s", userTwoSystem.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus:  404,
			TestAppFactory:  testAppFactory,
			ExpectedContent: []string{"resource wasn't found"},
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount)
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount)
			},
		},
		{
			Name:   "Readonly cannot delete a system even if SHARE_ALL_SYSTEMS is enabled",
			Method: http.MethodDelete,
			URL:    fmt.Sprintf("/api/collections/systems/records/%s", sharedSystem.Id),
			Headers: map[string]string{
				"Authorization": userReadonlyToken,
			},
			ExpectedStatus:  404,
			ExpectedContent: []string{"resource wasn't found"},
			TestAppFactory:  testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				t.Setenv("SHARE_ALL_SYSTEMS", "true")
				hub.SetCollectionAuthSettings()
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount)
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				t.Setenv("SHARE_ALL_SYSTEMS", "")
				hub.SetCollectionAuthSettings()
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount)
			},
		},
		{
			Name:   "User 1 can delete user 2's system if SHARE_ALL_SYSTEMS is enabled",
			Method: http.MethodDelete,
			URL:    fmt.Sprintf("/api/collections/systems/records/%s", userTwoSystem.Id),
			Headers: map[string]string{
				"Authorization": user1Token,
			},
			ExpectedStatus: 204,
			TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) {
				t.Setenv("SHARE_ALL_SYSTEMS", "true")
				hub.SetCollectionAuthSettings()
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 2, systemsCount)
			},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				t.Setenv("SHARE_ALL_SYSTEMS", "")
				hub.SetCollectionAuthSettings()
				systemsCount, _ := app.CountRecords("systems")
				assert.EqualValues(t, 1, systemsCount)
			},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

func TestApiMonitorAuthRules(t *testing.T) {
	hub, _ := beszelTests.NewTestHub(t.TempDir())
	defer hub.Cleanup()
	hub.StartHub()

	user1, _ := beszelTests.CreateUser(hub, "user1@example.com", "password")
	user1Token, _ := user1.NewAuthToken()
	user2, _ := beszelTests.CreateUser(hub, "user2@example.com", "password")
	user2Token, _ := user2.NewAuthToken()
	user3, _ := beszelTests.CreateUser(hub, "user3@example.com", "password")
	user3Token, _ := user3.NewAuthToken()
	readonly, _ := beszelTests.CreateUserWithRole(hub, "readonly@example.com", "password", "readonly")
	readonlyToken, _ := readonly.NewAuthToken()

	system1, err := beszelTests.CreateRecord(hub, "systems", map[string]any{"name": "system1", "users": []string{user1.Id, readonly.Id}, "host": "127.0.0.1"})
	require.NoError(t, err)
	system2, err := beszelTests.CreateRecord(hub, "systems", map[string]any{"name": "system2", "users": []string{user2.Id}, "host": "127.0.0.2"})
	require.NoError(t, err)

	agentMonitor, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"system": system1.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60,
	})
	require.NoError(t, err)
	agentMonitor2, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"system": system1.Id, "target": "8.8.8.8", "protocol": "icmp", "interval": 60,
	})
	require.NoError(t, err)
	hubMonitor, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"users": []string{user1.Id, readonly.Id}, "target": "https://one.example.com", "protocol": "http", "interval": 60,
	})
	require.NoError(t, err)
	user2HubMonitor, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"users": []string{user2.Id}, "target": "https://two.example.com", "protocol": "http", "interval": 60,
	})
	require.NoError(t, err)
	agentStats, err := beszelTests.CreateRecord(hub, "network_monitor_stats", map[string]any{
		"system": system1.Id, "monitor": agentMonitor.Id, "type": "1m", "created": 1,
	})
	require.NoError(t, err)
	hubEvent, err := beszelTests.CreateRecord(hub, "monitor_events", map[string]any{
		"monitor": hubMonitor.Id, "status": "down", "start": 1,
	})
	require.NoError(t, err)
	for _, system := range []*core.Record{system1, system2} {
		_, err = beszelTests.CreateRecord(hub, "system_events", map[string]any{"system": system.Id, "status": "up", "start": 1})
		require.NoError(t, err)
	}
	user1Page, err := beszelTests.CreateRecord(hub, "status_pages", map[string]any{"user": user1.Id, "slug": "user-one", "title": "One"})
	require.NoError(t, err)
	user2Page, err := beszelTests.CreateRecord(hub, "status_pages", map[string]any{"user": user2.Id, "slug": "user-two", "title": "Two"})
	require.NoError(t, err)
	user1Maintenance, err := beszelTests.CreateRecord(hub, "monitor_maintenance", map[string]any{
		"user": user1.Id, "title": "Upgrade", "type": "one-time", "start": "2026-01-01 00:00:00.000Z",
		"end": "2026-01-01 01:00:00.000Z", "monitors": []string{hubMonitor.Id},
	})
	require.NoError(t, err)

	testAppFactory := func(t testing.TB) *pbTests.TestApp {
		return hub.TestApp
	}
	auth := func(token string) map[string]string {
		return map[string]string{"Authorization": token}
	}
	monitorURL := func(id string) string {
		return "/api/collections/network_monitors/records/" + id
	}
	shareAllSystems := func(enabled bool) func(t testing.TB) {
		return func(t testing.TB) {
			value := ""
			if enabled {
				value = "true"
			}
			t.Setenv("SHARE_ALL_SYSTEMS", value)
			require.NoError(t, hub.SetCollectionAuthSettings())
		}
	}
	monitorField := func(t testing.TB, app *pbTests.TestApp, id, field string) any {
		record, err := app.FindRecordById("network_monitors", id)
		require.NoError(t, err)
		return record.Get(field)
	}
	const monitorsURL = "/api/collections/network_monitors/records"

	scenarios := []beszelTests.ApiScenario{
		{
			Name: "Members list agent monitors of their systems and hub monitors they belong to", Method: http.MethodGet,
			URL: monitorsURL, Headers: auth(user1Token), ExpectedStatus: 200,
			ExpectedContent:    []string{agentMonitor.Id, hubMonitor.Id},
			NotExpectedContent: []string{user2HubMonitor.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name: "Non-members do not list other users' monitors", Method: http.MethodGet,
			URL: monitorsURL, Headers: auth(user2Token), ExpectedStatus: 200,
			ExpectedContent:    []string{user2HubMonitor.Id},
			NotExpectedContent: []string{agentMonitor.Id, hubMonitor.Id},
			TestAppFactory:     testAppFactory,
		},
		{
			Name: "Non-members cannot view an agent monitor", Method: http.MethodGet,
			URL: monitorURL(agentMonitor.Id), Headers: auth(user2Token), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Non-members cannot view a hub monitor", Method: http.MethodGet,
			URL: monitorURL(hubMonitor.Id), Headers: auth(user2Token), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Readonly members can view monitors", Method: http.MethodGet,
			URL: monitorURL(hubMonitor.Id), Headers: auth(readonlyToken), ExpectedStatus: 200,
			ExpectedContent: []string{hubMonitor.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Readonly members cannot create monitors", Method: http.MethodPost,
			URL: monitorsURL, Headers: auth(readonlyToken), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q,"target":"8.8.8.8","protocol":"icmp","interval":60}`, system1.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Readonly members cannot update hub monitors", Method: http.MethodPatch,
			URL: monitorURL(hubMonitor.Id), Headers: auth(readonlyToken), ExpectedStatus: 404,
			Body:            strings.NewReader(`{"name":"renamed"}`),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Readonly members cannot delete agent monitors", Method: http.MethodDelete,
			URL: monitorURL(agentMonitor.Id), Headers: auth(readonlyToken), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot create monitors on another user's system", Method: http.MethodPost,
			URL: monitorsURL, Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q,"target":"8.8.8.8","protocol":"icmp","interval":60}`, system2.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot create hub monitors for other users only", Method: http.MethodPost,
			URL: monitorsURL, Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"users":[%q],"target":"https://x.example.com","protocol":"http","interval":60}`, user2.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users can create hub monitors they belong to", Method: http.MethodPost,
			URL: monitorsURL, Headers: auth(user1Token), ExpectedStatus: 200,
			Body:            strings.NewReader(fmt.Sprintf(`{"users":[%q],"target":"https://x.example.com","protocol":"http","interval":60}`, user1.Id)),
			ExpectedContent: []string{`"target":"https://x.example.com"`, `"status":"unknown"`}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Members list stats of their monitors", Method: http.MethodGet,
			URL: "/api/collections/network_monitor_stats/records", Headers: auth(user1Token), ExpectedStatus: 200,
			ExpectedContent: []string{agentStats.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Non-members do not list stats of other monitors", Method: http.MethodGet,
			URL: "/api/collections/network_monitor_stats/records", Headers: auth(user2Token), ExpectedStatus: 200,
			ExpectedContent: []string{`"totalItems":0`}, NotExpectedContent: []string{agentStats.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Hub monitor users can view its events", Method: http.MethodGet,
			URL: "/api/collections/monitor_events/records/" + hubEvent.Id, Headers: auth(readonlyToken), ExpectedStatus: 200,
			ExpectedContent: []string{hubEvent.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Other users cannot list hub monitor events", Method: http.MethodGet,
			URL: "/api/collections/monitor_events/records", Headers: auth(user2Token), ExpectedStatus: 200,
			// Only the paused segment the status engine opened for user2's disabled monitor.
			ExpectedContent: []string{`"totalItems":1`, user2HubMonitor.Id}, NotExpectedContent: []string{hubEvent.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot move a hub monitor to another user's system", Method: http.MethodPatch,
			URL: monitorURL(hubMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 404,
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q}`, system2.Id)),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				assert.Equal(t, "", monitorField(t, app, hubMonitor.Id, "system"))
			},
		},
		{
			Name: "Users cannot remove themselves when changing hub monitor users", Method: http.MethodPatch,
			URL: monitorURL(hubMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 404,
			Body:            strings.NewReader(fmt.Sprintf(`{"users":[%q]}`, user2.Id)),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot add themselves to another user's hub monitor", Method: http.MethodPatch,
			URL: monitorURL(user2HubMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 404,
			Body:            strings.NewReader(fmt.Sprintf(`{"users+":[%q]}`, user1.Id)),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Hub monitor users can add users", Method: http.MethodPatch,
			URL: monitorURL(hubMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 200,
			Body:            strings.NewReader(fmt.Sprintf(`{"users+":[%q]}`, user2.Id)),
			ExpectedContent: []string{user2.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Added users can view the hub monitor", Method: http.MethodGet,
			URL: monitorURL(hubMonitor.Id), Headers: auth(user2Token), ExpectedStatus: 200,
			ExpectedContent: []string{hubMonitor.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Members can move an agent monitor to the hub", Method: http.MethodPatch,
			URL: monitorURL(agentMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 200,
			Body:           strings.NewReader(fmt.Sprintf(`{"system":"","users":[%q]}`, user1.Id)),
			TestAppFactory: testAppFactory, ExpectedContent: []string{agentMonitor.Id},
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				assert.Equal(t, "", monitorField(t, app, agentMonitor.Id, "system"))
			},
		},
		{
			Name: "Moving a monitor to the hub requires the requester in its users", Method: http.MethodPatch,
			URL: monitorURL(agentMonitor2.Id), Headers: auth(user1Token), ExpectedStatus: 404,
			Body:            strings.NewReader(fmt.Sprintf(`{"system":"","users":[%q]}`, user2.Id)),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Moving a monitor to the hub requires users", Method: http.MethodPatch,
			URL: monitorURL(agentMonitor2.Id), Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(`{"system":""}`),
			ExpectedContent: []string{"at least one user"}, TestAppFactory: testAppFactory,
			AfterTestFunc: func(t testing.TB, app *pbTests.TestApp, res *http.Response) {
				assert.Equal(t, system1.Id, monitorField(t, app, agentMonitor2.Id, "system"))
			},
		},
		{
			Name: "Users cannot delete another user's hub monitor", Method: http.MethodDelete,
			URL: monitorURL(user2HubMonitor.Id), Headers: auth(user1Token), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "SHARE_ALL_SYSTEMS lets any user list all monitors", Method: http.MethodGet,
			URL: monitorsURL, Headers: auth(user3Token), ExpectedStatus: 200,
			ExpectedContent: []string{agentMonitor.Id, hubMonitor.Id, user2HubMonitor.Id}, TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) { shareAllSystems(true)(t) },
			AfterTestFunc:  func(t testing.TB, app *pbTests.TestApp, res *http.Response) { shareAllSystems(false)(t) },
		},
		{
			Name: "SHARE_ALL_SYSTEMS lets any user edit a hub monitor", Method: http.MethodPatch,
			URL: monitorURL(user2HubMonitor.Id), Headers: auth(user3Token), ExpectedStatus: 200,
			Body:            strings.NewReader(`{"name":"shared"}`),
			ExpectedContent: []string{`"name":"shared"`}, TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) { shareAllSystems(true)(t) },
			AfterTestFunc:  func(t testing.TB, app *pbTests.TestApp, res *http.Response) { shareAllSystems(false)(t) },
		},
		{
			Name: "SHARE_ALL_SYSTEMS does not let readonly users delete monitors", Method: http.MethodDelete,
			URL: monitorURL(user2HubMonitor.Id), Headers: auth(readonlyToken), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) { shareAllSystems(true)(t) },
			AfterTestFunc:  func(t testing.TB, app *pbTests.TestApp, res *http.Response) { shareAllSystems(false)(t) },
		},
		{
			Name: "Hub monitor users can delete it", Method: http.MethodDelete,
			URL: monitorURL(user2HubMonitor.Id), Headers: auth(user2Token), ExpectedStatus: 204,
			TestAppFactory: testAppFactory,
		},
		{
			Name: "Users list only their status pages", Method: http.MethodGet,
			URL: "/api/collections/status_pages/records", Headers: auth(user1Token), ExpectedStatus: 200,
			ExpectedContent: []string{user1Page.Id}, NotExpectedContent: []string{user2Page.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot view another user's status page", Method: http.MethodGet,
			URL: "/api/collections/status_pages/records/" + user2Page.Id, Headers: auth(user1Token), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot create status pages for another user", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"other","title":"Other"}`, user2.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot add inaccessible monitors to a status page", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user3Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"stolen","title":"Stolen","monitors":[%q]}`, user3.Id, hubMonitor.Id)),
			ExpectedContent: []string{"do not have access to all selected monitors"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot add inaccessible systems to a status page", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"stolen-system","title":"Stolen","systems":[%q,%q]}`, user1.Id, system1.Id, system2.Id)),
			ExpectedContent: []string{"do not have access to all selected systems"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot update a status page with inaccessible systems", Method: http.MethodPatch,
			URL: "/api/collections/status_pages/records/" + user1Page.Id, Headers: auth(user1Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"systems+":[%q]}`, system2.Id)),
			ExpectedContent: []string{"do not have access to all selected systems"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users can create status pages with their systems", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user1Token), ExpectedStatus: 200,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"servers","title":"Servers","systems":[%q]}`, user1.Id, system1.Id)),
			ExpectedContent: []string{`"slug":"servers"`, system1.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Members list the status history of their systems", Method: http.MethodGet,
			URL: "/api/collections/system_events/records", Headers: auth(user1Token), ExpectedStatus: 200,
			ExpectedContent: []string{system1.Id}, NotExpectedContent: []string{system2.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Guests cannot list system status history", Method: http.MethodGet,
			URL: "/api/collections/system_events/records", ExpectedStatus: 200,
			ExpectedContent: []string{`"totalItems":0`}, TestAppFactory: testAppFactory,
		},
		{
			Name: "SHARE_ALL_SYSTEMS lets any user list system status history", Method: http.MethodGet,
			URL: "/api/collections/system_events/records", Headers: auth(user3Token), ExpectedStatus: 200,
			ExpectedContent: []string{system1.Id, system2.Id}, TestAppFactory: testAppFactory,
			BeforeTestFunc: func(t testing.TB, app *pbTests.TestApp, e *core.ServeEvent) { shareAllSystems(true)(t) },
			AfterTestFunc:  func(t testing.TB, app *pbTests.TestApp, res *http.Response) { shareAllSystems(false)(t) },
		},
		{
			Name: "Users cannot create system status history", Method: http.MethodPost,
			URL: "/api/collections/system_events/records", Headers: auth(user1Token), ExpectedStatus: 403,
			Body:            strings.NewReader(fmt.Sprintf(`{"system":%q,"status":"up","start":1}`, system1.Id)),
			ExpectedContent: []string{"Only superusers"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users can create status pages with their monitors", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user1Token), ExpectedStatus: 200,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"status","title":"Status","monitors":[%q]}`, user1.Id, hubMonitor.Id)),
			ExpectedContent: []string{`"slug":"status"`}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Status page slugs are unique", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(user2Token), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"status","title":"Status"}`, user2.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot give their status page to another user", Method: http.MethodPatch,
			URL: "/api/collections/status_pages/records/" + user1Page.Id, Headers: auth(user1Token), ExpectedStatus: 404,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q}`, user2.Id)),
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Readonly users cannot create status pages", Method: http.MethodPost,
			URL: "/api/collections/status_pages/records", Headers: auth(readonlyToken), ExpectedStatus: 400,
			Body:            strings.NewReader(fmt.Sprintf(`{"user":%q,"slug":"readonly","title":"Readonly"}`, readonly.Id)),
			ExpectedContent: []string{"Failed to create record"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users list only their maintenance windows", Method: http.MethodGet,
			URL: "/api/collections/monitor_maintenance/records", Headers: auth(user2Token), ExpectedStatus: 200,
			ExpectedContent: []string{`"totalItems":0`}, NotExpectedContent: []string{user1Maintenance.Id}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users cannot delete another user's maintenance window", Method: http.MethodDelete,
			URL: "/api/collections/monitor_maintenance/records/" + user1Maintenance.Id, Headers: auth(user2Token), ExpectedStatus: 404,
			ExpectedContent: []string{"resource wasn't found"}, TestAppFactory: testAppFactory,
		},
		{
			Name: "Users can delete their maintenance window", Method: http.MethodDelete,
			URL: "/api/collections/monitor_maintenance/records/" + user1Maintenance.Id, Headers: auth(user1Token), ExpectedStatus: 204,
			TestAppFactory: testAppFactory,
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
