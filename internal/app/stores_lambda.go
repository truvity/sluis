//go:build lambda

package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/truvity/sluis/internal/store"
)

// openKubeStores refuses: the Lambda build has no cluster to keep ConfigMaps in.
func openKubeStores(context.Context, Config, *store.Stores, *slog.Logger) (stores, error) {
	return stores{}, errors.New("store: kubernetes is not in the Lambda build; use ports.adapter: dynamodb")
}

// useCluster adds nothing: there is no cluster, so no token review and no
// mounted OAuth client.
func useCluster(*stores, Config, *store.Stores) {}
