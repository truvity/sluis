package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/truvity/sluis/policy"
)

// Bootstrap is what an installation adds to the access model's derived part:
// how it is reached before a directory can vouch for anybody. Every row is
// typed and checked by [FromAccess]; a nil row adds nothing.
//
// The rows fold into the groups the way an estate's own renderer has done it:
// matchers are appended after what the access matrix derived, in the order
// the fields below list them, and a group the matrix never produced is
// created with only these.
type Bootstrap struct {
	// Recovery is the account that holds the break-glass groups.
	Recovery *BootstrapRecovery
	// Viewers is the group of everyone who may look, and the people and
	// workloads it admits beside the matrix's own.
	Viewers *BootstrapViewers
	// Controllers are the workloads that run the directory controllers.
	Controllers *BootstrapControllers
	// Probe is the workload that proves the exchange.
	Probe *BootstrapProbe
	// ExchangeLimits narrow, per workload, which clients it may exchange a
	// proof for: the policy's `exchange_limits`, one limit per workload of
	// each row, in the order listed. A workload's groups still decide
	// whether a client admits it; a limit only takes clients away. See
	// [policy.ExchangeLimit].
	ExchangeLimits []BootstrapExchangeLimit
	// GenerateSecrets writes every confidential client's secret as
	// `{generate: true}` and not as the name the access document gives. The
	// issuer then makes and keeps the value.
	GenerateSecrets bool
	// Claims is the claims table: what a group adds to a token. The access
	// document has no such table, so it is carried through unchanged.
	Claims map[string]policy.Fragment
}

// BootstrapServiceAccount is one Kubernetes ServiceAccount.
type BootstrapServiceAccount struct {
	// Cluster narrows the account to one named cluster. Empty matches any.
	Cluster string
	// Namespace and Name are the account's, both required.
	Namespace string
	Name      string
}

// BootstrapRecovery admits one ServiceAccount to each of the named groups. Where
// recovery is the password (Lambda) the row is left out.
type BootstrapRecovery struct {
	ServiceAccount BootstrapServiceAccount
	// Groups are the groups the account joins, for example the operator
	// group and one cluster's auditor group. They may be groups of the
	// access matrix.
	Groups []string
}

// BootstrapViewers is the group that may look at the installation.
type BootstrapViewers struct {
	// Group is the group's name. It may be a group of the access matrix.
	Group string
	// EmailDomain admits every signed-in address of one domain.
	EmailDomain string
	// ServiceAccounts and RoleARNs are the workloads admitted besides the
	// controllers, which join the group on their own row.
	ServiceAccounts []BootstrapServiceAccount
	// RoleARNs are IAM roles, `arn:aws:iam::<account>:role/[path/]<name>`.
	RoleARNs []string
}

// BootstrapControllers are the workloads of the GitHub and Slack controllers. They
// join the viewers, and, when Group is set, a group of their own that the
// console reads to serve them unrecorded and that grants no role.
type BootstrapControllers struct {
	// Group is that group's name. It must not be a group of the access
	// matrix: a person placed there would be served as a controller.
	Group string
	// ServiceAccounts is the release's ServiceAccount on Kubernetes.
	ServiceAccounts []BootstrapServiceAccount
	// RoleARNs are the functions' roles on Lambda.
	RoleARNs []string
}

// BootstrapProbe is the workload that proves a token minted by another cluster is
// verified against the key set that cluster publishes. Its client is an
// exchange target that grants nothing.
type BootstrapProbe struct {
	// Group is the group the workload joins. It must not be a group of the
	// access matrix.
	Group string
	// Client is the exchange client, which requires Group. It must not be a
	// client of the access matrix.
	Client string
	// ServiceAccount is the probe's account. Its cluster is required.
	ServiceAccount BootstrapServiceAccount
}

// BootstrapExchangeLimit limits a set of workloads to a set of clients: each
// of them may exchange its proof for these clients and no other. A workload
// named on several rows may be exchanged for every client those rows name.
//
// It is how an estate keeps its own functions, admitted to a group many
// clients require, from exchanging for any of them but the one each needs:
// the limit names the function's role, not a group, so it holds whatever
// groups that role is put in later.
type BootstrapExchangeLimit struct {
	// ServiceAccounts are Kubernetes ServiceAccounts.
	ServiceAccounts []BootstrapServiceAccount
	// RoleARNs are IAM roles, `arn:aws:iam::<account>:role/[path/]<name>`:
	// each is matched by its account, its name and its path, exactly.
	RoleARNs []string
	// Clients are the client ids they may be exchanged for, at least one.
	// [Render] refuses one the policy does not declare.
	Clients []string
}

var (
	// dnsLabel is a Kubernetes namespace; dnsName is a name or a cluster.
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dnsName  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// emailDomain is a host name with at least one dot-free label.
	emailDomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	account     = regexp.MustCompile(`^[0-9]{12}$`)
	// roleARN splits `arn:<partition>:iam::<account>:role/[path/]<name>`.
	arnPrefix = "arn:" + "aws[a-z-]*:"
	roleARN   = regexp.MustCompile(`^` + arnPrefix + `iam::([0-9]{12}):role(/(?:[^/\s]+/)*)([^/\s]+)$`)
)

// FromAccess builds the installation's `access` section from an access
// document's derived part and the installation's bootstrap rows. It is the
// reshaping [policy.AccessDocument.Policy] does (lists to tables, camelCase
// to the policy's own keys) with the rows appended to their groups, and
// nothing else: no field of the access part is dropped, except the seven of a
// client that describe the relying party's own deployment and are read by
// whatever deploys it ([policy.AccessClient]).
//
// The result is not validated as a policy; [Render] does that. FromAccess
// refuses a Bootstrap that is malformed, names a workload twice in one list,
// uses a group or client of its own that the access part also declares, or
// uses one name for two rows.
func FromAccess(access policy.Access, b Bootstrap) (Access, error) {
	overlay, err := b.overlay(access)
	if err != nil {
		return Access{}, err
	}
	pol, err := policy.AccessDocument{Version: 1, Access: access, Overlay: overlay}.Policy()
	if err != nil {
		return Access{}, err
	}
	if b.GenerateSecrets {
		for id := range pol.Clients {
			c := pol.Clients[id]
			if c.Kind == "confidential" {
				c.Secret = policy.ClientSecret{Generate: true}
				pol.Clients[id] = c
			}
		}
	}
	if len(b.Claims) > 0 {
		pol.Claims = maps.Clone(b.Claims)
	}
	if pol.ExchangeLimits, err = b.exchangeLimits(); err != nil {
		return Access{}, err
	}
	return Access{Policy: pol}, nil
}

// exchangeLimits checks the exchange-limit rows and writes them as the
// policy's limits: one per workload, with that row's clients.
func (b Bootstrap) exchangeLimits() ([]policy.ExchangeLimit, error) {
	var out []policy.ExchangeLimit
	for i, row := range b.ExchangeLimits {
		name := fmt.Sprintf("exchange limit %d", i+1)
		if len(row.ServiceAccounts) == 0 && len(row.RoleARNs) == 0 {
			return nil, fmt.Errorf("%s: no workload to limit", name)
		}
		if len(row.Clients) == 0 {
			return nil, fmt.Errorf("%s: no clients: a limit names the clients its workloads may be exchanged for", name)
		}
		clients := make([]string, 0, len(row.Clients))
		for _, id := range row.Clients {
			if id == "" || strings.TrimSpace(id) != id {
				return nil, fmt.Errorf("%s: client %q is not a client id", name, id)
			}
			if slices.Contains(clients, id) {
				return nil, fmt.Errorf("%s: client %q is listed twice", name, id)
			}
			clients = append(clients, id)
		}
		seen := map[string]bool{}
		for _, sa := range row.ServiceAccounts {
			if err := sa.check(name, false); err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s/%s/%s", sa.Cluster, sa.Namespace, sa.Name)
			if seen[key] {
				return nil, fmt.Errorf("%s: service account %s/%s is listed twice", name, sa.Namespace, sa.Name)
			}
			seen[key] = true
			out = append(out, policy.ExchangeLimit{Match: sa.matcher(), Clients: slices.Clone(clients)})
		}
		for _, arn := range row.RoleARNs {
			m, err := parseRoleARN(arn)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if seen["role/"+arn] {
				return nil, fmt.Errorf("%s: role %s is listed twice", name, arn)
			}
			seen["role/"+arn] = true
			out = append(out, policy.ExchangeLimit{Match: m, Clients: slices.Clone(clients)})
		}
	}
	return out, nil
}

// overlay validates the rows and writes them as the overlay of an access
// document, in the order the matchers are appended.
func (b Bootstrap) overlay(access policy.Access) (policy.AccessOverlay, error) {
	derived := map[string]bool{}
	for i := range access.Groups {
		derived[access.Groups[i].Name] = true
	}
	matchers := map[string][]policy.Matcher{}
	// owned are the groups only a workload may be in, and who owns each.
	owned := map[string]string{}
	add := func(group string, m ...policy.Matcher) { matchers[group] = append(matchers[group], m...) }
	own := func(group, row string) error {
		if err := checkGroup(group); err != nil {
			return fmt.Errorf("%s: %w", row, err)
		}
		if derived[group] {
			return fmt.Errorf("%s: group %q is also a group of the access document, which would put its people among the workloads", row, group)
		}
		if other, dup := owned[group]; dup {
			return fmt.Errorf("%s: group %q is also the %s group", row, group, other)
		}
		if group == viewerGroupName(b) || slices.Contains(recoveryGroups(b), group) {
			return fmt.Errorf("%s: group %q is also a group of the viewers or of recovery", row, group)
		}
		owned[group] = row
		return nil
	}

	if r := b.Recovery; r != nil {
		if err := r.ServiceAccount.check("recovery", false); err != nil {
			return policy.AccessOverlay{}, err
		}
		if len(r.Groups) == 0 {
			return policy.AccessOverlay{}, errors.New("recovery: no groups")
		}
		seen := map[string]bool{}
		for _, g := range r.Groups {
			if err := checkGroup(g); err != nil {
				return policy.AccessOverlay{}, fmt.Errorf("recovery: %w", err)
			}
			if seen[g] {
				return policy.AccessOverlay{}, fmt.Errorf("recovery: group %q is listed twice", g)
			}
			seen[g] = true
			add(g, r.ServiceAccount.matcher())
		}
	}

	var viewerGroup string
	if v := b.Viewers; v != nil {
		viewerGroup = v.Group
		if err := checkGroup(v.Group); err != nil {
			return policy.AccessOverlay{}, fmt.Errorf("viewers: %w", err)
		}
		if v.EmailDomain != "" {
			if !emailDomain.MatchString(v.EmailDomain) {
				return policy.AccessOverlay{}, fmt.Errorf("viewers: email domain %q is not a domain", v.EmailDomain)
			}
			add(v.Group, policy.Matcher{EmailDomain: v.EmailDomain})
		}
	}

	// A workload is named once per list. One named on both rows joins the
	// viewers once, where the viewers row put it.
	seen := map[string]string{}
	admit := func(row string, sas []BootstrapServiceAccount, arns []string) (viewer, all []policy.Matcher, err error) {
		note := func(key, what string, m policy.Matcher) error {
			prev, dup := seen[key]
			switch {
			case dup && prev == row:
				return fmt.Errorf("%s: %s is listed twice", row, what)
			case !dup:
				seen[key] = row
				viewer = append(viewer, m)
			}
			all = append(all, m)
			return nil
		}
		for _, sa := range sas {
			if err := sa.check(row, false); err != nil {
				return nil, nil, err
			}
			key := fmt.Sprintf("sa/%s/%s/%s", sa.Cluster, sa.Namespace, sa.Name)
			if err := note(key, "service account "+sa.Namespace+"/"+sa.Name, sa.matcher()); err != nil {
				return nil, nil, err
			}
		}
		for _, arn := range arns {
			m, err := parseRoleARN(arn)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", row, err)
			}
			if err := note("role/"+arn, "role "+arn, m); err != nil {
				return nil, nil, err
			}
		}
		return viewer, all, nil
	}

	if v := b.Viewers; v != nil {
		ms, _, err := admit("viewers", v.ServiceAccounts, v.RoleARNs)
		if err != nil {
			return policy.AccessOverlay{}, err
		}
		add(v.Group, ms...)
	}
	if c := b.Controllers; c != nil {
		ms, all, err := admit("controllers", c.ServiceAccounts, c.RoleARNs)
		if err != nil {
			return policy.AccessOverlay{}, err
		}
		if viewerGroup != "" {
			add(viewerGroup, ms...)
		}
		if c.Group != "" {
			if err := own(c.Group, "controllers"); err != nil {
				return policy.AccessOverlay{}, err
			}
			if len(all) == 0 {
				return policy.AccessOverlay{}, errors.New("controllers: a group and no workload to put in it")
			}
			add(c.Group, all...)
		}
	}

	var clients map[string]policy.Client
	if p := b.Probe; p != nil {
		if err := own(p.Group, "probe"); err != nil {
			return policy.AccessOverlay{}, err
		}
		if err := p.ServiceAccount.check("probe", true); err != nil {
			return policy.AccessOverlay{}, err
		}
		if !dnsName.MatchString(p.Client) {
			return policy.AccessOverlay{}, fmt.Errorf("probe: client %q is not a name", p.Client)
		}
		for i := range access.Clients {
			if access.Clients[i].Name == p.Client {
				return policy.AccessOverlay{}, fmt.Errorf("probe: client %q is also a client of the access document", p.Client)
			}
		}
		add(p.Group, p.ServiceAccount.matcher())
		clients = map[string]policy.Client{p.Client: {Kind: "exchange", Requires: []string{p.Group}}}
	}

	out := policy.AccessOverlay{Clients: clients}
	if len(matchers) > 0 {
		out.Groups = make(map[string]policy.OverlayGroup, len(matchers))
		for g, ms := range matchers {
			out.Groups[g] = policy.OverlayGroup{Matchers: ms}
		}
	}
	return out, nil
}

func checkGroup(name string) error {
	switch {
	case name == "":
		return errors.New("a group has no name")
	case strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n"):
		return fmt.Errorf("group %q has white space", name)
	case strings.HasPrefix(name, ":") || strings.HasSuffix(name, ":") || strings.Contains(name, "::"):
		return fmt.Errorf("group %q has an empty segment", name)
	}
	return nil
}

func (s BootstrapServiceAccount) check(row string, clusterRequired bool) error {
	switch {
	case s.Namespace == "" || s.Name == "":
		return fmt.Errorf("%s: a service account needs a namespace and a name", row)
	case !dnsLabel.MatchString(s.Namespace):
		return fmt.Errorf("%s: namespace %q is not a Kubernetes namespace", row, s.Namespace)
	case !dnsName.MatchString(s.Name):
		return fmt.Errorf("%s: service account name %q is not a Kubernetes name", row, s.Name)
	case clusterRequired && s.Cluster == "":
		return fmt.Errorf("%s: the cluster is required", row)
	case s.Cluster != "" && !dnsName.MatchString(s.Cluster):
		return fmt.Errorf("%s: cluster %q is not a name", row, s.Cluster)
	}
	return nil
}

func (s BootstrapServiceAccount) matcher() policy.Matcher {
	return policy.Matcher{ServiceAccount: &policy.ServiceAccountMatcher{Cluster: s.Cluster, Namespace: s.Namespace, Name: s.Name}}
}

// parseRoleARN reads an IAM role ARN as the matcher that admits it: the
// account, the role's name and its path (`/` for none).
func parseRoleARN(arn string) (policy.Matcher, error) {
	m := roleARN.FindStringSubmatch(arn)
	if m == nil || !account.MatchString(m[1]) {
		return policy.Matcher{}, fmt.Errorf("%q is not an IAM role ARN", arn)
	}
	return policy.Matcher{AWS: &policy.AWSMatcher{Account: m[1], Role: m[3], Path: m[2]}}, nil
}

func viewerGroupName(b Bootstrap) string {
	if b.Viewers == nil {
		return ""
	}
	return b.Viewers.Group
}

func recoveryGroups(b Bootstrap) []string {
	if b.Recovery == nil {
		return nil
	}
	return b.Recovery.Groups
}
