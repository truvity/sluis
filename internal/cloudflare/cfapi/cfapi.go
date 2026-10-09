// Package cfapi is the real client of Cloudflare's API for the minter: an
// account reached with a token, over plain net/http against
// https://api.cloudflare.com/client/v4. Everything else speaks [minter.API].
//
// It is not cloudflare-go. The minter makes five calls on one resource (an
// account's tokens), and the SDK's root client wires every Cloudflare service
// at init: importing it put about 64 MB into the Lambda bootstrap, past the
// 100 MiB unzipped limit deploy/pulumi enforces. The five calls are small
// enough to speak directly.
//
// A token's policies and condition are carried as the JSON Cloudflare sent, so a
// clone copies them exactly and depends on no SDK's types.
package cfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
)

// BaseURL is Cloudflare's API.
const BaseURL = "https://api.cloudflare.com/client/v4/"

const (
	// maxRetries is how many times a call is repeated after a refusal that
	// says to try again (see [retryable]).
	maxRetries = 2
	// attemptTimeout bounds one attempt; the caller's context bounds the call.
	attemptTimeout = 30 * time.Second
	// maxBody bounds what is read of a response.
	maxBody = 16 << 20
)

// Option changes how [Dial] reaches Cloudflare; for tests.
type Option func(*Client)

// WithBaseURL points the client at another server (a test's).
func WithBaseURL(base string) Option {
	return func(c *Client) { c.base = strings.TrimSuffix(base, "/") + "/" }
}

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// Client is one account.
type Client struct {
	account string
	token   string
	base    string
	http    *http.Client
	backoff time.Duration
}

var _ minter.API = (*Client)(nil)

// Dial opens an account with a token. Options are for tests (a base URL).
func Dial(opts ...Option) minter.Dialer {
	return func(_ context.Context, accountID, token string) (minter.API, error) {
		if accountID == "" || token == "" {
			return nil, errors.New("cfapi: an account id and a token are required")
		}
		c := &Client{
			account: accountID, token: token, base: BaseURL,
			http: &http.Client{Timeout: attemptTimeout}, backoff: 500 * time.Millisecond,
		}
		for _, o := range opts {
			o(c)
		}
		return c, nil
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

// apiError is a call Cloudflare refused. It carries the status and
// Cloudflare's error codes and messages, never the body: a body can hold a
// token's value.
type apiError struct {
	method, path string
	status       int
	messages     []string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("cloudflare: %s %s: HTTP %d", e.method, e.path, e.status)
	if len(e.messages) > 0 {
		msg += ": " + strings.Join(e.messages, "; ")
	}
	return msg
}

// Unwrap makes a 404 [cloudflare.ErrNotFound].
func (e *apiError) Unwrap() error {
	if e.status == http.StatusNotFound {
		return cloudflare.ErrNotFound
	}
	return nil
}

// envelope is what every call answers: {success, errors, result, result_info}.
type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// retryable is a refusal worth repeating: rate limited, or the server's fault.
// A create is repeated only when rate limited, which says it was not done; a
// 5xx or a broken connection might have created a token whose value is lost.
func retryable(method string, status int, err error) bool {
	if method == http.MethodPost {
		return err == nil && status == http.StatusTooManyRequests
	}
	if err != nil {
		return true
	}
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// do makes one call and decodes the envelope's result into out (when not nil).
// It returns the raw envelope for callers that read more of it.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, out any) ([]byte, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var (
		raw    []byte
		status int
		err    error
	)
	for attempt := 0; ; attempt++ {
		raw, status, err = c.once(ctx, method, u, body)
		if attempt >= maxRetries || !retryable(method, status, err) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.backoff << attempt):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if status < 200 || status > 299 || jsonErr != nil || !env.Success {
		e := &apiError{method: method, path: path, status: status}
		for _, m := range env.Errors {
			e.messages = append(e.messages, strconv.Itoa(m.Code)+" "+m.Message)
		}
		if status >= 200 && status <= 299 && jsonErr != nil {
			e.messages = append(e.messages, "the response is not Cloudflare's envelope")
		}
		return nil, e
	}
	if out != nil {
		if err = json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: %s %s: decoding the result: %w", method, path, err)
		}
	}
	return raw, nil
}

func (c *Client) once(ctx context.Context, method, u string, body []byte) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sluis")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func (c *Client) tokens(rest ...string) string {
	p := "accounts/" + url.PathEscape(c.account) + "/tokens"
	for _, r := range rest {
		p += "/" + url.PathEscape(r)
	}
	return p
}

// GetToken implements [minter.API].
func (c *Client) GetToken(ctx context.Context, id string) (cloudflare.Token, error) {
	var w wire
	if _, err := c.do(ctx, http.MethodGet, c.tokens(id), nil, nil, &w); err != nil {
		return cloudflare.Token{}, err
	}
	return w.token(), nil
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
	var w wire
	if _, err = c.do(ctx, http.MethodPost, c.tokens(), nil, raw, &w); err != nil {
		return cloudflare.Created{}, err
	}
	out := cloudflare.Created{ID: w.ID, Name: w.Name, Value: w.Value}
	if w.ExpiresOn != nil {
		out.ExpiresOn = *w.ExpiresOn
	}
	return out, nil
}

// DeleteToken implements [minter.API].
func (c *Client) DeleteToken(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, c.tokens(id), nil, nil, nil)
	return err
}

// ListTokens implements [minter.API]: every page, the recently expired
// included.
func (c *Client) ListTokens(ctx context.Context) ([]cloudflare.Token, error) {
	var out []cloudflare.Token
	for page := 1; ; page++ {
		var result []wire
		raw, err := c.do(ctx, http.MethodGet, c.tokens(), url.Values{
			"include_expired": {"true"}, "per_page": {"50"}, "page": {strconv.Itoa(page)},
		}, nil, &result)
		if err != nil {
			return nil, err
		}
		var info struct {
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		if err = json.Unmarshal(raw, &info); err != nil {
			return nil, fmt.Errorf("cloudflare: listing tokens: %w", err)
		}
		for _, w := range result {
			out = append(out, w.token())
		}
		if page >= info.ResultInfo.TotalPages || len(result) == 0 {
			return out, nil
		}
	}
}

// PermissionGroups implements [minter.API].
func (c *Client) PermissionGroups(ctx context.Context) (map[string]string, error) {
	var result []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := c.do(ctx, http.MethodGet, c.tokens("permission_groups"), nil, nil, &result); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(result))
	for _, g := range result {
		out[g.ID] = g.Name
	}
	return out, nil
}
