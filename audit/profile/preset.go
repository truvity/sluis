package profile

import (
	"fmt"
	"sort"
	"strings"
)

// Preset is an install preset: how much of the audit system an installation
// provisions. It is not a framework profile (what a framework asks of a copy)
// and not a grant preset (what a role may read).
//
// The preset is derived. Each framework profile states the lowest preset it
// can be kept under (min_preset in its file) and an installation's preset is the
// highest of those over every profile it composes. A deployment may ask for a
// stronger preset than it needs; asking for a weaker one is refused, naming the
// profile that needs more.
type Preset string

const (
	// Operational is the writer, the archive, deduplication and the queue or
	// HTTP intake. No notary, no seal key, no Object Lock, no pseudonym keys,
	// and no alarms. It is also what an installation gets when it chooses
	// nothing.
	Operational Preset = "operational"
	// Standard adds the notary and its seal key, and the alarms.
	Standard Preset = "standard"
	// Attested adds compliance Object Lock and the pseudonym keys.
	Attested Preset = "attested"
)

// Presets lists the install presets, weakest first.
var Presets = []Preset{Operational, Standard, Attested}

// Rank orders presets from operational to attested. An unknown preset ranks
// below every known one.
func (p Preset) Rank() int {
	for i, q := range Presets {
		if p == q {
			return i
		}
	}
	return -1
}

// Valid reports whether p is one of the three presets.
func (p Preset) Valid() bool { return p.Rank() >= 0 }

// AtLeast reports whether p provisions everything q does.
func (p Preset) AtLeast(q Preset) bool { return p.Rank() >= q.Rank() }

// Features is what a preset provisions.
type Features struct {
	// Notary is the notary, its schedule and the seal key it signs with.
	Notary bool
	// Alarms are the alarms and alert rules.
	Alarms bool
	// ObjectLock is compliance Object Lock on the archive.
	ObjectLock bool
	// PseudonymKeys are the keys identities are pseudonymised under.
	PseudonymKeys bool
}

// Features returns what the preset provisions.
func (p Preset) Features() Features {
	return Features{
		Notary:        p.AtLeast(Standard),
		Alarms:        p.AtLeast(Standard),
		ObjectLock:    p.AtLeast(Attested),
		PseudonymKeys: p.AtLeast(Attested),
	}
}

// ParsePreset reads a preset name. The empty string is "not chosen" and is
// returned as the empty preset, not as an error.
func ParsePreset(s string) (Preset, error) {
	if s == "" {
		return "", nil
	}
	p := Preset(s)
	if !p.Valid() {
		names := make([]string, len(Presets))
		for i, q := range Presets {
			names[i] = string(q)
		}
		return "", fmt.Errorf("preset %q is not one of %s", s, strings.Join(names, ", "))
	}
	return p, nil
}

// Needs is a profile's lowest preset and the framework profiles that set it.
type Needs struct {
	Preset Preset
	// By names the framework profiles whose minimum is Preset.
	By []string
}

// ProfileNeeds is the lowest preset each profile of a deployment can be kept
// under: the highest minimum of the framework profiles it is composed from.
func (d *Deployment) ProfileNeeds(frameworks map[string]*Framework) (map[string]Needs, error) {
	out := make(map[string]Needs, len(d.Profiles))
	for name, entry := range d.Profiles {
		n := Needs{Preset: Operational}
		for _, fw := range entry.Frameworks {
			f, ok := frameworks[fw]
			if !ok {
				return nil, fmt.Errorf("profile %s: no framework profile named %q", name, fw)
			}
			min := f.MinPreset
			if min == "" {
				min = Operational
			}
			switch {
			case min.Rank() > n.Preset.Rank():
				n = Needs{Preset: min, By: []string{fw}}
			case min == n.Preset && min.Rank() > 0:
				n.By = append(n.By, fw)
			}
		}
		sort.Strings(n.By)
		out[name] = n
	}
	return out, nil
}

// Derive is the lowest preset that satisfies every profile of the deployment,
// which is operational when it has none.
func (d *Deployment) Derive(frameworks map[string]*Framework) (Preset, error) {
	needs, err := d.ProfileNeeds(frameworks)
	if err != nil {
		return "", err
	}
	derived := Operational
	for _, n := range needs {
		if n.Preset.Rank() > derived.Rank() {
			derived = n.Preset
		}
	}
	return derived, nil
}

// ResolvePreset is the preset an installation runs: the derived one, or the
// explicit one when it is at least as strong. An explicit preset (the
// deployment's own `preset:` when it has one, otherwise the argument) weaker
// than a profile needs is refused, naming the profile and the framework
// profiles that need more.
func (d *Deployment) ResolvePreset(frameworks map[string]*Framework, explicit Preset) (Preset, error) {
	if explicit == "" {
		explicit = d.Preset
	}
	if explicit != "" && !explicit.Valid() {
		return "", fmt.Errorf("deployment: preset %q is not one of operational, standard, attested", explicit)
	}
	derived, err := d.Derive(frameworks)
	if err != nil {
		return "", err
	}
	if explicit == "" {
		return derived, nil
	}
	if explicit.Rank() >= derived.Rank() {
		return explicit, nil
	}
	needs, _ := d.ProfileNeeds(frameworks)
	names := make([]string, 0, len(needs))
	for name := range needs {
		names = append(names, name)
	}
	sort.Strings(names)
	var msgs []string
	for _, name := range names {
		if n := needs[name]; n.Preset.Rank() > explicit.Rank() {
			msgs = append(msgs, fmt.Sprintf("profile %s (framework profiles %s) needs %s",
				name, strings.Join(n.By, ", "), n.Preset))
		}
	}
	return "", fmt.Errorf("preset %s is weaker than the profiles need: %s. Set preset to %s or stronger, "+
		"or compose the profile from framework profiles that need less", explicit, strings.Join(msgs, "; "), derived)
}
