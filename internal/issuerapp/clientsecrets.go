package issuerapp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/policy"
)

// clientSecretsInterval is how often a server process looks again for a
// generated client whose secret could not be settled, and for a record that
// was never written. A Lambda has no loop; its schedule calls
// [App.ReconcileClientSecrets].
const clientSecretsInterval = 5 * time.Minute

// generatedClients are the ids of the clients whose secret the issuer makes.
func generatedClients(set *policy.Set) []string {
	var ids []string
	// By index: a ClientView is wide, and copying one per iteration is what
	// the linter objects to.
	clients := set.Clients()
	for i := range clients {
		if clients[i].SecretGenerated() {
			ids = append(ids, clients[i].ID)
		}
	}
	return ids
}

// checkGeneratedSecrets refuses, at start, a configuration that declares a
// generated client where the secrets adapter cannot create a secret only if
// absent: the legacy adapter's Kubernetes Secrets have no such write, and two
// replicas starting together would each make a different secret.
func checkGeneratedSecrets(ids []string, st *store.Stores) error {
	if len(ids) == 0 {
		return nil
	}
	if st.Plan.Name(port.ConcernSecrets) == store.AdapterLegacy || st.Ports.Secrets == nil {
		return fmt.Errorf("client %q has `secret: {generate: true}` and the secrets adapter cannot create a secret "+
			"only if absent (the legacy adapter, or none): choose a secrets adapter (ssm, openbao or memory) "+
			"or name the secret with `secret: <name>`", ids[0])
	}
	// A rotation and an orphan mark are serialised by a lease on the State. A
	// lease held in this process only would let two replicas write one record
	// at once (ssm's conditional write is a read and then a write), so a
	// generated secret needs a shared State: it fails closed rather than
	// risk two replicas serving different secrets.
	// Exempt only when the SECRETS are in this process too (the memory
	// adapter), so there is no second replica to exclude; a process-local
	// State beside shared secrets (ssm, openbao) is not.
	if _, shared := st.LeaseState(); !shared && st.Plan.Name(port.ConcernSecrets) != store.AdapterMemory {
		return fmt.Errorf("client %q has `secret: {generate: true}` and the State is not shared between replicas, "+
			"so a rotation cannot be serialised: configure a shared State (adapters.state) "+
			"or name the secret with `secret: <name>`", ids[0])
	}
	return nil
}

// newClientSecretManager is what the operator endpoint, and the reconcile's
// writes to an existing record, go through. Its lease is the State's, so two
// replicas rotate one client one at a time (ssm's conditional write is a read
// and then a write, and does not exclude a second writer by itself).
func newClientSecretManager(
	set *policy.Set, st *store.Stores, creds *clientcreds.Resolver, log *slog.Logger,
) (*clientcreds.Manager, *rails.Leases) {
	state, _ := st.LeaseState()
	leases := &rails.Leases{State: state, Holder: rails.NewHolder(), Log: log}
	return &clientcreds.Manager{
		Store:    st.Ports.Secrets,
		Lock:     leases,
		Resolver: creds,
		// The policy in force when asked, not when assembled.
		Generated: func(id string) bool {
			c, ok := set.Client(id)
			return ok && c.SecretGenerated()
		},
		Log: log,
	}, leases
}

// ReconcileClientSecrets makes sure every generated client has its secret
// stored, and reports what it did. A failure for one client is logged and
// counted, never returned: the next pass retries, and meanwhile the resolver
// serves the input secret, if the installation delivers one.
//
// It also reports, once each, the stored secrets whose client is no longer a
// generated client of the policy (see [clientcreds.ReconcileOrphans]).
func (a *App) ReconcileClientSecrets(ctx context.Context) clientcreds.Result {
	if a.credStore == nil {
		return clientcreds.Result{}
	}
	var input clientcreds.Input
	if a.cfg.secrets != nil {
		input = a.cfg.secrets
	}
	now := time.Now()
	hooks := clientcreds.Hooks{
		Lock: a.leases,
		Orphaned: func(ctx context.Context, id string) {
			a.issuer.Record(ctx, audit.ClientSecretOrphaned(id, now))
		},
		Outcome: func(ctx context.Context, id string, o clientcreds.Outcome, err error) {
			if o == clientcreds.OutcomeCreated || o == clientcreds.OutcomeAdopted ||
				o == clientcreds.OutcomeConflict || o == clientcreds.OutcomeRestored {
				a.creds.Forget(id)
			}
			if err != nil {
				return
			}
			switch o {
			case clientcreds.OutcomeCreated:
				a.issuer.Record(ctx, audit.ClientSecretCreated(id, now))
			case clientcreds.OutcomeAdopted:
				a.issuer.Record(ctx, audit.ClientSecretAdopted(id, "input", now))
			case clientcreds.OutcomeRestored:
				a.issuer.Record(ctx, audit.ClientSecretAdopted(id, "record", now))
			}
		},
	}
	res := clientcreds.Result{}
	if len(a.generated) > 0 {
		res = clientcreds.Reconcile(ctx, a.generated, a.credStore, input, now, a.log, hooks)
	}
	clientcreds.ReconcileOrphans(ctx, a.generated, a.credStore, now, a.log, hooks)
	return res
}

// watchClientSecrets repeats [App.ReconcileClientSecrets] until ctx ends.
func (a *App) watchClientSecrets(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(clientSecretsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if res := a.ReconcileClientSecrets(ctx); res.Failed() > 0 {
			log.WarnContext(ctx, "some generated client secrets are still unsettled", "clients", res.Failed())
		}
	}
}
