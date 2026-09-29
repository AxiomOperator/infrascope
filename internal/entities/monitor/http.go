package monitor

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits for HTTP options, enforced by Validate.
const (
	MaxHTTPBodyLen       = 64 << 10
	MaxHTTPHeaders       = 50
	MaxHTTPHeaderLen     = 8 << 10
	MaxHTTPAcceptedCodes = 20
	MaxHTTPRedirects     = 20
	MaxHTTPTextLen       = 500 // keyword, JSON path, expected value and credentials
)

// HTTPOptions configures the request and success criteria of an http monitor.
// The zero value sends a GET request, follows up to 10 redirects and accepts
// status codes 200-399.
type HTTPOptions struct {
	// Method is the request method; empty means GET.
	Method  string      `cbor:"0,keyasint,omitempty"`
	Headers [][2]string `cbor:"1,keyasint,omitempty"`
	Body    string      `cbor:"2,keyasint,omitempty"`
	// AcceptedCodes lists status codes ("301") or inclusive ranges ("200-299")
	// that count as success; empty means 200-399.
	AcceptedCodes []string `cbor:"3,keyasint,omitempty"`
	// MaxRedirects is the number of redirects to follow: 0 means 10, and -1
	// evaluates the redirect response itself.
	MaxRedirects int8 `cbor:"4,keyasint,omitempty"`
	// IgnoreTLS skips certificate verification.
	IgnoreTLS bool `cbor:"5,keyasint,omitempty"`
	// Keyword must appear in the response body, or must not when KeywordInvert is set.
	Keyword       string `cbor:"6,keyasint,omitempty"`
	KeywordInvert bool   `cbor:"7,keyasint,omitempty"`
	// JSONPath selects a value (e.g. "data.items[0].status") in a JSON response,
	// which must equal JSONExpected when formatted as text.
	JSONPath     string `cbor:"8,keyasint,omitempty"`
	JSONExpected string `cbor:"9,keyasint,omitempty"`
	BasicUser    string `cbor:"10,keyasint,omitempty"`
	BasicPass    string `cbor:"11,keyasint,omitempty"`
}

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// Validate reports whether the config can be sent to an agent.
func (c Config) Validate() error {
	if time.Duration(c.Timeout)*time.Second > MaxProbeTimeout {
		return fmt.Errorf("timeout must be at most %d seconds", int(MaxProbeTimeout/time.Second))
	}
	if c.HTTP != nil {
		if c.Protocol != "http" {
			return errors.New("http options require the http protocol")
		}
		if err := c.HTTP.Validate(); err != nil {
			return fmt.Errorf("http options: %w", err)
		}
	}
	if c.Protocol != "" && !slices.Contains(Protocols, c.Protocol) {
		return fmt.Errorf("unsupported protocol %q", c.Protocol)
	}
	if UsesPort(c.Protocol) && c.Port == 0 {
		return errors.New("port is required")
	}
	if err := c.validateTarget(); err != nil {
		return err
	}
	if err := c.Check.Validate(c.Protocol); err != nil {
		return fmt.Errorf("check options: %w", err)
	}
	return nil
}

// Validate reports whether the options are well formed and within limits.
func (o *HTTPOptions) Validate() error {
	if o == nil {
		return nil
	}
	if o.Method != "" && !slices.Contains(httpMethods, o.Method) {
		return fmt.Errorf("unsupported method %q", o.Method)
	}
	if len(o.Headers) > MaxHTTPHeaders {
		return fmt.Errorf("at most %d headers are allowed", MaxHTTPHeaders)
	}
	for _, header := range o.Headers {
		if !validHeaderName(header[0]) {
			return fmt.Errorf("invalid header name %q", header[0])
		}
		if len(header[1]) > MaxHTTPHeaderLen {
			return fmt.Errorf("header %s is too long", header[0])
		}
		if strings.ContainsAny(header[1], "\r\n\x00") {
			return fmt.Errorf("header %s contains invalid characters", header[0])
		}
	}
	if len(o.Body) > MaxHTTPBodyLen {
		return fmt.Errorf("body must be at most %d bytes", MaxHTTPBodyLen)
	}
	if len(o.AcceptedCodes) > MaxHTTPAcceptedCodes {
		return fmt.Errorf("at most %d accepted status codes are allowed", MaxHTTPAcceptedCodes)
	}
	for _, code := range o.AcceptedCodes {
		if _, _, err := parseStatusRange(code); err != nil {
			return err
		}
	}
	if o.MaxRedirects < -1 || o.MaxRedirects > MaxHTTPRedirects {
		return fmt.Errorf("max redirects must be between -1 and %d", MaxHTTPRedirects)
	}
	for name, value := range map[string]string{
		"keyword":             o.Keyword,
		"json path":           o.JSONPath,
		"expected json value": o.JSONExpected,
		"basic auth user":     o.BasicUser,
		"basic auth password": o.BasicPass,
	} {
		if len(value) > MaxHTTPTextLen {
			return fmt.Errorf("%s must be at most %d characters", name, MaxHTTPTextLen)
		}
	}
	if strings.Contains(o.BasicUser, ":") {
		return errors.New("basic auth user must not contain a colon")
	}
	if o.JSONPath != "" {
		if _, err := ParseJSONPath(o.JSONPath); err != nil {
			return err
		}
	}
	return nil
}

// AcceptsStatus reports whether code counts as success. Invalid entries are
// ignored; nil options or an empty list accept 200-399.
func (o *HTTPOptions) AcceptsStatus(code int) bool {
	if o == nil || len(o.AcceptedCodes) == 0 {
		return code >= 200 && code < 400
	}
	for _, entry := range o.AcceptedCodes {
		lo, hi, err := parseStatusRange(entry)
		if err == nil && code >= lo && code <= hi {
			return true
		}
	}
	return false
}

// parseStatusRange parses "301" or "200-299" into an inclusive range.
func parseStatusRange(entry string) (lo, hi int, err error) {
	loText, hiText, isRange := strings.Cut(strings.TrimSpace(entry), "-")
	if lo, err = parseStatusCode(loText); err != nil {
		return 0, 0, fmt.Errorf("invalid accepted status code %q", entry)
	}
	hi = lo
	if isRange {
		if hi, err = parseStatusCode(hiText); err != nil || hi < lo {
			return 0, 0, fmt.Errorf("invalid accepted status code range %q", entry)
		}
	}
	return lo, hi, nil
}

func parseStatusCode(text string) (int, error) {
	text = strings.TrimSpace(text)
	if len(text) != 3 {
		return 0, errors.New("status code must have three digits")
	}
	code, err := strconv.Atoi(text)
	if err != nil || code < 100 || code > 599 {
		return 0, errors.New("status code must be between 100 and 599")
	}
	return code, nil
}

// validHeaderName reports whether name is a non-empty RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// JSONPathSegment is an object key or, when IsIndex is set, an array index.
type JSONPathSegment struct {
	Key     string
	Index   int
	IsIndex bool
}

// ParseJSONPath parses a dotted path with array indexes, such as
// "a.b[0].c". A leading "$" or "$." is accepted, and "$" alone selects the
// document root. Keys cannot contain '.', '[' or ']'.
func ParseJSONPath(path string) ([]JSONPathSegment, error) {
	s := path
	if rest, ok := strings.CutPrefix(s, "$"); ok {
		if rest == "" {
			return nil, nil
		}
		switch rest[0] {
		case '.':
			rest = rest[1:]
		case '[':
		default:
			return nil, fmt.Errorf("invalid json path %q", path)
		}
		s = rest
	}
	if s == "" {
		return nil, fmt.Errorf("invalid json path %q", path)
	}
	var segments []JSONPathSegment
	for i := 0; i < len(s); {
		if s[i] == '[' {
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("invalid json path %q: unclosed index", path)
			}
			digits := s[i+1 : i+end]
			index, err := strconv.Atoi(digits)
			if err != nil || digits == "" || digits[0] < '0' || digits[0] > '9' {
				return nil, fmt.Errorf("invalid json path %q: bad index", path)
			}
			segments = append(segments, JSONPathSegment{Index: index, IsIndex: true})
			i += end + 1
			if i < len(s) && s[i] != '.' && s[i] != '[' {
				return nil, fmt.Errorf("invalid json path %q: expected '.' or '[' after index", path)
			}
		} else {
			j := i
			for j < len(s) && s[j] != '.' && s[j] != '[' && s[j] != ']' {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("invalid json path %q: empty key", path)
			}
			segments = append(segments, JSONPathSegment{Key: s[i:j]})
			i = j
		}
		if i < len(s) && s[i] == '.' {
			i++
			if i == len(s) || s[i] == '[' {
				return nil, fmt.Errorf("invalid json path %q: empty key", path)
			}
		}
	}
	return segments, nil
}
