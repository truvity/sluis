package port

import (
	"context"
	"errors"
)

// KMSSigning is the kms signing adapter's settings, the same as
// `signingKey.kms`: the keys, oldest first with the last signing, the region,
// and the secret the sign-in state is derived from.
type KMSSigning struct {
	Keys        []string `json:"keys"`
	Region      string   `json:"region,omitempty"`
	StateSecret string   `json:"stateSecret"`
	// Additional is the keys of every other algorithm (RS256).
	Additional []KMSSigningAlg `json:"additional,omitempty"`
}

// KMSSigningAlg is one more algorithm's keys.
type KMSSigningAlg struct {
	Alg  string   `json:"alg"`
	Keys []string `json:"keys"`
}

// KMSWrappedSigning is the kms-wrapped signing adapter's settings, the same as
// `signingKey.kmsWrapped`. The durations are Go duration strings; an empty one
// takes the default.
type KMSWrappedSigning struct {
	// KeyID is deprecated: `keys.sign` names the key.
	KeyID       string   `json:"keyId,omitempty"`
	Region      string   `json:"region,omitempty"`
	StateSecret string   `json:"stateSecret"`
	Algorithms  []string `json:"algorithms,omitempty"`
	RotateEvery string   `json:"rotateEvery,omitempty"`
	Prepublish  string   `json:"prepublish,omitempty"`
	Retain      string   `json:"retain,omitempty"`
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
		Summary:  "Token signing by AWS KMS keys (ES384 on ECC_NIST_P384, and RS256 on RSA); the private key never leaves KMS (`signingKey.kms`).",
		Requires: Requires{AWS: true},
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeLambda},
		Factory: func(_ context.Context, s Settings) (any, error) {
			var k KMSSigning
			if err := s.Decode(&k); err != nil {
				return nil, err
			}
			if len(k.Keys) == 0 || k.StateSecret == "" {
				return nil, errors.New("the kms adapter needs keys and stateSecret (set `signingKey.kms` or `adapters.signing.settings`)")
			}
			return &k, nil
		},
	})
	Register(Descriptor{
		Name: "kms-wrapped", Concern: ConcernSigning,
		Summary: "Token signing by key pairs AWS KMS generates and wraps under one symmetric key, rotated automatically (ES384 and RS256); " +
			"the private key is decrypted into process memory to sign (`signingKey.kmsWrapped`).",
		Requires: Requires{AWS: true},
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeLambda},
		Factory: func(_ context.Context, s Settings) (any, error) {
			var k KMSWrappedSigning
			if err := s.Decode(&k); err != nil {
				return nil, err
			}
			if k.StateSecret == "" {
				return nil, errors.New("the kms-wrapped adapter needs stateSecret (set `signingKey.kmsWrapped` or " +
					"`adapters.signing.settings`); the wrapping key is `keys.sign`")
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
		Name: "eventbridge", Concern: ConcernSchedule,
		Summary:  "One EventBridge Scheduler schedule per target invokes the controller function with {\"kind\":\"tick\",\"target\":\"<id>\"}.",
		Requires: Requires{AWS: true},
		Runtimes: []Runtime{RuntimeLambda},
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
