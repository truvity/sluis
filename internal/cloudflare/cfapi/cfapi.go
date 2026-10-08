// Package cfapi is the real client of Cloudflare's API for the minter: an
// account reached with a token, over cloudflare-go v7. It is the only package
// that imports the SDK; everything else speaks [minter.API].
//
// A token's policies and condition are carried as the JSON Cloudflare sent, so a
// clone copies them exactly and does not depend on the SDK's types for create
// (which only marshal).
package cfapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/option"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
)

// Client is one account.
type Client struct {
	account string
	api     *cf.Client
}

var _ minter.API = (*Client)(nil)

// Dial opens an account with a token. Extra options are for tests (a base URL).
func Dial(opts ...option.RequestOption) minter.Dialer {
	return func(_ context.Context, accountID, token string) (minter.API, error) {
		if accountID == "" || token == "" {
			return nil, errors.New("cfapi: an account id and a token are required")
		}
		all := append([]option.RequestOption{option.WithAPIToken(token), option.WithMaxRetries(2)}, opts...)
		return &Client{account: accountID, api: cf.NewClient(all...)}, nil
	}
}

// wire is a token as the API sends it.
type wire struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	ExpiresOn *time.Time      `json:"expires_on"`
	Policies  json.RawMessage `json:"policies"`
	Condition json.RawMessage `json:"condition"`
	Value     string          `json:"value"`
}

func (w wire) token() cloudflare.Token {
	t := cloudflare.Token{ID: w.ID, Name: w.Name, Status: w.Status, Policies: w.Policies, Condition: w.Condition}
	if w.ExpiresOn != nil {
		t.ExpiresOn = *w.ExpiresOn
	}
	return t
}

// notFound maps a 404 to [cloudflare.ErrNotFound]. The SDK's error does not
// carry the body of a call, so nothing here can leak a value.
func notFound(err error) error {
	var apiErr *cf.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: HTTP %d", cloudflare.ErrNotFound, apiErr.StatusCode)
	}
	return err
}

// GetToken implements [minter.API].
func (c *Client) GetToken(ctx context.Context, id string) (cloudflare.Token, error) {
	var env struct {
		Result wire `json:"result"`
	}
	_, err := c.api.Accounts.Tokens.Get(ctx, id, accounts.TokenGetParams{AccountID: cf.F(c.account)},
		option.WithResponseBodyInto(&env))
	if err != nil {
		return cloudflare.Token{}, notFound(err)
	}
	return env.Result.token(), nil
}

// CreateToken implements [minter.API].
func (c *Client) CreateToken(ctx context.Context, in cloudflare.NewToken) (cloudflare.Created, error) {
	body := map[string]any{"name": in.Name, "policies": in.Policies, "expires_on": in.ExpiresOn.UTC().Format(time.RFC3339)}
	if len(in.Condition) > 0 {
		body["condition"] = in.Condition
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return cloudflare.Created{}, err
	}
	var env struct {
		Result wire `json:"result"`
	}
	_, err = c.api.Accounts.Tokens.New(ctx, accounts.TokenNewParams{AccountID: cf.F(c.account)},
		option.WithRequestBody("application/json", raw), option.WithResponseBodyInto(&env))
	if err != nil {
		return cloudflare.Created{}, err
	}
	out := cloudflare.Created{ID: env.Result.ID, Name: env.Result.Name, Value: env.Result.Value}
	if env.Result.ExpiresOn != nil {
		out.ExpiresOn = *env.Result.ExpiresOn
	}
	return out, nil
}

// DeleteToken implements [minter.API].
func (c *Client) DeleteToken(ctx context.Context, id string) error {
	_, err := c.api.Accounts.Tokens.Delete(ctx, id, accounts.TokenDeleteParams{AccountID: cf.F(c.account)})
	return notFound(err)
}

// ListTokens implements [minter.API]: every page, the recently expired
// included.
func (c *Client) ListTokens(ctx context.Context) ([]cloudflare.Token, error) {
	var out []cloudflare.Token
	for page := 1; ; page++ {
		var env struct {
			Result     []wire `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		_, err := c.api.Accounts.Tokens.List(ctx, accounts.TokenListParams{
			AccountID: cf.F(c.account), IncludeExpired: cf.F(true), PerPage: cf.F(50.0), Page: cf.F(float64(page)),
		}, option.WithResponseBodyInto(&env))
		if err != nil {
			return nil, err
		}
		for _, w := range env.Result {
			out = append(out, w.token())
		}
		if page >= env.ResultInfo.TotalPages || len(env.Result) == 0 {
			return out, nil
		}
	}
}

// PermissionGroups implements [minter.API].
func (c *Client) PermissionGroups(ctx context.Context) (map[string]string, error) {
	var env struct {
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	_, err := c.api.Accounts.Tokens.PermissionGroups.List(ctx, accounts.TokenPermissionGroupListParams{AccountID: cf.F(c.account)},
		option.WithResponseBodyInto(&env))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(env.Result))
	for _, g := range env.Result {
		out[g.ID] = g.Name
	}
	return out, nil
}
