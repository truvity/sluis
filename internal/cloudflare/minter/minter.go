//nolint:lll // messages and fixtures are prose and one-line tables
package minter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// The errors a caller of the minter tells apart.
var (
	// ErrUnknownPreset is a preset the service document does not declare.
	ErrUnknownPreset = errors.New("cloudflare: no such preset")
	// ErrNotGranted is a caller the policy's cloudflare.grants do not open the
	// preset to.
	ErrNotGranted = errors.New("cloudflare: the caller is not granted this preset")
	// ErrLifetime is a requested lifetime longer than the preset's or under a
	// minute.
	ErrLifetime = errors.New("cloudflare: the requested lifetime is outside the preset's")
	// ErrNotOurs is a token id that is not one sluis minted for the preset.
	ErrNotOurs = errors.New("cloudflare: the token was not minted by sluis for this preset")
	// ErrNoMinter is an account whose minter credential is not in the store.
	ErrNoMinter = errors.New("cloudflare: the account's minter credential is not in the secrets store")
)

// permissionGroupsTTL is how long an account's permission groups are reused.
const permissionGroupsTTL = 10 * time.Minute

// DefaultPropagation is measured against a live account (2026-10-08): derived
// R2 credentials work about five seconds after the token is created.
const DefaultPropagation = 5 * time.Second

// LeaseKind is the kind of the lease a preset's tick runs under (internal/rails).
const LeaseKind = "cloudflare-tick"

// Recorder is what records an audit record (internal/audit.Recorder).
type Recorder interface {
	Record(ctx context.Context, r *record.Record)
}

// Lock serialises the tick of one preset between replicas and invocations
// (internal/rails.Leases). ran is false, with no error, when another runner
// holds it.
type Lock interface {
	Do(ctx context.Context, kind, target string, fn func(ctx context.Context)) (ran bool, err error)
}

// Config is what a [Minter] is made of.
type Config struct {
	// Instance names this installation in a token's name: sluis/<instance>/...
	Instance string
	// Cloudflare is the service document's section; nil makes a minter with
	// nothing to do.
	Cloudflare *config.Cloudflare
	// Grants is the policy's cloudflare section.
	Grants *config.PolicyCloudflare
	// Layout, Internal and External are the installation's secrets on layout
	// v4: the minter credential is read from Internal, the stored credentials
	// are written to External. A v3 layout has neither and is refused.
	Layout   secretstore.Layout
	Internal secretstore.Internal
	External secretstore.External
	// Dial opens an account (cfapi.Dial).
	Dial Dialer
	// Audit receives the records; nil records nothing.
	Audit Recorder
	// Lock serialises ticks; nil runs them unserialised.
	Lock Lock
	Log  *slog.Logger
	// Propagation is how long a [Provider] waits after minting R2 credentials
	// before handing them out: Cloudflare accepts a derived credential about
	// five seconds after the token is created and refuses it (403) before.
	// Zero is [DefaultPropagation]; negative waits for nothing.
	Propagation time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Minter mints Cloudflare credentials for the presets of one installation.
type Minter struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	groups map[string]cachedGroups
}

type cachedGroups struct {
	at    time.Time
	names map[string]string
}

// New makes a minter. It refuses a layout that cannot hold the stored
// credentials: `secrets.layout` must be v4 or transition.
func New(cfg Config) (*Minter, error) {
	if cfg.Cloudflare == nil || len(cfg.Cloudflare.Presets) == 0 {
		return nil, errors.New("cloudflare: the service document declares no presets")
	}
	if !cfg.Layout.WritesV4() {
		return nil, fmt.Errorf("cloudflare: the stored credentials live at external/cloudflare/<preset> and the minter credential at internal/..., so secrets.layout must be %s or %s (it is %q)",
			secretstore.LayoutTransition, secretstore.LayoutV4, cfg.Layout)
	}
	if cfg.Dial == nil {
		return nil, errors.New("cloudflare: no way to reach Cloudflare is configured")
	}
	if err := cloudflare.ValidateInstance(cfg.Instance); err != nil {
		return nil, err
	}
	if err := cfg.Cloudflare.Validate(); err != nil {
		return nil, err
	}
	m := &Minter{cfg: cfg, log: cfg.Log, now: cfg.Now, groups: map[string]cachedGroups{}}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.cfg.Grants == nil {
		m.cfg.Grants = &config.PolicyCloudflare{}
	}
	return m, nil
}

// PresetInfo is what a preset shows to someone allowed to ask for it. Nothing
// here is a secret.
type PresetInfo struct {
	Name        string
	Description string
	Account     string
	// R2 is a preset that hands out R2 (S3) credentials; Endpoint is then its
	// S3 endpoint, and empty otherwise.
	R2       bool
	Endpoint string
	Lifetime time.Duration
	Rotation time.Duration
}

func infoOf(name string, p config.CloudflarePreset) PresetInfo {
	return PresetInfo{
		Name: name, Description: p.Description, Account: p.Account, R2: p.R2(), Endpoint: p.Endpoint,
		Lifetime: p.Lifetime.D(), Rotation: p.Rotation.D(),
	}
}

// Presets are all the presets, sorted by name.
func (m *Minter) Presets() []PresetInfo {
	var out []PresetInfo
	for _, name := range m.cfg.Cloudflare.PresetNames() {
		out = append(out, infoOf(name, m.cfg.Cloudflare.Presets[name]))
	}
	return out
}

// Preset is one preset, or false.
func (m *Minter) Preset(name string) (PresetInfo, bool) {
	p, ok := m.cfg.Cloudflare.Presets[name]
	if !ok {
		return PresetInfo{}, false
	}
	return infoOf(name, p), true
}

// Caller is who asks for a token, as the token exchange verified them. The
// minter does not verify anyone: it checks the policy's grants against what it
// is given.
type Caller struct {
	// Actor is who acted, for the audit trail and the token's name.
	Actor audit.Actor
	// Groups are the groups the caller holds (a person's, from the verified
	// session or token).
	Groups []string
}

// Granted are the presets the policy opens to the caller, sorted. It is what
// `sluisctl whoami` and the console list.
func (m *Minter) Granted(c Caller) []PresetInfo {
	var out []PresetInfo
	for _, name := range m.cfg.Cloudflare.PresetNames() {
		if m.cfg.Grants.Allows(name, c.Groups) {
			out = append(out, infoOf(name, m.cfg.Cloudflare.Presets[name]))
		}
	}
	return out
}

// Minted is a credential just made. It carries the secret and is never logged
// or recorded.
type Minted struct {
	Preset  string
	Account string
	// TokenID is the token's id in Cloudflare; for R2 it is the access key id.
	TokenID   string
	Name      string
	ExpiresOn time.Time
	// R2 says the preset hands out S3 credentials.
	R2 bool
	// Token is the token's value. For an R2 preset it is what the secret is
	// derived from, and callers use the R2 fields instead.
	Token           string
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	// RenewAt is, for a credential a [Provider] holds, when to replace it: a
	// third of the lifetime before ExpiresOn. Zero otherwise.
	RenewAt time.Time
}

// Document is the cloudflare/v1 document the credential is stored as.
func (m Minted) Document() secretstore.Cloudflarev1 {
	d := secretstore.Cloudflarev1{ExpiresOn: m.ExpiresOn.UTC().Format(time.RFC3339)}
	if m.R2 {
		d.AccessKeyID, d.SecretAccessKey, d.Endpoint = m.AccessKeyID, m.SecretAccessKey, m.Endpoint
	} else {
		d.Token = m.Token
	}
	return d
}

// MintFor mints a token of the preset for one caller, on demand: the same
// prototype, a lifetime of the caller's choosing up to the preset's (zero is the
// preset's), named sluis/<instance>/<preset>/<caller>/<time>. It is audited
// whether it succeeds or not. The caller must be granted the preset by the
// policy's cloudflare.grants ([ErrNotGranted]); how the caller proved who they
// are is the caller's business, not the minter's.
func (m *Minter) MintFor(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error) {
	p, ok := m.cfg.Cloudflare.Presets[preset]
	if !ok {
		m.refused(ctx, caller.Actor, preset, audit.CloudflareOnDemand, "unknown_preset", "", true)
		return nil, fmt.Errorf("%w: %q", ErrUnknownPreset, preset)
	}
	if !m.cfg.Grants.Allows(preset, caller.Groups) {
		m.refused(ctx, caller.Actor, preset, audit.CloudflareOnDemand, "not_granted", "", true)
		return nil, fmt.Errorf("%w: %s", ErrNotGranted, preset)
	}
	if lifetime == 0 {
		lifetime = p.Lifetime.D()
	}
	if lifetime < config.CloudflareMinLifetime || lifetime > p.Lifetime.D() {
		m.refused(ctx, caller.Actor, preset, audit.CloudflareOnDemand, "lifetime_too_long",
			fmt.Sprintf("asked %s, the preset allows %s to %s", lifetime, config.CloudflareMinLifetime, p.Lifetime.D()), true)
		return nil, fmt.Errorf("%w: %s, want %s to %s", ErrLifetime, lifetime, config.CloudflareMinLifetime, p.Lifetime.D())
	}
	who := caller.Actor.ID
	now := m.now()
	out, err := m.mint(ctx, preset, p, cloudflare.OnDemandName(m.cfg.Instance, preset, who, now), now, lifetime)
	if err != nil {
		m.fail(ctx, caller.Actor, preset, audit.CloudflareOnDemand, err)
		return nil, err
	}
	m.cfg.record(ctx, audit.CloudflareTokenMinted(caller.Actor, preset, audit.CloudflareToken{
		Variant: audit.CloudflareOnDemand, R2: p.R2(), Account: p.Account, TokenID: out.TokenID, ExpiresOn: out.ExpiresOn,
	}))
	meters.mint(ctx, preset, audit.CloudflareOnDemand, "ok")
	return out, nil
}

// mint clones the preset's prototype into a new token and returns the value.
// It checks the prototype every time: it is re-read at each mint so an edit
// applies at the next rotation, and so is refused the moment it becomes active.
func (m *Minter) mint(ctx context.Context, preset string, p config.CloudflarePreset, name string, now time.Time, lifetime time.Duration) (*Minted, error) {
	api, err := m.api(ctx, p.Account)
	if err != nil {
		return nil, err
	}
	proto, err := api.GetToken(ctx, p.Prototype)
	if errors.Is(err, cloudflare.ErrNotFound) {
		return nil, &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeMissing, Detail: "the prototype token " + p.Prototype + " does not exist"}
	}
	if err != nil {
		return nil, &cloudflareError{err: fmt.Errorf("read the prototype: %w", err)}
	}
	if err = m.checkPrototype(ctx, p.Account, api, proto); err != nil {
		return nil, err
	}
	policies, err := cloudflare.ClonePolicies(proto.Policies)
	if err != nil {
		return nil, &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeMissing, Detail: err.Error()}
	}
	condition, err := cloudflare.CloneCondition(proto.Condition)
	if err != nil {
		return nil, &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeMissing, Detail: err.Error()}
	}
	expires := now.Add(lifetime).UTC().Truncate(time.Second)
	created, err := api.CreateToken(ctx, cloudflare.NewToken{Name: name, ExpiresOn: expires, Policies: policies, Condition: condition})
	if err != nil {
		return nil, &cloudflareError{err: fmt.Errorf("create the token: %w", err)}
	}
	if created.Value == "" || created.ID == "" {
		return nil, &cloudflareError{err: errors.New("create the token: Cloudflare answered without an id or a value")}
	}
	if !created.ExpiresOn.IsZero() {
		expires = created.ExpiresOn.UTC()
	}
	// Recorded before the value leaves: a token nobody can sweep is a token
	// that counts toward the account's limit for good.
	if err = m.track(ctx, preset, created.ID, expires); err != nil {
		dctx := context.WithoutCancel(ctx)
		if derr := api.DeleteToken(dctx, created.ID); derr != nil && !errors.Is(derr, cloudflare.ErrNotFound) {
			m.log.WarnContext(ctx, "a minted token could not be recorded and could not be deleted", "preset", preset, "token", created.ID, "error", derr)
		}
		return nil, &storeError{err: fmt.Errorf("record the minted token: %w", err)}
	}
	out := &Minted{Preset: preset, Account: p.Account, TokenID: created.ID, Name: name, ExpiresOn: expires, Token: created.Value, R2: p.R2()}
	if p.R2() {
		out.AccessKeyID, out.SecretAccessKey, out.Endpoint = created.ID, cloudflare.R2Secret(created.Value), p.Endpoint
	}
	return out, nil
}

// checkPrototype resolves the prototype's permission groups to names, asking
// the account again once when one is not in the cached list.
func (m *Minter) checkPrototype(ctx context.Context, account string, api API, proto cloudflare.Token) error {
	names, err := m.permissionGroups(ctx, account, api, false)
	if err != nil {
		return &cloudflareError{err: fmt.Errorf("list the permission groups: %w", err)}
	}
	err = cloudflare.CheckPrototype(proto, names, m.cfg.Cloudflare.ForbiddenPermissionGroups...)
	if pe, ok := cloudflare.IsPrototypeError(err); ok && pe.Reason == cloudflare.ReasonPrototypeForbidden && m.unresolved(proto, names) {
		if names, err = m.permissionGroups(ctx, account, api, true); err != nil {
			return &cloudflareError{err: fmt.Errorf("list the permission groups: %w", err)}
		}
		return cloudflare.CheckPrototype(proto, names, m.cfg.Cloudflare.ForbiddenPermissionGroups...)
	}
	return err
}

func (m *Minter) unresolved(proto cloudflare.Token, names map[string]string) bool {
	ids, err := cloudflare.GroupIDs(proto.Policies)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			return true
		}
	}
	return false
}

func (m *Minter) permissionGroups(ctx context.Context, account string, api API, refresh bool) (map[string]string, error) {
	m.mu.Lock()
	c, ok := m.groups[account]
	m.mu.Unlock()
	if ok && !refresh && m.now().Sub(c.at) < permissionGroupsTTL {
		return c.names, nil
	}
	names, err := api.PermissionGroups(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.groups[account] = cachedGroups{at: m.now(), names: names}
	m.mu.Unlock()
	return names, nil
}

// api reads the account's minter credential and opens the account. The
// credential is read at every use: it is never kept in memory beyond the call.
func (m *Minter) api(ctx context.Context, account string) (API, error) {
	a, ok := m.cfg.Cloudflare.Accounts[account]
	if !ok {
		return nil, fmt.Errorf("cloudflare: account %q is not declared", account)
	}
	v, err := m.cfg.Internal.CloudflareMinter(a.Minter)
	if err != nil {
		return nil, err
	}
	doc, _, err := v.Get(ctx)
	if errors.Is(err, state.ErrNotFound) {
		return nil, &minterMissing{ref: a.Minter}
	}
	if err != nil {
		return nil, &cloudflareError{err: fmt.Errorf("read the minter credential %s: %w", a.Minter, err)}
	}
	api, err := m.cfg.Dial(ctx, a.ID, doc.Token)
	if err != nil {
		return nil, &cloudflareError{err: err}
	}
	return api, nil
}

// cloudflareError is an error from Cloudflare or from reaching it.
type cloudflareError struct{ err error }

func (e *cloudflareError) Error() string { return e.err.Error() }
func (e *cloudflareError) Unwrap() error { return e.err }

// minterMissing is an account with no minter credential stored.
type minterMissing struct{ ref string }

func (e *minterMissing) Error() string {
	return ErrNoMinter.Error() + ": " + e.ref
}
func (e *minterMissing) Is(target error) bool { return target == ErrNoMinter }

// storeError is an error from the secrets store.
type storeError struct{ err error }

func (e *storeError) Error() string { return e.err.Error() }
func (e *storeError) Unwrap() error { return e.err }

// reasonOf classifies why a mint failed, for the audit record and the log.
func reasonOf(err error) (reason, detail string) {
	var pe *cloudflare.PrototypeError
	var mm *minterMissing
	var se *storeError
	switch {
	case errors.As(err, &pe):
		return pe.Reason, pe.Detail
	case errors.As(err, &mm):
		return "minter_missing", mm.ref
	case errors.As(err, &se):
		return "store_error", ""
	default:
		return "cloudflare_error", ""
	}
}

// refused records a refusal.
func (m *Minter) refused(ctx context.Context, actor audit.Actor, preset, variant, reason, detail string, denied bool) {
	m.cfg.record(ctx, audit.CloudflareTokenRefused(actor, preset, variant, reason, detail, denied))
	meters.mint(ctx, preset, variant, "refused")
}

// fail records a mint that did not complete, and logs it without the error's
// detail beyond what reasonOf lets out.
func (m *Minter) fail(ctx context.Context, actor audit.Actor, preset, variant string, err error) {
	reason, detail := reasonOf(err)
	_, isPrototype := cloudflare.IsPrototypeError(err)
	m.cfg.record(ctx, audit.CloudflareTokenRefused(actor, preset, variant, reason, detail, false))
	outcome := "failed"
	if isPrototype {
		outcome = "refused"
	}
	meters.mint(ctx, preset, variant, outcome)
	if isPrototype {
		meters.prototypeRefused(ctx, preset, reason)
	}
	m.log.WarnContext(ctx, "a Cloudflare token was not minted", "preset", preset, "variant", variant, "reason", reason, "error", err)
}

func (c Config) record(ctx context.Context, r *record.Record) {
	if c.Audit != nil {
		c.Audit.Record(ctx, r)
	}
}

// sortedPresets are the presets' names, sorted.
func (m *Minter) sortedPresets() []string {
	names := m.cfg.Cloudflare.PresetNames()
	sort.Strings(names)
	return names
}
