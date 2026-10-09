package memory_test

import (
	"testing"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest"
)

func TestConformance(t *testing.T) {
	porttest.Run(t, func(*testing.T) porttest.Env {
		s := memory.New()
		s.Allow("workload-token", "system:serviceaccount:ns:sa", "sluis")
		return porttest.Env{
			Set:          s.Set(),
			Advance:      s.Advance,
			BlobPrefixes: []string{"reports/", "google/"},
			Proof: func() porttest.Proof {
				return porttest.Proof{Token: "workload-token", Subject: "system:serviceaccount:ns:sa", Audience: "sluis"}
			},
		}
	})
}

func TestSecretsConformance(t *testing.T) {
	porttest.RunSecrets(t, func(*testing.T) port.Secrets { return memory.NewSecrets() })
}
