package sqsorigin_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/sluis/audit/internal/sqsorigin"
	"github.com/truvity/sluis/audit/sdk/record"
)

func forged() *record.Record {
	r := &record.Record{
		Id: record.NewID(), TenantId: "acme", Action: "x.y",
		Observer:   &record.Observer{Id: "system:serviceaccount:kube-system:somebody", Version: "9.9", Instance: "evil"},
		RecordedAt: timestamppb.New(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
	if err := record.Stamp(r, r.Observer, r.RecordedAt.AsTime()); err != nil {
		panic(err)
	}
	return r
}

func TestAMappedSenderIsNamedAndTheSentTimeIsRecordedAt(t *testing.T) {
	o := &sqsorigin.Origin{
		Senders: map[string]string{"AROAEXAMPLEROLEID": "workload:wallet"},
		Version: "1.2.3", Instance: "writer-1",
	}
	sent := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	r := forged()
	if err := o.Stamp(r, map[string]string{sqsorigin.SenderID: "AROAEXAMPLEROLEID:session-7", sqsorigin.SentTimestamp: "1792324800000"}); err != nil {
		t.Fatal(err)
	}
	if r.GetObserver().GetId() != "workload:wallet" || r.GetObserver().GetVersion() != "1.2.3" || r.GetObserver().GetInstance() != "writer-1" {
		t.Errorf("observer = %+v, want the mapped sender and this writer's version and instance", r.GetObserver())
	}
	if !r.GetRecordedAt().AsTime().Equal(sent) {
		t.Errorf("recorded_at = %s, want SentTimestamp %s", r.GetRecordedAt().AsTime(), sent)
	}
	if h, err := record.OriginHash(r); err != nil || h != r.GetOriginHash() {
		t.Errorf("the origin hash does not describe the stamped record: %v", err)
	}
}

// With no table, or a sender the table does not name, the observer is empty:
// the writer's own attribution. The forged one is gone either way, and the time is
// still SQS's.
func TestAnUnmappedSenderIsNotAttributedToWhatItClaimed(t *testing.T) {
	now := time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)
	for name, o := range map[string]*sqsorigin.Origin{
		"no table":       {Now: func() time.Time { return now }},
		"another sender": {Senders: map[string]string{"AROAOTHER": "workload:other"}, Now: func() time.Time { return now }},
	} {
		r := forged()
		if err := o.Stamp(r, map[string]string{sqsorigin.SenderID: "AROAEXAMPLEROLEID:s", sqsorigin.SentTimestamp: "1792324800000"}); err != nil {
			t.Fatal(err)
		}
		if r.GetObserver().GetId() != "" || r.GetObserver().GetInstance() == "evil" || r.GetObserver().GetVersion() == "9.9" {
			t.Errorf("%s: observer = %+v", name, r.GetObserver())
		}
		if r.GetRecordedAt().AsTime().Year() == 2020 {
			t.Errorf("%s: recorded_at is the sender's", name)
		}
	}
}

func TestAWholeSenderIdMatchesBeforeItsRoleId(t *testing.T) {
	o := &sqsorigin.Origin{Senders: map[string]string{"AROAROLE": "role", "AROAROLE:batch": "batch-job"}}
	r := forged()
	_ = o.Stamp(r, map[string]string{sqsorigin.SenderID: "AROAROLE:batch"})
	if r.GetObserver().GetId() != "batch-job" {
		t.Errorf("observer = %q", r.GetObserver().GetId())
	}
}

func TestAMessageWithNoSentTimestampIsStampedWithTheClock(t *testing.T) {
	now := time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)
	o := &sqsorigin.Origin{Now: func() time.Time { return now }}
	r := forged()
	_ = o.Stamp(r, map[string]string{sqsorigin.SentTimestamp: "not a number"})
	if !r.GetRecordedAt().AsTime().Equal(now) {
		t.Errorf("recorded_at = %s", r.GetRecordedAt().AsTime())
	}
}

func TestTrustIsOnlyWhatWasMarked(t *testing.T) {
	if sqsorigin.Trusted(context.Background()) || !sqsorigin.Trusted(sqsorigin.Trust(context.Background())) {
		t.Error("Trusted does not follow Trust")
	}
}
