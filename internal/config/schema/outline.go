//nolint:lll // a schema is prose, and a description is one string
package schema

// outlineRemoteSchema is the issuer's `outline` section: where the Outline
// module is. The issuer never runs it, so there is no `serve` here and nothing
// is in this process when it is unset: the console's Outline pages then say the
// installation runs no Outline module.
func outlineRemoteSchema() m {
	s := remoteSchema("outline", "Outline")
	s["description"] = "The Outline module's routing (docs/decisions/0077). The issuer never runs the module: unset, the console's Outline pages say there is none."
	props, _ := s["properties"].(m)
	remote, _ := props["remote"].(m)
	remote["description"] = "Where the Outline module is: the console's Outline pages are its. Set exactly one of `function` and `url`."
	return s
}

// sluisOutlineSchema is the Outline module's own document (docs/decisions/0077):
// a controller document, with the module's own section under `outline`.
func sluisOutlineSchema() m {
	props := rosterProps("outline", "/var/run/secrets/outline-roster/token", "")
	props["apiVersion"] = m{"const": Group + "/sluis-outline/v1", "description": "Which version of which document this is: the Outline module's own document (docs/decisions/0077)."}
	delete(props, "recordsDir")
	for _, k := range []string{"github", "slack", "google"} {
		delete(props, k)
	}
	props["policy"] = policyRef("The policy document: the bindings are its outline table, and its `controllers.outline.enabledWorkspaces` is what this module may change.")
	props["interval"] = duration("How long between passes. Positive. Short, because a person signing in for the first time sees nothing but their own documents until the next pass.", "2m")
	serve := remoteSchema("slack", "Outline")["properties"].(m)["serve"].(m)
	serve["description"] = "Makes the Outline module answer the console's calls on Kubernetes. The module listens on `address` and accepts a call whose projected ServiceAccount token was minted for `audience`, checked with a TokenReview, and whose subject is one of `issuers`; any other call is a 401. On Lambda the function answers `rpc` events and IAM decides who may invoke it. Unset serves nothing."
	serve["properties"].(m)["audience"] = str("The audience a caller's token must be minted for. The module's name, `outline`, when unset.")
	props["outline"] = obj("What is the Outline module's own (docs/reference/sluis/outline.md).", m{
		"serve":     serve,
		"shareScan": duration("How often the single-document shares in the collections that name a directory are read and reported. Each document is two calls to Outline.", "1h"),
	})
	s := document("sluis-outline", "sluis outline",
		"The configuration of the Outline module (`sluis outline`, and the `sluis-outline` function): the controller that makes each Outline workspace's admins, groups and collections match the policy's outline table."+secretsNote,
		props, []string{"apiVersion", "consoleURL", "policy"}, []string{"duration", "url"}, nil)
	return s
}
