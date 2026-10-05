package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/settings"
)

// declaredOAuthClient reads the client the document declares: its secrets by
// the names `provider` gives them (providers/google/<provider>/client-id and
// .../client-secret), the id instead as `id` where it is given. Nothing declared
// yields the zero client. Half a client, a secret the source does not deliver,
// or this beside a Kubernetes Secret is refused: one way to declare the OAuth
// client.
func declaredOAuthClient(ctx context.Context, o *config.OAuthClient, src secrets.Source) (settings.OAuthClient, error) {
	if o == nil || (o.ID == "" && o.Provider == "") {
		return settings.OAuthClient{}, nil
	}
	if o.SecretName != "" {
		return settings.OAuthClient{}, errors.New(
			"secretName and id/provider both declare the client: use one way to declare the OAuth client")
	}
	if o.Provider == "" {
		return settings.OAuthClient{}, errors.New("a client needs its secret: name the provider whose client-secret it is")
	}
	if src == nil {
		return settings.OAuthClient{}, errors.New("provider: no secrets source is configured")
	}
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
		return settings.OAuthClient{}, errors.New("the client id and secret must not be empty")
	}
	return c, nil
}
