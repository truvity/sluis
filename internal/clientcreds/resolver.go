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

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	rec   *Record // nil: no record
	until time.Time
}

// NewResolver returns a Resolver. store may be nil (no records: the input
// only), and so may input.
func NewResolver(store port.Secrets, input Input, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{store: store, input: input, log: log, now: time.Now, cache: map[string]cached{}}
}

// Forget drops what is cached for a client, so the next [Resolver.Resolve]
// reads the record again. A rotation calls it after it writes.
func (r *Resolver) Forget(clientID string) {
	r.mu.Lock()
	delete(r.cache, clientID)
	r.mu.Unlock()
}

// Resolve implements [Lookup]. The record, when there is one; otherwise the
// input `clients/<id>/secret`, for every client, so that a generated client's
// first deploy is served by the input until the record exists. The input is
// read live, as ever; only the record is cached.
func (r *Resolver) Resolve(ctx context.Context, clientID string) (Secrets, bool) {
	if clientID == "" || strings.ContainsAny(clientID, `/\`) {
		return Secrets{}, false
	}
	if rec := r.record(ctx, clientID); rec != nil {
		return Secrets{Current: rec.Current, Previous: rec.Previous, PreviousValidUntil: rec.PreviousValidUntil}, true
	}
	if r.input == nil {
		return Secrets{}, false
	}
	name := secrets.ClientSecret(clientID)
	if secrets.Check(name) != nil {
		return Secrets{}, false
	}
	value, err := r.input.Get(ctx, name)
	if err != nil {
		if !errors.Is(err, secrets.ErrNotFound) {
			r.log.WarnContext(ctx, "a declared client's secret could not be read; that client cannot authenticate",
				"client", clientID, "error", err)
		}
		return Secrets{}, false
	}
	return Secrets{Current: value}, true
}

// record is the client's record, from the cache while it is fresh. A failed
// read serves the last good one, or none (and the input then answers).
func (r *Resolver) record(ctx context.Context, clientID string) *Record {
	if r.store == nil {
		return nil
	}
	now := r.now()
	r.mu.Lock()
	c, ok := r.cache[clientID]
	r.mu.Unlock()
	if ok && now.Before(c.until) {
		return c.rec
	}
	got, err := r.store.Get(ctx, Path(clientID))
	var rec *Record
	switch {
	case err == nil:
		decoded, derr := DecodeRecord(got.Value)
		if derr != nil {
			r.log.WarnContext(ctx, "a client's secret record cannot be read", "client", clientID, "error", derr)
			return c.rec
		}
		rec = &decoded
	case errors.Is(err, port.ErrNotFound):
	default:
		r.log.WarnContext(ctx, "a client's secret record could not be fetched", "client", clientID, "error", err)
		return c.rec
	}
	r.mu.Lock()
	r.cache[clientID] = cached{rec: rec, until: now.Add(CacheTTL)}
	r.mu.Unlock()
	return rec
}
