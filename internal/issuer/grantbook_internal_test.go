package issuer

import (
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"
)

// fakeExchange is a request the book can tell from another: the book keys on
// the request's identity and reads nothing from it.
type fakeExchange struct {
	op.TokenExchangeRequest
	n int
}

func TestTheGrantBookDoesNotGrowWithEveryExchange(t *testing.T) {
	t.Parallel()
	now := time.Now()
	book := &grantBook{now: func() time.Time { return now }}

	// An exchange every few seconds for a day, none of them read again: what
	// a refused or abandoned request leaves behind.
	const exchanges = 24 * 3600 / 5
	peak := 0
	for i := range exchanges {
		book.Store(&fakeExchange{n: i}, Grant{Subject: "job"})
		now = now.Add(5 * time.Second)
		peak = max(peak, book.Len())
	}

	if limit := int(grantTTL/(5*time.Second))*2 + 2; peak > limit {
		t.Errorf("the book held %d decisions at its peak after %d exchanges, want at most %d", peak, exchanges, limit)
	}
}

func TestTheGrantBookAnswersWithinItsTimeAndNotAfter(t *testing.T) {
	t.Parallel()
	now := time.Now()
	book := &grantBook{now: func() time.Time { return now }}
	request := &fakeExchange{}
	book.Store(request, Grant{Subject: "job"})

	now = now.Add(grantTTL - time.Second)
	if grant, ok := book.Load(request); !ok || grant.Subject != "job" {
		t.Errorf("inside the time: %+v %v", grant, ok)
	}
	if _, ok := book.Load(&fakeExchange{n: 1}); ok {
		t.Error("a request nobody decided was answered")
	}

	now = now.Add(2 * time.Second)
	if _, ok := book.Load(request); ok {
		t.Error("a decision past its time was answered")
	}
}
