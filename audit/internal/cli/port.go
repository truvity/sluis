package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/keys"
	"github.com/truvity/sluis/storage/cloudflare"
	skeys "github.com/truvity/sluis/storage/keys"
	kmskeys "github.com/truvity/sluis/storage/keys/kms"
	localkeys "github.com/truvity/sluis/storage/keys/local"
	transitkeys "github.com/truvity/sluis/storage/keys/transit"
	"github.com/truvity/sluis/storage/openbao"
	"github.com/truvity/sluis/storage/state"
	ssmstate "github.com/truvity/sluis/storage/state/ssm"
)

// The storage port is how an installation's keys and its internal state are
// reached: keys by purpose (seal, pseudonym, conceal, archive) and state by
// address. What follows builds both from a configuration file.

// stateAt opens the installation's state store at the address a StateRef names.
// The store is SSM Parameter Store under the root, with the process's own
// identity: on Lambda the function's role, on Kubernetes the pod's.
func stateAt(ctx context.Context, ref config.StateRef, region string) (state.Store, error) {
	var opts []state.Option
	if region != "" {
		opts = append(opts, state.WithRegion(region))
	}
	return ssmstate.Open(ctx, ref.Root, opts...)
}

// archiveCredentials is the static credential pair at the address of the
// installation's state store that archive.credentials names. The value is a
// JSON object {"accessKeyID": ..., "secretAccessKey": ...}; a field it lacks,
// or one it adds, is refused by name, and never printed.
func archiveCredentials(ctx context.Context, ref config.StateRef) (id, secret string, err error) {
	st, err := stateAt(ctx, ref, "")
	if err != nil {
		return "", "", fmt.Errorf("archive.credentials: %w", err)
	}
	return readCredentials(ctx, st, ref.Address)
}

// readCredentials reads the credential pair at address in st.
func readCredentials(ctx context.Context, st state.Store, address string) (id, secret string, err error) {
	item, err := st.Get(ctx, address)
	if err != nil {
		return "", "", fmt.Errorf("archive.credentials: reading %s: %w", address, err)
	}
	var v struct {
		AccessKeyID     string `json:"accessKeyID"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	dec := json.NewDecoder(bytes.NewReader(item.Value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return "", "", fmt.Errorf("archive.credentials: %s is not {accessKeyID, secretAccessKey}: %w", address, err)
	}
	if v.AccessKeyID == "" || v.SecretAccessKey == "" {
		return "", "", fmt.Errorf("archive.credentials: %s must hold both accessKeyID and secretAccessKey", address)
	}
	return v.AccessKeyID, v.SecretAccessKey, nil
}

// OpenKeyPort opens the keys of a storage-shaped keys block (adapter) and
// names, for the log, the adapter and alias of the seal key. The block has
// been checked by the loader (config.Keys).
func OpenKeyPort(ctx context.Context, k *config.Keys, secrets *config.Secrets) (set *skeys.Keys, sealName string, err error) {
	if !k.Storage() {
		return nil, "", errors.New("keys: not in the adapter shape")
	}
	cfg := skeys.Config{Adapter: k.Adapter, Keys: map[skeys.Purpose]skeys.Entry{}}
	for p, e := range map[skeys.Purpose]*skeys.Entry{
		skeys.Seal: k.Seal, skeys.Pseudonym: k.Pseudonym, skeys.Conceal: k.Conceal, skeys.Archive: k.Archive,
	} {
		if e != nil {
			cfg.Keys[p] = *e
		}
	}
	var backend skeys.Backend
	switch k.Adapter {
	case "kms":
		awscfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("keys: %w", err)
		}
		var opts []kmskeys.Option
		if k.State != nil {
			st, err := stateAt(ctx, config.StateRef{Root: k.State.Root + "/" + k.State.Address}, "")
			if err != nil {
				return nil, "", fmt.Errorf("keys.state: %w", err)
			}
			opts = append(opts, kmskeys.WithWrappedStore(kmskeys.FromState(st)))
		}
		backend = kmskeys.New(kms.NewFromConfig(awscfg), opts...)
	case "transit":
		oc := openbao.Config{Address: k.OpenBAO.Address, Namespace: k.OpenBAO.Namespace, CAFile: k.OpenBAO.CAFile}
		login, token, tokenFile, err := openBAOCredentials(ctx, *k.OpenBAO, secrets)
		if err != nil {
			return nil, "", err
		}
		switch {
		case login != nil:
			oc.Login = &openbao.Login{Mount: login.Mount, Role: login.Role, TokenFile: login.TokenFile}
		case tokenFile != "":
			return nil, "", errors.New("keys.openbao.tokenFile: the storage port signs in with a JWT login or a token from tokenSecret; use one of them")
		default:
			oc.Token = token
		}
		client, err := openbao.New(oc)
		if err != nil {
			return nil, "", fmt.Errorf("keys.openbao: %w", err)
		}
		var opts []transitkeys.Option
		if k.OpenBAO.Mount != "" {
			opts = append(opts, transitkeys.WithMount(k.OpenBAO.Mount))
		}
		backend = transitkeys.New(client, opts...)
	case "local":
		root, err := os.ReadFile(k.RootFile)
		if err != nil {
			return nil, "", fmt.Errorf("keys.rootFile: %w", err)
		}
		b, err := localkeys.New(root)
		if err != nil {
			return nil, "", fmt.Errorf("keys.rootFile: %w", err)
		}
		backend = b
	default:
		return nil, "", fmt.Errorf("keys.adapter %q is not one of kms, transit, local", k.Adapter)
	}
	set, err = skeys.Open(cfg, skeys.Options{Backend: backend, Instance: k.Instance})
	if err != nil {
		return nil, "", fmt.Errorf("keys: %w", err)
	}
	if k.Seal != nil {
		sealName = k.Adapter + ":" + k.Seal.Key
	}
	return set, sealName, nil
}

// OpenPortProvider is the key provider of a storage-shaped keys block, or nil
// where it names neither a pseudonym nor a conceal key.
func OpenPortProvider(ctx context.Context, k *config.Keys, secrets *config.Secrets) (keys.Provider, error) {
	set, _, err := OpenKeyPort(ctx, k, secrets)
	if err != nil {
		return nil, err
	}
	p, err := keys.NewPortProvider(set)
	if err != nil || p == nil {
		return nil, err
	}
	return p, nil
}

// mintInstance names the tokens an audit process mints: sluis/audit/<preset>/self/<time>.
const mintInstance = "audit"

// minterDocument is the credential the process mints R2 credentials with, at an
// address of the state store: {"schema": "cloudflare-minter/v1", "token": ...}.
type minterDocument struct {
	Schema string `json:"schema"`
	Token  string `json:"token"`
}

// mintedProvider is the credentials provider of a store whose R2 credentials
// are minted from a Cloudflare preset (storage/cloudflare, shared with sluis):
// the minter token is read from the state store at each mint, the prototype is
// checked at each mint, and the ids of the tokens minted are kept in the same
// store so the expired ones are deleted.
func mintedProvider(ctx context.Context, m MintedCredentials) (*cloudflare.Provider, error) {
	st, err := stateAt(ctx, config.StateRef{Root: m.Root}, "")
	if err != nil {
		return nil, fmt.Errorf("credentials_preset: %w", err)
	}
	life, err := m.Spec.LifetimeDuration()
	if err != nil {
		return nil, fmt.Errorf("credentials_preset: %w", err)
	}
	return cloudflare.NewProvider(cloudflare.ProviderConfig{
		Instance: mintInstance, Preset: string(m.Preset), Account: m.Spec.Account, Prototype: m.Spec.Prototype, Lifetime: life,
		Minter: func(ctx context.Context) (string, error) { return readMinter(ctx, st, m.Spec.Minter) },
		Record: st,
	})
}

// readMinter reads the minter token at address in st. Neither the token nor
// the document is ever put in an error.
func readMinter(ctx context.Context, st state.Store, address string) (string, error) {
	item, err := st.Get(ctx, address)
	if err != nil {
		return "", fmt.Errorf("credentials_preset.minter: reading %s: %w", address, err)
	}
	var d minterDocument
	if err := json.Unmarshal(item.Value, &d); err != nil || d.Schema != "cloudflare-minter/v1" || d.Token == "" {
		return "", fmt.Errorf("credentials_preset.minter: %s is not a cloudflare-minter/v1 document with a token", address)
	}
	return d.Token, nil
}
