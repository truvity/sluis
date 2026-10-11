package rosterapp

import (
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/store"
)

// Issuer is the assembled issuer, for a test to mint through.
func (a *App) Issuer() *issuer.Issuer { return a.issuer.Issuer() }

// Stores are the ports the issuer was assembled over.
func (a *App) Stores() *store.Stores { return a.stores }

// Hub is the directory hub the issuer reads the snapshots with.
func (a *App) Hub() *hub.Hub { return a.directory.Hub() }
