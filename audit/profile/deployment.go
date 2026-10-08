package profile

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"

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
	// Preset is the install preset the deployment asks for: operational,
	// standard or attested. Optional. Unset, the preset is the lowest one that
	// satisfies every profile (Derive); set, it may be stronger than that and is
	// refused when weaker (ResolvePreset).
	Preset Preset `json:"preset,omitempty"`
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
	// KeyAlias is the alias of the key this destination's objects are encrypted
	// with, a name and not a key (`alias/...`). Empty is the archive's key.
	KeyAlias string `json:"key_alias,omitempty"`
	// Preset is this destination's install preset, when it asks for more than
	// its framework profiles need. Object Lock is the writer's only for a
	// destination whose preset is attested.
	Preset Preset `json:"preset,omitempty"`
}

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
	if err := refuseOldKey(raw); err != nil {
		return nil, err
	}
	var d Deployment
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("deployment: %w", err)
	}
	switch d.APIVersion {
	case DeploymentAPIVersion:
	case "", DeploymentAPIVersionV1:
		slog.Warn("the deployment document is in version 1, which is deprecated and read for one minor only: "+
			"set apiVersion to "+DeploymentAPIVersion, "apiVersion", d.APIVersion)
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

var (
	categoryRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	aliasRE    = regexp.MustCompile(`^alias/[A-Za-z0-9/_-]+$`)
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
		if e.KeyAlias != "" && !aliasRE.MatchString(e.KeyAlias) {
			problems = append(problems, fmt.Errorf("deployment: profile %s: key_alias %q must be an alias (alias/<name>), never a key id or ARN", name, e.KeyAlias))
		}
		if e.Preset != "" && !e.Preset.Valid() {
			problems = append(problems, fmt.Errorf("deployment: profile %s: preset %q is not one of operational, standard, attested", name, e.Preset))
		}
	}
	return errors.Join(problems...)
}

// refuseOldKey names the new key to a document that still uses the old one.
// A profile's framework profiles were listed under `presets:` before the
// rename; strict parsing would call the key unknown and say nothing of where it
// went, and "preset" now means something else.
func refuseOldKey(raw []byte) error {
	var doc struct {
		Profiles map[string]map[string]any `json:"profiles"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil // the strict parse says what is wrong with it
	}
	for name, entry := range doc.Profiles {
		if _, old := entry["presets"]; old {
			return fmt.Errorf("deployment: profile %s: the key `presets` is now `frameworks` (the framework profiles it is composed from); rename it", name)
		}
	}
	return nil
}

// Compose resolves every profile a deployment declares.
//
// This is where ExternalIdentifiersAreOpaque takes effect, so that what a
// profile says it keeps is what it keeps: `audit profile explain` prints the
// composed profile, and a treatment the deployment has relaxed should not be
// something a reader has to know to subtract.
func (d *Deployment) Compose(frameworks map[string]*Framework) (map[string]*Profile, error) {
	if _, err := d.ResolvePreset(frameworks, ""); err != nil {
		return nil, fmt.Errorf("deployment: %w", err)
	}
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
		p.KeyAlias = c.KeyAlias
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
