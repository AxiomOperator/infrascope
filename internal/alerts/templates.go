package alerts

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"
	"unicode/utf8"
)

// Notification templates
//
// Users can override the title and body of their notifications with Go
// text/template templates, globally (user_settings.settings.templates) and
// per channel (notification_channels.template, which wins). Templates see
// TemplateData ({{.Title}}, {{.Message}}, ...) and a small function set.
// Loops, template definitions and calls, and formatting functions are not
// allowed, so execution is linear in the template size; output is capped and
// execution has a timeout as well. A template that fails to render falls
// back to the built-in text (logged once per template).
const (
	// maxTemplateLen is the maximum length of a title or body template.
	maxTemplateLen = 2000
	// maxTitleOutput and maxBodyOutput cap rendered output, in bytes.
	maxTitleOutput = 4000
	maxBodyOutput  = 20000
	// templateTimeout bounds a template's execution.
	templateTimeout = 250 * time.Millisecond
)

// NotificationTemplate overrides the title and body of notifications. Empty
// parts use the built-in text.
type NotificationTemplate struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// IsZero reports whether the template overrides nothing.
func (t NotificationTemplate) IsZero() bool {
	return strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Body) == ""
}

// TemplateData is the data templates render.
type TemplateData struct {
	Title    string
	Message  string
	Severity string
	Status   string
	Name     string
	Value    string
	Link     string
	AckLink  string
	Time     string
}

// sampleTemplateData renders template previews and validation dry runs.
func sampleTemplateData() TemplateData {
	return TemplateData{
		Title:    "web-01 CPU above threshold",
		Message:  "CPU averaged 92.40% for the previous 5 minutes.",
		Severity: string(SeverityWarning),
		Status:   "triggered",
		Name:     "web-01",
		Value:    "92.40%",
		Link:     "https://infrascope.example.com/system/abc123",
		AckLink:  "https://infrascope.example.com/api/beszel/ack/token",
		Time:     formatAlertTime(time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)),
	}
}

// templateFuncs are the functions templates may call, besides the
// comparison and logic builtins in allowedTemplateBuiltins.
var templateFuncs = template.FuncMap{
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"title": titleCase,
	"trim":  strings.TrimSpace,
	// default returns value, or def when value is empty: {{.Name | default "n/a"}}
	"default": func(def, value string) string {
		if strings.TrimSpace(value) == "" {
			return def
		}
		return value
	},
	// truncate shortens s to at most n runes: {{truncate 50 .Message}}
	"truncate": func(n int, s string) string {
		if n < 0 {
			n = 0
		}
		if utf8.RuneCountInString(s) <= n {
			return s
		}
		return string([]rune(s)[:n]) + "…"
	},
}

// allowedTemplateBuiltins are the text/template builtins templates may use.
var allowedTemplateBuiltins = map[string]bool{
	"and": true, "or": true, "not": true, "len": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
}

func titleCase(s string) string {
	prev := ' '
	return strings.Map(func(r rune) rune {
		defer func() { prev = r }()
		if unicode.IsSpace(prev) {
			return unicode.ToTitle(r)
		}
		return r
	}, s)
}

// parseTemplate parses a title or body template and checks it only uses
// allowed constructs.
func parseTemplate(name, text string) (*template.Template, error) {
	if len(text) > maxTemplateLen {
		return nil, fmt.Errorf("the %s template is longer than %d characters", name, maxTemplateLen)
	}
	tmpl, err := template.New(name).Option("missingkey=error").Funcs(templateFuncs).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("invalid %s template: %s", name, cleanTemplateError(err))
	}
	if len(tmpl.Templates()) > 1 {
		return nil, fmt.Errorf("invalid %s template: define and block are not allowed", name)
	}
	if tmpl.Tree != nil && tmpl.Root != nil {
		if err := checkTemplateNode(tmpl.Root); err != nil {
			return nil, fmt.Errorf("invalid %s template: %w", name, err)
		}
	}
	return tmpl, nil
}

func cleanTemplateError(err error) string {
	msg := err.Error()
	// "template: body:1: ..." -> "line 1: ..."
	if _, rest, ok := strings.Cut(msg, "template: "); ok {
		if _, after, ok := strings.Cut(rest, ":"); ok {
			return "line " + after
		}
	}
	return msg
}

// checkTemplateNode rejects loops, template calls and functions other than
// the allowed ones.
func checkTemplateNode(node parse.Node) error {
	switch n := node.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := checkTemplateNode(child); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return checkTemplateNode(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, cmd := range n.Cmds {
			if err := checkTemplateNode(cmd); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			if err := checkTemplateNode(arg); err != nil {
				return err
			}
		}
	case *parse.IdentifierNode:
		if _, ok := templateFuncs[n.Ident]; !ok && !allowedTemplateBuiltins[n.Ident] {
			return fmt.Errorf("function %q is not allowed", n.Ident)
		}
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.RangeNode:
		return errors.New("range is not allowed")
	case *parse.TemplateNode:
		return errors.New("template calls are not allowed")
	case *parse.ChainNode:
		return checkTemplateNode(n.Node)
	case *parse.TextNode, *parse.FieldNode, *parse.VariableNode, *parse.DotNode,
		*parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode, *parse.CommentNode:
		return nil
	default:
		return fmt.Errorf("unsupported template construct %q", node.String())
	}
	return nil
}

func checkBranch(n *parse.BranchNode) error {
	if err := checkTemplateNode(n.Pipe); err != nil {
		return err
	}
	if err := checkTemplateNode(n.List); err != nil {
		return err
	}
	if n.ElseList != nil {
		return checkTemplateNode(n.ElseList)
	}
	return nil
}

var errTemplateOutputTooLarge = errors.New("template output is too large")

// cappedBuffer fails writes past its limit.
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errTemplateOutputTooLarge
	}
	return b.Buffer.Write(p)
}

// executeTemplate renders tmpl with a timeout and an output cap.
func executeTemplate(tmpl *template.Template, data TemplateData, limit int) (string, error) {
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("template panic: %v", r)}
			}
		}()
		buf := &cappedBuffer{limit: limit}
		err := tmpl.Execute(buf, data)
		done <- result{out: buf.String(), err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return "", fmt.Errorf("%s", cleanTemplateError(r.err))
		}
		return r.out, nil
	case <-time.After(templateTimeout):
		return "", errors.New("template execution timed out")
	}
}

// renderTemplatePart renders one part of a template.
func renderTemplatePart(name, text string, data TemplateData, limit int) (string, error) {
	tmpl, err := parseTemplate(name, text)
	if err != nil {
		return "", err
	}
	return executeTemplate(tmpl, data, limit)
}

// ValidateTemplate parses t and renders it with sample data, returning a
// user-facing error.
func ValidateTemplate(t NotificationTemplate) error {
	_, _, err := RenderTemplate(t, sampleTemplateData())
	return err
}

// RenderTemplate renders t with data. Empty parts render as data's title or
// message.
func RenderTemplate(t NotificationTemplate, data TemplateData) (title, body string, err error) {
	title, body = data.Title, data.Message
	if strings.TrimSpace(t.Title) != "" {
		if title, err = renderTemplatePart("title", t.Title, data, maxTitleOutput); err != nil {
			return "", "", err
		}
		title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
		if title == "" {
			return "", "", errors.New("the title template renders empty")
		}
	}
	if strings.TrimSpace(t.Body) != "" {
		if body, err = renderTemplatePart("body", t.Body, data, maxBodyOutput); err != nil {
			return "", "", err
		}
	}
	return title, body, nil
}

// loggedTemplateErrors remembers failing templates already logged.
var loggedTemplateErrors sync.Map

// logTemplateErrorOnce logs a template failure once per template text.
func (am *AlertManager) logTemplateErrorOnce(t NotificationTemplate, err error) {
	key := sha256.Sum256([]byte(t.Title + "\x00" + t.Body))
	if _, loaded := loggedTemplateErrors.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	am.hub.Logger().Warn("Notification template failed; using the built-in message", "err", err)
}
