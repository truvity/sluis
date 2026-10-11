package port

import (
	"context"
	"time"
)

// ReadOnlyState is s with every write refused as [ErrNotOwner], for a process
// that reads another module's table and must not write it even if a broader
// grant would let it.
func ReadOnlyState(s State) State { return readOnlyState{s} }

type readOnlyState struct{ State }

func (readOnlyState) Put(context.Context, string, []byte, time.Duration) (Revision, error) {
	return "", ErrNotOwner
}

func (readOnlyState) Create(context.Context, string, []byte, time.Duration) (Revision, error) {
	return "", ErrNotOwner
}

func (readOnlyState) Update(context.Context, string, []byte, time.Duration, Revision) (Revision, error) {
	return "", ErrNotOwner
}
func (readOnlyState) Delete(context.Context, string) error                     { return ErrNotOwner }
func (readOnlyState) DeleteIfRevision(context.Context, string, Revision) error { return ErrNotOwner }

// ReadOnlyBlob is b with every write and delete refused as [ErrNotOwner]. The
// optional capabilities of b are not carried, so none can write around it.
func ReadOnlyBlob(b Blob) Blob { return readOnlyBlob{b} }

type readOnlyBlob struct{ Blob }

func (readOnlyBlob) Write(context.Context, string, []byte) (string, error) { return "", ErrNotOwner }
func (readOnlyBlob) WriteIfVersion(context.Context, string, []byte, string) (string, error) {
	return "", ErrNotOwner
}
func (readOnlyBlob) Delete(context.Context, string) error { return ErrNotOwner }
