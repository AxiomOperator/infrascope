package hub

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/expirymap"
	"github.com/henrygd/beszel/internal/netmon"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

const (
	// pushTokenInterval is the shortest time between accepted pushes of a monitor.
	pushTokenInterval = time.Second
	// pushUnknownLimit is how many unknown-token pushes a client IP may send per pushUnknownWindow.
	pushUnknownLimit  = 30
	pushUnknownWindow = time.Minute
	// maxPushMessage is the maximum length of a push message, in characters.
	maxPushMessage = 200
	// pushDownError is the error of a down push without a message.
	pushDownError = "push reported down"
)

// pushLimiter rate limits push requests per monitor and unknown tokens per client IP.
type pushLimiter struct {
	mu       sync.Mutex
	monitors *expirymap.ExpiryMap[bool]
	ips      *expirymap.ExpiryMap[pushWindow]
}

// pushWindow counts unknown-token requests of a client IP until reset.
type pushWindow struct {
	count int
	reset time.Time
}

func newPushLimiter() *pushLimiter {
	return &pushLimiter{
		monitors: expirymap.New[bool](time.Minute),
		ips:      expirymap.New[pushWindow](time.Minute),
	}
}

func (l *pushLimiter) stop() {
	l.monitors.StopCleaner()
	l.ips.StopCleaner()
}

// allowMonitor reports whether a push for the monitor is accepted now. Only
// accepted pushes start a new interval.
func (l *pushLimiter) allowMonitor(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, limited := l.monitors.GetOk(id); limited {
		return false
	}
	l.monitors.Set(id, true, pushTokenInterval)
	return true
}

// ipBlocked reports whether the IP sent too many unknown-token requests.
func (l *pushLimiter) ipBlocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, ok := l.ips.GetOk(ip)
	return ok && window.count >= pushUnknownLimit
}

// unknownToken counts an unknown-token request of the IP.
func (l *pushLimiter) unknownToken(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window, ok := l.ips.GetOk(ip)
	if !ok {
		window = pushWindow{reset: now.Add(pushUnknownWindow)}
	}
	window.count++
	l.ips.Set(ip, window, window.reset.Sub(now))
}

// pushResponse is the JSON body of push responses (Uptime Kuma compatible).
type pushResponse struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg,omitempty"`
}

// handlePush records a check of a push monitor. It is compatible with Uptime
// Kuma push URLs: GET or POST /api/beszel/push/{token} with the optional query
// parameters status ("up" by default; any other value is down), msg (stored as
// the error of a down check) and ping (response time in milliseconds).
//
// Unknown tokens are limited per client IP, as reported by e.RealIP. Behind a
// reverse proxy, configure the trusted proxy settings in PocketBase (Settings >
// Application > User IP proxy headers) so clients are told apart; otherwise
// all clients share the proxy's limit.
func (h *Hub) handlePush(e *core.RequestEvent) error {
	runner, ok := h.hubMonitors.(*hubRunner)
	var limits *pushLimiter
	if ok {
		limits = runner.pushLimits()
	}
	if limits == nil {
		return e.JSON(http.StatusServiceUnavailable, pushResponse{Msg: "hub monitors are disabled"})
	}
	ip := e.RealIP()
	if limits.ipBlocked(ip) {
		return e.JSON(http.StatusTooManyRequests, pushResponse{Msg: "too many requests"})
	}
	id, ok := runner.monitorForToken(e.Request.PathValue("token"))
	if !ok {
		limits.unknownToken(ip)
		return e.JSON(http.StatusNotFound, pushResponse{Msg: "monitor not found"})
	}
	if !limits.allowMonitor(id) {
		return e.JSON(http.StatusTooManyRequests, pushResponse{Msg: "too many requests"})
	}

	query := e.Request.URL.Query()
	var out netmon.Outcome
	if status := query.Get("status"); status != "" && status != "up" {
		msg := truncateRunes(query.Get("msg"), maxPushMessage)
		if msg == "" {
			msg = pushDownError
		}
		out = netmon.Outcome{ResponseUs: -1, Err: errors.New(msg)}
	} else if ping, err := strconv.ParseFloat(query.Get("ping"), 64); err == nil && ping > 0 && !math.IsInf(ping, 0) {
		out.ResponseUs = int64(min(ping, float64(monitor.MaxProbeTimeout.Milliseconds())) * 1000)
	}
	if err := runner.recordPush(id, out); err != nil {
		return e.JSON(http.StatusNotFound, pushResponse{Msg: "monitor not found"})
	}
	return e.JSON(http.StatusOK, pushResponse{OK: true})
}

// truncateRunes returns at most n characters of s.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// regeneratePushToken replaces the push token of a push monitor the requester
// can update and returns the new token.
func (h *Hub) regeneratePushToken(e *core.RequestEvent) error {
	record, err := e.App.FindRecordById("network_monitors", e.Request.PathValue("id"))
	if err != nil {
		return e.NotFoundError("", nil)
	}
	if !e.HasSuperuserAuth() {
		info, err := e.RequestInfo()
		if err != nil {
			return err
		}
		collection := record.Collection()
		if ok, _ := e.App.CanAccessRecord(record, info, collection.ViewRule); !ok {
			return e.NotFoundError("", nil)
		}
		if ok, _ := e.App.CanAccessRecord(record, info, collection.UpdateRule); !ok {
			return e.ForbiddenError("You cannot update this monitor.", nil)
		}
	}
	if record.GetString("protocol") != monitor.ProtocolPush {
		return e.BadRequestError("Only push monitors have a push token", nil)
	}
	token := security.RandomStringWithAlphabet(pushTokenLength, pushTokenAlphabet)
	record.Set("pushToken", token)
	// Save directly: the request hooks keep pushToken unchanged.
	if err := e.App.Save(record); err != nil {
		return e.BadRequestError("Failed to save the push token", err)
	}
	h.hubMonitors.Sync(record)
	return e.JSON(http.StatusOK, map[string]string{"pushToken": token})
}

// pushLimits returns the push rate limiter while the runner runs, or nil.
func (r *hubRunner) pushLimits() *pushLimiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.stopped {
		return nil
	}
	return r.limits
}
