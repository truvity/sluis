//nolint:lll // a schema is prose, and a description is one string
package schema

// The policy document's schema. Its tables are the access model's
// (docs/reference/policy.md), held here to their shape: every object closed, so
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
		"description": "Which held groups beyond the requires pairs a token for it carries: `all`, or a list of things, families or names (docs/reference/policy.md#groups-in-a-token-scoping).",
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

func clientSchema() m {
	return obj("A client: who may be issued a token, and for what. Its id is the audience.", m{
		"kind":                   m{"enum": []string{"public", "confidential", "exchange"}, "description": "public, confidential or exchange."},
		"display_name":           str("What the sign-in page calls it. Public."),
		"description":            str("One line about it on the sign-in page. Public."),
		"secret":                 str("For a confidential client: the name its secret is delivered under."),
		"redirects":              strList("The redirect URIs."),
		"signed_out":             strList("Where sign-out may return the browser."),
		"requires":               strList("The groups a caller must hold, any of them."),
		"ttl_cap":                duration("The longest a token for it lives.", ""),
		"sign_in_exchange":       boolean("It may exchange a sign-in for a token of its own."),
		"backchannel_logout_uri": str("Where a back-channel logout is posted."),
		"signing_alg":            str("The algorithm its tokens are signed with, when it cannot verify the default: RS256, ES256 or ES384."),
		"groups":                 groupsOverride(),
		"groups_delimiter":       str("Rewrites `:` in its `groups` claim (docs/decisions/0015)."),
	}, "kind")
}

func resourceSchema() m {
	return obj("A resource a token may be minted for, when it is not the client asking.", m{
		"display_name":     str("What the consent page calls it."),
		"description":      str("One line about it."),
		"requires":         strList("The groups a caller must hold, any of them."),
		"ttl_cap":          duration("The longest a token for it lives.", ""),
		"absolute_cap":     duration("The longest a session that touched it lives.", ""),
		"read_only":        boolean("It only reads: an absolute cap past the installation's may be honoured."),
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
		"events":       strList("Webhook events; the webhook stays inactive."),
		"installation": m{"enum": []string{"all", "selected"}, "description": "all or selected (default)."},
		"grants": list("Who may ask for tokens of it, and for how much.", obj("A grant.", m{
			"group":        str("The internal group."),
			"repositories": m{"type": "array", "minItems": 1, "items": m{"type": "string", "minLength": 1}, "description": "Repository globs."},
			"permissions":  perms,
		}, "group", "repositories", "permissions")),
	}, "id", "org", "permissions")
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
			"runnerTiers": m{"type": "array", "uniqueItems": true, "items": m{"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,14}[a-z0-9])?$"}, "description": "The tiers an operator may create a runner App for: lower-case letters, digits and dashes, at most 16, each once."},
			"catalogue":   list("Every GitHub App the installation declares (docs/connect/github-apps-catalogue.md). A grant naming an undeclared group stops the service.", catalogueAppSchema()),
		}),
		"slack": obj("Slack Apps.", m{
			"catalogue": list("Every Slack App the installation declares (docs/connect/slack-apps-catalogue.md). One for a workspace the policy does not declare stops the service.", slackAppSchema()),
		}),
	})
}

func policyControllersSchema() m {
	return obj("What each controller may CHANGE. Everything else the policy binds is derived every pass and shown with what would happen, and left alone: an organisation or workspace is born disabled.", m{
		"github": obj("The GitHub controller.", m{
			"enabledOrgs": list("The organisations it changes. Each must be bound by the github table.", m{"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$"}),
		}),
		"slack": obj("The Slack controller.", m{
			"enabledWorkspaces": list("The workspaces it changes, by key. Each must be declared by the slack table.", m{"type": "string", "pattern": "^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$"}),
		}),
	})
}

func policySchema() m {
	exports := exportsSchema()
	exports["description"] = "The secrets the service copies out of itself into the store its `ports.export` (or its secrets adapter) names (docs/decisions/0034): a copy is asynchronous, retried with backoff and never a dependency. An unknown source, a source this document does not declare and two exports that would write one key are refused before anything starts."
	props := m{
		"apiVersion":       apiVersion("policy"),
		"vocabulary":       vocabularySchema(),
		"groups":           table("Every internal group, by name, and how a caller comes to be in it.", obj("A group.", m{"members": strList("Directory groups whose members are in it."), "matchers": list("The matchers.", matcherSchema())})),
		"claims":           table("What a group adds to a token, by group.", m{"type": "object", "description": "A claims fragment: merged into the token (docs/reference/policy.md#claims)."}),
		"lifetimes":        table("How long a token lives, by group, and `default`. The shortest across a caller's groups wins.", duration("A lifetime.", "")),
		"resources":        table("What a token may be minted FOR, by resource indicator.", resourceSchema()),
		"client_documents": obj("Admits clients that are not declared, by a document they serve about themselves. Off unless it names an origin.", m{"origins": strList("The origins."), "requires": strList("The groups a caller must hold."), "ttl_cap": duration("The longest a token lives.", ""), "groups": groupsOverride()}),
		"clients":          table("Who may be issued a token, by client id.", clientSchema()),
		"github":           githubTableSchema(),
		"people":           table("Which addresses are one person, by a name the installation chooses.", strList("The addresses.")),
		"slack":            slackTableSchema(),
		"exchange":         policyExchangeSchema(),
		"apps":             policyAppsSchema(),
		"controllers":      policyControllersSchema(),
		"exports":          exports,
	}
	return document("policy", "sluis policy",
		"The policy document: what an installation decides, read by every process of it. The access model's tables (docs/reference/policy.md) and beside them whom the exchange trusts, what an operator may make, what each controller may change and what is copied out. Rendered by `sluisctl policy render` from layers; a process reads exactly one. Nothing here is a secret.",
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
