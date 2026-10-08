package cli

import (
	"context"
	"testing"

	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/sink/sqssink"
)

func TestASinkThatNamesAQueueGivesQueuedAndNoMore(t *testing.T) {
	was := newSQS
	t.Cleanup(func() { newSQS = was })
	var region string
	newSQS = func(_ context.Context, r string) (sqssink.API, error) {
		region = r
		return struct{ sqssink.API }{}, nil
	}
	s := config.Sink{SQS: &config.SQS{QueueURL: "https://sqs.eu-west-1.amazonaws.com/1/audit", Region: "eu-west-1"}}
	if _, err := SinkFrom(s, "queued"); err != nil {
		t.Fatal(err)
	}
	if region != "eu-west-1" {
		t.Errorf("region = %q", region)
	}
	if _, err := SinkFrom(s, "archived"); err == nil {
		t.Error("a queue was held to archived")
	}
}
