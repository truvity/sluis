package main

import (
	"testing"
	"time"

	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/internal/registry"
	"github.com/truvity/sluis/audit/sdk/record"
)

// The writer's own record of a registration is a record like any other: it
// passes the check every emitter's record passes, and carries the time it
// happened. Built by hand, it once carried none, and the archive keyed it
// under the epoch -- outside every digest window, for the life of its lock.
func TestARegistrationRecordIsAWholeRecord(t *testing.T) {
	r := registrationRecord(registry.Entry{Source: "app", Version: "1.0.0", RegisteredBy: "app"}, "0.2.6")
	if err := record.Check(r, record.Default); err != nil {
		t.Fatalf("the writer's own record fails the check: %v", err)
	}
	if got := r.GetOccurredAt().AsTime(); time.Since(got) > time.Minute || got.IsZero() {
		t.Fatalf("occurred_at = %v, want now", got)
	}
	if r.GetObserver().GetVersion() != "0.2.6" {
		t.Fatalf("observer.version = %q, want the writer's", r.GetObserver().GetVersion())
	}
}

// Only a NATS stream is read as the receivers' own: the stream's credentials
// admit them and nothing else. A queue delivers whatever its sender wrote, so a
// stamp inside a message from one is a claim and is replaced.
func TestOnlyANATSStreamKeepsTheUpstreamStamp(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Writer
		want bool
	}{
		{"no stream", &config.Writer{}, false},
		{"nats", &config.Writer{Consume: &config.Consume{NATS: &config.Stream{}}}, true},
		{"sqs", &config.Writer{Consume: &config.Consume{SQS: &config.ConsumeSQS{}}}, false},
	}
	for _, c := range cases {
		if got := keepsUpstreamStamp(c.cfg); got != c.want {
			t.Errorf("%s: keepsUpstreamStamp = %v, want %v", c.name, got, c.want)
		}
	}
}
