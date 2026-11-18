//nolint:lll // a schema is prose, and a description is one string
package schema

const orgLogin = "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$"

// orgSettingsSchema is `controllers.github.orgSettings`, the same in the policy
// document and in the installation document (ADR 0080).
func orgSettingsSchema() m {
	repos := list("Repositories of the organisation, by name, when the visibility is `selected`.", m{"type": "string", "pattern": "^[A-Za-z0-9._-]{1,100}$"})
	name := "^[A-Za-z_][A-Za-z0-9_]*$"
	return m{
		"type":          "object",
		"propertyNames": m{"pattern": orgLogin},
		"description":   "Optional. For an organisation the github table binds, how the controller reconciles its Actions variables, Actions secrets and code security configurations from the typed document the organisation keeps in its own account (docs/guides/sluis/github-organisation-settings.md). An organisation with no entry has none of this.",
		"additionalProperties": obj("One organisation's settings.", m{
			"mode": enum("`report` computes and reports what differs and changes nothing; `enforce` applies it. The organisation's document has no key for this, so the installation can always hold an organisation to `report`.", "report", "report", "enforce"),
			"read": obj("Where the organisation's document is and as whom the controller reads it.", m{
				"roleArn":    m{"type": "string", "pattern": `^arn:[a-z-]+:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`, "description": "The role the controller assumes in the organisation's own AWS account. It can read the document and the secrets' parameters and nothing else, and it trusts only the controller's role."},
				"region":     m{"type": "string", "pattern": `^[a-z]{2}(-[a-z]+)+-[0-9]$`, "description": "The region of that account's parameter store."},
				"parameter":  m{"type": "string", "pattern": `^/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`, "description": "The name of the parameter that holds the document: the `org` section of a company fragment (docs/reference/sluis/policy-fragment.md). It must not be under `secretPrefix`."},
				"externalId": m{"type": "string", "pattern": `^[\w+=,.@:/-]+$`, "minLength": 2, "maxLength": 1224, "description": "Optional. Passed to STS as the ExternalId of the assumption, so that the organisation's trust policy can require `sts:ExternalId`. The role is also assumed with a session policy that limits it to the document and the secrets' parameters."},
			}, "roleArn", "region", "parameter"),
			"platformVariables": list("path.Match patterns of the variable names the platform owns: `CI_RUNNER_LABEL_*`. The organisation's document may not declare one; its value is set under `variables`.", m{"type": "string", "minLength": 1}),
			"platformValued":    list("Variable names whose value the platform sets and whose visibility and repositories the organisation chooses: the document may declare them with no value.", m{"type": "string", "pattern": name}),
			"secretPrefix":      m{"type": "string", "pattern": "^/([A-Za-z0-9_.-]+/)+$", "description": "The prefix every secret's parameter must have in the organisation's own parameter store, ending in a slash. Unset puts secrets out of scope."},
			"variables": m{"type": "object", "propertyNames": m{"pattern": name}, "description": "The variables the installation sets, by name. Each name must match `platformVariables` or be listed in `platformValued`.",
				"additionalProperties": obj("One variable.", m{
					"value":        m{"type": "string", "minLength": 1, "description": "The value."},
					"visibility":   enum("Which repositories see it. For a platform-valued name the organisation's document may choose its own.", "all", "all", "private", "selected"),
					"repositories": repos,
				}, "value")},
		}, "read"),
	}
}
