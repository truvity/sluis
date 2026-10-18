package sinkserver_test

import (
	"context"
	"testing"

	"github.com/truvity/sluis/audit/sdk/auth"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
	"github.com/truvity/sluis/audit/sinkserver"
)

func bound(next sink.Sink, refused *[]string) *sinkserver.SourceBound {
	return &sinkserver.SourceBound{
		To: next,
		Workloads: auth.Workloads{
			{Subject: "system:serviceaccount:shop:api", Source: "shop"},
			{Subject: "system:serviceaccount:wallet:api", Source: "wallet"},
		},
		OnRefused: func(_ context.Context, subject, source string) { *refused = append(*refused, subject+"->"+source) },
	}
}

func asCaller(subject string) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Subject: subject})
}

// A workload writes the source it is listed for. The record of another source in
// the same batch is refused by id and the rest goes on.
func TestSourceBoundRefusesARecordOfAnotherSource(t *testing.T) {
	next := &sink.Memory{}
	var refused []string
	b := bound(next, &refused)
	own := &record.Record{Id: record.NewID(), Source: "shop", Action: "shop.order.viewed"}
	other := &record.Record{Id: record.NewID(), Source: "wallet", Action: "wallet.card.added"}

	res, err := b.Write(asCaller("system:serviceaccount:shop:api"), &sink.Request{Records: []*record.Record{own, other}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Len() != 1 {
		t.Fatalf("forwarded %d records, want only the caller's own", next.Len())
	}
	if len(res.Rejected) != 1 || res.Rejected[0].ID != other.GetId() {
		t.Fatalf("rejected = %+v, want the record of the other source", res.Rejected)
	}
	if len(refused) != 1 || refused[0] != "system:serviceaccount:shop:api->wallet" {
		t.Errorf("OnRefused saw %v", refused)
	}
}

// A caller the list does not name speaks for nothing, and a batch of only
// refusals is never forwarded.
func TestSourceBoundRefusesACallerThatIsNotListed(t *testing.T) {
	next := &sink.Memory{}
	var refused []string
	b := bound(next, &refused)
	rec := &record.Record{Id: record.NewID(), Source: "shop", Action: "shop.order.viewed"}

	res, err := b.Write(asCaller("system:serviceaccount:kube-system:somebody"), &sink.Request{Records: []*record.Record{rec}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Len() != 0 || len(res.Rejected) != 1 {
		t.Fatalf("forwarded %d, rejected %d; want nothing forwarded and one refusal", next.Len(), len(res.Rejected))
	}
}

// With no principal there is nobody to hold to a source: an in-process call, or
// a trial install that accepts anonymous writes.
func TestSourceBoundPassesAContextWithNoPrincipal(t *testing.T) {
	next := &sink.Memory{}
	var refused []string
	b := bound(next, &refused)
	rec := &record.Record{Id: record.NewID(), Source: "audit", Action: "audit.writer.started"}
	res, err := b.Write(context.Background(), &sink.Request{Records: []*record.Record{rec}})
	if err != nil || next.Len() != 1 || len(res.Rejected) != 0 {
		t.Fatalf("err=%v forwarded=%d rejected=%v", err, next.Len(), res.Rejected)
	}
}
