package auditpulumi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/audit/profile"
)

// Install presets. The library provisions as much of the system as the
// presets an installation configures say: operational is the writer, the
// archive, deduplication and the queue intake; standard adds the notary with its
// seal key and the alarms; attested adds compliance Object Lock (on that
// preset's bucket alone) and is where pseudonym keys are provisioned.
//
// A profile's preset is derived (profile.Deployment.ProfileNeeds): the highest
// minimum of the framework profiles it is composed from, or the stronger one it
// asks for. Each preset an installation's profiles use is configured in
// Args.Presets with the storage of its own, and a profile whose preset is not
// configured is refused, naming both.
const (
	// PresetOperational, PresetStandard and PresetAttested are the keys of
	// Args.Presets.
	PresetOperational = string(profile.Operational)
	PresetStandard    = string(profile.Standard)
	PresetAttested    = string(profile.Attested)
)

// PresetStorage is where one install preset keeps its copies: a bucket of its
// own on AWS S3 or on an S3-compatible store.
//
// Object Lock is a property of the attested preset's bucket alone (compliance,
// S3 only): every other preset's bucket is written without a lock, so a profile
// below attested writes nothing it cannot clear.
type PresetStorage struct {
	// Bucket is the bucket's name. Required: it is in the deployment document the
	// functions read, so it has to be known before anything is created, and a
	// bucket name is global. With Create it is the bucket the library makes
	// (PresetBucketName gives a conventional name).
	Bucket string
	// Prefix is the prefix within the bucket every key of this preset lives
	// under, such as "standard/": it ends in "/" and does not start with one.
	// Optional, but required wherever the bucket is shared with another
	// installation or preset. The roles' grants are scoped to it.
	Prefix string
	// Region is the bucket's region on AWS (default: the stack's), and the
	// region the store is addressed with at an Endpoint (default "auto", which is
	// what R2 signs with).
	Region string
	// Endpoint, when set, puts the preset on an S3-compatible store that is not
	// AWS (Cloudflare R2 is the one this has been measured against): an https
	// URL with a host and no path. The bucket is the store's, made there by the
	// estate: the library creates no bucket, no lifecycle rule and no encryption
	// setting for it, and no role is granted anything on it. Empty is AWS S3.
	// The attested preset is refused here: Object Lock is an AWS S3 guarantee.
	Endpoint string
	// PathStyle addresses the bucket as endpoint/bucket/key instead of
	// bucket.endpoint/key, for a store whose certificate does not cover a bucket
	// subdomain. Only with Endpoint.
	PathStyle bool
	// CredentialsAddress is where the store's credentials are, below State.Root: a
	// JSON object {"accessKeyID": ..., "secretAccessKey": ...} written by the
	// operator as a SecureString, read by the functions through the state store.
	// Must be below internal/. Default "internal/archive/<preset>". Only with
	// Endpoint: on AWS the roles are the credential.
	CredentialsAddress string
	// CredentialsPreset makes the functions mint the store's R2 credentials for
	// themselves (the deployment document's `credentials_preset`): they clone a
	// disabled Cloudflare prototype with the minter token kept at Minter, below
	// State.Root, and record the ids they mint at cloudflare-minted/<preset>
	// there. The roles are granted read on exactly the Minter address and read and
	// write on exactly that record, and nothing else for it. Exclusive with
	// CredentialsAddress, and only with Endpoint. Unset (the default) is the
	// static credentials, which need no Cloudflare account.
	CredentialsPreset *profile.CredentialsPreset
	// KeyAlias is the alias (`alias/...`) of the KMS key this preset's objects are
	// encrypted with, in place of the installation's archive key. It is looked up
	// and never created, and is also the document's key_alias. Only on AWS S3, and
	// only with Archive.Encryption "kms".
	KeyAlias string
	// Create makes the library create the bucket: versioned, closed to the public
	// and to plain HTTP, encrypted as ArchiveArgs says, with a lifecycle rule for
	// each `<Prefix>records/<profile>/` of the profiles kept under this preset,
	// and, for the attested preset, compliance Object Lock. Only on AWS S3. False
	// is an existing bucket, which the library only grants access to (or adopts,
	// see Adopt).
	Create bool
	// Adopt makes the library manage an EXISTING bucket's settings and leave its
	// lifecycle exactly as it is: the bucket is imported by name (never created,
	// Protect and RetainOnDelete: a destroy of the stack leaves it), and the
	// library declares its versioning (Enabled), default encryption (as ArchiveArgs
	// says), public-access block, ownership controls (BucketOwnerEnforced) and
	// bucket policy (the TLS-only deny). It declares NO lifecycle configuration, so
	// the rules the bucket has stay as they are: an estate whose archive must never
	// expire says so by having no expiration rule there. Object Lock is not
	// touched either (it is a property of how the bucket was made). The first
	// `pulumi up` imports the bucket and its settings and then changes them to
	// these; read the preview. The bucket policy is replaced as a whole by the
	// library's, so statements the bucket already has must not be relied on after
	// the import. Not with Create, not with Endpoint, and Archive.GlacierIRDays,
	// Archive.DeepArchiveDays and the lock settings do not apply to it.
	Adopt bool
	// AcknowledgeLifecycle is the estate's statement, required with Adopt when a
	// profile kept in this preset has a fixed minimum retention in its framework
	// profiles, that the bucket's own lifecycle (and Object Lock) keeps objects at
	// least that long. The library cannot read the bucket's lifecycle, so it
	// cannot verify it; it refuses to adopt without the statement.
	AcknowledgeLifecycle bool
}

// managed is a bucket whose settings the library declares: one it creates, or
// one it adopts.
func (s presetStore) managed() bool { return s.Create || s.Adopt }

// PresetBucketName is the conventional name of a preset's bucket: the archive
// bucket name (ArchiveBucketName) and the preset.
func PresetBucketName(archiveBucket, preset string) string { return archiveBucket + "-" + preset }

// lifecycleProfile is a profile kept under a preset and when its objects
// expire, from its framework profiles; 0 means the profile keeps them until
// told otherwise.
type lifecycleProfile struct {
	Name          string
	RetentionDays int
	// MinimumDays is the fixed retention its framework profiles demand, whether or
	// not the objects are deleted at its end; 0 is none.
	MinimumDays int
}

// presetStore is a configured preset with its defaults applied.
type presetStore struct {
	Preset profile.Preset
	PresetStorage
	// Locked is the attested preset's bucket: written under compliance Object
	// Lock.
	Locked bool
	// Profiles are the profiles kept under this preset, sorted.
	Profiles []lifecycleProfile
}

func (s presetStore) external() bool { return s.Endpoint != "" }

// key is a key of this preset's, under its prefix.
func (s presetStore) key(rest string) string { return s.Prefix + rest }

// credentialsPath is the SSM parameter an endpoint preset's static credentials
// are at. A preset that mints its credentials has none ([presetStore.minted]).
func (s presetStore) credentialsPath(stateRoot string) string {
	return stateRoot + "/" + s.CredentialsAddress
}

// minted reports whether the preset's credentials are minted from a Cloudflare
// prototype and not read from a static document.
func (s presetStore) minted() bool { return s.CredentialsPreset != nil }

// minterPath is the SSM parameter of the minter credential of a preset that
// mints, and recordPath the one the functions record the tokens they minted in
// (storage/cloudflare.Provider: cloudflare-minted/<preset> below the state root).
func (s presetStore) minterPath(stateRoot string) string {
	return stateRoot + "/" + s.CredentialsPreset.Minter
}

func (s presetStore) recordPath(stateRoot string) string {
	return stateRoot + "/cloudflare-minted/" + string(s.Preset)
}

// resolvePresets decides what the installation's presets are: it reads the
// profiles document, takes the storage from Args.Presets (or from the document,
// when that is where it was written), checks it, and renders the deployment
// document the functions read, with the presets in it. It sets stores,
// deployment, deploymentYAML and features on the arguments.
func resolvePresets(a *Args) error {
	var d *profile.Deployment
	hasDoc := strings.TrimSpace(a.Writer.DeploymentYAML) != ""
	if hasDoc {
		var err error
		if d, err = profile.ParseDeployment([]byte(a.Writer.DeploymentYAML)); err != nil {
			return fmt.Errorf("auditpulumi: Writer.DeploymentYAML: %w", err)
		}
	}

	var in map[profile.Preset]PresetStorage
	switch {
	case d != nil && len(d.Presets) > 0 && len(a.Presets) > 0:
		return errors.New("auditpulumi: Presets is set and Writer.DeploymentYAML has a `presets:` block of its own: " +
			"the storage of each preset is named in one place. Leave `presets:` out of the document (the library renders it " +
			"from Presets), or leave Presets out")
	case len(a.Presets) > 0:
		in = make(map[profile.Preset]PresetStorage, len(a.Presets))
		for k, v := range a.Presets {
			name, err := profile.ParsePreset(k)
			if err != nil || name == "" {
				return fmt.Errorf("auditpulumi: Presets has the key %q: one of operational, standard, attested", k)
			}
			in[name] = v
		}
	case d != nil && len(d.Presets) > 0:
		in = make(map[profile.Preset]PresetStorage, len(d.Presets))
		for name, s := range d.Presets {
			in[name] = PresetStorage{
				Bucket: s.Bucket, Prefix: s.Prefix, Region: s.Region, Endpoint: s.Endpoint,
				PathStyle: s.PathStyle, CredentialsAddress: s.Credentials, KeyAlias: s.KeyAlias,
				CredentialsPreset: s.CredentialsPreset,
			}
		}
	default:
		return errors.New("auditpulumi: Presets is required: name the storage of each install preset the profiles use " +
			`(Presets: {"standard": {Bucket: ..., Prefix: "standard/", Create: true}}); a profile is kept under the ` +
			"preset its framework profiles need (operational, standard or attested)")
	}

	names := make([]profile.Preset, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Rank() < names[j].Rank() })

	stores := make([]presetStore, 0, len(names))
	for _, name := range names {
		s, err := checkPresetStorage(name, in[name])
		if err != nil {
			return err
		}
		stores = append(stores, presetStore{Preset: name, PresetStorage: s, Locked: name == profile.Attested})
	}
	if err := checkPresetBuckets(stores); err != nil {
		return err
	}

	// The document the functions read: the profiles the user wrote, and the
	// presets built from Presets. Without profiles (Ingest.Disabled, no
	// document) a placeholder profile lets the presets be validated.
	final := profile.Deployment{
		APIVersion: profile.DeploymentAPIVersion,
		Profiles:   map[string]profile.Entry{"placeholder": {Frameworks: []string{"history"}}},
		Presets:    make(map[profile.Preset]profile.PresetStorage, len(stores)),
	}
	if d != nil {
		final.Profiles = d.Profiles
		final.ExternalIdentifiersAreOpaque = d.ExternalIdentifiersAreOpaque
	}
	for _, s := range stores {
		final.Presets[s.Preset] = documentStorage(s)
	}
	raw, err := yaml.Marshal(&final)
	if err != nil {
		return fmt.Errorf("auditpulumi: rendering the deployment document: %w", err)
	}
	checked, err := profile.ParseDeployment(raw)
	if err != nil {
		return fmt.Errorf("auditpulumi: Presets: %w", mapFieldNames(err))
	}
	if d != nil {
		frameworks, err := profile.Builtin()
		if err != nil {
			return fmt.Errorf("auditpulumi: the framework profiles: %w", err)
		}
		if err := checked.CheckStorage(frameworks); err != nil {
			return fmt.Errorf("auditpulumi: Presets: %w", err)
		}
		composed, err := checked.Compose(frameworks)
		if err != nil {
			return fmt.Errorf("auditpulumi: Writer.DeploymentYAML: %w", err)
		}
		profiles := make([]string, 0, len(composed))
		for name := range composed {
			profiles = append(profiles, name)
		}
		sort.Strings(profiles)
		for _, name := range profiles {
			c := composed[name]
			lp := lifecycleProfile{Name: name}
			if r := c.Retention; r.Policy == "fixed" {
				lp.MinimumDays = r.Days
				if r.DeleteAtEnd {
					lp.RetentionDays = r.Days
				}
			}
			for i := range stores {
				if stores[i].Preset == c.Preset {
					stores[i].Profiles = append(stores[i].Profiles, lp)
				}
			}
		}
		a.hasDocument = true
	}

	a.stores = stores
	a.deployment = checked
	a.deploymentYAML = string(raw)
	a.features = checked.Features()
	return nil
}

// mapFieldNames makes a refusal of the profile package say the field of Args it
// is about, where it has a word for it.
func mapFieldNames(err error) error {
	m := strings.NewReplacer("key_alias", "KeyAlias", "path_style", "PathStyle", "credentials are", "CredentialsAddress is")
	return errors.New(m.Replace(err.Error()))
}

// documentStorage is a preset's storage as the deployment document says it.
func documentStorage(s presetStore) profile.PresetStorage {
	return profile.PresetStorage{
		Bucket: s.Bucket, Prefix: s.Prefix, Region: s.Region, Endpoint: s.Endpoint,
		PathStyle: s.PathStyle, Credentials: s.CredentialsAddress, KeyAlias: s.KeyAlias,
		CredentialsPreset: s.CredentialsPreset,
	}
}

// checkPresetStorage holds one preset's storage to what a bucket there can be,
// and applies its defaults. Every refusal names the field.
func checkPresetStorage(name profile.Preset, s PresetStorage) (PresetStorage, error) {
	field := func(f string) string { return fmt.Sprintf("Presets[%q].%s", string(name), f) }
	if s.Bucket == "" {
		return s, fmt.Errorf("auditpulumi: %s is required: the preset's bucket (PresetBucketName gives a conventional one)", field("Bucket"))
	}
	if !bucketNameRE.MatchString(s.Bucket) {
		return s, fmt.Errorf("auditpulumi: %s %q is not a bucket name (3 to 63 characters of a-z, 0-9, . and -)", field("Bucket"), s.Bucket)
	}
	if s.Prefix != "" && (strings.HasPrefix(s.Prefix, "/") || !strings.HasSuffix(s.Prefix, "/")) {
		return s, fmt.Errorf("auditpulumi: %s %q is a path ending in a slash and not starting with one (%s/)", field("Prefix"), s.Prefix, name)
	}
	if s.Adopt && s.Create {
		return s, fmt.Errorf("auditpulumi: %s and %s are both set: Create makes a bucket and writes its lifecycle, Adopt takes an existing bucket "+
			"and leaves its lifecycle alone. Choose one", field("Create"), field("Adopt"))
	}
	if s.AcknowledgeLifecycle && !s.Adopt {
		return s, fmt.Errorf("auditpulumi: %s is set without %s: it is the estate's "+
			"statement about the lifecycle of a bucket it adopts", field("AcknowledgeLifecycle"), field("Adopt"))
	}
	if s.KeyAlias != "" {
		if err := checkAlias(field("KeyAlias"), s.KeyAlias); err != nil {
			return s, err
		}
	}
	if s.Endpoint == "" {
		switch {
		case s.PathStyle:
			return s, fmt.Errorf("auditpulumi: %s is for a store at %s; on AWS S3 the bucket is addressed by the SDK", field("PathStyle"), field("Endpoint"))
		case s.CredentialsAddress != "":
			return s, fmt.Errorf("auditpulumi: %s is for a store at %s; on AWS S3 the roles are the credential", field("CredentialsAddress"), field("Endpoint"))
		case s.CredentialsPreset != nil:
			return s, fmt.Errorf("auditpulumi: %s is for a store at %s; on AWS S3 the roles are the credential", field("CredentialsPreset"), field("Endpoint"))
		}
		return s, nil
	}
	if err := checkEndpoint(field("Endpoint"), s.Endpoint); err != nil {
		return s, err
	}
	switch {
	case name == profile.Attested:
		return s, fmt.Errorf("auditpulumi: %s is set on the attested preset: it keeps its objects under compliance Object Lock, "+
			"which is an AWS S3 guarantee the store at %s does not make. Keep the attested preset on AWS S3", field("Endpoint"), s.Endpoint)
	case s.Adopt:
		return s, fmt.Errorf("auditpulumi: %s is set with %s: the library adopts buckets on AWS S3 only, and the bucket at %s is the store's, "+
			"made by the estate", field("Adopt"), field("Endpoint"), s.Endpoint)
	case s.Create:
		return s, fmt.Errorf("auditpulumi: %s is set with %s: the library creates buckets on AWS S3 only, and the bucket at %s is the store's, "+
			"made by the estate", field("Create"), field("Endpoint"), s.Endpoint)
	case s.KeyAlias != "":
		return s, fmt.Errorf("auditpulumi: %s is set with %s: a store at an endpoint is not encrypted under a KMS key of the account. "+
			"Leave KeyAlias out, or keep the preset on AWS S3", field("KeyAlias"), field("Endpoint"))
	}
	if s.Region == "" {
		s.Region = "auto"
	}
	if c := s.CredentialsPreset; c != nil {
		if s.CredentialsAddress != "" {
			return s, fmt.Errorf("auditpulumi: %s and %s are both set: static credentials or minted ones, not both",
				field("CredentialsAddress"), field("CredentialsPreset"))
		}
		if !addressRE.MatchString(c.Minter) {
			return s, fmt.Errorf("auditpulumi: %s %q must be below internal/ (internal/cloudflare/main/minter): the grant is on that address only",
				field("CredentialsPreset.Minter"), c.Minter)
		}
		if c.Account == "" || c.Prototype == "" {
			return s, fmt.Errorf("auditpulumi: %s needs Account, Minter and Prototype", field("CredentialsPreset"))
		}
		if _, err := c.LifetimeDuration(); err != nil {
			return s, fmt.Errorf("auditpulumi: %s.%w", field("CredentialsPreset"), err)
		}
		return s, nil
	}
	if s.CredentialsAddress == "" {
		s.CredentialsAddress = defaultCredentialsAddress + "/" + string(name)
	}
	if !addressRE.MatchString(s.CredentialsAddress) {
		return s, fmt.Errorf("auditpulumi: %s %q must be below internal/ (%s): the library's own parameters are there, "+
			"and the grant is on that address only", field("CredentialsAddress"), s.CredentialsAddress, defaultCredentialsAddress+"/"+string(name))
	}
	return s, nil
}

// checkPresetBuckets holds the presets' buckets apart: a bucket the library
// creates is one preset's, the attested preset's lock is a property of its
// bucket, and two presets sharing a bucket keep to prefixes of their own.
func checkPresetBuckets(stores []presetStore) error {
	for i, s := range stores {
		for _, t := range stores[:i] {
			if s.Bucket != t.Bucket || s.Endpoint != t.Endpoint {
				continue
			}
			switch {
			case s.managed() || t.managed():
				return fmt.Errorf("auditpulumi: Presets[%q] and Presets[%q] both name the bucket %s and one has Create or Adopt: "+
					"a bucket the library manages is one preset's", t.Preset, s.Preset, s.Bucket)
			case s.Preset == profile.Attested || t.Preset == profile.Attested:
				return fmt.Errorf("auditpulumi: Presets[%q] and Presets[%q] both name the bucket %s: Object Lock is a property of the "+
					"bucket, and the attested preset's is its own", t.Preset, s.Preset, s.Bucket)
			case s.Prefix == "" || t.Prefix == "" || strings.HasPrefix(s.Prefix, t.Prefix) || strings.HasPrefix(t.Prefix, s.Prefix):
				return fmt.Errorf("auditpulumi: Presets[%q] and Presets[%q] share the bucket %s and their prefixes (%q, %q) overlap: "+
					"give each its own, such as %s/", t.Preset, s.Preset, s.Bucket, t.Prefix, s.Prefix, s.Preset)
			}
		}
	}
	return nil
}

// home is the strongest configured preset: where what belongs to no profile
// (the catalogues, the schemas) is kept (store/routed).
func (a *Args) home() presetStore { return a.stores[len(a.stores)-1] }

// awsStores are the presets on AWS S3, which the roles are granted.
func (a *Args) awsStores() []presetStore {
	var out []presetStore
	for _, s := range a.stores {
		if !s.external() {
			out = append(out, s)
		}
	}
	return out
}

// endpointStores are the presets at an endpoint, whose credentials the
// functions read from the state store.
func (a *Args) endpointStores() []presetStore {
	var out []presetStore
	for _, s := range a.stores {
		if s.external() {
			out = append(out, s)
		}
	}
	return out
}

// created are the presets whose bucket the library creates.
func (a *Args) created() []presetStore {
	var out []presetStore
	for _, s := range a.stores {
		if s.Create {
			out = append(out, s)
		}
	}
	return out
}

// adopted are the presets whose existing bucket the library adopts.
func (a *Args) adopted() []presetStore {
	var out []presetStore
	for _, s := range a.stores {
		if s.Adopt {
			out = append(out, s)
		}
	}
	return out
}

// checkAdopted holds an adopted bucket to the retention its profiles demand: the
// library declares no lifecycle for it and cannot read the one it has, so the
// estate states that it keeps objects long enough.
func (a *Args) checkAdopted() error {
	for _, s := range a.adopted() {
		for _, lp := range s.Profiles {
			if lp.MinimumDays > 0 && !s.AcknowledgeLifecycle {
				return fmt.Errorf("auditpulumi: Presets[%q].Adopt leaves the bucket's lifecycle as it is, and the profile %q has a fixed minimum "+
					"retention of %d days: the library cannot read the existing lifecycle to verify that it (and Object Lock) keeps objects that long. "+
					"Set Presets[%q].AcknowledgeLifecycle once the estate has checked it, or use Create", s.Preset, lp.Name, lp.MinimumDays, s.Preset)
			}
		}
	}
	return nil
}

// applyFeatures sets what the configured presets decide and refuses what they
// leave out and the arguments ask for anyway.
func applyFeatures(c *Args) error {
	f := c.features
	have := make([]string, len(c.stores))
	for i, s := range c.stores {
		have[i] = string(s.Preset)
	}
	configured := strings.Join(have, ", ")

	if !f.Notary {
		if !c.Notary.Disabled && (c.Notary.Package != "" || c.Notary.PackageSHA256 != "") {
			return fmt.Errorf("auditpulumi: Notary.Package is set and the configured presets (%s) provision no notary and no seal key: "+
				"no profile needs more. Configure Presets[%q] (and give a profile that needs it, such as security) to run the notary, "+
				"or leave Notary out", configured, profile.Standard)
		}
		c.Notary.Disabled = true
	}
	if !f.Alarms && c.Alerts.EndpointURL != nil {
		return fmt.Errorf("auditpulumi: Alerts.EndpointURL is set and the configured presets (%s) provision no alarms. "+
			"Configure Presets[%q] to have them", configured, profile.Standard)
	}
	return nil
}
