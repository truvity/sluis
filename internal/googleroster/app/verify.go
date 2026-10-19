package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/adaptercheck"
	"github.com/truvity/sluis/internal/portstore"
)

// Verify builds what New builds at the start of a refresh tick that touches no
// directory: the storage ports (the module's own table, the blobs the
// snapshots are kept in, the secrets), the audit trail's sink and the
// credentials' store. A setting the document lacks is refused as New would
// refuse it. It reaches nothing that mutates (adaptercheck).
func (c Config) Verify(ctx context.Context, log *slog.Logger) error {
	st, err := adaptercheck.Verify(ctx, log, c.stores, c.audit, adaptercheck.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if st == nil {
		return errors.New("ports.adapter: the Google module keeps its snapshots in the blob store and its lease in its table, and has no Kubernetes to do it in")
	}
	base := portstore.New(st.Ports).WithV5(st.V5)
	if err = base.RequireSecrets(); err != nil {
		return fmt.Errorf("ports.adapter %s: %w", st.Adapter, err)
	}
	if st.Ports.Blob == nil {
		return errors.New("ports.blob: the snapshots of the directory are kept in the blob store, and none is configured")
	}
	return nil
}
