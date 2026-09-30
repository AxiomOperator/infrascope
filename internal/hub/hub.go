// Package hub handles updating systems and serving the web UI.
package hub

import (
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/config"
	"github.com/henrygd/beszel/internal/hub/heartbeat"
	"github.com/henrygd/beszel/internal/hub/systemevents"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/henrygd/beszel/internal/hub/utils"
	"github.com/henrygd/beszel/internal/records"
	"github.com/henrygd/beszel/internal/users"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/ssh"
)

// Hub is the application. It embeds the PocketBase app and keeps references to subcomponents.
type Hub struct {
	core.App
	*alerts.AlertManager
	um     *users.UserManager
	rm     *records.RecordManager
	sm     *systems.SystemManager
	hb     *heartbeat.Heartbeat
	hbStop chan struct{}
	// keyMu guards pubKey, signer and signerPath.
	keyMu      sync.Mutex
	pubKey     string
	signer     ssh.Signer
	signerPath string // key file the cached signer was loaded from
	appURL     string
	// hubMonitors runs monitors that have no system.
	hubMonitors hubMonitorRunner
	// uptime derives monitor status from check results.
	uptime     *uptime.Engine
	uptimeOnce sync.Once
	// maintenance holds the monitor maintenance windows for the engine.
	maintenance *maintenanceWindows
	// monitorNotices delivers the engine's status changes to the alert manager.
	monitorNotices *transitionQueue
	// dependencies updates systems whose parent monitors changed state.
	dependencies *idQueue
	// statusPages caches and rate limits public status page requests.
	statusPages *statusPages
	// statusSubscriptions emails the subscribers of status pages.
	statusSubscriptions *statusSubscriptions
	// systemEvents records the status history of systems.
	systemEvents *systemevents.Recorder
	// discovery rate limits Docker label discovery reconciles.
	discovery *dockerDiscovery
}

// NewHub creates a new Hub instance with default configuration
func NewHub(app core.App) *Hub {
	hub := &Hub{App: app}
	hub.AlertManager = alerts.NewAlertManager(hub)
	hub.um = users.NewUserManager(hub)
	hub.rm = records.NewRecordManager(hub)
	hub.sm = systems.NewSystemManager(hub)
	hub.maintenance = newMaintenanceWindows(app)
	hub.AlertManager.SetMaintenanceCheck(hub.maintenance.Active)
	hub.monitorNotices = newTransitionQueue(hub.handleMonitorTransitions)
	hub.uptime = uptime.New(app,
		uptime.WithNotifier(hub.monitorNotices.push),
		uptime.WithMaintenanceCheck(hub.maintenance.Active),
		uptime.WithDependencyListener(func(ids []string) { hub.dependencies.push(ids) }),
	)
	hub.dependencies = newIDQueue(hub.refreshSystemDependencies)
	hub.AlertManager.SetDependencyChecks(hub.uptime.Suppressed, hub.systemSuppressed)
	hub.hubMonitors = newHubMonitorRunner(app, hub.uptime, func(results map[string]monitor.Result) {
		hub.HandleMonitorResults("", results)
	})
	hub.statusPages = newStatusPages()
	hub.statusSubscriptions = newStatusSubscriptions(hub)
	hub.discovery = newDockerDiscovery()
	hub.systemEvents = systemevents.New()
	hub.systemEvents.Bind(app)
	hub.hb = heartbeat.New(app, utils.GetEnv)
	if hub.hb != nil {
		hub.hbStop = make(chan struct{})
	}
	_ = onAfterBootstrapAndMigrations(app, hub.initialize)
	return hub
}

// onAfterBootstrapAndMigrations ensures the provided function runs after the database is set up and migrations are applied.
// This is a workaround for behavior in PocketBase where onBootstrap runs before migrations, forcing use of onServe for this purpose.
// However, PB's tests.TestApp is already bootstrapped, generally doesn't serve, but does handle migrations.
// So this ensures that the provided function runs at the right time either way, after DB is ready and migrations are done.
func onAfterBootstrapAndMigrations(app core.App, fn func(app core.App) error) error {
	// pb tests.TestApp is already bootstrapped and doesn't serve
	if app.IsBootstrapped() {
		return fn(app)
	}
	// Must use OnServe because OnBootstrap appears to run before migrations, even if calling e.Next() before anything else
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if err := fn(e.App); err != nil {
			return err
		}
		return e.Next()
	})
	return nil
}

// StartHub sets up event handlers and starts the PocketBase server
func (h *Hub) StartHub() error {
	h.App.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// sync systems with config
		if err := config.SyncSystems(e); err != nil {
			return err
		}
		// restrict CORS and cross-origin writes under implicit authentication
		h.registerOriginPolicy(e)
		// register middlewares
		h.registerMiddlewares(e)
		// register api routes
		if err := h.registerApiRoutes(e); err != nil {
			return err
		}
		// register cron jobs
		if err := h.registerCronJobs(e); err != nil {
			return err
		}
		// start server
		if err := h.startServer(e); err != nil {
			return err
		}
		// encrypt monitor secrets stored before encryption at rest
		if err := h.sealStoredMonitorSecrets(); err != nil {
			h.Logger().Error("Failed to encrypt stored monitor secrets", "err", err)
		}
		// restore monitor status before systems deliver monitor results
		if err := h.uptime.Load(); err != nil {
			return err
		}
		// start hub monitors before the engine loop, so the engine's final
		// flush on terminate follows the runner's last checks
		if err := h.startHubMonitors(); err != nil {
			return err
		}
		h.startUptimeEngine()
		// match the open system status segments to the current statuses
		if err := h.systemEvents.Reconcile(h); err != nil {
			h.Logger().Error("Failed to reconcile system status history", "err", err)
		}
		// start system updates
		if err := h.sm.Initialize(); err != nil {
			return err
		}
		// start heartbeat if configured
		if h.hb != nil {
			go h.hb.Start(h.hbStop)
		}
		return e.Next()
	})

	// TODO: move to users package
	// handle default values for user / user_settings creation
	h.App.OnRecordAuthWithOAuth2Request("users").BindFunc(h.um.InitializeOAuthUserRole)
	h.App.OnRecordCreate("users").BindFunc(h.um.InitializeUserRole)
	h.App.OnRecordCreate("user_settings").BindFunc(h.um.InitializeUserSettings)

	bindNetworkMonitorsEvents(h)
	bindDependencyEvents(h)
	bindDockerDiscoveryEvents(h)
	bindStatusPageHooks(h)
	bindIncidentHooks(h)
	bindSystemIncidentEvents(h)
	bindStatusSubscriptions(h)

	pb, ok := h.App.(*pocketbase.PocketBase)
	if !ok {
		return errors.New("not a pocketbase app")
	}
	return pb.Start()
}

// Uptime returns the engine that derives monitor status from check results.
func (h *Hub) Uptime() *uptime.Engine {
	return h.uptime
}

// startUptimeEngine runs the uptime engine's periodic work until the app
// terminates, then waits for its final flush. Only the first call starts it.
func (h *Hub) startUptimeEngine() {
	h.uptimeOnce.Do(func() {
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			h.uptime.Run(stop)
		}()
		h.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			close(stop)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				e.App.Logger().Warn("Timed out flushing monitor status")
			}
			return e.Next()
		})
	})
}

// initialize sets up initial configuration (collections, settings, etc.)
func (h *Hub) initialize(app core.App) error {
	// set general settings
	settings := app.Settings()
	// batch requests (for alerts)
	settings.Batch.Enabled = true
	settings.Batch.MaxRequests = 100
	settings.Batch.MaxBodySize = 1 << 20 // 1 MiB
	// set URL if APP_URL env is set
	if appURL, isSet := utils.GetEnv("APP_URL"); isSet {
		h.appURL = appURL
		settings.Meta.AppURL = appURL
	}
	if err := app.Save(settings); err != nil {
		return err
	}
	// set auth settings
	return setCollectionAuthSettings(app)
}

// registerCronJobs sets up scheduled tasks
func (h *Hub) registerCronJobs(_ *core.ServeEvent) error {
	// delete old system_stats and alerts_history records once every hour
	h.Cron().MustAdd("delete old records", "8 * * * *", h.rm.DeleteOldRecords)
	// create longer records every 10 minutes
	h.Cron().MustAdd("create longer records", "*/10 * * * *", h.rm.CreateLongerRecords)
	// repeat notifications of unacknowledged alerts for users with reminders on
	h.Cron().MustAdd("alert reminders", "* * * * *", h.SendAlertReminders)
	// notify expiring monitor certificates; new certificate info is picked up within the hour
	h.Cron().MustAdd("monitor certificates", "17 * * * *", h.CheckMonitorCerts)
	return nil
}

// publicKey returns the hub's SSH public key in authorized_keys format, or ""
// before the key has been loaded.
func (h *Hub) publicKey() string {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	return h.pubKey
}

// GetSSHKey generates key pair if it doesn't exist and returns signer.
// The signer is cached, so the key file is read once rather than on every
// agent connection.
func (h *Hub) GetSSHKey(dataDir string) (ssh.Signer, error) {
	if dataDir == "" {
		dataDir = h.DataDir()
	}
	privateKeyPath := path.Join(dataDir, "id_ed25519")

	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	if h.signer != nil && h.signerPath == privateKeyPath {
		return h.signer, nil
	}
	signer, pubKey, err := h.loadOrCreateSSHKey(privateKeyPath)
	if err != nil {
		return nil, err
	}
	h.signer, h.signerPath, h.pubKey = signer, privateKeyPath, pubKey
	return signer, nil
}

// loadOrCreateSSHKey reads the key at privateKeyPath, generating it if missing.
func (h *Hub) loadOrCreateSSHKey(privateKeyPath string) (ssh.Signer, string, error) {

	// check if the key pair already exists
	existingKey, err := os.ReadFile(privateKeyPath)
	if err == nil {
		private, err := ssh.ParsePrivateKey(existingKey)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse private key: %s", err)
		}
		pubKeyBytes := ssh.MarshalAuthorizedKey(private.PublicKey())
		return private, strings.TrimSuffix(string(pubKeyBytes), "\n"), nil
	} else if !os.IsNotExist(err) {
		// File exists but couldn't be read for some other reason
		return nil, "", fmt.Errorf("failed to read %s: %w", privateKeyPath, err)
	}

	// Generate the Ed25519 key pair
	_, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, "", err
	}
	privKeyPem, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, "", err
	}

	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(privKeyPem), 0600); err != nil {
		return nil, "", fmt.Errorf("failed to write private key to %q: err: %w", privateKeyPath, err)
	}

	sshPrivate, err := ssh.NewSignerFromSigner(privKey)
	if err != nil {
		return nil, "", err
	}
	pubKeyBytes := ssh.MarshalAuthorizedKey(sshPrivate.PublicKey())

	h.Logger().Info("ed25519 key pair generated successfully.")
	h.Logger().Info("Saved to: " + privateKeyPath)

	return sshPrivate, strings.TrimSuffix(string(pubKeyBytes), "\n"), nil
}

// MakeLink formats a link with the app URL and path segments.
// Only path segments should be provided.
func (h *Hub) MakeLink(parts ...string) string {
	base := strings.TrimSuffix(h.Settings().Meta.AppURL, "/")
	for _, part := range parts {
		if part == "" {
			continue
		}
		base = fmt.Sprintf("%s/%s", base, url.PathEscape(part))
	}
	return base
}
