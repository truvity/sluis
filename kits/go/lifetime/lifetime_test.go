package lifetime_test

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/truvity/sluis/v2/kits/go/lifetime"
)

// The vector file is shared (kits/conformance/token): session and exchange are held to the same rule through this
// package, and signer holds the cap of the same vectors.
const vectorFile = "../../conformance/token/lifetime-vectors.json"

// Vector is one case of the vector file.
type Vector struct {
	Name           string `json:"name"`
	NowFractionMs  int64  `json:"now_fraction_ms"`
	Issuer         int64  `json:"issuer"`
	ClientTTLCap   int64  `json:"client_ttl_cap"`
	ResourceTTLCap int64  `json:"resource_ttl_cap"`
	ChainLeft      *int64 `json:"chain_left"`
	ProofLeft      *int64 `json:"proof_left"`
	Lifetime       int64  `json:"lifetime"`
	Refused        bool   `json:"refused"`
	Cap            int64  `json:"cap"`
}

type vectors struct {
	Maximum int64    `json:"maximum"`
	Expiry  []Vector `json:"expiry"`
}

func load(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Expiry) == 0 {
		t.Fatal("the vector file holds no expiry vector")
	}
	return v
}

func sec(n int64) time.Duration { return time.Duration(n) * time.Second }

// limits is the vector as the kit's input, at the instant now.
func limits(v Vector, now time.Time) lifetime.Limits {
	l := lifetime.Limits{Issuer: sec(v.Issuer), Caps: []time.Duration{sec(v.ClientTTLCap), sec(v.ResourceTTLCap)}}
	if v.ChainLeft != nil {
		l.Ends = append(l.Ends, now.Add(sec(*v.ChainLeft)))
	}
	if v.ProofLeft != nil {
		l.Ends = append(l.Ends, now.Add(sec(*v.ProofLeft)))
	}
	return l
}

// verifies: REQ-TOKEN-036, REQ-TOKEN-037, REQ-TOKEN-049
func TestExpiryMatchesTheVectors(t *testing.T) {
	f := load(t)
	if sec(f.Maximum) != lifetime.Maximum {
		t.Errorf("the vectors' maximum is %ds, the kit's is %s", f.Maximum, lifetime.Maximum)
	}
	seen := map[string]bool{}
	for _, v := range f.Expiry {
		if seen[v.Name] {
			t.Errorf("two vectors are named %q", v.Name)
		}
		seen[v.Name] = true
		now := time.Unix(1_700_000_000, v.NowFractionMs*int64(time.Millisecond))
		l := limits(v, now)
		iat, exp, ok := lifetime.Expiry(now, l)
		if ok == v.Refused {
			t.Errorf("%s: ok = %v, want %v", v.Name, ok, !v.Refused)
			continue
		}
		if iat != now.Unix() {
			t.Errorf("%s: iat = %d, want %d", v.Name, iat, now.Unix())
		}
		if ok && exp-iat != v.Lifetime {
			t.Errorf("%s: exp - iat = %d, want %d", v.Name, exp-iat, v.Lifetime)
		}
		if got := lifetime.Cap(l); got != sec(v.Cap) {
			t.Errorf("%s: cap = %s, want %s", v.Name, got, sec(v.Cap))
		}
	}
}

// verifies: REQ-TOKEN-037, REQ-TOKEN-049
func TestAnyLimitOnlyShortens(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11)) //nolint:gosec // G404: a fixed seed on purpose, so a failure repeats
	now := time.Unix(1_700_000_000, 400*int64(time.Millisecond))
	pick := func() time.Duration { return sec(int64(r.IntN(9000)) - 1000) }
	for range 20000 {
		l := lifetime.Limits{Issuer: pick(), Caps: []time.Duration{pick(), pick()}, Ends: []time.Time{now.Add(pick()), now.Add(pick())}}
		_, exp, ok := lifetime.Expiry(now, l)
		if !ok {
			continue
		}
		// Never longer than the maximum, than a limit that is set, or than an end.
		if exp-now.Unix() > int64(lifetime.Maximum/time.Second) {
			t.Fatalf("%+v: lives past the maximum", l)
		}
		for _, d := range append([]time.Duration{l.Issuer}, l.Caps...) {
			if d > 0 && exp-now.Unix() > int64(d/time.Second) {
				t.Fatalf("%+v: lives past a limit of %s", l, d)
			}
		}
		for _, e := range l.Ends {
			if exp > e.Unix() {
				t.Fatalf("%+v: lives past an end", l)
			}
		}
		// Adding a cap or an end never lengthens the token.
		more := lifetime.Limits{Issuer: l.Issuer, Caps: append([]time.Duration{pick()}, l.Caps...), Ends: l.Ends}
		if _, e2, ok2 := lifetime.Expiry(now, more); ok2 && e2 > exp {
			t.Fatalf("%+v then %+v: a further cap lengthened the token", l, more)
		}
	}
}
