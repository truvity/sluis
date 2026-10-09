package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/githubapp/catalogue"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/policy"
)

// PolicyDocument is the policy document: what an installation decides, in one
// file every process of it reads (`policy.file`).
//
// Its tables are the access model's (package policy, docs/reference/sluis/policy.md),
// unchanged; beside them are the decisions that used to be spread over the
// service documents and the files they named:
//
//	exchange     whom the token exchange trusts: the federated clusters, the
//	             AWS accounts, the GitHub owners whose CI tokens are verified
//	apps         what an operator may make: runner tiers, the GitHub and Slack
//	             App catalogues
//	controllers  what each controller may change (the dry-run gate)
//
// It is rendered, never layered at run time: `sluisctl policy render` (and the
// Pulumi library) merge a directory of layers, access documents and fragments
// into one canonical document ([Render]), and a process reads that one file.
type PolicyDocument struct {
	// APIVersion is sluis.truvity.github.io/policy/v2. A v1 policy file (`version:
	// 1`, or an access document) is converted as it is loaded.
	APIVersion string
	// Policy is the access model's tables. Its Version is always 1: the
	// document's own version is APIVersion.
	Policy policy.Policy
	// Exchange is whom the token exchange trusts.
	Exchange *PolicyExchange `yaml:"exchange,omitempty"`
	// Apps is what an operator may make on the console.
	Apps *PolicyApps `yaml:"apps,omitempty"`
	// Controllers is what each controller may change.
	Controllers *PolicyControllers `yaml:"controllers,omitempty"`
	// CloudflareGrants is who may ask for which Cloudflare preset
	// (`cloudflare.grants`); the presets are the service document's.
	CloudflareGrants *PolicyCloudflare `yaml:"cloudflare,omitempty"`
}

type (
	// PolicyExchange is whom the token exchange trusts. Nothing here is a
	// secret: every row is a name and a URL.
	PolicyExchange struct {
		// Clusters are the clusters whose ServiceAccount tokens are verified,
		// each against the key set it publishes for itself.
		Clusters []FederatedCluster `yaml:"clusters,omitempty"`
		// AWS is the AWS accounts whose roles' outbound identity tokens are
		// verified.
		AWS *AWSFederation `yaml:"aws,omitempty"`
		// GitHub is whose CI tokens are verified.
		GitHub *ExchangeGitHub `yaml:"github,omitempty"`
	}

	// FederatedCluster is one cluster's row.
	FederatedCluster struct {
		// Name is the estate's own word for the cluster; it becomes the
		// cluster in a `service_account` matcher and in a minted token's
		// subject.
		Name string `yaml:"name"`
		// Issuer is the `iss` its ServiceAccount tokens carry.
		Issuer string `yaml:"issuer"`
		// JWKSURI is where its keys are. Empty discovers it from the issuer.
		JWKSURI string `yaml:"jwksUri,omitempty"`
	}

	// AWSFederation is the AWS accounts whose roles may exchange their
	// outbound identity federation token.
	AWSFederation struct {
		// Audience the role must request from sts:GetWebIdentityToken.
		// Required whenever an account is listed: it is the trust boundary.
		Audience string `yaml:"audience,omitempty"`
		// MaxAge refuses a token whose `iat` is older. Default 5m, at most 1h.
		MaxAge *Duration `yaml:"maxAge,omitempty"`
		// Accounts are the trusted accounts.
		Accounts []AWSAccount `yaml:"accounts,omitempty"`
	}

	// AWSAccount is one account's row.
	AWSAccount struct {
		Account string   `yaml:"account"`
		Name    string   `yaml:"name"`
		Issuer  string   `yaml:"issuer"`
		JWKSURI string   `yaml:"jwksUri,omitempty"`
		OrgID   string   `yaml:"orgId,omitempty"`
		Algs    []string `yaml:"algs,omitempty"`
	}

	// ExchangeGitHub is whose GitHub Actions tokens are verified.
	ExchangeGitHub struct {
		// Owners are the organisations whose repositories' CI tokens are
		// verified. None verifies none: an empty list would admit every
		// repository there is.
		Owners []string `yaml:"owners,omitempty"`
	}

	// PolicyApps is what an operator may make on the console.
	PolicyApps struct {
		GitHub *AppsGitHub `yaml:"github,omitempty"`
		Slack  *AppsSlack  `yaml:"slack,omitempty"`
	}

	// AppsGitHub is the GitHub Apps an operator may make.
	AppsGitHub struct {
		// RunnerTiers are the tiers an operator may create a runner App for.
		RunnerTiers []string `yaml:"runnerTiers,omitempty"`
		// Catalogue is every catalogue App, created and installed from the
		// console.
		Catalogue []catalogue.App `yaml:"catalogue,omitempty"`
	}

	// AppsSlack is the Slack Apps an operator may make.
	AppsSlack struct {
		// Catalogue is every catalogue Slack App.
		Catalogue []slackcatalogue.App `yaml:"catalogue,omitempty"`
	}

	// PolicyControllers is what each controller may change. Everything else
	// the policy binds is derived and reported, and left alone.
	PolicyControllers struct {
		GitHub *ControllersGitHub `yaml:"github,omitempty"`
		Slack  *ControllersSlack  `yaml:"slack,omitempty"`
	}

	// ControllersGitHub is what the GitHub controller may change.
	ControllersGitHub struct {
		// EnabledOrgs are the organisations it changes. Each must be bound
		// by the policy's github table.
		EnabledOrgs []string `yaml:"enabledOrgs,omitempty"`
	}

	// ControllersSlack is what the Slack controller may change.
	ControllersSlack struct {
		// EnabledWorkspaces are the workspaces it changes, by the policy's
		// key. Each must be declared by the policy's slack table.
		EnabledWorkspaces []string `yaml:"enabledWorkspaces,omitempty"`
	}
)

// The sections the policy document adds to the access model's tables, as the
// file spells them.
var policySections = []string{"exchange", "apps", "controllers", "cloudflare"}

// policySectionsDoc is the part of a policy document beside the tables.
type policySectionsDoc struct {
	Exchange    *PolicyExchange    `yaml:"exchange,omitempty"`
	Apps        *PolicyApps        `yaml:"apps,omitempty"`
	Controllers *PolicyControllers `yaml:"controllers,omitempty"`
	Cloudflare  *PolicyCloudflare  `yaml:"cloudflare,omitempty"`
}

// NewPolicyDocument is a v2 document holding only the access model's tables.
func NewPolicyDocument(p policy.Policy) *PolicyDocument {
	p.Version = 1
	return &PolicyDocument{APIVersion: APIVersion("policy"), Policy: p}
}

// decodePolicyDocument reads a v2 document that already holds to its schema.
// The tables go through the access model's own parser (policy.Parse, strict),
// so a table means here exactly what it means in a v1 file; the sections are
// decoded strictly beside them.
func decodePolicyDocument(raw []byte) (*PolicyDocument, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("a policy document is a mapping")
	}
	top := root.Content[0]
	tables := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	sections := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	tables.Content = append(tables.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "version"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"})
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, value := top.Content[i], top.Content[i+1]
		switch {
		case key.Value == "apiVersion":
		case slices.Contains(policySections, key.Value):
			sections.Content = append(sections.Content, key, value)
		default:
			tables.Content = append(tables.Content, key, value)
		}
	}
	tablesRaw, err := yaml.Marshal(tables)
	if err != nil {
		return nil, err
	}
	p, err := policy.Parse(tablesRaw)
	if err != nil {
		return nil, err
	}
	sectionsRaw, err := yaml.Marshal(sections)
	if err != nil {
		return nil, err
	}
	var s policySectionsDoc
	dec := yaml.NewDecoder(bytes.NewReader(sectionsRaw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse the policy document: %w", err)
	}
	return &PolicyDocument{
		APIVersion: APIVersion("policy"), Policy: p,
		Exchange: s.Exchange, Apps: s.Apps, Controllers: s.Controllers, CloudflareGrants: s.Cloudflare,
	}, nil
}

// UnmarshalJSON reads a document already held to its schema (JSON is YAML):
// what the versioned loader decodes into.
func (d *PolicyDocument) UnmarshalJSON(raw []byte) error {
	out, err := decodePolicyDocument(raw)
	if err != nil {
		return err
	}
	*d = *out
	return nil
}

// MarshalYAML writes the canonical document: apiVersion first, the tables as
// the access model writes them, then the sections. Map keys are sorted, so two
// renders of one policy are the same bytes.
func (d PolicyDocument) MarshalYAML() (any, error) {
	p := d.Policy
	p.Version = 1
	var tables yaml.Node
	raw, err := yaml.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err = yaml.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	out.Content = append(out.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "apiVersion"}, &yaml.Node{Kind: yaml.ScalarNode, Value: APIVersion("policy")})
	if len(tables.Content) > 0 {
		m := tables.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "version" {
				continue
			}
			out.Content = append(out.Content, m.Content[i], m.Content[i+1])
		}
	}
	var sections yaml.Node
	if err = sections.Encode(policySectionsDoc{Exchange: d.Exchange, Apps: d.Apps, Controllers: d.Controllers, Cloudflare: d.CloudflareGrants}); err != nil {
		return nil, err
	}
	out.Content = append(out.Content, sections.Content...)
	return out, nil
}

// Encode is the canonical document as YAML.
func (d *PolicyDocument) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Clusters are the federated clusters, or none.
func (d *PolicyDocument) Clusters() []FederatedCluster {
	if d == nil || d.Exchange == nil {
		return nil
	}
	return d.Exchange.Clusters
}

// AWS is the AWS federation, empty when none is declared.
func (d *PolicyDocument) AWS() AWSFederation {
	if d == nil || d.Exchange == nil || d.Exchange.AWS == nil {
		return AWSFederation{}
	}
	return *d.Exchange.AWS
}

// GitHubOwners are the organisations whose CI tokens are verified.
func (d *PolicyDocument) GitHubOwners() []string {
	if d == nil || d.Exchange == nil || d.Exchange.GitHub == nil {
		return nil
	}
	return d.Exchange.GitHub.Owners
}

// RunnerTiers are the tiers an operator may create a runner App for.
func (d *PolicyDocument) RunnerTiers() []string {
	if d == nil || d.Apps == nil || d.Apps.GitHub == nil {
		return nil
	}
	return d.Apps.GitHub.RunnerTiers
}

// GitHubCatalogue is the GitHub App catalogue, never nil.
func (d *PolicyDocument) GitHubCatalogue() *catalogue.Catalogue {
	if d == nil || d.Apps == nil || d.Apps.GitHub == nil {
		return &catalogue.Catalogue{}
	}
	return &catalogue.Catalogue{Apps: d.Apps.GitHub.Catalogue}
}

// SlackCatalogue is the Slack App catalogue, never nil.
func (d *PolicyDocument) SlackCatalogue() *slackcatalogue.Catalogue {
	if d == nil || d.Apps == nil || d.Apps.Slack == nil {
		return &slackcatalogue.Catalogue{}
	}
	return &slackcatalogue.Catalogue{Apps: d.Apps.Slack.Catalogue}
}

// EnabledOrgs are the organisations the GitHub controller changes.
func (d *PolicyDocument) EnabledOrgs() []string {
	if d == nil || d.Controllers == nil || d.Controllers.GitHub == nil {
		return nil
	}
	return d.Controllers.GitHub.EnabledOrgs
}

// EnabledWorkspaces are the workspaces the Slack controller changes.
func (d *PolicyDocument) EnabledWorkspaces() []string {
	if d == nil || d.Controllers == nil || d.Controllers.Slack == nil {
		return nil
	}
	return d.Controllers.Slack.EnabledWorkspaces
}

// The algorithms an AWS account's outbound identity tokens are signed with.
const (
	AWSAlgES384 = "ES384"
	AWSAlgRS256 = "RS256"
	// MaxAWSMaxAge is the longest exchange.aws.maxAge may be: AWS lets a
	// token live an hour.
	MaxAWSMaxAge = time.Hour
)

// Validate holds the document to what a schema cannot say: the access model's
// own rules, and every reference from a section to the tables or to another
// section. Everything is checked before a process starts, and the checks are
// the same wherever the document is read: the binary, `sluisctl policy
// render`, and the Pulumi library before it publishes one.
func (d *PolicyDocument) Validate() error {
	if d.APIVersion != APIVersion("policy") {
		return fmt.Errorf("apiVersion %q: this build writes %s", d.APIVersion, APIVersion("policy"))
	}
	d.Policy.Version = 1
	var errs []error
	if err := d.Policy.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := validateClusters(d.Clusters()); err != nil {
		errs = append(errs, fmt.Errorf("exchange.clusters: %w", err))
	}
	if d.Exchange != nil && d.Exchange.AWS != nil {
		if err := d.Exchange.AWS.validate(); err != nil {
			errs = append(errs, fmt.Errorf("exchange.aws: %w", err))
		}
	}
	for i, owner := range d.GitHubOwners() {
		if strings.TrimSpace(owner) == "" {
			errs = append(errs, fmt.Errorf("exchange.github.owners[%d] is empty: an empty owner would admit every repository there is", i))
		}
	}
	errs = append(errs, d.validateApps()...)
	errs = append(errs, d.validateControllers()...)
	errs = append(errs, d.CloudflareGrants.validate(func(g string) bool { _, ok := d.Policy.Groups[g]; return ok })...)
	return errors.Join(errs...)
}

func (d *PolicyDocument) validateApps() []error {
	var errs []error
	tiers := map[string]bool{}
	for _, tier := range d.RunnerTiers() {
		if tiers[tier] {
			errs = append(errs, fmt.Errorf("apps.github.runnerTiers: %q is listed twice", tier))
		}
		tiers[tier] = true
	}
	github := d.GitHubCatalogue()
	if err := github.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("apps.github.catalogue: %w", err))
	}
	// A grant naming a group the policy does not declare would read as though
	// somebody may ask for a token, and nobody could.
	if undeclared := github.UndeclaredGroups(func(g string) bool { _, ok := d.Policy.Groups[g]; return ok }); len(undeclared) > 0 {
		errs = append(errs, fmt.Errorf("apps.github.catalogue: grants name groups the policy does not declare: %s",
			strings.Join(undeclared, "; ")))
	}
	slack := d.SlackCatalogue()
	if err := slack.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("apps.slack.catalogue: %w", err))
	}
	// An App declared for a workspace the policy does not name could never be
	// installed: there is no connected workspace to install it into.
	if err := slack.CheckWorkspaces(func(key string) bool { _, ok := d.Policy.Slack.Workspaces[key]; return ok }); err != nil {
		errs = append(errs, fmt.Errorf("apps.slack.catalogue: %w", err))
	}
	return errs
}

func (d *PolicyDocument) validateControllers() []error {
	var errs []error
	for _, org := range d.EnabledOrgs() {
		if _, bound := d.Policy.GitHub[org]; !bound {
			errs = append(errs, fmt.Errorf("controllers.github.enabledOrgs names %s, which the policy's github table does not bind", org))
		}
	}
	for _, ws := range d.EnabledWorkspaces() {
		if _, declared := d.Policy.Slack.Workspaces[ws]; !declared {
			errs = append(errs, fmt.Errorf("controllers.slack.enabledWorkspaces names %s, which the policy's slack table does not declare", ws))
		}
	}
	return errs
}

// validateClusters refuses a row that would verify nothing, and two rows for
// one issuer: the first would answer for every token of it, so a rule written
// against the second's name would grant nothing with no reason visible.
func validateClusters(rows []FederatedCluster) error {
	seen := map[string]string{}
	for i, row := range rows {
		switch {
		case strings.TrimSpace(row.Name) == "":
			return fmt.Errorf("cluster %d names no cluster", i+1)
		case strings.TrimSpace(row.Issuer) == "":
			return fmt.Errorf("cluster %q names no issuer", row.Name)
		}
		if other, clash := seen[row.Issuer]; clash {
			return fmt.Errorf("%q and %q both claim the issuer %s", other, row.Name, row.Issuer)
		}
		seen[row.Issuer] = row.Name
	}
	return nil
}

func (f *AWSFederation) validate() error {
	f.Audience = strings.TrimSpace(f.Audience)
	if len(f.Accounts) == 0 {
		return nil
	}
	if f.Audience == "" {
		return errors.New("an audience is required: without one every token any AWS account mints would be a proof")
	}
	if f.MaxAge != nil && (f.MaxAge.D() < 0 || f.MaxAge.D() > MaxAWSMaxAge) {
		return fmt.Errorf("maxAge %s is outside 0..%s", f.MaxAge.D(), MaxAWSMaxAge)
	}
	accounts, issuers, names := map[string]bool{}, map[string]string{}, map[string]bool{}
	for i := range f.Accounts {
		row := &f.Accounts[i]
		row.Account, row.Name = strings.TrimSpace(row.Account), strings.TrimSpace(row.Name)
		row.Issuer = strings.TrimSuffix(strings.TrimSpace(row.Issuer), "/")
		row.JWKSURI, row.OrgID = strings.TrimSpace(row.JWKSURI), strings.TrimSpace(row.OrgID)
		switch {
		case !policy.ValidAWSAccount(row.Account):
			return fmt.Errorf("account %d: %q is not a 12-digit AWS account id", i+1, row.Account)
		case row.Name == "":
			return fmt.Errorf("account %s names no name", row.Account)
		case accounts[row.Account]:
			return fmt.Errorf("account %s is listed twice", row.Account)
		case names[row.Name]:
			return fmt.Errorf("the name %q is used by two accounts", row.Name)
		}
		if err := requireHTTPS("issuer", row.Issuer); err != nil {
			return fmt.Errorf("account %s: %w", row.Account, err)
		}
		if row.JWKSURI != "" {
			if err := requireHTTPS("jwksUri", row.JWKSURI); err != nil {
				return fmt.Errorf("account %s: %w", row.Account, err)
			}
		}
		if other, clash := issuers[row.Issuer]; clash {
			return fmt.Errorf("accounts %s and %s both claim the issuer %s", other, row.Account, row.Issuer)
		}
		for _, alg := range row.Algs {
			if alg != AWSAlgES384 && alg != AWSAlgRS256 {
				return fmt.Errorf("account %s: algorithm %q is not one AWS signs with (%s, %s)",
					row.Account, alg, AWSAlgES384, AWSAlgRS256)
			}
		}
		accounts[row.Account], names[row.Name], issuers[row.Issuer] = true, true, row.Account
	}
	return nil
}

func requireHTTPS(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s %q is not an https URL", field, raw)
	}
	return nil
}

// Unconsumed is the access model's lint, with the GitHub catalogue's grants
// counted as consumers.
func (d *PolicyDocument) Unconsumed() []string {
	return d.Policy.Unconsumed(d.GitHubCatalogue().GrantGroups()...)
}
