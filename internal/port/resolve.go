package port

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Platform is the answers to the questions that pick a preset.
type Platform struct {
	AWS        bool
	Kubernetes bool
	OpenBao    bool
	// Runtime is where sluis itself runs. Empty is derived: kubernetes if
	// there is a cluster, else lambda on AWS, else process.
	Runtime Runtime
	// Replicas is how many replicas share this state; 0 is one.
	Replicas int
}

// Preset is a named set of adapters, one per concern.
type Preset string

// The presets.
const (
	PresetServer        Preset = "server"
	PresetK8sMinimal    Preset = "k8s-minimal"
	PresetK8sOpenBao    Preset = "k8s-openbao"
	PresetAWSServerless Preset = "aws-serverless"
	PresetAWSHybrid     Preset = "aws-hybrid"
	// PresetK8sAWS is sluis as a pod on Kubernetes with AWS storage: DynamoDB,
	// S3, KMS. The secrets are SSM unless `adapters.secrets` names another.
	PresetK8sAWS Preset = "k8s-aws"
	// PresetAWSEKS is the former name of [PresetK8sAWS]: deprecated, resolved
	// as it. It never started (it named the trigger `watch`, which is not built),
	// so no running installation changes by the alias.
	PresetAWSEKS Preset = "aws-eks"
)

// Presets lists them: the current names, not the deprecated alias.
var Presets = []Preset{PresetServer, PresetK8sMinimal, PresetK8sOpenBao, PresetAWSServerless, PresetAWSHybrid, PresetK8sAWS}

// Valid reports whether p is one of [Presets], or a deprecated name of one.
func (p Preset) Valid() bool { return slices.Contains(Presets, p) || p == PresetAWSEKS }

// Canonical is the preset a deprecated name stands for, and p itself otherwise.
func (p Preset) Canonical() Preset {
	if p == PresetAWSEKS {
		return PresetK8sAWS
	}
	return p
}

// Deprecated is the warning for a preset that has been renamed, empty for one
// that has not.
func (p Preset) Deprecated() string {
	if p == PresetAWSEKS {
		return fmt.Sprintf("preset %q is deprecated: it is now %q (the adapters are the same as written there)", p, PresetK8sAWS)
	}
	return ""
}

// RuntimeOf is the runtime the answers imply when none is stated.
func (p Platform) RuntimeOf() Runtime {
	switch {
	case p.Runtime != "":
		return p.Runtime
	case p.Kubernetes:
		return RuntimeKubernetes
	case p.AWS:
		return RuntimeLambda
	}
	return RuntimeProcess
}

// PresetFor is the decision tree:
//
//	AWS? no  -> Kubernetes? no -> server
//	                        yes -> OpenBao? yes -> k8s-openbao, no -> k8s-minimal
//	AWS? yes -> Kubernetes? no -> aws-serverless
//	                        yes -> sluis on Lambda? yes -> aws-hybrid, no -> k8s-aws
func PresetFor(p Platform) Preset {
	switch {
	case !p.AWS && !p.Kubernetes:
		return PresetServer
	case !p.AWS && p.OpenBao:
		return PresetK8sOpenBao
	case !p.AWS:
		return PresetK8sMinimal
	case !p.Kubernetes:
		return PresetAWSServerless
	case p.RuntimeOf() == RuntimeLambda:
		return PresetAWSHybrid
	}
	return PresetK8sAWS
}

// Answers are the platform answers a preset stands for, for a configuration
// that names a preset and no `platform` block.
func (p Preset) Answers() Platform {
	switch p.Canonical() {
	case PresetK8sMinimal:
		return Platform{Kubernetes: true, Runtime: RuntimeKubernetes}
	case PresetK8sOpenBao:
		return Platform{Kubernetes: true, OpenBao: true, Runtime: RuntimeKubernetes}
	case PresetAWSServerless:
		return Platform{AWS: true, Runtime: RuntimeLambda}
	case PresetAWSHybrid:
		return Platform{AWS: true, Kubernetes: true, Runtime: RuntimeLambda}
	case PresetK8sAWS:
		return Platform{AWS: true, Kubernetes: true, Runtime: RuntimeKubernetes}
	}
	return Platform{Runtime: RuntimeProcess}
}

// presetTable is the adapter each preset names per concern. Several name a
// planned adapter: that preset is not usable until it is built, and start
// says so.
var presetTable = map[Preset]map[Concern]string{
	PresetServer: {
		ConcernState: "postgres", ConcernSecrets: "store", ConcernBlobs: "postgres", ConcernSigning: "generated",
		ConcernTrigger: "http", ConcernSchedule: "ticker", ConcernAudit: "log",
	},
	PresetK8sMinimal: {
		ConcernState: "kubernetes", ConcernSecrets: "kubernetes", ConcernBlobs: "off", ConcernSigning: "file",
		ConcernTrigger: "watch", ConcernSchedule: "ticker", ConcernAudit: "log",
	},
	PresetK8sOpenBao: {
		ConcernState: "kubernetes", ConcernSecrets: "openbao", ConcernBlobs: "off", ConcernSigning: "transit",
		ConcernTrigger: "watch", ConcernSchedule: "ticker", ConcernAudit: "log",
	},
	PresetAWSServerless: awsLambda,
	PresetAWSHybrid:     awsLambda,
	// k8s-aws: every adapter is built. Remote signing (`kms`) and an SQS audit
	// stay selectable through `adapters`; the secrets are overridable to openbao.
	PresetK8sAWS: {
		ConcernState: "dynamodb", ConcernSecrets: "ssm", ConcernBlobs: "s3", ConcernSigning: "kms-wrapped",
		ConcernTrigger: "dynamodb", ConcernSchedule: "ticker", ConcernAudit: "connect",
	},
}

// awsLambda is the table of sluis on Lambda: DynamoDB state, SSM secrets, S3
// blobs, KMS-wrapped token signing (`kms` for remote signing), SQS audit, EventBridge ticks and an asynchronous
// invoke for "run a pass now". It is the target of both aws presets: they
// differ in what else the platform has, not in the adapters.
var awsLambda = map[Concern]string{
	ConcernState: "dynamodb", ConcernSecrets: "ssm", ConcernBlobs: "s3", ConcernSigning: "kms-wrapped",
	ConcernTrigger: "invoke", ConcernSchedule: "eventbridge", ConcernAudit: "sqs",
}

// presetUnavailable marks the presets that name an adapter which is not built:
// the reason is what start says. A preset leaves this map when every adapter it
// names is registered, and TestPresetsNameOnlyImplementedAdapters fails on a
// preset that is in neither state, in either direction. The docs say so
// (docs/reference/adapters.md).
var presetUnavailable = map[Preset]string{
	PresetServer:     "its state, secrets, blobs, signing and trigger adapters (postgres, store, generated, http) are planned and not built",
	PresetK8sMinimal: "its state, secrets, blobs and trigger adapters (kubernetes, off, watch) are planned and not built",
	PresetK8sOpenBao: "its state, blobs, signing and trigger adapters (kubernetes, off, transit, watch) are planned and not built",
}

// Unavailable is why a preset cannot be used yet, empty for one that can. A
// deprecated name answers as the preset it stands for.
func (p Preset) Unavailable() string { return presetUnavailable[p.Canonical()] }

// BuiltPresets lists the presets that can be used now.
func BuiltPresets() []Preset {
	var out []Preset
	for _, p := range Presets {
		if p.Unavailable() == "" {
			out = append(out, p)
		}
	}
	return out
}

// PresetTable returns the adapter names a preset gives, by concern.
func PresetTable(p Preset) map[Concern]string {
	out := map[Concern]string{}
	for c, n := range presetTable[p.Canonical()] {
		out[c] = n
	}
	return out
}

// Source says why a concern has the adapter it has.
type Source string

// The sources, strongest first.
const (
	// SourceOverride is an adapter named for the concern, in `adapters` or by
	// a legacy key (`ports.adapter`, `ports.blob`) beside a preset.
	SourceOverride Source = "override"
	// SourcePreset is the `preset` key.
	SourcePreset Source = "preset"
	// SourceDerived is the preset the `platform` answers lead to.
	SourceDerived Source = "derived"
	// SourceLegacy is what the `ports` keys, or their absence, mean: what
	// every deployment ran before adapters were chosen by name.
	SourceLegacy Source = "legacy"
)

// Choice is the adapter picked for one concern.
type Choice struct {
	Adapter  string
	Settings Settings
	Source   Source
}

// Table is the resolved adapter per concern.
type Table map[Concern]Choice

// Name is the adapter's name for a concern, empty if none.
func (t Table) Name(c Concern) string { return t[c].Adapter }

// String prints the table as `concern=adapter` pairs in [Concerns] order.
func (t Table) String() string {
	var parts []string
	for _, c := range Concerns {
		if ch, ok := t[c]; ok {
			parts = append(parts, string(c)+"="+ch.Adapter)
		}
	}
	return strings.Join(parts, " ")
}

// Override names an adapter for one concern.
type Override struct {
	Adapter  string
	Settings Settings
}

// Selection is everything configuration says about adapters.
type Selection struct {
	// Platform is the `platform` block; nil when absent.
	Platform *Platform
	// Preset is the `preset` key; empty when absent.
	Preset Preset
	// Overrides are the per-concern adapters.
	Overrides map[Concern]Override
	// Legacy is the table the `ports` keys mean, complete: the default when
	// neither Platform nor Preset is given.
	Legacy Table
	// LegacySet are the concerns a legacy key names explicitly. Beside a
	// preset they override it, so `ports.adapter: dynamodb` still wins; with
	// no preset they are redundant with Legacy.
	LegacySet []Concern
}

// Resolve builds the table. The order is: an explicit override, then the
// preset, then the preset the platform answers derive, then (when no preset
// and no platform is given) the legacy table.
func Resolve(sel Selection) (Table, error) {
	t := Table{}
	var preset Preset
	var src Source
	switch {
	case sel.Preset != "":
		if !sel.Preset.Valid() {
			return nil, fmt.Errorf("preset: %q is none of %v", sel.Preset, Presets)
		}
		preset, src = sel.Preset.Canonical(), SourcePreset
	case sel.Platform != nil:
		preset, src = PresetFor(*sel.Platform), SourceDerived
	}
	if preset == "" {
		for c, ch := range sel.Legacy {
			ch.Source = SourceLegacy
			t[c] = ch
		}
	} else {
		for c, n := range presetTable[preset] {
			t[c] = Choice{Adapter: n, Source: src}
		}
		for _, c := range sel.LegacySet {
			if ch, ok := sel.Legacy[c]; ok {
				ch.Source = SourceOverride
				t[c] = ch
			}
		}
	}
	for c, o := range sel.Overrides {
		if !c.Valid() {
			return nil, fmt.Errorf("adapters.%s: not a concern (%v)", c, Concerns)
		}
		if o.Adapter == "" {
			return nil, fmt.Errorf("adapters.%s.adapter: required", c)
		}
		t[c] = Choice{Adapter: o.Adapter, Settings: o.Settings, Source: SourceOverride}
	}
	for _, c := range Concerns {
		if _, ok := t[c]; !ok {
			return nil, fmt.Errorf("no adapter for %s: the selection names none", c)
		}
	}
	// A preset NAMED in the configuration that names adapters which are not
	// built is refused here, in words that name the preset, unless the
	// configuration replaced every such adapter itself. (A preset only derived
	// from `platform` is left to Validate, which names each planned adapter and
	// reports every other problem of the table with it.)
	if why := preset.Unavailable(); why != "" && src == SourcePreset {
		var left []string
		for _, c := range Concerns {
			n := presetTable[preset][c]
			if d, ok := Default.Lookup(c, n); (!ok || d.Status != StatusImplemented) && t[c].Adapter == n {
				left = append(left, string(c)+"="+n)
			}
		}
		if len(left) > 0 {
			return nil, fmt.Errorf("preset %q is unavailable: %s (planned, not built: %s). "+
				"Name built adapters under `adapters` to replace them, or use a preset that is built (%v)",
				preset, why, strings.Join(left, ", "), BuiltPresets())
		}
	}
	return t, nil
}

// Env is what a table is validated against.
type Env struct {
	// Answers are the platform answers; nil when the configuration gave
	// neither a platform nor a preset, and the answers-based checks are off.
	Answers *Platform
	// Runtime is where this process runs; empty when unknown.
	Runtime Runtime
	// Replicas is how many replicas share the state; 0 is one.
	Replicas int
	// PlatformStated is true when the answers are a `platform` block the
	// configuration wrote, not a preset's: then an `openbao: false` in it is a
	// statement, and an adapter that needs OpenBao is refused against it.
	PlatformStated bool
}

// Validate refuses a table that cannot run: an adapter that is unknown or only
// planned, one that needs a platform answer that is no, one that cannot run on
// this runtime, a process-local one with more than one replica, and a secrets
// adapter that is not a secret store when the platform has one. It reports
// every problem, not the first.
func (r *Registry) Validate(t Table, env Env) error {
	var errs []error
	fail := func(c Concern, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s adapter %q: %s", c, t[c].Adapter, fmt.Sprintf(format, args...)))
	}
	for _, c := range Concerns {
		ch, ok := t[c]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: no adapter", c))
			continue
		}
		d, ok := r.Lookup(c, ch.Adapter)
		if !ok {
			fail(c, "unknown (known: %s)", strings.Join(r.Names(c), ", "))
			continue
		}
		if d.Status != StatusImplemented {
			fail(c, "is planned (on request) and not built")
			continue
		}
		if a := env.Answers; a != nil {
			if d.Requires.AWS && !a.AWS {
				fail(c, "needs AWS, and platform.aws is false")
			}
			if d.Requires.Kubernetes && !a.Kubernetes {
				fail(c, "needs Kubernetes, and platform.kubernetes is false")
			}
			// An adapter named on purpose is the statement that there is an
			// OpenBao to use: a preset's answers (k8s-aws) do not ask for one.
			if d.Requires.OpenBao && !a.OpenBao && (ch.Source != SourceOverride || env.PlatformStated) {
				fail(c, "needs OpenBao, and platform.openbao is false")
			}
		}
		if env.Runtime != "" && !d.Works(env.Runtime) {
			fail(c, "cannot run on the %s runtime (works on %v)", env.Runtime, d.Runtimes)
		}
		if d.ProcessLocal && env.Replicas > 1 {
			fail(c, "keeps its data in this process, so %d replicas would each see their own copy", env.Replicas)
		}
		if c == ConcernSecrets && !d.SecretStore && d.Name != "memory" && env.Answers != nil &&
			(env.Answers.AWS || env.Answers.Kubernetes || env.Answers.OpenBao) {
			fail(c, "is not a secret store, and this platform has one (aws, kubernetes or openbao): use ssm, kubernetes or openbao")
		}
	}
	return errors.Join(errs...)
}
