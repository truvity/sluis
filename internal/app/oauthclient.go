package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/settings"
)

// declaresOAuthClient says whether the document declares a client, without
// reading it.
func declaresOAuthClient(o *config.OAuthClient) bool {
	return o != nil && (o.ID != "" || o.Provider != "")
}

// declaredOAuthSettings is the store that serves the client the document
// declares, or an empty one when it declares none. It reads nothing: the
// secrets are read when a request first asks for the client (the console's
// settings page, a connect flow) and again after [lazy.TTL]. Its client is
// declared by the provider whose client-id and client-secret it is, the id
// instead as `id` where it is given. Half a client, or this beside a Kubernetes
// Secret, is refused here; a secret the source does not deliver is refused by
// the request that needs it.
func declaredOAuthSettings(o *config.OAuthClient, src secrets.Source) (settings.Store, error) {
	if !declaresOAuthClient(o) {
		return settings.NewMemory(settings.OAuthClient{}), nil
	}
	if o.SecretName != "" {
		return nil, errors.New(
			"secretName and id/provider both declare the client: use one way to declare the OAuth client")
	}
	if o.Provider == "" {
		return nil, errors.New("a client needs its secret: name the provider whose client-secret it is")
	}
	if src == nil {
		return nil, errors.New("provider: no secrets source is configured")
	}
	return settings.NewLazy(lazy.TTL, func(ctx context.Context) (settings.OAuthClient, error) {
		c := settings.OAuthClient{ID: o.ID, Declared: true}
		var err error
		if c.ID == "" {
			if c.ID, err = src.Get(ctx, secrets.ProviderClientID(o.Provider)); err != nil {
				return settings.OAuthClient{}, fmt.Errorf("provider: %w", err)
			}
		}
		if c.Secret, err = src.Get(ctx, secrets.ProviderClientSecret(o.Provider)); err != nil {
			return settings.OAuthClient{}, fmt.Errorf("provider: %w", err)
		}
		if !c.Configured() {
			return settings.OAuthClient{}, errors.New("provider: the client id and secret must not be empty")
		}
		return c, nil
	}), nil
}
