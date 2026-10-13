package lambdaapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A collector that cannot take an export (its extension has no token) costs
// one invocation at most flushBudget, and the ones after it nothing, for a
// backoff that grows while it keeps failing.
func TestAFailingFlushIsBoundedAndThenSkipped(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	calls := 0
	var deadline time.Duration
	failing := true
	f := &flusher{now: func() time.Time { return now }, flush: func(ctx context.Context, _ bool) error {
		calls++
		d, _ := ctx.Deadline()
		deadline = time.Until(d)
		if failing {
			return errors.New("503 no access token available")
		}
		return nil
	}}

	f.Flush(context.Background())
	if calls != 1 || deadline > flushBudget {
		t.Fatalf("calls = %d, deadline in %v", calls, deadline)
	}
	// Skipped for the backoff, whatever the invocation's own deadline.
	now = now.Add(flushBackoff - time.Millisecond)
	f.Flush(context.Background())
	if calls != 1 {
		t.Fatalf("a flush inside the backoff ran: %d", calls)
	}
	// Then tried again, and the next backoff is twice as long.
	now = now.Add(time.Millisecond)
	f.Flush(context.Background())
	if calls != 2 {
		t.Fatalf("calls = %d after the backoff", calls)
	}
	now = now.Add(2*flushBackoff - time.Millisecond)
	f.Flush(context.Background())
	if calls != 2 {
		t.Fatalf("the second backoff did not double: %d", calls)
	}
	// It stops growing at flushBackoffMax.
	for range 20 {
		now = now.Add(flushBackoffMax)
		f.Flush(context.Background())
	}
	if got := f.skipTill.Sub(now); got != flushBackoffMax {
		t.Fatalf("backoff = %v, want %v", got, flushBackoffMax)
	}
	// A flush that succeeds resets it.
	failing = false
	now = now.Add(flushBackoffMax)
	f.Flush(context.Background())
	before := calls
	f.Flush(context.Background())
	if calls != before+1 {
		t.Fatal("a flush after a success was skipped")
	}
}

// Traces go out on every invocation; metrics on the first and then once per
// metricsInterval after the last successful metric flush.
func TestMetricsFlushOncePerInterval(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var traces, metrics int
	f := &flusher{now: func() time.Time { return now }, flush: func(_ context.Context, m bool) error {
		traces++
		if m {
			metrics++
		}
		return nil
	}}
	f.Flush(context.Background())
	if traces != 1 || metrics != 1 {
		t.Fatalf("first invocation: traces %d, metrics %d", traces, metrics)
	}
	now = now.Add(metricsInterval - time.Second)
	f.Flush(context.Background())
	if traces != 2 || metrics != 1 {
		t.Fatalf("within the interval: traces %d, metrics %d", traces, metrics)
	}
	now = now.Add(time.Second)
	f.Flush(context.Background())
	if traces != 3 || metrics != 2 {
		t.Fatalf("after the interval: traces %d, metrics %d", traces, metrics)
	}
	// The interval counts from that flush, not from the first.
	now = now.Add(metricsInterval - time.Second)
	f.Flush(context.Background())
	if metrics != 2 {
		t.Fatalf("metrics = %d, the interval restarts at each successful flush", metrics)
	}
}

// A failed flush does not count as a metric flush: the next one carries the
// metrics again once the backoff has passed, however recent the failure.
func TestAFailedMetricFlushIsNotCounted(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var got []bool
	fail := true
	f := &flusher{now: func() time.Time { return now }, flush: func(_ context.Context, m bool) error {
		got = append(got, m)
		if fail {
			return errors.New("503")
		}
		return nil
	}}
	f.Flush(context.Background())
	fail = false
	now = now.Add(flushBackoff)
	f.Flush(context.Background())
	now = now.Add(time.Second)
	f.Flush(context.Background())
	if len(got) != 3 || !got[0] || !got[1] || got[2] {
		t.Fatalf("metrics flags = %v, want [true true false]", got)
	}
}
