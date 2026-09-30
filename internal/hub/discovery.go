package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"net"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Docker label discovery: systems with autoDiscover get monitors declared by
// the labels of their running containers (see agent/docker_discovery.go and
// supplemental/guides/docker-label-monitors.md for the label scheme).
//
// Discovered monitors are ordinary network_monitors records marked with
// managedBy "docker", managedSystem and managedKey ("docker:<container>:<id>").
// The fields set by labels (managedFields) are read-only for users; other
// fields (notify, thresholds, dependencies, ...) stay editable unless a label
// sets them. A monitor whose container disappears is disabled and deleted
// after discoveryMissingGrace. Monitors without managedBy are never touched.
const (
	managedByDocker = "docker"
	// discoveryMissingGrace is how long a disabled monitor of a vanished
	// container is kept before it is deleted.
	discoveryMissingGrace = 24 * time.Hour
	// discoveryRefresh reconciles unchanged discovery payloads this often,
	// so monitors of vanished containers are deleted in time.
	discoveryRefresh = time.Hour
	// discoveryMaxErrors bounds systems.discoveryErrors.
	discoveryMaxErrors = 50
	// Defaults of discovered monitors without the corresponding label.
	discoveryDefaultInterval = 60
	discoveryDefaultRetries  = 1
)

// discoveryLabelFields maps label fields to the managedFields entries they control.
var discoveryLabelFields = map[string]string{
	"type": "protocol", "name": "name", "target": "target", "port": "port",
	"interval": "interval", "retries": "retries", "timeout": "timeout",
	"keyword": "keyword", "accepted_codes": "acceptedCodes", "location": "locations", "notify": "notify",
}

// discoveryKeyRe matches the monitor ids of infrascope.monitor.<id>.<field> labels.
var discoveryKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// discoveryError is an entry of systems.discoveryErrors.
type discoveryError struct {
	Container string `json:"container"`
	Key       string `json:"key"`
	Error     string `json:"error"`
}

// dockerDiscovery rate limits reconciliation: a system is reconciled when
// its discovery payload or settings change, and at least every discoveryRefresh.
type dockerDiscovery struct {
	mu     sync.Mutex
	states map[string]*discoveryState
	now    func() time.Time
}

type discoveryState struct {
	hash    uint64
	at      time.Time
	running bool
	// gen changes when the state is invalidated, so a running reconcile does
	// not store its (stale) hash.
	gen uint64
}

func newDockerDiscovery() *dockerDiscovery {
	return &dockerDiscovery{states: map[string]*discoveryState{}, now: time.Now}
}

// invalidate makes the next payload of a system reconcile.
func (d *dockerDiscovery) invalidate(systemID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state, ok := d.states[systemID]; ok {
		state.hash = 0
		state.gen++
	}
}

// begin reports whether a system with the given payload hash needs a
// reconcile and, if so, marks it running.
func (d *dockerDiscovery) begin(systemID string, hash uint64, now time.Time) (uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.states[systemID]
	if state == nil {
		state = &discoveryState{}
		d.states[systemID] = state
	}
	if state.running || (state.hash == hash && now.Sub(state.at) < discoveryRefresh) {
		return 0, false
	}
	state.running = true
	return state.gen, true
}

// end records a finished reconcile; failed runs are retried with the next payload.
func (d *dockerDiscovery) end(systemID string, hash, gen uint64, now time.Time, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.states[systemID]
	if state == nil {
		return
	}
	state.running = false
	if ok && state.gen == gen {
		state.hash, state.at = hash, now
	} else {
		state.hash = 0
	}
}

// discoveryHash hashes a discovery payload with the system settings that
// shape the monitors.
func discoveryHash(systemRecord *core.Record, discovery *system.Discovery) uint64 {
	h := fnv.New64a()
	payload, _ := json.Marshal(discovery)
	_, _ = h.Write(payload)
	settings, _ := json.Marshal([]any{
		systemRecord.GetBool("autoDiscoverTraefik"), systemRecord.GetString("host"), systemRecord.GetStringSlice("users"),
	})
	_, _ = h.Write(settings)
	return max(h.Sum64(), 1)
}

// ReconcileDockerDiscovery reconciles the monitors a system's container
// labels declare in the background, when the payload or settings changed.
func (h *Hub) ReconcileDockerDiscovery(systemRecord *core.Record, discovery *system.Discovery) {
	if h.discovery == nil || discovery == nil || !systemRecord.GetBool("autoDiscover") {
		return
	}
	now := h.discovery.now()
	hash := discoveryHash(systemRecord, discovery)
	gen, ok := h.discovery.begin(systemRecord.Id, hash, now)
	if !ok {
		return
	}
	go func() {
		err := h.reconcileDockerDiscovery(systemRecord.Id, discovery, now)
		if err != nil {
			h.Logger().Error("Docker discovery failed", "system", systemRecord.Id, "err", err)
		}
		h.discovery.end(systemRecord.Id, hash, gen, now, err == nil)
	}()
}

// discoveredSpec is the desired state of a discovered monitor.
type discoveredSpec struct {
	key     string
	fields  map[string]any // record fields set on create and update
	defs    map[string]any // defaults only set on create
	http    map[string]any // http options set by labels
	managed []string       // managedFields
}

// managedMonitorKey returns the managedKey of a discovered monitor.
func managedMonitorKey(dm system.DiscoveredMonitor) string {
	return "docker:" + dm.Container + ":" + dm.Key
}

// parseLabelSeconds parses a label value in seconds, as a number or a Go duration.
func parseLabelSeconds(field, value string) (int, error) {
	value = strings.TrimSpace(value)
	if n, err := strconv.Atoi(value); err == nil && n >= 0 {
		return n, nil
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 && d%time.Second == 0 {
		return int(d / time.Second), nil
	}
	return 0, fmt.Errorf("invalid %s %q: use seconds or a duration like 1m", field, value)
}

// buildDiscoveredSpec converts the labels of a discovered monitor into
// record fields. systemRecord is the system whose container declares it.
func buildDiscoveredSpec(systemRecord *core.Record, dm system.DiscoveredMonitor) (discoveredSpec, error) {
	spec := discoveredSpec{key: managedMonitorKey(dm), fields: map[string]any{}, defs: map[string]any{}, http: map[string]any{}}
	labels := dm.Labels
	if !dm.Traefik && !discoveryKeyRe.MatchString(dm.Key) {
		return spec, fmt.Errorf("invalid monitor id %q: use letters, digits, - and _", dm.Key)
	}
	for _, field := range slices.Sorted(maps.Keys(labels)) {
		if _, ok := discoveryLabelFields[field]; !ok {
			return spec, fmt.Errorf("unknown label field %q", field)
		}
	}

	protocol := strings.ToLower(strings.TrimSpace(labels["type"]))
	target := strings.TrimSpace(labels["target"])
	if protocol == "" {
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			protocol = monitor.ProtocolHTTP
		} else {
			return spec, errors.New("type is required")
		}
	}
	if !slices.Contains(monitor.Protocols, protocol) {
		return spec, fmt.Errorf("unknown type %q", protocol)
	}
	if protocol == monitor.ProtocolPush {
		return spec, errors.New("push monitors cannot be discovered")
	}

	location := strings.ToLower(strings.TrimSpace(labels["location"]))
	var locations []string
	switch location {
	case "", "agent":
		locations = []string{systemRecord.Id}
	case monitorloc.Hub:
		locations = []string{monitorloc.Hub}
		// Hub monitors belong to the system's users.
		spec.fields["users"] = systemRecord.GetStringSlice("users")
	default:
		return spec, fmt.Errorf("invalid location %q: use agent or hub", location)
	}
	spec.fields["locations"] = locations

	// The host of targets derived from published ports.
	host := "localhost"
	if location == monitorloc.Hub {
		host = systemRecord.GetString("host")
		if host == "" || strings.ContainsAny(host, "/ ") {
			return spec, errors.New("target is required: the system has no usable host")
		}
	}

	var port int
	if raw, ok := labels["port"]; ok {
		port = int(dm.Port)
		if port == 0 {
			n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
			if err != nil || n == 0 {
				return spec, fmt.Errorf("invalid port %q", raw)
			}
			port = int(n)
		}
	} else if target == "" {
		port = int(dm.Port)
	}

	if target == "" {
		switch {
		case protocol == monitor.ProtocolDocker:
			target = dm.Container
		case protocol == monitor.ProtocolHTTP:
			if port == 0 {
				return spec, errors.New("target is required: the container publishes no TCP port")
			}
			target = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
		case monitor.UsesPort(protocol), protocol == monitor.ProtocolICMP:
			target = host
		default:
			return spec, errors.New("target is required")
		}
	}
	spec.fields["protocol"] = protocol
	spec.fields["target"] = target
	spec.managed = []string{"protocol", "target", "locations"}
	if monitor.UsesPort(protocol) {
		if port == 0 {
			port = int(monitor.DefaultPort(protocol, nil))
		}
		spec.fields["port"] = port
		spec.managed = append(spec.managed, "port")
	}

	name := strings.TrimSpace(labels["name"])
	if name != "" {
		spec.fields["name"] = name
		spec.managed = append(spec.managed, "name")
	} else {
		switch {
		case dm.Traefik:
			spec.defs["name"] = strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://")
		case dm.Key == "default":
			spec.defs["name"] = dm.Container
		default:
			spec.defs["name"] = dm.Container + "/" + dm.Key
		}
	}

	spec.defs["interval"] = discoveryDefaultInterval
	spec.defs["retries"] = discoveryDefaultRetries
	spec.defs["notify"] = true
	for _, field := range []string{"interval", "timeout"} {
		if raw, ok := labels[field]; ok {
			seconds, err := parseLabelSeconds(field, raw)
			if err != nil {
				return spec, err
			}
			spec.fields[field] = seconds
			spec.managed = append(spec.managed, field)
		}
	}
	if raw, ok := labels["retries"]; ok {
		retries, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || retries < 0 || retries > 10 {
			return spec, fmt.Errorf("invalid retries %q: use 0 to 10", raw)
		}
		spec.fields["retries"] = retries
		spec.managed = append(spec.managed, "retries")
	}
	if raw, ok := labels["notify"]; ok {
		notify, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return spec, fmt.Errorf("invalid notify %q: use true or false", raw)
		}
		spec.fields["notify"] = notify
		spec.managed = append(spec.managed, "notify")
	}
	for label, option := range map[string]string{"keyword": "keyword", "accepted_codes": "acceptedCodes"} {
		raw, ok := labels[label]
		if !ok {
			continue
		}
		if protocol != monitor.ProtocolHTTP {
			return spec, fmt.Errorf("%s only applies to http monitors", label)
		}
		if option == "acceptedCodes" {
			var codes []string
			for code := range strings.SplitSeq(raw, ",") {
				if code = strings.TrimSpace(code); code != "" {
					codes = append(codes, code)
				}
			}
			spec.http[option] = codes
		} else {
			spec.http[option] = raw
		}
		spec.managed = append(spec.managed, option)
	}
	slices.Sort(spec.managed)
	return spec, nil
}

// reconcileDockerDiscovery creates, updates, disables and deletes the
// managed monitors of a system to match its discovery payload.
func (h *Hub) reconcileDockerDiscovery(systemID string, discovery *system.Discovery, now time.Time) error {
	app := h.App
	systemRecord, err := app.FindRecordById("systems", systemID)
	if err != nil {
		return err
	}
	if !systemRecord.GetBool("autoDiscover") {
		return nil
	}
	traefik := systemRecord.GetBool("autoDiscoverTraefik")
	existing, err := app.FindAllRecords("network_monitors", dbx.HashExp{"managedSystem": systemID, "managedBy": managedByDocker})
	if err != nil {
		return err
	}
	byKey := make(map[string]*core.Record, len(existing))
	for _, record := range existing {
		byKey[record.GetString("managedKey")] = record
	}

	logger := app.Logger().With("system", systemID)
	var problems []discoveryError
	seen := map[string]bool{}
	var firstErr error
	for _, dm := range discovery.Monitors {
		if dm.Traefik && !traefik {
			continue
		}
		key := managedMonitorKey(dm)
		if seen[key] {
			continue
		}
		seen[key] = true
		spec, err := buildDiscoveredSpec(systemRecord, dm)
		if err != nil {
			err = monitorInputError(err.Error())
		} else {
			err = h.applyDiscoveredMonitor(systemRecord, byKey[key], spec)
		}
		if err == nil {
			continue
		}
		var inputErr monitorInputError
		if !errors.As(err, &inputErr) && !isValidationError(err) {
			logger.Error("Docker discovery: failed to save monitor", "key", key, "err", err)
			firstErr = cmpErr(firstErr, err)
		}
		if len(problems) < discoveryMaxErrors {
			problems = append(problems, discoveryError{Container: dm.Container, Key: dm.Key, Error: err.Error()})
		}
	}

	// Monitors of vanished containers are disabled, then deleted after the grace period.
	for key, record := range byKey {
		if seen[key] {
			continue
		}
		missingSince := record.GetDateTime("managedMissingSince")
		if !missingSince.IsZero() && now.Sub(missingSince.Time()) >= discoveryMissingGrace {
			if err := app.Delete(record); err != nil {
				firstErr = cmpErr(firstErr, err)
				continue
			}
			logger.Info("Docker discovery: deleted monitor of removed container", "monitor", record.Id, "key", key)
			continue
		}
		if !missingSince.IsZero() && !record.GetBool("enabled") {
			continue
		}
		original := record.Original()
		if missingSince.IsZero() {
			since, _ := types.ParseDateTime(now)
			record.Set("managedMissingSince", since)
		}
		record.Set("enabled", false)
		if err := app.Save(record); err != nil {
			firstErr = cmpErr(firstErr, err)
			continue
		}
		h.syncUpdatedMonitor(original, record)
		logger.Info("Docker discovery: disabled monitor of removed container", "monitor", record.Id, "key", key)
	}

	if err := h.setDiscoveryErrors(systemRecord, problems); err != nil {
		firstErr = cmpErr(firstErr, err)
	}
	return firstErr
}

// cmpErr keeps the first error.
func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// isValidationError reports whether err is a record field validation error.
func isValidationError(err error) bool {
	// ozzo-validation's Errors, returned by Save for invalid field values.
	var validationErrors interface {
		error
		Filter() error
	}
	return errors.As(err, &validationErrors)
}

// setDiscoveryErrors stores the label errors of a system when they changed.
func (h *Hub) setDiscoveryErrors(systemRecord *core.Record, problems []discoveryError) error {
	var current []discoveryError
	_ = json.Unmarshal([]byte(systemRecord.GetString("discoveryErrors")), &current)
	if len(current) == 0 && len(problems) == 0 || reflect.DeepEqual(current, problems) {
		return nil
	}
	fresh, err := h.FindRecordById("systems", systemRecord.Id)
	if err != nil {
		return err
	}
	if len(problems) == 0 {
		fresh.Set("discoveryErrors", nil)
	} else {
		fresh.Set("discoveryErrors", problems)
	}
	return h.SaveNoValidate(fresh)
}

// applyDiscoveredMonitor creates the monitor of spec, or updates record to
// it. Invalid label sets are reported as monitorInputError.
func (h *Hub) applyDiscoveredMonitor(systemRecord, record *core.Record, spec discoveredSpec) error {
	app := h.App
	isNew := record == nil
	if isNew {
		collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Load(spec.defs)
		record.Set("enabled", true)
	}
	original := record.Original()
	before := discoverySnapshot(record)

	for field, value := range spec.fields {
		record.Set(field, value)
	}
	if !slices.Contains(spec.managed, "name") && record.GetString("name") == "" {
		record.Set("name", spec.defs["name"])
	}
	if record.GetString("protocol") == monitor.ProtocolHTTP && len(spec.http) > 0 {
		options := map[string]any{}
		_ = json.Unmarshal([]byte(record.GetString("http")), &options)
		if options == nil {
			options = map[string]any{}
		}
		maps.Copy(options, spec.http)
		record.Set("http", options)
	}
	if name := uniqueMonitorName(app, systemRecord.Id, record.Id, record.GetString("name"), strings.SplitN(spec.key, ":", 3)[1]); name != record.GetString("name") {
		record.Set("name", name)
	}
	// A container that is back re-enables the monitor disabled for its absence.
	reappeared := !isNew && !record.GetDateTime("managedMissingSince").IsZero()
	if reappeared {
		record.Set("enabled", true)
	}
	if !isNew && !reappeared && discoverySnapshot(record) == before &&
		slices.Equal(record.GetStringSlice("managedFields"), spec.managed) {
		return nil
	}

	var originalArg *core.Record
	if !isNew {
		originalArg = original
	}
	if err := prepareMonitor(app, record, originalArg, nil, true); err != nil {
		return err
	}
	record.Set("managedBy", managedByDocker)
	record.Set("managedKey", spec.key)
	record.Set("managedSystem", systemRecord.Id)
	record.Set("managedFields", spec.managed)
	record.Set("managedMissingSince", nil)
	if err := app.Save(record); err != nil {
		return err
	}
	logger := app.Logger().With("system", systemRecord.Id, "monitor", record.Id, "key", spec.key)
	if isNew {
		logger.Info("Docker discovery: created monitor")
		return nil
	}
	h.syncUpdatedMonitor(original, record)
	logger.Info("Docker discovery: updated monitor")
	return nil
}

// discoverySnapshot returns the label-controllable fields of a monitor, to
// detect changes.
func discoverySnapshot(record *core.Record) string {
	values := map[string]any{}
	for _, field := range []string{"name", "protocol", "target", "port", "interval", "retries", "timeout", "notify", "users", "enabled"} {
		values[field] = record.Get(field)
	}
	values["locations"] = monitorloc.Of(record)
	var options any
	_ = json.Unmarshal([]byte(record.GetString("http")), &options)
	values["http"] = options
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

// uniqueMonitorName returns name, or name with the container and then a
// number appended when another monitor of the system already uses it.
func uniqueMonitorName(app core.App, systemID, monitorID, name, container string) string {
	taken := func(candidate string) bool {
		var count int
		err := app.DB().Select("count(*)").From("network_monitors").
			Where(dbx.HashExp{"name": candidate}).
			AndWhere(dbx.Or(dbx.HashExp{"system": systemID}, dbx.HashExp{"managedSystem": systemID})).
			AndWhere(dbx.Not(dbx.HashExp{"id": monitorID})).Row(&count)
		return err == nil && count > 0
	}
	if !taken(name) {
		return name
	}
	base := name
	if container != "" && !strings.Contains(name, container) {
		base = name + " (" + container + ")"
		if !taken(base) {
			return base
		}
	}
	for i := 2; i < 100; i++ {
		if candidate := fmt.Sprintf("%s %d", base, i); !taken(candidate) {
			return candidate
		}
	}
	return base
}

// managedFieldColumns maps managedFields entries to the record fields they protect.
var managedFieldColumns = map[string][]string{
	"locations": {"locations", "system", "locationSystems", "users"},
}

// keepManagedFields restores the label-controlled fields of a managed
// monitor submitted through the API to their stored values.
func keepManagedFields(original, record *core.Record) {
	if original == nil || original.GetString("managedBy") == "" {
		return
	}
	var httpOptions []string
	for _, field := range original.GetStringSlice("managedFields") {
		switch field {
		case "keyword", "acceptedCodes":
			httpOptions = append(httpOptions, field)
			continue
		}
		columns, ok := managedFieldColumns[field]
		if !ok {
			columns = []string{field}
		}
		for _, column := range columns {
			record.Set(column, original.Get(column))
		}
	}
	if len(httpOptions) > 0 {
		submitted, stored := map[string]any{}, map[string]any{}
		_ = json.Unmarshal([]byte(record.GetString("http")), &submitted)
		_ = json.Unmarshal([]byte(original.GetString("http")), &stored)
		if submitted == nil {
			submitted = map[string]any{}
		}
		for _, option := range httpOptions {
			if value, ok := stored[option]; ok {
				submitted[option] = value
			} else {
				delete(submitted, option)
			}
		}
		record.Set("http", submitted)
	}
}

// bindDockerDiscoveryEvents protects the server-managed discovery fields.
func bindDockerDiscoveryEvents(h *Hub) {
	// Before prepareMonitorRecord, so the restored values are validated and synced.
	h.OnRecordUpdateRequest("network_monitors").Bind(&hook.Handler[*core.RecordRequestEvent]{
		Priority: -1,
		Func: func(e *core.RecordRequestEvent) error {
			keepManagedFields(e.Record.Original(), e.Record)
			return e.Next()
		},
	})
	// A deleted managed monitor is recreated with the next payload while its labels exist.
	h.OnRecordAfterDeleteSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		if h.discovery != nil && e.Record.GetString("managedBy") != "" {
			h.discovery.invalidate(e.Record.GetString("managedSystem"))
		}
		return e.Next()
	})
	// Discovery errors are written by the hub only; settings changes apply
	// with the next payload.
	h.OnRecordCreateRequest("systems").BindFunc(func(e *core.RecordRequestEvent) error {
		e.Record.Set("discoveryErrors", nil)
		return e.Next()
	})
	h.OnRecordUpdateRequest("systems").BindFunc(func(e *core.RecordRequestEvent) error {
		original := e.Record.Original()
		e.Record.Set("discoveryErrors", original.Get("discoveryErrors"))
		if h.discovery != nil && (e.Record.GetBool("autoDiscover") != original.GetBool("autoDiscover") ||
			e.Record.GetBool("autoDiscoverTraefik") != original.GetBool("autoDiscoverTraefik")) {
			h.discovery.invalidate(e.Record.Id)
		}
		if !e.Record.GetBool("autoDiscover") {
			e.Record.Set("discoveryErrors", nil)
		}
		return e.Next()
	})
}
