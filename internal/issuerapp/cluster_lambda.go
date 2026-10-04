//go:build lambda

package issuerapp

import "github.com/truvity/sluis/internal/store"

// clusterNamespace has none: the Lambda build runs in no cluster.
func clusterNamespace(*store.Stores) (string, bool) { return "", false }
