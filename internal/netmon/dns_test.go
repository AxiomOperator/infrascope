package netmon

import (
	"context"
	"net"
	"testing"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsRecordsTestServer answers queries with the given resources, matched by
// name and type. Names without records get NXDOMAIN.
func dnsRecordsTestServer(t *testing.T, records []dnsmessage.Resource) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			msg.Header.Response = true
			msg.Header.RecursionAvailable = true
			known := false
			for _, question := range msg.Questions {
				for _, record := range records {
					if record.Header.Name != question.Name {
						continue
					}
					known = true
					if record.Header.Type == question.Type {
						msg.Answers = append(msg.Answers, record)
					}
				}
			}
			if !known {
				msg.Header.RCode = dnsmessage.RCodeNameError
			}
			packet, err := msg.Pack()
			if err != nil {
				continue
			}
			_, _ = conn.WriteToUDP(packet, addr)
		}
	}()
	return conn.LocalAddr().String()
}

func dnsResource(name string, body dnsmessage.ResourceBody) dnsmessage.Resource {
	var recordType dnsmessage.Type
	switch body.(type) {
	case *dnsmessage.AResource:
		recordType = dnsmessage.TypeA
	case *dnsmessage.AAAAResource:
		recordType = dnsmessage.TypeAAAA
	case *dnsmessage.MXResource:
		recordType = dnsmessage.TypeMX
	case *dnsmessage.TXTResource:
		recordType = dnsmessage.TypeTXT
	case *dnsmessage.NSResource:
		recordType = dnsmessage.TypeNS
	case *dnsmessage.CNAMEResource:
		recordType = dnsmessage.TypeCNAME
	case *dnsmessage.SRVResource:
		recordType = dnsmessage.TypeSRV
	}
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: recordType, Class: dnsmessage.ClassINET, TTL: 60},
		Body:   body,
	}
}

func TestProbeDNSRecordTypes(t *testing.T) {
	server := dnsRecordsTestServer(t, []dnsmessage.Resource{
		dnsResource("example.test.", &dnsmessage.AResource{A: [4]byte{192, 0, 2, 10}}),
		dnsResource("example.test.", &dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP("2001:db8::1"))}),
		dnsResource("example.test.", &dnsmessage.MXResource{Pref: 10, MX: dnsmessage.MustNewName("mail.example.test.")}),
		dnsResource("example.test.", &dnsmessage.TXTResource{TXT: []string{"v=spf1 -all"}}),
		dnsResource("example.test.", &dnsmessage.NSResource{NS: dnsmessage.MustNewName("ns1.example.test.")}),
		dnsResource("www.example.test.", &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("example.test.")}),
		dnsResource("_sip._tcp.example.test.", &dnsmessage.SRVResource{Priority: 1, Weight: 5, Port: 5060, Target: dnsmessage.MustNewName("sip.example.test.")}),
	})
	probe := func(target string, check monitor.CheckOptions) Outcome {
		return probeDNS(context.Background(), monitor.Config{Protocol: "dns", Target: target, Server: server, Check: &check})
	}
	for _, tc := range []struct {
		name   string
		target string
		check  monitor.CheckOptions
		err    string
	}{
		{"A", "example.test.", monitor.CheckOptions{RecordType: "A", Expected: "192.0.2.10", MatchMode: monitor.MatchEquals}, ""},
		{"A mismatch", "example.test.", monitor.CheckOptions{RecordType: "A", Expected: "192.0.2.11", MatchMode: monitor.MatchEquals}, "expected value not found"},
		{"AAAA", "example.test.", monitor.CheckOptions{RecordType: "AAAA", Expected: "2001:db8::1"}, ""},
		{"addresses contain", "example.test.", monitor.CheckOptions{Expected: "192.0.2."}, ""},
		{"MX host", "example.test.", monitor.CheckOptions{RecordType: "MX", Expected: "MAIL.example.test.", MatchMode: monitor.MatchEquals}, ""},
		{"MX with preference", "example.test.", monitor.CheckOptions{RecordType: "MX", Expected: "10 mail.example.test", MatchMode: monitor.MatchEquals}, ""},
		{"TXT", "example.test.", monitor.CheckOptions{RecordType: "TXT", Expected: "spf1"}, ""},
		{"TXT is case sensitive", "example.test.", monitor.CheckOptions{RecordType: "TXT", Expected: "SPF1"}, "expected value not found"},
		{"NS", "example.test.", monitor.CheckOptions{RecordType: "NS", Expected: "ns1.example.test", MatchMode: monitor.MatchEquals}, ""},
		{"CNAME", "www.example.test.", monitor.CheckOptions{RecordType: "CNAME", Expected: "example.test", MatchMode: monitor.MatchEquals}, ""},
		{"no CNAME", "example.test.", monitor.CheckOptions{RecordType: "CNAME"}, "no records"},
		{"SRV", "_sip._tcp.example.test.", monitor.CheckOptions{RecordType: "SRV", Expected: "sip.example.test:5060", MatchMode: monitor.MatchEquals}, ""},
		{"record type only", "example.test.", monitor.CheckOptions{RecordType: "MX"}, ""},
		{"missing name", "missing.example.test.", monitor.CheckOptions{RecordType: "TXT"}, "no records"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := probe(tc.target, tc.check)
			if tc.err == "" {
				require.NoError(t, out.Err)
				assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
			} else {
				require.Error(t, out.Err)
				assert.Equal(t, tc.err, out.Err.Error())
				assert.EqualValues(t, -1, out.ResponseUs)
			}
		})
	}

	t.Run("without options resolves addresses", func(t *testing.T) {
		out := probeDNS(context.Background(), monitor.Config{Protocol: "dns", Target: "example.test.", Server: server})
		require.NoError(t, out.Err)
	})

	t.Run("timeout", func(t *testing.T) {
		silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer silent.Close()
		out := probeDNS(context.Background(), monitor.Config{
			Protocol: "dns", Target: "example.test.", Server: silent.LocalAddr().String(), Timeout: 1,
			Check: &monitor.CheckOptions{RecordType: "TXT"},
		})
		require.Error(t, out.Err)
	})
}
