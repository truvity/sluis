package issuerapp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/srp"
)

// The subjects the two password accounts complete as. Both are
// ServiceAccount spellings, so the policy's `service_account` matchers decide
// what they are in, as they do for a recovery token: the recovery account in
// the namespace and under the name the token door uses where there are
// some, and the conformance account only ever here.
const (
	// passwordNamespace is the namespace of the subjects where the issuer is not
	// in a cluster to take one from.
	passwordNamespace = "sluis"
	// recoveryName is the recovery account's name when `recovery.serviceAccount`
	// names none.
	recoveryName = "recovery"
	// conformanceName is the conformance account's.
	conformanceName = "conformance"
)

// openPassword builds the password sign-in (docs/decisions/0076) for the
// accounts this installation has a verifier for, or nothing. The conformance
// account is built only when `conformance.enabled` is set, and the service
// refuses to start when the policy would make it an operator.
func openPassword(
	ctx context.Context, cfg Config, st *store.Stores, set *policy.Set,
	stateKey access.KeyFunc, log *slog.Logger,
) (*issuer.PasswordDoor, error) {
	door := &issuer.PasswordDoor{}
	if cfg.recoveryPassword {
		if src := srpauth.SourceOf(cfg.v5, cfg.secrets, srpauth.RecoveryUser, cfg.recoveryVerifier); src != nil {
			door.Recovery = src
			namespace, name := passwordNamespace, cfg.recoveryAccount
			if ns, ok := clusterNamespace(st); ok && cfg.inCluster {
				namespace = ns
			}
			if name == "" {
				name = recoveryName
			}
			door.RecoverySubject = access.ServiceAccountSubject(namespace, name)
			log.InfoContext(ctx, "the recovery account may sign in by password (SRP): the issuer holds a verifier, not the password",
				slog.String("subject", door.RecoverySubject))
		}
	}
	if cfg.conformanceEnabled {
		src := srpauth.SourceOf(cfg.v5, cfg.secrets, srpauth.ConformanceUser, cfg.conformanceVerifier)
		if src == nil {
			return nil, errors.New("conformance.enabled is set and the conformance account's verifier is not available: " +
				"name it with conformance.verifierSecret, or use layout v5")
		}
		door.Conformance = src
		door.ConformanceSubject = access.ServiceAccountSubject(passwordNamespace, conformanceName)
		if err := issuer.ConformanceSubject(set, door.ConformanceSubject); err != nil {
			return nil, err
		}
		log.WarnContext(ctx, "the conformance test account is ON: a username and password sign in to clients marked harness, "+
			"as a subject that holds no operator group. Turn conformance.enabled off when the run is over",
			slog.String("subject", door.ConformanceSubject))
	}
	if door.Recovery == nil && door.Conformance == nil {
		return nil, nil //nolint:nilnil // no password door is a configuration, not a failure
	}
	var exchanges port.State = memory.New()
	if st.Usable {
		exchanges = st.Ports.State
	}
	door.Service = &srpauth.Service{
		Params:   srp.Default,
		Store:    srpauth.StateStore{State: exchanges},
		DecoyKey: srpauth.DecoyKey(stateKey),
		Refusals: throttle.Default(),
		Starts:   srpauth.NewStarts(nil),
	}
	return door, nil
}
