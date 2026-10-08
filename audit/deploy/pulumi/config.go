package auditpulumi

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	yaml "go.yaml.in/yaml/v3"
)

// Where the configuration layer puts what the functions read. A layer is
// extracted under /opt, and its zip holds the files under audit/, so they are at
// /opt/audit/: the binaries' own default for the configuration file.
const (
	configRoot = "/opt/audit"
	// layerRoot is the directory of the layer's zip that becomes configRoot.
	layerRoot = "audit"
	// The name is joined so that a scan for emitted action names, which reads a
	// string of this shape as one, does not take it for an action.
	configFile        = "audit" + ".yaml"
	deploymentFile    = "deployment.yaml"
	cataloguesDir     = "catalogues"
	writerService     = "audit-writer"
	notaryService     = "audit-notary"
	extensionLoopback = "http://127.0.0.1:4318"
	// apiVersionPrefix is the group of the documents' apiVersion:
	// `audit.truvity.github.io/<kind>/v2`.
	apiVersionPrefix = "audit.truvity.github.io/"
)

// archiveConfig is the `archive` block of both functions: what the process adds
// to the deployment's presets (the bucket, endpoint, lock and per-preset key are
// in the deployment document). The state root, when any preset is at an endpoint,
// is where those stores' credentials are read from; the key objects are encrypted
// with where a preset names none, by alias (or by ARN) so that the file is known
// before the key is looked up. It is nil when there is nothing to say.
func archiveConfig(a *Args) map[string]any {
	out := map[string]any{}
	if len(a.endpointStores()) > 0 {
		out["stateRoot"] = a.State.Root
	}
	// With SSE-S3 and the AWS-managed key there is no key to name: the bucket's
	// default encryption applies.
	if a.Archive.Encryption == EncryptionKMS {
		if a.Archive.KeyArn != "" {
			out["kmsKey"] = a.Archive.KeyArn
		} else if a.Keys.Archive != "" {
			out["kmsKey"] = a.Keys.Archive
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dedupeTable(name string) string { return name + "-dedupe" }

// writerConfig is audit-writer-lambda's configuration file
// (schemas/config/audit-writer-lambda.schema.json). It holds no secret and
// nothing that is known only after something is created, so it is rendered
// before the first resource exists and ships in the configuration layer. A
// secret it names (`...Secret`) is read from SSM under `secrets.root`.
func writerConfig(name string, a *Args) ([]byte, error) {
	dyn := map[string]any{"table": dedupeTable(name)}
	if a.Writer.DedupeWindow != "" {
		dyn["window"] = a.Writer.DedupeWindow
	}
	doc := map[string]any{
		"apiVersion": apiVersionPrefix + "audit-writer-lambda/v2",
		"deployment": configRoot + "/" + deploymentFile,
		"dedupe":     map[string]any{"dynamodb": dyn},
		"require":    "archived",
	}
	if ac := archiveConfig(a); ac != nil {
		doc["archive"] = ac
	}
	if len(a.Writer.Catalogues) > 0 {
		doc["catalogues"] = configRoot + "/" + cataloguesDir
	}
	if a.Writer.Keys != nil {
		doc["keys"] = a.Writer.Keys
	}
	// The pseudonym and conceal keys, by purpose, through the storage port: the
	// aliases, the instance the encryption context carries, and where the
	// per-tenant secrets behind a pseudonym are kept.
	if k := a.Keys; k.Pseudonym != "" || k.Conceal != "" {
		keys := map[string]any{"adapter": "kms", "instance": a.instance(name)}
		if k.Pseudonym != "" {
			keys["pseudonym"] = k.Pseudonym
			keys["state"] = map[string]any{"root": a.State.Root, "address": pseudonymStateAddress}
		}
		if k.Conceal != "" {
			keys["conceal"] = k.Conceal
		}
		doc["keys"] = keys
	}
	// The secrets Keys names are read from SSM with the function's role. The
	// function's environment holds none: what is here is a path.
	if s := a.Writer.Secrets; s != nil {
		doc["secrets"] = map[string]any{"source": "ssm", "root": s.Root}
	}
	if a.Writer.ForgetIdentities {
		doc["forgetIdentities"] = true
	}
	return render(doc)
}

// notaryConfig is audit-notary's configuration file, as the Lambda reads it: the
// same schema as the Job's, with `keys.seal` naming the seal key by alias.
func notaryConfig(name string, a *Args) ([]byte, error) {
	doc := map[string]any{
		"apiVersion": apiVersionPrefix + "audit-notary/v2",
		"deployment": configRoot + "/" + deploymentFile,
		"keys":       map[string]any{"adapter": "kms", "instance": a.instance(name), "seal": a.Keys.Seal},
		"settle":     a.Notary.Settle,
	}
	if ac := archiveConfig(a); ac != nil {
		doc["archive"] = ac
	}
	if len(a.Notary.Profiles) > 0 {
		doc["profiles"] = a.Notary.Profiles
	}
	return render(doc)
}

func render(doc map[string]any) ([]byte, error) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: rendering a function's configuration: %w", err)
	}
	return b, nil
}

// functionEnv is the environment of a function: the file its process reads, the
// layer version that carried it (which the writer repeats in its start-up record,
// because the platform does not tell a function which layers it has), and the
// telemetry's.
func functionEnv(t *TelemetryArgs, service string, layerArn pulumi.StringInput) pulumi.StringMap {
	env := pulumi.StringMap{
		"AUDIT_CONFIG":       pulumi.String(configRoot + "/" + configFile),
		"AUDIT_CONFIG_LAYER": layerArn,
	}
	for k, v := range telemetryEnv(t, service) {
		env[k] = pulumi.String(v)
	}
	return env
}

// telemetryEnv is the environment of a function with the OTLP extension: the
// extension's own settings and the SDK's, which points at the extension's
// loopback proxy. No secret: the extension trades the function role's identity
// for a token.
//
// The settings are named for what they are, AUDIT_OTLP_*. The names the extension
// has read so far, ACCESS_ROSTER_*, are deprecated aliases: they are set beside
// the new ones for one minor, so that an extension build that reads only them
// keeps working, and Telemetry.OmitLegacyEnv drops them once the extension reads
// the new names.
func telemetryEnv(t *TelemetryArgs, service string) map[string]string {
	if t == nil {
		return nil
	}
	env := map[string]string{
		"AUDIT_OTLP_ISSUER":       t.IssuerURL,
		"AUDIT_OTLP_STS_AUDIENCE": t.STSAudience,
		"AUDIT_OTLP_ENDPOINT":     t.OTLPEndpoint,
		// The exchange's audience and client id.
		"AUDIT_OTLP_AUDIENCE": t.OTLPAudience,
		// The SDK exports to the extension, which holds the credential.
		"OTEL_EXPORTER_OTLP_ENDPOINT": extensionLoopback,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":           service,
	}
	if !t.OmitLegacyEnv {
		for legacy, current := range legacyTelemetryEnv {
			env[legacy] = env[current]
		}
	}
	for k, v := range t.ExtraEnv {
		env[k] = v
	}
	return env
}

// legacyTelemetryEnv is each deprecated ACCESS_ROSTER_* name and the AUDIT_OTLP_*
// name that replaces it.
var legacyTelemetryEnv = map[string]string{
	"ACCESS_ROSTER_ISSUER":        "AUDIT_OTLP_ISSUER",
	"ACCESS_ROSTER_AUDIENCE":      "AUDIT_OTLP_STS_AUDIENCE",
	"ACCESS_ROSTER_OTLP_ENDPOINT": "AUDIT_OTLP_ENDPOINT",
	"ACCESS_ROSTER_OTLP_AUDIENCE": "AUDIT_OTLP_AUDIENCE",
}
