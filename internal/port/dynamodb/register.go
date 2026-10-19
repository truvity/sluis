package dynamodb

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

// DynamoDB keeps State and the Trigger in a table per module, with the
// platform's credentials. It runs on every runtime.
func init() {
	for _, c := range []struct {
		concern port.Concern
		summary string
	}{
		{port.ConcernState, "State, sessions included, in a DynamoDB table per module with per-item TTL."},
		{port.ConcernTrigger, "Notifications over the same table."},
	} {
		port.Register(port.Descriptor{
			Name: "dynamodb", Concern: c.concern, Summary: c.summary,
			Requires: port.Requires{AWS: true},
			Factory: func(ctx context.Context, s port.Settings) (any, error) {
				var cfg Config
				if err := s.Decode(&cfg); err != nil {
					return nil, err
				}
				// The tables are a set of stores, one per module, which the store
				// package wires (OpenTables): there is no one State to build.
				return OpenTables(ctx, cfg)
			},
		})
	}
}
