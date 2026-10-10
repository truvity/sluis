//go:build lambda

package store

// The Lambda build's storage: the memory adapter (tests) and DynamoDB, with S3
// for blobs and KMS for the sealer. There is no cluster, no Valkey and no NATS,
// and none of their clients is linked in: internal/boundaries' zip guard holds
// the binary to it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
)

// Backend is the cluster's own objects; there are none on Lambda.
type Backend struct{}

type k8sConfig struct{}

func (c *Config) fromServeK8s(f *config.Serve) error {
	if f.Valkey != nil && f.Valkey.Address != "" {
		return errors.New("valkey.address: the Lambda build has no Valkey client")
	}
	return nil
}

func (c *Config) fromRosterK8s(*config.Roster) {}

// restPorts is what the DynamoDB adapter takes from the legacy one in a
// cluster: here it takes nothing, so the blobs and the sealer must be named.
func restPorts(_ context.Context, cfg Config, _ *slog.Logger) (port.Set, *Backend, error) {
	if cfg.Blob == nil {
		return port.Set{}, nil, errors.New("ports.blob: the Lambda build has no cluster to keep blobs in, " +
			"so ports.adapter: dynamodb needs the s3 blobs adapter")
	}
	return port.Set{}, nil, nil
}

// openK8s refuses the adapters that need a cluster.
func openK8s(_ context.Context, cfg Config, _ *slog.Logger) (*Stores, error) {
	return nil, fmt.Errorf("ports.adapter: %q needs Kubernetes or Valkey, neither of which is in the Lambda build; "+
		"use %q", cfg.Adapter, AdapterDynamoDB)
}

// valkeyConfigured is always false: the Lambda build has no Valkey.
func (Config) valkeyConfigured() bool { return false }
