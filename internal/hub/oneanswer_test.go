package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/hub"
)

type countingResolver struct{ calls int }

func (c *countingResolver) ResolveUser(_ context.Context, email string, _ *time.Duration) (hub.UserResult, error) {
	c.calls++
	return hub.UserResult{Email: email, Found: true, Authoritative: true, Groups: []string{"a@north.example", "b@north.example"}}, nil
}

// Within a request that asks for one answer, a person is resolved once
// however many times they are asked about; outside one, and for a question
// that demands freshness, every call is passed on.
func TestOneAnswerPerRequest(t *testing.T) {
	t.Parallel()
	inner := &countingResolver{}
	once := hub.OneAnswerPerRequest(inner)
	if hub.OneAnswerPerRequest(once) != once {
		t.Error("wrapping twice wrapped twice")
	}

	ctx := hub.WithOneAnswer(t.Context())
	first, _ := once.ResolveUser(ctx, "ada@north.example", nil)
	// A caller narrowing what it was handed must not narrow it for the
	// next caller in the same request.
	first.Groups = first.Groups[:0]
	second, _ := once.ResolveUser(ctx, "ADA@north.example ", nil)
	if inner.calls != 1 {
		t.Errorf("asked %d times within one request, want once", inner.calls)
	}
	if len(second.Groups) != 2 {
		t.Errorf("the second answer has %v, want both groups", second.Groups)
	}

	if _, _ = once.ResolveUser(ctx, "grace@north.example", nil); inner.calls != 2 {
		t.Errorf("another person was answered from the first one's memory (%d calls)", inner.calls)
	}
	fresh := time.Minute
	if _, _ = once.ResolveUser(ctx, "ada@north.example", &fresh); inner.calls != 3 {
		t.Errorf("a question demanding freshness was answered from memory (%d calls)", inner.calls)
	}
	if _, _ = once.ResolveUser(t.Context(), "ada@north.example", nil); inner.calls != 4 {
		t.Errorf("a request without one-answer memory was answered from another's (%d calls)", inner.calls)
	}
	if _, _ = once.ResolveUser(hub.WithOneAnswer(t.Context()), "ada@north.example", nil); inner.calls != 5 {
		t.Errorf("the next request was answered from the last one's memory (%d calls)", inner.calls)
	}
}
