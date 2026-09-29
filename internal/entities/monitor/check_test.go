package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckOptionsCBORCompatibility(t *testing.T) {
	config := Config{
		ID: "m1", Target: "db.local", Protocol: ProtocolRedis, Port: 6379, Interval: 30,
		Check: &CheckOptions{TLS: true, IgnoreTLS: true, Username: "u", Password: "p"},
	}
	data, err := cbor.Marshal(config)
	require.NoError(t, err)

	var decoded Config
	require.NoError(t, cbor.Unmarshal(data, &decoded))
	assert.True(t, config.Equal(decoded))

	var old legacyConfig
	require.NoError(t, cbor.Unmarshal(data, &old), "old agents ignore the check options")
	assert.Equal(t, legacyConfig{ID: "m1", Target: "db.local", Protocol: ProtocolRedis, Port: 6379, Interval: 30}, old)

	legacy, err := cbor.Marshal(config.Legacy())
	require.NoError(t, err)
	oldData, err := cbor.Marshal(old)
	require.NoError(t, err)
	assert.Equal(t, oldData, legacy, "Legacy strips the check options")
	assert.NotNil(t, config.Check, "Legacy must not modify the original")

	withoutCheck, err := cbor.Marshal(Config{ID: "m1", Target: "1.1.1.1", Protocol: "icmp", Interval: 10})
	require.NoError(t, err)
	oldIcmp, err := cbor.Marshal(legacyConfig{ID: "m1", Target: "1.1.1.1", Protocol: "icmp", Interval: 10})
	require.NoError(t, err)
	assert.Equal(t, oldIcmp, withoutCheck, "configs without check options encode unchanged")
}

func TestCheckOptionsForProtocol(t *testing.T) {
	all := &CheckOptions{
		RecordType: "mx", Expected: "mail", MatchMode: MatchEquals, Banner: "b", TLS: true,
		IgnoreTLS: true, Username: "u", Password: "p", Service: "s",
	}
	for _, tc := range []struct {
		protocol string
		want     *CheckOptions
	}{
		{ProtocolDNS, &CheckOptions{RecordType: "MX", Expected: "mail", MatchMode: MatchEquals}},
		{ProtocolTCP, &CheckOptions{Banner: "b", TLS: true, IgnoreTLS: true}},
		{ProtocolSSH, &CheckOptions{Banner: "b"}},
		{ProtocolPostgres, &CheckOptions{TLS: true, IgnoreTLS: true, Username: "u", Password: "p"}},
		{ProtocolRedis, &CheckOptions{TLS: true, IgnoreTLS: true, Username: "u", Password: "p"}},
		{ProtocolSMTP, &CheckOptions{TLS: true, IgnoreTLS: true}},
		{ProtocolGRPC, &CheckOptions{TLS: true, IgnoreTLS: true, Service: "s"}},
		{ProtocolMySQL, nil},
		{ProtocolDocker, nil},
		{ProtocolHTTP, nil},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			assert.Equal(t, tc.want, all.ForProtocol(tc.protocol))
		})
	}
	assert.Nil(t, (&CheckOptions{IgnoreTLS: true}).ForProtocol(ProtocolTCP), "ignoreTLS needs TLS")
	assert.Equal(t, &CheckOptions{Expected: "1.1.1.1"},
		(&CheckOptions{Expected: "1.1.1.1", MatchMode: MatchContains}).ForProtocol(ProtocolDNS), "contains is the default")
	assert.Nil(t, (&CheckOptions{MatchMode: MatchEquals}).ForProtocol(ProtocolDNS), "match mode needs a value")
	var nilOptions *CheckOptions
	assert.Nil(t, nilOptions.ForProtocol(ProtocolDNS))
}

func TestConfigValidateChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		err    string
	}{
		{"dns record type", Config{Protocol: ProtocolDNS, Target: "example.com", Check: &CheckOptions{RecordType: "TXT", Expected: "v=spf1"}}, ""},
		{"dns bad record type", Config{Protocol: ProtocolDNS, Target: "example.com", Check: &CheckOptions{RecordType: "PTR"}}, "record type"},
		{"dns bad match mode", Config{Protocol: ProtocolDNS, Target: "example.com", Check: &CheckOptions{Expected: "x", MatchMode: "regex"}}, "match mode"},
		{"dns url target", Config{Protocol: ProtocolDNS, Target: "https://example.com"}, "host name"},
		{"tcp banner", Config{Protocol: ProtocolTCP, Target: "h", Port: 21, Check: &CheckOptions{Banner: "220"}}, ""},
		{"tcp without port", Config{Protocol: ProtocolTCP, Target: "h"}, "port is required"},
		{"banner too long", Config{Protocol: ProtocolTCP, Target: "h", Port: 21, Check: &CheckOptions{Banner: strings.Repeat("a", MaxBannerLen+1)}}, "banner"},
		{"banner newline", Config{Protocol: ProtocolSSH, Target: "h", Port: 22, Check: &CheckOptions{Banner: "a\nb"}}, "invalid characters"},
		{"ssh", Config{Protocol: ProtocolSSH, Target: "h", Port: 22}, ""},
		{"ssh with tls", Config{Protocol: ProtocolSSH, Target: "h", Port: 22, Check: &CheckOptions{TLS: true}}, "unsupported check options"},
		{"redis auth", Config{Protocol: ProtocolRedis, Target: "h", Port: 6379, Check: &CheckOptions{Password: "p"}}, ""},
		{"password too long", Config{Protocol: ProtocolRedis, Target: "h", Port: 6379, Check: &CheckOptions{Password: strings.Repeat("p", MaxCheckTextLen+1)}}, "password"},
		{"smtp tls and starttls", Config{Protocol: ProtocolSMTP, Target: "h", Port: 25, Check: &CheckOptions{TLS: true, StartTLS: true}}, "cannot be combined"},
		{"mysql options", Config{Protocol: ProtocolMySQL, Target: "h", Port: 3306, Check: &CheckOptions{Username: "u"}}, "unsupported check options"},
		{"host with scheme", Config{Protocol: ProtocolPostgres, Target: "postgres://h", Port: 5432}, "host name"},
		{"grpc service", Config{Protocol: ProtocolGRPC, Target: "h", Port: 50051, Check: &CheckOptions{Service: "svc"}}, ""},
		{"docker name", Config{Protocol: ProtocolDocker, Target: "my_app-1.web"}, ""},
		{"docker bad name", Config{Protocol: ProtocolDocker, Target: "../etc"}, "container name"},
		{"unknown protocol", Config{Protocol: "ftp", Target: "h"}, "unsupported protocol"},
		{"minecraft", Config{Protocol: ProtocolMinecraft, Target: "mc.local", Port: 25565}, ""},
		{"a2s without port", Config{Protocol: ProtocolA2S, Target: "h"}, "port is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
			}
		})
	}
}

func TestProtocolHelpers(t *testing.T) {
	assert.EqualValues(t, 465, DefaultPort(ProtocolSMTP, &CheckOptions{TLS: true}))
	assert.EqualValues(t, 587, DefaultPort(ProtocolSMTP, &CheckOptions{StartTLS: true}))
	assert.EqualValues(t, 25, DefaultPort(ProtocolSMTP, nil))
	assert.EqualValues(t, 993, DefaultPort(ProtocolIMAP, &CheckOptions{TLS: true}))
	assert.EqualValues(t, 143, DefaultPort(ProtocolIMAP, nil))
	assert.EqualValues(t, 5432, DefaultPort(ProtocolPostgres, nil))
	assert.Zero(t, DefaultPort(ProtocolGRPC, nil), "grpc requires a port")
	assert.Zero(t, DefaultPort(ProtocolTCP, nil))
	for _, protocol := range checkProtocols {
		assert.True(t, IsCheckProtocol(protocol), protocol)
		assert.Contains(t, Protocols, protocol)
	}
	assert.False(t, IsCheckProtocol(ProtocolHTTP))
	assert.True(t, IsAgentOnlyProtocol(ProtocolDocker))
	assert.False(t, UsesPort(ProtocolDocker))
	assert.Equal(t, 10*time.Second, Config{Protocol: ProtocolSMTP}.ProbeTimeout())
	assert.Equal(t, 3*time.Second, Config{Protocol: ProtocolRedis}.ProbeTimeout())
}
