package openbao

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

func init() {
	port.Register(port.Descriptor{
		Name: "openbao", Concern: port.ConcernSecrets,
		Summary: "Dynamic secrets as KV version 2 secrets in an OpenBao mount, laid out like SSM (layout v3); " +
			"logs in with a ServiceAccount or web identity JWT.",
		Requires:    port.Requires{OpenBao: true},
		SecretStore: true,
		Factory: func(_ context.Context, s port.Settings) (any, error) {
			var set SecretsSettings
			if err := s.Decode(&set); err != nil {
				return nil, err
			}
			return NewSecrets(set.Config())
		},
	})
}
