package srpauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/srp"
	"github.com/truvity/sluis/storage/state"
)

// ErrNoCredential is an account whose credential is not stored (yet): the
// account is not offered, and a try against it ends as one against an unknown
// username does.
var ErrNoCredential = errors.New("srpauth: no credential")

// Source reads an account's credential. It is read on first use and again
// after [lazy.TTL], so a rotated verifier takes effect without a restart and
// opening a door reads no secret.
type Source func(ctx context.Context) (srp.Credential, error)

// SourceOf is where an account's credential is kept. On layout v5 it is the
// fixed address of the account (`internal/oidc/<account>-verifier`) whatever the
// document names; otherwise it is the secret the document names, delivered by
// src. It returns nil when neither is available: the account is not offered.
func SourceOf(v5 *secretstore.StoresV5, src secrets.Source, account, name string) Source {
	var read func(context.Context) ([]byte, error)
	switch {
	case v5 != nil && account == RecoveryUser:
		read = func(ctx context.Context) ([]byte, error) { return readValue(ctx, v5.OIDC().RecoveryVerifier()) }
	case v5 != nil && account == ConformanceUser:
		read = func(ctx context.Context) ([]byte, error) { return readValue(ctx, v5.OIDC().ConformanceVerifier()) }
	case v5 == nil && src != nil && name != "":
		read = func(ctx context.Context) ([]byte, error) {
			text, err := src.Get(ctx, name)
			return []byte(text), err
		}
	default:
		return nil
	}
	return Cached(lazy.TTL, func(ctx context.Context) (srp.Credential, error) {
		raw, err := read(ctx)
		if errors.Is(err, state.ErrNotFound) || errors.Is(err, secrets.ErrNotFound) {
			return srp.Credential{}, fmt.Errorf("%w: the %s account's verifier is not there", ErrNoCredential, account)
		}
		if err != nil {
			return srp.Credential{}, fmt.Errorf("the %s account's verifier: %w", account, err)
		}
		cred, err := srp.Default.ParseCredential(raw)
		if err != nil {
			return srp.Credential{}, fmt.Errorf("the %s account's verifier: %w", account, err)
		}
		return cred, nil
	})
}

func readValue(ctx context.Context, v state.Value[[]byte]) ([]byte, error) {
	raw, _, err := v.Get(ctx)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty")
	}
	return raw, nil
}

// Cached makes a source of load that is read at most once per ttl.
func Cached(ttl time.Duration, load func(context.Context) (srp.Credential, error)) Source {
	return lazy.New(ttl, load).Get
}

// Fixed is a source over a credential in hand, for an account whose password
// the process holds (a generated one) or a test.
func Fixed(cred srp.Credential) Source {
	return func(context.Context) (srp.Credential, error) { return cred, nil }
}

// DecoyKey derives the secret the made-up credentials of unknown usernames
// come from, out of a key every container shares (the state codec's).
func DecoyKey(key func() ([]byte, error)) func() ([]byte, error) {
	return func() ([]byte, error) {
		k, err := key()
		if err != nil {
			return nil, err
		}
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte("sluis srp decoy"))
		return mac.Sum(nil), nil
	}
}
