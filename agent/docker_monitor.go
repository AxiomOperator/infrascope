package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/netmon"
)

// Fixed failure messages of docker container checks.
var (
	errDockerMonitorUnavailable = errors.New("docker is not available")
	errDockerMonitorExcluded    = errors.New("excluded")
	errDockerMonitorNotFound    = errors.New("container not found")
	errDockerMonitorTarget      = errors.New("invalid container name or ID")
	errDockerMonitorStarting    = errors.New("health: starting")
	errDockerMonitorUnhealthy   = errors.New("unhealthy")
)

// dockerContainerState holds the inspect fields of a container check.
type dockerContainerState struct {
	Name  string
	State struct {
		Status   string
		Running  bool
		ExitCode int
		Health   *struct {
			Status string
		}
	}
}

// dockerMonitorProbe returns the probe of docker monitors. The target is a
// container name or ID; a running container succeeds unless its health
// check reports it starting or unhealthy.
func dockerMonitorProbe(dm *dockerManager) netmon.ProbeFunc {
	return func(ctx context.Context, config monitor.Config) netmon.Outcome {
		if dm == nil {
			return netmon.Outcome{ResponseUs: -1, Err: errDockerMonitorUnavailable}
		}
		ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
		defer cancel()
		start := time.Now()
		err := dm.checkContainer(ctx, config.Target)
		if err != nil {
			return netmon.Outcome{ResponseUs: -1, Err: err}
		}
		return netmon.Outcome{ResponseUs: time.Since(start).Microseconds()}
	}
}

// checkContainer inspects a container and reports why it is not running and
// healthy.
func (dm *dockerManager) checkContainer(ctx context.Context, ref string) error {
	if !monitor.ValidContainerRef(ref) {
		return errDockerMonitorTarget
	}
	// Names of excluded containers are not inspected at all.
	if dm.shouldExcludeContainer(ref) {
		return errDockerMonitorExcluded
	}
	endpoint := (&url.URL{Scheme: "http", Host: "localhost", Path: "/containers/" + url.PathEscape(ref) + "/json"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := dm.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("timeout")
		}
		return errDockerMonitorUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return errDockerMonitorNotFound
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("docker api status %d", resp.StatusCode)
	}
	var info dockerContainerState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return errors.New("invalid docker api response")
	}
	// An ID or name prefix may resolve to an excluded container.
	if dm.shouldExcludeContainer(strings.TrimPrefix(info.Name, "/")) {
		return errDockerMonitorExcluded
	}
	return containerStateError(info)
}

// containerStateError returns nil for a running, healthy container (or one
// without a health check), and otherwise a fixed error describing its state.
func containerStateError(info dockerContainerState) error {
	state := info.State
	status := strings.ToLower(state.Status)
	if !state.Running && status == "" {
		status = "exited"
	}
	switch status {
	case "running", "":
	case "exited":
		return fmt.Errorf("container exited (code %d)", state.ExitCode)
	case "paused", "restarting", "created", "removing", "dead", "stopped", "stopping", "configured", "initialized":
		return fmt.Errorf("container %s", status)
	default:
		return errors.New("container not running")
	}
	if state.Health != nil {
		switch strings.ToLower(state.Health.Status) {
		case "starting":
			return errDockerMonitorStarting
		case "unhealthy":
			return errDockerMonitorUnhealthy
		}
	}
	return nil
}
