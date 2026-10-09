// Package rpc is the signer as another module sees it (docs/decisions/0071
// D204 c): signing, the published keys and the read-only directory of the key
// rings, as methods of the `signer` module over internal/modcall. A caller
// holds a [signer.Signer] and a [signer.Directory]; the in-process signer is
// both, so a process that has the key rings keeps calling them directly, and a
// process that does not holds a [Client] that calls the module that does.
//
// The private key never crosses the boundary: only a signed token, the public
// keys and the names of the algorithms and key ids. [signer.Directory.Secret]
// does not cross either: the client answers nil, see [Client.Secret].
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/signer"
)

// Module is the name of the module and the methods it answers.
const (
	Module           = "signer"
	MethodSign       = "sign"
	MethodPublicKeys = "publicKeys"
	MethodDirectory  = "directory"
)

// The codes of the errors that cross the boundary: the three refusals a caller
// tells apart by name.
const (
	codeUnknownPurpose = "unknown_purpose"
	codeLifetime       = "lifetime"
	codeNoKey          = "no_key"
)

type signRequest struct {
	Purpose   string `json:"purpose"`
	Algorithm string `json:"algorithm,omitempty"`
	Payload   []byte `json:"payload"`
}

type signResponse struct {
	Token     string `json:"token"`
	KID       string `json:"kid"`
	Algorithm string `json:"algorithm"`
}

type publicKeysResponse struct {
	// Keys are JWKs: the key, its kid and its algorithm.
	Keys []json.RawMessage `json:"keys"`
}

// directoryResponse is the whole read-only view of the rings at one moment.
type directoryResponse struct {
	Default    string            `json:"default"`
	Configured []string          `json:"configured"`
	Algorithms []string          `json:"algorithms"`
	Active     map[string]string `json:"active"`
}

// Register makes sg and dir answer the module's methods on s.
func Register(s *modcall.Server, sg signer.Signer, dir signer.Directory) {
	modcall.Handle(s, MethodSign, func(ctx context.Context, r signRequest) (signResponse, error) {
		out, err := sg.Sign(ctx, signer.Request{
			Purpose:   signer.Purpose(r.Purpose),
			Algorithm: jose.SignatureAlgorithm(r.Algorithm),
			Payload:   r.Payload,
		})
		if err != nil {
			return signResponse{}, codeOf(err)
		}
		return signResponse{Token: out.Token, KID: out.KID, Algorithm: string(out.Algorithm)}, nil
	})
	modcall.Handle(s, MethodPublicKeys, func(ctx context.Context, _ struct{}) (publicKeysResponse, error) {
		keys, err := sg.PublicKeys(ctx)
		if err != nil {
			return publicKeysResponse{}, err
		}
		out := publicKeysResponse{Keys: make([]json.RawMessage, 0, len(keys))}
		for _, k := range keys {
			raw, err := json.Marshal(jose.JSONWebKey{Key: k.Key, KeyID: k.KID, Algorithm: string(k.Algorithm), Use: "sig"})
			if err != nil {
				return publicKeysResponse{}, err
			}
			out.Keys = append(out.Keys, raw)
		}
		return out, nil
	})
	modcall.Handle(s, MethodDirectory, func(ctx context.Context, _ struct{}) (directoryResponse, error) {
		dir.Maintain(ctx)
		out := directoryResponse{Default: string(dir.Default()), Active: map[string]string{}}
		for _, a := range dir.Configured() {
			out.Configured = append(out.Configured, string(a))
			if kid, ok := dir.ActiveKID(a); ok {
				out.Active[string(a)] = kid
			}
		}
		for _, a := range dir.Algorithms() {
			out.Algorithms = append(out.Algorithms, string(a))
		}
		return out, nil
	})
}

// codeOf is an error as the boundary tells it: the refusals by name and
// nothing else, since what went wrong inside the signer is its own log's.
func codeOf(err error) error {
	switch {
	case errors.Is(err, signer.ErrUnknownPurpose):
		return modcall.Coded(codeUnknownPurpose, "")
	case errors.Is(err, signer.ErrLifetime):
		return modcall.Coded(codeLifetime, "")
	case errors.Is(err, signer.ErrNoKey):
		return modcall.Coded(codeNoKey, "")
	}
	return err
}

// errOf is the sentinel a code stands for.
func errOf(err error) error {
	var e *modcall.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case codeUnknownPurpose:
		return signer.ErrUnknownPurpose
	case codeLifetime:
		return signer.ErrLifetime
	case codeNoKey:
		return signer.ErrNoKey
	}
	return err
}

// DefaultTTL is how long a [Client] reuses the directory it read.
const DefaultTTL = 10 * time.Second

// Client is a [signer.Signer] and a [signer.Directory] in another module.
//
// The directory methods take no context and return no error, and the issuer
// calls them on its request path, so the client reads the whole directory in
// one call and reuses it for TTL; [Client.Maintain] is where it is refreshed.
// A signer that cannot be reached leaves the last directory in place, and an
// empty one if there never was any, and says so in the log.
type Client struct {
	c   modcall.Caller
	log *slog.Logger

	// TTL is how long a read directory is reused; [DefaultTTL] when zero.
	TTL time.Duration
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu   sync.Mutex
	dir  directoryResponse
	read time.Time
	ok   bool
}

var (
	_ signer.Signer    = (*Client)(nil)
	_ signer.Directory = (*Client)(nil)
)

// NewClient calls the signer module through c.
func NewClient(c modcall.Caller, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{c: c, log: log}
}

// Sign implements [signer.Signer].
func (c *Client) Sign(ctx context.Context, req signer.Request) (signer.Signed, error) {
	out, err := modcall.Do[signRequest, signResponse](ctx, c.c, Module, MethodSign, signRequest{
		Purpose: string(req.Purpose), Algorithm: string(req.Algorithm), Payload: req.Payload,
	})
	if err != nil {
		return signer.Signed{}, errOf(err)
	}
	if out.Token == "" {
		return signer.Signed{}, errors.New("signer: the module answered no token")
	}
	return signer.Signed{Token: out.Token, KID: out.KID, Algorithm: jose.SignatureAlgorithm(out.Algorithm)}, nil
}

// PublicKeys implements [signer.Signer].
func (c *Client) PublicKeys(ctx context.Context) ([]signer.PublicKey, error) {
	out, err := modcall.Do[struct{}, publicKeysResponse](ctx, c.c, Module, MethodPublicKeys, struct{}{})
	if err != nil {
		return nil, errOf(err)
	}
	keys := make([]signer.PublicKey, 0, len(out.Keys))
	for _, raw := range out.Keys {
		var k jose.JSONWebKey
		if err = json.Unmarshal(raw, &k); err != nil {
			return nil, fmt.Errorf("signer: the module answered a key that is not valid: %w", err)
		}
		if !k.Valid() || !k.IsPublic() {
			return nil, errors.New("signer: the module answered a key that is not a public key")
		}
		keys = append(keys, signer.PublicKey{KID: k.KeyID, Algorithm: jose.SignatureAlgorithm(k.Algorithm), Key: k.Key})
	}
	return keys, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// refresh reads the directory when it is older than the TTL, or never was read.
func (c *Client) refresh(ctx context.Context) {
	ttl := c.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.now().Sub(c.read) < ttl {
		return
	}
	out, err := modcall.Do[struct{}, directoryResponse](ctx, c.c, Module, MethodDirectory, struct{}{})
	if err != nil {
		c.log.WarnContext(ctx, "the signer's directory could not be read", slog.Any("error", err))
		return
	}
	c.dir, c.read, c.ok = out, c.now(), true
}

// view is the directory as last read; it reads once if it never was.
func (c *Client) view() directoryResponse {
	c.mu.Lock()
	ok := c.ok
	c.mu.Unlock()
	if !ok {
		c.refresh(context.Background())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dir
}

// Maintain implements [signer.Directory]: the signer maintains its rings on
// every call it answers, so here it only refreshes the directory read.
func (c *Client) Maintain(ctx context.Context) { c.refresh(ctx) }

// Default implements [signer.Directory].
func (c *Client) Default() jose.SignatureAlgorithm { return jose.SignatureAlgorithm(c.view().Default) }

// Has implements [signer.Directory].
func (c *Client) Has(alg jose.SignatureAlgorithm) bool {
	return slices.Contains(c.view().Configured, string(alg))
}

// Configured implements [signer.Directory].
func (c *Client) Configured() []jose.SignatureAlgorithm { return algs(c.view().Configured) }

// Algorithms implements [signer.Directory].
func (c *Client) Algorithms() []jose.SignatureAlgorithm { return algs(c.view().Algorithms) }

// ActiveKID implements [signer.Directory].
func (c *Client) ActiveKID(alg jose.SignatureAlgorithm) (string, bool) {
	kid, ok := c.view().Active[string(alg)]
	return kid, ok
}

// Secret implements [signer.Directory]. It answers nil and asks the signer
// nothing: a secret derived from the signing key's seed is key material, and
// the seed never leaves the signer. The one caller, the issuer's cache of dead
// refresh tokens, takes nil as "no seed" and draws a key at random for its own
// process, which is all that cache needs: it lives in the process's memory.
func (c *Client) Secret(string) []byte { return nil }

func algs(in []string) []jose.SignatureAlgorithm {
	out := make([]jose.SignatureAlgorithm, len(in))
	for i, a := range in {
		out[i] = jose.SignatureAlgorithm(a)
	}
	return out
}
