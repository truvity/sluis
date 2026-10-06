package issuerapp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/port"
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
	return nil
}

// ReconcileClientSecrets makes sure every generated client has its secret
// stored, and reports what it did. A failure for one client is logged and
// counted, never returned: the next pass retries, and meanwhile the resolver
// serves the input secret, if the installation delivers one.
func (a *App) ReconcileClientSecrets(ctx context.Context) clientcreds.Result {
	if len(a.generated) == 0 {
		return clientcreds.Result{}
	}
	var input clientcreds.Input
	if a.cfg.secrets != nil {
		input = a.cfg.secrets
	}
	return clientcreds.Reconcile(ctx, a.generated, a.credStore, input, time.Now(), a.log, clientcreds.Hooks{
		Outcome: func(_ context.Context, id string, o clientcreds.Outcome, _ error) {
			if o == clientcreds.OutcomeCreated || o == clientcreds.OutcomeAdopted || o == clientcreds.OutcomeConflict {
				a.creds.Forget(id)
			}
		},
	})
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
