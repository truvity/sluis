package memory

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

// The memory adapter registers for every concern that has a port of its own.
// It is process-local: a second replica would see another copy.
func init() {
	const summary = "In this process's memory; a restart loses it. For tests and the demonstration."
	for _, c := range []struct {
		concern port.Concern
		build   port.Factory
	}{
		{port.ConcernState, func(context.Context, port.Settings) (any, error) { return New(), nil }},
		{port.ConcernSecrets, func(context.Context, port.Settings) (any, error) { return NewSecrets(), nil }},
		{port.ConcernBlobs, func(context.Context, port.Settings) (any, error) { return New().Blobs(), nil }},
		{port.ConcernTrigger, func(context.Context, port.Settings) (any, error) { return NewTrigger(), nil }},
	} {
		port.Register(port.Descriptor{
			Name: "memory", Concern: c.concern, Summary: summary,
			ProcessLocal: true, Factory: c.build,
		})
	}
}
