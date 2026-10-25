package minter

import (
	"context"
	"time"
)

// TestOnlyTrack records an id as if it had been minted as the preset's stored
// token (with no caller).
func (m *Minter) TestOnlyTrack(ctx context.Context, preset, id string, expires time.Time) error {
	return m.track(ctx, preset, id, "", expires)
}
