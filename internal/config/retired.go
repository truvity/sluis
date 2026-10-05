package config

import (
	"fmt"
	"slices"
	"strings"
)

// The environment variables the binaries used to be configured with. A retired
// variable that is still set is refused at start, not ignored: a deployment
// that still sets one believes it is configuring something, and an unknown one
// ignored is the silence docs/decisions/0007 and 0032 exist to end.
//
// Each names what replaces it. docs/reference/configuration.md carries the same
// table, and a test holds the two to one another.

// retiredCommon are retired for every binary that read them, with the same
// replacement.
var retiredCommon = map[string]string{
	"POLICY_DIR":       "policyDir",
	"RELEASE_NAME":     "release",
	"LOG_LEVEL":        "log.level",
	"AUDIT_WRITER_URL": "audit.writer",
	"AUDIT_TOKEN_FILE": "audit.tokenFile",
}

var retiredIssuer = map[string]string{
	"ISSUER_URL":                       "issuerURL",
	"PORT":                             "listen.address",
	"HEALTH_PORT":                      "probes.address",
	"API_PORT":                         "nothing: the directory's own listeners are not served by sluis serve",
	"CONSOLE_PORT":                     "nothing: the console is served on the issuer's listener",
	"DEMO":                             "demo",
	"ALLOW_INSECURE":                   "allowInsecure",
	"IN_CLUSTER":                       "inCluster",
	"PUBLIC_URL":                       "publicURL",
	"PUBLIC_ROOT_URL":                  "publicRootURL",
	"SECURE_COOKIES":                   "secureCookies",
	"STORE":                            "store",
	"OVERLAY_FILE":                     "overlayFile",
	"GROUPS_SCOPING":                   "groupsScoping",
	"CLUSTER":                          "cluster",
	"CLIENT_SECRETS_DIR":               "clientSecretsDir",
	"ADMIN_PASSWORD":                   "adminPasswordEnv, which names the variable that holds it",
	"RECOVERY_ENABLED":                 "recovery.enabled",
	"RECOVERY_SERVICE_ACCOUNT":         "recovery.serviceAccount",
	"RECOVERY_AUDIENCE":                "recovery.audience",
	"API_AUDIENCE":                     "api.audience",
	"CONSUMERS_FILE":                   "api.consumersFile",
	"LOGIN_DIRECTORY":                  "login.directory",
	"SIGN_OUT_URL":                     "login.signOutURL",
	"FORWARDED_EMAIL_HEADER":           "login.forwarded.emailHeader",
	"FORWARDED_ISSUER":                 "login.forwarded.issuer",
	"FORWARDED_AUDIENCE":               "login.forwarded.audience",
	"CONSOLE_ORIGIN":                   "console.origin",
	"CONSOLE_CLIENT_ID":                "console.client",
	"OAUTH_CLIENT_ID":                  "oauthClient.id",
	"OAUTH_CLIENT_ID_FILE":             "oauthClient.idFile",
	"OAUTH_CLIENT_SECRET":              "oauthClient.secretEnv, which names the variable that holds it",
	"OAUTH_CLIENT_SECRET_FILE":         "oauthClient.secretFile",
	"OAUTH_CLIENT_SECRET_NAME":         "oauthClient.secretName",
	"OAUTH_CLIENT_ID_KEY":              "oauthClient.idKey",
	"OAUTH_CLIENT_SECRET_KEY":          "oauthClient.secretKey",
	"SIGNING_KEY_FILE":                 "signingKey.file",
	"SIGNING_KEY_FILES":                "signingKey.additionalFiles",
	"SIGNING_KEY_POLL_INTERVAL":        "signingKey.pollInterval",
	"SIGNING_KEY_ACTIVATION_DELAY":     "signingKey.activationDelay",
	"SIGNING_KEY_OVERLAP":              "signingKey.overlap",
	"TOKEN_LIFETIME":                   "lifetimes.token",
	"REFRESH_LIFETIME":                 "lifetimes.refresh",
	"ABSOLUTE_LIFETIME":                "lifetimes.absolute",
	"HOLD_WINDOW":                      "lifetimes.hold",
	"SESSION_LIFETIME":                 "lifetimes.session",
	"REFRESH_INTERVAL":                 "freshness.refreshInterval",
	"FRESHNESS_WINDOW":                 "freshness.freshnessWindow",
	"PROBE_INTERVAL":                   "freshness.probeInterval",
	"EXCHANGE_AUDIENCE":                "exchange.audience",
	"CLUSTERS_FILE":                    "exchange.clustersFile",
	"AWS_FEDERATION_FILE":              "exchange.awsFile",
	"VALKEY_ADDRESS":                   "valkey.address",
	"VALKEY_PASSWORD":                  "valkey.passwordSecret, the secret's name (valkey/password), delivered by `secrets`",
	"VALKEY_TLS":                       "valkey.tls",
	"VALKEY_CLUSTER":                   "valkey.cluster",
	"GITHUB_OWNERS":                    "github.owners",
	"GITHUB_RUNNER_TIERS":              "github.runnerTiers",
	"GITHUB_APPS_CATALOGUE_FILE":       "github.catalogueFile",
	"SLACK_APPS_CATALOGUE_FILE":        "slack.catalogueFile",
	"AUDIT_QUERY_URL":                  "audit.queryURL",
	"AUDIT_AUDIENCE":                   "audit.audience",
	"AUDIT_FORWARDED_FOR_TRUSTED_HOPS": "audit.forwardedForTrustedHops",
	"SECRET_MANAGERS_FILE": "nothing: the console's secret-store view was removed in v1.30.0, " +
		"see docs/decisions/0002-mission-boundary-tokens-and-memberships.md",
}

var retiredGitHubRoster = map[string]string{
	"CONSOLE_URL":                "consoleURL",
	"TOKEN_FILE":                 "tokenFile",
	"APPS_DIR":                   "appsDir",
	"RECORDS_DIR":                "recordsDir",
	"INTERVAL":                   "interval",
	"ENABLED_ORGS":               "enabledOrgs",
	"GITHUB_APPS_CATALOGUE_FILE": "catalogueFile",
}

var retiredSlackRoster = map[string]string{
	"CONSOLE_URL":        "consoleURL",
	"TOKEN_FILE":         "tokenFile",
	"CREDENTIALS_DIR":    "credentialsDir",
	"RECORDS_DIR":        "recordsDir",
	"INTERVAL":           "interval",
	"ENABLED_WORKSPACES": "enabledWorkspaces",
}

// retiredLambda are the variables a function was configured with before the
// configuration layer and the `secrets` source, retired for every binary: a
// function that still sets one is deployed by an older Pulumi library.
var retiredLambda = map[string]string{
	"SLUIS_ROLE": "nothing: there is ONE function, which serves the issuer and the console and runs the controllers' passes; " +
		"deploy with the v1.63 Pulumi library",
	"SLUIS_CONFIG_FILE": "SLUIS_CONFIG, which names the service document (on Lambda, /opt/sluis/sluis.yaml in the configuration layer): " +
		"deploy with the v1.62 Pulumi library",
	"SLUIS_SECRET_FILES": "the document's `secrets` source (ssm, root /sluis/<instance>) and the names its keys give " +
		"(`signingKey.kms.stateSecret`, `recovery.passwordSecret`, ...): deploy with the v1.62 Pulumi library",
}

// Retired returns, for one binary, every retired variable and what replaces it.
// The documentation's migration table is this, and a test holds them together.
func Retired(binary string) map[string]string {
	out := map[string]string{}
	add := func(from map[string]string) {
		for k, v := range from {
			out[k] = v
		}
	}
	add(retiredLambda)
	switch binary {
	case "serve", "sluis":
		add(retiredIssuer)
		// The service's own spelling of what the controllers share.
		out["POLICY_DIR"] = "policyDir"
		out["RELEASE_NAME"] = "release"
		out["LOG_LEVEL"] = "log.level"
		out["AUDIT_WRITER_URL"] = "audit.writer"
		out["AUDIT_TOKEN_FILE"] = "audit.tokenFile"
	case "controller-github":
		add(retiredCommon)
		add(retiredGitHubRoster)
		out["AUDIT_WRITER_URL"] = "audit.writer"
		out["AUDIT_TOKEN_FILE"] = "audit.tokenFile"
	case "controller-slack":
		add(retiredCommon)
		add(retiredSlackRoster)
	}
	return out
}

// RefuseRetired is the start-up check: it fails when the environment still
// holds a variable the subcommand no longer reads, naming each with what replaces
// it. environ is os.Environ's shape.
func RefuseRetired(binary string, environ []string) error {
	retired := Retired(binary)
	var found []string
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if _, ok := retired[name]; ok && value != "" {
			found = append(found, name)
		}
		// A variable whose value is `ssm:<path>` was filled from SSM at cold
		// start; the `secrets` source reads SSM itself now, by name.
		if strings.HasPrefix(value, "ssm:/") {
			retired[name] = "a secret the document names (`...Secret`), read by its `secrets` source (ssm): the `ssm:` mapping is retired; " +
				"deploy with the v1.62 Pulumi library"
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	var b strings.Builder
	fmt.Fprintf(&b, "sluis %s is configured by one file, --config <file>, and no longer reads these environment "+
		"variables, which are set: ", strings.ReplaceAll(binary, "-", " "))
	for i, name := range found {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s (now %s)", name, retired[name])
	}
	b.WriteString(". Remove them and put the setting in the file: docs/reference/configuration.md has the mapping")
	return fmt.Errorf("%s", b.String())
}

// The keys a v1 document had that v2 does not, by document, each with where it
// went. A v2 document that still names one is refused with this message rather
// than the schema's bare "not a key this service reads": the person reading it
// is migrating, and needs the new place. A v1 document (no apiVersion) is
// converted instead (docs/reference/configuration.md, "apiVersion").
var retiredKeys = map[string]map[string]string{
	"serve": {
		"policyDir": "the policy is one rendered document now: name it with policy.file " +
			"(`sluisctl policy render <dir>` writes it from the directory policyDir named)",
		"overlayFile":                           "the declared workspaces are directory.workspaces in this document",
		"api":                                   "removed: sluis serve serves no directory API listener, so its guard configured nothing",
		"github":                                "moved to the policy document: exchange.github.owners, apps.github.runnerTiers and apps.github.catalogue",
		"slack":                                 "moved to the policy document: apps.slack.catalogue",
		"exports":                               "moved to the policy document: exports",
		"exchange.clustersFile":                 "moved to the policy document: exchange.clusters, the rows themselves",
		"exchange.awsFile":                      "moved to the policy document: exchange.aws, the rows themselves",
		"valkey.passwordEnv":                    "valkey.passwordSecret, the secret's name (valkey/password), delivered by `secrets`",
		"oauthClient.idFile":                    "oauthClient.provider: the client's secrets are providers/google/<provider>/client-id and client-secret",
		"oauthClient.secretFile":                "oauthClient.provider: the client's secrets are providers/google/<provider>/client-id and client-secret",
		"oauthClient.secretEnv":                 "oauthClient.provider: the client's secrets are providers/google/<provider>/client-id and client-secret",
		"adminPasswordEnv":                      "recovery.passwordSecret, the secret's name (recovery/password), delivered by `secrets`",
		"recovery.passwordFile":                 "recovery.passwordSecret, the secret's name (recovery/password), delivered by `secrets`",
		"clientSecretsDir":                      "nothing: a confidential client's secret is clients/<client-id>/secret, delivered by `secrets`",
		"signingKey.kms.stateSecretFile":        "signingKey.kms.stateSecret, the secret's name (issuer/state-secret)",
		"signingKey.kmsWrapped.stateSecretFile": "signingKey.kmsWrapped.stateSecret, the secret's name (issuer/state-secret)",
	},
	"controller-github": {
		"policyDir":     "the policy is one rendered document now: name it with policy.file",
		"catalogueFile": "the catalogue is the policy document's apps.github.catalogue, read from policy.file",
		"enabledOrgs":   "moved to the policy document: controllers.github.enabledOrgs",
	},
	"controller-slack": {
		"policyDir":         "the policy is one rendered document now: name it with policy.file",
		"enabledWorkspaces": "moved to the policy document: controllers.slack.enabledWorkspaces",
	},
	"policy": {
		"version": "a policy document says apiVersion: sluis.truvity.github.io/policy/v2 in place of version: 1",
		"access":  "an access document is a layer, not a policy document: `sluisctl policy render` reshapes it into one",
		"overlay": "an access document is a layer, not a policy document: `sluisctl policy render` reshapes it into one",
	},
}

// RetiredKeys returns, for one document, every key v2 retired and where it went.
func RetiredKeys(document string) map[string]string {
	out := map[string]string{}
	for k, v := range retiredKeys[document] {
		out[k] = v
	}
	return out
}

// refuseRetiredKeys fails when a v2 document names a key v2 retired, naming
// each with where it went.
func refuseRetiredKeys(document string, doc map[string]any) error {
	var found []string
	for key, why := range retiredKeys[document] {
		var node any = doc
		for _, part := range strings.Split(key, ".") {
			m, ok := node.(map[string]any)
			if !ok {
				node = nil
				break
			}
			node = m[part]
		}
		if node != nil {
			found = append(found, key+": "+why)
		}
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	return fmt.Errorf("apiVersion v2 retired these keys, which are set:\n  %s", strings.Join(found, "\n  "))
}
