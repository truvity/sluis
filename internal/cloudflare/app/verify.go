package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/truvity/sluis/internal/adaptercheck"
)

// Verify builds what New builds: the storage ports, the audit trail's sink and
// the SSM secrets store the minter credential lives in. It reaches nothing that
// mutates and mints nothing (adaptercheck).
func (c Config) Verify(ctx context.Context, log *slog.Logger) error {
	st, err := adaptercheck.Verify(ctx, log, c.stores, c.audit, adaptercheck.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if st != nil && st.V4 == nil && st.V5 == nil {
		return errors.New("cloudflare: the minter credential and the stored credentials live in the SSM secrets store (layout v4 or v5), " +
			"so `secrets.source: ssm` is required")
	}
	return nil
}
