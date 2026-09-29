package pipeline

// maxDedupTracking bounds the in-run duplicate-detection set. A full-corpus run
// has tens of millions of reviews, so tracking every key would exhaust memory;
// past the cap the `deduped` counter becomes a lower bound. Correctness does not
// depend on the cap: the deterministic ids plus unique keys still guarantee that
// no duplicate document is written.
const maxDedupTracking = 2_000_000

// dedupTracker counts source records collapsed by a deterministic key within a
// single run (duplicates in the input stream, not rows already in Atlas).
type dedupTracker struct {
	seen  map[string]struct{}
	max   int
	count int64
	full  bool
}

func newDedupTracker(max int) *dedupTracker {
	if max <= 0 {
		max = maxDedupTracking
	}
	return &dedupTracker{seen: make(map[string]struct{}, 4096), max: max}
}

// Observe records a key and reports whether it was already seen in this run.
func (d *dedupTracker) Observe(key string) bool {
	if key == "" || d.full {
		return false
	}
	if _, ok := d.seen[key]; ok {
		d.count++
		return true
	}
	if len(d.seen) >= d.max {
		d.full = true
		return false
	}
	d.seen[key] = struct{}{}
	return false
}
