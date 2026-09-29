package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Uptime Kuma import
//
// POST /api/beszel/import/uptime-kuma creates monitors from an Uptime Kuma
// backup (the JSON export of Kuma 1.x: version, notificationList,
// monitorList). Monitors run on the hub (system "") or, as agent monitors, on
// a system the requester can update. Each monitor goes through the same
// preparation as the record API (prepareMonitor) and is saved with the
// record hooks, so validation, secret encryption and runner sync apply.

const (
	kumaImportMaxBody     = 10 << 20
	kumaImportMaxMonitors = 1000
	// kumaCertExpiryDays is the certificate alert window of imported monitors
	// with Kuma's certificate expiry notification enabled.
	kumaCertExpiryDays = 14
	// Field limits of network_monitors.
	monitorMaxInterval      = 3600
	monitorMaxRetries       = 10
	monitorMaxRetryInterval = 3600
	monitorMaxNameLen       = 100
	monitorMaxTargetLen     = 500
)

type kumaImportRequest struct {
	Backup json.RawMessage `json:"backup"`
	DryRun bool            `json:"dryRun"`
	System string          `json:"system"`
}

type kumaImportNote struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
	Change string `json:"change,omitempty"`
}

type kumaImportedMonitor struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Target   string `json:"target"`
}

type kumaImportResponse struct {
	Created  int                   `json:"created"`
	Skipped  []kumaImportNote      `json:"skipped"`
	Adjusted []kumaImportNote      `json:"adjusted"`
	Monitors []kumaImportedMonitor `json:"monitors"`
}

type kumaBackup struct {
	Version     json.RawMessage `json:"version"`
	MonitorList []kumaMonitor   `json:"monitorList"`
}

// kumaMonitor is a monitor of a Kuma backup. Kuma stores booleans as 0/1 in
// some versions and many fields may be null, so loose types are used.
type kumaMonitor struct {
	ID                 kumaInt         `json:"id"`
	Name               kumaString      `json:"name"`
	Type               kumaString      `json:"type"`
	URL                kumaString      `json:"url"`
	Hostname           kumaString      `json:"hostname"`
	Port               kumaInt         `json:"port"`
	Interval           kumaInt         `json:"interval"`
	RetryInterval      kumaInt         `json:"retryInterval"`
	MaxRetries         kumaInt         `json:"maxretries"`
	Active             *kumaBool       `json:"active"`
	Keyword            kumaString      `json:"keyword"`
	InvertKeyword      kumaBool        `json:"invertKeyword"`
	JSONPath           kumaString      `json:"jsonPath"`
	JSONPathOperator   kumaString      `json:"jsonPathOperator"`
	ExpectedValue      kumaString      `json:"expectedValue"`
	Method             kumaString      `json:"method"`
	Headers            kumaString      `json:"headers"`
	Body               kumaString      `json:"body"`
	AcceptedStatus     []kumaString    `json:"accepted_statuscodes"`
	MaxRedirects       *kumaInt        `json:"maxredirects"`
	IgnoreTLS          kumaBool        `json:"ignoreTls"`
	UpsideDown         kumaBool        `json:"upsideDown"`
	AuthMethod         kumaString      `json:"authMethod"`
	BasicAuthUser      kumaString      `json:"basic_auth_user"`
	BasicAuthPass      kumaString      `json:"basic_auth_pass"`
	DNSResolveServer   kumaString      `json:"dns_resolve_server"`
	DNSResolveType     kumaString      `json:"dns_resolve_type"`
	PushToken          kumaString      `json:"pushToken"`
	Timeout            kumaFloat       `json:"timeout"`
	ExpiryNotification kumaBool        `json:"expiryNotification"`
	NotificationIDs    json.RawMessage `json:"notificationIDList"`
}

// kumaString accepts a string, number, boolean or null.
type kumaString string

func (s *kumaString) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch v := value.(type) {
	case nil:
		*s = ""
	case string:
		*s = kumaString(v)
	default:
		*s = kumaString(strings.TrimSpace(string(data)))
	}
	return nil
}

// kumaFloat accepts a number, numeric string, boolean or null.
type kumaFloat float64

func (f *kumaFloat) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch v := value.(type) {
	case float64:
		*f = kumaFloat(v)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		*f = kumaFloat(n)
	case bool:
		*f = 0
		if v {
			*f = 1
		}
	default:
		*f = 0
	}
	return nil
}

// kumaInt is a kumaFloat rounded to an int.
type kumaInt int

func (i *kumaInt) UnmarshalJSON(data []byte) error {
	var f kumaFloat
	if err := f.UnmarshalJSON(data); err != nil {
		return err
	}
	if math.IsNaN(float64(f)) || math.Abs(float64(f)) > math.MaxInt32 {
		f = 0
	}
	*i = kumaInt(math.Round(float64(f)))
	return nil
}

// kumaBool accepts true/false, 0/1, "0"/"1", "true"/"false" or null.
type kumaBool bool

func (b *kumaBool) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch v := value.(type) {
	case bool:
		*b = kumaBool(v)
	case float64:
		*b = v != 0
	case string:
		*b = kumaBool(v == "1" || strings.EqualFold(v, "true"))
	default:
		*b = false
	}
	return nil
}

// kumaSkip is the reason a Kuma monitor is not imported.
type kumaSkip string

// kumaConversion is a Kuma monitor converted to network_monitors fields.
type kumaConversion struct {
	name     string
	fields   map[string]any
	notes    []string
	pushKuma string // Kuma push token, kept when valid and unused
}

// convertKumaMonitor maps a Kuma monitor to network_monitors fields.
// minInterval is the shortest allowed interval.
func convertKumaMonitor(km kumaMonitor, minInterval int) (kumaConversion, kumaSkip) {
	conv := kumaConversion{fields: map[string]any{}}
	kind := strings.ToLower(strings.TrimSpace(string(km.Type)))
	note := func(format string, args ...any) { conv.notes = append(conv.notes, fmt.Sprintf(format, args...)) }

	if km.UpsideDown {
		return conv, "upside down mode is not supported"
	}
	var protocol, target string
	switch kind {
	case "http", "keyword", "json-query":
		protocol, target = "http", strings.TrimSpace(string(km.URL))
	case "port":
		protocol, target = "tcp", strings.TrimSpace(string(km.Hostname))
		if km.Port < 1 || km.Port > 65535 {
			return conv, "invalid port"
		}
		conv.fields["port"] = int(km.Port)
	case "ping":
		protocol, target = "icmp", strings.TrimSpace(string(km.Hostname))
	case "dns":
		protocol, target = "dns", strings.TrimSpace(string(km.Hostname))
		server := strings.TrimSpace(string(km.DNSResolveServer))
		if server != "" && km.Port > 0 && km.Port != 53 && km.Port <= 65535 {
			server = net.JoinHostPort(server, strconv.Itoa(int(km.Port)))
		}
		conv.fields["server"] = server
		if recordType := strings.ToUpper(strings.TrimSpace(string(km.DNSResolveType))); recordType != "" && recordType != "A" && recordType != "AAAA" {
			note("DNS record type %s is not supported; the host name is resolved instead", recordType)
		}
	case "push":
		protocol = monitor.ProtocolPush
		conv.pushKuma = strings.TrimSpace(string(km.PushToken))
	case "group":
		return conv, "groups are not imported"
	case "":
		return conv, "missing monitor type"
	default:
		return conv, kumaSkip("unsupported type " + kind)
	}
	if protocol != monitor.ProtocolPush && target == "" {
		return conv, "missing target"
	}
	if len(target) > monitorMaxTargetLen {
		return conv, "target is too long"
	}

	name := strings.TrimSpace(string(km.Name))
	if name == "" {
		name = target
	}
	if len([]rune(name)) > monitorMaxNameLen {
		name = string([]rune(name)[:monitorMaxNameLen])
		note("name shortened to %d characters", monitorMaxNameLen)
	}
	conv.name = name

	interval := int(km.Interval)
	if interval <= 0 {
		interval = 60
	}
	if interval < minInterval {
		note("interval raised from %ds to %ds", interval, minInterval)
		interval = minInterval
	} else if interval > monitorMaxInterval {
		note("interval lowered from %ds to %ds", interval, monitorMaxInterval)
		interval = monitorMaxInterval
	}
	retries := int(km.MaxRetries)
	if retries < 0 {
		retries = 0
	} else if retries > monitorMaxRetries {
		note("retries lowered from %d to %d", retries, monitorMaxRetries)
		retries = monitorMaxRetries
	}
	retryInterval := int(km.RetryInterval)
	if retryInterval < 0 || retryInterval == interval {
		retryInterval = 0
	} else if retryInterval > monitorMaxRetryInterval {
		note("retry interval lowered from %ds to %ds", retryInterval, monitorMaxRetryInterval)
		retryInterval = monitorMaxRetryInterval
	}
	timeout := 0
	if km.Timeout > 0 {
		timeout = int(math.Ceil(float64(km.Timeout)))
		if maxTimeout := int(monitor.MaxProbeTimeout.Seconds()); timeout > maxTimeout {
			note("timeout lowered from %ds to %ds", timeout, maxTimeout)
			timeout = maxTimeout
		}
	}
	enabled := km.Active == nil || bool(*km.Active)

	conv.fields["name"] = name
	conv.fields["protocol"] = protocol
	conv.fields["target"] = target
	conv.fields["interval"] = interval
	conv.fields["retries"] = retries
	conv.fields["retryInterval"] = retryInterval
	conv.fields["timeout"] = timeout
	conv.fields["enabled"] = enabled
	conv.fields["notify"] = kumaHasNotifications(km.NotificationIDs)

	if protocol == "http" {
		options, skip := kumaHTTPOptions(km, kind, note)
		if skip != "" {
			return conv, skip
		}
		conv.fields["http"] = map[string]any{
			"method": options.Method, "acceptedCodes": options.AcceptedCodes, "maxRedirects": options.MaxRedirects,
			"ignoreTLS": options.IgnoreTLS, "keyword": options.Keyword, "keywordInvert": options.KeywordInvert,
			"jsonPath": options.JSONPath, "jsonExpected": options.JSONExpected,
		}
		conv.fields["httpSecrets"] = map[string]any{
			"headers": options.Headers, "body": options.Body, "basicUser": options.BasicUser, "basicPass": options.BasicPass,
		}
		if bool(km.ExpiryNotification) && strings.HasPrefix(strings.ToLower(target), "https://") {
			conv.fields["certExpiryDays"] = kumaCertExpiryDays
		}
	}
	return conv, ""
}

// kumaHTTPOptions maps the HTTP settings of a Kuma http, keyword or json-query monitor.
func kumaHTTPOptions(km kumaMonitor, kind string, note func(string, ...any)) (monitor.HTTPOptions, kumaSkip) {
	options := monitor.HTTPOptions{
		Method:    strings.ToUpper(strings.TrimSpace(string(km.Method))),
		Body:      string(km.Body),
		IgnoreTLS: bool(km.IgnoreTLS),
	}
	if options.Method == "GET" {
		options.Method = ""
	}
	for _, code := range km.AcceptedStatus {
		if code := strings.TrimSpace(string(code)); code != "" {
			options.AcceptedCodes = append(options.AcceptedCodes, code)
		}
	}
	// Kuma's default; it is the default here too.
	if slices.Equal(options.AcceptedCodes, []string{"200-299"}) {
		options.AcceptedCodes = nil
	}
	if km.MaxRedirects != nil {
		switch redirects := int(*km.MaxRedirects); {
		case redirects <= 0:
			// Kuma's 0 follows no redirects.
			options.MaxRedirects = -1
		case redirects > monitor.MaxHTTPRedirects:
			note("max redirects lowered from %d to %d", redirects, monitor.MaxHTTPRedirects)
			options.MaxRedirects = monitor.MaxHTTPRedirects
		default:
			options.MaxRedirects = int8(redirects)
		}
	}
	if headers := strings.TrimSpace(string(km.Headers)); headers != "" && headers != "null" {
		var values map[string]any
		if err := json.Unmarshal([]byte(headers), &values); err != nil {
			return options, "headers are not a JSON object"
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			value, ok := values[name].(string)
			if !ok {
				encoded, _ := json.Marshal(values[name])
				value = string(encoded)
			}
			options.Headers = append(options.Headers, [2]string{name, value})
		}
	}
	switch method := strings.ToLower(strings.TrimSpace(string(km.AuthMethod))); method {
	case "", "null", "basic":
		options.BasicUser = string(km.BasicAuthUser)
		options.BasicPass = string(km.BasicAuthPass)
	default:
		return options, kumaSkip("unsupported authentication method " + method)
	}
	switch kind {
	case "keyword":
		if km.Keyword == "" {
			return options, "missing keyword"
		}
		options.Keyword = string(km.Keyword)
		options.KeywordInvert = bool(km.InvertKeyword)
	case "json-query":
		if operator := strings.TrimSpace(string(km.JSONPathOperator)); operator != "" && operator != "==" {
			return options, kumaSkip("unsupported JSON query operator " + operator)
		}
		path := strings.TrimPrefix(strings.TrimSpace(string(km.JSONPath)), "$.")
		if _, err := monitor.ParseJSONPath(path); err != nil {
			return options, "unsupported JSON query"
		}
		options.JSONPath = path
		options.JSONExpected = string(km.ExpectedValue)
	}
	return options, ""
}

// kumaHasNotifications reports whether a notificationIDList (an object of
// notification IDs to true, or an array) enables any notification.
func kumaHasNotifications(raw json.RawMessage) bool {
	var byID map[string]any
	if err := json.Unmarshal(raw, &byID); err == nil {
		for _, enabled := range byID {
			if enabled == true || enabled == float64(1) {
				return true
			}
		}
		return false
	}
	var list []any
	return json.Unmarshal(raw, &list) == nil && len(list) > 0
}

// parseKumaBackup decodes a backup given as a JSON object or as a JSON string holding one.
func parseKumaBackup(raw json.RawMessage) (kumaBackup, error) {
	var backup kumaBackup
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return backup, err
		}
		raw = json.RawMessage(text)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields["monitorList"] == nil {
		return backup, errors.New("not an Uptime Kuma backup")
	}
	if err := json.Unmarshal(raw, &backup); err != nil {
		return backup, fmt.Errorf("invalid Uptime Kuma backup: %w", err)
	}
	return backup, nil
}

// importUptimeKuma handles POST /api/beszel/import/uptime-kuma.
func (h *Hub) importUptimeKuma(e *core.RequestEvent) error {
	var req kumaImportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, kumaImportMaxBody))
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return e.Error(http.StatusRequestEntityTooLarge, "The backup is too large", nil)
		}
		return e.BadRequestError("Invalid request body", err)
	}
	backup, err := parseKumaBackup(req.Backup)
	if err != nil {
		return e.BadRequestError(err.Error(), nil)
	}
	if len(backup.MonitorList) > kumaImportMaxMonitors {
		return e.BadRequestError(fmt.Sprintf("A backup can have at most %d monitors", kumaImportMaxMonitors), nil)
	}

	superuser := e.HasSuperuserAuth()
	var user *core.Record
	if !superuser {
		user = e.Auth
	}
	minInterval := 1
	if req.System == "" {
		if user == nil {
			return e.BadRequestError("Hub monitors need a user; import into a system instead", nil)
		}
		minInterval = hubMonitorMinInterval()
	} else {
		system, err := e.App.FindRecordById("systems", req.System)
		if err != nil {
			return e.NotFoundError("System not found", nil)
		}
		if !superuser {
			// The body was consumed; system rules only need the auth record.
			info := &core.RequestInfo{
				Auth: e.Auth, Method: e.Request.Method, Context: core.RequestInfoContextDefault,
				Query: map[string]string{}, Headers: map[string]string{}, Body: map[string]any{},
			}
			collection := system.Collection()
			if ok, _ := e.App.CanAccessRecord(system, info, collection.ViewRule); !ok {
				return e.NotFoundError("System not found", nil)
			}
			if ok, _ := e.App.CanAccessRecord(system, info, collection.UpdateRule); !ok {
				return e.ForbiddenError("You cannot add monitors to this system.", nil)
			}
		}
	}

	result, err := h.runKumaImport(e.App, backup, req.System, user, superuser, minInterval, req.DryRun)
	if err != nil {
		return e.InternalServerError("Import failed", err)
	}
	return e.JSON(http.StatusOK, result)
}

// kumaMonitorKey identifies a monitor for duplicate detection.
func kumaMonitorKey(name, target, protocol string) string {
	return name + "\x00" + target + "\x00" + protocol
}

// runKumaImport converts, validates and (unless dryRun) creates the backup's
// monitors on systemID ("" for the hub). user is the requester, nil for superusers.
func (h *Hub) runKumaImport(app core.App, backup kumaBackup, systemID string, user *core.Record, superuser bool, minInterval int, dryRun bool) (kumaImportResponse, error) {
	result := kumaImportResponse{Skipped: []kumaImportNote{}, Adjusted: []kumaImportNote{}, Monitors: []kumaImportedMonitor{}}
	collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
	if err != nil {
		return result, err
	}
	existing, err := ownedMonitorKeys(app, systemID, user)
	if err != nil {
		return result, err
	}
	usedTokens := map[string]bool{}
	for i, km := range backup.MonitorList {
		label := strings.TrimSpace(string(km.Name))
		if label == "" {
			label = fmt.Sprintf("Monitor %d", i+1)
		}
		conv, skip := convertKumaMonitor(km, minInterval)
		if skip != "" {
			result.Skipped = append(result.Skipped, kumaImportNote{Name: label, Reason: string(skip)})
			continue
		}
		protocol, target := conv.fields["protocol"].(string), conv.fields["target"].(string)
		if protocol == monitor.ProtocolPush && systemID != "" {
			result.Skipped = append(result.Skipped, kumaImportNote{Name: label, Reason: "push monitors can only run on the hub"})
			continue
		}
		key := kumaMonitorKey(conv.name, target, protocol)
		if existing[key] {
			result.Skipped = append(result.Skipped, kumaImportNote{Name: label, Reason: "a monitor with the same name and target exists"})
			continue
		}

		record := core.NewRecord(collection)
		record.Load(conv.fields)
		record.Set("system", systemID)
		if user != nil && systemID == "" {
			record.Set("users", []string{user.Id})
		}
		if err := prepareMonitor(app, record, nil, user, superuser); err != nil {
			var inputErr monitorInputError
			if !errors.As(err, &inputErr) {
				return result, err
			}
			result.Skipped = append(result.Skipped, kumaImportNote{Name: label, Reason: inputErr.Error()})
			continue
		}
		if protocol == monitor.ProtocolPush {
			if token := conv.pushKuma; validPushToken(token) && !usedTokens[token] && !pushTokenInUse(app, token, "") {
				record.Set("pushToken", token)
				conv.notes = append(conv.notes, "push URL is now /api/beszel/push/<token> (same token)")
			} else {
				conv.notes = append(conv.notes, "new push token generated; update the push URL")
			}
			usedTokens[record.GetString("pushToken")] = true
		}
		if !dryRun {
			if err := app.Save(record); err != nil {
				result.Skipped = append(result.Skipped, kumaImportNote{Name: label, Reason: "failed to save: " + err.Error()})
				continue
			}
		}
		existing[key] = true
		for _, change := range conv.notes {
			result.Adjusted = append(result.Adjusted, kumaImportNote{Name: conv.name, Change: change})
		}
		result.Created++
		result.Monitors = append(result.Monitors, kumaImportedMonitor{Name: conv.name, Protocol: protocol, Target: target})
	}
	return result, nil
}

// ownedMonitorKeys returns the keys (kumaMonitorKey) of the monitors the
// requester owns: hub monitors listing the user, and monitors of systems the
// user belongs to. For superusers (user nil), the monitors of systemID.
func ownedMonitorKeys(app core.App, systemID string, user *core.Record) (map[string]bool, error) {
	var rows []struct {
		Name     string `db:"name"`
		Target   string `db:"target"`
		Protocol string `db:"protocol"`
	}
	query := app.DB().Select("network_monitors.name", "network_monitors.target", "network_monitors.protocol").From("network_monitors")
	if user == nil {
		query = query.Where(dbx.HashExp{"system": systemID})
	} else {
		query = query.
			LeftJoin("systems", dbx.NewExp("systems.id = network_monitors.system")).
			Where(dbx.Or(
				dbx.NewExp("network_monitors.system = '' AND EXISTS (SELECT 1 FROM json_each(network_monitors.users) WHERE value = {:user})", dbx.Params{"user": user.Id}),
				dbx.NewExp("network_monitors.system != '' AND EXISTS (SELECT 1 FROM json_each(systems.users) WHERE value = {:user})", dbx.Params{"user": user.Id}),
			))
	}
	if err := query.All(&rows); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(rows))
	for _, row := range rows {
		keys[kumaMonitorKey(row.Name, row.Target, row.Protocol)] = true
	}
	return keys, nil
}
