// Package schema builds the JSON Schema of each binary's configuration file.
//
// It is a package of its own, apart from the types and the loader, so that the
// generator which writes the committed files does not need them to exist.
//
//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"bytes"
	"encoding/json"
)

// BaseID is where the schemas are named: the identifier is a name, and nothing
// fetches it.
const BaseID = "https://truvity.github.io/sluis/schemas/v2/config/"

// The shared shapes this repository takes from truvity/policy, by the `$id`
// they carry. The loader resolves them from its embedded copies.
const policy = "https://github.com/truvity/policy/schemas/"

// Names are the documents, as the schema files are named:
// schemas/config/<name>.schema.json. The first three are the service documents,
// one per process; `policy` is the one policy document they all name.
var Names = []string{"serve", "controller-github", "controller-slack", "policy"}

// Services are the service documents: the ones a process is started with.
var Services = []string{"serve", "controller-github", "controller-slack"}

// Group is the group of the documents' kinds: `apiVersion` is
// `<Group>/<document>/v<N>` (truvity/policy docs/contracts/config.md, rule 7).
const Group = "sluis.truvity.github.io"

// apiVersion is the `apiVersion` every v2 document carries.
func apiVersion(name string) m {
	return m{"const": Group + "/" + name + "/v2", "description": "Which version of which document this is. " + Group + "/" + name + "/v2 is what this build writes; a document with no apiVersion is v1, which the binary converts as it loads it, and a binary reads v2 and v1 (docs/reference/configuration.md)."}
}

// policyRef is `policy`: the policy document a process decides by.
func policyRef(description string) m {
	return obj(description, m{
		"file": str("The policy document: the one canonical file `sluisctl policy render` writes (schemas/config/policy.schema.json). Read once, at start: a change is a new instance."),
	}, "file")
}

type m = map[string]any

func ref(id string) m { return m{"$ref": id} }

func obj(description string, props m, required ...string) m {
	o := m{
		"type":                 "object",
		"additionalProperties": false,
		"description":          description,
		"properties":           props,
	}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

// exclusive refuses an object that names both a and b.
// exclusiveOf is exclusive for any number of keys: no two of names are set together.
func exclusiveOf(o m, names ...string) m {
	var pairs []any
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			pairs = append(pairs, m{"required": []string{names[i], names[j]}})
		}
	}
	o["not"] = m{"anyOf": pairs}
	return o
}

func str(description string) m {
	return m{"type": "string", "minLength": 1, "description": description}
}

func strDefault(description, def string) m {
	s := str(description)
	s["default"] = def
	return s
}

func enum(description, def string, values ...string) m {
	o := m{"enum": values, "description": description}
	if def != "" {
		o["default"] = def
	}
	return o
}

func integer(description string, least int, def any) m {
	o := m{"type": "integer", "minimum": least, "description": description}
	if def != nil {
		o["default"] = def
	}
	return o
}

func boolean(description string) m {
	return m{"type": "boolean", "description": description}
}

func boolDefault(description string, def bool) m {
	b := boolean(description)
	b["default"] = def
	return b
}

func duration(description string, def string) m {
	o := m{"$ref": "#/$defs/duration", "description": description}
	if def != "" {
		o["default"] = def
	}
	return o
}

func list(description string, item m) m {
	return m{"type": "array", "items": item, "description": description}
}

// url is an address with no credentials in it: a password in a URL is a secret
// in the file.
func url(description string) m {
	return m{"$ref": "#/$defs/url", "description": description}
}

// sharedDefs are the shapes more than one schema uses, written once here and
// carried into each schema that names them, because a schema the loader reads
// has to stand alone.
func sharedDefs() map[string]m {
	return map[string]m{
		"duration": {
			"type":        "string",
			"pattern":     `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`,
			"description": "A Go duration: 30s, 2m, 168h.",
		},
		// The two checks that a secret might fail are `not`, because a pattern
		// that fails is reported with the value it was given, and an error is
		// logged.
		"url": {
			"type":      "string",
			"minLength": 1,
			"allOf": []any{
				m{"pattern": `^https?://\S+$`},
				m{"not": m{"pattern": `^https?://[^/?#\s]*@`}},
			},
			"description": "An http or https URL with a host and no credentials: a password in a URL is a secret in the file, and a secret is named, never carried.",
		},
		"secretName": {
			"type":        "string",
			"pattern":     `^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`,
			"description": "The NAME of a secret, delivered as `secrets.source` says: `valkey/password`, `issuer/state-secret`. A value is never in the document.",
		},
	}
}

func listen(def string) m {
	return m{"$ref": policy + "fragments/listen.json", "default": m{"address": def}}
}

func probes(def string) m {
	return m{"$ref": policy + "fragments/probes.json", "default": m{"address": def}}
}

func logLevel() m { return ref(policy + "fragments/log.json") }

// document builds one schema: its identity, its properties, and the shapes it
// shares.
func document(name, title, description string, props m, required []string, uses []string, extra m) m {
	shared := sharedDefs()
	defs := m{}
	for _, u := range uses {
		defs[u] = shared[u]
	}
	s := m{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  BaseID + name + ".schema.json",
		"title":                title,
		"description":          description,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if len(defs) > 0 {
		s["$defs"] = defs
	}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

const secretsNote = " Secrets are never in this file: a field ending in ...Secret holds the NAME of a secret, which `secrets` says how to deliver, and a field ending in ...File holds a path (a credential the platform mounts and rotates). Telemetry is the OTEL_* environment, not configuration."

func secretField(description string) m {
	return m{"$ref": "#/$defs/secretName", "description": description}
}

// secretsSchema is `secrets`: how the names the document gives are delivered.
func secretsSchema() m {
	s := obj("How the secrets this document names are delivered (truvity/policy config.md section 5). Absent is `env`.", m{
		"source":   enum("`env`: the variable SLUIS_SECRET_<NAME> (the name upper-cased, every other character an underscore), for a local run. `file`: the file <root>/<name>, read on every use, so a rotated mount takes effect without a restart. `ssm`: the SecureString <root>/private/config/<name> in AWS SSM Parameter Store, every one under the prefix read at once and again after `refresh` (layout v3, root /sluis/<instance>).", "env", "env", "file", "ssm"),
		"root":     str("`file`: the directory the secrets are mounted under. `ssm`: the installation's root, /sluis/<instance>."),
		"region":   str("`ssm`: the region. Unset follows the AWS SDK's own resolution."),
		"endpoint": url("`ssm`: overrides the SSM address, for LocalStack."),
		"refresh":  duration("`ssm`: how old the copy may be before it is read again: a rotated secret reaches every instance within it.", "5m"),
	}, "source")
	s["allOf"] = []any{
		m{"if": m{"properties": m{"source": m{"enum": []string{"file", "ssm"}}}}, "then": m{"required": []string{"root"}}},
	}
	return s
}

func serveSchema() m {
	props := m{
		"apiVersion":    apiVersion("serve"),
		"issuerURL":     m{"$ref": "#/$defs/url", "description": "The issuer: baked into every token and every relying party's trust, so there is no default. No trailing slash is kept."},
		"release":       strDefault("The name this installation's objects carry: the Kubernetes object names (`<release>-github-orgs`, ...) and the prefix of its keys in a shared store. The chart requires it to be the release's full name.", "sluis"),
		"cluster":       str("Names this cluster in a ServiceAccount's subject. A pod cannot discover it; unset keeps the older unqualified subject."),
		"allowInsecure": boolean("Accept a plain-http issuer URL, for a local run."),
		"demo":          boolean("Two tenants held in memory, which need no credential and no network."),
		"inCluster":     boolean("Prove recovery against the cluster the pod runs in. Set by a deployment that turns recovery on."),
		"listen":        listen(":8080"),
		"probes":        probes(":7070"),
		"log":           logLevel(),
		"store": enum("Where what an operator connected is kept: `memory` keeps nothing (a restart is a fresh installation), `kubernetes` keeps it in this namespace.", "memory",
			"memory", "kubernetes"),
		"ports":         portsSchema(true),
		"platform":      platformSchema(),
		"preset":        presetSchema(),
		"adapters":      adaptersSchema(),
		"policy":        policyRef("The policy document this service decides by. Unset is the built-in two groups, or the demonstration policy under `demo`."),
		"directory":     directorySchema(),
		"publicURL":     m{"$ref": "#/$defs/url", "description": "Where a browser reaches the console, including its mount. The admin-consent redirect URI and the values the setup steps show are built from it. Default http://localhost:8081."},
		"publicRootURL": m{"$ref": "#/$defs/url", "description": "The host's root, never carrying the console's mount: the bootstrap surface stays there. Unset follows `publicURL`."},
		"secureCookies": boolean("Mark session cookies Secure. Unset follows the scheme the browser will use: https in the URL."),
		"groupsScoping": enum("How far this installation has moved toward per-audience `groups` scoping: `off`, `report` or `enforce`.", "report", "off", "report", "enforce"),
		"secrets":       secretsSchema(),
		"lifetimes": obj("How long what the issuer hands out lives.", m{
			"token":    duration("An access token.", "1h"),
			"refresh":  duration("A refresh token.", "12h"),
			"absolute": duration("A session, at most. Positive, and at least `token`: a session has to end SOMETIME after sign-in, and an access token cannot outlive the session that grants it.", "24h"),
			"hold":     duration("How long a removal is held before it takes effect.", "4h"),
			"session":  duration("The console's own session cookie, capped at `absolute`.", "12h"),
		}),
		"freshness": obj("How the directory's snapshot is kept current.", m{
			"refreshInterval": duration("How often a snapshot is refreshed.", "15m"),
			"freshnessWindow": duration("How old a snapshot may be and still be answered from.", "30m"),
			"probeInterval":   duration("How often the directory is probed.", "5m"),
		}),
		"exchange": obj("How the token exchange verifies workloads. Whom it trusts (the clusters, the AWS accounts, the GitHub owners) is the policy document's `exchange`.", m{
			"audience": str("The audience a workload token must be minted for. Defaults to `release`, so two issuers in one cluster cannot accept each other's proofs."),
		}),
		"recovery": obj("The sign-in that needs no directory: in a cluster, a token for a ServiceAccount proven against the API server; anywhere else, a password. Unset is off for the issuer and, for the hub, on.", m{
			"enabled":        boolean("Turn recovery on. Needs `inCluster`, `serviceAccount` and `audience` to be usable."),
			"serviceAccount": str("The ServiceAccount whose token signs in."),
			"audience":       str("The audience its token must carry."),
			"passwordSecret": secretField("Outside a cluster: the secret the recovery password is (`recovery/password`), read at start (the hub keeps only an Argon2id digest of it). Unset generates one and prints it, except on a function, where recovery is then off. `enabled: false` leaves it untouched and refuses the sign-in, so turning it back on needs no new password."),
		}),
		"login": obj("How a person signs in to the console.", m{
			"directory":  boolDefault("Sign in with the corporate directory.", true),
			"signOutURL": str("Where sign-out sends the browser."),
			"forwarded": obj("A sign-in an authenticating proxy has already done.", m{
				"issuer":      str("The proxy's issuer."),
				"audience":    str("The audience its token carries."),
				"emailHeader": str("The header that carries the signed-in address."),
			}),
		}),
		"console": obj("Where the console is published, for the sign-in that starts there.", m{
			"origin":      str("The console's origin, when it is not the issuer's."),
			"client":      str("The client id the console signs in as."),
			"awsAudience": str("The audience an AWS role's web identity token must be minted for to be a bearer at the console (a Lambda controller). Its own, distinct from the AWS federation file's, so a token for token exchange is no proof here and the reverse. Default `<issuerURL>/console`."),
		}),
		"oauthClient": obj("The OAuth client registered once with the directory backend: it drives both admin consent and operator sign-in.", m{
			"id":         str("The client id, for a local run. Not a secret."),
			"provider":   str("Names the client's secrets: providers/google/<provider>/client-secret, and providers/google/<provider>/client-id unless `id` gives it."),
			"secretName": str("The Kubernetes Secret the client is declared in, which the console shows and cannot change."),
			"idKey":      str("The key of the id in that Secret."),
			"secretKey":  str("The key of the secret in that Secret."),
		}),
		"signingKey": exclusiveOf(obj("The issuer's signing keys: provisioned, never minted here.", m{
			"file": str("The primary key. Unset generates one for this process, which a local run may do and nothing else should. Exclusive with `kms`."),
			"kms": obj("Sign with AWS KMS keys instead of a file: the private key never leaves KMS. Exclusive with `file`.", m{
				"keys": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": m{"type": "string", "minLength": 1},
					"description": "ECC_NIST_P384 SIGN_VERIFY keys, as ids, ARNs or aliases, oldest first. The LAST signs; the earlier ones stay published until `overlap` after the next one activates. Rotation appends a key. The role needs kms:Sign and kms:GetPublicKey on each."},
				"additional": list("Every OTHER algorithm this installation signs with at once, each on its own rotation track, as `signingKey.additionalFiles` does for files. RS256 is the one that exists: for relying parties that need it (Kargo, EKS's OIDC provider).",
					obj("One algorithm's keys.", m{
						"alg": m{"enum": []string{"RS256"}, "description": "The algorithm."},
						"keys": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": m{"type": "string", "minLength": 1},
							"description": "RSA_2048, RSA_3072 or RSA_4096 SIGN_VERIFY keys, oldest first, the last signing; same rotation rules as `keys`."},
					}, "alg", "keys")),
				"region":      str("The keys' region. Unset follows the AWS SDK's own resolution."),
				"stateSecret": secretField("The secret (`issuer/state-secret`) holding at least 32 random bytes as base64 or hex (`openssl rand -base64 32`), the same in every replica (a replica whose secret differs refuses to start), from which the sign-in state is derived: a KMS key has no private bytes to derive from."),
			}, "keys", "stateSecret"),
			"kmsWrapped": obj("Sign with key pairs AWS KMS generates and wraps under ONE symmetric key (the `kms-wrapped` adapter): a new pair per algorithm every `rotateEvery`, published before it signs and kept after it is replaced. The private key is decrypted into process memory to sign. Exclusive with `file` and `kms`.", m{
				"keyId":       str("The symmetric application key (SYMMETRIC_DEFAULT, ENCRYPT_DECRYPT), as an id, an ARN or an alias. The role needs kms:GenerateDataKeyPairWithoutPlaintext and kms:Decrypt on it, with the encryption context purpose=sluis-signing."),
				"region":      str("The key's region. Unset follows the AWS SDK's own resolution."),
				"stateSecret": secretField("The secret (`issuer/state-secret`) holding at least 32 random bytes as base64 or hex, the same in every replica, from which the sign-in state is derived: a wrapped key is replaced daily and the state must outlive it."),
				"algorithms": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": m{"enum": []string{"ES384", "RS256"}},
					"description": "The algorithms signed with, the first the installation default. Unset is ES384 and RS256. EdDSA is not supported yet."},
				"rotateEvery": duration("How often a new key pair is generated for each algorithm. Longer than `prepublish`, at most 168h.", "24h"),
				"prepublish":  duration("How long a new key is published before anything signs with it: longer than a verifier caches the key set (Envoy's jwt_authn: 10m). Unset is `activationDelay`.", "15m"),
				"retain":      duration("How long a replaced key stays published: at least `lifetimes.token` plus a skew margin. Unset is `overlap`.", ""),
			}, "keyId", "stateSecret"),
			"additionalFiles": list("Every OTHER algorithm this installation signs with at once, one file per algorithm.", str("A key file.")),
			"pollInterval":    duration("How often the files are re-read.", "30s"),
			"activationDelay": duration("How long a newly published key waits before a replica signs with it. At least `pollInterval`.", "15m"),
			"overlap":         duration("How long a rotated key stays published. Unset is `lifetimes.token` plus a margin for clock skew.", ""),
		}), "file", "kms", "kmsWrapped"),
		"valkey": obj("The shared store for logins in progress and snapshots. Unset keeps both in memory, correct for one replica.", m{
			"address":        m{"type": "string", "allOf": []any{m{"pattern": `^[^\s/]+:[0-9]{1,5}$`}, m{"not": m{"pattern": "@"}}}, "description": "host:port, with no credentials."},
			"passwordSecret": secretField("The secret the password is (`valkey/password`). Unset connects with none."),
			"tls":            boolean("Speak TLS to the server."),
			"cluster":        boolDefault("Speak the cluster protocol. A plain single server needs it off.", true),
		}),
		"audit": obj("The audit installation this service records to. Unset keeps the trail in the log only.", m{
			"writer":                  url("The installation's receiver."),
			"tokenFile":               str("This workload's projected service-account token, presented on every call."),
			"queryURL":                url("The query service, for the console's Audit page. Its own setting: it needs no `writer`, so the page works with the `sqs` and `log` sinks. A path prefix (`https://audit.example/sluis`) is kept and the procedure path appended."),
			"audience":                strDefault("The client whose audience the Audit page's tokens carry.", "audit"),
			"forwardedForTrustedHops": integer("How many of the deployment's own proxies append to X-Forwarded-For; zero records the peer.", 0, 0),
		}),
	}
	return document("serve", "sluis serve",
		"The configuration of `sluis serve`: the issuer, the console and the directory hub, one process."+secretsNote,
		props, []string{"apiVersion", "issuerURL"}, []string{"duration", "url", "secretName"},
		m{
			"allOf": []any{
				m{"if": m{"required": []string{"valkey"}}, "then": m{"properties": m{"valkey": m{"required": []string{"address"}}}}},
			},
		})
}

// portsSchema is the `ports` section both kinds of file share. Only the
// service copies secrets out of itself, so only its file may name an Export.
func portsSchema(export bool) m {
	o := obj("The adapters behind the storage ports (docs/design/ports.md).", m{
		"adapter": enum("`legacy` keeps state where it has always been kept: the namespace's ConfigMaps and Secrets and, when `valkey` is set, Valkey. `memory` keeps all of it in this process, which a restart loses: for a local run and the demonstration, and not with `store: kubernetes` or `valkey`.  `dynamodb` keeps the same in one DynamoDB table (`ports.dynamodb`), with the platform's credentials, and takes its Blob from `legacy` unless `ports.blob` names its own.", "legacy",
			"legacy", "memory", "dynamodb"),
		"blob":     portsBlobSchema(),
		"dynamodb": portsDynamoDBSchema(),
	})
	if export {
		o["properties"].(m)["export"] = portsExportSchema()
	}
	o["allOf"] = []any{
		m{"if": m{"properties": m{"adapter": m{"const": "dynamodb"}}, "required": []string{"adapter"}}, "then": m{"required": []string{"dynamodb"}}},
	}
	return o
}

// portsExportSchema is `ports.export`: where the copies of `exports` go.
func portsExportSchema() m {
	s := obj("The store the copies of `exports` are written to (docs/decisions/0034). Absent, nothing is copied out of the service, and `exports` must be empty.", m{
		"adapter": enum("`openbao` writes to a KV version 2 mount of an OpenBao. `memory` keeps the copies in this process and is for a test or the demonstration.", "", "openbao", "memory"),
		"openbao": obj("The OpenBao the copies are written to. Nothing is contacted at start: an OpenBao that is down must not stop the service, since a copy is never a dependency.", m{
			"address":   url("The server, with no path: `https://openbao.example`."),
			"caFile":    str("A PEM bundle of the authorities that sign the server's certificate, in place of the system's."),
			"mount":     strDefault("The KV version 2 mount.", "kv"),
			"namespace": str("The OpenBao namespace an export that names none is written to."),
			"auth": obj("How the service logs in, inside each namespace it writes to. The `kubernetes` and `jwt` methods take the same request (`auth/<mount>/login` with a role and a JWT) and differ in the mount they default to and where the JWT comes from.", m{
				"method":    enum("`kubernetes`: the Kubernetes auth method, with this pod's ServiceAccount token. `jwt`: the JWT/OIDC method, with a token the platform projects (a ServiceAccount token of another audience, or, on AWS Lambda, the web identity token of outbound federation) from `tokenFile`.", "", "kubernetes", "jwt"),
				"mount":     str("The auth method's mount path in each namespace. Absent, the method's name."),
				"role":      str("The role the login asks for. It must be bound to this workload's identity and carry a policy that reads, creates, updates and patches only the paths `exports` names."),
				"tokenFile": str("Where the JWT is read from, afresh on every login. Absent with `kubernetes`, the pod's ServiceAccount token; required with `jwt`."),
			}, "method", "role"),
		}, "address", "auth"),
	}, "adapter")
	s["allOf"] = []any{
		m{"if": m{"properties": m{"adapter": m{"const": "openbao"}}}, "then": m{"required": []string{"openbao"}}},
		m{"if": m{"properties": m{"auth": m{"properties": m{"method": m{"const": "jwt"}}}}}, "then": m{"properties": m{"auth": m{"required": []string{"tokenFile"}}}}},
	}
	return s
}

// exportsSchema is `exports`: the copies of secrets made out of the service.
func exportsSchema() m {
	item := obj("One copy: what is copied (`source` and the field that names it) and where it goes (`path`, in `namespace`).", m{
		"name":       str("Identifies the export in the log, the metrics and its lease: lower-case letters, digits, '.', '_' and '-'. Absent, the source and what it names, e.g. `slack-app.alerts`."),
		"source":     enum("What is copied. `slack-app`: a catalogue Slack App's bot token (`app`). `github-app`: a catalogue GitHub App's id, installation id and private key (`app`). `runner-app`: a runner App's id, installation id and private key (`tier`, `org`). `bundle`: one of the disaster-recovery bundles, whole (`bundle`).", "", "slack-app", "github-app", "runner-app", "bundle"),
		"app":        str("The catalogue id, for `slack-app` and `github-app`. It must be declared in the catalogue."),
		"tier":       str("A runner tier, for `runner-app`. It must be one of `github.runnerTiers`."),
		"org":        str("The organisation, for `runner-app`."),
		"bundle":     enum("The bundle, for `bundle`: each is what the Kubernetes Secret of that name held, one JSON document per entry. Written with `replace`: the key holds exactly the bundle.", "", "workspace-credentials", "github-apps", "github-links", "github-runner-apps", "github-catalogue-apps", "slack-credentials", "slack-records"),
		"namespace":  str("The OpenBao namespace. Absent, `ports.export.openbao.namespace`."),
		"path":       str("The key under the KV mount: `slack-apps/alerts`. No leading or trailing slash."),
		"properties": m{"type": "object", "additionalProperties": m{"type": "string", "minLength": 1}, "description": "Which properties of the App are written and under what names: `{private_key: github-private-key}`. Absent, all of the source's, under the names the External Secrets PushSecrets wrote: `bot_token`; `app_id`, `installation_id`, `private_key`; `github-app-id`, `github-installation-id`, `github-private-key` for a runner App. A property export is a PATCH: other properties of the key are left as they are. Not for `bundle`."},
		"interval":   duration("How often the copy is made again with nothing changed, to put back what somebody altered. A change is copied at once; this is the backstop.", "1h"),
	}, "source", "path")
	item["allOf"] = []any{
		m{"if": m{"properties": m{"source": m{"enum": []string{"slack-app", "github-app"}}}}, "then": m{"required": []string{"app"}}},
		m{"if": m{"properties": m{"source": m{"const": "runner-app"}}}, "then": m{"required": []string{"tier", "org"}}},
		m{"if": m{"properties": m{"source": m{"const": "bundle"}}}, "then": m{"required": []string{"bundle"}}},
	}
	return m{"type": "array", "items": item, "description": "The secrets this service copies out of itself into the store `ports.export` names (docs/decisions/0034): a copy is asynchronous, retried with backoff and never a dependency. Validated at start; an unknown source, a source this deployment does not declare and two exports that would write one key stop the service before it serves."}
}

// portsDynamoDBSchema is `ports.dynamodb`: the table of the `dynamodb` adapter.
func portsDynamoDBSchema() m {
	return obj("The DynamoDB table of the `dynamodb` adapter: one table with a string partition key `pk`, a string sort key `sk` and the TTL attribute `expires`. Credentials are the platform's (EKS Pod Identity, IRSA, a Lambda role) and are never configured here.", m{
		"table":    str("The table's name."),
		"region":   str("The table's region. Absent, the SDK's own resolution (`AWS_REGION`)."),
		"endpoint": url("Overrides the DynamoDB address: LocalStack or DynamoDB Local."),
		"create":   boolDefault("Create the table (on-demand, TTL on `expires`) at start when it is not there, for a test or a development installation. Off, the table must exist: production uses the one the infrastructure code made, and the role needs `dynamodb:DescribeTable` on it.", false),
	}, "table")
}

// portsBlobSchema is `ports.blob`: the Blob port's own adapter, which
// replaces the one `ports.adapter` brings and composes with any of them.
func portsBlobSchema() m {
	s := obj("Replaces the Blob port (status reports, directory snapshots) with an adapter of its own, whatever `ports.adapter` is. Absent, the Blob is `ports.adapter`'s.", m{
		"adapter": enum("`s3` keeps the blobs in an S3 bucket.", "", "s3"),
		"s3": obj("Where the S3 adapter keeps its objects. Credentials are the platform's (EKS Pod Identity, IRSA, a Lambda role) and are never configured here.", m{
			"bucket":    str("The bucket. It must exist, with public access blocked."),
			"prefix":    str("A key prefix inside the bucket, for an installation that shares it. Names are `<prefix>/reports/<target>` and `<prefix>/snapshots/<directory>`."),
			"region":    str("The bucket's region. Absent, the SDK's own resolution (`AWS_REGION`)."),
			"kmsKey":    str("A KMS key id, ARN or alias for server-side encryption (SSE-KMS) of every write. Absent, the bucket's default encryption applies."),
			"endpoint":  url("Overrides the S3 address: LocalStack or an S3-compatible store."),
			"pathStyle": boolean("Addresses the bucket in the path and not the host name, which LocalStack and most S3-compatible stores need."),
		}, "bucket"),
	}, "adapter")
	s["allOf"] = []any{m{"if": m{"properties": m{"adapter": m{"const": "s3"}}}, "then": m{"required": []string{"s3"}}}}
	return s
}

func rosterProps(kind, mountDefault, recordsDefault string) m {
	kindName := "controller-" + kind
	return m{
		"apiVersion": apiVersion(kindName),
		"release":    strDefault("The name the installation's objects carry. It must be the release's full name: the controller reads the report and the records the service writes under it.", "sluis"),
		"policy":     policyRef("The policy document: the bindings are its " + kind + " table, and its `controllers." + kind + "` section is what this controller may change."),
		"consoleURL": url("The console's API, which answers who holds a group."),
		"tokenFile":  strDefault("This pod's projected ServiceAccount token, presented to the console and read afresh on every call.", mountDefault),
		"console": obj("How the controller proves itself to the console, when the pod's `tokenFile` is not the way.", m{
			"auth": obj("The proof. Absent, `tokenFile`.", m{
				"aws": obj("The function role's AWS outbound web identity token (`sts:GetWebIdentityToken`), re-minted every four minutes. The console's issuer must federate the account, and its policy must declare an `aws` matcher for the role.", m{
					"audience": str("The audience requested from STS. It must equal the issuer's `console.awsAudience` (default `<issuerURL>/console`), NOT the audience of its AWS federation file."),
				}, "audience"),
			}),
		}),
		"recordsDir": strDefault("The console's records, mounted.", recordsDefault),
		"interval":   duration("How long between passes. Positive.", "15m"),
		"log":        logLevel(),
		"ports":      portsSchema(false),
		"platform":   platformSchema(),
		"preset":     presetSchema(),
		"adapters":   adaptersSchema(),
		"probes":     probes(":7070"),
		"audit": obj("The audit installation the controller records to, as its own workload. Unset only logs what it did.", m{
			"writer":    url("The installation's receiver."),
			"tokenFile": str("This workload's projected service-account token."),
		}),
	}
}

func controllerGitHubSchema() m {
	props := rosterProps("github", "/var/run/secrets/github-roster/token", "/var/run/github-roster/records")
	props["appsDir"] = strDefault("One file per connected organisation: its App's credentials.", "/var/run/github-roster/apps")
	return document("controller-github", "sluis controller github",
		"The configuration of `sluis controller github`: the controller that makes each GitHub organisation's teams match the policy's github table."+secretsNote,
		props, []string{"apiVersion", "consoleURL"}, []string{"duration", "url"}, nil)
}

func controllerSlackSchema() m {
	props := rosterProps("slack", "/var/run/secrets/slack-roster/token", "/var/run/slack-roster/workspaces")
	props["credentialsDir"] = strDefault("One file per connected workspace: the app's credentials and its bot token.", "/var/run/slack-roster/credentials")
	return document("controller-slack", "sluis controller slack",
		"The configuration of `sluis controller slack`: the controller that makes each Slack workspace's user groups match the policy's slack table."+secretsNote,
		props, []string{"apiVersion", "consoleURL"}, []string{"duration", "url"}, nil)
}

// Schema returns one binary's schema, written as the committed file is.
func Schema(name string) ([]byte, bool) {
	var s m
	switch name {
	case "serve":
		s = serveSchema()
	case "controller-github":
		s = controllerGitHubSchema()
	case "controller-slack":
		s = controllerSlackSchema()
	case "policy":
		s = policySchema()
	default:
		return nil, false
	}
	return encode(s), true
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// platformSchema is `platform`: the answers that pick a preset.
func platformSchema() m {
	return obj("What this installation has to build on. The answers pick a preset (the decision tree in docs/design/ports.md), and start refuses an adapter that needs an answer that is no. Absent, the `ports` keys decide and nothing is checked against the platform.", m{
		"aws":        boolean("AWS is available: its credentials, DynamoDB, S3, SSM, KMS, SQS and EventBridge."),
		"kubernetes": boolean("A Kubernetes cluster is available."),
		"openbao":    boolean("An OpenBao is available."),
		"runtime":    enum("Where sluis itself runs. Absent: `kubernetes` with a cluster, else `lambda` on AWS, else `process`.", "", "kubernetes", "lambda", "process"),
		"replicas":   integer("How many replicas share this installation's state. Above 1 start refuses an adapter that keeps its data in the process (`memory`).", 1, 1),
	})
}

// presetSchema is `preset`.
func presetSchema() m {
	return enum("The adapters of a whole platform, one per concern: `server` (no AWS, no Kubernetes), `k8s-minimal`, `k8s-openbao`, `aws-serverless`, `aws-hybrid` (sluis on Lambda, a cluster for the workloads), `aws-eks`. Absent, the preset the `platform` answers lead to; with neither, the `ports` keys decide. `adapters` and the `ports` keys override single concerns.", "",
		"server", "k8s-minimal", "k8s-openbao", "aws-serverless", "aws-hybrid", "aws-eks")
}

// adaptersSchema is `adapters`: the per-concern overrides.
func adaptersSchema() m {
	choice := func(concern string) m {
		return obj("The adapter for "+concern+", replacing the preset's.", m{
			"adapter":  str("The adapter's name, as the compatibility matrix lists it."),
			"settings": m{"type": "object", "description": "The adapter's own settings (an object; the adapter refuses a key it does not know)."},
		}, "adapter")
	}
	return obj("Names the adapter of single concerns, over the preset and the `ports` keys. The names and what each needs are in the matrix of docs/design/ports.md.", m{
		"state":    choice("state, sessions included"),
		"secrets":  choice("secrets (dynamic secrets, exports under `export/`)"),
		"blobs":    choice("blobs"),
		"signing":  choice("token signing"),
		"trigger":  choice("the \"run a pass now\" trigger"),
		"schedule": choice("the schedule of passes"),
		"audit":    choice("the audit sink"),
	})
}
