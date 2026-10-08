//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"encoding/json"
	"sort"
)

// installationSchema is the installation document: what an estate knows about
// one installation of sluis, from which `sluisctl render` and the Pulumi
// library write the service document and the policy document.
//
// It is a superset of the two. Every section that a document of its own
// would hold is here under the key that document gives it, with the schema
// that document gives it, so that what is learned reading one is true of the
// other. What an installation adds is what a document cannot say: the shape it
// runs in, the AWS resources and the OpenBao it is built on, from which the
// adapters and the library-owned names follow, and the one `exchange`,
// `controllers` and `access` an estate writes once and the two documents split.
func installationSchema() m {
	serve := serveSchema()
	sluis := sluisSchema()
	pol := policySchema()
	serveProps, _ := serve["properties"].(m)
	sluisProps, _ := sluis["properties"].(m)
	polProps, _ := pol["properties"].(m)

	// signingKey: as the service document's, except that the state secret is
	// named by the renderer (`issuer/state-secret`, layout v3) when it is left
	// out, so an estate need not repeat what the layout says.
	signing := clone(serveProps["signingKey"].(m))
	relaxRequired(signing, "stateSecret")

	exchange := policyExchangeSchema()
	exchangeProps, _ := exchange["properties"].(m)
	exchangeProps["audience"] = str("The audience a workload token must be minted for (the service document's `exchange.audience`). Defaults to `release`, so two issuers in one cluster cannot accept each other's proofs.")
	exchange["description"] = "How the token exchange verifies workloads and whom it trusts. `audience` is written to the service document; the clusters, the AWS accounts and the GitHub owners are the policy document's `exchange`."

	controllers := clone(sluisProps["controllers"].(m))
	cprops, _ := controllers["properties"].(m)
	github, _ := cprops["github"].(m)
	slack, _ := cprops["slack"].(m)
	githubProps, _ := github["properties"].(m)
	slackProps, _ := slack["properties"].(m)
	githubProps["enabledOrgs"] = list("The organisations the controller may CHANGE (the policy document's `controllers.github.enabledOrgs`). Each must be bound by `access.github`. The rest are derived every pass and left alone.", m{"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$"})
	slackProps["enabledWorkspaces"] = list("The workspaces the controller may CHANGE (the policy document's `controllers.slack.enabledWorkspaces`), by the key of `access.slack.workspaces`.", m{"type": "string", "pattern": "^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$"})
	controllers["description"] = "The controllers this installation runs, each in its own loop; an absent one is off. A section's own keys go to the service document's `controllers`; `enabledOrgs` and `enabledWorkspaces` go to the policy document's."

	// access: the access model's tables, which are the policy document less its
	// sections.
	tables := m{}
	for k, v := range polProps {
		switch k {
		case "apiVersion", "exchange", "apps", "controllers", "exports":
		default:
			tables[k] = v
		}
	}

	props := m{
		"apiVersion": m{"const": Group + "/installation/v1", "description": "Which version of which document this is. " + Group + "/installation/v1 is what this build reads and writes."},
		"instance":   m{"type": "string", "pattern": `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`, "not": m{"enum": []string{"private", "export"}}, "description": "The installation's name (`acme`, `prod`): lower-case letters, digits and dashes. Its SSM root is `/sluis/<instance>` (layout v3), so two installations share an account; `private` and `export` would nest under another's tree."},
		"shape":      enum("Where the installation runs: `lambda` (one AWS Lambda function), `kubernetes` (one Deployment) or `server` (one process). It fixes the paths the documents name and what is derived.", "", "lambda", "kubernetes", "server"),
		"preset":     presetSchema(),
		"release":    strDefault("The name the installation's objects carry (the service document's `release`). On Kubernetes, the release's full name.", "sluis"),
		"cluster":    str("Names the cluster the installation runs on in a ServiceAccount's subject (the service document's `cluster`)."),
		"policyFile": str("Where the service finds the policy document (the service document's `policy.file`). Default by shape: `/opt/sluis/policy.yaml` (lambda, the configuration layer), `/var/run/access-issuer/policy/policy.yaml` (kubernetes, where the chart mounts it), `/etc/sluis/policy.yaml` (server)."),
		"issuer": obj("The issuer and where a browser reaches the console.", m{
			"url":           m{"$ref": "#/$defs/url", "description": "The issuer (the service document's `issuerURL`): baked into every token and every relying party's trust."},
			"consoleURL":    m{"$ref": "#/$defs/url", "description": "Where a browser reaches the console, with its mount (`publicURL`). Default `<url>/console`."},
			"rootURL":       m{"$ref": "#/$defs/url", "description": "The host's root (`publicRootURL`). Default `url`."},
			"secureCookies": boolean("Mark session cookies Secure (`secureCookies`). Unset follows the https of the URL."),
			"groupsScoping": enum("How far per-audience `groups` scoping has been taken: `off`, `report` or `enforce` (`groupsScoping`).", "", "off", "report", "enforce"),
		}, "url"),
		"log":       serveProps["log"],
		"lifetimes": serveProps["lifetimes"],
		"freshness": serveProps["freshness"],
		"secrets":   serveProps["secrets"],
		"aws": obj("The AWS account and the resources the installation is built on. Each resource named here becomes the adapter setting that uses it (`state` the table, `blobs` the bucket, `audit` the queue), unless `adapters` names that concern.", m{
			"account":       m{"type": "string", "pattern": "^[0-9]{12}$", "description": "The 12-digit account id."},
			"region":        str("The region of the resources and of the SSM parameters. Required for shape `lambda`."),
			"functionName":  str("The function's name (shape `lambda`), which the `invoke` trigger names for a run-now. Default `sluis`; the Pulumi library supplies its own `FunctionName`."),
			"table":         str("The DynamoDB table of the `dynamodb` state adapter."),
			"bucket":        str("The S3 bucket of the `s3` blobs adapter."),
			"auditQueueURL": url("The audit ingest queue the `sqs` audit adapter sends to."),
		}),
		"openbao": obj("An OpenBao the secrets are kept in (the `openbao` secrets adapter, layout v3 under `root`); the exports go there too. Setting it chooses that adapter for `secrets` unless `adapters.secrets` names another. It is an answer to the preset's question: the preset's own answers do not ask for an OpenBao.", m{
			"address":   m{"type": "string", "pattern": `^https://[^\s/?#@]+/?$`, "description": "The server, https only and with no path."},
			"caFile":    str("The PEM bundle the server's certificate chains to."),
			"mount":     str("The KV version 2 mount. Default the adapter's."),
			"namespace": str("The OpenBao namespace."),
			"root":      str("The root of the layout inside the mount: `sluis`."),
			"auth": obj("How the service logs in: a role of the kubernetes or jwt auth method, with a token read from a file.", m{
				"method":    enum("The auth method.", "", "kubernetes", "jwt"),
				"mount":     str("The auth method's mount."),
				"role":      str("The role."),
				"tokenFile": str("The projected token."),
			}, "method", "role"),
		}, "address", "root", "auth"),
		"adapters":    adaptersSchema(),
		"signingKey":  signing,
		"recovery":    serveProps["recovery"],
		"login":       serveProps["login"],
		"console":     serveProps["console"],
		"oauthClient": serveProps["oauthClient"],
		"valkey":      serveProps["valkey"],
		"keys":        serveProps["keys"],
		"directory":   serveProps["directory"],
		"audit":       serveProps["audit"],
		"exchange":    exchange,
		"apps":        policyAppsSchema(),
		"controllers": controllers,
		"exports":     polProps["exports"],
		"access": obj("The access model's tables (docs/reference/policy.md): the groups, the clients, the resources, the GitHub and Slack bindings. They are the policy document's, unchanged.",
			tables),
	}
	return document("installation", "sluis installation",
		"What an estate knows about one installation of sluis, from which `sluisctl render` and the Pulumi library write the service document and the policy document. A superset of the two: a section is under the key the document gives it, and the installation adds the shape it runs in and the AWS and OpenBao resources it is built on."+secretsNote,
		props, []string{"apiVersion", "instance", "shape", "issuer"}, []string{"duration", "url", "secretName"},
		m{"allOf": []any{
			m{"if": m{"properties": m{"shape": m{"const": "lambda"}}}, "then": m{"required": []string{"aws"}, "properties": m{"aws": m{"required": []string{"region"}}}}},
		}})
}

// clone is a deep copy, by JSON: a schema is data.
func clone(v m) m {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out m
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

// relaxRequired removes name from every `required` list in the schema.
func relaxRequired(v any, name string) {
	switch t := v.(type) {
	case map[string]any:
		if req, ok := t["required"].([]any); ok {
			var kept []any
			for _, r := range req {
				if r != name {
					kept = append(kept, r)
				}
			}
			if len(kept) == 0 {
				delete(t, "required")
			} else {
				t["required"] = kept
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			relaxRequired(t[k], name)
		}
	case []any:
		for _, x := range t {
			relaxRequired(x, name)
		}
	}
}
