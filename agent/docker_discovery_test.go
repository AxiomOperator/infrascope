//go:build testing

package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverContainerMonitors(t *testing.T) {
	ports := []container.ApiPort{
		{PrivatePort: 80, PublicPort: 8080, IP: "0.0.0.0", Type: "tcp"},
		{PrivatePort: 80, PublicPort: 8080, IP: "::", Type: "tcp"},
		{PrivatePort: 53, PublicPort: 5353, Type: "udp"},
		{PrivatePort: 5432, PublicPort: 15432, Type: "tcp"},
	}
	tests := []struct {
		name   string
		labels map[string]string
		ports  []container.ApiPort
		want   []system.DiscoveredMonitor
	}{
		{name: "no labels"},
		{name: "unrelated labels", labels: map[string]string{"com.docker.compose.project": "x"}},
		{
			name:   "single monitor with published port",
			labels: map[string]string{"infrascope.monitor.type": "http", "infrascope.monitor.name": "Web"},
			ports:  ports,
			want: []system.DiscoveredMonitor{{Container: "web", Key: "default", Port: 8080,
				Labels: map[string]string{"type": "http", "name": "Web"}}},
		},
		{
			name: "container port label maps to published host port",
			labels: map[string]string{
				"infrascope.monitor.db.type": "postgres", "infrascope.monitor.db.port": "5432",
				"infrascope.monitor.raw.type": "tcp", "infrascope.monitor.raw.port": "9000",
			},
			ports: ports,
			want: []system.DiscoveredMonitor{
				{Container: "web", Key: "db", Port: 15432, Labels: map[string]string{"type": "postgres", "port": "5432"}},
				{Container: "web", Key: "raw", Port: 9000, Labels: map[string]string{"type": "tcp", "port": "9000"}},
			},
		},
		{
			name:   "container disabled",
			labels: map[string]string{"infrascope.monitor.enable": "false", "infrascope.monitor.type": "tcp"},
		},
		{
			name: "one monitor disabled",
			labels: map[string]string{
				"infrascope.monitor.a.type": "tcp", "infrascope.monitor.a.enable": "false",
				"infrascope.monitor.b.type": "icmp", "infrascope.monitor.b.enable": "true",
			},
			want: []system.DiscoveredMonitor{{Container: "web", Key: "b", Labels: map[string]string{"type": "icmp"}}},
		},
		{
			name: "traefik routers",
			labels: map[string]string{
				"traefik.http.routers.app.rule":             "Host(`app.example.com`) || Host(`www.example.com`)",
				"traefik.http.routers.app.tls":              "true",
				"traefik.http.routers.api.rule":             "Host(`api.example.com`, `API.example.com`) && PathPrefix(`/v1`)",
				"traefik.http.routers.api.entrypoints":      "web",
				"traefik.http.routers.sec.rule":             "Host(\"sec.example.com\")",
				"traefik.http.routers.sec.entrypoints":      "websecure",
				"traefik.http.routers.sni.rule":             "HostSNI(`*`)",
				"traefik.http.services.app.loadbalancer":    "x",
				"traefik.http.routers.app.middlewares":      "auth",
				"traefik.http.routers.res.rule":             "Host(`res.example.com`)",
				"traefik.http.routers.res.tls.certresolver": "le",
			},
			want: []system.DiscoveredMonitor{
				{Container: "web", Key: "traefik.api.api.example.com", Traefik: true,
					Labels: map[string]string{"type": "http", "target": "http://api.example.com/v1"}},
				{Container: "web", Key: "traefik.app.app.example.com", Traefik: true,
					Labels: map[string]string{"type": "http", "target": "https://app.example.com"}},
				{Container: "web", Key: "traefik.app.www.example.com", Traefik: true,
					Labels: map[string]string{"type": "http", "target": "https://www.example.com"}},
				{Container: "web", Key: "traefik.res.res.example.com", Traefik: true,
					Labels: map[string]string{"type": "http", "target": "https://res.example.com"}},
				{Container: "web", Key: "traefik.sec.sec.example.com", Traefik: true,
					Labels: map[string]string{"type": "http", "target": "https://sec.example.com"}},
			},
		},
		{
			name: "traefik opt-out",
			labels: map[string]string{
				"traefik.http.routers.app.rule": "Host(`app.example.com`)",
				"infrascope.monitor.traefik":    "false",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, discoverContainerMonitors("web", tt.labels, tt.ports))
		})
	}
}

func TestDiscoverContainerMonitorsLimits(t *testing.T) {
	labels := map[string]string{"infrascope.monitor.name": strings.Repeat("x", 5000)}
	for i := range 50 {
		labels[fmt.Sprintf("infrascope.monitor.m%02d.type", i)] = "tcp"
	}
	monitors := discoverContainerMonitors("web", labels, nil)
	assert.Len(t, monitors, discoveryMaxPerContainer)
	assert.Len(t, monitors[0].Labels["name"], discoveryMaxValueLen)
}

func TestGetDockerStatsDiscovery(t *testing.T) {
	list := `[
		{"Id":"aaaaaaaaaaaaaaaa","Names":["/web"],"Status":"Up 2 hours","Labels":{"infrascope.monitor.type":"http"},"Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]},
		{"Id":"bbbbbbbbbbbbbbbb","Names":["/secret-db"],"Status":"Up 2 hours","Labels":{"infrascope.monitor.type":"tcp"}},
		{"Id":"cccccccccccccccc","Names":["/plain"],"Status":"Up 2 hours"}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			fmt.Fprint(w, list)
		case strings.Contains(r.URL.Path, "/stats"):
			fmt.Fprint(w, `{"memory_stats":{"usage":1048576},"cpu_stats":{},"networks":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dm := newDockerManagerForVersionTest(server)
	dm.dockerVersionChecked = true
	dm.imageUpdatesDisabled = true
	dm.excludeContainers = []string{"secret-*"}

	assert.Nil(t, dm.getDiscovery(), "unknown before the first listing")
	_, err := dm.getDockerStats(defaultCacheTimeMs)
	require.NoError(t, err)
	discovery := dm.getDiscovery()
	require.NotNil(t, discovery)
	assert.Equal(t, []system.DiscoveredMonitor{{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http"}}},
		discovery.Monitors, "excluded containers are skipped")
	for _, ctr := range dm.apiContainerList {
		assert.Nil(t, ctr.Labels, "labels are cleared so reused structs cannot merge them")
	}

	// Labels removed from a container are not merged from the reused struct.
	list = `[{"Id":"aaaaaaaaaaaaaaaa","Names":["/web"],"Status":"Up 2 hours"}]`
	_, err = dm.getDockerStats(defaultCacheTimeMs)
	require.NoError(t, err)
	discovery = dm.getDiscovery()
	require.NotNil(t, discovery)
	assert.Empty(t, discovery.Monitors)

	// A failed listing makes discovery unknown.
	server.Close()
	_, err = dm.getDockerStats(defaultCacheTimeMs)
	require.Error(t, err)
	assert.Nil(t, dm.getDiscovery())
}

func TestAttachDiscovery(t *testing.T) {
	dm := &dockerManager{}
	dm.setDiscovery([]system.DiscoveredMonitor{{Container: "web", Key: "default"}})
	a := &Agent{dockerManager: dm}
	data := &system.CombinedData{}

	assert.Same(t, data, a.attachDiscovery(data, defaultDataCacheTimeMs, false), "not requested")
	assert.Same(t, data, a.attachDiscovery(data, 1000, true), "only with default-interval data")
	response := a.attachDiscovery(data, defaultDataCacheTimeMs, true)
	require.NotNil(t, response.Discovery)
	assert.Nil(t, data.Discovery, "the cached data is not modified")

	dm.setDiscovery(nil)
	assert.Same(t, data, a.attachDiscovery(data, defaultDataCacheTimeMs, true), "unknown discovery is omitted")
}

func TestDiscoveryCBORCompat(t *testing.T) {
	// Discovery is omitted unless set, and an empty list stays distinguishable.
	encoded, err := cbor.Marshal(system.CombinedData{})
	require.NoError(t, err)
	var decoded system.CombinedData
	require.NoError(t, cbor.Unmarshal(encoded, &decoded))
	assert.Nil(t, decoded.Discovery)

	encoded, err = cbor.Marshal(system.CombinedData{Discovery: &system.Discovery{}})
	require.NoError(t, err)
	decoded = system.CombinedData{}
	require.NoError(t, cbor.Unmarshal(encoded, &decoded))
	assert.NotNil(t, decoded.Discovery)
	assert.Empty(t, decoded.Discovery.Monitors)

	// Older agents ignore the request flag; older hubs never set it.
	encoded, err = cbor.Marshal(common.DataRequestOptions{CacheTimeMs: 60_000})
	require.NoError(t, err)
	var options common.DataRequestOptions
	require.NoError(t, cbor.Unmarshal(encoded, &options))
	assert.False(t, options.Discovery)
	var legacy struct {
		CacheTimeMs    uint16 `cbor:"0,keyasint"`
		IncludeDetails bool   `cbor:"1,keyasint"`
	}
	encoded, err = cbor.Marshal(common.DataRequestOptions{CacheTimeMs: 60_000, Discovery: true})
	require.NoError(t, err)
	require.NoError(t, cbor.Unmarshal(encoded, &legacy))
	assert.Equal(t, uint16(60_000), legacy.CacheTimeMs)
}
