//go:build lambda

package app

import (
	"github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/store"
)

// clusterLinks has none: the Lambda build keeps links on the State port.
func clusterLinks(*store.Stores) controller.LinkStore { return nil }
