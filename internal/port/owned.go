package port

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotOwner is a write of a key that belongs to another module than the one
// the store serves. Nothing is written.
var ErrNotOwner = errors.New("port: the key belongs to another module")

// StateReader is the read half of [State]: what a peer view offers. A module
// holds one of another module's table only where a named cross-grant says so
// (ADR 0072, decision 1), and the type keeps it from writing: there is no
// method to call.
type StateReader interface {
	Get(ctx context.Context, key string) (Record, error)
	List(ctx context.Context, prefix, page string, limit int) (Page, error)
}

var _ StateReader = State(nil)

// ReadOnly is the peer view of a State: Get and List and nothing else. The
// result does not carry the underlying State, so it cannot be type-asserted
// back to one that writes.
func ReadOnly(s State) StateReader { return peerView{s} }

type peerView struct{ s State }

func (p peerView) Get(ctx context.Context, key string) (Record, error) { return p.s.Get(ctx, key) }

func (p peerView) List(ctx context.Context, prefix, page string, limit int) (Page, error) {
	return p.s.List(ctx, prefix, page, limit)
}

// Peer is the read-only view of another module's State, if the set holds one.
func (s Set) Peer(m Module) (StateReader, bool) {
	r, ok := s.Peers[m]
	return r, ok && r != nil
}

// Owned returns s as the State of module m: reads pass through, and a write of
// a key that layout 5 locates in another module returns [ErrNotOwner] without
// reaching s. A key of no module (leases, notifications, gates of no one
// module: [Address5.Shared]) is the table's own, so it passes. A key of a
// backend with no module is refused as [ErrUnsupported].
func Owned(m Module, s State) State {
	o := owned{m, s}
	rev, peeks := s.(RevisionPeeker)
	exp, exports := s.(StateExporter)
	switch {
	case peeks && exports:
		return &ownedBoth{o, rev, exp}
	case peeks:
		return &ownedPeeker{o, rev}
	case exports:
		return &ownedExporter{o, exp}
	}
	return &o
}

type owned struct {
	module Module
	State
}

// The optional capabilities of the inner State are read-only, so they pass
// through unchecked.
type ownedPeeker struct {
	owned
	RevisionPeeker
}

type ownedExporter struct {
	owned
	StateExporter
}

type ownedBoth struct {
	owned
	RevisionPeeker
	StateExporter
}

// check refuses a key the module does not own.
func (o *owned) check(key string) error {
	a, err := Locate5(key)
	if err != nil {
		return err
	}
	if a.Module != "" && a.Module != o.module {
		return fmt.Errorf("%w: %q is the %s module's, this is %s", ErrNotOwner, key, a.Module, o.module)
	}
	return nil
}

func (o *owned) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (Revision, error) {
	if err := o.check(key); err != nil {
		return "", err
	}
	return o.State.Put(ctx, key, value, ttl)
}

func (o *owned) Create(ctx context.Context, key string, value []byte, ttl time.Duration) (Revision, error) {
	if err := o.check(key); err != nil {
		return "", err
	}
	return o.State.Create(ctx, key, value, ttl)
}

func (o *owned) Update(ctx context.Context, key string, value []byte, ttl time.Duration, rev Revision) (Revision, error) {
	if err := o.check(key); err != nil {
		return "", err
	}
	return o.State.Update(ctx, key, value, ttl, rev)
}

func (o *owned) Delete(ctx context.Context, key string) error {
	if err := o.check(key); err != nil {
		return err
	}
	return o.State.Delete(ctx, key)
}

func (o *owned) DeleteIfRevision(ctx context.Context, key string, rev Revision) error {
	if err := o.check(key); err != nil {
		return err
	}
	return o.State.DeleteIfRevision(ctx, key, rev)
}
