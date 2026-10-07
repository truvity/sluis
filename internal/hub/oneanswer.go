package hub

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// UserResolver is [Hub.ResolveUser] alone: what both of the hub's in-process
// consumers ask, the issuer (through internal/hublocal) and the console's
// authorizer (internal/access).
type UserResolver interface {
	ResolveUser(ctx context.Context, email string, maxAge *time.Duration) (UserResult, error)
}

// OneAnswerPerRequest asks inner about any one address once within a
// request that carries [WithOneAnswer], and answers every later question
// about it in that request with the first answer. Outside such a request
// it passes every call through unchanged.
//
// It exists for the console. Every request it serves is identified twice
// over the same person: the issuer decides whether the browser's sign-in
// still stands -- the same directory check its silent sign-in makes --
// and then the console's authorizer resolves the role. Each of those was
// a full resolution: a workspace listing and a snapshot read. One answer
// costs half, and is also the only way the two cannot disagree -- a
// snapshot replaced between them could otherwise admit a sign-in the
// authorizer then reads as suspended, or the other way round.
//
// Only a question that leaves freshness to the hub (a nil maxAge) is
// shared: one that demands a fresher answer is asking for exactly what a
// remembered one is not.
func OneAnswerPerRequest(inner UserResolver) UserResolver {
	if inner == nil {
		return nil
	}
	if _, already := inner.(oneAnswer); already {
		return inner
	}
	return oneAnswer{inner: inner}
}

type oneAnswer struct{ inner UserResolver }

func (o oneAnswer) ResolveUser(ctx context.Context, email string, maxAge *time.Duration) (UserResult, error) {
	memo := answersFrom(ctx)
	if memo == nil || maxAge != nil {
		return o.inner.ResolveUser(ctx, email, maxAge)
	}
	key := strings.ToLower(strings.TrimSpace(email))
	if got, ok := memo.get(key); ok {
		return got.res, got.err
	}
	res, err := o.inner.ResolveUser(ctx, email, maxAge)
	memo.put(key, res, err)
	return res, err
}

// answers is one request's memory of what the directory said.
type answers struct {
	mu   sync.Mutex
	done map[string]answered
}

type answered struct {
	res UserResult
	err error
}

type answersKey struct{}

// WithOneAnswer returns ctx carrying an empty memory, so that every
// [OneAnswerPerRequest] resolver it reaches asks about any one address
// once. It lives as long as ctx and never longer: the next request asks
// afresh.
func WithOneAnswer(ctx context.Context) context.Context {
	return context.WithValue(ctx, answersKey{}, &answers{done: map[string]answered{}})
}

func answersFrom(ctx context.Context) *answers {
	memo, _ := ctx.Value(answersKey{}).(*answers)
	return memo
}

func (a *answers) get(email string) (answered, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	got, ok := a.done[email]
	// A copy of the groups: a caller that narrows the slice it was handed
	// must not narrow it for the next caller in the same request.
	got.res.Groups = slices.Clone(got.res.Groups)
	return got, ok
}

func (a *answers) put(email string, res UserResult, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res.Groups = slices.Clone(res.Groups)
	a.done[email] = answered{res: res, err: err}
}
