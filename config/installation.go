package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	sluis "github.com/truvity/sluis"
	internal "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/keys"
)

// InstallationVersion is the apiVersion of the installation document.
const InstallationVersion = Group + "/installation/v1"

// Shape is where an installation runs.
type Shape string

// The shapes.
const (
	// ShapeLambda is one AWS Lambda function behind an HTTP API; the documents
	// are an immutable configuration layer mounted at /opt/sluis.
	ShapeLambda Shape = "lambda"
	// ShapeKubernetes is one Deployment; the chart mounts the documents.
	ShapeKubernetes Shape = "kubernetes"
	// ShapeServer is one process on a host.
	ShapeServer Shape = "server"
)

// Where each shape finds the policy document, unless the installation says.
const (
	// LambdaPolicyFile is the policy in the configuration layer.
	LambdaPolicyFile = "/opt/sluis/policy.yaml"
	// KubernetesPolicyFile is where the chart mounts the policy document.
	KubernetesPolicyFile = "/var/run/access-issuer/policy/policy.yaml"
	// ServerPolicyFile is the policy of a process on a host.
	ServerPolicyFile = "/etc/sluis/policy.yaml"
)

// Installation is everything an estate knows about one installation of sluis.
// It is a superset of the service document and the policy document: a section
// is under the key the document gives it (the types are the documents' own),
// and the installation adds what a document cannot say, which is the shape it
// runs in and the AWS and OpenBao resources it is built on. [Render] splits it
// into the two documents.
//
// It holds no secret. A document names each secret, and where the names are
// read from is `secrets` or the secrets adapter.
type Installation struct {
	// APIVersion is [InstallationVersion].
	APIVersion string `json:"apiVersion"`
	// Instance is the installation's name (`acme`, `prod`). Its SSM root is
	// /sluis/<instance>.
	Instance string `json:"instance"`
	// Shape is where it runs.
	Shape Shape `json:"shape"`
	// Preset names the adapters of a whole platform. Unset follows the shape:
	// aws-hybrid (lambda), k8s-aws (kubernetes with `aws`), k8s-openbao
	// (kubernetes with `openbao`), k8s-minimal, server.
	Preset string `json:"preset,omitempty"`
	// Release is the name the installation's objects carry. Unset is `sluis`.
	Release string `json:"release,omitempty"`
	// Cluster names the cluster in a ServiceAccount's subject.
	Cluster string `json:"cluster,omitempty"`
	// PolicyFile is where the service finds the policy document. Unset follows
	// the shape.
	PolicyFile string `json:"policyFile,omitempty"`

	Issuer    Issuer     `json:"issuer"`
	Log       *Log       `json:"log,omitempty"`
	Lifetimes *Lifetimes `json:"lifetimes,omitempty"`
	Freshness *Freshness `json:"freshness,omitempty"`
	// Secrets is the service document's `secrets`, for a platform with no
	// secrets adapter of its own. Shape lambda fills it (ssm, the instance's
	// root) and refuses another value.
	Secrets *Secrets `json:"secrets,omitempty"`

	// AWS is the account and the resources the installation is built on.
	AWS *AWS `json:"aws,omitempty"`
	// OpenBao is the OpenBao the secrets and the exports are kept in.
	OpenBao *OpenBao `json:"openbao,omitempty"`
	// Adapters name single concerns over what the preset and the resources
	// above give.
	Adapters map[string]AdapterChoice `json:"adapters,omitempty"`

	// Keys says which key serves which purpose (the service document's
	// `keys`). With `signingKey.kmsWrapped` and no `keys`, the sign key is
	// alias/sluis-<instance>-sign on kms.
	Keys        *keys.Config `json:"keys,omitempty"`
	SigningKey  *SigningKey  `json:"signingKey,omitempty"`
	Recovery    *Recovery    `json:"recovery,omitempty"`
	Login       *Login       `json:"login,omitempty"`
	Console     *Console     `json:"console,omitempty"`
	OAuthClient *OAuthClient `json:"oauthClient,omitempty"`
	Valkey      *Valkey      `json:"valkey,omitempty"`
	Directory   *Directory   `json:"directory,omitempty"`
	Audit       *Audit       `json:"audit,omitempty"`

	// Exchange is how the token exchange verifies workloads and whom it trusts.
	Exchange *Exchange `json:"exchange,omitempty"`
	// Apps is what an operator may make on the console.
	Apps *Apps `json:"apps,omitempty"`
	// Controllers are the controllers that run, and what each may change.
	Controllers *Controllers `json:"controllers,omitempty"`
	// Exports are the secrets the service copies out of itself.
	Exports []Export `json:"exports,omitempty"`
	// Access is the access model's tables: groups, clients, resources and the
	// GitHub and Slack bindings.
	Access *Access `json:"access,omitempty"`
}

// Issuer is the issuer and where a browser reaches the console.
type Issuer struct {
	// URL is the issuer (`issuerURL`).
	URL string `json:"url"`
	// ConsoleURL is `publicURL`. Unset is URL plus /console.
	ConsoleURL string `json:"consoleURL,omitempty"`
	// RootURL is `publicRootURL`. Unset is URL.
	RootURL string `json:"rootURL,omitempty"`
	// SecureCookies is `secureCookies`.
	SecureCookies *bool `json:"secureCookies,omitempty"`
	// GroupsScoping is `groupsScoping`: off, report or enforce.
	GroupsScoping string `json:"groupsScoping,omitempty"`
}

// AWS is the account and the resources an installation is built on. Each
// resource becomes the setting of the adapter that uses it.
type AWS struct {
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
	// FunctionName is the Lambda function the `invoke` trigger names. Unset is
	// `sluis`.
	FunctionName string `json:"functionName,omitempty"`
	// Table is the DynamoDB table of the `dynamodb` state adapter.
	Table string `json:"table,omitempty"`
	// Bucket is the S3 bucket of the `s3` blobs adapter.
	Bucket string `json:"bucket,omitempty"`
	// AuditQueueURL is the SQS queue of the `sqs` audit adapter.
	AuditQueueURL string `json:"auditQueueURL,omitempty"`
}

// OpenBao is the OpenBao the secrets are kept in: the settings of the
// `openbao` secrets adapter.
type OpenBao struct {
	Address   string       `json:"address"`
	CAFile    string       `json:"caFile,omitempty"`
	Mount     string       `json:"mount,omitempty"`
	Namespace string       `json:"namespace,omitempty"`
	Root      string       `json:"root"`
	Auth      OpenBaoLogin `json:"auth"`
}

// OpenBaoLogin is how the service logs in to the OpenBao.
type OpenBaoLogin struct {
	Method    string `json:"method"`
	Mount     string `json:"mount,omitempty"`
	Role      string `json:"role"`
	TokenFile string `json:"tokenFile,omitempty"`
}

// Exchange is how the token exchange verifies workloads. Audience is the
// service document's; the rest is the policy document's `exchange`.
type Exchange struct {
	Audience string             `json:"audience,omitempty"`
	Clusters []FederatedCluster `json:"clusters,omitempty"`
	AWS      *FederatedAWS      `json:"aws,omitempty"`
	GitHub   *ExchangeGitHub    `json:"github,omitempty"`
}

// FederatedCluster is one cluster whose ServiceAccount tokens are verified.
type FederatedCluster struct {
	Name    string `json:"name"`
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwksUri,omitempty"`
}

// FederatedAWS is the AWS accounts whose roles' outbound identity tokens are
// verified.
type FederatedAWS struct {
	Audience string       `json:"audience,omitempty"`
	MaxAge   *Duration    `json:"maxAge,omitempty"`
	Accounts []AWSAccount `json:"accounts,omitempty"`
}

// AWSAccount is one trusted account.
type AWSAccount struct {
	Account string   `json:"account"`
	Name    string   `json:"name"`
	Issuer  string   `json:"issuer"`
	JWKSURI string   `json:"jwksUri,omitempty"`
	OrgID   string   `json:"orgId,omitempty"`
	Algs    []string `json:"algs,omitempty"`
}

// ExchangeGitHub is whose GitHub Actions tokens are verified.
type ExchangeGitHub struct {
	Owners []string `json:"owners,omitempty"`
}

// Apps is what an operator may make on the console: the policy document's
// `apps`, under the same keys.
type Apps struct{ internal.PolicyApps }

// UnmarshalJSON reads the section as the policy document does.
func (a *Apps) UnmarshalJSON(raw []byte) error { return decodeYAMLStrict(raw, &a.PolicyApps) }

// MarshalJSON writes the section under the keys the policy document gives it.
func (a Apps) MarshalJSON() ([]byte, error) { return yamlToJSON(a.PolicyApps) }

// Controllers are the controllers the installation runs. A nil one is off.
type Controllers struct {
	GitHub *GitHubController `json:"github,omitempty"`
	Slack  *SlackController  `json:"slack,omitempty"`
}

// GitHubController is the GitHub controller: its own keys go to the service
// document's `controllers.github`, EnabledOrgs to the policy document's.
type GitHubController struct {
	internal.GitHubController
	EnabledOrgs []string `json:"enabledOrgs,omitempty"`
}

// SlackController is [GitHubController] for Slack.
type SlackController struct {
	internal.SlackController
	EnabledWorkspaces []string `json:"enabledWorkspaces,omitempty"`
}

// Access is the access model's tables, as the policy document holds them.
type Access struct{ policy.Policy }

// UnmarshalJSON reads the tables as the policy document does (JSON is YAML):
// through the access model's own strict parser.
func (a *Access) UnmarshalJSON(raw []byte) error {
	var tables map[string]any
	if err := json.Unmarshal(raw, &tables); err != nil {
		return err
	}
	tables["version"] = 1
	withVersion, err := yaml.Marshal(tables)
	if err != nil {
		return err
	}
	p, err := policy.Parse(withVersion)
	if err != nil {
		return err
	}
	a.Policy = p
	return nil
}

// MarshalJSON writes the tables as the access model writes them.
func (a Access) MarshalJSON() ([]byte, error) {
	raw, err := yamlToJSON(a.Policy)
	if err != nil {
		return nil, err
	}
	var tables map[string]any
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	delete(tables, "version")
	return json.Marshal(tables)
}

func decodeYAMLStrict(raw []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(into)
}

func yamlToJSON(v any) ([]byte, error) {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	if generic == nil {
		generic = map[string]any{}
	}
	return json.Marshal(generic)
}

// InstallationSchema is the authored schema of the installation document, the
// file schemas/config/installation.schema.json: the one `sluisctl render`
// holds a document to, and the one an estate's CI can validate against without
// this package.
func InstallationSchema() []byte {
	b, err := sluis.ConfigSchemas.ReadFile(path.Join("schemas/config", "installation.schema.json"))
	if err != nil {
		// Embedded at build time: a missing file fails to compile.
		panic(err)
	}
	return b
}

// LoadInstallation reads an installation document from a file and holds it to
// its schema.
func LoadInstallation(file string) (*Installation, error) {
	var in Installation
	kind := policyconfig.Kind{Name: Group + "/installation", Version: 1, Schema: InstallationSchema()}
	if err := policyconfig.LoadKind(file, kind, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// ValidateInstallation checks a decoded installation document (a generic YAML
// or JSON value) against the schema.
func ValidateInstallation(doc any) error {
	return policyconfig.Validate(doc, InstallationSchema())
}

// String names the installation without printing any of it.
func (in *Installation) String() string {
	return fmt.Sprintf("installation %q (%s)", in.Instance, in.Shape)
}
