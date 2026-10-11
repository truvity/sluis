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
)

// The two places GitHub sends the browser back to, at the origin root beside a
// directory's consent callback and for the same reason: they finish a flow an
// operator started, on the bootstrap surface. They are routes of the issuer.
const (
	CallbackPath = "/connect/github/callback"
	SetupPath    = "/connect/github/setup"
)

// What a flow's signed state binds, so a state of one flow can never finish
// another's. The issuer signs them and the module reads them.
const (
	BindOrg       = "github:"
	BindCatalogue = "github-catalogue:"
	BindRunner    = "github-runner:"
	BindLink      = "github-link"
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
