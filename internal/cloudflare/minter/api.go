// Package minter does what sluis does as the STS for Cloudflare: clone a
// preset's prototype into a short-lived account token, keep the stored one
// fresh on a schedule, sweep its own expired tokens, and mint on demand for a
// granted caller. It works over [API], so no test and no package other than
// internal/cloudflare/cfapi talks to Cloudflare.
package minter

import (
	"context"

	"github.com/truvity/sluis/internal/cloudflare"
)

// API is one Cloudflare account, reached with its minter credential.
//
// Cloudflare documents 1,200 API requests per 5 minutes per user (exceeding
// them blocks the API for 5 minutes) and 500 account tokens per account.
// A rotation costs a get (the prototype), a create and, once per rotation, a
// list and one delete per expired token; the permission groups are cached.
type API interface {
	// GetToken returns an account token, or [cloudflare.ErrNotFound].
	GetToken(ctx context.Context, id string) (cloudflare.Token, error)
	// CreateToken makes an account token and returns its value, shown once.
	CreateToken(ctx context.Context, in cloudflare.NewToken) (cloudflare.Created, error)
	// DeleteToken deletes an account token. A token that is gone is
	// [cloudflare.ErrNotFound].
	DeleteToken(ctx context.Context, id string) error
	// ListTokens lists the account's tokens, the expired ones included.
	ListTokens(ctx context.Context) ([]cloudflare.Token, error)
	// PermissionGroups maps a permission group's id to its name.
	PermissionGroups(ctx context.Context) (map[string]string, error)
}

// Dialer opens an account with the minter credential's token value. Nothing is
// kept between calls: the credential is read when it is used.
type Dialer func(ctx context.Context, accountID, minterToken string) (API, error)
