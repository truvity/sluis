// Package adaptercheck is the half of a deploy's post-deploy check that the
// secrets check is not: it builds every adapter a module's function builds at
// start and in a tick, from the function's own document, and refuses with the
// setting that is missing. A document that lacks `adapters.audit` passes a
// check of the declared secrets and fails the first tick; this fails it at
// deploy.
//
// It builds and validates and mutates nothing: a table is bound and never
// created, a trail is constructed and never registered, a credential is not
// minted (store.Config.OpenDry, audit.Verify).
package adaptercheck

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/store"
)

// Options say what else the module's function builds.
type Options struct {
	// Signing is set for the issuer, the only module that signs: the signing
	// adapter's settings are built.
	Signing bool
}

// Verify builds the adapters of a module whose storage is stores and whose
// audit trail is trail (what the document's legacy keys said; the plan's audit
// adapter is applied on top, as the module does). The Stores it opened are
// returned for the module's own checks and are the caller's to Close; they are
// nil for an adapter that needs a cluster.
func Verify(ctx context.Context, log *slog.Logger, stores store.Config, trail audit.Config, opts Options) (*store.Stores, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	st, plan, err := stores.OpenDry(ctx, log)
	if err != nil {
		return nil, fmt.Errorf("storage adapters: %w", err)
	}
	fail := func(err error) (*store.Stores, error) {
		if st != nil {
			st.Close()
		}
		return nil, err
	}
	trail, err = audit.FromPlan(trail, plan)
	if err != nil {
		return fail(err)
	}
	if err = audit.Verify(trail); err != nil {
		return fail(fmt.Errorf("audit adapter: %w", err))
	}
	if opts.Signing {
		if ch, ok := plan[port.ConcernSigning]; ok {
			if d, ok := port.Default.Lookup(port.ConcernSigning, ch.Adapter); ok && d.Factory != nil {
				if _, err = d.Factory(ctx, ch.Settings); err != nil {
					return fail(fmt.Errorf("signing adapter %q: %w", ch.Adapter, err))
				}
			}
		}
	}
	return st, nil
}
