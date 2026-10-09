package clientcreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secrets"
)

// Outcome is what [Reconcile] did for one client.
type Outcome string

// The outcomes. Part of the audit and metric vocabulary: keep the spellings.
const (
	// OutcomeCreated: a new secret was generated and stored.
	OutcomeCreated Outcome = "created"
	// OutcomeAdopted: the input `clients/<id>/secret` was copied into the
	// record, so a client that already uses it keeps working.
	OutcomeAdopted Outcome = "adopted"
	// OutcomeRestored: a record that had been reported orphaned is back in use
	// (its client is in the policy again); the record is kept as it is and
	// only the orphaned mark was cleared.
	OutcomeRestored Outcome = "restored"
	// OutcomeExisting: a record was already there; nothing was written.
	OutcomeExisting Outcome = "existing"
	// OutcomeConflict: another writer created the record between the read and
	// our create; ours was discarded and theirs is the record.
	OutcomeConflict Outcome = "conflict"
	// OutcomeUnsupported: the secrets adapter cannot create only-if-absent,
	// so no secret was made (fail closed).
	OutcomeUnsupported Outcome = "unsupported"
	// OutcomeFailed: any other error; the next pass tries again.
	OutcomeFailed Outcome = "failed"
)

// Input delivers the installation's input secrets by name: [secrets.Source]
// is one.
type Input interface {
	Get(ctx context.Context, name string) (string, error)
}

// Hooks are told what [Reconcile] did, for the audit trail part of the
// installation. Every field may be nil. A hook receives the client id and the
// outcome and never a value.
type Hooks struct {
	// Lock serialises a write to an existing record (the orphaned mark) with
	// a rotation of the same client. A secrets adapter whose conditional
	// write is a read and then a write (ssm) needs it. Nil runs unserialised,
	// which is right only for one process.
	Lock Locker
	// Orphaned is called once for each record newly found with no generated
	// client in the policy. See [ReconcileOrphans].
	Orphaned func(ctx context.Context, clientID string)
	// Outcome is called once per client per pass. err is set for
	// OutcomeUnsupported and OutcomeFailed.
	Outcome func(ctx context.Context, clientID string, outcome Outcome, err error)
}

// Result is what one pass did, by client.
type Result struct {
	Outcomes map[string]Outcome
}

// Failed counts the clients whose secret could not be settled.
func (r Result) Failed() int {
	n := 0
	for _, o := range r.Outcomes {
		if o == OutcomeFailed || o == OutcomeUnsupported {
			n++
		}
	}
	return n
}

// Reconcile makes sure every client in clients (the ids of those declared
// `secret: {generate: true}`) has a record in store.
//
// A record that exists is left alone, always. A missing one is made from the
// input `clients/<id>/secret` when the installation delivers one (adopt: the
// secret a client already uses stays valid), otherwise from a newly generated
// value, and written create-only. A lost race re-reads and continues; the
// write is never retried and never overwrites.
//
// A client's failure does not stop the others and is not returned: it is
// logged, counted and given to hooks, and the next pass tries again. Nothing
// here logs a value.
func Reconcile(
	ctx context.Context, clients []string, store port.Secrets, input Input,
	now time.Time, log *slog.Logger, hooks Hooks,
) Result {
	if log == nil {
		log = slog.Default()
	}
	res := Result{Outcomes: make(map[string]Outcome, len(clients))}
	for _, id := range clients {
		outcome, err := reconcileOne(ctx, id, store, input, now, hooks.Lock)
		res.Outcomes[id] = outcome
		countReconcile(ctx, outcome)
		switch outcome {
		case OutcomeUnsupported, OutcomeFailed:
			log.WarnContext(ctx, "a generated client secret could not be settled; the client keeps the input secret, if any, until the next pass",
				slog.String("client", id), slog.String("outcome", string(outcome)), slog.String("error", safeError(err)))
		case OutcomeCreated, OutcomeAdopted, OutcomeConflict, OutcomeRestored:
			log.InfoContext(ctx, "a generated client secret was settled", slog.String("client", id), slog.String("outcome", string(outcome)))
		default:
			log.DebugContext(ctx, "a generated client secret is in place", slog.String("client", id), slog.String("outcome", string(outcome)))
		}
		if hooks.Outcome != nil {
			hooks.Outcome(ctx, id, outcome, err)
		}
	}
	return res
}

func reconcileOne(ctx context.Context, id string, store port.Secrets, input Input, now time.Time, lock Locker) (Outcome, error) {
	if store == nil {
		return OutcomeUnsupported, errors.New("no secrets adapter is configured")
	}
	path := Path(id)
	got, err := store.Get(ctx, path)
	switch {
	case err == nil:
		rec, derr := DecodeRecord(got.Value)
		if derr != nil {
			// A record nobody can read is not a record in place: reported,
			// never replaced, and the client is not served from the input.
			return OutcomeFailed, fmt.Errorf("the stored record is unreadable: %w", derr)
		}
		if !rec.Orphaned.IsZero() {
			return restore(ctx, id, store, lock)
		}
		return OutcomeExisting, nil
	case !errors.Is(err, port.ErrNotFound):
		return OutcomeFailed, fmt.Errorf("read the record: %w", err)
	}

	value, adopted, err := initialValue(ctx, id, input)
	if err != nil {
		return OutcomeFailed, err
	}
	rec := Record{V: RecordVersion, Current: value, Created: now.UTC()}
	body, err := rec.Encode()
	if err != nil {
		return OutcomeFailed, err
	}
	_, err = store.PutIfVersion(ctx, path, body, "")
	switch {
	case err == nil:
		if adopted {
			return OutcomeAdopted, nil
		}
		return OutcomeCreated, nil
	case errors.Is(err, port.ErrConflict):
		// Somebody else made it first. Theirs is the record; look, and do not
		// write again.
		if _, gerr := store.Get(ctx, path); gerr != nil {
			return OutcomeFailed, fmt.Errorf("read the record another writer made: %w", gerr)
		}
		return OutcomeConflict, nil
	case errors.Is(err, port.ErrUnsupported):
		return OutcomeUnsupported, fmt.Errorf("the secrets adapter cannot create a secret only if absent: %w", err)
	default:
		return OutcomeFailed, fmt.Errorf("write the record: %w", err)
	}
}

// initialValue is the input secret when the installation delivers one, else a
// new one. An input that is there but cannot be read is an error, not a reason
// to generate: that would replace a secret a client is using with one it has
// never seen.
func initialValue(ctx context.Context, id string, input Input) (value string, adopted bool, err error) {
	if input != nil {
		name := secrets.ClientSecret(id)
		if secrets.Check(name) == nil && !strings.ContainsAny(id, `/\`) {
			v, gerr := input.Get(ctx, name)
			switch {
			case gerr == nil && v != "":
				return v, true, nil
			case gerr == nil || errors.Is(gerr, secrets.ErrNotFound):
			default:
				return "", false, fmt.Errorf("read the input secret %s: %w", name, gerr)
			}
		}
	}
	v, err := Generate()
	return v, false, err
}

// safeError is the error as a string for a log line. Errors here are built
// from adapter errors, which never hold a value (the port's contract), but a
// record's JSON is not an error.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// restore clears the orphaned mark of a record whose client is declared
// again, and nothing else: the secret it holds is the one the client's
// relying parties already use.
func restore(ctx context.Context, id string, store port.Secrets, lock Locker) (Outcome, error) {
	outcome, err := OutcomeExisting, error(nil)
	ran, lerr := withLock(ctx, lock, KindLease, leaseTarget(id), func(held context.Context) {
		got, gerr := store.Get(held, Path(id))
		if gerr != nil {
			outcome, err = OutcomeFailed, fmt.Errorf("read the record: %w", gerr)
			return
		}
		rec, derr := DecodeRecord(got.Value)
		if derr != nil || rec.Orphaned.IsZero() {
			return
		}
		rec.Orphaned = time.Time{}
		body, eerr := rec.Encode()
		if eerr != nil {
			outcome, err = OutcomeFailed, eerr
			return
		}
		switch _, perr := store.PutIfVersion(held, Path(id), body, got.Version); {
		case perr == nil:
			outcome = OutcomeRestored
		case errors.Is(perr, port.ErrConflict):
			// Changed under us: the next pass looks again.
		default:
			outcome, err = OutcomeFailed, fmt.Errorf("write the record: %w", perr)
		}
	})
	if lerr != nil {
		return OutcomeFailed, fmt.Errorf("take the lease: %w", lerr)
	}
	if !ran {
		return OutcomeExisting, nil
	}
	return outcome, err
}
