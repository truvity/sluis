//go:build !lambda

package app

import (
	"github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/store"
)

// clusterLinks reads people's links from the Secret they are kept in, with the
// `legacy` adapter; nil when there is no cluster to read it from.
func clusterLinks(stores *store.Stores) controller.LinkStore {
	if stores.Backend == nil || stores.Backend.Kube == nil {
		return nil
	}
	return kube.NewGitHubLinks(stores.Backend.Kube)
}
