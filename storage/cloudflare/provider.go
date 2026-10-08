package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/truvity/sluis/storage/state"
)

// API is one Cloudflare account, reached with a minter token.
type API interface {
	// GetToken returns an account token, or [ErrNotFound].
	GetToken(ctx context.Context, id string) (Token, error)
	// CreateToken makes an account token and returns its value, shown once.
	CreateToken(ctx context.Context, in NewToken) (Created, error)
	// DeleteToken deletes an account token; a missing one is [ErrNotFound].
	DeleteToken(ctx context.Context, id string) error
	// PermissionGroups maps a permission group's id to its name.
	PermissionGroups(ctx context.Context) (map[string]string, error)
}

// DefaultPropagation is how long a derived R2 credential takes to be accepted
// after its token is created (measured against a live account).
const DefaultPropagation = 5 * time.Second

// ProviderConfig is what a [Provider] is made of.
type ProviderConfig struct {
	// Instance and Preset name the tokens it mints: sluis/<Instance>/<Preset>/<Caller>/<time>.
	Instance, Preset string
	// Caller is who the tokens are minted for. Default "self".
	Caller string
	// Account is the Cloudflare account id; Prototype is the id of the DISABLED
	// token whose policies and condition every credential copies.
	Account, Prototype string
	// Lifetime is how long each minted token lives. Renewal is with a third of it left.
	Lifetime time.Duration
	// Minter reads the minter token's value. It is called at each mint and the
	// value is not kept.
	Minter func(ctx context.Context) (string, error)
	// Dial opens the account; nil is the HTTP client of this package.
	Dial func(account, token string) API
	// ExtraForbidden adds names to the built-in refusal list.
	ExtraForbidden []string
	// Record, when set, is the state store in which the ids of the tokens the
	// provider minted are kept (at cloudflare-minted/<preset>, the document
	// sluis's own sweep reads), so that expired ones are deleted: Cloudflare
	// hides an expired token from its list while still counting it.
	Record state.Store
	// Propagation is how long to wait after a mint before handing the credentials
	// out. Zero is [DefaultPropagation]; negative waits for nothing.
	Propagation time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Provider keeps R2 credentials for one process fresh from a preset: it clones the
// prototype with the minter token, renews with a third of the lifetime left,
// and mints again on [Provider.Remint] (what a 403 asks for). It is an
// [aws.CredentialsProvider]: wrap it in aws.NewCredentialsCache for an S3 client.
type Provider struct {
	cfg ProviderConfig
	now func() time.Time

	mu       sync.Mutex
	lastRe   time.Time // when Reauthenticate last minted
	access   string
	secret   string
	expires  time.Time
	groups   map[string]string
	groupsAt time.Time
}

// NewProvider checks the configuration and returns the provider. It reaches
// nothing: the first credentials are minted at the first [Provider.Retrieve].
func NewProvider(cfg ProviderConfig) (*Provider, error) {
	switch {
	case cfg.Minter == nil:
		return nil, errors.New("cloudflare: no way to read the minter token")
	case cfg.Account == "" || cfg.Prototype == "" || cfg.Preset == "":
		return nil, errors.New("cloudflare: an account, a prototype and a preset name are required")
	case cfg.Lifetime < time.Minute:
		return nil, fmt.Errorf("cloudflare: lifetime %s is under a minute", cfg.Lifetime)
	}
	if err := ValidateInstance(cfg.Instance); err != nil {
		return nil, err
	}
	if cfg.Caller == "" {
		cfg.Caller = "self"
	}
	if cfg.Dial == nil {
		cfg.Dial = func(account, token string) API { return NewHTTP(account, token, nil) }
	}
	p := &Provider{cfg: cfg, now: cfg.Now}
	if p.now == nil {
		p.now = time.Now
	}
	return p, nil
}

func (p *Provider) renewAt() time.Time { return p.expires.Add(-p.cfg.Lifetime / 3) }

// Retrieve implements [aws.CredentialsProvider]: the held credentials, minted
// first or when due. They expire at the renewal point, so a credentials cache
// above asks again in time.
func (p *Provider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.access == "" || !p.now().Before(p.renewAt()) {
		if err := p.mintLocked(ctx); err != nil {
			return aws.Credentials{}, err
		}
	}
	return p.credentials(), nil
}

// Valid reports whether credentials are held that are not yet due for renewal.
func (p *Provider) Valid() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.access != "" && p.now().Before(p.renewAt())
}

// Remint replaces the held credentials whatever their age: a 403 asks for it,
// once. The caller bounds how often.
func (p *Provider) Remint(ctx context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.mintLocked(ctx); err != nil {
		return aws.Credentials{}, err
	}
	return p.credentials(), nil
}

// MinReauth is the least time between two mints that [Provider.Reauthenticate]
// makes: a store that answers 403 for a reason new credentials do not cure is
// not asked again.
const MinReauth = 30 * time.Second

// Reauthenticate mints new credentials because the store answered 403, unless it
// did so less than [MinReauth] ago, and reports whether it did. A caller holding
// the credentials in a cache invalidates it when this answers true.
func (p *Provider) Reauthenticate(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastRe.IsZero() && p.now().Sub(p.lastRe) < MinReauth {
		return false, nil
	}
	if err := p.mintLocked(ctx); err != nil {
		return false, err
	}
	p.lastRe = p.now()
	return true, nil
}

func (p *Provider) credentials() aws.Credentials {
	return aws.Credentials{AccessKeyID: p.access, SecretAccessKey: p.secret, Source: "sluis-cloudflare", CanExpire: true, Expires: p.renewAt()}
}

func (p *Provider) mintLocked(ctx context.Context) error {
	minter, err := p.cfg.Minter(ctx)
	if err != nil {
		return fmt.Errorf("cloudflare: reading the minter token: %w", err)
	}
	api := p.cfg.Dial(p.cfg.Account, minter)
	proto, err := api.GetToken(ctx, p.cfg.Prototype)
	if errors.Is(err, ErrNotFound) {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: "the prototype token " + p.cfg.Prototype + " does not exist"}
	}
	if err != nil {
		return fmt.Errorf("cloudflare: reading the prototype: %w", err)
	}
	if err = p.check(ctx, api, proto); err != nil {
		return err
	}
	policies, err := ClonePolicies(proto.Policies)
	if err != nil {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: err.Error()}
	}
	condition, err := CloneCondition(proto.Condition)
	if err != nil {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: err.Error()}
	}
	now := p.now()
	expires := now.Add(p.cfg.Lifetime).UTC().Truncate(time.Second)
	created, err := api.CreateToken(ctx, NewToken{
		Name: OnDemandName(p.cfg.Instance, p.cfg.Preset, p.cfg.Caller, now), ExpiresOn: expires, Policies: policies, Condition: condition,
	})
	if err != nil {
		return fmt.Errorf("cloudflare: creating the token: %w", err)
	}
	if created.ID == "" || created.Value == "" {
		return errors.New("cloudflare: Cloudflare answered without an id or a value")
	}
	if !created.ExpiresOn.IsZero() {
		expires = created.ExpiresOn.UTC()
	}
	// Recorded before the value is used, so that a token nobody can sweep never exists.
	if err = p.record(ctx, created.ID, expires, now, api); err != nil {
		_ = api.DeleteToken(context.WithoutCancel(ctx), created.ID)
		return fmt.Errorf("cloudflare: recording the minted token: %w", err)
	}
	if err = p.settle(ctx); err != nil {
		return err
	}
	p.access, p.secret, p.expires = created.ID, R2Secret(created.Value), expires
	return nil
}

// check holds the prototype to the rules, refreshing the permission groups once
// when one is not in the cached list.
func (p *Provider) check(ctx context.Context, api API, proto Token) error {
	for _, refresh := range []bool{false, true} {
		if p.groups == nil || refresh || p.now().Sub(p.groupsAt) > 10*time.Minute {
			g, err := api.PermissionGroups(ctx)
			if err != nil {
				return fmt.Errorf("cloudflare: listing the permission groups: %w", err)
			}
			p.groups, p.groupsAt = g, p.now()
		}
		err := CheckPrototype(proto, p.groups, p.cfg.ExtraForbidden...)
		pe, ok := IsPrototypeError(err)
		if err == nil || !ok || pe.Reason != ReasonPrototypeForbidden || refresh {
			return err
		}
		ids, _ := GroupIDs(proto.Policies)
		if !slices.ContainsFunc(ids, func(id string) bool { _, known := p.groups[id]; return !known }) {
			return err
		}
	}
	return nil
}

func (p *Provider) settle(ctx context.Context) error {
	d := p.cfg.Propagation
	if d == 0 {
		d = DefaultPropagation
	}
	if d < 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// minted is the record of the tokens a preset minted; the same document the
// service's own sweep reads (internal/secretstore.CloudflareMinted).
type minted struct {
	Tokens []mintedToken `json:"tokens"`
}

type mintedToken struct {
	ID        string    `json:"id"`
	ExpiresOn time.Time `json:"expires_on"`
}

// record adds the token to the record and deletes the preset's recorded tokens
// that have expired. Without a Record store it does nothing.
func (p *Provider) record(ctx context.Context, id string, expires, now time.Time, api API) error {
	if p.cfg.Record == nil {
		return nil
	}
	key := "cloudflare-minted/" + p.cfg.Preset
	var err error
	for range 6 {
		var doc minted
		var rev state.Rev
		it, gerr := p.cfg.Record.Get(ctx, key)
		switch {
		case gerr == nil:
			rev = it.Rev
			if err = json.Unmarshal(it.Value, &doc); err != nil {
				return fmt.Errorf("the record %s is damaged: %w", key, err)
			}
		case !errors.Is(gerr, state.ErrNotFound):
			return gerr
		}
		doc.Tokens = p.sweepExpired(ctx, api, doc.Tokens, now)
		doc.Tokens = append(doc.Tokens, mintedToken{ID: id, ExpiresOn: expires.UTC()})
		raw, merr := json.Marshal(doc)
		if merr != nil {
			return merr
		}
		if _, err = p.cfg.Record.Put(ctx, key, raw, rev); err == nil {
			return nil
		}
		if !errors.Is(err, state.ErrConflict) {
			return err
		}
	}
	return err
}

// sweepExpired deletes the recorded tokens past their expiry, by id, checking
// the name still carries this preset's prefix, and returns the rest.
func (p *Provider) sweepExpired(ctx context.Context, api API, tokens []mintedToken, now time.Time) []mintedToken {
	var keep []mintedToken
	for _, t := range tokens {
		if t.ExpiresOn.After(now) {
			keep = append(keep, t)
			continue
		}
		tok, err := api.GetToken(ctx, t.ID)
		switch {
		case errors.Is(err, ErrNotFound), err == nil && !IsOwn(tok.Name, p.cfg.Instance, p.cfg.Preset):
			continue // gone, or not ours: forget it
		case err != nil:
			keep = append(keep, t) // try again at the next mint
			continue
		}
		if derr := api.DeleteToken(ctx, t.ID); derr != nil && !errors.Is(derr, ErrNotFound) {
			keep = append(keep, t)
		}
	}
	return keep
}

// ---------------------------------------------------------------- HTTP client

// BaseURL is Cloudflare's API.
const BaseURL = "https://api.cloudflare.com/client/v4"

// HTTP is the [API] over Cloudflare's REST API, with the standard library only.
type HTTP struct {
	account, token, base string
	client               *http.Client
}

// NewHTTP returns the client of one account. base is the API's URL, or empty
// for [BaseURL]; a test passes its own.
func NewHTTP(account, token string, client *http.Client, base ...string) *HTTP {
	h := &HTTP{account: account, token: token, base: BaseURL, client: client}
	if len(base) > 0 && base[0] != "" {
		h.base = base[0]
	}
	if h.client == nil {
		h.client = &http.Client{Timeout: 30 * time.Second}
	}
	return h
}

type envelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// do sends one request and returns the result. An error carries Cloudflare's
// codes and messages and never the request's body or the token.
func (h *HTTP) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+"/accounts/"+url.PathEscape(h.account)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, errors.Unwrap(err))
	}
	defer resp.Body.Close() //nolint:errcheck // read once
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: HTTP 404", ErrNotFound)
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		msg := ""
		for _, e := range env.Errors {
			msg += fmt.Sprintf(" [%d] %s", e.Code, e.Message)
		}
		return nil, fmt.Errorf("%s %s: HTTP %d%s", method, path, resp.StatusCode, msg)
	}
	return env.Result, nil
}

type wireToken struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	ExpiresOn *time.Time      `json:"expires_on"`
	Policies  json.RawMessage `json:"policies"`
	Condition json.RawMessage `json:"condition"`
	Value     string          `json:"value"`
}

func (w wireToken) token() Token {
	t := Token{ID: w.ID, Name: w.Name, Status: w.Status, Policies: w.Policies, Condition: w.Condition}
	if w.ExpiresOn != nil {
		t.ExpiresOn = *w.ExpiresOn
	}
	return t
}

// GetToken implements [API].
func (h *HTTP) GetToken(ctx context.Context, id string) (Token, error) {
	res, err := h.do(ctx, http.MethodGet, "/tokens/"+url.PathEscape(id), nil)
	if err != nil {
		return Token{}, err
	}
	var w wireToken
	if err = json.Unmarshal(res, &w); err != nil {
		return Token{}, err
	}
	return w.token(), nil
}

// CreateToken implements [API].
func (h *HTTP) CreateToken(ctx context.Context, in NewToken) (Created, error) {
	body := map[string]any{"name": in.Name, "policies": in.Policies, "expires_on": in.ExpiresOn.UTC().Format(time.RFC3339)}
	if len(in.Condition) > 0 {
		body["condition"] = in.Condition
	}
	res, err := h.do(ctx, http.MethodPost, "/tokens", body)
	if err != nil {
		return Created{}, err
	}
	var w wireToken
	if err = json.Unmarshal(res, &w); err != nil {
		return Created{}, err
	}
	out := Created{ID: w.ID, Name: w.Name, Value: w.Value}
	if w.ExpiresOn != nil {
		out.ExpiresOn = *w.ExpiresOn
	}
	return out, nil
}

// DeleteToken implements [API].
func (h *HTTP) DeleteToken(ctx context.Context, id string) error {
	_, err := h.do(ctx, http.MethodDelete, "/tokens/"+url.PathEscape(id), nil)
	return err
}

// PermissionGroups implements [API].
func (h *HTTP) PermissionGroups(ctx context.Context) (map[string]string, error) {
	res, err := h.do(ctx, http.MethodGet, "/tokens/permission_groups", nil)
	if err != nil {
		return nil, err
	}
	var list []struct{ ID, Name string }
	if err = json.Unmarshal(res, &list); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, g := range list {
		out[g.ID] = g.Name
	}
	return out, nil
}

var (
	_ API                     = (*HTTP)(nil)
	_ aws.CredentialsProvider = (*Provider)(nil)
)
