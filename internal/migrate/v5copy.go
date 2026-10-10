package migrate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// This file is `sluis migrate v5 copy` and `verify` (ADR 0072, point 7). Both run
// over the engine of the plan: it reads a layout v4 installation and a layout v5
// one and decides, per item, new, same, different or refused. A copy then writes
// what is new (and, with Overwrite, what is different) and reads both sides
// again to verify; a verify only reads. Neither prints a value, and neither
// deletes anything from the source.
//
// The issuer's records are carried too (issuer.go): the key ring with its wrapped
// entries byte for byte, then, unless --sessions skip, the sessions, refresh
// tokens and the markers of the spent ones, single sign-on records, codes in
// flight, requests and the Index sets, each with what is left of its lifetime.

// VerifyResult is what a copy found when it read both sides again after writing.
type VerifyResult struct {
	PlanCounts
	// Problems are the items that did not match.
	Problems []PlanItem `json:"problems,omitempty"`
	OK       bool       `json:"ok"`
}

// V5Options is how a copy behaves: what the plan is told, and what may be
// written.
type V5Options struct {
	PlanOptions
	// DryRun reads and reports and writes nothing.
	DryRun bool
	// Overwrite replaces what the destination holds with the source's. Without
	// it a destination value that differs stops the run before any write.
	Overwrite bool
	// WritersStopped is the operator's statement that nothing writes to the
	// source. A run that writes refuses without it.
	WritersStopped bool
}

// sameSecret compares two secret values by their SHA-256, so that nothing
// depends on a version, a revision or the way a store lays the value out.
func sameSecret(a, b []byte) bool {
	x, y := sha256.Sum256(a), sha256.Sum256(b)
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// wantedAppRef is the App an organisation names on layout v5.
func (p *planner) wantedAppRef(doc orgDoc) string {
	if doc.Record.AppRef != "" || p.opt.AppRef == nil {
		return doc.Record.AppRef
	}
	return p.opt.AppRef(doc.Record.Org)
}

// sameAppRef is true unless the destination holds an organisation that names
// another App than the source's organisation is to name.
func (p *planner) sameAppRef(src, dst entry) bool {
	s, ok := src.val.(orgDoc)
	if !ok {
		return true
	}
	d, ok := dst.val.(orgDoc)
	return ok && d.Record.AppRef == p.wantedAppRef(s)
}

// collectWrite keeps the write of a record or blob that is new or different.
func (p *planner) collectWrite(out *PlanItem, s step, e entry, old *entry) {
	if !p.collect || (out.Status != PlanNew && out.Status != PlanDifferent) {
		return
	}
	out.do = func(ctx context.Context, _ *PlanItem) error { return s.write(ctx, p.dst, e, old) }
	if s.domain == "github" && s.name == "organisations" {
		// An organisation is written naming an App whose key must be there.
		out.rank = 1
	}
}

// collectSecret keeps the write of a standalone secret that is new or different.
func (p *planner) collectSecret(out *PlanItem, t SecretTarget, value []byte, rev string) {
	if !p.collect {
		return
	}
	out.do = func(ctx context.Context, _ *PlanItem) error { return p.writeV5(ctx, t, value, state.Rev(rev)) }
}

// writeV5 puts a secret at its v5 address, under the revision the plan saw
// ("" for one the destination lacks).
func (p *planner) writeV5(ctx context.Context, t SecretTarget, value []byte, rev state.Rev) error {
	v5 := p.to.Stores.V5
	m := secretstore.Module(t.Module)
	var err error
	switch rest, ok := strings.CutPrefix(t.V5, "external/"+t.Module+"/"); {
	case ok:
		_, err = v5.ExternalStore(m).Put(ctx, rest, value, rev)
	case t.Config:
		_, err = state.NewValue(v5.InternalStore(m), strings.TrimPrefix(t.V5, "internal/"+t.Module+"/"), state.Raw()).Put(ctx, value, rev)
	default:
		_, err = v5.InternalStore(m).Put(ctx, strings.TrimPrefix(t.V5, "internal/"+t.Module+"/"), value, rev)
	}
	return err
}

// recount sets the counts from the items.
func (r *PlanReport) recount() {
	r.Totals = PlanCounts{}
	for i := range r.Modules {
		m := &r.Modules[i]
		m.PlanCounts = PlanCounts{}
		for j := range m.Items {
			m.add(m.Items[j].Status, m.Items[j].count())
			r.Totals.add(m.Items[j].Status, m.Items[j].count())
		}
	}
}

// CopyV5 copies a layout v4 installation to a layout v5 one, idempotently:
// nothing the destination already holds equal is written. It plans first and
// writes nothing when anything is refused or, without Overwrite, different. The
// report is returned with every error that is not a failure to start, so that a
// caller can still show it.
func CopyV5(ctx context.Context, from, to Side, opt V5Options) (*PlanReport, error) {
	// A copy that leaves the issuer out is the live first pass: the fast-changing
	// data is the issuer's, and the final pass with --overwrite catches up the
	// provider records this one copies.
	live := !opt.DryRun && !opt.WritersStopped && contains(opt.Skip, DomainIssuer)
	if !opt.DryRun && !opt.WritersStopped && !live {
		return nil, ErrWritersRunning
	}
	p, err := newPlanner(ctx, from, to, opt.PlanOptions, true)
	if err != nil {
		return nil, err
	}
	if err = p.readAll(ctx); err != nil {
		return nil, err
	}
	report, err := p.finish()
	report.Mode, report.DryRun = "copy", opt.DryRun
	if live {
		report.Live = true
		report.Notes = append(report.Notes, "this pass ran while the source was live and left the issuer out: "+
			"a final pass with --overwrite and --i-have-stopped-writers is required")
	}
	if err == nil && report.Totals.Different > 0 && !opt.Overwrite {
		first := firstWith(report, PlanDifferent)
		err = fmt.Errorf("%w: %d items, the first %s: nothing was written; look at them, then give --overwrite to replace the destination's values",
			ErrConflict, report.Totals.Different, first.From)
	}
	if err == nil && !opt.DryRun {
		err = p.write(ctx, report, opt.Overwrite)
	}
	if err == nil && !opt.DryRun {
		verified, verr := VerifyV5(ctx, from, to, opt.PlanOptions)
		if verified != nil {
			report.Verify = &VerifyResult{PlanCounts: verified.Totals, Problems: problems(verified), OK: verr == nil}
		}
		err = verr
	}
	if err != nil {
		report.Error = err.Error()
	}
	report.OK = err == nil
	return report, err
}

func firstWith(r *PlanReport, status PlanStatus) PlanItem {
	for i := range r.Modules {
		for j := range r.Modules[i].Items {
			if it := &r.Modules[i].Items[j]; it.Status == status {
				return *it
			}
		}
	}
	return PlanItem{}
}

func problems(r *PlanReport) []PlanItem {
	var out []PlanItem
	for i := range r.Modules {
		for j := range r.Modules[i].Items {
			if it := &r.Modules[i].Items[j]; it.Status != PlanSame {
				out = append(out, *it)
			}
		}
	}
	return out
}

// write runs the writes the plan collected, the Apps before the organisations
// that name them. It stops at the first failure: a re-run plans again and
// writes what is still missing.
func (p *planner) write(ctx context.Context, report *PlanReport, overwrite bool) error {
	type ref struct{ m, i int }
	var todo []ref
	for mi := range report.Modules {
		for i := range report.Modules[mi].Items {
			it := &report.Modules[mi].Items[i]
			if it.do != nil && (it.Status == PlanNew || (it.Status == PlanDifferent && overwrite)) {
				todo = append(todo, ref{mi, i})
			}
		}
	}
	at := func(r ref) *PlanItem { return &report.Modules[r.m].Items[r.i] }
	slices.SortStableFunc(todo, func(a, b ref) int { return at(a).rank - at(b).rank })
	var err error
	for _, r := range todo {
		it := at(r)
		if err = it.do(ctx, it); err != nil {
			err = fmt.Errorf("copy %s %s: %w", report.Modules[r.m].Module, it.From, err)
			break
		}
		it.Status = PlanCopied
		if it.expired > 0 {
			// Records whose lifetime ran out first are skipped and counted on
			// a row of their own.
			skipped := *it
			skipped.do, skipped.Status, skipped.Count, skipped.expired = nil, PlanExpired, it.expired, 0
			skipped.Reason = "the lifetime ran out before the record was written"
			if it.expired >= it.count() {
				*it = skipped
			} else {
				it.Count = it.count() - it.expired
				report.Modules[r.m].Items = append(report.Modules[r.m].Items, skipped)
			}
		}
	}
	report.recount()
	return err
}

// VerifyV5 reads both sides and says, per module, whether the destination holds
// what the source does: secrets compared by hash, records by value, and an
// organisation by its record and the App it names (the source has no key of its
// own for it to match). It writes nothing. A source item the destination lacks
// is missing, and one it holds with another value is different; either ends the
// run with ErrMismatch.
func VerifyV5(ctx context.Context, from, to Side, opt PlanOptions) (*PlanReport, error) {
	p, err := newPlanner(ctx, from, to, opt, false)
	if err != nil {
		return nil, err
	}
	if err = p.readAll(ctx); err != nil {
		return nil, err
	}
	report, err := p.finish()
	report.Mode = "verify"
	for mi := range report.Modules {
		for i := range report.Modules[mi].Items {
			switch it := &report.Modules[mi].Items[i]; it.Status {
			case PlanNew:
				it.Status, it.Reason = PlanMissing, "missing on the destination"
			case PlanDifferent:
				it.Reason = "the destination holds another value than the source"
			}
		}
	}
	report.recount()
	if err == nil && report.Totals.Missing+report.Totals.Different > 0 {
		first := firstWith(report, PlanMissing)
		if first.From == "" {
			first = firstWith(report, PlanDifferent)
		}
		err = fmt.Errorf("%w: %d items missing, %d different, the first %s",
			ErrMismatch, report.Totals.Missing, report.Totals.Different, first.From)
	}
	if err != nil && !errors.Is(err, ErrRefused) && !errors.Is(err, ErrUnreadable) {
		report.Error = err.Error()
	}
	report.OK = err == nil
	return report, err
}
