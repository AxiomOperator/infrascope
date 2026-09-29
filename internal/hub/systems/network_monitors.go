package systems

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/blang/semver"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/pocketbase/pocketbase/core"
)

// MonitorHTTPFields are the non-secret HTTP options stored in the network_monitors "http" field.
type MonitorHTTPFields struct {
	Method        string   `json:"method,omitzero"`
	AcceptedCodes []string `json:"acceptedCodes,omitzero"`
	MaxRedirects  int8     `json:"maxRedirects,omitzero"`
	IgnoreTLS     bool     `json:"ignoreTLS,omitzero"`
	Keyword       string   `json:"keyword,omitzero"`
	KeywordInvert bool     `json:"keywordInvert,omitzero"`
	JSONPath      string   `json:"jsonPath,omitzero"`
	JSONExpected  string   `json:"jsonExpected,omitzero"`
}

// MonitorHTTPSecrets are the HTTP options stored in the network_monitors
// "httpSecrets" field, which is only shown to users who can edit the monitor.
type MonitorHTTPSecrets struct {
	Headers   [][2]string `json:"headers,omitzero"`
	Body      string      `json:"body,omitzero"`
	BasicUser string      `json:"basicUser,omitzero"`
	BasicPass string      `json:"basicPass,omitzero"`
}

// SplitHTTPOptions returns the stored field values of options.
func SplitHTTPOptions(options *monitor.HTTPOptions) (MonitorHTTPFields, MonitorHTTPSecrets) {
	if options == nil {
		return MonitorHTTPFields{}, MonitorHTTPSecrets{}
	}
	return MonitorHTTPFields{
			Method:        options.Method,
			AcceptedCodes: options.AcceptedCodes,
			MaxRedirects:  options.MaxRedirects,
			IgnoreTLS:     options.IgnoreTLS,
			Keyword:       options.Keyword,
			KeywordInvert: options.KeywordInvert,
			JSONPath:      options.JSONPath,
			JSONExpected:  options.JSONExpected,
		}, MonitorHTTPSecrets{
			Headers:   options.Headers,
			Body:      options.Body,
			BasicUser: options.BasicUser,
			BasicPass: options.BasicPass,
		}
}

// MonitorConfigFromRecord builds the probe config of a network_monitors record.
// HTTP options are only set for http monitors with non-default options. It
// fails when the stored HTTP options are not valid JSON of the expected shape.
func MonitorConfigFromRecord(record *core.Record) (monitor.Config, error) {
	config := monitor.Config{
		ID:            record.Id,
		Target:        record.GetString("target"),
		Protocol:      record.GetString("protocol"),
		Port:          uint16(record.GetInt("port")),
		Interval:      uint16(record.GetInt("interval")),
		Server:        record.GetString("server"),
		Timeout:       uint16(record.GetInt("timeout")),
		RetryInterval: uint16(record.GetInt("retryInterval")),
	}
	if config.Protocol != "http" {
		return config, nil
	}
	var fields MonitorHTTPFields
	var secrets MonitorHTTPSecrets
	if err := unmarshalJSONField(record, "http", &fields); err != nil {
		return config, fmt.Errorf("invalid http options: %w", err)
	}
	if err := unmarshalJSONField(record, "httpSecrets", &secrets); err != nil {
		return config, fmt.Errorf("invalid http secrets: %w", err)
	}
	options := monitor.HTTPOptions{
		Method:        fields.Method,
		Headers:       secrets.Headers,
		Body:          secrets.Body,
		AcceptedCodes: fields.AcceptedCodes,
		MaxRedirects:  fields.MaxRedirects,
		IgnoreTLS:     fields.IgnoreTLS,
		Keyword:       fields.Keyword,
		KeywordInvert: fields.KeywordInvert,
		JSONPath:      fields.JSONPath,
		JSONExpected:  fields.JSONExpected,
		BasicUser:     secrets.BasicUser,
		BasicPass:     secrets.BasicPass,
	}
	if len(options.Headers) == 0 {
		options.Headers = nil
	}
	if len(options.AcceptedCodes) == 0 {
		options.AcceptedCodes = nil
	}
	if !reflect.ValueOf(options).IsZero() {
		config.HTTP = &options
	}
	return config, nil
}

// unmarshalJSONField decodes a JSON field, treating an empty or null value as unset.
func unmarshalJSONField(record *core.Record, field string, dest any) error {
	raw := record.GetString(field)
	if raw == "" || raw == "null" {
		return nil
	}
	return json.Unmarshal([]byte(raw), dest)
}

// syncPendingNetworkMonitors runs on WebSocket connect and after successful stats
// fetches. Failed syncs retry on the next update without taking the system down.
func (sys *System) syncPendingNetworkMonitors() {
	if !sys.monitorsNeedSync.Swap(false) {
		return
	}
	if err := sys.syncAllNetworkMonitors(); err != nil {
		sys.monitorsNeedSync.Store(true)
		sys.manager.hub.Logger().Warn("failed to sync monitors to agent", "system", sys.Id, "err", err)
	}
}

func (sys *System) syncAllNetworkMonitors() error {
	configs, err := sys.manager.GetMonitorConfigsForSystem(sys.Id)
	if err != nil {
		return fmt.Errorf("failed to load monitors: %w", err)
	}
	// An empty set must also replace probes retained across a disconnect.
	return sys.SyncNetworkMonitors(configs)
}

// SyncNetworkMonitors sends monitor configurations to the agent.
func (sys *System) SyncNetworkMonitors(configs []monitor.Config) error {
	_, err := sys.syncNetworkMonitors(monitor.SyncRequest{Action: monitor.SyncActionReplace, Configs: configs})
	return err
}

// UpsertNetworkMonitor sends a single monitor configuration change to the agent.
func (sys *System) UpsertNetworkMonitor(config monitor.Config, runNow bool) (*monitor.Result, error) {
	resp, err := sys.syncNetworkMonitors(monitor.SyncRequest{
		Action: monitor.SyncActionUpsert,
		Config: config,
		RunNow: runNow,
	})
	if err != nil {
		return nil, err
	}
	if resp.Result.IsZero() {
		return nil, nil
	}
	result := resp.Result
	return &result, nil
}

// DeleteNetworkMonitor removes a single monitor task from the agent.
func (sys *System) DeleteNetworkMonitor(id string) error {
	_, err := sys.syncNetworkMonitors(monitor.SyncRequest{
		Action: monitor.SyncActionDelete,
		Config: monitor.Config{ID: id},
	})
	return err
}

func (sys *System) syncNetworkMonitors(req monitor.SyncRequest) (monitor.SyncResponse, error) {
	agentVersion := sys.getAgentVersion()
	if agentVersion.LT(beszel.MinVersionNetworkMonitors) {
		return monitor.SyncResponse{}, nil
	}
	req = syncRequestForAgent(req, agentVersion)
	timeout := 5 * time.Second
	if req.Action == monitor.SyncActionUpsert && req.RunNow {
		// Allow the probe to finish, including a timeout result, while preserving
		// the normal request budget for transport and response handling. The
		// agent bounds an immediate certificate check by the same timeout.
		timeout += req.Config.ProbeTimeout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var result monitor.SyncResponse
	return result, sys.request(ctx, common.SyncNetworkMonitors, req, &result)
}

// syncRequestForAgent strips config fields the agent version does not support,
// so older agents keep probing with their defaults.
func syncRequestForAgent(req monitor.SyncRequest, agentVersion semver.Version) monitor.SyncRequest {
	if agentVersion.GTE(beszel.MinVersionMonitorChecks) {
		return req
	}
	req.Config = req.Config.Legacy()
	if req.Configs != nil {
		configs := make([]monitor.Config, len(req.Configs))
		for i, config := range req.Configs {
			configs[i] = config.Legacy()
		}
		req.Configs = configs
	}
	return req
}
