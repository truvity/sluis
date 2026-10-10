// Package githubtokens is how another module asks the GitHub module for an
// installation token (docs/decisions/0071): a [Minter] port, the in-process
// implementation that holds the App's key and talks to GitHub, and the
// modcall pair ([Register], [Client]) that carries the same call as the
// `github` module's `mint_installation` method.
//
// The split is the decision and the credential. The issuer decides which grant
// a request is minted under and what it is narrowed to; whoever implements the
// [Minter] holds the App's private key, signs for it and calls GitHub. Until
// the GitHub module has a function of its own, the issuer's process holds the
// [InProcess] minter and nothing crosses a boundary; the pair is the seam the
// split will use, exercised today over modcall.Local.
package githubtokens

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
)

// The refusals minting ends in. They are the issuer's (`invalid_target`,
// `invalid_scope`, `server_error`) and survive the module boundary by code.
var (
	// ErrUnavailable is an App that cannot mint: not created, not installed,
	// or uninstalled on GitHub since.
	ErrUnavailable = errors.New("that GitHub App cannot mint a token")
	// ErrScope is a narrowing wider than the installation: a repository the
	// installer did not select, or a permission they have not accepted.
	ErrScope = errors.New("the request is wider than any grant this proof holds")
	// ErrUpstream is GitHub failing, or the App's key failing.
	ErrUpstream = errors.New("GitHub did not mint the token")
)

// Request is one installation token asked for: an App of the catalogue and
// what the token is cut down to. The decision is already made.
type Request struct {
	// App is the catalogue id, without the `github-app:` prefix.
	App string `json:"app"`
	// Repositories are names within the installation's account; empty narrows
	// to none.
	Repositories []string `json:"repositories,omitempty"`
	// Permissions are name to level; empty narrows to none.
	Permissions map[string]string `json:"permissions,omitempty"`
}

// Narrowing is the request's cut as the GitHub client spells it.
func (r Request) Narrowing() githubapp.Narrowing {
	return githubapp.Narrowing{Repositories: r.Repositories, Permissions: r.Permissions}
}

// Minted is an installation token and what GitHub says it carries. It holds the
// secret; nothing logs it.
type Minted struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// Repositories and Permissions are what was granted, not what was asked.
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
	// Installation is the GitHub installation it was minted in, for the trail.
	Installation int64 `json:"installation,omitempty"`
}

// Granted is the part GitHub minted, as the GitHub client spells it.
func (m Minted) Granted() githubapp.MintedToken {
	return githubapp.MintedToken{Token: m.Token, ExpiresAt: m.ExpiresAt, Repositories: m.Repositories, Permissions: m.Permissions}
}

// Minter asks GitHub for an installation token.
type Minter interface {
	// MintInstallation mints for the request. The error wraps [ErrUnavailable],
	// [ErrScope] or [ErrUpstream].
	MintInstallation(ctx context.Context, r Request) (Minted, error)
}

// AppStore is where a created App's record and key are kept.
type AppStore interface {
	Get(ctx context.Context, id string) (catalogueapp.Record, string, bool, error)
}

// InProcess is the [Minter] that holds the keys: it reads the App, signs its
// JWT and calls GitHub.
type InProcess struct {
	// Store is nil where the deployment keeps no catalogue Apps; every
	// request is then refused as naming an App that cannot mint.
	Store AppStore
	// HTTP reaches GitHub. Nil uses a client with a thirty-second timeout.
	HTTP *http.Client
	// Now is the clock the App's JWT is signed against. Nil is time.Now.
	Now func() time.Time
}

var _ Minter = InProcess{}

// MintInstallation implements [Minter].
func (m InProcess) MintInstallation(ctx context.Context, r Request) (Minted, error) {
	if m.Store == nil {
		return Minted{}, fmt.Errorf("%w: this deployment keeps no catalogue Apps", ErrUnavailable)
	}
	record, key, found, err := m.Store.Get(ctx, r.App)
	switch {
	case err != nil:
		return Minted{}, fmt.Errorf("%w: read App %q: %w", ErrUpstream, r.App, err)
	case !found:
		return Minted{}, fmt.Errorf("%w: App %q is declared and has not been created", ErrUnavailable, r.App)
	case !record.Installed() || key == "":
		return Minted{}, fmt.Errorf("%w: App %q is created and not installed", ErrUnavailable, r.App)
	}
	out := Minted{Installation: record.InstallationID}

	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	appToken, err := githubapp.AppToken(record.AppID, key, now())
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	minted, err := githubapp.InstallationTokenFor(ctx, client, appToken, record.InstallationID, r.Narrowing())
	var status *githubapp.StatusError
	switch {
	case errors.As(err, &status) && status.Code == http.StatusNotFound:
		return out, fmt.Errorf("%w: GitHub no longer knows App %q's installation: %w", ErrUnavailable, r.App, err)
	case errors.As(err, &status) && status.Code == http.StatusUnprocessableEntity:
		return out, fmt.Errorf("%w: %w", ErrScope, err)
	case err != nil:
		return out, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	out.Token, out.ExpiresAt = minted.Token, minted.ExpiresAt
	out.Repositories, out.Permissions = minted.Repositories, minted.Permissions
	return out, nil
}
