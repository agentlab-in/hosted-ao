package reachability

import (
	"sync"
	"time"
)

// maxTrackedKeys bounds the memory one window may hold. Unlike the device
// flow's per-account limiter, the keys here are caller-supplied (a client
// address and a target hostname), so the map cannot be left to grow.
//
// ponytail: expired entries are swept only when the map reaches this cap, so
// the common path stays O(1) and a burst of fresh keys costs one full sweep
// rather than one per request. If the sweep does not free anything, a key that
// is not already tracked is refused rather than admitted: a flood of fresh
// keys degrades into refusing new callers, which resolves on its own within
// one window, instead of into unbounded memory. If the control plane is ever
// replicated, move this to a shared store: each replica otherwise grants the
// full allowance.
const maxTrackedKeys = 4096

// window is a fixed-window rate limiter keyed by an arbitrary string, in the
// shape of the device flow's attemptLimiter but with expiry sweeping and a
// bound on how many keys it will track.
type window struct {
	mu    sync.Mutex
	limit int
	per   time.Duration
	seen  map[string][]time.Time
	clock func() time.Time
}

func newWindow(limit int, per time.Duration) *window {
	return &window{limit: limit, per: per, seen: make(map[string][]time.Time), clock: time.Now}
}

// allow records one request against key and reports whether it is within the
// allowance. A refused request is not recorded, so the allowance itself bounds
// how fast the map can grow.
func (w *window) allow(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.clock()
	cutoff := now.Add(-w.per)

	times, tracked := w.seen[key]
	times = live(times, cutoff)
	if len(times) >= w.limit {
		w.seen[key] = times
		return false
	}
	if !tracked && len(w.seen) >= maxTrackedKeys {
		w.sweep(cutoff)
		if len(w.seen) >= maxTrackedKeys {
			return false
		}
	}
	w.seen[key] = append(times, now)
	return true
}

// sweep drops every key whose requests have all aged out.
func (w *window) sweep(cutoff time.Time) {
	for k, times := range w.seen {
		if kept := live(times, cutoff); len(kept) == 0 {
			delete(w.seen, k)
		} else {
			w.seen[k] = kept
		}
	}
}

// live returns the timestamps still inside the window, reusing the backing
// array.
func live(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
