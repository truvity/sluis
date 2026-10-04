package nats

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

// settings are the keys of `ports.nats`.
type settings struct {
	URL       string `json:"url"`
	Bucket    string `json:"bucket"`
	Replicas  int    `json:"replicas"`
	TokenFile string `json:"tokenFile"`
	CredsFile string `json:"credsFile"`
	CAFile    string `json:"caFile"`
	Create    *bool  `json:"create"`
}

// NATS keeps State and the Trigger in a JetStream KV bucket. It is on its way
// out (the owner's decision of 2026-10-04) and needs a long-lived connection,
// so it does not run on Lambda.
func init() {
	for _, c := range []struct {
		concern port.Concern
		summary string
	}{
		{port.ConcernState, "State in a NATS JetStream KV bucket; being retired."},
		{port.ConcernTrigger, "Notifications over the same bucket; being retired."},
	} {
		port.Register(port.Descriptor{
			Name: "nats", Concern: c.concern, Summary: c.summary,
			Runtimes: []port.Runtime{port.RuntimeKubernetes, port.RuntimeProcess},
			Factory: func(ctx context.Context, s port.Settings) (any, error) {
				var in settings
				if err := s.Decode(&in); err != nil {
					return nil, err
				}
				return Open(ctx, Config{
					URL: in.URL, Bucket: in.Bucket, Replicas: in.Replicas,
					TokenFile: in.TokenFile, CredsFile: in.CredsFile, CAFile: in.CAFile,
					NoCreate: in.Create != nil && !*in.Create,
				})
			},
		})
	}
}
