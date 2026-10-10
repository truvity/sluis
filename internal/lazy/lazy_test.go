package lazy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/lazy"
)

func TestAValueIsReadOnFirstUseAndAgainAfterTheTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	v := lazy.New(time.Minute, func(context.Context) (int, error) { reads++; return reads, nil })
	v.SetClock(func() time.Time { return now })
	if reads != 0 {
		t.Fatalf("New read %d times, want 0", reads)
	}
	for range 3 {
		if got, err := v.Get(context.Background()); err != nil || got != 1 {
			t.Fatalf("Get = %d, %v; want 1", got, err)
		}
	}
	if reads != 1 {
		t.Fatalf("reads within the TTL = %d, want 1", reads)
	}
	now = now.Add(time.Minute)
	if got, _ := v.Get(context.Background()); got != 2 || reads != 2 {
		t.Fatalf("after the TTL: value %d, reads %d; want 2, 2", got, reads)
	}
}

func TestAFailedReadIsNotRememberedOrServedStale(t *testing.T) {
	now := time.Unix(1000, 0)
	fail := false
	v := lazy.New(time.Minute, func(context.Context) (string, error) {
		if fail {
			return "", errors.New("denied")
		}
		return "a", nil
	})
	v.SetClock(func() time.Time { return now })
	if _, err := v.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	now, fail = now.Add(2*time.Minute), true
	if _, err := v.Get(context.Background()); err == nil {
		t.Fatal("a value past its TTL was served though the re-read failed")
	}
	fail = false
	if got, err := v.Get(context.Background()); err != nil || got != "a" {
		t.Fatalf("after recovery: %q, %v", got, err)
	}
}
