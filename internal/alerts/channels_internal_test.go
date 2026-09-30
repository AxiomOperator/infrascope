//go:build testing

package alerts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func channelIDs(channels []channel) []string {
	ids := make([]string, len(channels))
	for i, c := range channels {
		ids[i] = c.ID
	}
	return ids
}

func TestRouteChannelsMatrix(t *testing.T) {
	channels := []channel{
		{ID: "info", Enabled: true, IsDefault: true, MinSeverity: SeverityInfo},
		{ID: "warn", Enabled: true, IsDefault: true, MinSeverity: SeverityWarning},
		{ID: "crit", Enabled: true, IsDefault: true, MinSeverity: SeverityCritical},
		{ID: "off", Enabled: false, IsDefault: true, MinSeverity: SeverityInfo},
		{ID: "extra", Enabled: true, IsDefault: false, MinSeverity: SeverityCritical},
	}
	for _, tc := range []struct {
		name     string
		explicit []string
		severity Severity
		want     []string
	}{
		{"info reaches info thresholds", nil, SeverityInfo, []string{"info"}},
		{"warning reaches info and warning", nil, SeverityWarning, []string{"info", "warn"}},
		{"critical reaches all enabled defaults", nil, SeverityCritical, []string{"info", "warn", "crit"}},
		{"unknown severity routes as warning", nil, Severity("bogus"), []string{"info", "warn"}},
		{"explicit ignores threshold and default flag", []string{"extra"}, SeverityInfo, []string{"extra"}},
		{"explicit several", []string{"crit", "extra"}, SeverityInfo, []string{"crit", "extra"}},
		{"explicit disabled sends nothing", []string{"off"}, SeverityCritical, nil},
		{"explicit foreign falls back to defaults", []string{"someone-else"}, SeverityWarning, []string{"info", "warn"}},
		{"explicit mixed keeps own", []string{"someone-else", "extra"}, SeverityInfo, []string{"extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := channelIDs(routeChannels(channels, tc.explicit, tc.severity))
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestDefaultSeverities(t *testing.T) {
	for name, want := range map[string]Severity{
		"Status": SeverityCritical, alertNameMonitorDown: SeverityCritical,
		containerAlertName: SeverityCritical, alertNameSystemdFailed: SeverityCritical,
		"CPU": SeverityWarning, "Memory": SeverityWarning, "Disk": SeverityWarning, "Temperature": SeverityWarning,
		"Bandwidth": SeverityWarning, "GPU": SeverityWarning, "LoadAvg5": SeverityWarning, "Battery": SeverityWarning,
		alertNameMonitorLoss: SeverityWarning, alertNameMonitorLatency: SeverityWarning,
		alertNameNetworkMonitorLoss: SeverityWarning,
	} {
		assert.Equal(t, want, defaultSeverity(name, 0), name)
	}
	assert.Equal(t, SeverityWarning, defaultSeverity(alertNameMonitorCert, 10))
	assert.Equal(t, SeverityWarning, defaultSeverity(alertNameMonitorCert, 4))
	assert.Equal(t, SeverityCritical, defaultSeverity(alertNameMonitorCert, 3))
	assert.Equal(t, SeverityCritical, defaultSeverity(alertNameMonitorCert, -1))
	assert.Equal(t, SeverityCritical, Severity("").orDefault(SeverityCritical))
	assert.Equal(t, SeverityInfo, Severity("info").orDefault(SeverityCritical))
}

func TestMonitorRecoveryFollowsAlertRouting(t *testing.T) {
	target := monitorTarget{id: "m1", name: "api", severity: SeverityInfo, channels: []string{"c1"}}
	down := target.message("u", alertNameMonitorDown, SeverityCritical, true)
	up := target.message("u", alertNameMonitorDown, SeverityCritical, false)
	assert.Equal(t, down.Severity, up.Severity)
	assert.Equal(t, down.Channels, up.Channels)
	assert.Equal(t, SeverityInfo, up.Severity, "override applies")
	target.severity = ""
	assert.Equal(t, SeverityCritical, target.message("u", alertNameMonitorDown, SeverityCritical, false).Severity)
	assert.Equal(t, "resolved", up.Status)
}

func TestRenderTemplate(t *testing.T) {
	data := sampleTemplateData()
	title, body, err := RenderTemplate(NotificationTemplate{
		Title: "[{{upper .Severity}}] {{.Name}}",
		Body:  "{{.Message}}\n{{if eq .Status \"triggered\"}}Ack: {{.AckLink}}{{else}}ok{{end}}\n{{truncate 3 .Name}} {{.Value | default \"n/a\"}} {{title \"a b\"}}",
	}, data)
	require.NoError(t, err)
	assert.Equal(t, "[WARNING] web-01", title)
	assert.Equal(t, data.Message+"\nAck: "+data.AckLink+"\nweb… 92.40% A B", body)

	// Empty parts keep the built-in text.
	title, body, err = RenderTemplate(NotificationTemplate{Body: "x"}, data)
	require.NoError(t, err)
	assert.Equal(t, data.Title, title)
	assert.Equal(t, "x", body)
}

func TestValidateTemplateRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		tmpl NotificationTemplate
		msg  string
	}{
		"syntax":         {NotificationTemplate{Body: "{{.Title"}, "invalid body template"},
		"unknown field":  {NotificationTemplate{Title: "{{.Nope}}"}, "Nope"},
		"range":          {NotificationTemplate{Body: "{{range 1000000000}}x{{end}}"}, "range is not allowed"},
		"define":         {NotificationTemplate{Body: `{{define "a"}}{{template "a"}}{{end}}`}, "not allowed"},
		"template call":  {NotificationTemplate{Body: `{{template "x"}}`}, "not allowed"},
		"printf":         {NotificationTemplate{Body: `{{printf "%999999999d" 1}}`}, `"printf" is not allowed`},
		"call":           {NotificationTemplate{Body: `{{call .Title}}`}, `"call" is not allowed`},
		"too long":       {NotificationTemplate{Body: strings.Repeat("a", maxTemplateLen+1)}, "longer than 2000"},
		"empty title":    {NotificationTemplate{Title: "{{if false}}x{{end}}"}, "renders empty"},
		"nested printf":  {NotificationTemplate{Body: `{{if true}}{{printf "%v" 1}}{{end}}`}, `"printf" is not allowed`},
		"field on value": {NotificationTemplate{Body: `{{.Title.Foo}}`}, "Foo"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateTemplate(tc.tmpl)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
	require.NoError(t, ValidateTemplate(NotificationTemplate{}))
	require.NoError(t, ValidateTemplate(NotificationTemplate{Title: strings.Repeat("a", maxTemplateLen)}))
}

func TestTemplateOutputCap(t *testing.T) {
	// 2000 chars of template can expand .Message many times; the cap stops it.
	body := strings.Repeat("{{.Message}}", maxTemplateLen/len("{{.Message}}"))
	data := sampleTemplateData()
	data.Message = strings.Repeat("x", 1000)
	_, _, err := RenderTemplate(NotificationTemplate{Body: body}, data)
	require.ErrorContains(t, err, "too large")
}
