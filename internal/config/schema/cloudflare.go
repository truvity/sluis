//nolint:lll // a schema is prose, and a description is one string
package schema

const (
	cloudflareName = `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`
)

// cloudflareSchema is the service document's `cloudflare` section. What a
// pattern cannot say (rotation shorter than lifetime, both at least a minute,
// an account a preset names being declared) is internal/config.Cloudflare.Validate.
func cloudflareSchema() m {
	section := obj("sluis as the STS for Cloudflare API tokens and R2 credentials (docs/guides/sluis/cloudflare-tokens.md). Cloudflare has no web-identity federation, so sluis holds one minter credential per account and hands out short-lived account tokens cloned from a DISABLED prototype token. Needs the ssm secrets source. Unset is off.", m{
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
			"additionalProperties": withRotationUnlessOnDemand(obj("One preset: a prototype token and how long its clones live.", m{
				"account":                  str("A key of `accounts`."),
				"prototype":                m{"type": "string", "pattern": `^[A-Za-z0-9_-]{8,64}$`, "description": "The id of the prototype: an account token in Cloudflare that is DISABLED and whose policies and condition are the preset's rights. An active prototype is refused (it would be a usable token that never expires), and so is one granting Account API Tokens Edit, Billing, Account Settings, Memberships or Access identity providers. It is read at every mint, so editing it applies at the next rotation."},
				"description":              str("What the preset is for. Shown by the console and `sluisctl whoami`."),
				"lifetime":                 duration("How long each minted token lives: its `expires_on`. At least 1m, at most 24h. Past it sluis deletes the token in Cloudflare.", ""),
				"rotation":                 duration("How often the stored token is replaced. At least 1m and shorter than `lifetime`: `lifetime - rotation` is the time consumers have to pick up a new token. Required unless `store` is `none`, and then left out.", ""),
				"store":                    enum("Where the schedule keeps this preset's token. `external` rotates it into `external/cloudflare/<preset>` for consumers to read. `none` keeps nothing: the schedule never mints it, no parameter exists for it, and a token is minted only on demand (`sluisctl cloudflare token <preset>`) for a caller the policy's grants allow.", "external", "external", "none"),
				"allowedPermissionGroups":  list("Limits the prototype to these permission groups, by the name Cloudflare lists them under (case and spacing do not matter). A prototype that grants any other is refused with `prototype_forbidden`. Unset asks nothing beyond the built-in list. The built-in list and `forbiddenPermissionGroups` stay the floor: a name here does not lift them. Prefer setting it: the built-in list names five kinds of group, and Cloudflare adds new ones.", m{"type": "string", "minLength": 1}),
				"maxLivePerCaller":         m{"type": "integer", "minimum": 1, "description": "How many unexpired on-demand tokens of this preset one caller may hold; a mint over it is refused (`caller_cap`). Unset is 10."},
				"maxLive":                  m{"type": "integer", "minimum": 1, "description": "How many unexpired on-demand tokens this preset may have in all, whoever holds them; a mint over it is refused (`preset_cap`). Cloudflare allows an account 500 tokens in all, and an expired one counts until it is deleted. The record of minted tokens holds about 65 on-demand tokens, so a value above about 60 is refused as `record_full` first. Unset is 50."},
				"maxMintsPerCallerPerHour": m{"type": "integer", "minimum": 1, "description": "How many tokens of this preset one caller may be minted in an hour; one over it is refused (`rate_limited`). Counted from the preset's record of minted tokens and from this process's own mints, so a token that expired inside the hour and was deleted is only counted where it was minted. Unset is 30."},
				"endpoint":                 url("Makes this an R2 preset: the S3 endpoint of the account (`https://<account-id>.r2.cloudflarestorage.com`, or `https://<account-id>.eu.r2.cloudflarestorage.com` for an EU jurisdiction). The stored document then holds an access key and secret instead of a token."),
			}, "account", "prototype", "description", "lifetime")),
		},
		"tick": duration("How often the rotation looks at the presets: the interval of the schedule that invokes it on Lambda (the library fills it from `CloudflareRotation` when unset), a minute for the Kubernetes loop. A credential rotates up to one tick after its preset's `rotation`, so the console calls one overdue only past `rotation + tick`; make `rotation` a multiple of the tick to rotate when intended. Unset is a minute.", ""),
		"remote": obj("Where the minter is when it is not this process: the on-demand token exchange calls the Cloudflare module there (docs/decisions/0071), and this document then declares no `accounts` or `presets`. Set exactly one of `function` and `url`. Unset keeps the minter in this process.", m{
			"function":  str("The module's Lambda function, name or ARN. It is invoked through its `live-issuer` alias."),
			"url":       url("The module's Kubernetes Service. A call carries the pod's projected ServiceAccount token."),
			"audience":  str("The audience of that token. The module's name, `cloudflare`, when unset."),
			"tokenFile": str("Where the projected token is mounted. `/var/run/secrets/sluis/cloudflare/token` when unset."),
		}),
		"serve": obj("Makes the Cloudflare module answer other modules' calls (docs/decisions/0071): the issuer's token exchange when its `remote` names this module. On Kubernetes the module listens on `address` and accepts a call that carries a projected ServiceAccount token minted for `audience`, checked with a TokenReview; any other call is a 401. On Lambda the function answers `rpc` events, and IAM decides who may invoke it. Needs `presets`; not with `remote`. Unset serves nothing.", m{
			"address":  str("The listener on Kubernetes, `host:port`; it also answers `GET /healthz`. Unused on Lambda."),
			"audience": str("The audience a caller's token must be minted for. The module's name, `cloudflare`, when unset."),
			"issuers":  m{"type": "array", "uniqueItems": true, "items": m{"type": "string", "pattern": `^system:serviceaccount:[^:]+:[^:]+$`}, "description": "The issuer's ServiceAccount, as `system:serviceaccount:<namespace>:<name>`, which may call the listener on Kubernetes. A call from it is both the issuer class (the mint) and the console class; a token of any other subject is a 401. The listener refuses to start with none. Unused on Lambda."},
		}),
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

// policyBackupSchema is the policy document's `backup` section.
func policyBackupSchema() m {
	return obj("Who may start a backup through the issuer, from the console or `sluisctl backup run`. Reading the status, the list and a restore needs the viewer role and is not named here. A restore is never started through the issuer.", m{
		"runGroups": m{"type": "array", "uniqueItems": true, "items": m{"type": "string", "minLength": 1}, "description": "Groups of this policy: every holder may start a backup. Empty or unset lets nobody; the operators group is not implied."},
	})
}

// withRotationUnlessOnDemand makes `rotation` required for a stored preset and
// refused for one with `store: none`, which is never rotated.
func withRotationUnlessOnDemand(preset m) m {
	preset["if"] = m{"properties": m{"store": m{"const": "none"}}, "required": []string{"store"}}
	preset["then"] = m{"not": m{"required": []string{"rotation"}}}
	preset["else"] = m{"required": []string{"rotation"}}
	return preset
}
