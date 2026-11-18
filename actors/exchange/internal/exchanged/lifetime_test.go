package exchanged

import (
	"math/rand/v2"
	"testing"
	"time"
)

// verifies: REQ-EXCH-004
func TestTheLifetimeOfAnExchangeIsNeverLongerThanTheSubjectTokenOrAnyCap(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16)) //nolint:gosec // G404: a fixed seed on purpose, so a failure repeats
	now := time.Unix(1_800_000_000, 0)
	secs := func(limit int) time.Duration {
		return time.Duration(r.IntN(limit)) * time.Second
	}
	for range 50000 {
		issuer := secs(7200)
		clientCap := time.Duration(0)
		if r.IntN(2) == 0 {
			clientCap = secs(7200)
		}
		subjectExp := now.Add(secs(7200) + time.Duration(r.IntN(1000))*time.Millisecond - time.Second)
		iat, exp, ok := lifetime(now, issuer, clientCap, subjectExp)

		// The longest it may be: the earliest of the issuer's, the client's cap, the built-in hour and the end of the
		// subject token, in whole seconds.
		limit := hardCap
		if issuer > 0 {
			limit = min(issuer, hardCap) // an issuer's lifetime of zero is not set (kits/go/lifetime)
		}
		if clientCap > 0 {
			limit = min(limit, clientCap)
		}
		limitSecs := min(int64(limit/time.Second), subjectExp.Unix()-now.Unix())
		if ok != (limitSecs >= 1) {
			t.Fatalf("issuer %v cap %v subject %v: ok = %v, but the longest life is %d s", issuer, clientCap, subjectExp.Sub(now), ok, limitSecs)
		}
		if !ok {
			continue
		}
		if iat != now.Unix() || exp-iat != limitSecs {
			t.Fatalf("issuer %v cap %v subject %v: life %d s, want %d s", issuer, clientCap, subjectExp.Sub(now), exp-iat, limitSecs)
		}
		if exp > subjectExp.Unix() {
			t.Fatalf("the token outlives the subject token: exp %d, subject %d", exp, subjectExp.Unix())
		}
		if exp-iat > int64(hardCap/time.Second) {
			t.Fatalf("the token outlives the built-in maximum: %d s", exp-iat)
		}
	}
}
