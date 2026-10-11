package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
)

// explainLivePath is the issuer's operator endpoint for the live form.
const explainLivePath = "/.access/policy/explain"

const explainUsage = `Usage: sluisctl explain --policy <policy.yaml> --audience <client>
                        (--email <addr> | --subject <workload subject>)
                        [--directory-group <email>]... [--format yaml|json]
       sluisctl explain --live [--issuer <url>] [--client <id>] --audience <client>
                        (--email <addr> | --subject <workload subject>)
                        [--format yaml|json]

Says what a token for one client would carry for one caller: the groups kept,
the groups dropped with the reason, the claim fragments of every held group,
how long the token would live and which of the client's required groups admits
the caller. Secrets and registered claims (iss, sub, aud, exp...) are never
printed, and no token is minted.

Offline (--policy) it loads a rendered policy document the way the issuer does
and needs no session or network. --email is a person; give the directory groups
the directory would report for them with --directory-group (an address each,
repeatable).

Live (--live) it asks the issuer, as you, against the policy in force and the
directory snapshots the issuer already holds; it never makes the issuer read the
directory. It needs your sluisctl login to be an operator's, and the ask is
audited. --directory-group does not apply: the directory facts are the issuer's.
The output has the same shape as offline.

--subject is a workload: a ServiceAccount (<cluster>:k8s:<namespace>:<name>) or
an AWS role (aws:<account>:role/<path><name>). An audience that is not a
declared client is refused.
`

// multiFlag collects a repeatable flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// explainCommand is `sluisctl explain`: what a token for an audience would
// carry for a caller, read off a rendered policy document.
func explainCommand(args []string) error {
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(os.Stderr, explainUsage) }
	file := flags.String("policy", "", "the rendered policy document")
	live := flags.Bool("live", false, "ask the issuer, against its live policy and directory snapshots")
	issuer := flags.String("issuer", "", "the issuer, with --live, when not configured")
	client := flags.String("client", "", "the client to present, with --live")
	audience := flags.String("audience", "", "the client the token is for")
	email := flags.String("email", "", "a person's address")
	subject := flags.String("subject", "", "a workload's subject")
	format := flags.String("format", "yaml", "yaml or json")
	var groups multiFlag
	flags.Var(&groups, "directory-group", "a directory group the person is in (repeatable)")
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}
	switch {
	case flags.NArg() != 0:
		return badUsage("sluisctl explain takes flags only")
	case *live && *file != "":
		return badUsage("--policy and --live are exclusive: offline reads a policy file, live asks the issuer")
	case !*live && *file == "":
		return badUsage("sluisctl explain needs --policy (offline) or --live")
	case !*live && (*issuer != "" || *client != ""):
		return badUsage("--issuer and --client go with --live")
	case *live && len(groups) > 0:
		return badUsage("--directory-group goes with --policy: live uses the issuer's own directory facts")
	case *audience == "":
		return badUsage("sluisctl explain needs --audience")
	case (*email == "") == (*subject == ""):
		return badUsage("sluisctl explain needs exactly one of --email and --subject")
	case *format != "yaml" && *format != "json":
		return badUsage("--format is yaml or json")
	case *subject != "" && len(groups) > 0:
		return badUsage("--directory-group goes with --email")
	}
	if *live {
		return explainLive(*issuer, *client, *audience, strings.TrimSpace(*email), strings.TrimSpace(*subject), *format)
	}
	var in policy.Input
	if *email != "" {
		in = policy.ExplainInput(strings.TrimSpace(*email), groups)
	} else {
		var ok bool
		if in, ok = policy.WorkloadInput(strings.TrimSpace(*subject)); !ok {
			return badUsage("--subject %q is neither a ServiceAccount nor an AWS role subject", *subject)
		}
	}
	doc, err := config.Load[config.PolicyDocument](*file)
	if err != nil {
		return fmt.Errorf("load %s: %w", *file, err)
	}
	explained, err := doc.Policy.Explain(*audience, in)
	if err != nil {
		return badUsage("%v", err)
	}
	return writeExplanation(explained, *format)
}

// explainLive asks the issuer, as the signed-in person, what a token for
// audience would carry for an address or a workload.
func explainLive(issuer, client, audience, email, subject, format string) error {
	body := map[string]string{"audience": audience}
	if email != "" {
		body["email"] = email
	} else {
		body["subject"] = subject
	}
	var explained policy.Explanation
	if err := operatorCall(issuer, client, explainLivePath, body, &explained); err != nil {
		return err
	}
	return writeExplanation(explained, format)
}

func writeExplanation(e policy.Explanation, format string) error {
	if format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(e)
	}
	enc := yaml.NewEncoder(stdout)
	enc.SetIndent(2)
	if err := enc.Encode(e); err != nil {
		return err
	}
	return enc.Close()
}
