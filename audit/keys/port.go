package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	skeys "github.com/truvity/sluis/storage/keys"
)

// This file is the way into the storage port (github.com/truvity/sluis/storage/keys):
// the keys of an installation are named there by purpose (seal, pseudonym,
// conceal, archive), by alias, and audit asks for them by that name. The types
// in this file adapt a port key to what the rest of this package asks of one:
// a Provider and Sealer for the pseudonym and conceal purposes, a Signer for
// the seal purpose. They are not deprecated; the providers and signers beside
// them are.

// PortProvider is a Provider and Sealer over the pseudonym and conceal keys of
// a storage port.
//
// A pseudonym is the port's MAC under a secret unique to (purpose, tenant): the
// per-tenant secret is generated once, wrapped under the pseudonym key and
// kept in the installation's state store, so no replica can mint a different
// one. The audit purpose (a profile's name) and the tenant are folded into the
// port's tenant, which keeps the property the first releases had: the same
// person has an unrelated pseudonym in every profile.
//
// What it cannot do is Destroy. A port key is one key for the installation, not
// one per tenant, so there is no key of a tenant's to destroy; ending a tenant's
// pseudonyms is deleting its wrapped secrets from the state store, which the
// port does not offer as an operation yet.
type PortProvider struct {
	PseudonymKey *skeys.Key
	ConcealKey   *skeys.Key
}

var (
	_ Provider = (*PortProvider)(nil)
	_ Sealer   = (*PortProvider)(nil)
)

// NewPortProvider returns the provider over the keys a storage port opened, or
// nil when it holds neither a pseudonym nor a conceal key (a deployment without
// pseudonyms).
func NewPortProvider(set *skeys.Keys) (*PortProvider, error) {
	p := &PortProvider{}
	var err error
	if p.PseudonymKey, err = optional(set, skeys.Pseudonym); err != nil {
		return nil, err
	}
	if p.ConcealKey, err = optional(set, skeys.Conceal); err != nil {
		return nil, err
	}
	if p.PseudonymKey == nil && p.ConcealKey == nil {
		return nil, nil
	}
	return p, nil
}

func optional(set *skeys.Keys, p skeys.Purpose) (*skeys.Key, error) {
	k, err := set.For(p)
	switch {
	case errors.Is(err, skeys.ErrNotConfigured):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return k, nil
}

// scope is the port's tenant for an audit tenant and purpose. The two names
// are checked against safeName, which has no "/", so the pair cannot be read
// two ways.
func scope(tenant string, purpose Purpose) string { return string(purpose) + "/" + tenant }

// Pseudonym implements Provider.
func (p *PortProvider) Pseudonym(ctx context.Context, tenant string, purpose Purpose, identifier string) (string, error) {
	if err := checkName(tenant, purpose); err != nil {
		return "", err
	}
	if identifier == "" {
		return "", nil
	}
	if p.PseudonymKey == nil {
		return "", fmt.Errorf("keys: no pseudonym key is configured (keys.pseudonym): %w", skeys.ErrNotConfigured)
	}
	mac, err := p.PseudonymKey.MAC(ctx, scope(tenant, purpose), []byte(identifier))
	if err != nil {
		return "", fmt.Errorf("keys: pseudonym for %s/%s: %w", purpose, tenant, err)
	}
	return PseudonymPrefix + base64.RawURLEncoding.EncodeToString(mac), nil
}

// Destroy implements Provider. It is refused: see PortProvider.
func (p *PortProvider) Destroy(_ context.Context, tenant string, purpose Purpose) error {
	return fmt.Errorf("keys: cannot destroy the key of %s/%s: the keys of an installation are named by purpose, not per tenant, "+
		"so there is no key of a tenant's to destroy through the storage port: %w", purpose, tenant, skeys.ErrUnsupported)
}

// Close implements Provider.
func (p *PortProvider) Close() error { return nil }

// Seal implements Sealer under the conceal key. The tenant and purpose are
// sealed with the value and checked on Open, so a sealed identifier does not
// open as another tenant's.
func (p *PortProvider) Seal(ctx context.Context, tenant string, purpose Purpose, plaintext []byte) ([]byte, error) {
	if err := checkName(tenant, purpose); err != nil {
		return nil, err
	}
	if p.ConcealKey == nil {
		return nil, fmt.Errorf("keys: no conceal key is configured (keys.conceal): %w", skeys.ErrNotConfigured)
	}
	return p.ConcealKey.Encrypt(ctx, append([]byte(scope(tenant, purpose)+"\x00"), plaintext...))
}

// Open implements Sealer.
func (p *PortProvider) Open(ctx context.Context, tenant string, purpose Purpose, sealed []byte) ([]byte, error) {
	if err := checkName(tenant, purpose); err != nil {
		return nil, err
	}
	if p.ConcealKey == nil {
		return nil, fmt.Errorf("keys: no conceal key is configured (keys.conceal): %w", skeys.ErrNotConfigured)
	}
	plain, err := p.ConcealKey.Decrypt(ctx, sealed)
	if err != nil {
		return nil, err
	}
	rest, ok := strings.CutPrefix(string(plain), scope(tenant, purpose)+"\x00")
	if !ok {
		return nil, skeys.ErrDecrypt
	}
	return []byte(rest), nil
}

// PortSigner is a Signer over the seal key of a storage port: an asymmetric
// key (ECC P-384, ES384) the private half of which never leaves the key
// service.
type PortSigner struct {
	Key *skeys.Key
	// Name is what KeyID answers, the adapter and alias of the key
	// ("kms:alias/audit-seal").
	Name string
}

var _ Signer = (*PortSigner)(nil)

// NewPortSigner returns the signer over the seal purpose of a storage port.
func NewPortSigner(set *skeys.Keys, name string) (*PortSigner, error) {
	k, err := set.For(skeys.Seal)
	if err != nil {
		return nil, err
	}
	return &PortSigner{Key: k, Name: name}, nil
}

// Sign implements Signer: ECDSA over a SHA-384 of the message, ASN.1 encoded.
func (s *PortSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	pub, err := s.Key.PublicKey(ctx)
	if err != nil {
		return nil, err
	}
	if pub.Algorithm != "ES384" {
		return nil, fmt.Errorf("keys: %s is a %s key; seals are signed with an ECC P-384 key (ES384)", s.Name, pub.Algorithm)
	}
	sum := sha512.Sum384(message)
	sig, err := s.Key.Sign(ctx, sum[:])
	if err != nil {
		return nil, fmt.Errorf("keys: sign with %s: %w", s.Name, err)
	}
	return sig.Value, nil
}

// PublicKey implements Signer.
func (s *PortSigner) PublicKey(ctx context.Context) ([]byte, error) {
	pub, err := s.Key.PublicKey(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := pub.Key.(*ecdsa.PublicKey); !ok {
		return nil, fmt.Errorf("keys: %s is not an ECDSA key", s.Name)
	}
	der, err := x509.MarshalPKIXPublicKey(pub.Key)
	if err != nil {
		return nil, fmt.Errorf("keys: public key of %s: %w", s.Name, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// KeyID implements Signer.
func (s *PortSigner) KeyID() string { return s.Name }
