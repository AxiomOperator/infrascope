package netmon

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// Fixed failure messages of dns checks.
var (
	errDNSNoRecords        = errors.New("no records")
	errDNSExpectedNotFound = errors.New("expected value not found")
)

// monitorDNS measures DNS resolution response time in microseconds. If server is
// non-empty, the lookup is sent to that DNS server (host or host:port, default
// port 53) instead of the system resolver. Returns -1 and an error on failure.
func monitorDNS(ctx context.Context, target, server string, timeout time.Duration) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolver := net.DefaultResolver
	if server != "" {
		resolver = dnsResolverForServer(server)
	}

	start := time.Now()
	ips, err := resolver.LookupHost(ctx, target)
	if err != nil {
		return -1, err
	}
	if len(ips) == 0 {
		return -1, errors.New("no addresses resolved")
	}
	return time.Since(start).Microseconds(), nil
}

// probeDNS queries the configured record type and, when an expected value is
// set, checks that one of the records matches it. Without check options it
// resolves the target's addresses like monitorDNS.
func probeDNS(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil || (check.RecordType == "" && check.Expected == "") {
		return outcomeOf(monitorDNS(ctx, config.Target, config.Server, config.ProbeTimeout()))
	}
	ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
	defer cancel()
	resolver := net.DefaultResolver
	if config.Server != "" {
		resolver = dnsResolverForServer(config.Server)
	}
	start := time.Now()
	records, err := lookupDNSRecords(ctx, resolver, check.RecordType, config.Target)
	responseUs := time.Since(start).Microseconds()
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return outcomeOf(-1, errDNSNoRecords)
		}
		return outcomeOf(-1, err)
	}
	if len(records) == 0 {
		return outcomeOf(-1, errDNSNoRecords)
	}
	if check.Expected != "" && !matchDNSRecords(records, check.Expected, check.MatchMode, check.RecordType == "TXT") {
		return outcomeOf(-1, errDNSExpectedNotFound)
	}
	return Outcome{ResponseUs: responseUs}
}

// lookupDNSRecords returns the records of the given type as match
// candidates. Records with several parts, such as MX, yield both their full
// text ("10 mail.example.com") and their host name.
func lookupDNSRecords(ctx context.Context, resolver *net.Resolver, recordType, name string) ([]string, error) {
	switch recordType {
	case "":
		return resolver.LookupHost(ctx, name)
	case "A", "AAAA":
		network := "ip4"
		if recordType == "AAAA" {
			network = "ip6"
		}
		ips, err := resolver.LookupIP(ctx, network, name)
		if err != nil {
			return nil, err
		}
		records := make([]string, len(ips))
		for i, ip := range ips {
			records[i] = ip.String()
		}
		return records, nil
	case "CNAME":
		cname, err := resolver.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		// The resolver returns the name itself when it has no CNAME record.
		if cname = trimDot(cname); cname == "" || strings.EqualFold(cname, trimDot(name)) {
			return nil, nil
		}
		return []string{cname}, nil
	case "MX":
		mxs, err := resolver.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		var records []string
		for _, mx := range mxs {
			host := trimDot(mx.Host)
			records = append(records, host, strconv.Itoa(int(mx.Pref))+" "+host)
		}
		return records, nil
	case "TXT":
		return resolver.LookupTXT(ctx, name)
	case "NS":
		nss, err := resolver.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		records := make([]string, len(nss))
		for i, ns := range nss {
			records[i] = trimDot(ns.Host)
		}
		return records, nil
	case "SRV":
		_, srvs, err := resolver.LookupSRV(ctx, "", "", name)
		if err != nil {
			return nil, err
		}
		var records []string
		for _, srv := range srvs {
			target := trimDot(srv.Target)
			port := strconv.Itoa(int(srv.Port))
			records = append(records, target, net.JoinHostPort(target, port),
				strings.Join([]string{strconv.Itoa(int(srv.Priority)), strconv.Itoa(int(srv.Weight)), port, target}, " "))
		}
		return records, nil
	default:
		return nil, errors.New("unsupported record type")
	}
}

// matchDNSRecords reports whether a record contains (or, with MatchEquals,
// equals) expected. Names compare case-insensitively; TXT records compare
// exactly.
func matchDNSRecords(records []string, expected, mode string, caseSensitive bool) bool {
	if !caseSensitive {
		expected = strings.ToLower(trimDot(expected))
	}
	for _, record := range records {
		if !caseSensitive {
			record = strings.ToLower(record)
		}
		if mode == monitor.MatchEquals {
			if record == expected {
				return true
			}
		} else if strings.Contains(record, expected) {
			return true
		}
	}
	return false
}

func trimDot(name string) string {
	return strings.TrimSuffix(name, ".")
}

// dnsResolverForServer builds a resolver that sends lookups to the given DNS
// server address instead of the system resolver. server may be a bare host or
// host:port; when no port is given, the standard DNS port 53 is used.
func dnsResolverForServer(server string) *net.Resolver {
	address := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		address = net.JoinHostPort(server, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
}
