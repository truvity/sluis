package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/settings"
)

// declaredOAuthClient reads the client declared by value, file or variable,
// which is the only way to declare one off-cluster. Nothing declared yields
// the zero client. Half a client, an unreadable file, or this beside a
// Kubernetes Secret is refused: one way to declare the OAuth client.
func declaredOAuthClient(o *config.OAuthClient) (settings.OAuthClient, error) {
	hasID := o.ID != "" || o.IDFile != ""
	hasSecret := o.SecretFile != "" || o.SecretEnv != ""
	if !hasID && !hasSecret {
		return settings.OAuthClient{}, nil
	}
	if o.SecretName != "" {
		return settings.OAuthClient{}, errors.New(
			"secretName and id/idFile/secretFile/secretEnv both declare the client: use one way to declare the OAuth client")
	}
	if o.ID != "" && o.IDFile != "" {
		return settings.OAuthClient{}, errors.New("id and idFile are exclusive")
	}
	if o.SecretFile != "" && o.SecretEnv != "" {
		return settings.OAuthClient{}, errors.New("secretFile and secretEnv are exclusive")
	}
	if !hasID || !hasSecret {
		return settings.OAuthClient{}, errors.New("a client needs both an id (id or idFile) and a secret (secretFile or secretEnv)")
	}
	c := settings.OAuthClient{ID: o.ID, Declared: true}
	var err error
	if o.IDFile != "" {
		if c.ID, err = readTrimmed(o.IDFile); err != nil {
			return settings.OAuthClient{}, fmt.Errorf("idFile: %w", err)
		}
	}
	if o.SecretFile != "" {
		if c.Secret, err = readTrimmed(o.SecretFile); err != nil {
			return settings.OAuthClient{}, fmt.Errorf("secretFile: %w", err)
		}
	} else if c.Secret, err = config.Secret(o.SecretEnv); err != nil {
		return settings.OAuthClient{}, fmt.Errorf("secretEnv: %w", err)
	}
	if !c.Configured() {
		return settings.OAuthClient{}, errors.New("the client id and secret must not be empty")
	}
	return c, nil
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
