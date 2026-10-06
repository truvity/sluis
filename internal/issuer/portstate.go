package issuer

import (
	"context"
	"errors"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// portState is [State] over the ports: what the issuer keeps about a login in
// progress, written through the State and Index ports instead of to a store
// of its own. The keys are the ones the issuer has always used.
type portState struct {
	state port.State
	index port.Index
}

var (
	_ State          = portState{}
	_ versionedState = portState{}
)

// NewPortState returns the issuer's [State] over the ports. A value is
// written with a lifetime always, as the Valkey-backed store refused one
// without; the port refuses it for a key the layout does not mark permanent.
func NewPortState(state port.State, index port.Index) State {
	return portState{state: state, index: index}
}

func (p portState) Get(ctx context.Context, key string) ([]byte, bool, error) {
	record, err := p.state.Get(ctx, key)
	if errors.Is(err, port.ErrNotFound) {
		// Expired or never written, and those are the same answer: there
		// is nothing to continue.
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return record.Value, true, nil
}

func (p portState) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	_, err := p.state.Put(ctx, key, value, ttl)
	return err
}

func (p portState) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	_, err := p.state.Create(ctx, key, value, ttl)
	if errors.Is(err, port.ErrExists) {
		return false, nil
	}
	return err == nil, err
}

func (p portState) Delete(ctx context.Context, key string) error { return p.state.Delete(ctx, key) }

func (p portState) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	return p.index.Add(ctx, key, member, ttl)
}

func (p portState) Remove(ctx context.Context, key, member string) error {
	return p.index.Remove(ctx, key, member)
}

func (p portState) Members(ctx context.Context, key string) ([]string, error) {
	return p.index.Members(ctx, key)
}

// GetVersion implements [versionedState].
func (p portState) GetVersion(ctx context.Context, key string) ([]byte, string, bool, error) {
	record, err := p.state.Get(ctx, key)
	if errors.Is(err, port.ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return record.Value, string(record.Revision), true, nil
}

// Replace implements [versionedState]: an Update at the revision read.
func (p portState) Replace(ctx context.Context, key string, value []byte, ttl time.Duration, version string) error {
	_, err := p.state.Update(ctx, key, value, ttl, port.Revision(version))
	switch {
	case errors.Is(err, port.ErrConflict):
		return errMoved
	case errors.Is(err, port.ErrNotFound):
		return errGone
	}
	return err
}
