// Package throttle slows an endpoint that answers anyone and costs something
// to answer: a recovery proof, which spends memory on a digest or a call to
// the cluster's API server.
//
// It counts refusals per SOURCE and, beside that, across all sources. The
// per-source count is what protects the endpoint from one noisy caller; the
// global ceiling is what protects the process from many, and it is set well
// above what one source may do. A single global counter alone -- the first
// version of this -- is a lock somebody else holds: ten wrong proofs from
// anywhere shut the door on the one operator who has the right one, which for
// a break-glass path is the attack.
package throttle

import (
	"sync"
	"time"
)

// Defaults for a recovery endpoint: five refusals a source may spend in a
// window, and a ceiling across every source at twenty times that.
const (
	DefaultPerSource = 5
	DefaultGlobal    = 100
	DefaultWindow    = time.Minute
	// DefaultSources is how many sources are told apart. Beyond it, new
	// sources share one count, so a caller who can vary its address cannot
	// grow the process.
	DefaultSources = 4096
)

// overflow is the count the sources beyond the table share.
const overflow = "\x00overflow"

// Limiter is a fixed-window count of refusals. The zero value is not ready:
// use [New].
type Limiter struct {
	perSource int
	global    int
	window    time.Duration
	sources   int
	now       func() time.Time

	mu      sync.Mutex
	bySrc   map[string]*count
	overall count
}

type count struct {
	n     int
	start time.Time
}

// New returns a Limiter allowing perSource refusals from one source, and
// global across all, per window, telling apart at most sources sources.
// A non-positive value takes the default. now is the clock; nil is time.Now.
func New(perSource, global int, window time.Duration, sources int, now func() time.Time) *Limiter {
	if perSource <= 0 {
		perSource = DefaultPerSource
	}
	if global <= 0 {
		global = DefaultGlobal
	}
	if window <= 0 {
		window = DefaultWindow
	}
	if sources <= 0 {
		sources = DefaultSources
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{perSource: perSource, global: global, window: window, sources: sources, now: now, bySrc: map[string]*count{}}
}

// Default returns a Limiter with every default.
func Default() *Limiter { return New(0, 0, 0, 0, nil) }

// expired reports whether a count's window is over.
func (l *Limiter) expired(c *count, now time.Time) bool {
	return c.n == 0 || !now.Before(c.start.Add(l.window))
}

// Blocked reports whether source may not try: it has spent its refusals for
// the window, or every source together have.
func (l *Limiter) Blocked(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if !l.expired(&l.overall, now) && l.overall.n >= l.global {
		return true
	}
	if c, ok := l.bySrc[l.key(source)]; ok && !l.expired(c, now) && c.n >= l.perSource {
		return true
	}
	return false
}

// Refused records that source's attempt was refused.
func (l *Limiter) Refused(source string) {
	l.refuse(source, true)
}

// RefusedBySource records a refusal against source alone, and not against the
// ceiling across every source: for an attempt that cost nothing to refuse, which
// must not be a way to use up everybody's share.
func (l *Limiter) RefusedBySource(source string) {
	l.refuse(source, false)
}

func (l *Limiter) refuse(source string, overall bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if overall {
		bump(&l.overall, now, l.expired(&l.overall, now))
	}

	key := source
	c, ok := l.bySrc[key]
	if !ok {
		// A source the table does not hold: it gets a place if there is
		// one once what has expired is dropped, and the shared count if not.
		l.sweep(now)
		if len(l.bySrc) >= l.sources {
			key = overflow
			c = l.bySrc[key]
		}
		if c == nil {
			c = &count{}
			l.bySrc[key] = c
		}
	}
	bump(c, now, l.expired(c, now))
}

// Accepted clears source's refusals: the right proof from a source that
// fumbled one is not a source to go on slowing. The global count is not
// cleared; it measures work, not people.
func (l *Limiter) Accepted(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.bySrc, l.key(source))
}

// Len is how many sources are held; for tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.bySrc)
}

// key is the table's name for a source: itself while the table has room or
// already holds it, and the shared overflow otherwise.
func (l *Limiter) key(source string) string {
	if _, ok := l.bySrc[source]; ok || len(l.bySrc) < l.sources {
		return source
	}
	return overflow
}

// sweep drops the sources whose window is over.
func (l *Limiter) sweep(now time.Time) {
	if len(l.bySrc) < l.sources {
		return
	}
	for key, c := range l.bySrc {
		if l.expired(c, now) {
			delete(l.bySrc, key)
		}
	}
}

func bump(c *count, now time.Time, restart bool) {
	if restart {
		c.n, c.start = 0, now
	}
	c.n++
}
