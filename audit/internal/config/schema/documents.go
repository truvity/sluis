//nolint:lll // a schema is prose, and a description is one string
package schema

// The documents a configuration file names beside itself. Each is YAML that was
// decoded strictly and checked in code; these are their schemas, so that what a
// deployer's CI validates against is a file and not a reading of the Go. They
// are built here, with the configuration schemas, and held to the code that
// reads them by the same tests.

// Documents are the sub-documents, as the schema files are named.
var Documents = []string{"audit-deployment", "audit-grants", "audit-workloads"}

// apiVersion is the property every document carries. Absent means v1.
func apiVersion(name string) m {
	v2 := Group + "/" + name + "/v2"
	return m{
		"enum":        []string{v2},
		"description": "The version of this document's shape, `" + v2 + "`. Absent means version 1, which is deprecated and read for one minor (`" + LegacyGroup + "/" + name + "/v1`); another value is refused, so that a later shape arrives by a version and not by a file that quietly means something else.",
	}
}

func issuers(description string) m {
	return m{
		"type": "array", "description": description,
		"items": obj("A trusted issuer.", m{
			"url":      str("The issuer's URL, as a token's `iss` claim names it."),
			"audience": str("The audience a token from this issuer must carry."),
		}, "url", "audience"),
	}
}

func document2(name, title, description string, props m, required []string) m {
	props["apiVersion"] = apiVersion(name)
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
	return s
}

func deploymentSchema() m {
	return document2("audit-deployment", "audit deployment",
		"The profile document a configuration names as `deployment`: which framework profiles each profile is composed from. Every component that reads profiles reads this one, because retention is a property of the profile and every one of them has to agree about it.",
		m{
			"profiles": m{
				"type": "object", "minProperties": 1,
				"description":   "The profiles, by name. A name is the first component of the bucket key, so it holds no `/`.",
				"propertyNames": m{"pattern": `^[^/]+$`},
				"additionalProperties": obj("One profile's composition.", m{
					"frameworks": m{"type": "array", "items": str("A framework profile's name."), "description": "The framework profiles the profile is composed from."},
					"categories": m{"type": "array", "items": m{"type": "string", "pattern": "^[a-z][a-z0-9_-]*$"}, "uniqueItems": true, "description": "The action categories (an action's `category`) this destination takes. The writer stores a projection of each record, only this destination's fields, under every destination that takes its category. Unset keeps only the actions that name the profile in their deprecated `profiles`."},
					"preset":     m{"enum": []string{"operational", "standard", "attested"}, "description": "This profile's install preset, when it asks for more than its framework profiles need (asking for less is refused). It names the preset, and so the storage, the profile's copies land in, and must be one of `presets`."},
				}, "frameworks"),
			},
			"presets": m{
				"type": "object", "minProperties": 1,
				"description":          presetDescription,
				"propertyNames":        m{"enum": []string{"operational", "standard", "attested"}},
				"additionalProperties": presetStorage(),
			},
			"external_identifiers_are_opaque": boolean("The identifiers this deployment receives for people outside the organisation are already pseudonyms an application minted, so a profile asking for `external: pseudonym` gets `clear`. Defaults to false: a deployment arrives at clear identifiers by saying so and not by omission."),
		}, []string{"profiles"})
}

func presetStorage() m {
	return obj("Where one install preset keeps its copies: a bucket of its own.", m{
		"bucket":      str("The bucket."),
		"prefix":      m{"type": "string", "pattern": "^([^/].*/)?$", "description": "The prefix within the bucket every key of this preset lives under, ending in a slash (`standard/`). Required wherever the bucket is shared with another installation."},
		"region":      str("The region. For a store at an endpoint, `auto` unless the store says otherwise."),
		"endpoint":    str("The URL of an S3-compatible store that is not AWS (for example Cloudflare R2). Empty is AWS S3. Not with the attested preset: Object Lock is S3 only."),
		"path_style":  boolean("Address the bucket as endpoint/bucket/key, for a store whose certificate does not cover a bucket subdomain. Only with `endpoint`."),
		"credentials": str("The address, below the installation's state root, of the static credentials of a store at an endpoint: a JSON object {accessKeyID, secretAccessKey} in the state store, read with the process's own identity. Only with `endpoint`: on AWS the workload's identity is the credential."),
		"credentials_preset": obj("Instead of static credentials, mint the store's R2 credentials for this process: it clones a disabled Cloudflare prototype token with the minter token and renews with a third of the lifetime left (and mints again once after a 403). Exclusive with `credentials`; only with `endpoint`. The static `credentials` stay the default and need no Cloudflare account.", m{
			"account":   str("The Cloudflare account id."),
			"minter":    str("The address, below the installation's state root, of the minter credential: a `cloudflare-minter/v1` document {schema, token} holding an account token with Account API Tokens Read and Write. It can mint anything the account owner can, so its custody is the owner's; the refusal list in the minting code is the only guard."),
			"prototype": str("The id of the DISABLED account token whose policies and condition every minted token copies. An active prototype, or one granting token admin, billing, account settings, memberships or Access identity providers, is refused at every mint."),
			"lifetime":  duration("How long each minted token lives, at least a minute.", ""),
		}, "account", "minter", "prototype", "lifetime"),
		"key_alias": m{"type": "string", "pattern": "^alias/[A-Za-z0-9/_-]+$", "description": "The alias of the KMS key this preset's objects are encrypted with, a name and never a key id or ARN. Empty is the installation's archive key, or the bucket's default encryption. Only on AWS S3."},
	}, "bucket")
}

func grantsSchema() m {
	return document2("audit-grants", "audit grants",
		"The grants document a configuration names as `grants`: who may read what. A mapping an auditor can read, kept as a file and not computed by a service.",
		m{
			"issuers": issuers("The token issuers trusted to say who a caller is. They live in this file, beside the rules, because a rule is only as safe as the issuers able to satisfy it."),
			"presets": m{
				"type": "array", "description": "Grant presets, which read grants out of a claim's vocabulary rather than one value each. Only `access-roster` exists.",
				"items": obj("A grant preset.", m{
					"name":   m{"enum": []string{"access-roster"}, "description": "The preset."},
					"issuer": str("The issuer whose claims it reads."),
					"claim":  str("The claim it reads."),
				}, "name"),
			},
			"rules": m{
				"type": "array", "description": "Rules mapping a claim value to a grant.",
				"items": obj("One rule.", m{
					"name":   str("What the rule is for, as it is named in a refusal."),
					"issuer": str("The issuer whose token must carry the claim. Unset lets any trusted issuer satisfy it, which a deployment with more than one should not."),
					"claim":  str("The claim to read."),
					"value":  str("The value of the claim that this rule applies to."),
					"grant": obj("What the holders may do.", m{
						"all_tenants": boolean("Every tenant. Without it, `tenants` names them."),
						"tenants":     m{"type": "array", "items": str("A tenant identifier."), "description": "The tenants the holders may read."},
						"profiles":    m{"type": "array", "minItems": 1, "items": str("A profile name."), "description": "The profiles the holders may read."},
						"operations": m{"type": "array", "minItems": 1, "description": "What they may do.",
							"items": m{"enum": []string{"search", "facets", "get", "export", "tail", "resolve"}}},
						"from":  m{"type": "string", "format": "date-time", "description": "RFC 3339. The earliest a record may have happened for the holders to read it. Unset is unbounded."},
						"until": m{"type": "string", "format": "date-time", "description": "RFC 3339. The latest. Unset is unbounded."},
					}, "profiles", "operations"),
				}, "name", "grant"),
			},
		}, nil)
}

func workloadsSchema() m {
	return document2("audit-workloads", "audit workloads",
		"The workloads document a writer's configuration names as `workloads`: the issuers trusted to say which workload is publishing, and which source each speaks for.",
		m{
			"issuers": func() m {
				i := issuers("The issuers whose tokens name a workload. At least one: a writer with none could never admit anybody.")
				i["minItems"] = 1
				return i
			}(),
			"workloads": m{
				"type": "array", "description": "The service accounts that publish, and the source each speaks for.",
				"items": obj("One workload.", m{
					"issuer":  str("The issuer the subject belongs to. Required when more than one issuer is trusted."),
					"subject": str("The token's subject: in a cluster, `system:serviceaccount:<namespace>:<name>`."),
					"source":  str("The catalogue source the workload may record for."),
				}, "subject", "source"),
			},
		}, []string{"issuers"})
}

// presetDescription is what the install preset is, wherever it is set: the
// deployment document, the chart's values.
const presetDescription = "The install presets this installation uses, each with the storage of its own (`operational`, `standard`, `attested`). A profile's preset is the lowest its framework profiles can be kept under (`min_preset`), or the stronger one it asks for, and must be configured here. Object Lock is a property of the preset's bucket: `attested` is compliance Object Lock on S3 (never at an endpoint), every other preset is unlocked. The notary, seal key, alarms and pseudonym keys are provisioned when any configured preset needs them."
