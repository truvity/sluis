package kms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/truvity/sluis/storage/state"
)

// FromState adapts a state.Store to a WrappedStore: each wrapped key is one
// JSON object, {"wrapped": "<base64>"}, under the id as its key. Give it a
// store (or Child) of its own, so the ids cannot collide with other values.
//
// The store it returns is an ErasableStore: a tombstone is one more object,
// {"destroyed": true}, under "destroyed/" + id, and erasing deletes the
// wrapped key with all its versions (state.Store.Delete).
func FromState(s state.Store) WrappedStore { return stateStore{s} }

type stateStore struct{ s state.Store }

type wrappedDoc struct {
	Wrapped []byte `json:"wrapped"` // encoded as base64 by encoding/json
}

func (w stateStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	it, err := w.s.Get(ctx, id)
	if errors.Is(err, state.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return decode(it.Value)
}

func (w stateStore) PutIfAbsent(ctx context.Context, id string, wrapped []byte) ([]byte, error) {
	doc, err := json.Marshal(wrappedDoc{Wrapped: wrapped})
	if err != nil {
		return nil, err
	}
	_, err = w.s.Put(ctx, id, doc, "")
	switch {
	case err == nil:
		return wrapped, nil
	case errors.Is(err, state.ErrConflict):
		it, err := w.s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		stored, _, err := decode(it.Value)
		return stored, err
	}
	return nil, err
}

func decode(b []byte) ([]byte, bool, error) {
	var d wrappedDoc
	if err := json.Unmarshal(b, &d); err != nil || len(d.Wrapped) == 0 {
		return nil, false, fmt.Errorf("kms: a stored wrapped key is not {\"wrapped\": ...}: %v", err)
	}
	return d.Wrapped, true, nil
}

const tombstonePrefix = "destroyed/"

type tombstoneDoc struct {
	Destroyed bool `json:"destroyed"`
}

var _ ErasableStore = stateStore{}

func (w stateStore) Tombstone(ctx context.Context, id string) error {
	doc, _ := json.Marshal(tombstoneDoc{Destroyed: true})
	if _, err := w.s.Put(ctx, tombstonePrefix+id, doc, ""); err != nil && !errors.Is(err, state.ErrConflict) {
		return err // a conflict is a tombstone already there
	}
	if err := w.s.Delete(ctx, id); err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

func (w stateStore) Tombstoned(ctx context.Context, id string) (bool, error) {
	_, err := w.s.Get(ctx, tombstonePrefix+id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, state.ErrNotFound):
		return false, nil
	}
	return false, err
}
