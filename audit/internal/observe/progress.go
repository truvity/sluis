package observe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Progress is what readiness and the stall metrics know of the indexer's
// passes: how many failed in a row and when one last succeeded. A pass that
// fails leaves the pod running, so without this a stalled index looks like a
// healthy process.
type Progress struct {
	// FailedPasses is how many passes in a row may fail before Ready fails.
	// Default 3.
	FailedPasses int
	// StaleIntervals is how many Intervals may pass without a successful pass
	// before Ready fails. Default 3.
	StaleIntervals int
	// Interval is the poll the indexer runs at.
	Interval time.Duration
	// Now is the clock, for tests.
	Now func() time.Time

	mu      sync.Mutex
	started time.Time
	success time.Time
	failed  int
}

func (p *Progress) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Pass records the outcome of one pass. A pass cut short by the process
// stopping is not a failure.
func (p *Progress) Pass(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started.IsZero() {
		p.started = p.now()
	}
	switch {
	case err == nil:
		p.success = p.now()
		p.failed = 0
	case errors.Is(err, context.Canceled):
	default:
		p.failed++
	}
}

// SinceSuccess is how long since a pass last succeeded, or since the process
// started when none has.
func (p *Progress) SinceSuccess() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started.IsZero() {
		p.started = p.now()
	}
	at := p.success
	if at.IsZero() {
		at = p.started
	}
	return max(p.now().Sub(at), 0)
}

// Ready is nil while the indexer is making progress. It is a readiness check,
// not a liveness one: restarting does not mend a catalogue the indexer cannot
// read, and a restart loop would hide the reason.
func (p *Progress) Ready(context.Context) error {
	n, k := p.FailedPasses, p.StaleIntervals
	if n <= 0 {
		n = 3
	}
	if k <= 0 {
		k = 3
	}
	p.mu.Lock()
	failed := p.failed
	p.mu.Unlock()
	if failed >= n {
		return fmt.Errorf("the last %d indexing passes failed", failed)
	}
	if limit := time.Duration(k) * p.interval(); p.SinceSuccess() > limit {
		return fmt.Errorf("no indexing pass has succeeded for %s (limit %s)", p.SinceSuccess().Round(time.Second), limit)
	}
	return nil
}

func (p *Progress) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}
