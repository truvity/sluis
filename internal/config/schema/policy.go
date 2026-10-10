//nolint:lll // a schema is prose, and a description is one string
package schema

// The policy document's schema. Its tables are the access model's
// (docs/reference/sluis/policy.md), held here to their shape: every object closed, so
// a misspelt key fails the document before the access model's own parser reads
// it. What a shape cannot say (a group a client requires that nobody declared,
// a catalogue grant naming no group) is the loader's semantic check, the same
// one `sluisctl policy render` and the Pulumi library run.

func strList(description string) m { return list(description, m{"type": "string", "minLength": 1}) }

func table(description string, value m) m {
	return m{"type": "object", "additionalProperties": value, "description": description}
}

// groupsOverride is `groups: all` or a list of thing names.
func groupsOverride() m {
	return m{
		"description": "Which held groups beyond the requires pairs a token for it carries: `all`, or a list of things, families or names (docs/reference/sluis/policy.md#groups-in-a-token-scoping).",
		"oneOf": []any{
			m{"const": "all"},
			m{"type": "array", "items": m{"type": "string", "minLength": 1}},
		},
	}
}

func matcherSchema() m {
	return obj("One way a caller comes to be in the group. Exactly one kind is set.", m{
		"github": obj("A GitHub Actions job, by what its verified token says.", m{
			"repository":       str("owner/name, a glob."),
			"owner":            str("The repository's owner."),
			"ref":              str("The ref, a glob."),
			"workflow":         str("The workflow's name."),
			"environment":      str("The deployment environment."),
			"visibility":       str("public, private or internal."),
			"workflow_ref":     str("The workflow file at a ref, a glob."),
			"job_workflow_ref": str("The reusable workflow at a ref, a glob."),
			"sha":              str("The commit."),
			"event_name":       str("The triggering event."),
			"ref_type":         str("branch or tag."),
		}),
		"service_account": obj("A ServiceAccount on a federated cluster.", m{
			"cluster":   str("The cluster's name in `exchange.clusters`. Unset is the unqualified subject."),
			"namespace": str("The namespace."),
			"name":      str("The ServiceAccount."),
		}, "namespace", "name"),
		"aws": obj("An IAM role in an account `exchange.aws` federates.", m{
			"account":  str("The 12-digit account id."),
			"role":     str("The role's name, a glob."),
			"path":     str("The role's path."),
			"function": str("A Lambda function's name."),
			"org_id":   str("The AWS Organizations id."),
		}, "account"),
		"email":        str("One signed-in address."),
		"email_domain": str("Every signed-in address in a domain."),
	})
}

func vocabularySchema() m {
	role := m{
		"description": "A role: the roles it implies, or `{implies, scopes}` to restrict it to some of its thing's scopes.",
		"oneOf": []any{
			m{"type": "array", "items": m{"type": "string", "minLength": 1}},
			obj("A role restricted to scopes.", m{
				"implies": strList("The roles it implies."),
				"scopes":  strList("The scopes it may be exercised on."),
			}),
		},
	}
	return obj("Which scopes, things and roles a grant name may use, and each thing's role ladder. Optional: absent, any `<scope>:<thing>:<role>` is accepted unchecked.", m{
		"scopes": table("The scopes.", obj("A scope.", m{
			"sensitive": boolean("Grants on it are sensitive."),
		})),
		"things": table("The things.", obj("A thing.", m{
			"scopes": strList("The scopes it exists in."),
			"roles":  table("Its roles.", role),
		})),
	})
}

// clientSecretSchema is a client's `secret`: the name of an input, or
// `{generate: true}` to have the issuer make the value itself.
func clientSecretSchema() m {
	return m{
		"description": "For a confidential client: the name its secret is delivered under, or `{generate: true}` to have the issuer generate it and keep it with its credentials.",
		"oneOf": []any{
			m{"type": "string", "minLength": 1},
			obj("The issuer generates the secret.", m{
				"generate": m{"const": true, "description": "Must be true; `generate: false` is refused."},
			}, "generate"),
		},
	}
}

// sessionClass is `session`: the class of the refresh chains a client opens.
func sessionClass(whose, rule string) m {
	return m{
		"enum":        []string{"interactive", "agent"},
		"default":     "interactive",
		"description": whose + ": `interactive` (a person at a browser, the installation's `lifetimes`) or `agent` (software that holds its refresh token and works in the background, the service's `lifetimes.agent`). Recorded on each chain when its authorization completes (docs/decisions/0040). " + rule,
	}
}

func clientSchema() m {
	return obj("A client: who may be issued a token, and for what. Its id is the audience.", m{
		"kind":                   m{"enum": []string{"public", "confidential", "exchange"}, "description": "public, confidential or exchange."},
		"display_name":           str("What the sign-in page calls it. Public."),
		"description":            str("One line about it on the sign-in page. Public."),
		"secret":                 clientSecretSchema(),
		"redirects":              strList("The redirect URIs."),
		"signed_out":             strList("Where sign-out may return the browser."),
		"requires":               strList("The groups a caller must hold, any of them."),
		"ttl_cap":                duration("The longest a token for it lives.", ""),
		"sign_in_exchange":       boolean("It may exchange a sign-in for a token of its own."),
		"backchannel_logout_uri": str("Where a back-channel logout is posted."),
		"signing_alg":            str("The algorithm its tokens are signed with, when it cannot verify the default: RS256, ES256 or ES384."),
		"groups":                 groupsOverride(),
		"groups_delimiter":       str("Rewrites `:` in its `groups` claim (docs/decisions/0015)."),
		"session":                sessionClass("Its refresh chains", "Refused on an exchange client and, as `agent`, with `sign_in_exchange`."),
	}, "kind")
}

func resourceSchema() m {
	return obj("A resource a token may be minted for, when it is not the client asking.", m{
		"display_name":     str("What the consent page calls it."),
		"description":      str("One line about it."),
		"requires":         strList("The groups a caller must hold, any of them."),
		"ttl_cap":          duration("The longest a token for it lives.", ""),
		"absolute_cap":     duration("The longest a session that touched it lives.", ""),
		"read_only":        boolean("It only reads: an absolute cap past the installation's may be honoured. Deprecated as a lengthening (docs/decisions/0040): mark the clients `session: agent` instead; a lengthening row is warned about at start."),
		"signing_alg":      str("The algorithm its tokens are signed with: RS256, ES256 or ES384."),
		"groups":           groupsOverride(),
		"groups_delimiter": str("Rewrites `:` in its `groups` claim."),
	})
}

func slackTableSchema() m {
	return obj("Slack channels bound to internal groups, across workspaces.", m{
		"workspaces": table("Each workspace, by the installation's key.", obj("A workspace.", m{
			"channels": table("Each channel, by name.", obj("A channel's binding.", m{
				"private": boolean("A private channel."),
				"mode":    str("How membership is kept."),
				"ignore":  strList("Members left alone."),
				"from":    strList("The groups whose holders belong in it."),
				"adopt":   str("Adopt an existing channel."),
			})),
		})),
	})
}

func githubTableSchema() m {
	return table("GitHub organisations bound to internal groups, by login.", obj("An organisation's bindings.", m{
		"members": strList("The groups whose holders belong in the organisation."),
		"teams": table("Each team, by slug.", obj("A team's binding.", m{
			"members":     strList("The groups whose holders are members."),
			"maintainers": strList("The groups whose holders are maintainers."),
		})),
		"ignore": strList("Addresses and logins the controller leaves alone."),
	}))
}

// catalogueAppSchema is one GitHub catalogue App, as internal/githubapp/catalogue reads it.
func catalogueAppSchema() m {
	perms := m{"type": "object", "minProperties": 1, "additionalProperties": m{"enum": []string{"read", "write", "admin"}}, "description": "Permission name to level: read, write or admin."}
	return obj("One App: created under `org` from the console, and installed there.", m{
		"id":           m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$", "description": "[a-z0-9-], at most 32, unique; never changes."},
		"org":          m{"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$", "description": "The organisation it is created under."},
		"name":         m{"type": "string", "minLength": 1, "maxLength": 34, "description": "Default <org>-<id>."},
		"description":  str("What it is for."),
		"public":       boolean("Installable by other organisations."),
		"permissions":  perms,
		"events":       strList("Webhook events the App subscribes to. They are delivered only to an App that declares a `webhook`."),
		"webhook":      catalogueWebhookSchema(),
		"installation": m{"enum": []string{"all", "selected"}, "description": "all or selected (default)."},
		"export":       boolean("Place the installed App's key at `external/github/<id>` for a consumer to read. Unset keeps it internal. An id may not begin `runner-`."),
		"grants": list("Who may ask for tokens of it, and for how much.", obj("A grant.", m{
			"group":        str("The internal group."),
			"repositories": m{"type": "array", "minItems": 1, "items": m{"type": "string", "minLength": 1}, "description": "Repository globs."},
			"permissions":  perms,
		}, "group", "repositories", "permissions")),
	}, "id", "org", "permissions")
}

// githubAppSchema is one entry of `apps.github.apps`: a catalogue App's
// declaration, a runner tier or the link App, told apart by `purpose`.
func githubAppSchema() m {
	app := catalogueAppSchema()
	props := app["properties"].(m)
	props["id"] = m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$", "description": "The App's key in storage, unique, never changes. `link` for the link App; for a catalogue App [a-z0-9-], at most 32, not beginning `runner-`; a runner entry may leave it out (`runner-<tier>`, and `runner-<tier>-<org>` with an `org`)."}
	props["purpose"] = m{"enum": []string{"link", "catalogue", "runner"}, "description": "What the App is for: `link` is the App that links a person's GitHub account, `catalogue` an App an operator creates and installs from the console, `runner` the tier an operator may create a runner App for."}
	props["labels"] = m{"type": "object", "maxProperties": 16, "propertyNames": m{"pattern": "^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$"}, "additionalProperties": m{"type": "string", "pattern": "^([a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?)?$"}, "description": "At most 16 short lower-case labels, key to value, kept on the App's record for a reader that selects Apps by them."}
	props["tier"] = m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,14}[a-z0-9])?$", "description": "A runner entry's tier: lower-case letters, digits and dashes, at most 16."}
	props["org"] = m{"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$", "description": "The organisation a catalogue App is created under (required for it), or the only organisation of a runner entry (no `org` offers the tier in every organisation). The link App's owner."}
	props["export"] = boolean("Place the installed App's key at `external/github/<id>` for a consumer to read. Unset keeps it internal. A runner App is always exported.")
	app["description"] = "One App. A `catalogue` entry needs `org` and `permissions`; a `runner` entry needs `tier` and declares nothing of a catalogue App's; the `link` entry has the id `link`."
	app["required"] = []string{"purpose"}
	app["allOf"] = []any{
		m{"if": m{"properties": m{"purpose": m{"const": "catalogue"}}, "required": []string{"purpose"}}, "then": m{"required": []string{"id", "org", "permissions"}}},
		m{"if": m{"properties": m{"purpose": m{"const": "runner"}}, "required": []string{"purpose"}}, "then": m{"required": []string{"tier"}}},
		m{"if": m{"properties": m{"purpose": m{"const": "link"}}, "required": []string{"purpose"}}, "then": m{"required": []string{"id"}, "properties": m{"id": m{"const": "link"}}}},
	}
	return app
}

// deprecated marks a schema node deprecated.
func deprecated(node m) m {
	node["deprecated"] = true
	return node
}

// catalogueWebhookSchema is where a catalogue App's events are delivered: one
// URL, or a Kargo receiver.
func catalogueWebhookSchema() m {
	httpsURL := func(description string) m {
		return m{"type": "string", "pattern": `^https://[^/@\s]+(/.*)?$`, "description": description}
	}
	w := obj("Where GitHub delivers the App's events: exactly one of `url` and `kargo`. Needs non-empty `events`; set `export: true` so a consumer can read the secret. The secret is generated by this service, set on GitHub right after the App is created, and kept at `external/github/<id>` as `webhook_secret`; it is rotated from the console, without overlap (docs/decisions/0041). An App created before this is declared cannot be switched to a webhook: GitHub has no API for that, so disconnect it and create it again.", m{
		"url": httpsURL("The endpoint every delivery is POSTed to, e.g. Argo CD's `https://argocd.example/api/webhook`."),
		"kargo": obj("A Kargo GitHub receiver. Kargo derives the receiver's path from its secret (`/github/<hex sha256(project + receiver + secret)>`), so the URL moves whenever the secret does.", m{
			"base":     httpsURL("Where Kargo's receivers are served."),
			"receiver": str("The receiver's name in the Kargo project."),
			"project":  m{"type": "string", "description": "The Kargo project; empty for a cluster-scoped receiver."},
		}, "base", "receiver"),
	})
	w["oneOf"] = []any{m{"required": []string{"url"}}, m{"required": []string{"kargo"}}}
	return w
}

func slackAppSchema() m {
	return obj("One Slack App, created and installed from the console.", m{
		"id":          m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$", "description": "[a-z0-9-], at most 32, unique; never changes."},
		"workspace":   str("A key of the policy's slack.workspaces."),
		"name":        m{"type": "string", "minLength": 1, "maxLength": 35, "description": "Default <workspace>-<id>."},
		"description": m{"type": "string", "minLength": 1, "maxLength": 140, "description": "At most 140."},
		"botScopes":   m{"type": "array", "minItems": 1, "items": m{"type": "string", "minLength": 1}, "description": "The bot scopes."},
	}, "id", "workspace", "botScopes")
}

func policyExchangeSchema() m {
	return obj("Whom the token exchange trusts. Nothing here is a secret: every row is a name and a URL.", m{
		"clusters": list("The clusters whose ServiceAccount tokens are verified, each against the key set it publishes for itself. This service's own cluster is a row like any other.", obj("A cluster.", m{
			"name":    str("The estate's word for it: the cluster in a `service_account` matcher. Renaming a row changes every rule about it."),
			"issuer":  str("The `iss` its ServiceAccount tokens carry."),
			"jwksUri": url("Where its keys are. Unset discovers them from the issuer."),
		}, "name", "issuer")),
		"aws": obj("The AWS accounts whose roles' outbound identity tokens (sts:GetWebIdentityToken) are verified. An empty list verifies none.", m{
			"audience": str("The audience the role must request. Required with an account: it is the trust boundary."),
			"maxAge":   duration("Refuse a token whose `iat` is older than this. At most 1h.", "5m"),
			"accounts": list("The accounts.", obj("An account.", m{
				"account": m{"type": "string", "pattern": "^[0-9]{12}$", "description": "The 12-digit account id."},
				"name":    str("The estate's word for it, for logs."),
				"issuer":  url("From `aws iam get-outbound-web-identity-federation-info`. https."),
				"jwksUri": url("Default <issuer>/.well-known/jwks.json."),
				"orgId":   str("Require this AWS Organizations id."),
				"algs":    m{"type": "array", "uniqueItems": true, "items": m{"enum": []string{"ES384", "RS256"}}, "description": "Default both; narrow, never widen."},
			}, "account", "name", "issuer")),
		}),
		"github": obj("Whose GitHub Actions tokens are verified, for this issuer's URL as the audience.", m{
			"owners": strList("The organisations. None verifies none: an empty list would admit every repository there is."),
		}),
	})
}

func policyAppsSchema() m {
	return obj("What an operator may make on the console.", m{
		"github": obj("GitHub Apps.", m{
			"apps":        list("Every GitHub App the installation declares, of every purpose: the link App, the catalogue Apps and the runner tiers (docs/guides/sluis/connect/github-apps-catalogue.md). A grant naming an undeclared group stops the service.", githubAppSchema()),
			"runnerTiers": m{"deprecated": true, "type": "array", "uniqueItems": true, "items": m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,14}[a-z0-9])?$"}, "description": "DEPRECATED, removed in v1.77: declare a `purpose: runner` entry in `apps` for each tier. Read as those entries, with a warning."},
			"catalogue":   deprecated(list("DEPRECATED, removed in v1.77: declare each as a `purpose: catalogue` entry in `apps`. Read as those entries, with a warning.", catalogueAppSchema())),
		}),
		"slack": obj("Slack Apps.", m{
			"catalogue": list("Every Slack App the installation declares (docs/guides/sluis/connect/slack-apps-catalogue.md). One for a workspace the policy does not declare stops the service.", slackAppSchema()),
		}),
	})
}

func policyControllersSchema() m {
	return obj("What each controller may CHANGE. Everything else the policy binds is derived every pass and shown with what would happen, and left alone: an organisation or workspace is born disabled.", m{
		"github": obj("The GitHub controller.", m{
			"enabledOrgs": list("The organisations it changes. Each must be bound by the github table.", m{"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$"}),
			"appRefs":     m{"type": "object", "propertyNames": m{"pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$"}, "additionalProperties": m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"}, "description": "For an organisation the github table binds, the id of the catalogue App (in `apps.github.apps`, created under that organisation) whose key its record refers to, `app_ref`. On storage layout v5 an organisation must refer to an App."},
		}),
		"slack": obj("The Slack controller.", m{
			"enabledWorkspaces": list("The workspaces it changes, by key. Each must be declared by the slack table.", m{"type": "string", "pattern": "^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$"}),
		}),
	})
}

func policySchema() m {
	props := m{
		"apiVersion":       apiVersion("policy"),
		"vocabulary":       vocabularySchema(),
		"groups":           table("Every internal group, by name, and how a caller comes to be in it.", obj("A group.", m{"members": strList("Directory groups whose members are in it."), "matchers": list("The matchers.", matcherSchema())})),
		"claims":           table("What a group adds to a token, by group.", m{"type": "object", "description": "A claims fragment: merged into the token (docs/reference/sluis/policy.md#claims)."}),
		"lifetimes":        table("How long a token lives, by group, and `default`. The shortest across a caller's groups wins.", duration("A lifetime.", "")),
		"resources":        table("What a token may be minted FOR, by resource indicator.", resourceSchema()),
		"client_documents": obj("Admits clients that are not declared, by a document they serve about themselves. Off unless it names an origin.", m{"origins": strList("The origins."), "requires": strList("The groups a caller must hold."), "ttl_cap": duration("The longest a token lives.", ""), "groups": groupsOverride(), "session": sessionClass("Every document client's refresh chains", "Never read from a document: admit with `agent` only an origin whose documents its vendor controls.")}),
		"clients":          table("Who may be issued a token, by client id.", clientSchema()),
		"github":           githubTableSchema(),
		"people":           table("Which addresses are one person, by a name the installation chooses.", strList("The addresses.")),
		"slack":            slackTableSchema(),
		"exchange":         policyExchangeSchema(),
		"apps":             policyAppsSchema(),
		"controllers":      policyControllersSchema(),
		"cloudflare":       policyCloudflareSchema(),
	}
	return document("policy", "sluis policy",
		"The policy document: what an installation decides, read by every process of it. The access model's tables (docs/reference/sluis/policy.md) and beside them whom the exchange trusts, what an operator may make, what each controller may change and what is copied out. Rendered by `sluisctl policy render` from layers; a process reads exactly one. Nothing here is a secret.",
		props, []string{"apiVersion"}, []string{"duration", "url"}, nil)
}

func directorySchema() m {
	return obj("The corporate directories this deployment declares: each adopted at start (the console connects the others). Unset declares none.", m{
		"workspaces": list("The declared workspaces. One that cannot be adopted stops the service.", obj("A workspace: deliberately thin. Its domains and the tenant's own id are discovered.", m{
			"id":         str("The backend's tenant id. Optional: given, the adoption checks it."),
			"backend":    m{"enum": []string{"google"}, "description": "The implementation that reads it."},
			"admin":      str("The account the credential impersonates."),
			"keySecret":  secretField("The secret the service-account key is: `directory/<id>/key`."),
			"serve":      strList("Narrows the tenant to these domains. Empty serves every domain discovered."),
			"syncGroups": strList("Narrows the tenant to these groups. Empty keeps every group in the served domains."),
		}, "backend", "admin", "keySecret")),
	})
}
