// Package ssm registers the `ssm` Secrets adapter: the installation's secrets
// in AWS Systems Manager Parameter Store, in layout v4
// (docs/decisions/0041-the-secret-contract.md).
//
// The adapter has no port of its own. Choosing it puts the Secrets port over
// the layout-v4 stores of the serve document's `secrets` section
// (internal/secretstore), which keep the parameters at `<root>/internal/` and
// `<root>/external/`. This package holds the adapter's settings and their
// checks, so that a mistake in them is refused at start.
//
// Credentials are ambient: the AWS SDK's default chain (EKS Pod Identity,
// IRSA, a Lambda role, the environment). Nothing in the configuration holds a
// secret.
package ssm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/port"
)

// Config is where the parameters live.
type Config struct {
	// Root is the installation's parameter hierarchy, `/sluis/<instance>`:
	// a leading slash, no trailing one. Required.
	Root string
	// KMSKeyID, when set, is the id, ARN or alias of the customer-managed key
	// the SecureStrings are encrypted with. Empty is the AWS-managed key.
	KMSKeyID string
	// Region defaults to the SDK's own resolution (AWS_REGION, the profile).
	Region string
	// Endpoint overrides the service address, for LocalStack.
	Endpoint string
}

// rootOf checks the root.
func rootOf(root string) error {
	if root == "" {
		return errors.New("ssm: root is required: the installation's /sluis/<instance>")
	}
	if !strings.HasPrefix(root, "/") || strings.HasSuffix(root, "/") {
		return fmt.Errorf("ssm: root %q must begin with a slash and not end with one", root)
	}
	if err := port.CheckSecretPath(strings.TrimPrefix(root, "/")); err != nil {
		return fmt.Errorf("ssm: root %q: %w", root, err)
	}
	// An instance named like a namespace would nest its tree under another's.
	for _, seg := range strings.Split(strings.Trim(root, "/"), "/") {
		switch seg {
		case "private", "export", "internal", "external":
			return fmt.Errorf("ssm: root %q has a segment %q: an instance may not be named private, export, internal or external", root, seg)
		}
	}
	return nil
}

func init() {
	port.Register(port.Descriptor{
		Name: "ssm", Concern: port.ConcernSecrets,
		Summary:     "Dynamic secrets as SecureString parameters in AWS SSM Parameter Store (layout v4).",
		Requires:    port.Requires{AWS: true},
		Runtimes:    []port.Runtime{port.RuntimeKubernetes, port.RuntimeLambda},
		SecretStore: true,
		// The factory checks the settings and returns them; the port itself is
		// the layout-v4 stores, which the store package opens.
		Factory: func(_ context.Context, s port.Settings) (any, error) {
			var cfg Config
			if err := s.Decode(&cfg); err != nil {
				return nil, err
			}
			if err := rootOf(cfg.Root); err != nil {
				return nil, err
			}
			return &cfg, nil
		},
	})
}
