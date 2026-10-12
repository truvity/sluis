package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/adaptercheck"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/policy"
)

// Verify builds what a pass and the mint build: the policy, the storage ports,
// the secrets, the trigger and the audit trail's sink. A setting the document
// lacks is refused as New would refuse it. It reaches nothing that mutates
// (adaptercheck).
func (c Config) Verify(ctx context.Context, log *slog.Logger) error {
	if _, err := policy.NewSet(c.policy.Policy); err != nil {
		return fmt.Errorf("the policy: %w", err)
	}
	for org := range c.enabled {
		if _, bound := c.policy.Policy.GitHub[org]; !bound {
			return fmt.Errorf("controllers.github.enabledOrgs names %s, which the policy does not bind", org)
		}
	}
	st, err := adaptercheck.Verify(ctx, log, c.stores, c.audit, adaptercheck.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if st == nil || st.Adapter == store.AdapterLegacy {
		return nil
	}
	if err = portstore.New(st.Ports).WithV4(st.V4).WithV5(st.V5).RequireSecrets(); err != nil {
		return fmt.Errorf("ports.adapter %s: %w", st.Adapter, err)
	}
	return nil
}
