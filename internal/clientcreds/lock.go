package clientcreds

import (
	"context"
	"strings"

	"github.com/truvity/sluis/internal/port"
)

// KindLease is the lease kind of one client's record: whoever writes an
// existing record (a rotation, an orphaned mark) holds the lease on its client,
// since a secrets adapter's conditional write may be a read and then a write.
// [rails.Leases] is a [Locker].
const KindLease = "client-secret"

// Locker runs fn under an exactly-one-holder lease on a target of a kind, and
// says whether it ran: false with no error when another runner holds the lease.
type Locker interface {
	Do(ctx context.Context, kind, target string, fn func(ctx context.Context)) (ran bool, err error)
}

// withLock runs fn under lock, or directly when there is none.
func withLock(ctx context.Context, lock Locker, kind, target string, fn func(context.Context)) (bool, error) {
	if lock == nil {
		fn(ctx)
		return true, nil
	}
	return lock.Do(ctx, kind, target, fn)
}

// leaseTarget is the client's lease target: the path segment its record uses,
// so an id with a colon or a slash is still one well-formed State key.
func leaseTarget(clientID string) string {
	seg := strings.TrimPrefix(Path(clientID), port.CredentialsPrefix+Kind+"/")
	seg, _, _ = strings.Cut(seg, "/")
	return seg
}
