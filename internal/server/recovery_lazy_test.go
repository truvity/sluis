package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A lazily opened password recovery reads nothing until a proof arrives, reuses
// the password within the TTL, and reads it again for the proof after: a
// rotated password then takes effect without a restart, and the old one stops.
func TestALazyPasswordRecoveryReadsOnTheFirstProofAndAgainAfterTheTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_000_000, 0)
	reads, password := 0, "first-recovery-password"
	r := NewLazyPasswordRecovery(time.Minute, func(context.Context) (string, error) {
		reads++
		return password, nil
	})
	r.now = func() time.Time { return now }
	if reads != 0 {
		t.Fatalf("opening read %d times", reads)
	}
	if _, err := r.Verify(ctx, "first-recovery-password"); err != nil {
		t.Fatalf("the password was refused: %v", err)
	}
	if _, err := r.Verify(ctx, "wrong"); !errors.Is(err, ErrRecoveryRefused) {
		t.Errorf("a wrong proof = %v", err)
	}
	if reads != 1 {
		t.Fatalf("two proofs within the TTL made %d reads, want 1", reads)
	}

	password = "second-recovery-password"
	now = now.Add(time.Minute)
	if _, err := r.Verify(ctx, "second-recovery-password"); err != nil {
		t.Errorf("the rotated password was refused after the TTL: %v", err)
	}
	if _, err := r.Verify(ctx, "first-recovery-password"); !errors.Is(err, ErrRecoveryRefused) {
		t.Errorf("the old password after the TTL = %v", err)
	}
	if reads != 2 {
		t.Errorf("reads = %d, want 2", reads)
	}
}

func TestALazyPasswordRecoveryFailsClosedOnAnUnreadableOrEmptyPassword(t *testing.T) {
	ctx := context.Background()
	for name, load := range map[string]func(context.Context) (string, error){
		"unreadable": func(context.Context) (string, error) { return "", errors.New("denied") },
		"empty":      func(context.Context) (string, error) { return "", nil },
	} {
		r := NewLazyPasswordRecovery(0, load)
		if _, err := r.Verify(ctx, ""); err == nil || errors.Is(err, ErrRecoveryRefused) {
			t.Errorf("%s: a proof = %v, want an error naming the secret", name, err)
		}
	}
}
