package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/storage/state"
)

// This file is `sluis migrate v5 prune` (ADR 0072, point 7; the v1.75 upgrade
// guide, "After 7 days"). After a migration the layout v4 parameters are still
// there: `copy` never deletes from the source. Prune takes out the ones whose
// layout v5 copy exists and is equal, and nothing else.
//
// The order is fixed, so that a prune cannot be the first look at an installation:
//
//  1. The gate. The same comparison as `verify` runs first. Every source item that
//     has a layout v5 address must be `same` on the destination: one `new`,
//     `different`, `refused` or `missing` item, and nothing is deleted.
//  2. The set. Every parameter below the source's internal and external roots is
//     classified. Only a parameter that has a layout v5 address and whose copy the
//     gate found equal is deleted; the rest is listed as kept, with the reason.
//  3. The confirmation. A real run needs the plan hash a --dry-run printed: it
//     names the parameters to delete and the ones kept, so a changed installation
//     (or another set of flags) needs a new dry run.
//  4. The deletes, ten parameters per request, paced and backed off.
//
// The legacy table's items are not deleted here. The table retires as a whole
// (Compat.RetireLegacyTable of the Pulumi library, which backs it up first), and
// emptying it item by item would only make a restore of that backup the way back
// to nothing; prune counts them so the operator sees what the retirement removes.
//
// Nothing printed or logged is a value: names, counts and versions only.

var (
	// ErrPruneGate is a prune refused because the destination does not hold
	// every migrated item equal.
	ErrPruneGate = errors.New("migrate: prune refused: the destination does not hold every migrated item equal; nothing was deleted")
	// ErrPruneUnconfirmed is a real prune without the plan hash of a dry run, or
	// with the hash of another plan.
	ErrPruneUnconfirmed = errors.New("migrate: prune refused: a real run needs the plan hash a --dry-run printed (--confirm-plan)")
	// ErrPruneNoSecrets is a source that keeps no layout v4 parameters.
	ErrPruneNoSecrets = errors.New("migrate: the source keeps no layout v4 secrets of its own: there is nothing to prune")
)

// The reasons a parameter is kept.
const (
	// KeptNotMigrated is a parameter the migration deliberately does not carry.
	KeptNotMigrated = "not-migrated"
	// KeptPulumi is a parameter the Pulumi library owns (Compat.DropV4).
	KeptPulumi = "pulumi-owned"
	// KeptUnmapped is a name layout v4 never wrote, or that is already a layout
	// v5 name.
	KeptUnmapped = "unmapped"
	// KeptUnverified is a parameter with a layout v5 address whose copy the
	// plan did not find equal (the plan did not look for it).
	KeptUnverified = "unverified"
	// KeptShared is a parameter whose layout v5 address is the same name in a
	// root both sides share: deleting it would delete the copy.
	KeptShared = "shared-address"
)

// pulumiOwned are the layout v4 parameters the Pulumi library creates and
// `Compat.DropV4` deletes.
var pulumiOwned = []string{"internal/config/issuer/state-secret", "internal/config/recovery/password"}

// Prune pacing: SSM's DeleteParameters is throttled like PutParameter, so a
// batch of ten every half second, and a doubling wait when it is throttled.
const (
	// DefaultPrunePause is the wait between two delete requests.
	DefaultPrunePause = 500 * time.Millisecond
	// DefaultPruneBackoff is the first wait after a throttled request.
	DefaultPruneBackoff = time.Second
	// maxPruneBackoff caps the wait; maxPruneRetries is how often one batch is
	// tried again before the run stops.
	maxPruneBackoff = 30 * time.Second
	maxPruneRetries = 8
)

// KeptParam is a parameter prune leaves.
type KeptParam struct {
	Name   string `json:"name"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// PruneOptions is how a prune behaves.
type PruneOptions struct {
	PlanOptions
	// DryRun reads, reports the plan hash and deletes nothing.
	DryRun bool
	// Confirm is the plan hash of a dry run; a real run needs it.
	Confirm string
	// SameRoot is true when both sides keep their parameters below one root: a
	// layout v4 name that is also the layout v5 name is then the copy itself.
	SameRoot bool
	// Pause is the wait between two requests (zero is [DefaultPrunePause]) and
	// Backoff the first wait after a throttled one (zero is [DefaultPruneBackoff]).
	Pause, Backoff time.Duration
	// Sleep waits; nil is a context-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// PruneReport is what a prune found and did: names and counts, never a value.
type PruneReport struct {
	From string `json:"from"`
	To   string `json:"to"`
	// DryRun is a run that deleted nothing.
	DryRun bool `json:"dryRun,omitempty"`
	// PlanHash identifies the parameters to delete and the ones kept; a real
	// run is given it as --confirm-plan.
	PlanHash string `json:"planHash,omitempty"`
	// Verify is what the gate found, by status.
	Verify PlanCounts `json:"verify"`
	// Gate is "passed", or why the run was refused.
	Gate string `json:"gate"`
	// Delete are the parameters to delete (a dry run) or deleted.
	Delete []string `json:"delete"`
	// Deleted counts the parameters a real run removed, in Batches requests.
	Deleted int `json:"deleted,omitempty"`
	Batches int `json:"batches,omitempty"`
	// Kept are the parameters left, with the reason.
	Kept []KeptParam `json:"kept"`
	// LegacyTableItems are the migrated records on the source: the legacy table
	// holds them until it retires; prune does not touch it.
	LegacyTableItems int      `json:"legacyTableItems"`
	Notes            []string `json:"notes,omitempty"`
	OK               bool     `json:"ok"`
	Error            string   `json:"error,omitempty"`
}

// JSON renders the report, indented.
func (r *PruneReport) JSON() []byte {
	raw, _ := json.MarshalIndent(r, "", "  ")
	return append(raw, '\n')
}

// covered are the layout v4 secrets whose copy a plan found equal.
type covered struct {
	names    []string
	families []string
}

// cover notes the credentials of a record the destination holds equal: the
// document a consumer reads when the App is exported, and the family of the
// record's credential when layout v5 carries it.
func (p *planner) cover(out *PlanItem, row stateRow, it item) {
	if out.External != "" {
		p.cov.names = append(p.cov.names, out.External)
	}
	if row.domain == "slack" && row.kind == "catalogue-apps" {
		// The App's bot token is exported at the same address on both layouts.
		p.cov.names = append(p.cov.names, "external/slack/"+it.id)
	}
	if out.Secrets == 0 || row.credential == nil {
		return
	}
	fam := row.credential(it)
	if _, err := MapSecret(fam); err != nil {
		return // an organisation's key is not carried: its parameters are kept
	}
	if prefix, ok := strings.CutSuffix(fam, "<ref>"); ok {
		p.cov.families = append(p.cov.families, prefix)
		return
	}
	p.cov.names = append(p.cov.names, fam)
}

func (c covered) has(name string) bool {
	if slices.Contains(c.names, name) {
		return true
	}
	for _, f := range c.families {
		if rest, ok := strings.CutPrefix(name, f); ok && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}

// PruneV5 runs the gate, decides what to delete and keep, and, unless DryRun,
// deletes it once the plan hash is confirmed. The report is returned with every
// error that is not a failure to start, so that a caller can still show it.
func PruneV5(ctx context.Context, from, to Side, opt PruneOptions) (*PruneReport, error) {
	if from.Stores == nil || from.Stores.V4 == nil {
		return nil, ErrPruneNoSecrets
	}
	verified, verr := VerifyV5(ctx, from, to, opt.PlanOptions)
	if verified == nil {
		return nil, verr
	}
	out := &PruneReport{From: from.Name, To: to.Name, DryRun: opt.DryRun, Verify: verified.Totals, Delete: []string{}, Kept: []KeptParam{}}
	fail := func(err error) (*PruneReport, error) {
		out.Error = err.Error()
		return out, err
	}
	if t := verified.Totals; verr != nil || t.New+t.Different+t.Refused+t.Missing > 0 {
		out.Gate = fmt.Sprintf("refused: %d new, %d different, %d refused, %d missing", t.New, t.Different, t.Refused, t.Missing)
		if first := firstNotSame(verified); first.From != "" {
			out.Gate += fmt.Sprintf("; the first is %s %s (%s)", first.Status, first.From, first.Reason)
		}
		return fail(fmt.Errorf("%w: %s", ErrPruneGate, out.Gate))
	}
	out.Gate = "passed"
	for i := range verified.Modules {
		for _, it := range verified.Modules[i].Items {
			if it.Concern == "state" && it.Status == PlanSame {
				out.LegacyTableItems += it.count()
			}
		}
	}
	names, err := v4Names(ctx, from)
	if err != nil {
		return fail(err)
	}
	for _, name := range names {
		if why, class := keptReason(name, verified.covered, opt.SameRoot, opt.S3Ref); class != "" {
			out.Kept = append(out.Kept, KeptParam{Name: name, Class: class, Reason: why})
		} else {
			out.Delete = append(out.Delete, name)
		}
	}
	out.PlanHash = planHash(out)
	out.Notes = append(out.Notes,
		fmt.Sprintf("%d records of the legacy table are not deleted here: the table retires as a whole (Compat.RetireLegacyTable), after a backup", out.LegacyTableItems))
	if opt.DryRun {
		out.OK = true
		return out, nil
	}
	if opt.Confirm == "" || opt.Confirm != out.PlanHash {
		return fail(fmt.Errorf("%w: this plan is %s; run with --dry-run, read the lists, then give its hash", ErrPruneUnconfirmed, out.PlanHash))
	}
	if err = deleteNames(ctx, from, out, opt); err != nil {
		return fail(err)
	}
	out.OK = true
	return out, nil
}

func firstNotSame(r *PlanReport) PlanItem {
	for i := range r.Modules {
		for _, it := range r.Modules[i].Items {
			if it.Status != PlanSame {
				return it
			}
		}
	}
	return PlanItem{}
}

// v4Names are the full names of every parameter below the source's internal and
// external roots, sorted.
func v4Names(ctx context.Context, from Side) ([]string, error) {
	var names []string
	v4 := from.Stores.V4
	for _, ns := range []struct {
		prefix string
		store  state.Store
	}{{"internal/", v4.Internal.Store()}, {"external/", v4.External.Store()}} {
		keys, err := state.ListAll(ctx, ns.store)
		if err != nil {
			return nil, fmt.Errorf("list the source's %sparameters: %w", ns.prefix, err)
		}
		for _, k := range keys {
			names = append(names, ns.prefix+k)
		}
	}
	slices.Sort(names)
	return names, nil
}

// keptReason says why a parameter is kept; the class is empty for one to delete.
func keptReason(name string, cov covered, sameRoot bool, s3Ref string) (reason, class string) {
	if slices.Contains(pulumiOwned, name) {
		return "created by the Pulumi library; Compat.DropV4 deletes it", KeptPulumi
	}
	t, err := MapSecret(name)
	if name == s3Ref && s3Ref != "" {
		// The blob credential's address is the configuration's, not a fixed name.
		var v5 string
		if v5, err = MapS3Ref(s3Ref); err == nil {
			t = SecretTarget{V5: v5}
		}
	}
	switch {
	case errors.Is(err, ErrNotMigrated):
		return strings.TrimPrefix(err.Error(), ErrNotMigrated.Error()+": "), KeptNotMigrated
	case err != nil:
		return "not a name layout v4 wrote, or a layout v5 name", KeptUnmapped
	case sameRoot && t.V5 == name:
		return "layout v5 has this name too, in the root both sides share", KeptShared
	case !cov.has(name):
		return "no copy on the destination was found equal", KeptUnverified
	}
	return "", ""
}

// planHash identifies what a prune would delete and keep: names only.
func planHash(r *PruneReport) string {
	h := sha256.New()
	for _, n := range r.Delete {
		_, _ = fmt.Fprintf(h, "delete %s\n", n)
	}
	for _, k := range r.Kept {
		_, _ = fmt.Fprintf(h, "keep %s %s\n", k.Class, k.Name)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// deleteNames removes r.Delete in batches of [state.MaxDeleteBatch], one store
// at a time, and records one audit line per batch: the names and the count.
func deleteNames(ctx context.Context, from Side, r *PruneReport, opt PruneOptions) error {
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sleep := opt.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	pause, backoff := opt.Pause, opt.Backoff
	if pause == 0 {
		pause = DefaultPrunePause
	}
	if backoff == 0 {
		backoff = DefaultPruneBackoff
	}
	v4 := from.Stores.V4
	for _, ns := range []struct {
		prefix string
		store  state.Store
	}{{"internal/", v4.Internal.Store()}, {"external/", v4.External.Store()}} {
		var keys []string
		for _, n := range r.Delete {
			if k, ok := strings.CutPrefix(n, ns.prefix); ok {
				keys = append(keys, k)
			}
		}
		for chunk := range slices.Chunk(keys, state.MaxDeleteBatch) {
			n, err := deleteBatch(ctx, ns.store, chunk, backoff, sleep)
			if err != nil {
				return fmt.Errorf("delete %s%s and %d more: %w (deleted so far: %d; run a new --dry-run)", ns.prefix, chunk[0], len(chunk)-1, err, r.Deleted)
			}
			r.Deleted += n
			r.Batches++
			log.InfoContext(ctx, "audit: migrate v5 prune deleted layout v4 parameters",
				slog.String("event", "migrate.v5.prune.batch"), slog.Int("batch", r.Batches), slog.Int("count", n),
				slog.String("namespace", strings.TrimSuffix(ns.prefix, "/")), slog.Any("keys", chunk))
			if err = sleep(ctx, pause); err != nil {
				return err
			}
		}
	}
	log.InfoContext(ctx, "audit: migrate v5 prune finished", slog.String("event", "migrate.v5.prune"),
		slog.Int("deleted", r.Deleted), slog.Int("batches", r.Batches), slog.Int("kept", len(r.Kept)), slog.String("plan", r.PlanHash))
	return nil
}

// deleteBatch deletes up to ten keys, waiting and trying again while the backend
// throttles. A key that is already gone is not an error.
func deleteBatch(ctx context.Context, s state.Store, keys []string, backoff time.Duration, sleep func(context.Context, time.Duration) error) (int, error) {
	wait := backoff
	for try := 0; ; try++ {
		n, err := deleteOnce(ctx, s, keys)
		if err == nil || !throttled(err) || try >= maxPruneRetries {
			return n, err
		}
		if err = sleep(ctx, wait); err != nil {
			return 0, err
		}
		wait = min(wait*2, maxPruneBackoff)
	}
}

func deleteOnce(ctx context.Context, s state.Store, keys []string) (int, error) {
	if bd, ok := s.(state.BatchDeleter); ok {
		return bd.DeleteMany(ctx, keys)
	}
	n := 0
	for _, k := range keys {
		switch err := s.Delete(ctx, k); {
		case err == nil:
			n++
		case errors.Is(err, state.ErrNotFound):
		default:
			return n, err
		}
	}
	return n, nil
}

// throttled is true for the errors AWS answers a request over its rate with.
func throttled(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "ThrottlingException", "Throttling", "TooManyUpdates", "RequestLimitExceeded", "ThrottledException":
		return true
	}
	return false
}
