package secrets

import (
	"context"
	"fmt"

	"github.com/truvity/sluis/internal/config"
)

// Open builds the source a serve document's `secrets` names: env when it names
// none. A document converted from v1 reads, in front of it, every secret where
// v1 said it was.
func Open(ctx context.Context, f *config.Serve) (Source, error) {
	var src Source = Env{}
	if s := f.Secrets; s != nil {
		switch s.Source {
		case "", KindEnv:
		case KindFile:
			src = File{Root: s.Root}
		case KindSSM:
			refresh := DefaultRefresh
			if s.Refresh != nil {
				refresh = s.Refresh.D()
			}
			ssm, err := NewSSM(ctx, s.Root, s.Region, s.Endpoint, refresh)
			if err != nil {
				return nil, err
			}
			src = ssm
		default:
			return nil, fmt.Errorf("secrets.source: %q is env, file or ssm", s.Source)
		}
	}
	locations, clientDir := f.LegacySecrets()
	if len(locations) == 0 && clientDir == "" {
		return src, nil
	}
	l := Legacy{Locations: map[string]Location{}, ClientDir: clientDir, Next: src}
	for name, loc := range locations {
		l.Locations[name] = Location{Env: loc.Env, File: loc.File}
	}
	return l, nil
}
