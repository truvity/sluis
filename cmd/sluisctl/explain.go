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

const explainUsage = `Usage: sluisctl explain --policy <policy.yaml> --audience <client>
                        (--email <addr> | --subject <workload subject>)
                        [--directory-group <email>]... [--format yaml|json]

Says what a token for one client would carry for one caller, from a rendered
policy document, offline: no session, no network. It loads the document the
way the issuer does, evaluates the caller, narrows the groups to the audience
(token scoping as enforced) and prints the groups kept, the groups dropped with
the reason, and the claim fragments the kept groups add.
Secrets and registered claims (iss, sub, aud, exp...) are never printed.

--email is a person; give the directory groups the directory would report for
them with --directory-group (an address each, repeatable). --subject is a
workload: a ServiceAccount (<cluster>:k8s:<namespace>:<name>) or an AWS role
(aws:<account>:role/<path><name>). An audience that is not a declared client is
refused.
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
	case *file == "" || *audience == "":
		return badUsage("sluisctl explain needs --policy and --audience")
	case (*email == "") == (*subject == ""):
		return badUsage("sluisctl explain needs exactly one of --email and --subject")
	case *format != "yaml" && *format != "json":
		return badUsage("--format is yaml or json")
	case *subject != "" && len(groups) > 0:
		return badUsage("--directory-group goes with --email")
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
