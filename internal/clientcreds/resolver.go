package clientcreds

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secrets"
)

// The slots an authentication can match.
const (
	SlotCurrent  = "current"
	SlotPrevious = "previous"
	SlotNone     = "none"
)

// CacheTTL is how long a read of a record is reused.
const CacheTTL = 30 * time.Second

// StaleFor is how long past its last successful read a record is still served
// when the store cannot be read: a replica whose store fails does not accept a
// rotated-away secret for longer than this.
const StaleFor = 5 * time.Minute

// retryAfterFailure is how soon a failed read is tried again.
const retryAfterFailure = 5 * time.Second

// Secrets is what a client may authenticate with.
type Secrets struct {
	Current string
	// Previous is the secret Current replaced; empty when there is none.
	// It is accepted only while now is before PreviousValidUntil.
	Previous           string
	PreviousValidUntil time.Time
}

// Lookup is what the token endpoint asks for a client's secrets.
type Lookup interface {
	// Resolve returns the client's secrets, false when it has none.
	Resolve(ctx context.Context, clientID string) (Secrets, bool)
}

// Resolver is the [Lookup] over the credential record and the input secret.
type Resolver struct {
	store port.Secrets
	input Input
	log   *slog.Logger
	now   func() time.Time

	// generated says whether the policy in force declares the client
	// `secret: {generate: true}`. Nil treats every client as generated.
	generated func(clientID string) bool

	mu    sync.Mutex
	cache map[string]cached
	// inputs are the input secrets read, for [CacheTTL]: a source that does
	// not cache (layout v5's) would otherwise be read on every request.
	inputs map[string]inputRead
}

type inputRead struct {
	secrets Secrets
	at      time.Time
}

type cached struct {
	rec   *Record // nil: no record
	until time.Time
	// good is when the record was last read successfully.
	good time.Time
	// unavailable: the last read failed and nothing recent could be served.
	unavailable bool
}

// UseGenerated tells the Resolver which clients are generated, from the policy
// in force when asked. Only a generated client's record is consulted; any
// other client has its input secret and nothing else, so a client returned to
// `secret: <name>` is served by the input at once.
func (r *Resolver) UseGenerated(generated func(clientID string) bool) { r.generated = generated }

func (r *Resolver) isGenerated(id string) bool { return r.generated == nil || r.generated(id) }

// NewResolver returns a Resolver. store may be nil (no records: the input
// only), and so may input.
func NewResolver(store port.Secrets, input Input, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{store: store, input: input, log: log, now: time.Now, cache: map[string]cached{}, inputs: map[string]inputRead{}}
}

// Forget drops what is cached for a client, so the next [Resolver.Resolve]
// reads the record again. A rotation calls it after it writes.
func (r *Resolver) Forget(clientID string) {
	r.mu.Lock()
	delete(r.cache, clientID)
	delete(r.inputs, clientID)
	r.mu.Unlock()
}

// RereadFloor is how recent a read must be for [Resolver.Reread] to reuse it:
// a flood of wrong guesses costs the store one read per client per floor, not
// one per guess.
const RereadFloor = 5 * time.Second

// Reread is what the token check asks before it refuses a secret: the
// client's secrets read again from the store, because this replica may have
// cached them before a rotation. A read younger than [RereadFloor] is reused.
func (r *Resolver) Reread(ctx context.Context, clientID string) (Secrets, bool) {
	r.mu.Lock()
	c, ok := r.cache[clientID]
	fresh := ok && !c.unavailable && r.now().Sub(c.good) < RereadFloor
	if !fresh {
		delete(r.cache, clientID)
	}
	if in, read := r.inputs[clientID]; read && r.now().Sub(in.at) >= RereadFloor {
		delete(r.inputs, clientID)
	}
	r.mu.Unlock()
	return r.Resolve(ctx, clientID)
}

// Resolve implements [Lookup].
//
// A generated client is served by its record. The input
// `clients/<id>/secret` serves it only while the record is definitively absent
// (the store says not found), so a first deploy never refuses a valid client
// before the secret exists. A record that is corrupt, or a store that cannot
// be read with nothing recent to serve, authenticates nobody: failing closed.
// A client with a named input secret is served by the input alone.
func (r *Resolver) Resolve(ctx context.Context, clientID string) (Secrets, bool) {
	if clientID == "" || strings.ContainsAny(clientID, `/\`) {
		return Secrets{}, false
	}
	if r.isGenerated(clientID) {
		rec, st := r.record(ctx, clientID)
		switch st {
		case recordFound:
			return Secrets{Current: rec.Current, Previous: rec.Previous, PreviousValidUntil: rec.PreviousValidUntil}, true
		case recordUnavailable:
			return Secrets{}, false
		}
	}
	return r.fromInput(ctx, clientID)
}

func (r *Resolver) fromInput(ctx context.Context, clientID string) (Secrets, bool) {
	if r.input == nil {
		return Secrets{}, false
	}
	name := secrets.ClientSecret(clientID)
	if secrets.Check(name) != nil {
		return Secrets{}, false
	}
	now := r.now()
	r.mu.Lock()
	in, read := r.inputs[clientID]
	r.mu.Unlock()
	if read && now.Sub(in.at) < CacheTTL {
		return in.secrets, true
	}
	value, err := r.input.Get(ctx, name)
	if err != nil {
		if !errors.Is(err, secrets.ErrNotFound) {
			r.log.WarnContext(ctx, "a declared client's secret could not be read; that client cannot authenticate",
				slog.String("client", clientID), slog.Any("error", err))
		}
		return Secrets{}, false
	}
	out := Secrets{Current: value}
	r.mu.Lock()
	r.inputs[clientID] = inputRead{secrets: out, at: now}
	r.mu.Unlock()
	return out, true
}

type recordState int

const (
	// recordAbsent: the store says there is no record.
	recordAbsent recordState = iota
	recordFound
	// recordUnavailable: the record could not be read or understood, and
	// nothing recent is cached to serve instead.
	recordUnavailable
)

// record is the client's record, from the cache while it is fresh. A failed or
// corrupt read serves the last good record for at most [StaleFor] after it was
// read, and otherwise nothing.
func (r *Resolver) record(ctx context.Context, clientID string) (*Record, recordState) {
	if r.store == nil {
		return nil, recordAbsent
	}
	now := r.now()
	r.mu.Lock()
	c, ok := r.cache[clientID]
	r.mu.Unlock()
	if ok && now.Before(c.until) {
		if c.unavailable {
			return nil, recordUnavailable
		}
		return c.rec, stateOf(c.rec)
	}
	got, err := r.store.Get(ctx, Path(clientID))
	var rec *Record
	switch {
	case err == nil:
		decoded, derr := DecodeRecord(got.Value)
		if derr != nil {
			r.log.WarnContext(ctx, "a client's secret record cannot be read", slog.String("client", clientID), slog.Any("error", derr))
			return r.stale(clientID, c, ok, now)
		}
		rec = &decoded
	case errors.Is(err, port.ErrNotFound):
	default:
		r.log.WarnContext(ctx, "a client's secret record could not be fetched", slog.String("client", clientID), slog.Any("error", err))
		return r.stale(clientID, c, ok, now)
	}
	r.mu.Lock()
	r.cache[clientID] = cached{rec: rec, until: now.Add(CacheTTL), good: now}
	r.mu.Unlock()
	return rec, stateOf(rec)
}

func stateOf(rec *Record) recordState {
	if rec == nil {
		return recordAbsent
	}
	return recordFound
}

// stale answers a failed read: the last good record while it is recent enough,
// otherwise nothing. The failure is retried soon, not on every request.
func (r *Resolver) stale(clientID string, c cached, ok bool, now time.Time) (*Record, recordState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ok || now.Sub(c.good) > StaleFor {
		// Unavailable is cached too, so a record that stays unreadable costs
		// one read per retry window, not one per request. It still never
		// falls to the input.
		r.cache[clientID] = cached{until: now.Add(retryAfterFailure), good: c.good, unavailable: true}
		return nil, recordUnavailable
	}
	c.until = now.Add(retryAfterFailure)
	r.cache[clientID] = c
	return c.rec, stateOf(c.rec)
}
