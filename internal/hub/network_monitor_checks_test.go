//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorCheckProtocols(t *testing.T) {
	env := newMonitorTestEnv(t)
	agentMonitor := func(body map[string]any) map[string]any {
		values := map[string]any{"system": env.system.Id, "target": "db.internal", "interval": 60}
		for key, value := range body {
			values[key] = value
		}
		return values
	}

	t.Run("default ports", func(t *testing.T) {
		for protocol, port := range map[string]int{
			"ssh": 22, "postgres": 5432, "mysql": 3306, "redis": 6379, "smtp": 25, "imap": 143, "minecraft": 25565, "a2s": 27015,
		} {
			record := env.createMonitor(t, agentMonitor(map[string]any{"protocol": protocol}))
			assert.Equal(t, port, record.GetInt("port"), protocol)
		}
		record := env.createMonitor(t, agentMonitor(map[string]any{"protocol": "smtp", "check": map[string]any{"tls": true}}))
		assert.Equal(t, 465, record.GetInt("port"))
		record = env.createMonitor(t, agentMonitor(map[string]any{"protocol": "imap", "check": map[string]any{"tls": true}}))
		assert.Equal(t, 993, record.GetInt("port"))
		record = env.createMonitor(t, agentMonitor(map[string]any{"protocol": "ssh", "port": 2222}))
		assert.Equal(t, 2222, record.GetInt("port"), "a configured port is kept")
	})

	t.Run("redis credentials are sealed and fields normalized", func(t *testing.T) {
		record := env.createMonitor(t, agentMonitor(map[string]any{
			"protocol": "redis",
			// banner and service do not apply to redis; ignoreTLS needs TLS
			"check":       map[string]any{"tls": true, "ignoreTLS": true, "banner": "x", "service": "y"},
			"httpSecrets": map[string]any{"username": "monitor", "password": "hunter2", "basicPass": "leak"},
			"http":        map[string]any{"method": "POST"},
		}))
		assert.JSONEq(t, `{"tls":true,"ignoreTLS":true}`, record.GetString("check"))
		assert.Contains(t, []string{"", "null"}, record.GetString("http"))
		assert.NotContains(t, record.GetString("httpSecrets"), "hunter2", "secrets are stored encrypted")
		secrets, err := systems.HTTPSecretsJSON(env.hub, record)
		require.NoError(t, err)
		assert.JSONEq(t, `{"username":"monitor","password":"hunter2"}`, secrets)

		config, err := systems.MonitorConfigFromRecord(env.hub, record)
		require.NoError(t, err)
		assert.Equal(t, &monitor.CheckOptions{TLS: true, IgnoreTLS: true, Username: "monitor", Password: "hunter2"}, config.Check)

		// The owner sees the credentials in plaintext.
		response := env.request(t, http.MethodGet, "/api/collections/network_monitors/records/"+record.Id, env.owner, nil)
		require.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), "hunter2")

		// Switching to a protocol without credentials drops them.
		response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"protocol": "ssh"})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		record, err = env.hub.FindRecordById("network_monitors", record.Id)
		require.NoError(t, err)
		assert.Contains(t, []string{"", "null"}, record.GetString("httpSecrets"))
		assert.Contains(t, []string{"", "null"}, record.GetString("check"))
		assert.Equal(t, 6379, record.GetInt("port"), "a configured port is kept")
	})

	t.Run("dns check options", func(t *testing.T) {
		record := env.createMonitor(t, agentMonitor(map[string]any{
			"protocol": "dns", "target": "example.com",
			"check": map[string]any{"recordType": "mx", "expected": "mail.example.com", "matchMode": "contains", "tls": true},
		}))
		assert.JSONEq(t, `{"recordType":"MX","expected":"mail.example.com"}`, record.GetString("check"))
		assert.Zero(t, record.GetInt("port"))
	})

	t.Run("http monitors have no check options", func(t *testing.T) {
		record := env.createMonitor(t, agentMonitor(map[string]any{
			"protocol": "http", "target": "https://example.com", "check": map[string]any{"banner": "x"},
		}))
		assert.Contains(t, []string{"", "null"}, record.GetString("check"))
	})

	for _, tc := range []struct {
		name    string
		body    map[string]any
		message string
	}{
		{"docker on the hub", env.hubMonitor(map[string]any{"protocol": "docker", "target": "web"}), "Docker monitors require a system"},
		{"docker bad target", agentMonitor(map[string]any{"protocol": "docker", "target": "a/b"}), "container name or ID"},
		{"grpc without port", agentMonitor(map[string]any{"protocol": "grpc"}), "ort is required"},
		{"tcp without port", agentMonitor(map[string]any{"protocol": "tcp"}), "ort is required"},
		{"bad record type", agentMonitor(map[string]any{"protocol": "dns", "check": map[string]any{"recordType": "PTR"}}), "record type"},
		{"tls and starttls", agentMonitor(map[string]any{"protocol": "smtp", "check": map[string]any{"tls": true, "startTLS": true}}), "cannot be combined"},
		{"url target", agentMonitor(map[string]any{"protocol": "postgres", "target": "postgres://db"}), "host name"},
		{"malformed check", agentMonitor(map[string]any{"protocol": "ssh", "check": map[string]any{"banner": 5}}), "nvalid check options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner, tc.body)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), tc.message)
		})
	}

	t.Run("hub monitor with a new protocol", func(t *testing.T) {
		record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "redis", "target": "cache.internal"}))
		assert.Equal(t, 6379, record.GetInt("port"))
	})
}

func TestMonitorCheckProtocolsRequireAgentVersion(t *testing.T) {
	env := newMonitorTestEnv(t)
	setVersion := func(version string) {
		info, err := json.Marshal(map[string]any{"v": version})
		require.NoError(t, err)
		env.system.Set("info", string(info))
		require.NoError(t, env.hub.SaveNoValidate(env.system))
	}
	body := map[string]any{"system": env.system.Id, "target": "db.internal", "protocol": "postgres", "interval": 60}

	setVersion("0.20.3")
	response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner, body)
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "require agent version 0.21.0 or newer")

	// Existing protocols still work on older agents.
	env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60})

	setVersion("0.21.0")
	record := env.createMonitor(t, body)
	assert.Equal(t, "postgres", record.GetString("protocol"))

	// A system that never reported a version is not blocked; syncs gate it.
	setVersion("")
	env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "web", "protocol": "docker", "interval": 60})
}
