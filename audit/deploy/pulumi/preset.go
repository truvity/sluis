package auditpulumi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/truvity/sluis/audit/profile"
)

// Install presets. The library provisions as much of the system as the
// installation's preset says: operational is the writer, the archive,
// deduplication and the queue intake; standard adds the notary with its seal key
// and the alarms; attested adds compliance Object Lock (and is where pseudonym
// keys are provisioned).
//
// The preset is derived from the profiles, with the one rule the chart and the
// writer use (profile.Deployment.ResolvePreset): the lowest preset that satisfies
// every profile, each framework profile stating its own minimum as `min_preset`.
// Args.Preset may name a stronger one; a weaker one is refused, naming the
// profile that needs more.
const (
	// PresetOperational, PresetStandard and PresetAttested are the values of
	// Args.Preset.
	PresetOperational = string(profile.Operational)
	PresetStandard    = string(profile.Standard)
	PresetAttested    = string(profile.Attested)
)

// resolvePreset is the preset an installation runs. The profiles are the
// `profiles:` of Writer.DeploymentYAML, the document the writer reads. An
// installation without a writer here (Ingest.Disabled) and no document has no
// profiles to derive from, and is operational unless it asks for more.
func resolvePreset(a *Args) (profile.Preset, []destination, error) {
	explicit, err := profile.ParsePreset(a.Preset)
	if err != nil {
		return "", nil, fmt.Errorf("auditpulumi: Preset: %w", err)
	}
	if strings.TrimSpace(a.Writer.DeploymentYAML) == "" {
		if !a.Ingest.Disabled {
			return "", nil, errors.New("auditpulumi: Writer.DeploymentYAML is required: the profile configuration, which the preset is derived from (or set Ingest.Disabled)")
		}
		if explicit == "" {
			return profile.Operational, nil, nil
		}
		return explicit, nil, nil
	}
	d, err := profile.ParseDeployment([]byte(a.Writer.DeploymentYAML))
	if err != nil {
		return "", nil, fmt.Errorf("auditpulumi: Writer.DeploymentYAML: %w", err)
	}
	if explicit != "" && d.Preset != "" && explicit != d.Preset {
		return "", nil, fmt.Errorf("auditpulumi: Preset is %s and Writer.DeploymentYAML says preset %s: set one of them", explicit, d.Preset)
	}
	frameworks, err := profile.Builtin()
	if err != nil {
		return "", nil, fmt.Errorf("auditpulumi: the framework profiles: %w", err)
	}
	p, err := d.ResolvePreset(frameworks, explicit)
	if err != nil {
		return "", nil, fmt.Errorf("auditpulumi: Preset: %w", err)
	}
	composed, err := d.Compose(frameworks)
	if err != nil {
		return "", nil, fmt.Errorf("auditpulumi: Writer.DeploymentYAML: %w", err)
	}
	names := make([]string, 0, len(composed))
	for name := range composed {
		names = append(names, name)
	}
	sort.Strings(names)
	dests := make([]destination, 0, len(names))
	for _, name := range names {
		c := composed[name]
		dest := destination{Name: name, KeyAlias: c.KeyAlias, Preset: c.Preset}
		if r := c.Retention; r.Policy == "fixed" && r.DeleteAtEnd {
			dest.RetentionDays = r.Days
		}
		dests = append(dests, dest)
	}
	return p, dests, nil
}

// destination is a destination of the archive as the deployment document
// declares it: a prefix with its own retention, key and preset.
type destination struct {
	Name string
	// KeyAlias, when set, is the alias of a key the library creates for this
	// destination's objects.
	KeyAlias string
	// RetentionDays is when the prefix's objects expire, from the destination's
	// framework profiles; 0 means the profile keeps them until told otherwise.
	RetentionDays int
	Preset        profile.Preset
}

// applyPreset sets what the preset decides and refuses what it leaves out and
// the arguments ask for anyway.
func applyPreset(c *Args, preset profile.Preset, dests []destination) error {
	c.destinations = dests
	f := preset.Features()
	c.Preset = string(preset)

	if !f.Notary {
		if !c.Notary.Disabled && (c.Notary.Package != "" || c.Notary.PackageSHA256 != "") {
			return fmt.Errorf("auditpulumi: Notary.Package is set and the preset is %s, which has no notary and no seal key: "+
				"the profiles need nothing more. Set Preset to %s (or compose a profile that needs it, such as security) to run the notary, "+
				"or leave Notary out", preset, profile.Standard)
		}
		c.Notary.Disabled = true
	}

	if !f.Alarms && c.Alerts.EndpointURL != nil {
		return fmt.Errorf("auditpulumi: Alerts.EndpointURL is set and the preset is %s, which has no alarms. "+
			"Set Preset to %s to have them", preset, profile.Standard)
	}

	// Object Lock is the attested preset's alone: a destination below it writes
	// objects it can clear, so a bucket under a lock for an archive with no
	// attested destination would lock nothing that was meant to be locked and
	// leave a default retention that holds the rest.
	ar := &c.Archive
	if ar.ObjectLockMode != "" {
		if err := CheckLockMode(ar.ObjectLockMode); err != nil {
			return err
		}
	}
	if ar.Endpoint != "" {
		// Object Lock is an AWS S3 guarantee; another store does not make it.
		if f.ObjectLock {
			return fmt.Errorf("auditpulumi: Archive.Endpoint is set and the preset is %s, which keeps the archive under compliance Object Lock: "+
				"a store at an endpoint of its own does not make that guarantee. Use AWS S3 for this installation, or a preset below %s "+
				"(compose profiles that need no more)", preset, profile.Attested)
		}
		if ar.ObjectLockMode != "" && ar.ObjectLockMode != None {
			return fmt.Errorf("auditpulumi: Archive.ObjectLockMode is %s with Archive.Endpoint: Object Lock is refused on an S3-compatible store "+
				"(NONE is the only mode there)", ar.ObjectLockMode)
		}
	}
	switch {
	case ar.ObjectLockMode == "" && f.ObjectLock:
		ar.ObjectLockMode = Compliance
	case ar.ObjectLockMode == "":
		ar.ObjectLockMode = None
	case !f.ObjectLock && ar.ObjectLockMode != None:
		return fmt.Errorf("auditpulumi: Archive.ObjectLockMode is %s and the archive has no attested destination (the preset is %s): "+
			"Object Lock is written only for a destination whose preset is attested, on S3. Compose a destination from framework profiles "+
			"that need it (dora, pci-dss, nen-7513, evidence-etsi) or give it preset: attested, or set Archive.ObjectLockMode to NONE",
			ar.ObjectLockMode, preset)
	case f.ObjectLock && ar.ObjectLockMode == None:
		return fmt.Errorf("auditpulumi: Archive.ObjectLockMode is NONE and the preset is %s, which keeps the archive under Object Lock "+
			"(GOVERNANCE for a trial, COMPLIANCE for the target: docs/how-to/aws-turn-on-object-lock.md)", preset)
	}
	return nil
}

// features is what the resolved preset provisions.
func (a *Args) features() profile.Features { return profile.Preset(a.Preset).Features() }
