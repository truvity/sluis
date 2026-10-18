package sinkserver

import (
	"context"
	"maps"
	"slices"

	"github.com/truvity/sluis/audit/sdk/auth"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
)

// SourceBound holds each caller to the source its workload is listed for.
//
// A verified caller is a service account, and the workloads file says which
// source that account speaks for. Without this a caller whose token verifies may
// write a record of any source whose catalogue the installation knows, and the
// trail then says a workload did what another workload's catalogue describes.
//
// A record whose source is not the caller's is refused, by id, and the rest of
// the batch goes on. A caller the list does not name speaks for no source, so
// everything it sends is refused: the registry reads such a caller as nobody
// too. A context with no principal (an in-process call, or a trial install that
// accepts anonymous writes, which has no list) is passed through untouched.
type SourceBound struct {
	// To is the next hop.
	To sink.Sink
	// Workloads maps a verified principal to its source.
	Workloads auth.Workloads
	// OnRefused is told once per batch that had a refusal, with the caller's
	// subject, how many records were refused, and the sources they claimed
	// (distinct, sorted, at most five). Once per batch, not per record: a caller
	// that sends a thousand records of the wrong source writes one line, not a
	// thousand. Nil says nothing.
	OnRefused func(ctx context.Context, subject string, refused int, sources []string)
}

// Guarantees implements sink.Guarantor: a hop that only refuses gives what its
// successor gives.
func (b *SourceBound) Guarantees() sink.Durability { return sink.Guarantees(b.To) }

// Write implements sink.Sink.
func (b *SourceBound) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || len(b.Workloads) == 0 {
		return b.To.Write(ctx, req)
	}
	want := b.Workloads.SourceOf(p)
	var refused []sink.Rejection
	claimed := map[string]bool{}
	kept := make([]*record.Record, 0, len(req.Records))
	for _, r := range req.Records {
		if want != "" && r.GetSource() == want {
			kept = append(kept, r)
			continue
		}
		reason := "the record's source is not the one this caller is listed for"
		if want == "" {
			reason = "the caller is not listed as speaking for any source"
		}
		refused = append(refused, sink.Rejection{ID: r.GetId(), Reason: reason})
		claimed[r.GetSource()] = true
	}
	if len(refused) > 0 && b.OnRefused != nil {
		sources := slices.Sorted(maps.Keys(claimed))
		b.OnRefused(ctx, p.Subject, len(refused), sources[:min(len(sources), 5)])
	}
	if len(refused) == 0 {
		return b.To.Write(ctx, req)
	}
	if len(kept) == 0 {
		return &sink.Result{Rejected: refused, Durability: sink.Guarantees(b.To)}, nil
	}
	next := *req
	next.Records = kept
	res, err := b.To.Write(ctx, &next)
	if err != nil {
		return nil, err
	}
	out := *res
	out.Rejected = append(append([]sink.Rejection(nil), res.Rejected...), refused...)
	return &out, nil
}
