package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyConfig and legacyResult mirror the wire types before monitor checks
// were added, standing in for older agents and hubs.
type legacyConfig struct {
	ID       string `cbor:"0,keyasint"`
	Target   string `cbor:"1,keyasint"`
	Protocol string `cbor:"2,keyasint"`
	Port     uint16 `cbor:"3,keyasint,omitempty"`
	Interval uint16 `cbor:"4,keyasint"`
	Server   string `cbor:"5,keyasint,omitempty"`
}

type legacyResult struct {
	AvgResponse   int64     `cbor:"0,keyasint,omitempty"`
	AvgResponse1h int64     `cbor:"1,keyasint,omitempty"`
	MinResponse   int64     `cbor:"2,keyasint,omitempty"`
	MinResponse1h int64     `cbor:"3,keyasint,omitempty"`
	MaxResponse   int64     `cbor:"4,keyasint,omitempty"`
	MaxResponse1h int64     `cbor:"5,keyasint,omitempty"`
	PacketLoss    float64   `cbor:"6,keyasint,omitempty"`
	PacketLoss1h  float64   `cbor:"7,keyasint,omitempty"`
	LastProbeAt   int64     `cbor:"8,keyasint"`
	SampleCount   int64     `cbor:"9,keyasint,omitempty"`
	TotalCount    int64     `cbor:"10,keyasint"`
	SuccessCount  int64     `cbor:"11,keyasint"`
	ResponseSum   int64     `cbor:"12,keyasint"`
	Cert          *CertInfo `cbor:"13,keyasint,omitempty"`
}

func fullConfig() Config {
	return Config{
		ID: "m1", Target: "https://example.com", Protocol: "http", Interval: 30,
		Timeout: 20, RetryInterval: 10,
		HTTP: &HTTPOptions{
			Method: "POST", Headers: [][2]string{{"X-Test", "1"}}, Body: "{}",
			AcceptedCodes: []string{"200-299", "301"}, MaxRedirects: -1, IgnoreTLS: true,
			Keyword: "ok", KeywordInvert: true, JSONPath: "a.b[0]", JSONExpected: "1",
			BasicUser: "user", BasicPass: "pass",
		},
	}
}

func TestCBORCompatibility(t *testing.T) {
	t.Run("new config decoded by old agent", func(t *testing.T) {
		data, err := cbor.Marshal(fullConfig())
		require.NoError(t, err)
		var old legacyConfig
		require.NoError(t, cbor.Unmarshal(data, &old))
		assert.Equal(t, legacyConfig{ID: "m1", Target: "https://example.com", Protocol: "http", Interval: 30}, old)
	})

	t.Run("old config decoded by new agent", func(t *testing.T) {
		data, err := cbor.Marshal(legacyConfig{ID: "m1", Target: "1.1.1.1", Protocol: "icmp", Interval: 10})
		require.NoError(t, err)
		var config Config
		require.NoError(t, cbor.Unmarshal(data, &config))
		assert.Equal(t, Config{ID: "m1", Target: "1.1.1.1", Protocol: "icmp", Interval: 10}, config)
	})

	t.Run("legacy config encodes like an old config", func(t *testing.T) {
		old, err := cbor.Marshal(legacyConfig{ID: "m1", Target: "https://example.com", Protocol: "http", Interval: 30})
		require.NoError(t, err)
		legacy, err := cbor.Marshal(fullConfig().Legacy())
		require.NoError(t, err)
		assert.Equal(t, old, legacy)
	})

	t.Run("new config round trip", func(t *testing.T) {
		data, err := cbor.Marshal(fullConfig())
		require.NoError(t, err)
		var config Config
		require.NoError(t, cbor.Unmarshal(data, &config))
		assert.True(t, fullConfig().Equal(config))
	})

	t.Run("new result decoded by old hub", func(t *testing.T) {
		result := Result{AvgResponse: 5, LastProbeAt: 1000, TotalCount: 2, SuccessCount: 1, ResponseSum: 5,
			Checks: []CheckEvent{{At: 1, ResponseUs: 5, StatusCode: 200}, {At: 2, ResponseUs: -1, Err: "boom"}}, Dropped: 3}
		data, err := cbor.Marshal(result)
		require.NoError(t, err)
		var old legacyResult
		require.NoError(t, cbor.Unmarshal(data, &old))
		assert.Equal(t, legacyResult{AvgResponse: 5, LastProbeAt: 1000, TotalCount: 2, SuccessCount: 1, ResponseSum: 5}, old)

		var decoded Result
		require.NoError(t, cbor.Unmarshal(data, &decoded))
		assert.Equal(t, result, decoded)
	})

	t.Run("old result decoded by new hub", func(t *testing.T) {
		data, err := cbor.Marshal(legacyResult{AvgResponse: 5, LastProbeAt: 1000})
		require.NoError(t, err)
		var result Result
		require.NoError(t, cbor.Unmarshal(data, &result))
		assert.Equal(t, Result{AvgResponse: 5, LastProbeAt: 1000}, result)
	})

	t.Run("results without checks encode unchanged", func(t *testing.T) {
		old, err := cbor.Marshal(legacyResult{AvgResponse: 5, LastProbeAt: 1000})
		require.NoError(t, err)
		current, err := cbor.Marshal(Result{AvgResponse: 5, LastProbeAt: 1000})
		require.NoError(t, err)
		assert.Equal(t, old, current)
	})
}

func TestResultIsZero(t *testing.T) {
	assert.True(t, Result{}.IsZero())
	assert.False(t, Result{Checks: []CheckEvent{{At: 1}}}.IsZero())
	assert.False(t, Result{LastProbeAt: 1}.IsZero())
}

func TestConfigEqualComparesHTTPByValue(t *testing.T) {
	a, b := fullConfig(), fullConfig()
	require.NotSame(t, a.HTTP, b.HTTP)
	assert.True(t, a.Equal(b))
	b.HTTP.Headers[0][1] = "2"
	assert.False(t, a.Equal(b))
	b = fullConfig()
	b.HTTP = nil
	assert.False(t, a.Equal(b))
}

func TestConfigProbeTimeout(t *testing.T) {
	assert.Equal(t, 3*time.Second, Config{Protocol: "tcp"}.ProbeTimeout())
	assert.Equal(t, 3*time.Second, Config{Protocol: "icmp"}.ProbeTimeout())
	assert.Equal(t, 3*time.Second, Config{Protocol: "dns"}.ProbeTimeout())
	assert.Equal(t, 10*time.Second, Config{Protocol: "http"}.ProbeTimeout())
	assert.Equal(t, 25*time.Second, Config{Protocol: "tcp", Timeout: 25}.ProbeTimeout())
	assert.Equal(t, MaxProbeTimeout, Config{Protocol: "http", Timeout: 600}.ProbeTimeout())
}

func TestConfigLegacy(t *testing.T) {
	config := fullConfig()
	legacy := config.Legacy()
	assert.Zero(t, legacy.Timeout)
	assert.Nil(t, legacy.HTTP)
	assert.Zero(t, legacy.RetryInterval)
	assert.Equal(t, config.Target, legacy.Target)
	assert.NotNil(t, config.HTTP, "Legacy must not modify the original")
}

func TestConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		err    string
	}{
		{"valid", func(*Config) {}, ""},
		{"no http options", func(c *Config) { c.HTTP = nil }, ""},
		{"empty http options", func(c *Config) { c.HTTP = &HTTPOptions{} }, ""},
		{"max timeout", func(c *Config) { c.Timeout = 60 }, ""},
		{"timeout too long", func(c *Config) { c.Timeout = 61 }, "timeout"},
		{"http options on tcp", func(c *Config) { c.Protocol = "tcp" }, "http protocol"},
		{"all methods", func(c *Config) { c.HTTP.Method = "OPTIONS" }, ""},
		{"bad method", func(c *Config) { c.HTTP.Method = "TRACE" }, "method"},
		{"lowercase method", func(c *Config) { c.HTTP.Method = "get" }, "method"},
		{"bad header name", func(c *Config) { c.HTTP.Headers = [][2]string{{"X Bad", "1"}} }, "header name"},
		{"empty header name", func(c *Config) { c.HTTP.Headers = [][2]string{{"", "1"}} }, "header name"},
		{"header CR", func(c *Config) { c.HTTP.Headers = [][2]string{{"X-A", "1\r\nX-B: 2"}} }, "invalid characters"},
		{"header LF", func(c *Config) { c.HTTP.Headers = [][2]string{{"X-A", "1\n"}} }, "invalid characters"},
		{"too many headers", func(c *Config) { c.HTTP.Headers = make([][2]string, MaxHTTPHeaders+1) }, "headers"},
		{"body limit", func(c *Config) { c.HTTP.Body = strings.Repeat("a", MaxHTTPBodyLen) }, ""},
		{"body too long", func(c *Config) { c.HTTP.Body = strings.Repeat("a", MaxHTTPBodyLen+1) }, "body"},
		{"code range", func(c *Config) { c.HTTP.AcceptedCodes = []string{"100-599", "200-200"} }, ""},
		{"code below range", func(c *Config) { c.HTTP.AcceptedCodes = []string{"099"} }, "status code"},
		{"code above range", func(c *Config) { c.HTTP.AcceptedCodes = []string{"600"} }, "status code"},
		{"code text", func(c *Config) { c.HTTP.AcceptedCodes = []string{"2xx"} }, "status code"},
		{"inverted range", func(c *Config) { c.HTTP.AcceptedCodes = []string{"299-200"} }, "range"},
		{"open range", func(c *Config) { c.HTTP.AcceptedCodes = []string{"200-"} }, "range"},
		{"redirects -1", func(c *Config) { c.HTTP.MaxRedirects = -1 }, ""},
		{"redirects max", func(c *Config) { c.HTTP.MaxRedirects = 20 }, ""},
		{"redirects too many", func(c *Config) { c.HTTP.MaxRedirects = 21 }, "redirects"},
		{"redirects too low", func(c *Config) { c.HTTP.MaxRedirects = -2 }, "redirects"},
		{"keyword too long", func(c *Config) { c.HTTP.Keyword = strings.Repeat("a", 501) }, "keyword"},
		{"json path too long", func(c *Config) { c.HTTP.JSONPath = strings.Repeat("a", 501) }, "json path"},
		{"bad json path", func(c *Config) { c.HTTP.JSONPath = "a..b" }, "json path"},
		{"basic user colon", func(c *Config) { c.HTTP.BasicUser = "a:b" }, "colon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := fullConfig()
			tc.mutate(&config)
			err := config.Validate()
			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
			}
		})
	}
}

func TestHTTPOptionsAcceptsStatus(t *testing.T) {
	var none *HTTPOptions
	assert.True(t, none.AcceptsStatus(200))
	assert.True(t, none.AcceptsStatus(399))
	assert.False(t, none.AcceptsStatus(400))
	assert.False(t, none.AcceptsStatus(199))
	opts := &HTTPOptions{AcceptedCodes: []string{"200-204", "404"}}
	assert.True(t, opts.AcceptsStatus(204))
	assert.True(t, opts.AcceptsStatus(404))
	assert.False(t, opts.AcceptsStatus(301))
}

func TestParseJSONPath(t *testing.T) {
	key := func(k string) JSONPathSegment { return JSONPathSegment{Key: k} }
	index := func(i int) JSONPathSegment { return JSONPathSegment{Index: i, IsIndex: true} }
	for _, tc := range []struct {
		path string
		want []JSONPathSegment
	}{
		{"a", []JSONPathSegment{key("a")}},
		{"a.b[0].c", []JSONPathSegment{key("a"), key("b"), index(0), key("c")}},
		{"$.a.b", []JSONPathSegment{key("a"), key("b")}},
		{"$[1]", []JSONPathSegment{index(1)}},
		{"[0][2]", []JSONPathSegment{index(0), index(2)}},
		{"$", nil},
		{"a-b.c_d", []JSONPathSegment{key("a-b"), key("c_d")}},
	} {
		got, err := ParseJSONPath(tc.path)
		require.NoError(t, err, tc.path)
		assert.Equal(t, tc.want, got, tc.path)
	}
	for _, path := range []string{"", ".", "a.", ".a", "a..b", "a[", "a[x]", "a[-1]", "a[+1]", "a[]", "a]", "a[0]b", "a.[0]", "$a", "$."} {
		_, err := ParseJSONPath(path)
		assert.Error(t, err, path)
	}
}

func TestCheckErrorCompaction(t *testing.T) {
	events := []CheckEvent{
		{At: 1, ResponseUs: -1, Err: "a"},
		{At: 2, ResponseUs: -1, Err: "a"},
		{At: 3, ResponseUs: -1, Err: "b"},
		{At: 4, ResponseUs: 10},
		{At: 5, ResponseUs: -1, Err: "b"},
		{At: 6, ResponseUs: -1, Err: "b"},
	}
	compact := append([]CheckEvent(nil), events...)
	CompactCheckErrors(compact)
	assert.Equal(t, []string{"a", "", "b", "", "b", ""}, []string{compact[0].Err, compact[1].Err, compact[2].Err, compact[3].Err, compact[4].Err, compact[5].Err})
	ExpandCheckErrors(compact)
	assert.Equal(t, events, compact)
}
