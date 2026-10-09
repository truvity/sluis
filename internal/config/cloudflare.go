//nolint:lll // messages and fixtures are prose and one-line tables
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Cloudflare is `cloudflare`: sluis as the STS for Cloudflare API tokens and R2
// credentials (docs/guides/sluis/cloudflare-tokens.md). An account names the minter
// credential sluis holds; a preset names a disabled prototype token in that
// account whose policies and condition are the rights a minted token has.
type Cloudflare struct {
	// Accounts are the Cloudflare accounts sluis mints in, by the name presets
	// use.
	Accounts map[string]CloudflareAccount `json:"accounts,omitempty"`
	// Presets are what may be minted, by name.
	Presets map[string]CloudflarePreset `json:"presets,omitempty"`
	// ForbiddenPermissionGroups ADD to the built-in list of permission groups
	// a prototype may never grant (token admin, billing, account settings,
	// memberships, Access identity providers), by the name Cloudflare lists
	// them under. They cannot remove from it: nothing in a document shortens
	// the built-in list.
	ForbiddenPermissionGroups []string `json:"forbiddenPermissionGroups,omitempty"`
	// Remote is where the minter is when it is not this process: the on-demand
	// exchange then calls the Cloudflare module there, and this process holds no
	// account and no preset.
	Remote *CloudflareRemote `json:"remote,omitempty"`
	// Serve makes the Cloudflare module answer other modules' calls: on
	// Kubernetes a listener that verifies the caller's projected token, on
	// Lambda the function's `rpc` events (docs/decisions/0071).
	Serve *CloudflareServe `json:"serve,omitempty"`
}

// CloudflareServe is where the Cloudflare module answers other modules.
type CloudflareServe struct {
	// Address is the listener on Kubernetes: host:port. Lambda ignores it.
	Address string `json:"address,omitempty"`
	// Audience is the audience a caller's projected token must have been
	// minted for; the module's name when unset.
	Audience string `json:"audience,omitempty"`
}

// CloudflareRemote is the Cloudflare module in another process: a Lambda
// function or a Kubernetes Service (docs/decisions/0071).
type CloudflareRemote struct {
	// Function is the module's Lambda function, name or ARN; it is invoked
	// through its `live` alias.
	Function string `json:"function,omitempty"`
	// URL is the module's Service, called with the pod's projected token.
	URL string `json:"url,omitempty"`
	// Audience is the audience of that token; the module's name when unset.
	Audience string `json:"audience,omitempty"`
	// TokenFile is where the projected token is mounted.
	TokenFile string `json:"tokenFile,omitempty"`
}

// CloudflareAccount is one Cloudflare account.
type CloudflareAccount struct {
	// ID is the account id.
	ID string `json:"id"`
	// Minter is the internal address of the minter credential, a
	// cloudflare-minter/v1 document: an account token that holds Account API
	// Tokens Read and Edit and nothing else.
	Minter string `json:"minter"`
}

// CloudflarePreset is one thing sluis mints.
type CloudflarePreset struct {
	// Account is a key of Accounts.
	Account string `json:"account"`
	// Prototype is the id of the DISABLED account token whose policies and
	// condition every minted token copies.
	Prototype string `json:"prototype"`
	// Description says what the preset is for; the console and sluisctl show it.
	Description string `json:"description"`
	// Lifetime is how long a minted token lives (its expires_on).
	Lifetime Duration `json:"lifetime"`
	// Rotation is how often the stored token is replaced. Shorter than Lifetime.
	Rotation Duration `json:"rotation"`
	// Endpoint, when set, makes this an R2 preset: the S3 endpoint of the
	// account (and jurisdiction) the credentials are for.
	Endpoint string `json:"endpoint,omitempty"`
}

// R2 reports whether the preset hands out R2 (S3) credentials.
func (p CloudflarePreset) R2() bool { return p.Endpoint != "" }

// The limits of a preset's two settings.
const (
	// CloudflareMinLifetime is the shortest lifetime and rotation: the tick
	// is a minute at best on Lambda.
	CloudflareMinLifetime = time.Minute
	// CloudflareMaxLifetime is the longest a minted token lives. A short-lived
	// credential that outlives a day is a long-lived one under another name.
	CloudflareMaxLifetime = 24 * time.Hour
)

var (
	// CloudflareNamePattern is the name of a preset or an account: a DNS label.
	CloudflareNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	// cloudflareMinterPattern is an address below internal/ of two to four
	// segments (`internal/cloudflare/<account>/minter`).
	cloudflareMinterPattern = regexp.MustCompile(`^internal/[a-z0-9][a-z0-9-]{0,30}(/[a-z0-9][a-z0-9._-]{0,62}){1,3}$`)
	cloudflareIDPattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// cloudflarePrototypePattern is a token id: Cloudflare spells it as 32 hex
	// digits; a looser form is accepted so a change of theirs is not ours.
	cloudflarePrototypePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

// CheckCloudflareMinterRef refuses a minter address that is not below internal/.
func CheckCloudflareMinterRef(ref string) error {
	if !cloudflareMinterPattern.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("%q is not an internal/<kind>/<name>[/...] address", ref)
	}
	return nil
}

// Validate holds the section to what its schema cannot say. A nil section is
// valid: Cloudflare is off.
func (c *Cloudflare) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	if r := c.Remote; r != nil {
		switch {
		case (r.Function == "") == (r.URL == ""):
			errs = append(errs, errors.New("cloudflare.remote: set exactly one of function and url"))
		case r.URL != "":
			if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				errs = append(errs, fmt.Errorf("cloudflare.remote.url: %q is not an http(s) URL", r.URL))
			}
		}
		if len(c.Accounts) > 0 || len(c.Presets) > 0 {
			errs = append(errs, errors.New("cloudflare.remote: a process that calls the module holds no accounts or presets"))
		}
	}
	if sv := c.Serve; sv != nil {
		if c.Remote != nil {
			errs = append(errs, errors.New("cloudflare.serve: a process that calls the module does not also serve it"))
		}
		if len(c.Presets) == 0 {
			errs = append(errs, errors.New("cloudflare.serve: the module serves its own presets, and the document declares none"))
		}
		if _, _, err := net.SplitHostPort(sv.Address); sv.Address != "" && err != nil {
			errs = append(errs, fmt.Errorf("cloudflare.serve.address: %q is not host:port", sv.Address))
		}
	}
	for i, g := range c.ForbiddenPermissionGroups {
		if strings.TrimSpace(g) == "" {
			errs = append(errs, fmt.Errorf("cloudflare.forbiddenPermissionGroups[%d] is empty", i))
		}
	}
	minters := map[string]string{}
	for _, name := range sortedKeys(c.Accounts) {
		a := c.Accounts[name]
		if !CloudflareNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("cloudflare.accounts: %q is not a lower-case DNS label", name))
		}
		if !cloudflareIDPattern.MatchString(a.ID) {
			errs = append(errs, fmt.Errorf("cloudflare.accounts.%s.id: %q is not a 32-digit hex account id", name, a.ID))
		}
		if err := CheckCloudflareMinterRef(a.Minter); err != nil {
			errs = append(errs, fmt.Errorf("cloudflare.accounts.%s.minter: %w", name, err))
		} else if other, dup := minters[a.Minter]; dup {
			errs = append(errs, fmt.Errorf("cloudflare.accounts.%s.minter: %s already holds the minter of account %s", name, a.Minter, other))
		}
		minters[a.Minter] = name
	}
	for _, name := range sortedKeys(c.Presets) {
		if err := c.validatePreset(name, c.Presets[name]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Cloudflare) validatePreset(name string, p CloudflarePreset) error {
	at := "cloudflare.presets." + name
	var errs []error
	if !CloudflareNamePattern.MatchString(name) {
		errs = append(errs, fmt.Errorf("cloudflare.presets: %q is not a lower-case DNS label", name))
	}
	if _, ok := c.Accounts[p.Account]; !ok {
		errs = append(errs, fmt.Errorf("%s.account: %q is not declared in cloudflare.accounts", at, p.Account))
	}
	if !cloudflarePrototypePattern.MatchString(p.Prototype) {
		errs = append(errs, fmt.Errorf("%s.prototype: %q is not a token id", at, p.Prototype))
	}
	if strings.TrimSpace(p.Description) == "" {
		errs = append(errs, fmt.Errorf("%s.description: required", at))
	}
	life, rot := p.Lifetime.D(), p.Rotation.D()
	switch {
	case life < CloudflareMinLifetime:
		errs = append(errs, fmt.Errorf("%s.lifetime: %s is shorter than %s", at, life, CloudflareMinLifetime))
	case life > CloudflareMaxLifetime:
		errs = append(errs, fmt.Errorf("%s.lifetime: %s is longer than %s", at, life, CloudflareMaxLifetime))
	}
	if rot < CloudflareMinLifetime {
		errs = append(errs, fmt.Errorf("%s.rotation: %s is shorter than %s", at, rot, CloudflareMinLifetime))
	}
	if rot >= life {
		errs = append(errs, fmt.Errorf("%s.rotation: %s must be shorter than lifetime %s (lifetime - rotation is the time consumers have to pick up the new token)", at, rot, life))
	}
	if p.Endpoint != "" {
		u, err := url.Parse(p.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
			errs = append(errs, fmt.Errorf("%s.endpoint: %q is not an https://<host> URL", at, p.Endpoint))
		}
	}
	return errors.Join(errs...)
}

// PresetNames are the presets' names, sorted.
func (c *Cloudflare) PresetNames() []string {
	if c == nil {
		return nil
	}
	return sortedKeys(c.Presets)
}

// AccountNames are the accounts' names, sorted.
func (c *Cloudflare) AccountNames() []string {
	if c == nil {
		return nil
	}
	return sortedKeys(c.Accounts)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PolicyCloudflare is the policy document's `cloudflare` section: who may ask
// for which preset.
type PolicyCloudflare struct {
	// Grants say which presets the holders of a group may have a token for. A
	// CI job is a group like any other: the policy declares it as a group with
	// `github` matchers (repository, ref, event, job_workflow_ref), and the
	// verified token is evaluated against them by the same group evaluation
	// as everything else.
	Grants []CloudflareGrant `yaml:"grants,omitempty" json:"grants,omitempty"`
}

// CloudflareGrant is one row of grants.
type CloudflareGrant struct {
	// Group is an internal group of the policy (a person group, or a declared CI
	// job group); every holder may ask.
	Group string `yaml:"group,omitempty" json:"group,omitempty"`
	// Presets are the presets the row opens.
	Presets []string `yaml:"presets" json:"presets"`
}

// Cloudflare is the policy's section, never nil.
func (d *PolicyDocument) Cloudflare() *PolicyCloudflare {
	if d == nil || d.CloudflareGrants == nil {
		return &PolicyCloudflare{}
	}
	return d.CloudflareGrants
}

// PresetsForGroups are the presets a holder of any of groups may ask for,
// sorted and without repeats.
func (c *PolicyCloudflare) PresetsForGroups(groups []string) []string {
	var out []string
	for _, g := range c.Grants {
		if g.Group != "" && slices.Contains(groups, g.Group) {
			out = union(out, g.Presets)
		}
	}
	sort.Strings(out)
	return out
}

// Allows reports whether a holder of groups may ask for preset.
func (c *PolicyCloudflare) Allows(preset string, groups []string) bool {
	return slices.Contains(c.PresetsForGroups(groups), preset)
}

// validate holds the grants to the policy's groups. The presets are the service
// document's, so a grant naming one that does not exist is [CheckCloudflare]'s.
func (c *PolicyCloudflare) validate(declared func(group string) bool) []error {
	if c == nil {
		return nil
	}
	var errs []error
	seen := map[string]bool{}
	for i, g := range c.Grants {
		at := fmt.Sprintf("cloudflare.grants[%d]", i)
		switch {
		case g.Group == "":
			errs = append(errs, fmt.Errorf("%s: name the group (a person group, or a CI job group the policy declares)", at))
			continue
		case len(g.Presets) == 0:
			errs = append(errs, fmt.Errorf("%s: presets is empty, so the row grants nothing", at))
		}
		if !declared(g.Group) {
			errs = append(errs, fmt.Errorf("%s: group %q is not declared by the policy", at, g.Group))
		}
		if seen[g.Group] {
			errs = append(errs, fmt.Errorf("%s: %q has a row already; list its presets in one row", at, g.Group))
		}
		seen[g.Group] = true
		for _, p := range g.Presets {
			if !CloudflareNamePattern.MatchString(p) {
				errs = append(errs, fmt.Errorf("%s.presets: %q is not a preset name", at, p))
			}
		}
	}
	return errs
}

// CheckCloudflare holds the policy's grants to the service document's presets:
// a grant for a preset that does not exist would read as a right nobody can use.
func CheckCloudflare(cf *Cloudflare, p *PolicyDocument) error {
	if cf != nil && cf.Remote != nil {
		// The presets are the remote module's document's, which holds them to the grants.
		return nil
	}
	grants := p.Cloudflare().Grants
	var errs []error
	for i, g := range grants {
		for _, name := range g.Presets {
			if _, ok := cf.PresetOf(name); !ok {
				errs = append(errs, fmt.Errorf("policy cloudflare.grants[%d]: preset %q is not declared in the service document's cloudflare.presets", i, name))
			}
		}
	}
	return errors.Join(errs...)
}

// PresetOf is a preset by name. A nil section has none.
func (c *Cloudflare) PresetOf(name string) (CloudflarePreset, bool) {
	if c == nil {
		return CloudflarePreset{}, false
	}
	p, ok := c.Presets[name]
	return p, ok
}

// ValidateCloudflare holds the cloudflare section, and what else in the service
// document names one of its presets (ports.blob.s3.credentials), to the checks a
// schema cannot make. A document that never mentions Cloudflare passes: the
// static credentials of `ports.blob.s3.credentialsRef` need no section.
func (s *Serve) ValidateCloudflare() error {
	errs := []error{s.Cloudflare.Validate(), s.Signer.Validate()}
	if b := s.blobS3(); b != nil && b.Credentials != nil {
		if b.CredentialsRef != "" {
			errs = append(errs, errors.New("ports.blob.s3: credentialsRef and credentials are exclusive"))
		}
		p, ok := s.Cloudflare.PresetOf(b.Credentials.Preset)
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("ports.blob.s3.credentials.preset: %q is not declared in cloudflare.presets", b.Credentials.Preset))
		case !p.R2():
			errs = append(errs, fmt.Errorf("ports.blob.s3.credentials.preset: %q has no endpoint, so it is not an R2 preset", b.Credentials.Preset))
		}
	}
	return errors.Join(errs...)
}

func (s *Serve) blobS3() *PortsBlobS3 {
	if s.Ports == nil || s.Ports.Blob == nil {
		return nil
	}
	return s.Ports.Blob.S3
}
