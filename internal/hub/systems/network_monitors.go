package systems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/blang/semver"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/dbx"
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

// MonitorHTTPSecrets are the secret options stored in the network_monitors
// "httpSecrets" field, which is stored encrypted and only shown to users who
// can edit the monitor. HTTP monitors use the headers, body and basic auth
// credentials; postgres and redis monitors the username and password.
type MonitorHTTPSecrets struct {
	Headers   [][2]string `json:"headers,omitzero"`
	Body      string      `json:"body,omitzero"`
	BasicUser string      `json:"basicUser,omitzero"`
	BasicPass string      `json:"basicPass,omitzero"`
	Username  string      `json:"username,omitzero"`
	Password  string      `json:"password,omitzero"`
}

// MonitorCheckFields are the non-secret check options stored in the
// network_monitors "check" field (see monitor.CheckOptions).
type MonitorCheckFields struct {
	RecordType string `json:"recordType,omitzero"`
	Expected   string `json:"expected,omitzero"`
	MatchMode  string `json:"matchMode,omitzero"`
	Banner     string `json:"banner,omitzero"`
	TLS        bool   `json:"tls,omitzero"`
	StartTLS   bool   `json:"startTLS,omitzero"`
	IgnoreTLS  bool   `json:"ignoreTLS,omitzero"`
	Service    string `json:"service,omitzero"`
}

// SplitCheckOptions returns the stored field values of check options: the
// non-secret fields and the credentials.
func SplitCheckOptions(check *monitor.CheckOptions) (MonitorCheckFields, MonitorHTTPSecrets) {
	if check == nil {
		return MonitorCheckFields{}, MonitorHTTPSecrets{}
	}
	return MonitorCheckFields{
		RecordType: check.RecordType,
		Expected:   check.Expected,
		MatchMode:  check.MatchMode,
		Banner:     check.Banner,
		TLS:        check.TLS,
		StartTLS:   check.StartTLS,
		IgnoreTLS:  check.IgnoreTLS,
		Service:    check.Service,
	}, MonitorHTTPSecrets{Username: check.Username, Password: check.Password}
}

// usesSecretCredentials reports whether monitors of protocol keep a username
// and password in httpSecrets.
func usesSecretCredentials(protocol string) bool {
	return protocol == monitor.ProtocolPostgres || protocol == monitor.ProtocolRedis
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

// HTTPSecretsJSON returns the plaintext JSON of a record's httpSecrets field,
// opening it with the key in app's data dir when it is sealed. Plaintext
// values (stored before encryption, or not yet saved) are returned as is.
func HTTPSecretsJSON(app core.App, record *core.Record) (string, error) {
	raw := record.GetString("httpSecrets")
	if !monitorsecrets.IsSealedJSON(raw) {
		return raw, nil
	}
	if app == nil {
		return "", errors.New("sealed http secrets require the hub key")
	}
	box, err := monitorsecrets.ForDataDir(app.DataDir())
	if err != nil {
		return "", err
	}
	return box.OpenJSON(raw)
}

// MonitorConfigFromRecord builds the probe config of a network_monitors record.
// HTTP options are only set for http monitors with non-default options, and
// check options only with the fields the protocol uses. It fails when the
// stored options are not valid JSON of the expected shape, or sealed secrets
// cannot be opened with the key of app's data dir.
func MonitorConfigFromRecord(app core.App, record *core.Record) (monitor.Config, error) {
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
	if config.Protocol != monitor.ProtocolHTTP {
		return config, checkOptionsFromRecord(app, record, &config)
	}
	var fields MonitorHTTPFields
	var secrets MonitorHTTPSecrets
	if err := unmarshalJSONField(record, "http", &fields); err != nil {
		return config, fmt.Errorf("invalid http options: %w", err)
	}
	rawSecrets, err := HTTPSecretsJSON(app, record)
	if err != nil {
		return config, fmt.Errorf("http secrets: %w", err)
	}
	if err := unmarshalJSON(rawSecrets, &secrets); err != nil {
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

// checkOptionsFromRecord sets the check options of a non-http monitor.
func checkOptionsFromRecord(app core.App, record *core.Record, config *monitor.Config) error {
	var fields MonitorCheckFields
	if err := unmarshalJSONField(record, "check", &fields); err != nil {
		return fmt.Errorf("invalid check options: %w", err)
	}
	var secrets MonitorHTTPSecrets
	if usesSecretCredentials(config.Protocol) {
		rawSecrets, err := HTTPSecretsJSON(app, record)
		if err != nil {
			return fmt.Errorf("monitor secrets: %w", err)
		}
		if err := unmarshalJSON(rawSecrets, &secrets); err != nil {
			return fmt.Errorf("invalid monitor secrets: %w", err)
		}
	}
	check := monitor.CheckOptions{
		RecordType: fields.RecordType,
		Expected:   fields.Expected,
		MatchMode:  fields.MatchMode,
		Banner:     fields.Banner,
		TLS:        fields.TLS,
		StartTLS:   fields.StartTLS,
		IgnoreTLS:  fields.IgnoreTLS,
		Service:    fields.Service,
		Username:   secrets.Username,
		Password:   secrets.Password,
	}
	config.Check = check.ForProtocol(config.Protocol)
	return nil
}

// unmarshalJSONField decodes a JSON field, treating an empty or null value as unset.
func unmarshalJSONField(record *core.Record, field string, dest any) error {
	return unmarshalJSON(record.GetString(field), dest)
}

// unmarshalJSON decodes raw JSON, treating an empty or null value as unset.
func unmarshalJSON(raw string, dest any) error {
	if raw == "" || raw == "null" {
		return nil
	}
	return json.Unmarshal([]byte(raw), dest)
}

// FindLocationMonitors returns the monitors with systemID as one of their
// locations (the hub for ""), optionally filtered further by where.
func FindLocationMonitors(app core.App, systemID string, where dbx.Expression) ([]*core.Record, error) {
	location := monitorloc.FromSystemID(systemID)
	var condition dbx.Expression = dbx.HashExp{"system": systemID}
	if systemID != "" {
		condition = dbx.Or(condition, dbx.NewExp(
			"EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(locationSystems) THEN locationSystems ELSE '[]' END) WHERE value = {:location})",
			dbx.Params{"location": systemID}))
	} else {
		condition = dbx.Or(condition, dbx.NewExp(
			"EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(locations) THEN locations ELSE '[]' END) WHERE value = {:location})",
			dbx.Params{"location": location}))
	}
	if where != nil {
		condition = dbx.And(condition, where)
	}
	records, err := app.FindAllRecords("network_monitors", condition)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(records, func(record *core.Record) bool {
		return !monitorloc.Has(record, location)
	}), nil
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
	// Agents that cannot run a monitor's protocol never report its checks,
	// so its status is unknown rather than stale.
	if agentVersion := sys.getAgentVersion(); agentVersion.GTE(beszel.MinVersionNetworkMonitors) &&
		agentVersion.LT(beszel.MinVersionMonitorChecks) {
		var unsupported []string
		for _, config := range configs {
			if monitor.IsCheckProtocol(config.Protocol) {
				unsupported = append(unsupported, config.ID)
			}
		}
		if engine := sys.manager.hub.Uptime(); engine != nil && len(unsupported) > 0 {
			engine.MarkLocationUnknown(sys.Id, unsupported)
		}
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
	if req.Action == monitor.SyncActionUpsert && agentVersion.LT(beszel.MinVersionMonitorChecks) &&
		monitor.IsCheckProtocol(req.Config.Protocol) {
		return monitor.SyncResponse{}, ErrAgentTooOldForProtocol
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

// ErrAgentTooOldForProtocol is returned when a monitor of a protocol added
// with check options is synced to an agent that cannot run it.
var ErrAgentTooOldForProtocol = fmt.Errorf("monitor protocol requires agent version %s or newer", beszel.MinVersionMonitorChecks)

// syncRequestForAgent strips config fields the agent version does not support,
// so older agents keep probing with their defaults. Monitors of protocols the
// agent cannot run are left out of full syncs.
func syncRequestForAgent(req monitor.SyncRequest, agentVersion semver.Version) monitor.SyncRequest {
	if agentVersion.GTE(beszel.MinVersionMonitorChecks) {
		return req
	}
	req.Config = req.Config.Legacy()
	if req.Configs != nil {
		configs := make([]monitor.Config, 0, len(req.Configs))
		for _, config := range req.Configs {
			if monitor.IsCheckProtocol(config.Protocol) {
				continue
			}
			configs = append(configs, config.Legacy())
		}
		req.Configs = configs
	}
	return req
}
