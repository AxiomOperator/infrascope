package agent

import (
	"log/slog"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/henrygd/beszel/internal/entities/container"
)

const imageUpdateInterval = time.Hour

type imageUpdateStatus struct {
	available bool      // result of the last successful check
	checkedAt time.Time // time of the last successful check
	// Failure tracking: a failed check keeps the previous result and is retried
	// after a short backoff (see updateCheckRetryDelay) instead of the full interval.
	lastErr  error
	failures int
	retryAt  time.Time
}

// due reports whether the entry should be checked. After a failure the retry
// time wins over the regular interval.
func (s *imageUpdateStatus) due(now time.Time) bool {
	if !s.retryAt.IsZero() {
		return !now.Before(s.retryAt)
	}
	return s.checkedAt.IsZero() || now.Sub(s.checkedAt) >= imageUpdateInterval
}

func normalizedImageReference(image string) string {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}
	// Digest-pinned references cannot move to a new version.
	if _, pinned := named.(reference.Digested); pinned {
		return ""
	}
	return reference.TagNameOnly(named).String()
}

// refreshImageUpdates starts at most one background batch. Neither its network
// work nor its completion is part of the container metrics wait group.
func (dm *dockerManager) refreshImageUpdates(containers []*container.ApiInfo, now time.Time) {
	if dm.imageUpdatesDisabled {
		return
	}
	dm.imageUpdatesMutex.Lock()
	defer dm.imageUpdatesMutex.Unlock()
	if dm.imageUpdatesRunning {
		return
	}
	if dm.imageUpdates == nil {
		dm.imageUpdates = make(map[string]*imageUpdateStatus)
	}
	active := make(map[string]struct{}, len(containers))
	pending := make(map[string]*imageUpdateStatus)
	for _, ctr := range containers {
		if len(ctr.Names) > 0 && dm.shouldExcludeContainer(containerName(ctr)) {
			continue
		}
		key := normalizedImageReference(ctr.Image)
		if key == "" {
			continue
		}
		active[key] = struct{}{}
		entry := dm.imageUpdates[key]
		if entry == nil {
			entry = &imageUpdateStatus{}
			dm.imageUpdates[key] = entry
		}
		if entry.due(now) {
			pending[key] = entry
		}
	}
	for key := range dm.imageUpdates {
		if _, ok := active[key]; !ok {
			delete(dm.imageUpdates, key)
		}
	}
	if len(pending) == 0 {
		return
	}
	dm.imageUpdatesRunning = true
	go func() {
		// Limit auxiliary requests even on hosts running many different images.
		sem := make(chan struct{}, 2)
		var wg sync.WaitGroup
		for key, entry := range pending {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				available, err := dm.checkImageUpdate(key)
				dm.imageUpdatesMutex.Lock()
				defer dm.imageUpdatesMutex.Unlock()
				if err != nil {
					// Keep the previous result; retry sooner than the full interval.
					entry.lastErr = err
					entry.failures++
					delay := updateCheckRetryDelay(entry.failures, imageUpdateInterval)
					entry.retryAt = time.Now().Add(delay)
					slog.Debug("Image update check failed", "image", key, "err", err, "retry_in", delay)
					return
				}
				entry.available = available
				entry.checkedAt = time.Now()
				entry.lastErr = nil
				entry.failures = 0
				entry.retryAt = time.Time{}
			}()
		}
		wg.Wait()
		dm.imageUpdatesMutex.Lock()
		dm.imageUpdatesRunning = false
		dm.imageUpdatesMutex.Unlock()
	}()
}

func (dm *dockerManager) cachedImageUpdate(image string) bool {
	key := normalizedImageReference(image)
	dm.imageUpdatesMutex.RLock()
	defer dm.imageUpdatesMutex.RUnlock()
	entry := dm.imageUpdates[key]
	return entry != nil && entry.available
}
