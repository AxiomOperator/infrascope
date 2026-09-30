package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"
)

// Monitor discovery from container labels. The agent only groups the labels;
// the hub validates them and creates the monitors (see docs in
// supplemental/guides/docker-label-monitors.md).
//
//	infrascope.monitor.<field>=<value>       fields of the container's default monitor
//	infrascope.monitor.<id>.<field>=<value>  fields of monitor <id>
//	infrascope.monitor.enable=false          no monitors for the container
//	infrascope.monitor.<id>.enable=false     skip monitor <id>
//	infrascope.monitor.traefik=false         no monitors from Traefik routers
const (
	discoveryLabelPrefix = "infrascope.monitor."
	// discoveryDefaultKey is the id of the monitor declared without an id.
	discoveryDefaultKey = "default"
	// Limits that keep the discovery payload small.
	discoveryMaxPerContainer = 32
	discoveryMaxValueLen     = 1024
	discoveryMaxFieldLen     = 64
)

var (
	// traefikHostRe matches Host(...) matchers of a Traefik router rule,
	// but not HostSNI or HostRegexp.
	traefikHostRe = regexp.MustCompile(`\bHost\(([^)]*)\)`)
	// traefikPathRe matches the first Path or PathPrefix matcher.
	traefikPathRe = regexp.MustCompile("\\bPath(?:Prefix)?\\(\\s*[`\"']([^`\"']+)[`\"']")
)

// labelDisabled reports whether a label value turns something off.
func labelDisabled(value string) bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && !enabled
}

// discoverContainerMonitors returns the monitors declared by the labels of a
// running container, sorted by key.
func discoverContainerMonitors(name string, labels map[string]string, ports []container.ApiPort) []system.DiscoveredMonitor {
	if len(labels) == 0 || labelDisabled(labels[discoveryLabelPrefix+"enable"]) {
		return nil
	}
	groups := map[string]map[string]string{}
	for label, value := range labels {
		rest, ok := strings.CutPrefix(label, discoveryLabelPrefix)
		if !ok || rest == "enable" || rest == "traefik" || rest == "" {
			continue
		}
		key, field := discoveryDefaultKey, rest
		if id, f, found := strings.Cut(rest, "."); found {
			key, field = id, f
		}
		if key == "" || field == "" || len(field) > discoveryMaxFieldLen || len(key) > discoveryMaxFieldLen {
			continue
		}
		if len(value) > discoveryMaxValueLen {
			value = value[:discoveryMaxValueLen]
		}
		if groups[key] == nil {
			groups[key] = map[string]string{}
		}
		groups[key][field] = value
	}

	var monitors []system.DiscoveredMonitor
	for key, fields := range groups {
		if labelDisabled(fields["enable"]) {
			continue
		}
		delete(fields, "enable")
		monitors = append(monitors, system.DiscoveredMonitor{
			Container: name,
			Key:       key,
			Labels:    fields,
			Port:      discoveryPort(fields["port"], ports),
		})
	}
	if !labelDisabled(labels[discoveryLabelPrefix+"traefik"]) {
		monitors = append(monitors, traefikMonitors(name, labels)...)
	}
	slices.SortFunc(monitors, func(a, b system.DiscoveredMonitor) int { return strings.Compare(a.Key, b.Key) })
	if len(monitors) > discoveryMaxPerContainer {
		monitors = monitors[:discoveryMaxPerContainer]
	}
	return monitors
}

// discoveryPort returns the host port to probe: the port label translated
// through the published TCP ports (a container port published to the host
// becomes its host port), or the lowest published TCP port without a label.
func discoveryPort(label string, ports []container.ApiPort) uint16 {
	label = strings.TrimSpace(label)
	var lowest uint16
	for _, p := range ports {
		if p.PublicPort == 0 || (p.Type != "" && p.Type != "tcp") {
			continue
		}
		if label != "" && strconv.Itoa(int(p.PrivatePort)) == label {
			return p.PublicPort
		}
		if lowest == 0 || p.PublicPort < lowest {
			lowest = p.PublicPort
		}
	}
	if label != "" {
		if port, err := strconv.ParseUint(label, 10, 16); err == nil {
			return uint16(port)
		}
		return 0
	}
	return lowest
}

// traefikMonitors returns an http monitor for each host of the container's
// Traefik HTTP routers. https is used when the router has TLS or listens on
// a TLS-looking entrypoint.
func traefikMonitors(name string, labels map[string]string) []system.DiscoveredMonitor {
	const routerPrefix = "traefik.http.routers."
	var monitors []system.DiscoveredMonitor
	for label, rule := range labels {
		router, ok := strings.CutPrefix(label, routerPrefix)
		if !ok {
			continue
		}
		router, ok = strings.CutSuffix(router, ".rule")
		if !ok || router == "" || strings.Contains(router, ".") {
			continue
		}
		scheme := "http"
		if traefikRouterTLS(labels, routerPrefix+router) {
			scheme = "https"
		}
		path := ""
		if m := traefikPathRe.FindStringSubmatch(rule); m != nil && strings.HasPrefix(m[1], "/") {
			path = m[1]
		}
		for _, host := range traefikRuleHosts(rule) {
			monitors = append(monitors, system.DiscoveredMonitor{
				Container: name,
				Key:       "traefik." + router + "." + host,
				// No name label: users may rename these monitors.
				Labels: map[string]string{
					"type":   "http",
					"target": scheme + "://" + host + path,
				},
				Traefik: true,
			})
		}
	}
	return monitors
}

// traefikRouterTLS reports whether a router serves TLS.
func traefikRouterTLS(labels map[string]string, router string) bool {
	if tls, ok := labels[router+".tls"]; ok {
		if enabled, err := strconv.ParseBool(strings.TrimSpace(tls)); err == nil {
			return enabled
		}
	}
	for label := range labels {
		if strings.HasPrefix(label, router+".tls.") {
			return true
		}
	}
	for entrypoint := range strings.SplitSeq(strings.ToLower(labels[router+".entrypoints"]), ",") {
		switch strings.TrimSpace(entrypoint) {
		case "websecure", "https", "web-secure", "secure", "443":
			return true
		}
	}
	return false
}

// traefikRuleHosts returns the hosts of the Host matchers of a router rule,
// deduplicated in order: Host(`a`) || Host(`b`) and Host(`a`, `b`) both give a, b.
func traefikRuleHosts(rule string) []string {
	var hosts []string
	for _, match := range traefikHostRe.FindAllStringSubmatch(rule, -1) {
		for host := range strings.SplitSeq(match[1], ",") {
			host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "`\"'"))
			if host == "" || strings.ContainsAny(host, " /*{}") || slices.Contains(hosts, host) {
				continue
			}
			hosts = append(hosts, host)
		}
	}
	return hosts
}
