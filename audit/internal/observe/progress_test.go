package observe_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/internal/observe"
)

func TestReadinessFailsOnConsecutiveFailedPasses(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &observe.Progress{FailedPasses: 3, Interval: 5 * time.Minute, Now: func() time.Time { return now }}
	p.Pass(nil)
	for i := range 2 {
		p.Pass(errors.New("boom"))
		if err := p.Ready(context.Background()); err != nil {
			t.Fatalf("after %d failures: %v", i+1, err)
		}
	}
	p.Pass(errors.New("boom"))
	if err := p.Ready(context.Background()); err == nil {
		t.Fatal("three failed passes in a row are still ready")
	}
	p.Pass(nil)
	if err := p.Ready(context.Background()); err != nil {
		t.Fatalf("a success did not clear it: %v", err)
	}
	p.Pass(context.Canceled)
	if err := p.Ready(context.Background()); err != nil {
		t.Fatalf("a cancelled pass counted as a failure: %v", err)
	}
}

func TestReadinessFailsWhenNoPassSucceededForKIntervals(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &observe.Progress{StaleIntervals: 3, Interval: 5 * time.Minute, Now: func() time.Time { return now }}
	if err := p.Ready(context.Background()); err != nil {
		t.Fatalf("a fresh process is not ready: %v", err)
	}
	p.Pass(nil)
	now = now.Add(14 * time.Minute)
	if err := p.Ready(context.Background()); err != nil {
		t.Fatalf("14m with a 15m limit: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := p.Ready(context.Background()); err == nil {
		t.Fatal("16m since a success with a 15m limit is still ready")
	}
	if got := p.SinceSuccess(); got != 16*time.Minute {
		t.Errorf("SinceSuccess = %v", got)
	}
}

func TestARepeatedFailureIsLoggedOnceUntilItClears(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &observe.Repeats{Every: time.Hour, Now: func() time.Time { return now }}
	if log, _ := r.Allow("a"); !log {
		t.Fatal("the first is not logged")
	}
	for range 3 {
		if log, _ := r.Allow("a"); log {
			t.Fatal("a repeat is logged")
		}
	}
	if log, _ := r.Allow("b"); !log {
		t.Fatal("a different error is not logged")
	}
	now = now.Add(time.Hour)
	if log, held := r.Allow("a"); !log || held != 3 {
		t.Fatalf("after the hour: log=%v held=%d", log, held)
	}
	r.Forget()
	if log, _ := r.Allow("a"); !log {
		t.Fatal("after a success the same error is not logged again")
	}
}

func TestTheNextPassComesWhenTheNewestKeyIsOldEnough(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		next time.Time
		want time.Duration
	}{
		"nothing left behind":          {time.Time{}, 5 * time.Minute},
		"ripe in 90s":                  {now.Add(90 * time.Second), 90*time.Second + 100*time.Millisecond},
		"ripe already":                 {now.Add(-time.Second), 100 * time.Millisecond},
		"ripe after the interval":      {now.Add(time.Hour), 5 * time.Minute},
		"ripe exactly at the interval": {now.Add(5 * time.Minute), 5 * time.Minute},
	} {
		if got := observe.WaitFor(5*time.Minute, c.next, now); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
