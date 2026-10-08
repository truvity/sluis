package secrets

import (
	"context"
	"fmt"

	"github.com/truvity/sluis/internal/config"
)

// Open builds the source a serve document's `secrets` names: env when it names
// none. A document converted from v1 reads, in front of it, every secret where
// v1 said it was. more are names the document does not hold that are read too
// (each confidential client's): with the env source no two names may share a
// variable.
func Open(ctx context.Context, f *config.Serve, more ...string) (Source, error) {
	var src Source = Env{}
	if s := f.Secrets; s == nil || s.Source == "" || s.Source == KindEnv {
		if err := CheckEnvNames(append(Named(f), more...)); err != nil {
			return nil, err
		}
	}
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
			ssm.Layout = s.Layout
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

// Named is every secret name a serve document gives.
func Named(f *config.Serve) []string {
	var out []string
	if f.Valkey != nil {
		out = append(out, f.Valkey.LoginSecret)
	}
	if f.Recovery != nil {
		out = append(out, f.Recovery.LoginSecret)
	}
	if o := f.OAuthClient; o != nil && o.Provider != "" {
		out = append(out, ProviderClientID(o.Provider), ProviderClientSecret(o.Provider))
	}
	if k := f.SigningKey; k != nil {
		if k.KMS != nil {
			out = append(out, k.KMS.StateSecret)
		}
		if k.KMSWrapped != nil {
			out = append(out, k.KMSWrapped.StateSecret)
		}
	}
	if f.Directory != nil {
		for _, w := range f.Directory.Workspaces {
			out = append(out, w.KeySecret)
		}
	}
	return out
}
