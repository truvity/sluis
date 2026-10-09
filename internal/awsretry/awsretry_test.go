package awsretry_test

import (
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/internal/awsretry"
)

func TestTheDelaysOfOneCallFitALambdaInit(t *testing.T) {
	if got := awsretry.WorstCase(); got != 3400*time.Millisecond {
		t.Errorf("worst case = %v, want 3.4s", got)
	}
	b := awsretry.Backoff{Rand: func() float64 { return 0.5 }}
	for attempt, want := range map[int]time.Duration{1: 100 * time.Millisecond, 3: 400 * time.Millisecond, 9: 500 * time.Millisecond} {
		if got, _ := b.BackoffDelay(attempt, nil); got != want {
			t.Errorf("attempt %d: %v, want %v", attempt, got, want)
		}
	}
}

func TestAThrottleIsRetriedSixTimesInAll(t *testing.T) {
	r := awsretry.New()
	if got := r.MaxAttempts(); got != 6 {
		t.Errorf("max attempts = %d", got)
	}
	throttled := &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
	if !r.IsErrorRetryable(throttled) {
		t.Error("a ThrottlingException is not retried")
	}
}
