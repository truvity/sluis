package observe

import (
	"sync"
	"time"
)

// Repeats keeps a log line that would be the same every pass from being
// written every pass: the first occurrence of a message is let through, the
// same one again only after Every, with how many were held back.
type Repeats struct {
	// Every is how long the same message is held back. Default 30m.
	Every time.Duration
	// Now is the clock, for tests.
	Now func() time.Time

	mu   sync.Mutex
	seen map[string]*repeat
}

type repeat struct {
	last time.Time
	held int
}

// maxRepeatKeys bounds the memory: past it the table starts over, which costs
// one repeated line.
const maxRepeatKeys = 1024

// Allow says whether to log the message now, and how many identical ones were
// held back since it was last logged.
func (r *Repeats) Allow(message string) (log bool, held int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	every := r.Every
	if every <= 0 {
		every = 30 * time.Minute
	}
	if r.seen == nil || len(r.seen) >= maxRepeatKeys {
		r.seen = map[string]*repeat{}
	}
	e, ok := r.seen[message]
	if !ok {
		r.seen[message] = &repeat{last: now}
		return true, 0
	}
	if now.Sub(e.last) < every {
		e.held++
		return false, 0
	}
	held = e.held
	e.last, e.held = now, 0
	return true, held
}

// Forget drops what is held for messages no longer failing, so that a failure
// that returns after a success is logged again at once.
func (r *Repeats) Forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.seen)
}
