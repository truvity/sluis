package rosterapp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/adaptercheck"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/store"
)

// Verify builds what New builds from the adapters the document names: the
// storage ports (the oidc table and its peers, the blobs), the secrets, the
// trigger, the audit trail's sink and the signing adapter's settings. A setting
// the document lacks is refused as New would refuse it. It reaches nothing that
// mutates (adaptercheck).
func (c Config) Verify(ctx context.Context, log *slog.Logger) error {
	st, err := adaptercheck.Verify(ctx, log, c.Stores, c.Directory.AuditConfig(), adaptercheck.Options{Signing: true})
	if err != nil {
		return err
	}
	defer st.Close()
	if st == nil || st.Adapter == store.AdapterLegacy {
		return nil
	}
	if err = portstore.New(st.Ports).WithV5(st.V5).RequireSecrets(); err != nil {
		return fmt.Errorf("ports.adapter %s: %w", st.Adapter, err)
	}
	return nil
}
