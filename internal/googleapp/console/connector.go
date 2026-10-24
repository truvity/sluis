// Package console is the Google module's half of the console: the Workspace
// service (the directories an operator connects), the consent and the key
// upload that connect one, and the sign-in's Identify. The console in the
// issuer authenticates a person and asks this package, as the caller class
// `console` ([consolecall]); the module holds the OAuth client secret, the
// service-account keys, the workspace records and the refresh writer, and the
// issuer holds none of them (docs/decisions/0071, v1.76 D-v176-1 b).
//
// The sign-in is the one method the issuer itself calls (`google.identify`,
// caller class `issuer`): it needs the client secret, once per login.
package console

import (
	"context"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/google"
	"github.com/truvity/sluis/internal/hub"
)

// Connector starts and finishes an admin-consent flow for one backend.
// The browser is in the middle of it, which is why the callback is a plain
// HTTP route rather than an RPC.
type Connector interface {
	// Kind names the backend, matching backend.Backend.Kind.
	Kind() string
	// AuthURL is where the browser goes to consent. It fails when this
	// deployment cannot start a consent yet — most often because nobody
	// has registered an OAuth client — because a button that navigates
	// nowhere is worse than one that says why.
	AuthURL(state string) (string, error)
	// Exchange turns the callback's code into a workspace and the backend
	// that reads it. Bind carries the workspace being reconnected, or is
	// empty for a new connection.
	Exchange(ctx context.Context, code, bind string) (hub.Workspace, backend.Backend, error)
}

// ClientVerifier is a connector that can say whether this installation's
// registered client would be accepted, without a person. A connector that
// cannot simply does not implement it, and the flow starts unchecked.
type ClientVerifier interface {
	VerifyClient(ctx context.Context) error
}

// KeyConnector is the second way in: a service-account key uploaded
// instead of a consent flow. A connector that cannot do it simply does not
// implement this.
type KeyConnector interface {
	FromKey(ctx context.Context, key []byte, admin string) (hub.Workspace, backend.Backend, error)
}

// CredentialReopener is a connector that can turn a stored credential back
// into a reader -- the counterpart of Exchange and FromKey for a process
// that starts already holding the secret, rather than mid-flow with a
// browser. A connector that cannot simply does not implement it, and a
// kind with no such connector is refused by name after a restart, the
// same as it would be refused at the console.
type CredentialReopener interface {
	OpenStored(ctx context.Context, cred backend.Credential) (backend.Backend, error)
}

// SignInConnector is a connector that can also say who somebody is.
//
// It is a different flow from [Connector], not a parameter of it: consent
// is an administrator granting this hub read access to a company, and
// asks for the directory scopes; sign-in is a person proving who they
// are, and asks for nothing but their address. They return to different
// endpoints because the two endpoints have opposite authorisation — one
// adopts a workspace and demands an operator, the other is how a person
// becomes anyone at all.
type SignInConnector interface {
	Connector
	// SignInURL is where the browser goes to prove who somebody is.
	SignInURL(state string) (string, error)
	// Identify turns the callback's code into the address that
	// authenticated. Nothing else is taken from it: what that address may
	// do is decided by the directory and the policy. state is the
	// callback's, already held to the browser: a provider that sent a nonce
	// derived from it checks the answer against it.
	Identify(ctx context.Context, code, state string) (string, error)
}

// ConsentRedirects is where a backend's CONSENT flow comes back: always the
// console, because connecting a directory is the console's job -- but at the
// HOST ROOT, never under route.pathPrefix. /connect is a bootstrap path, served
// on its own HTTPRoute precisely so the operator connecting the FIRST directory
// -- who by definition no directory can vouch for yet -- is not sent through
// whatever gates the console's own path. A redirect URI registered under the
// prefix would be one Google never returns to.
//
// Read from the backend for the same reason the scopes are: a copy would
// drift, and the drift surfaces in a cloud console's own words, much later,
// naming nothing useful.
var ConsentRedirects = map[string]string{
	"google": google.CallbackPath,
}

// SignInRedirects is where a backend's SIGN-IN flow comes back, which is a
// different service. With one shared OAuth client -- one client for the hub and
// the issuer, so there is one secret to rotate -- the sign-in redirect belongs
// to the ISSUER's hostname, not the console's.
var SignInRedirects = map[string]string{
	"google": google.SignInCallbackPath,
}

// Scopes is what each backend is asked for, all read-only. It reads the list
// from the backend rather than repeating it: a copy would drift, and the
// console would tell an operator to grant one set while the hub asked for
// another.
var Scopes = map[string][]string{
	"google": google.Scopes,
}

// ConsentCauses is what to check when a directory refuses the credential its
// own consent screen has just granted. Ordered by how often each one is the
// answer, because a list read top-down is a list whose order is a claim about
// likelihood.
var ConsentCauses = []string{
	"The directory API is not enabled in the cloud project that owns this hub's OAuth client. " +
		"The message above names the project and the page that enables it.",
	"The API was enabled moments ago. Enabling propagates over a few minutes; try again.",
	"The account that consented is not an administrator of the workspace, or lacks the " +
		"privileges to read users and groups.",
	"The consent was granted with a personal account, or with an account in a different " +
		"workspace than the one intended. The account chooser remembers the last one used.",
}

// ProviderName is a backend as a person calls it.
func ProviderName(kind string) string {
	switch kind {
	case "google":
		return "Google"
	case "entra":
		return "Microsoft"
	case "demo":
		return "the demonstration directory"
	default:
		return kind
	}
}
