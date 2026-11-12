package adroute

import (
	"errors"
	"sync"
	"time"
)

// failoverCooldown is how long a member that just failed is deprioritised in
// its chain. It is a product constant, not a user setting: the goal is only to
// stop every new request from paying the same member's timeout a second time.
const failoverCooldown = 60 * time.Second

// cooldownTable remembers when each model entry last failed. Entries are keyed
// by the stable model entry id, so renaming a model or moving it to another
// group keeps its state. The table lives in memory only: a restart forgets it,
// which is also the clearest signal that health is worth probing again.
type cooldownTable struct {
	mu     sync.Mutex
	until  map[string]time.Time
	window time.Duration
	now    func() time.Time
}

func newCooldownTable(window time.Duration, now func() time.Time) *cooldownTable {
	if now == nil {
		now = time.Now
	}
	return &cooldownTable{until: map[string]time.Time{}, window: window, now: now}
}

// cooling reports whether id failed recently enough to be moved to the back of
// its chain. An expired entry is dropped on read so the map cannot grow stale.
func (t *cooldownTable) cooling(id string) bool {
	if t == nil || t.window <= 0 || id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	until, found := t.until[id]
	if !found {
		return false
	}
	if !t.now().Before(until) {
		delete(t.until, id)
		return false
	}
	return true
}

// failed starts or extends the cooldown for id.
func (t *cooldownTable) failed(id string) {
	if t == nil || t.window <= 0 || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until[id] = t.now().Add(t.window)
}

// served clears the cooldown for id: the member answered, so an earlier failure
// is no longer evidence about it.
func (t *cooldownTable) served(id string) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.until, id)
}

// orderByCooldown moves members that just failed behind the healthy ones while
// preserving the configured order on each side. A healthy first member keeps
// the whole chain where it is, so the cache-friendly steady state is unchanged.
// When every member is cooling the original chain is returned untouched: health
// memory may reorder a chain, never empty or shrink it.
func orderByCooldown(t *cooldownTable, chain []Route) []Route {
	if t == nil || len(chain) < 2 {
		return chain
	}
	ready := make([]Route, 0, len(chain))
	cooled := make([]Route, 0, len(chain))
	for _, route := range chain {
		if t.cooling(route.ID) {
			cooled = append(cooled, route)
		} else {
			ready = append(ready, route)
		}
	}
	if len(ready) == 0 || len(cooled) == 0 {
		return chain
	}
	return append(ready, cooled...)
}

// memberUnhealthy reports whether a failed attempt is evidence about the member
// itself. Only failures another member could plausibly answer differently
// count: transport errors, header timeouts and retryable upstream statuses.
// Client cancellations and request-shaped errors say nothing about health, so a
// user pressing Esc can never demote their own primary member.
func memberUnhealthy(err error) bool {
	if err == nil {
		return false
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		return retryableStatus(upstream.Status)
	}
	var app *AppError
	if errors.As(err, &app) {
		switch app.Code {
		case "CLIENT_CANCELLED", "ADAPTER_NOT_FOUND", "ADAPTER_STREAM_REQUIRED", "INVALID_BASE_URL", "PROXY_LOOP":
			return false
		}
		return retryableStatus(app.Status)
	}
	return true
}
