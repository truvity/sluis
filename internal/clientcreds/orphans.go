package clientcreds

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// ReconcileOrphans looks for stored records whose client is not in clients
// (the generated clients of the policy in force) and reports each one once:
// it writes the time into the record (`orphaned`), calls hooks.Orphaned and
// counts it. A record already marked is not reported again, and a record whose
// client returns is cleared by [Reconcile], which keeps the secret as it is.
// It returns the ids it reported now.
//
// An orphan is never deleted here: a person decides (`sluisctl clients purge`).
// A failure is logged and left for the next pass.
func ReconcileOrphans(
	ctx context.Context, clients []string, store port.Secrets, now time.Time, log *slog.Logger, hooks Hooks,
) []string {
	if store == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	paths, err := store.List(ctx, port.CredentialsPrefix+Kind)
	if err != nil {
		if errors.Is(err, port.ErrUnsupported) || errors.Is(err, port.ErrNotFound) {
			return nil
		}
		log.WarnContext(ctx, "the stored client secrets could not be listed, so orphans are not looked for", slog.Any("error", err))
		return nil
	}
	declared := make(map[string]bool, len(clients))
	for _, id := range clients {
		declared[Path(id)] = true
	}
	var reported []string
	for _, p := range paths {
		if declared[p] {
			continue
		}
		id, ok := clientOfPath(p)
		if !ok || Path(id) != p {
			continue
		}
		if markOrphan(ctx, id, store, now, log, hooks) {
			reported = append(reported, id)
		}
	}
	return reported
}

// clientOfPath is the client id a record path belongs to: the inverse of
// [Path].
func clientOfPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, port.CredentialsPrefix+Kind+"/")
	if !ok {
		return "", false
	}
	seg, ok := strings.CutSuffix(rest, "/secret")
	if !ok || seg == "" || strings.Contains(seg, "/") {
		return "", false
	}
	if hexed, ok := strings.CutPrefix(seg, "u-"); ok {
		b, err := hex.DecodeString(hexed)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return seg, true
}

// markOrphan writes the orphaned time into a record that has none, under the
// client's lease, and says whether it did.
func markOrphan(ctx context.Context, id string, store port.Secrets, now time.Time, log *slog.Logger, hooks Hooks) bool {
	marked := false
	ran, err := withLock(ctx, hooks.Lock, KindLease, leaseTarget(id), func(held context.Context) {
		got, gerr := store.Get(held, Path(id))
		if gerr != nil {
			if !errors.Is(gerr, port.ErrNotFound) {
				log.WarnContext(ctx, "an orphaned client secret could not be read", slog.String("client", id), slog.Any("error", gerr))
			}
			return
		}
		rec, derr := DecodeRecord(got.Value)
		if derr != nil {
			log.WarnContext(ctx, "a stored client secret has no client in the policy and cannot be read", slog.String("client", id), slog.Any("error", derr))
			return
		}
		if !rec.Orphaned.IsZero() {
			return
		}
		rec.Orphaned = now.UTC()
		body, eerr := rec.Encode()
		if eerr != nil {
			return
		}
		switch _, perr := store.PutIfVersion(held, Path(id), body, got.Version); {
		case perr == nil:
			marked = true
		case errors.Is(perr, port.ErrConflict):
		default:
			log.WarnContext(ctx, "an orphaned client secret could not be marked", slog.String("client", id), slog.Any("error", fmt.Errorf("write the record: %w", perr)))
		}
	})
	if err != nil {
		log.WarnContext(ctx, "an orphaned client secret could not be marked", slog.String("client", id), slog.Any("error", err))
		return false
	}
	if !ran || !marked {
		return false
	}
	log.WarnContext(ctx, "a stored client secret has no generated client in the policy; it is kept until `sluisctl clients purge` removes it",
		slog.String("client", id))
	countOrphan(ctx)
	if hooks.Orphaned != nil {
		hooks.Orphaned(ctx, id)
	}
	return true
}
