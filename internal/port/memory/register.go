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
		{port.ConcernState, stateFactory},
		{port.ConcernSecrets, moduleScoped(func() any { return NewSecrets() })},
		{port.ConcernBlobs, moduleScoped(func() any { return New().Blobs() })},
		{port.ConcernTrigger, moduleScoped(func() any { return NewTrigger() })},
	} {
		port.Register(port.Descriptor{
			Name: "memory", Concern: c.concern, Summary: summary,
			ProcessLocal: true, Factory: c.build,
		})
	}
}

// stateSettings are the memory State's settings. Module, when set, makes the
// State that module's (layout 5, ADR 0072): a write of another module's key is
// [port.ErrNotOwner], as it is on the dynamodb adapter's table. Unset, the
// State is not split by module.
type stateSettings struct {
	Module string `json:"module"`
}

// module reads the `module` setting: empty is none, and a name that is not a
// module is refused.
func module(s port.Settings) (port.Module, error) {
	var cfg stateSettings
	if err := s.Decode(&cfg); err != nil {
		return "", err
	}
	if cfg.Module == "" {
		return "", nil
	}
	return port.ParseModule(cfg.Module)
}

func stateFactory(_ context.Context, s port.Settings) (any, error) {
	m, err := module(s)
	if err != nil {
		return nil, err
	}
	if m == "" {
		return New(), nil
	}
	return port.Owned(m, New()), nil
}

// moduleScoped is a factory of a concern that has nothing to refuse by module
// (a secret, a blob, a trigger): it takes the same `module` setting as the
// State, so one settings object serves a module's concerns, and validates it.
func moduleScoped(build func() any) port.Factory {
	return func(_ context.Context, s port.Settings) (any, error) {
		if _, err := module(s); err != nil {
			return nil, err
		}
		return build(), nil
	}
}
