package rosterapp

import (
	"context"
	"errors"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/server"
)

// A recovery sign-in carries its subject where a person carries an address,
// so the address alone does not make it a person: it is refused before any
// exchange, and as "not admitted", which the console answers with a 403.
func TestARecoverySignInIsNotMintedAnAuditToken(t *testing.T) {
	t.Parallel()
	// A nil issuer would panic if the mint were attempted.
	mint := auditToken(nil, "audit")
	for _, id := range []access.Identity{
		{Email: "system:serviceaccount:sluis:recovery", Source: access.SourceRecovery},
		{Subject: "system:serviceaccount:sluis:recovery", Source: access.SourceRecovery},
	} {
		if _, _, err := mint(context.Background(), id); !errors.Is(err, server.ErrNotAdmitted) {
			t.Errorf("%+v = %v, want not admitted", id, err)
		}
	}
}
