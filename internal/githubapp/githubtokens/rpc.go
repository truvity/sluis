package githubtokens

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/modcall"
)

// Module is the module that answers, and MethodMintInstallation its method.
const (
	Module                 = "github"
	MethodMintInstallation = "mint_installation"
)

// CallerIssuer is the one caller class that may ask for an installation token:
// the issuer decides the grant, and no other module has a reason to hold one.
const CallerIssuer = "issuer"

// The codes of the errors that cross the boundary.
const (
	codeUnavailable = "app_unavailable"
	codeScope       = "scope"
	codeUpstream    = "upstream"
)

// Register makes m answer the module's method on s.
func Register(s *modcall.Server, m Minter) {
	modcall.Handle(s, MethodMintInstallation, func(ctx context.Context, r Request) (Minted, error) {
		minted, err := m.MintInstallation(ctx, r)
		return minted, codeOf(err)
	}, modcall.Allow(CallerIssuer))
}

// codeOf is an error as the boundary tells it. An unavailable App and a scope
// refusal carry their reason, which the issuer already shows its own caller; an
// upstream failure carries nothing, since what GitHub or the key said is the
// log's.
func codeOf(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnavailable):
		return modcall.Coded(codeUnavailable, detail(err, ErrUnavailable))
	case errors.Is(err, ErrScope):
		return modcall.Coded(codeScope, detail(err, ErrScope))
	case errors.Is(err, ErrUpstream):
		return modcall.Coded(codeUpstream, "")
	}
	return err
}

// detail is err's text without the sentinel it starts with.
func detail(err, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// Client is a [Minter] in another module.
type Client struct{ c modcall.Caller }

var _ Minter = (*Client)(nil)

// NewClient calls the GitHub module through c.
func NewClient(c modcall.Caller) *Client { return &Client{c: c} }

// MintInstallation implements [Minter].
func (c *Client) MintInstallation(ctx context.Context, r Request) (Minted, error) {
	out, err := modcall.Do[Request, Minted](ctx, c.c, Module, MethodMintInstallation, r)
	if err != nil {
		return Minted{}, errOf(err)
	}
	if out.Token == "" {
		return Minted{}, errors.New("github: the module answered no installation token")
	}
	return out, nil
}

// errOf is the sentinel a code stands for, with the reason it carried.
func errOf(err error) error {
	var e *modcall.Error
	if !errors.As(err, &e) {
		return err
	}
	var sentinel error
	switch e.Code {
	case codeUnavailable:
		sentinel = ErrUnavailable
	case codeScope:
		sentinel = ErrScope
	case codeUpstream:
		sentinel = ErrUpstream
	default:
		return err
	}
	if e.Message == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, e.Message)
}
