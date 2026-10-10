package dynamodb

import (
	"context"
	"errors"

	"github.com/truvity/sluis/internal/port"
)

// DynamoDB keeps State and the Trigger in one table, with the platform's
// credentials. It runs on every runtime.
func init() {
	for _, c := range []struct {
		concern port.Concern
		summary string
	}{
		{port.ConcernState, "State, sessions included, in one DynamoDB table with per-item TTL."},
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
				if len(cfg.Tables) > 0 {
					// Layout 5 is a set of stores, one per module, which the store
					// package wires (OpenTables); this factory builds one table.
					return nil, errors.New("dynamodb: tables is layout 5 and is opened per module, not as one State")
				}
				return Open(ctx, cfg)
			},
		})
	}
}
