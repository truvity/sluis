package profile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Deployment is what a deployment declares about its profiles: which framework profiles
// each is composed from and where its copies land. It is the document the
// chart renders and the writer reads, and the jobs read the same one, because
// retention is a property of the profile and every one of them has to agree
// about it.
type Deployment struct {
	// APIVersion is `audit.truvity.github.io/audit-deployment/v2`. The version
	// before it, `truvity.github.io/audit-deployment/v1`, or absent, means the
	// same document and is read with a deprecation warning.
	APIVersion string           `json:"apiVersion,omitempty"`
	Profiles   map[string]Entry `json:"profiles"`
	// Presets are the install presets this installation uses, each with the
	// storage its profiles' copies land in. A profile's preset is the lowest its
	// framework profiles can be kept under, or the stronger one it asked for
	// (Entry.Preset), and must be one of these (CheckStorage).
	Presets map[Preset]PresetStorage `json:"presets,omitempty"`
	// ExternalIdentifiersAreOpaque is the deployment saying that the
	// identifiers it receives for people outside the organisation are already
	// pseudonyms: identifiers an application minted, which name nobody without
	// that application's own database.
	//
	// Where it is true, a profile asking for `external: pseudonym` gets
	// `clear`, because encrypting an opaque identifier a second time adds a key
	// to lose and tells a reader of the archive nothing new. The writer holds
	// the deployment to it, refusing a record whose external identifier looks
	// direct.
	//
	// It defaults to false, so a deployment arrives at clear identifiers by
	// saying so and not by omission.
	ExternalIdentifiersAreOpaque bool `json:"external_identifiers_are_opaque,omitempty"`
}

// Entry is one destination: a profile of the deployment and the prefix of the
// archive its copies land under. It is composed from framework profiles, takes
// the actions of the categories it lists, and keeps its own projection of each
// record.
type Entry struct {
	Frameworks []string `json:"frameworks"`
	// Categories are the action categories (an action's `category` in its
	// catalogue) this destination takes. A record is written once by its emitter
	// and the writer stores a projection of it, only this destination's
	// fields, under every destination that takes its category. A destination that
	// lists none keeps only the actions that name it in their deprecated
	// `profiles`.
	Categories []string `json:"categories,omitempty"`
	// Preset is this destination's install preset, when it asks for more than
	// its framework profiles need; asking for less is refused. It names the
	// preset, and so the storage, the destination's copies land in.
	Preset Preset `json:"preset,omitempty"`
}

// PresetStorage is where one install preset keeps its copies: a bucket of its
// own, on AWS S3 or on an S3-compatible store.
//
// Object Lock is a property of the preset's bucket: the attested preset's is
// compliance Object Lock on S3, and every other preset's is none.
type PresetStorage struct {
	Bucket string `json:"bucket"`
	// Prefix is the prefix within the bucket every key of this preset lives
	// under. Required wherever the bucket is shared with another installation.
	Prefix string `json:"prefix,omitempty"`
	Region string `json:"region,omitempty"`
	// Endpoint is the URL of an S3-compatible store that is not AWS. Empty is
	// AWS S3.
	Endpoint string `json:"endpoint,omitempty"`
	// PathStyle addresses the bucket as endpoint/bucket/key, for a store whose
	// certificate does not cover a bucket subdomain. Only with Endpoint.
	PathStyle bool `json:"path_style,omitempty"`
	// Credentials is the address, below the installation's state root, of the
	// static credentials of a store at an endpoint ({accessKeyID,
	// secretAccessKey}). Only with Endpoint: on AWS the workload's identity is
	// the credential.
	Credentials string `json:"credentials,omitempty"`
	// CredentialsPreset makes the process mint the store's R2 credentials for
	// itself instead of reading static ones: it clones a Cloudflare prototype
	// token with a minter token and renews before they expire. Exclusive with
	// Credentials, and only with Endpoint. The static Credentials stay the
	// default and the simpler path.
	//
	// It needs the minter credential, which can mint anything the Cloudflare
	// account owner can, in the custody of every process that reads the
	// archive. CredentialsRef is the mode to prefer.
	CredentialsPreset *CredentialsPreset `json:"credentials_preset,omitempty"`
	// CredentialsRef is the address, in the secret store of the sluis
	// installation that rotates them, of the R2 credentials sluis keeps for a
	// Cloudflare preset: `external/cloudflare/<preset>`, a `cloudflare/v1`
	// document (ADR 0070). The process reads it below `archive.sluisRoot` on
	// SSM, or below `archive.sluisDir` where a secrets operator projected it,
	// and reads it again before the credential expires and after a 403. It
	// mints nothing and holds no minter. Exclusive with Credentials and
	// CredentialsPreset, and only with Endpoint.
	CredentialsRef string `json:"credentials_ref,omitempty"`
	// KeyAlias is the alias of the KMS key this preset's objects are encrypted
	// with, a name and not a key (`alias/...`). Empty is the installation's
	// archive key (or the bucket's default). Only on AWS S3.
	KeyAlias string `json:"key_alias,omitempty"`
}

// CredentialsPreset is how a store's R2 credentials are minted: the Cloudflare
// account, the address of the minter credential, the disabled prototype token
// whose policies are the rights, and how long each minted token lives.
type CredentialsPreset struct {
	// Account is the Cloudflare account id.
	Account string `json:"account"`
	// Minter is the address, below the installation's state root, of the minter
	// credential: a `cloudflare-minter/v1` document {schema, token}.
	Minter string `json:"minter"`
	// Prototype is the id of the DISABLED account token to clone.
	Prototype string `json:"prototype"`
	// Lifetime is how long each minted token lives, a Go duration of at least
	// a minute (`15m`). The credentials are renewed with a third of it left.
	Lifetime string `json:"lifetime"`
}

// LifetimeDuration is Lifetime as a duration.
func (c CredentialsPreset) LifetimeDuration() (time.Duration, error) {
	d, err := time.ParseDuration(c.Lifetime)
	if err != nil {
		return 0, fmt.Errorf("lifetime %q: %w", c.Lifetime, err)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("lifetime %s is under a minute", d)
	}
	return d, nil
}

// External reports whether the preset's store is an S3-compatible one that is
// not AWS.
func (s PresetStorage) External() bool { return s.Endpoint != "" }

// DeploymentAPIVersion is the version of the deployment document this build
// writes and reads first. DeploymentAPIVersionV1 is the one before it, which is
// read for one minor with a warning: the document did not change, only its group.
const (
	DeploymentAPIVersion   = "audit.truvity.github.io/audit-deployment/v2"
	DeploymentAPIVersionV1 = "truvity.github.io/audit-deployment/v1"
)

// ParseDeployment reads a deployment document. Unknown keys are refused: a
// misspelt field in a document that decides retention is not one to ignore.
func ParseDeployment(raw []byte) (*Deployment, error) {
	var d Deployment
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("deployment: %w", err)
	}
	switch d.APIVersion {
	case DeploymentAPIVersion:
	case "", DeploymentAPIVersionV1:
		slog.WarnContext(context.Background(), "the deployment document is in version 1, which is deprecated and read for one minor only: "+
			"set apiVersion to "+DeploymentAPIVersion, slog.String("api_version", d.APIVersion))
	default:
		return nil, fmt.Errorf("deployment: apiVersion %q is not one this build reads (%s, or %s)",
			d.APIVersion, DeploymentAPIVersion, DeploymentAPIVersionV1)
	}
	if len(d.Profiles) == 0 {
		return nil, errors.New("deployment: no profiles")
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	return &d, nil
}

// CredentialsRefPattern is a `credentials_ref`: the external address sluis
// keeps an R2 preset's rotated credential at, `external/cloudflare/<preset>`,
// with the preset named as sluis names it (a DNS label).
const CredentialsRefPattern = `^external/cloudflare/[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`

var (
	credentialsRefRE = regexp.MustCompile(CredentialsRefPattern)
	categoryRE       = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	aliasRE          = regexp.MustCompile(`^alias/[A-Za-z0-9/_-]+$`)
)

// check is what the strict parse cannot say about destinations: the names of
// their categories and keys, and that two destinations that share a key say so
// on purpose.
func (d *Deployment) check() error {
	var problems []error
	for name, e := range d.Profiles {
		seen := map[string]bool{}
		for _, c := range e.Categories {
			switch {
			case !categoryRE.MatchString(c):
				problems = append(problems, fmt.Errorf("deployment: profile %s: category %q is not a lower-case name", name, c))
			case seen[c]:
				problems = append(problems, fmt.Errorf("deployment: profile %s: category %q is listed twice", name, c))
			}
			seen[c] = true
		}
		if e.Preset != "" && !e.Preset.Valid() {
			problems = append(problems, fmt.Errorf("deployment: profile %s: preset %q is not one of operational, standard, attested", name, e.Preset))
		}
	}
	for _, name := range d.presetNames() {
		problems = append(problems, d.Presets[name].check(name)...)
	}
	return errors.Join(problems...)
}

// presetNames lists the configured presets, weakest first.
func (d *Deployment) presetNames() []Preset {
	out := make([]Preset, 0, len(d.Presets))
	for name := range d.Presets {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank() != out[j].Rank() {
			return out[i].Rank() < out[j].Rank()
		}
		return out[i] < out[j]
	})
	return out
}

// check is what the strict parse cannot say about one preset's storage.
func (s PresetStorage) check(name Preset) []error {
	var problems []error
	if !name.Valid() {
		return []error{fmt.Errorf("deployment: presets: %q is not one of operational, standard, attested", name)}
	}
	if s.Bucket == "" {
		problems = append(problems, fmt.Errorf("deployment: preset %s: bucket is required", name))
	}
	if s.KeyAlias != "" && !aliasRE.MatchString(s.KeyAlias) {
		problems = append(problems, fmt.Errorf("deployment: preset %s: key_alias %q must be an alias (alias/<name>), never a key id or ARN", name, s.KeyAlias))
	}
	if s.Prefix != "" && (strings.HasPrefix(s.Prefix, "/") || !strings.HasSuffix(s.Prefix, "/")) {
		problems = append(problems, fmt.Errorf("deployment: preset %s: prefix %q is a path ending in a slash "+
			"and not starting with one (operational/)", name, s.Prefix))
	}
	if c := s.CredentialsPreset; c != nil {
		if s.Credentials != "" {
			problems = append(problems, fmt.Errorf("deployment: preset %s names credentials and credentials_preset: "+
				"static credentials or minted ones, not both", name))
		}
		if !s.External() {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_preset mints R2 credentials for a store at an endpoint; "+
				"set endpoint, or leave credentials_preset out", name))
		}
		if c.Account == "" || c.Minter == "" || c.Prototype == "" {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_preset needs account, minter and prototype", name))
		}
		if strings.Contains(c.Minter, "..") || strings.HasPrefix(c.Minter, "/") {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_preset.minter %q is an address below the state root", name, c.Minter))
		}
		if _, err := c.LifetimeDuration(); err != nil {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_preset.%w", name, err))
		}
	}
	if ref := s.CredentialsRef; ref != "" {
		if s.Credentials != "" || s.CredentialsPreset != nil {
			problems = append(problems, fmt.Errorf("deployment: preset %s names credentials_ref and credentials or credentials_preset: "+
				"one source of credentials, not two", name))
		}
		if !s.External() {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_ref names R2 credentials for a store at an endpoint; "+
				"set endpoint, or leave credentials_ref out", name))
		}
		if !credentialsRefRE.MatchString(ref) {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials_ref %q is not external/cloudflare/<preset>, "+
				"the address sluis keeps an R2 preset's rotated credentials at", name, ref))
		}
	}
	if s.External() {
		if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			problems = append(problems, fmt.Errorf("deployment: preset %s: endpoint %q is not an http(s) URL", name, s.Endpoint))
		}
		if s.KeyAlias != "" {
			problems = append(problems, fmt.Errorf("deployment: preset %s names key_alias %s and its store is the S3-compatible endpoint %s, "+
				"which is not encrypted under a KMS key of the account: leave key_alias out, or keep the preset on AWS S3", name, s.KeyAlias, s.Endpoint))
		}
		if name == Attested {
			problems = append(problems, fmt.Errorf("deployment: preset attested is on the S3-compatible endpoint %s, which has no Object Lock: "+
				"the attested preset keeps its objects under compliance Object Lock, which is S3 only. Keep it on AWS S3", s.Endpoint))
		}
	} else {
		if s.Credentials != "" {
			problems = append(problems, fmt.Errorf("deployment: preset %s: credentials are static credentials for a store at an endpoint; "+
				"on AWS the workload's identity is the credential (set endpoint, or leave credentials out)", name))
		}
		if s.PathStyle {
			problems = append(problems, fmt.Errorf("deployment: preset %s: path_style is for a store at an endpoint", name))
		}
	}
	return problems
}

// LockMode is the Object Lock the preset's bucket is written under:
// compliance for the attested preset and none for every other.
func (p Preset) LockMode() string {
	if p.Features().ObjectLock {
		return "compliance"
	}
	return "none"
}

// CheckStorage holds the deployment's profiles to its presets: every profile's
// preset (the lowest its framework profiles can be kept under, or the stronger
// one it asked for) must be one the deployment configures, and at least one
// preset must be. It composes the profiles, so a profile that names an
// unknown framework profile, or asks for a weaker preset than it needs, is
// refused here too.
func (d *Deployment) CheckStorage(frameworks map[string]*Framework) error {
	if len(d.Presets) == 0 {
		return errors.New("deployment: no presets: name the storage of each install preset the profiles use " +
			"(presets: {standard: {bucket: ..., prefix: ...}})")
	}
	needs, err := d.ProfileNeeds(frameworks)
	if err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	names := make([]string, 0, len(needs))
	for name := range needs {
		names = append(names, name)
	}
	sort.Strings(names)
	var problems []error
	for _, name := range names {
		n := needs[name]
		if _, ok := d.Presets[n.Preset]; !ok {
			why := "its framework profiles " + strings.Join(n.By, ", ") + " need it"
			if len(n.By) == 0 {
				why = "it is the lowest preset its framework profiles can be kept under, or the one it asks for"
			}
			problems = append(problems, fmt.Errorf("deployment: profile %s is kept under the %s preset (%s) and presets configures only %s: "+
				"configure presets.%s, or set the profile's preset to one that is configured",
				name, n.Preset, why, joinPresets(d.presetNames()), n.Preset))
		}
	}
	return errors.Join(problems...)
}

func joinPresets(ps []Preset) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

// Features is what the installation provisions: what any of its configured
// presets does. An installation of an operational and a standard preset runs
// the notary and has alarms; one with an attested preset has Object Lock on
// that preset's bucket and pseudonym keys.
func (d *Deployment) Features() Features {
	var f Features
	for name := range d.Presets {
		g := name.Features()
		f.Notary = f.Notary || g.Notary
		f.Alarms = f.Alarms || g.Alarms
		f.ObjectLock = f.ObjectLock || g.ObjectLock
		f.PseudonymKeys = f.PseudonymKeys || g.PseudonymKeys
	}
	return f
}

// Compose resolves every profile a deployment declares.
//
// This is where ExternalIdentifiersAreOpaque takes effect, so that what a
// profile says it keeps is what it keeps: `audit profile explain` prints the
// composed profile, and a treatment the deployment has relaxed should not be
// something a reader has to know to subtract.
func (d *Deployment) Compose(frameworks map[string]*Framework) (map[string]*Profile, error) {
	out := make(map[string]*Profile, len(d.Profiles))
	for name, c := range d.Profiles {
		p, err := Compose(Composition{Name: name, Frameworks: c.Frameworks}, frameworks)
		if err != nil {
			return nil, err
		}
		if d.ExternalIdentifiersAreOpaque && p.Identity[External] == Pseudonym {
			p.Identity[External] = Clear
			p.OpaqueExternal = true
		}
		p.Categories = append([]string(nil), c.Categories...)
		out[name] = p
	}
	needs, err := d.ProfileNeeds(frameworks)
	if err != nil {
		return nil, err
	}
	for name, p := range out {
		p.Preset = needs[name].Preset
		if explicit := d.Profiles[name].Preset; explicit.Rank() > p.Preset.Rank() {
			p.Preset = explicit
		}
	}
	return out, nil
}

// Pseudonymises reports whether the profile replaces any identity with a keyed
// pseudonym, which is what needs the pseudonym key.
func (p *Profile) Pseudonymises() bool {
	for _, t := range p.Identity {
		if t == Pseudonym {
			return true
		}
	}
	return false
}

// PseudonymProfiles names, sorted, the profiles of the deployment that
// pseudonymise an identity (after ExternalIdentifiersAreOpaque is applied). An
// installation provisions pseudonym keys when its preset does (attested) or
// this is not empty.
func (d *Deployment) PseudonymProfiles(frameworks map[string]*Framework) ([]string, error) {
	composed, err := d.Compose(frameworks)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, p := range composed {
		if p.Pseudonymises() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DefaultDeployment is what a deployment gets when it declares nothing: one
// profile per framework profile, named for the framework profile. It is a starting point for
// looking at what the framework profiles keep, not a recommendation.
func DefaultDeployment(frameworks map[string]*Framework) *Deployment {
	d := &Deployment{Profiles: map[string]Entry{}}
	for name := range frameworks {
		d.Profiles[name] = Entry{Frameworks: []string{name}}
	}
	return d
}
