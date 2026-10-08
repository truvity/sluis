package natssink_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/sdk/sink"
	"github.com/truvity/sluis/audit/sdk/sink/sinktest"
	"github.com/truvity/sluis/audit/sink/natssink"
)

func TestConforms(t *testing.T) {
	sinktest.Run(t, func(t *testing.T) sinktest.Subject {
		js, s, _ := stream(t)
		p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		return sinktest.Subject{
			Sink: p, Durability: sink.Queued, Refuses: true, Idempotent: true,
			Count: func() int {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				info, err := s.Info(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return int(info.State.Msgs)
			},
		}
	})
}
