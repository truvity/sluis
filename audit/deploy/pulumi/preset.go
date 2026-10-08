package auditpulumi

import (
	"errors"
	"fmt"
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
func resolvePreset(a *Args) (profile.Preset, error) {
	explicit, err := profile.ParsePreset(a.Preset)
	if err != nil {
		return "", fmt.Errorf("auditpulumi: Preset: %w", err)
	}
	if strings.TrimSpace(a.Writer.DeploymentYAML) == "" {
		if !a.Ingest.Disabled {
			return "", errors.New("auditpulumi: Writer.DeploymentYAML is required: the profile configuration, which the preset is derived from (or set Ingest.Disabled)")
		}
		if explicit == "" {
			return profile.Operational, nil
		}
		return explicit, nil
	}
	d, err := profile.ParseDeployment([]byte(a.Writer.DeploymentYAML))
	if err != nil {
		return "", fmt.Errorf("auditpulumi: Writer.DeploymentYAML: %w", err)
	}
	if explicit != "" && d.Preset != "" && explicit != d.Preset {
		return "", fmt.Errorf("auditpulumi: Preset is %s and Writer.DeploymentYAML says preset %s: set one of them", explicit, d.Preset)
	}
	frameworks, err := profile.Builtin()
	if err != nil {
		return "", fmt.Errorf("auditpulumi: the framework profiles: %w", err)
	}
	p, err := d.ResolvePreset(frameworks, explicit)
	if err != nil {
		return "", fmt.Errorf("auditpulumi: Preset: %w", err)
	}
	return p, nil
}

// applyPreset sets what the preset decides and refuses what it leaves out and
// the arguments ask for anyway.
func applyPreset(c *Args, preset profile.Preset) error {
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

	ar := &c.Archive
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
	case f.ObjectLock && ar.ObjectLockMode != Compliance:
		return fmt.Errorf("auditpulumi: Archive.ObjectLockMode is %s and the preset is %s, which keeps the archive under compliance Object Lock "+
			"(docs/how-to/aws-turn-on-object-lock.md)", ar.ObjectLockMode, preset)
	}
	return nil
}

// features is what the resolved preset provisions.
func (a *Args) features() profile.Features { return profile.Preset(a.Preset).Features() }
