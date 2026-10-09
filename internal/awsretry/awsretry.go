// Package awsretry is the retryer of the SSM clients sluis reads its
// configuration and secrets with.
//
// The SDK's default is three attempts with up to twenty seconds between them.
// That is too few for a herd of cold starts, which SSM answers with
// ThrottlingException until the herd thins out, and too slow for a Lambda
// init, which has ten seconds in all. This retryer makes more attempts, sooner
// and with full jitter, so a herd spreads out instead of retrying in step, and
// its delays add up to under four seconds.
//
// It is the SDK's standard mode, not its adaptive one. Adaptive mode also
// rate-limits the client after a throttle, and a client that makes a handful
// of calls at a cold start learns a rate so low that, in the test against a
// throttling fake, the start took nine seconds instead of one.
package awsretry

import (
	"math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
)

const (
	// MaxAttempts is how many times one call is tried in all.
	MaxAttempts = 6
	// BaseDelay is the ceiling of the first delay; each one after it doubles,
	// up to MaxDelay.
	BaseDelay = 100 * time.Millisecond
	// MaxDelay is the ceiling of any one delay.
	MaxDelay = time.Second
)

// New is a retryer for one client. Its retry quota (the SDK's token bucket) is
// the client's.
func New() aws.Retryer {
	return retry.NewStandard(func(so *retry.StandardOptions) {
		so.MaxAttempts = MaxAttempts
		so.MaxBackoff = MaxDelay
		so.Backoff = Backoff{}
	})
}

// Backoff is full jitter: the delay before attempt n+1 is uniform in
// [0, min(MaxDelay, BaseDelay*2^n)). The worst case of the five delays
// MaxAttempts allows is 0.2+0.4+0.8+1+1 = 3.4 seconds.
type Backoff struct {
	// Rand is uniform in [0, 1); nil is math/rand.
	Rand func() float64
}

// BackoffDelay implements [retry.BackoffDelayer].
func (b Backoff) BackoffDelay(attempt int, _ error) (time.Duration, error) {
	return b.ceiling(attempt, b.rand()), nil
}

func (b Backoff) rand() float64 {
	if b.Rand != nil {
		return b.Rand()
	}
	return rand.Float64() //nolint:gosec // jitter, not a secret
}

func (Backoff) ceiling(attempt int, r float64) time.Duration {
	ceiling := MaxDelay
	if attempt < 10 {
		ceiling = min(MaxDelay, BaseDelay<<attempt)
	}
	return time.Duration(r * float64(ceiling))
}

// WorstCase is the most the delays of one call add up to.
func WorstCase() time.Duration {
	var total time.Duration
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		total += Backoff{}.ceiling(attempt, 1)
	}
	return total
}
