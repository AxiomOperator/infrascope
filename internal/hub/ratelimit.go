package hub

import (
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/hub/expirymap"
)

// windowLimiter limits events per key (such as a client IP) to limit per
// fixed window. A key's window starts with its first counted event.
type windowLimiter struct {
	limit  int
	window time.Duration

	mu     sync.Mutex
	counts *expirymap.ExpiryMap[rateWindow]
}

// rateWindow counts the events of a key until reset.
type rateWindow struct {
	count int
	reset time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{
		limit:  limit,
		window: window,
		counts: expirymap.New[rateWindow](window),
	}
}

func (l *windowLimiter) stop() {
	l.counts.StopCleaner()
}

// blocked reports whether the key reached the limit in its current window.
func (l *windowLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, ok := l.counts.GetOk(key)
	return ok && window.count >= l.limit
}

// add counts an event of the key.
func (l *windowLimiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addLocked(key, time.Now())
}

func (l *windowLimiter) addLocked(key string, now time.Time) rateWindow {
	window, ok := l.counts.GetOk(key)
	if !ok {
		window = rateWindow{reset: now.Add(l.window)}
	}
	window.count++
	l.counts.Set(key, window, window.reset.Sub(now))
	return window
}

// allow counts an event of the key and reports whether it is within the
// limit. When it is not, retryAfter is the time until the window resets.
// Rejected events are not counted.
func (l *windowLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if window, found := l.counts.GetOk(key); found && window.count >= l.limit {
		return false, window.reset.Sub(now)
	}
	l.addLocked(key, now)
	return true, 0
}
