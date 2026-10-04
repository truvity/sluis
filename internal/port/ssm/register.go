package ssm

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

func init() {
	port.Register(port.Descriptor{
		Name: "ssm", Concern: port.ConcernSecrets,
		Summary:     "Dynamic secrets and exports as SecureString parameters in AWS SSM Parameter Store.",
		Requires:    port.Requires{AWS: true},
		Runtimes:    []port.Runtime{port.RuntimeKubernetes, port.RuntimeLambda},
		SecretStore: true,
		Factory: func(ctx context.Context, s port.Settings) (any, error) {
			var cfg Config
			if err := s.Decode(&cfg); err != nil {
				return nil, err
			}
			return New(ctx, cfg)
		},
	})
}
