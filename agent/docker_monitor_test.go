//go:build testing

package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerMonitorProbe(t *testing.T) {
	containers := map[string]string{
		"web":      `{"Name":"/web","State":{"Status":"running","Running":true}}`,
		"healthy":  `{"Name":"/healthy","State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`,
		"starting": `{"Name":"/starting","State":{"Status":"running","Running":true,"Health":{"Status":"starting"}}}`,
		"sick":     `{"Name":"/sick","State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy"}}}`,
		"crashed":  `{"Name":"/crashed","State":{"Status":"exited","Running":false,"ExitCode":1}}`,
		"paused":   `{"Name":"/paused","State":{"Status":"paused","Running":false}}`,
		// an ID resolving to an excluded container
		"abcdef123456": `{"Name":"/secret-db","State":{"Status":"running","Running":true}}`,
	}
	var paths []string
	dm := &dockerManager{
		excludeContainers: []string{"secret-*"},
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			paths = append(paths, r.URL.Path)
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
			body, ok := containers[name]
			status := http.StatusOK
			if !ok {
				status, body = http.StatusNotFound, `{"message":"No such container"}`
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	}
	probe := dockerMonitorProbe(dm)
	for _, tc := range []struct {
		target string
		err    string
	}{
		{"web", ""},
		{"healthy", ""},
		{"starting", "health: starting"},
		{"sick", "unhealthy"},
		{"crashed", "container exited (code 1)"},
		{"paused", "container paused"},
		{"missing", "container not found"},
		{"abcdef123456", "excluded"},
		{"secret-cache", "excluded"},
		{"../../version", "invalid container name or ID"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			out := probe(context.Background(), monitor.Config{Protocol: monitor.ProtocolDocker, Target: tc.target})
			if tc.err == "" {
				require.NoError(t, out.Err)
				assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
				return
			}
			require.Error(t, out.Err)
			assert.Equal(t, tc.err, out.Err.Error())
			assert.EqualValues(t, -1, out.ResponseUs)
		})
	}
	assert.NotContains(t, paths, "/containers/secret-cache/json", "excluded names are not inspected")

	out := dockerMonitorProbe(nil)(context.Background(), monitor.Config{Protocol: monitor.ProtocolDocker, Target: "web"})
	require.Error(t, out.Err)
	assert.Equal(t, "docker is not available", out.Err.Error())
}

func TestDockerMonitorRegisteredWithManager(t *testing.T) {
	dm := &dockerManager{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"Name":"/web","State":{"Status":"running","Running":true}}`))}, nil
	})}}
	manager := newMonitorManager(dm)
	defer manager.Stop()
	result, err := manager.UpsertMonitor(monitor.Config{ID: "m1", Protocol: monitor.ProtocolDocker, Target: "web", Interval: 3600}, true)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Checks, 1)
	assert.False(t, result.Checks[0].Failed(), result.Checks[0].Err)
}
