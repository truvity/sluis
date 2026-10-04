package port

import (
	"context"
	"errors"
)

// KMSSigning is the kms signing adapter's settings, the same as
// `signingKey.kms`: the keys, oldest first with the last signing, the region,
// and the file the sign-in state is derived from.
type KMSSigning struct {
	Keys            []string `json:"keys"`
	Region          string   `json:"region,omitempty"`
	StateSecretFile string   `json:"stateSecretFile"`
}

// The adapters of the concerns whose wiring is the process's own: the
// signing keys are read by the issuer, the schedule is the controllers'
// ticker, the audit sink is the audit package's. They are described here so
// the table and the matrix name them; none has a Factory.
func init() {
	Register(Descriptor{
		Name: "file", Concern: ConcernSigning,
		Summary: "Signing keys read from files the platform mounts (`signingKey`).",
		Factory: func(_ context.Context, s Settings) (any, error) {
			if len(s) > 0 {
				return nil, errors.New("the file adapter has no settings: the key is `signingKey.file`")
			}
			return nil, nil
		},
	})
	Register(Descriptor{
		Name: "kms", Concern: ConcernSigning,
		Summary:  "Token signing by AWS KMS ECC_NIST_P384 keys; the private key never leaves KMS (`signingKey.kms`).",
		Requires: Requires{AWS: true},
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeLambda},
		Factory: func(_ context.Context, s Settings) (any, error) {
			var k KMSSigning
			if err := s.Decode(&k); err != nil {
				return nil, err
			}
			if len(k.Keys) == 0 || k.StateSecretFile == "" {
				return nil, errors.New("the kms adapter needs keys and stateSecretFile")
			}
			return &k, nil
		},
	})
	Register(Descriptor{
		Name: "ticker", Concern: ConcernSchedule,
		Summary: "An in-process interval timer.",
		// A Lambda has no process between invocations to keep a timer in.
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeProcess},
	})
	Register(Descriptor{
		Name: "connect", Concern: ConcernAudit,
		Summary: "Events sent to the audit installation's receiver (`audit.writer`).",
	})
	Register(Descriptor{
		Name: "log", Concern: ConcernAudit,
		Summary: "Events written to the log only.",
	})
	Register(Descriptor{
		Name: "sqs", Concern: ConcernAudit,
		Summary: "Events published to an SQS queue the audit writer Lambda consumes " +
			"(settings `queueURL`, `region`, `endpoint`, `timeout`; the catalogue travels with the writer's package).",
		Requires: Requires{AWS: true},
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeLambda},
	})
}
