//nolint:lll // a schema is prose, and a description is one string
package schema

const (
	cloudflareName = `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`
)

// cloudflareSchema is the service document's `cloudflare` section. What a
// pattern cannot say (rotation shorter than lifetime, both at least a minute,
// an account a preset names being declared) is internal/config.Cloudflare.Validate.
func cloudflareSchema() m {
	section := obj("sluis as the STS for Cloudflare API tokens and R2 credentials (docs/how-to/cloudflare-tokens.md). Cloudflare has no web-identity federation, so sluis holds one minter credential per account and hands out short-lived account tokens cloned from a DISABLED prototype token. Needs `secrets.layout: v4` or `transition`. Unset is off.", m{
		"accounts": m{
			"type": "object", "propertyNames": m{"pattern": cloudflareName},
			"description": "The Cloudflare accounts sluis mints in, by the name presets use.",
			"additionalProperties": obj("One account.", m{
				"id":     m{"type": "string", "pattern": `^[0-9a-f]{32}$`, "description": "The account id."},
				"minter": m{"type": "string", "pattern": `^internal/[a-z0-9][a-z0-9-]{0,30}(/[a-z0-9][a-z0-9._-]{0,62}){1,3}$`, "description": "The internal address of the minter credential, `internal/cloudflare/<account>/minter`: a cloudflare-minter/v1 document holding an account token that has Account API Tokens Read and Edit and nothing else. It is never an external address: nobody but sluis is granted it."},
			}, "id", "minter"),
		},
		"presets": m{
			"type": "object", "propertyNames": m{"pattern": cloudflareName},
			"description": "What may be minted, by name (a DNS label). The name is in the external address, `external/cloudflare/<preset>`, and in the name of every token minted for it.",
			"additionalProperties": obj("One preset: a prototype token and how long its clones live.", m{
				"account":     str("A key of `accounts`."),
				"prototype":   m{"type": "string", "pattern": `^[A-Za-z0-9_-]{8,64}$`, "description": "The id of the prototype: an account token in Cloudflare that is DISABLED and whose policies and condition are the preset's rights. An active prototype is refused (it would be a usable token that never expires), and so is one granting Account API Tokens Edit, Billing, Account Settings, Memberships or Access identity providers. It is read at every mint, so editing it applies at the next rotation."},
				"description": str("What the preset is for. Shown by the console and `sluisctl whoami`."),
				"lifetime":    duration("How long each minted token lives: its `expires_on`. At least 1m, at most 24h. Past it sluis deletes the token in Cloudflare.", ""),
				"rotation":    duration("How often the stored token is replaced. At least 1m and shorter than `lifetime`: `lifetime - rotation` is the time consumers have to pick up a new token.", ""),
				"endpoint":    url("Makes this an R2 preset: the S3 endpoint of the account (`https://<account-id>.r2.cloudflarestorage.com`, or `https://<account-id>.eu.r2.cloudflarestorage.com` for an EU jurisdiction). The stored document then holds an access key and secret instead of a token."),
			}, "account", "prototype", "description", "lifetime", "rotation"),
		},
		"forbiddenPermissionGroups": list("Permission groups, by the name Cloudflare lists them under, that a prototype may never grant, IN ADDITION to the built-in list: Account API Tokens Edit (Write), Billing, Account Settings, Memberships, and Access: Organizations, Identity Providers, and Groups. The built-in list is the only thing between the minter and everything its creator could do, so it cannot be shortened by configuration, only extended.", m{"type": "string", "minLength": 1}),
	})
	return section
}

// policyCloudflareSchema is the policy document's `cloudflare` section: who may
// ask for which preset. The presets are the service document's.
func policyCloudflareSchema() m {
	return obj("Who may ask for a Cloudflare preset (the presets are the service document's `cloudflare.presets`). A preset named here and not there stops the service.", m{
		"grants": list("One row per group.", obj("A grant.", m{
			"group":   str("A group of this policy: every holder may ask, a person from the console or `sluisctl`, a CI job through the exchange. A CI job is a group declared with `github` matchers."),
			"presets": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": m{"type": "string", "pattern": cloudflareName}, "description": "The presets the row opens."},
		}, "group", "presets")),
	})
}
