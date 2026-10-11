// Package consolewire is what the console and the GitHub module agree on for
// the operations that moved into the module (v1.76 D-v176-1 b): the method
// names, and the values the protobuf contract has no room for, such as a
// flow's hand-over of a one-time code.
//
// It imports nothing of the module, so the console can name a method without
// importing the App code.
package consolewire

// Module is the module the methods are on.
const Module = "github"

// The module's console methods. Each answers the console class and no other.
const (
	// G1: organisation connect.
	BeginConnect            = "console.begin_connect"
	CompleteCreate          = "console.complete_create"
	CompleteInstall         = "console.complete_install"
	DisconnectOrganisation  = "console.disconnect_organisation"
	ChangeOrganisationOwner = "console.change_organisation_owner"
	RequestPass             = "console.request_pass"

	// G2: the Apps view and the webhook.
	ListApps         = "console.list_apps"
	GetApp           = "console.get_app"
	ListAppTokens    = "console.list_app_tokens"
	CheckApp         = "console.check_app"
	RotateAppWebhook = "console.rotate_app_webhook"
	// The GitHub page, and the per-kind Check that the Apps view replaced.
	GetStatus         = "console.get_status"
	CheckCatalogueApp = "console.check_catalogue_app"

	// G3: the catalogue and runner Apps.
	BeginCatalogueConnect   = "console.begin_catalogue_connect"
	CompleteCatalogueCreate = "console.complete_catalogue_create"
	CompleteCatalogueSetup  = "console.complete_catalogue_setup"
	DisconnectCatalogueApp  = "console.disconnect_catalogue_app"
	BeginRunnerConnect      = "console.begin_runner_connect"
	CompleteRunnerCreate    = "console.complete_runner_create"
	CompleteRunnerSetup     = "console.complete_runner_setup"
	DisconnectRunnerApp     = "console.disconnect_runner_app"

	// G4: the link App and people's links; and the two calls that start and
	// end any App by its id.
	BeginLinkAppConnect = "console.begin_link_app_connect"
	CompleteLinkApp     = "console.complete_link_app"
	DisconnectLinkApp   = "console.disconnect_link_app"
	LinkPageOf          = "console.link_page"
	LinkCompleteOf      = "console.link_complete"
	ConfirmRemovals     = "console.confirm_removals"
	ImportLinks         = "console.import_links"
	BeginAppConnect     = "console.begin_app_connect"
	DisconnectApp       = "console.disconnect_app"
)

// The two places GitHub sends the browser back to, at the origin root beside a
// directory's consent callback and for the same reason: they finish a flow an
// operator started, on the bootstrap surface. They are routes of the issuer.
const (
	CallbackPath = "/connect/github/callback"
	SetupPath    = "/connect/github/setup"
)

// Where GitHub sends the browser back to while a catalogue App or a runner App
// is created: after Create, and after Install.
const (
	CatalogueCallbackPath = "/connect/github/catalogue/callback"
	CatalogueSetupPath    = "/connect/github/catalogue/setup"
	RunnerCallbackPath    = "/connect/github/runner/callback"
	RunnerSetupPath       = "/connect/github/runner/setup"
)

// The link flows' paths, at the origin ROOT with the other GitHub callbacks.
// The page is there too, and not in the console: the people who link are every
// employee and every partner's engineer, and the console admits only its
// operators and viewers.
const (
	LinkAppCallbackPath = "/connect/github/link-app/callback"
	LinkPath            = "/connect/github/link"
	LinkCallbackPath    = "/connect/github/link/callback"
)

// What a flow's signed state binds, so a state of one flow can never finish
// another's. The issuer signs them and the module reads them.
const (
	BindOrg       = "github:"
	BindCatalogue = "github-catalogue:"
	BindRunner    = "github-runner:"
	BindLink      = "github-link"
	BindLinkApp   = "github-link-app:"
)

// StatePlaceholder stands in a URL the module returns for the signed state the
// issuer adds: the state is signed with the session key, which the module
// never holds. The issuer replaces it, once, with the state.
const StatePlaceholder = "sluis-state-placeholder"

// Begun is what starting a connect returns.
type Begun struct {
	// URL is where the browser goes, with [StatePlaceholder] where the state
	// belongs.
	URL string `json:"url"`
	// Manifest is the manifest the browser POSTs there when the App is still to
	// be created.
	Manifest string `json:"manifest,omitempty"`
	// Bind and Owner are what the state is signed to carry, with the caller as
	// its actor.
	Bind  string `json:"bind"`
	Owner string `json:"owner,omitempty"`
}

// BeginConnectRequest starts connecting an organisation.
type BeginConnectRequest struct {
	Org            string `json:"org"`
	OwnerDirectory string `json:"owner_directory,omitempty"`
}

// BeginCatalogueConnectRequest starts connecting a catalogue App.
type BeginCatalogueConnectRequest struct {
	ID string `json:"id"`
}

// BeginRunnerConnectRequest starts connecting a runner App.
type BeginRunnerConnectRequest struct {
	Org  string `json:"org"`
	Tier string `json:"tier"`
}

// Flow is what the issuer hands the module after it verified a provider's
// redirect: the state's binding, which the issuer signed and checked against
// the browser's cookie, and the one-time values the provider returned. The
// caller of the call is the person signed in at the callback, when there is
// one; with none, Actor is whom the state names.
type Flow struct {
	// Bind, Actor and Owner are the verified state's binding.
	Bind  string `json:"bind"`
	Actor string `json:"actor,omitempty"`
	Owner string `json:"owner,omitempty"`
	// Code is the provider's one-time code, and InstallationID the claim a
	// setup redirect carries.
	Code           string `json:"code,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
}

// Continued is what a flow's first redirect returns: the next place to send
// the browser, under a fresh state.
type Continued struct {
	URL   string `json:"url"`
	Bind  string `json:"bind"`
	Owner string `json:"owner,omitempty"`
	// Actor is whom the fresh state names.
	Actor string `json:"actor"`
}

// Done is what a flow's last redirect returns.
type Done struct{}

// BeginAppConnectRequest starts connecting any App by the id the Apps list
// gave it.
type BeginAppConnectRequest struct {
	ID             string `json:"id"`
	Owner          string `json:"owner,omitempty"`
	OwnerDirectory string `json:"owner_directory,omitempty"`
}

// BeginLinkAppConnectRequest starts creating the link App under an
// organisation.
type BeginLinkAppConnectRequest struct {
	Owner string `json:"owner"`
}

// LinkPage is what the page where a person starts linking needs: where GitHub's
// authorize page is, with [StatePlaceholder] where the state belongs.
type LinkPage struct {
	AuthorizeURL string `json:"authorize_url"`
}

// LinkComplete is what the issuer hands the module when GitHub sends a person
// back from authorizing a link: the one-time code, or GitHub's refusal. The
// issuer verified the signed state and the cookie it was pinned with.
type LinkComplete struct {
	Code                   string `json:"code,omitempty"`
	GitHubError            string `json:"github_error,omitempty"`
	GitHubErrorDescription string `json:"github_error_description,omitempty"`
}

// Linked is what finishing a link came to: the account, the addresses it was
// linked to, and a reason for each work address that was not. No Accepted is a
// link that was not made.
type Linked struct {
	Login    string   `json:"login"`
	Accepted []string `json:"accepted,omitempty"`
	Refused  []string `json:"refused,omitempty"`
}
