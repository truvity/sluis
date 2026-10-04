//go:build !lambda

package issuerapp

import "github.com/truvity/sluis/internal/store"

// clusterNamespace is the namespace this process runs in, when it has the
// cluster's objects.
func clusterNamespace(st *store.Stores) (string, bool) {
	if st.Backend == nil || st.Backend.Kube == nil {
		return "", false
	}
	return st.Backend.Kube.Namespace(), true
}
