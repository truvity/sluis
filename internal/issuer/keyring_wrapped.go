package issuer

import (
	"context"
	"crypto"
	"time"
)

// wrapHooks is what turns a [KeyRing] into one that generates its own keys:
// the `kms-wrapped` signing adapter (see [WrappedSigning]).
type wrapHooks struct {
	ws          *WrappedSigning
	rotateEvery time.Duration
	interval    time.Duration
}

// UseWrapped makes the ring generate and unwrap its keys with ws. Call it once,
// before the ring serves; a ring without it never generates anything.
func (r *KeyRing) UseWrapped(ws *WrappedSigning) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrap = &wrapHooks{ws: ws, rotateEvery: ws.cfg.RotateEvery, interval: ws.cfg.Interval}
}

// Maintain is the whole of automatic rotation, and cheap to call on every
// request: it does nothing unless [wrapHooks.interval] has passed since it last
// ran. It learns the keys other replicas recorded, unwraps the one that is now
// active if this process cannot sign with it yet, and, when the newest key is
// older than rotateEvery, generates the next one under the lease.
//
// A failure is logged and never returned: a KMS that is briefly unreachable
// must not stop this process signing with the key it already holds, and the
// next interval tries again. The lease makes a lost race harmless: whoever
// holds it generates, and a replica that finds it held does nothing.
func (r *KeyRing) Maintain(ctx context.Context) {
	r.mu.Lock()
	h := r.wrap
	if h == nil || r.now().Sub(r.lastMaintain) < h.interval {
		r.mu.Unlock()
		return
	}
	r.lastMaintain = r.now()
	r.mu.Unlock()

	// The caller's request may end first; a key half made is wasted KMS work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*wrapGenerateTimeout)
	defer cancel()

	r.sync(ctx, h)
	r.warnStale(ctx, h)
	if !r.generationDue(h) {
		return
	}
	ran, err := h.ws.lease(ctx, r.alg, func(lctx context.Context) error {
		// What another replica generated while this one waited for the lease.
		r.sync(lctx, h)
		if !r.generationDue(h) {
			return nil
		}
		return r.generate(lctx, h)
	})
	switch {
	case err != nil:
		r.log.WarnContext(ctx, "could not generate the next signing key; will try again",
			"algorithm", string(r.alg), "error", err)
	case !ran:
		r.log.DebugContext(ctx, "another replica holds the key-generation lease", "algorithm", string(r.alg))
	}
}

// sync learns what the shared state knows and makes sure the active key has a
// signer, unwrapping it when it was recorded by another replica.
func (r *KeyRing) sync(ctx context.Context, h *wrapHooks) {
	r.mu.Lock()
	r.absorb(ctx)
	r.recompute(ctx, r.now())
	var (
		id      string
		pub     crypto.PublicKey
		wrapped []byte
	)
	if e, ok := r.entries[r.activeID]; ok && e.signer == nil && len(e.Wrapped) > 0 {
		id, pub, wrapped = e.ID, e.JWK.Key, e.Wrapped
	}
	r.mu.Unlock()
	if wrapped == nil {
		return
	}
	key, err := h.ws.unwrap(ctx, r.alg, id, pub, wrapped)
	if err != nil {
		r.log.ErrorContext(ctx, "could not unwrap the active signing key; signing continues with the newest key this "+
			"replica can sign with", "kid", id, "algorithm", string(r.alg), "error", err)
		return
	}
	r.mu.Lock()
	if e, ok := r.entries[id]; ok && e.signer == nil {
		e.signer = key
	}
	r.recompute(ctx, r.now())
	r.mu.Unlock()
}

// generationDue is whether the newest wrapped key is older than rotateEvery, or
// there is none. Keys read from elsewhere do not count: they cannot sign here.
func (r *KeyRing) generationDue(h *wrapHooks) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest *ringEntry
	for _, e := range r.entries {
		if len(e.Wrapped) > 0 && (newest == nil || e.SeenAt.After(newest.SeenAt)) {
			newest = e
		}
	}
	return newest == nil || !r.now().Before(newest.SeenAt.Add(h.rotateEvery))
}

// generate makes the next key and records it. It runs under the lease.
func (r *KeyRing) generate(ctx context.Context, h *wrapHooks) error {
	r.mu.Lock()
	anyWrapped := false
	for _, e := range r.entries {
		if len(e.Wrapped) > 0 {
			anyWrapped = true
		}
	}
	r.mu.Unlock()

	key, err := h.ws.generate(ctx, r.alg)
	if err != nil {
		return err
	}
	// With no wrapped key to wait behind the new one signs at once; otherwise it
	// is published for the pre-publish period first (see [KeyRing.record]).
	key.activateNow = !anyWrapped
	return r.Observe(ctx, key)
}

// UseWrapped attaches ws to every ring, and so makes the key set rotate itself.
func (k *KeyRings) UseWrapped(ws *WrappedSigning) {
	for _, ring := range k.rings {
		ring.UseWrapped(ws)
	}
}

// Maintain runs [KeyRing.Maintain] on every ring.
func (k *KeyRings) Maintain(ctx context.Context) {
	for _, ring := range k.rings {
		ring.Maintain(ctx)
	}
}

// staleFactor is how many rotation periods old the active wrapped key may get
// before this logs an error: rotation that fails is otherwise only a warning
// per attempt, and the key just keeps signing.
const staleFactor = 1.5

// warnStale logs at ERROR, at most hourly, while the active wrapped key is older
// than staleFactor times rotateEvery. Alert on the same condition from the
// metric (access_issuer.signing_key.active_since_timestamp, docs/deployment/aws.md).
func (r *KeyRing) warnStale(ctx context.Context, h *wrapHooks) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[r.activeID]
	if !ok || len(e.Wrapped) == 0 {
		return
	}
	now := r.now()
	age := now.Sub(e.ActivateAt)
	if float64(age) <= staleFactor*float64(h.rotateEvery) || now.Sub(r.lastStaleLog) < time.Hour {
		return
	}
	r.lastStaleLog = now
	r.log.ErrorContext(ctx, "the active signing key is far older than the rotation period: rotation is failing "+
		"(KMS or State errors, or the key-generation lease is stuck); it keeps signing meanwhile",
		"kid", e.ID, "algorithm", string(r.alg), "age", age.Round(time.Minute), "rotateEvery", h.rotateEvery)
}
