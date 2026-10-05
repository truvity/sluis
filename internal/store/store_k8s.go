//go:build !lambda

package store

// The Kubernetes build's storage: today's ConfigMaps and Secrets and Valkey. The
// Lambda build (store_lambda.go) has none of them, so its binary carries no
// client-go and no Valkey client.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/legacy"
	"github.com/truvity/sluis/internal/port/observe"
	"github.com/truvity/sluis/internal/valkey"
)

// Backend is today's storage as it was opened.
type Backend = legacy.Backend

// k8sConfig is what only the Kubernetes build configures.
type k8sConfig struct {
	// Valkey is where the shared cache is; an empty address is none.
	Valkey valkey.Config
	// ValkeyLoginSecret names its password, resolved through Secrets when
	// the Valkey is opened. Empty is none.
	ValkeyLoginSecret string
	// KubeClient, when set, opens the namespace's objects in place of the
	// pod's own ServiceAccount: an operator's tool that runs from a
	// workstation (sluis migrate) names a kubeconfig this way.
	KubeClient func(release string) (*kube.Client, error)
}

func (c *Config) fromServeK8s(f *config.Serve) error {
	c.Valkey = valkeyOf(f.Release, f.Valkey)
	if f.Valkey != nil {
		c.ValkeyLoginSecret = f.Valkey.LoginSecret
	}
	return nil
}

// kubeBackend opens the namespace's objects as far as the configuration needs.
func kubeBackend(ctx context.Context, cfg Config, log *slog.Logger) (*legacy.Backend, error) {
	backend := &legacy.Backend{}
	if cfg.Kube == KubeNone {
		return backend, nil
	}
	client, err := cfg.kubeClient()
	switch {
	case err == nil:
		backend.Kube = client
		backend.ReviewToken = client.ReviewToken
	case cfg.Kube == KubeRequired:
		return nil, err
	default:
		log.WarnContext(ctx, "the namespace's objects are not available", "error", err)
	}
	return backend, nil
}

// restPorts is what the DynamoDB adapter takes from the legacy one.
func restPorts(ctx context.Context, cfg Config, log *slog.Logger) (port.Set, *Backend, error) {
	backend, err := kubeBackend(ctx, cfg, log)
	if err != nil {
		return port.Set{}, nil, err
	}
	return backend.Ports(legacy.Options{}), backend, nil
}

// openK8s opens the `legacy` adapter.
func openK8s(ctx context.Context, cfg Config, log *slog.Logger) (*Stores, error) {
	return openLegacy(ctx, cfg, log)
}

func valkeyOf(release string, v *config.Valkey) valkey.Config {
	c := valkey.Config{Cluster: true, Prefix: orDefault(release, "sluis")}
	if v != nil {
		c.Address = v.Address
		c.TLS = v.TLS
		if v.Cluster != nil {
			c.Cluster = *v.Cluster
		}
	}
	return c
}

// valkeyPassword resolves the password the document names, when it names one.
func valkeyPassword(ctx context.Context, cfg Config) (string, error) {
	if cfg.ValkeyLoginSecret == "" {
		return "", nil
	}
	if cfg.Secrets == nil {
		return "", errors.New("valkey.passwordSecret: no secrets source is configured")
	}
	password, err := cfg.Secrets.Get(ctx, cfg.ValkeyLoginSecret)
	if err != nil {
		return "", fmt.Errorf("valkey.passwordSecret: %w", err)
	}
	return password, nil
}

func openLegacy(ctx context.Context, cfg Config, log *slog.Logger) (*Stores, error) {
	backend := &legacy.Backend{}
	st := &Stores{Backend: backend, Adapter: AdapterLegacy}

	if cfg.Kube != KubeNone {
		client, err := cfg.kubeClient()
		switch {
		case err == nil:
			backend.Kube = client
			backend.ReviewToken = client.ReviewToken
		case cfg.Kube == KubeRequired:
			return nil, err
		default:
			log.WarnContext(ctx, "the namespace's objects are not available", "error", err)
		}
	}

	if cfg.Valkey.Address != "" {
		password, err := valkeyPassword(ctx, cfg)
		if err != nil {
			return nil, err
		}
		cfg.Valkey.Password = password
		shared, err := valkey.OpenState(ctx, cfg.Valkey)
		if err != nil {
			return nil, err
		}
		backend.Valkey = shared
		st.Shared, st.Usable, st.pinger = true, true, shared
		st.close = func() { _ = shared.Close() }
		log.InfoContext(ctx, "sharing state in Valkey",
			"cache", "valkey", "address", cfg.Valkey.Address, "cluster", cfg.Valkey.Cluster)
	}
	// Observed once, here, where the adapter is chosen: every caller crosses
	// the same seam, so every call is timed and counted without each of them
	// knowing.
	set, err := cfg.compose(ctx, backend.Ports(legacy.Options{}), log)
	if err != nil {
		st.Close()
		return nil, err
	}
	st.Ports = observe.Set(set)
	return st, nil
}

// kubeClient opens the namespace's objects.
func (c Config) kubeClient() (*kube.Client, error) {
	if c.KubeClient != nil {
		return c.KubeClient(c.Release)
	}
	return kube.InCluster(c.Release)
}
