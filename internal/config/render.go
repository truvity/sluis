package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/truvity/sluis/policy"
)

// Render builds the one canonical policy document from the layers a deployment
// declares, and holds it to every check the binary would: it is what
// `sluisctl policy render` and the Pulumi library call, and the only place
// layering happens. A process loads the document a render wrote, and nothing
// else.
//
// path is a file or a directory. Every YAML file in a directory is one layer,
// read in name order, and each is one of:
//
//   - a policy file of v1 (`version: 1`): tables;
//   - an access document (`access:` and `overlay:`), reshaped into tables;
//   - a policy document fragment (`apiVersion: sluis.truvity.github.io/policy/v2`): tables and any of the
//     sections, `exchange`, `apps`, `controllers`, `cloudflare`.
//
// Tables merge by key and a key declared twice is an error naming the file, as
// they always have. Of the sections, a list concatenates and a list of names
// unions (the checks refuse a name or an id twice); a scalar declared by two
// layers is an error.
func Render(path string) (*PolicyDocument, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("render the policy: %w", err)
	}
	files := []string{path}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("render the policy: %w", err)
		}
		files = files[:0]
		for _, e := range entries {
			if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("render the policy: %s holds no yaml", path)
		}
	}
	out := NewPolicyDocument(policy.Policy{})
	for _, file := range files {
		var layer PolicyDocument
		if err := parsePolicyDocument(file, &layer); err != nil {
			return nil, err
		}
		if err := out.merge(&layer, filepath.Base(file)); err != nil {
			return nil, err
		}
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// merge folds one layer into d.
func (d *PolicyDocument) merge(layer *PolicyDocument, from string) error {
	if err := d.Policy.Merge(layer.Policy, from); err != nil {
		return err
	}
	if x := layer.Exchange; x != nil {
		if d.Exchange == nil {
			d.Exchange = &PolicyExchange{}
		}
		d.Exchange.Clusters = append(d.Exchange.Clusters, x.Clusters...)
		if x.AWS != nil {
			if d.Exchange.AWS == nil {
				d.Exchange.AWS = &AWSFederation{}
			}
			a := d.Exchange.AWS
			if x.AWS.Audience != "" {
				if a.Audience != "" {
					return fmt.Errorf("%s: exchange.aws.audience is declared twice across merged files", from)
				}
				a.Audience = x.AWS.Audience
			}
			if x.AWS.MaxAge != nil {
				if a.MaxAge != nil {
					return fmt.Errorf("%s: exchange.aws.maxAge is declared twice across merged files", from)
				}
				a.MaxAge = x.AWS.MaxAge
			}
			a.Accounts = append(a.Accounts, x.AWS.Accounts...)
		}
		if x.GitHub != nil {
			if d.Exchange.GitHub == nil {
				d.Exchange.GitHub = &ExchangeGitHub{}
			}
			d.Exchange.GitHub.Owners = union(d.Exchange.GitHub.Owners, x.GitHub.Owners)
		}
		for i, row := range d.Exchange.Clusters {
			for _, other := range d.Exchange.Clusters[:i] {
				if other.Name == row.Name {
					return fmt.Errorf("%s: exchange.clusters: %q is declared twice across merged files", from, row.Name)
				}
			}
		}
	}
	if a := layer.Apps; a != nil {
		if d.Apps == nil {
			d.Apps = &PolicyApps{}
		}
		if a.GitHub != nil {
			if d.Apps.GitHub == nil {
				d.Apps.GitHub = &AppsGitHub{}
			}
			d.Apps.GitHub.Apps = append(d.Apps.GitHub.Apps, a.GitHub.Apps...)
			d.Apps.GitHub.legacy += a.GitHub.legacy
		}
		if a.Slack != nil {
			if d.Apps.Slack == nil {
				d.Apps.Slack = &AppsSlack{}
			}
			d.Apps.Slack.Catalogue = append(d.Apps.Slack.Catalogue, a.Slack.Catalogue...)
		}
	}
	if c := layer.Controllers; c != nil {
		if d.Controllers == nil {
			d.Controllers = &PolicyControllers{}
		}
		if c.GitHub != nil {
			if d.Controllers.GitHub == nil {
				d.Controllers.GitHub = &ControllersGitHub{}
			}
			d.Controllers.GitHub.EnabledOrgs = union(d.Controllers.GitHub.EnabledOrgs, c.GitHub.EnabledOrgs)
			for org, ref := range c.GitHub.AppRefs {
				if other, clash := d.Controllers.GitHub.AppRefs[org]; clash && other != ref {
					return fmt.Errorf("%s: controllers.github.appRefs: %s is declared twice across merged files", from, org)
				}
				if d.Controllers.GitHub.AppRefs == nil {
					d.Controllers.GitHub.AppRefs = map[string]string{}
				}
				d.Controllers.GitHub.AppRefs[org] = ref
			}
		}
		if c.Slack != nil {
			if d.Controllers.Slack == nil {
				d.Controllers.Slack = &ControllersSlack{}
			}
			d.Controllers.Slack.EnabledWorkspaces = union(d.Controllers.Slack.EnabledWorkspaces, c.Slack.EnabledWorkspaces)
		}
	}
	if c := layer.CloudflareGrants; c != nil {
		if d.CloudflareGrants == nil {
			d.CloudflareGrants = &PolicyCloudflare{}
		}
		d.CloudflareGrants.Grants = append(d.CloudflareGrants.Grants, c.Grants...)
	}
	return nil
}

func union(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}
