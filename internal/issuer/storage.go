package issuer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Verifier turns a third-party subject token into a proof. It is the
// whole of the issuer's trust in another system: GitHub's keys and the
// organisation allow-list, or a Kubernetes TokenReview. Everything after
// it is policy.
//
// It is an interface because the spike needs a fake and a deployment
// needs the real thing, and because a new CI platform should be a new
// implementation rather than a change to the exchange.
type Verifier interface {
	// Verify checks a subject token and returns what it proves. tokenType
	// is the RFC 8693 subject_token_type as presented.
	Verify(ctx context.Context, token, tokenType string) (Proof, error)
}

// ErrUnverified is returned when no verifier recognises a subject token.
var ErrUnverified = errors.New("the subject token was not verified by any configured proof")

// Verifiers tries each in turn. The first to recognise a token owns it;
// none recognising it is a refusal, never a fallback to trusting it.
type Verifiers []Verifier

// Verify implements [Verifier].
func (v Verifiers) Verify(ctx context.Context, token, tokenType string) (Proof, error) {
	for _, one := range v {
		proof, err := one.Verify(ctx, token, tokenType)
		if err == nil {
			return proof, nil
		}
		// A verifier that recognised the token and rejected it is final:
		// a GitHub token with a bad signature must not fall through to
		// being tried as a ServiceAccount token.
		if !errors.Is(err, ErrUnverified) {
			return Proof{}, err
		}
	}
	return Proof{}, ErrUnverified
}

// token is one issued access token, remembered so that userinfo and
// revocation can find it.
type token struct {
	ID       string         `json:"id"`
	Subject  string         `json:"subject"`
	ClientID string         `json:"clientID"`
	Audience []string       `json:"audience,omitempty"`
	Scopes   []string       `json:"scopes,omitempty"`
	Claims   map[string]any `json:"claims,omitempty"`
	Expires  time.Time      `json:"expires"`
	// The account's own names, as the directory gave them at the moment
	// this token was minted. Identity and not authorization: they are
	// what `userinfo` and an ID token say so that a relying party's UI
	// shows a person rather than an address. Empty is normal.
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	// Session is the session this token was issued under, when there is
	// one. It is what lets a REVOCATION reach an access token that has
	// already been minted: the token is a JWT and is verified offline
	// everywhere else, but `userinfo` reads this record and can ask
	// whether the session behind it is still alive.
	//
	// Empty for a grant that opens no session. Such a token lives out
	// its lifetime, which is what it is for.
	Session string `json:"session,omitempty"`
}

// authRequest is a login in progress: a browser part-way between the
// application that sent it and the directory that will say who it is.
//
// It is written down rather than held, because the browser that starts at
// one replica comes back at another. The library's own request travels
// with it: everything the protocol decided at /authorize — the scopes,
// the redirect, the PKCE challenge — has to survive to the moment the
// code is redeemed, and re-deriving any of it would be deciding it twice.
type authRequest struct {
	ID       string            `json:"id"`
	Req      *oidc.AuthRequest `json:"request"`
	Subject  string            `json:"subject,omitempty"`
	AuthTime time.Time         `json:"authTime,omitempty"`
	IsDone   bool              `json:"done,omitempty"`

	// How the person was proved: a provider kind, or "recovery". It is
	// the token's `acr`.
	How string `json:"how,omitempty"`

	// Resource is what this request asked a token FOR, when it named one
	// with `resource` (RFC 8707). Empty is the ordinary case and means
	// the client itself, which is what every client did before resources
	// existed.
	Resource string `json:"resource,omitempty"`

	// SSO is the browser session this request was completed against,
	// whether it was established just now or already existed. It travels
	// down into the per-client session so that ending the browser session
	// can find everything opened from it.
	SSO string `json:"sso,omitempty"`

	// Class is the class of the chain this request opens, decided ONCE
	// from the policy when the request is completed ([Storage.Complete])
	// and taken from here at code redemption ([Sessions.Record]), never
	// from the policy then: a policy change between the two cannot open a
	// chain of a class the completion did not decide. Empty, as a request
	// completed before classes existed has it, is interactive.
	Class SessionClass `json:"class,omitempty"`

	// Session is the session this request opened, learned when the code
	// is redeemed and used one step later to put `sid` in the ID token.
	// It is not written down with the rest: the library hands the SAME
	// request object to the token creation and to the ID token creation
	// within one exchange, and after that the request is deleted.
	Session string `json:"-"`

	// involved is set when the code's redemption has already recorded the
	// client among the sign-in's clients ([Storage.involveAhead]), so the
	// ID token that follows does not record it again. Never written down,
	// for the same reason as Session.
	involved bool
}

var _ op.AuthRequest = (*authRequest)(nil)

func (a *authRequest) GetID() string { return a.ID }

// GetACR says HOW this person was proved, which is the one claim a
// relying party can read to refuse a break-glass sign-in.
//
// It was empty, so a client asking with `acr_values` got no `acr` back —
// conformance says the server SHOULD return one, and more to the point a
// deployment had no way to tell a Google sign-in from recovery in a
// token. Recovery bypasses the directory by design; a relying party that
// wants to refuse it needs to be able to see it.
func (a *authRequest) GetACR() string { return acrFor(a.How) }

// GetAMR is what was presented. It used to say `pwd` unconditionally,
// which is untrue of every sign-in this issuer serves: a recovery
// sign-in presents a ServiceAccount token, and a directory sign-in
// presents whatever the provider asked for — which we are not told, so
// claiming a password is an invention.
func (a *authRequest) GetAMR() []string { return amrFor(a.How) }

// GetAudience is the resource this request named, or the client itself.
//
// The fallback is what keeps every existing client untouched: a client
// that names no resource is still its own audience, which is what a
// relying party pinning `aud` to a client id already expects.
func (a *authRequest) GetAudience() []string {
	if a.Resource != "" {
		return []string{a.Resource}
	}
	return []string{a.Req.ClientID}
}
func (a *authRequest) GetAuthTime() time.Time { return a.AuthTime }
func (a *authRequest) GetClientID() string    { return a.Req.ClientID }
func (a *authRequest) GetCodeChallenge() *oidc.CodeChallenge {
	if a.Req.CodeChallenge == "" {
		return nil
	}
	return &oidc.CodeChallenge{Challenge: a.Req.CodeChallenge, Method: a.Req.CodeChallengeMethod}
}
func (a *authRequest) GetNonce() string                   { return a.Req.Nonce }
func (a *authRequest) GetRedirectURI() string             { return a.Req.RedirectURI }
func (a *authRequest) GetResponseType() oidc.ResponseType { return a.Req.ResponseType }
func (a *authRequest) GetResponseMode() oidc.ResponseMode { return a.Req.ResponseMode }
func (a *authRequest) GetScopes() []string                { return a.Req.Scopes }
func (a *authRequest) GetState() string                   { return a.Req.State }
func (a *authRequest) GetSubject() string                 { return a.Subject }
func (a *authRequest) Done() bool                         { return a.IsDone }

// Storage is the shell the OpenID library needs, over the issuer's
// decision core. It stores; it does not decide. Every question about
// entitlement goes to the [Issuer], so that the console, the token and
// the audit trail cannot disagree about what someone is allowed.
type Storage struct {
	iss     *Issuer
	verify  Verifier
	keys    *KeyRings
	secrets clientcreds.Lookup
	// verifyOnly are public keys published beside the rings' (UseVerifyOnly),
	// and now is the clock that ends them.
	verifyOnly []VerifyOnlyKey
	now        func() time.Time
	// log is for the few things here worth saying out loud. Nothing in
	// this file logged until a reused authorization code needed to be —
	// which is either a broken client or a stolen code, and both are
	// worth seeing.
	log *slog.Logger

	// state is shared by every replica, because a login is not: a browser
	// starts at /authorize on one, comes back from the provider at
	// another, and the client redeems the code at a third.
	state State

	// documents resolves a client that identifies itself with a URL
	// rather than a declared id. Per-process, because its cache is a
	// cache: another replica fetching the same document again is correct,
	// and a shared one would be a way for one replica's stale answer to
	// become every replica's.
	documents *documentClients

	// grants is per-process on purpose. It carries an exchange decision
	// between two calls the library makes about the *same* request, in
	// the same handler, microseconds apart — writing that down would be
	// storing something that never outlives the function that made it.
	grants sync.Map // op.TokenExchangeRequest → Grant

	// scopingReport rate-limits the groups-scoping report line; see
	// [Storage.reportGroupsScoping].
	scopingReport *groupsScopingReporter

	// dead refuses a refresh token already refused as naming no live
	// session, with no State read ([deadRefreshes]). Per-process, like
	// documents: a replica that has not seen the token pays its reads once.
	dead *deadRefreshes

	// signInState is the codec the sign-in pages sign their states with:
	// what an agent-consent acceptance is verified against
	// ([Storage.UseSignInState]). Nil completes no agent-class request.
	signInState *access.StateCodec
}

// The authentication context classes this issuer can report. Custom
// URNs because no registered class describes "a corporate directory
// federated here" or "a ServiceAccount the cluster vouched for", and an
// approximate standard value would be a claim that reads as precise.
const (
	ACRDirectory = "urn:truvity:access-roster:acr:directory"
	ACRRecovery  = "urn:truvity:access-roster:acr:recovery"
)

// RecoveryHow is what a recovery sign-in records as its method. The
// rest of that vocabulary is a PROVIDER KIND -- "google", "entra" --
// which is a different set from the per-client session's `How`, and
// the reason this maps rather than switching on that type.
const RecoveryHow = "recovery"

// acrFor maps how somebody was proved onto a class a relying party can
// act on. The distinction that earns its keep is DIRECTORY against
// RECOVERY: one went through the company's own identity provider, the
// other bypassed it on purpose.
func acrFor(how string) string {
	if how == RecoveryHow {
		return ACRRecovery
	}

	// A provider kind, or empty on a deployment that records none:
	// the directory answered, which is the ordinary case.
	return ACRDirectory
}

// amrFor is what was actually presented, and nothing more. An empty list
// is the honest answer where we were not told, which is most of the time:
// the provider knows whether there was a second factor and does not say.
func amrFor(how string) []string {
	if how == RecoveryHow {
		// Not a password and not a person: a token the API server
		// vouched for.
		return []string{"swk"}
	}

	return nil
}

// How long each kind of thing in a login flow is worth keeping. Nothing
// here is state a person would miss: an abandoned login is abandoned, and
// a code nobody redeemed is a browser that closed.
const (
	// authRequestTTL bounds a login from /authorize to the redirect back.
	// Generous, because it spans a person reading a consent screen.
	authRequestTTL = 30 * time.Minute
	// authCodeTTL bounds the moment between the redirect and the token
	// call, which is a machine talking to a machine.
	authCodeTTL = 5 * time.Minute
)

// Keys. The prefix is what tells one kind from another in a store shared
// with the hub's snapshots.
func requestKey(id string) string { return "issuer:request:" + id }
func codeKey(code string) string  { return "issuer:code:" + code }

// logger is the storage's, or the default when a caller supplied none.
func (s *Storage) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}

	return slog.Default()
}

// UseLog gives the storage a logger of its own, in place of
// [slog.Default] -- the deployment's own, so this package's few log
// lines (a reused authorization code, the groups-scoping report) carry
// whatever attributes and level it configured, rather than depending on
// [slog.SetDefault] having been called first. A test that wants to
// capture what this package logs calls it with a logger of its own too,
// rather than mutating the process-wide default.
func (s *Storage) UseLog(log *slog.Logger) { s.log = log }

// codeSessionKey remembers which session one authorization code opened,
// so that a reuse of that code can end it.
func codeSessionKey(request string) string { return "issuer:code-session:" + request }
func tokenKey(id string) string            { return "issuer:token:" + id }

var (
	_ op.Storage                            = (*Storage)(nil)
	_ op.TokenExchangeStorage               = (*Storage)(nil)
	_ op.TokenExchangeTokensVerifierStorage = (*Storage)(nil)
)

// noClientSecrets is the lookup of a storage given none: no client has a
// secret, so no confidential client authenticates.
type noClientSecrets struct{}

func (noClientSecrets) Resolve(context.Context, string) (clientcreds.Secrets, bool) {
	return clientcreds.Secrets{}, false
}

// NewStorage returns the storage over an issuer.
//
// secrets resolves a confidential client's secrets (the current one and, for
// a while after a rotation, the previous), which live in the credentials or in
// the installation's inputs and never in the policy file. key is the primary
// signing key; a nil one is generated, which is right for a local run and
// wrong for a deployment — see [SigningKey]. additional is every OTHER
// algorithm this installation signs with at once, at most one key per
// algorithm — see [KeyRings]. Together they seed the [KeyRings] this
// storage keeps for the life of the process: a later key, read after a
// rotation, is fed to it with [Storage.Rotate] rather than by building a
// new Storage. state is where a login in progress lives, and where each
// ring's schedule is shared with every other replica; a nil one is kept
// in this process, which is right for one replica and wrong for more —
// see [State].
//
// This is also THE PLACE policy and keys meet, and so the one place a
// policy naming a `signing_alg` this installation has no key for can be
// refused loudly, before it ever reaches a token: see
// [checkSigningAlgorithms]. Falling back to the default here instead
// would mean an operator's `signing_alg: RS256` quietly minting ES384
// tokens because the RS256 Secret was never mounted — the opposite of
// what naming an algorithm at all is for.
func NewStorage(
	iss *Issuer, verify Verifier, secrets clientcreds.Lookup,
	key *SigningKey, additional []*SigningKey, state State,
) (*Storage, error) {
	if key == nil {
		generated, err := NewSigningKey()
		if err != nil {
			return nil, err
		}
		key = generated
	}
	if secrets == nil {
		secrets = noClientSecrets{}
	}
	if state == nil {
		state = NewMemoryState()
	}
	keys, err := NewKeyRings(key, additional, state, KeyRingConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("issuer: adopt the signing keys: %w", err)
	}
	if err := checkSigningAlgorithms(iss.Policy(), keys); err != nil {
		return nil, err
	}
	// Nothing here implements op.DeviceAuthorizationStorage, and that is
	// the mechanism by which the device flow is not served. The
	// library type-asserts for it and refuses the grant when the
	// assertion fails, so there is no device state to keep and no way for
	// a device code to be stored by something that changed its mind.
	return &Storage{
		iss:           iss,
		verify:        verify,
		keys:          keys,
		secrets:       secrets,
		state:         state,
		documents:     newDocumentClients(iss.Policy().ClientDocuments()),
		scopingReport: newGroupsScopingReporter(),
		now:           time.Now,
		// The session index's clock, so that time a test moves for the
		// sessions moves for the cache's TTL too.
		dead: newDeadRefreshes(fingerprintKey(key.seed), deadRefreshEntries, deadRefreshConfirm, deadRefreshTTL,
			func() time.Time { return iss.Sessions().now() }),
	}, nil
}

// checkSigningAlgorithms refuses a client or a resource naming a
// `signing_alg` this installation has no key configured for. [policy.Client]
// and [policy.Resource] can only check that the VALUE is one of
// [policy.SigningAlgs] — they know nothing of what keys exist — so this is
// the one place, at issuer start, that both are in hand together.
func checkSigningAlgorithms(set *policy.Set, keys *KeyRings) error {
	// By index rather than by value: [policy.ClientView] and
	// [policy.ResourceView] are wide structs, and copying one per
	// iteration is what the linter objects to -- see [policy.Set.Clients].
	clients := set.Clients()
	for i := range clients {
		if clients[i].SigningAlg == "" {
			continue
		}
		if alg := jose.SignatureAlgorithm(clients[i].SigningAlg); !keys.Has(alg) {
			return fmt.Errorf("client %q: signing_alg %q names an algorithm this installation "+
				"has no key for (configured: %v)", clients[i].ID, clients[i].SigningAlg, keys.Configured())
		}
	}
	resources := set.Resources()
	for i := range resources {
		if resources[i].SigningAlg == "" {
			continue
		}
		if alg := jose.SignatureAlgorithm(resources[i].SigningAlg); !keys.Has(alg) {
			return fmt.Errorf("resource %q: signing_alg %q names an algorithm this installation "+
				"has no key for (configured: %v)", resources[i].ID, resources[i].SigningAlg, keys.Configured())
		}
	}
	return nil
}

// ---------------------------------------------------------------- keys

// Rotate feeds a freshly re-read signing key to this storage's key ring —
// the whole of live rotation. A caller polling the mounted key file calls
// this on every read, whether or not the content changed; see
// [KeyRing.Observe] for the schedule that decides what happens next: a
// key never seen before is published immediately and starts signing only
// after its activation delay, and the key it supersedes stays published
// for its overlap before dropping out.
func (s *Storage) Rotate(ctx context.Context, key *SigningKey) error {
	return s.keys.Rotate(ctx, key)
}

// RotateKnown refreshes a signing key the rings already hold without ever
// adopting a new one: the older keys of a KMS list.
func (s *Storage) RotateKnown(ctx context.Context, key *SigningKey) error {
	return s.keys.RotateKnown(ctx, key)
}

// UseWrappedSigning makes the key rings generate, wrap and rotate their own
// keys (the `kms-wrapped` signing adapter), and starts the rotation: from here
// every signing and every JWKS request, and [Storage.MaintainKeys], keep it going.
func (s *Storage) UseWrappedSigning(ws *WrappedSigning) {
	s.keys.UseWrapped(ws)
}

// MaintainKeys runs the wrapped keys' rotation once: cheap, and a no-op until
// the interval has passed. A process with a loop of its own calls it on a
// timer, so rotation does not wait for traffic.
func (s *Storage) MaintainKeys(ctx context.Context) { s.keys.Maintain(ctx) }

// ConfigureKeyRotation overrides every ring's activation delay and
// overlap once a deployment's own settings are known — its token
// lifetime, chiefly, which [KeyRingConfig.Overlap] must be at least as
// long as. Call it before serving; [KeyRing.Configure] is not meant to be
// changed while replicas are actively rotating.
func (s *Storage) ConfigureKeyRotation(cfg KeyRingConfig) {
	s.keys.Configure(cfg)
}

// SigningKey implements [op.AuthStorage]: the key this replica currently
// signs with.
//
// WHICH key is the whole of per-audience signing. The library hands this
// nothing but ctx, so the audience travels in it — written by whichever
// storage hook learns it first for this token: [Storage.CreateAccessToken]
// and [Storage.CreateAccessAndRefreshTokens] for an access token, and
// [client.RestrictAdditionalIdTokenScopes] for an ID token, which the
// library always asks for a signing key AFTER an access token's, on the
// SAME context — see [signingAudience] for why a mutable carrier is what
// makes that possible with an interface this narrow, and every mint path
// that is NOT reached through the library (MintFor, a Back-Channel Logout
// token) resolves its algorithm directly instead, with no ctx trick
// needed, because it already holds its own target audience.
//
// A path that forgets to mark the carrier is not a crash: it reads back
// unset, exactly like a request naming no special audience, and this
// signs with the installation DEFAULT. That is deliberate — a forgotten
// mark must never become a hard failure a relying party sees — but it is
// also the risk every negative test here exists to catch: an audience
// configured for RS256 that quietly got ES384 because some path never
// marked the carrier would look, from here, identical to one that was
// never asked to be anything but the default.
func (s *Storage) SigningKey(ctx context.Context) (op.SigningKey, error) {
	s.keys.Maintain(ctx)
	alg := s.keys.Default()
	if id, ok := signingAudienceFrom(ctx).get(); ok {
		alg = s.signingAlgorithmFor(id)
	}

	active := s.keys.Active(alg)
	if active == nil && !s.keys.Has(alg) {
		// No ring exists for this algorithm: checkSigningAlgorithms refuses
		// that at start for anything a policy row names, so this is an
		// algorithm nobody configured, and the default answers.
		active = s.keys.Active(s.keys.Default())
	}
	if active == nil && s.keys.Has(alg) {
		// The ring exists but has no signer yet (its key is unactivated, or
		// retired): never a token of ANOTHER algorithm, which a relying
		// party pinned to this one would reject or, worse, accept.
		return nil, fmt.Errorf("issuer: no signing key is available yet for %s", alg)
	}
	if active == nil {
		return nil, errors.New("issuer: no signing key is available yet")
	}
	return active, nil
}

// signingAlgorithmFor is the algorithm a token FOR audience id is signed
// with: that audience's own `signing_alg` — a resource's, when id names
// one declared, else a client's — or the installation default when it
// named none or names something not declared at all (validated before the
// library was reached; reaching here with an undeclared id is a request
// this issuer is about to refuse for an unrelated reason, and the
// algorithm it never gets a chance to matter for is not worth refusing a
// second way).
//
// Resource ids and client ids share no space — [policy.validateResourceID]
// requires an absolute URI RFC 8707 style, checked against the RESOURCE
// table specifically — so trying the resource table first and falling
// through to the client table is unambiguous, not a guess.
func (s *Storage) signingAlgorithmFor(id string) jose.SignatureAlgorithm {
	if id != "" {
		if resource, ok := s.iss.Policy().Resource(id); ok && resource.SigningAlg != "" {
			return jose.SignatureAlgorithm(resource.SigningAlg)
		}
		if client, ok := s.iss.Policy().Client(id); ok && client.SigningAlg != "" {
			return jose.SignatureAlgorithm(client.SigningAlg)
		}
	}
	return s.keys.Default()
}

// SignatureAlgorithms implements [op.AuthStorage]. It is what the
// discovery document advertises as
// `id_token_signing_alg_values_supported`, so it names what is actually
// PUBLISHED right now — every key in the JWKS, across EVERY configured
// algorithm, not only the one currently signing a particular audience —
// because a relying party that fetched discovery mid-rotation still has
// to accept a token a moment away from expiring under a previous key, and
// one that reads `aud` for a client with no `signing_alg` still has to
// accept whatever the installation default is.
func (s *Storage) SignatureAlgorithms(context.Context) ([]jose.SignatureAlgorithm, error) {
	algs := s.keys.Algorithms()
	for _, k := range s.verifyOnlyKeys(s.keys.Published()) {
		if !slices.Contains(algs, k.Algorithm()) {
			algs = append(algs, k.Algorithm())
		}
	}
	slices.Sort(algs)
	return algs, nil
}

// KeySet implements [op.AuthStorage]: every key currently published, by
// every configured algorithm, signing or retiring — see [KeyRings.Published].
func (s *Storage) KeySet(ctx context.Context) ([]op.Key, error) {
	s.keys.Maintain(ctx)
	published := s.keys.Published()
	return append(published, s.verifyOnlyKeys(published)...), nil
}

// Health implements [op.Storage].
func (s *Storage) Health(context.Context) error { return nil }

// ------------------------------------------------------------- clients

// GetClientByClientID implements [op.OPStorage].
//
// A declared client is looked up first and always wins. That ordering is
// the guarantee that turning on client documents changes nothing about
// the clients this installation already has: a document served at a URL
// that happens to match a declared id can never displace it, because the
// declared one is found before anything is fetched.
func (s *Storage) GetClientByClientID(ctx context.Context, clientID string) (op.Client, error) {
	// The audience carrier this request's context holds, if any -- handed
	// to the returned client so that [client.RestrictAdditionalIdTokenScopes]
	// can mark it later with no context of its own to read it from. Both
	// branches below need it, so it is read once here rather than twice.
	signing := signingAudienceFrom(ctx)

	declared, ok := s.iss.Policy().Client(clientID)
	if ok {
		return &client{id: clientID, declared: declared, lifetime: s.tokenLifetime(declared), signing: signing}, nil
	}

	// Not declared. It may still be a client that describes itself.
	resolved, err := s.documents.Resolve(ctx, clientID)
	switch {
	case err == nil:
		// Worth saying out loud: it is the one client this installation
		// did not declare, so the log is where a reader sees that one was
		// admitted at all, and which URL it was.
		s.logger().InfoContext(ctx, "admitted a client that describes itself",
			logattr.SafeString("client", clientID), logattr.SafeString("name", resolved.DisplayName))
		return &client{id: clientID, declared: resolved, lifetime: s.tokenLifetime(resolved), signing: signing}, nil
	case errors.Is(err, errNotADocumentClient):
		return nil, fmt.Errorf("%w: %q", ErrUnknownTarget, clientID)
	default:
		return nil, err
	}
}

// tokenLifetime is how long this client's tokens live: the deployment's
// lifetime, narrowed by the client's own `ttl_cap`.
//
// The cap was honoured on token EXCHANGE and nowhere else, so declaring
// it on a browser client did nothing at all. It matters most there. A
// revoked session keeps working until the client next has to refresh, so
// the access token's lifetime IS the window in which a sign-out or a
// revoke has not taken effect yet — and for a console that window was
// the deployment-wide default.
func (s *Storage) tokenLifetime(declared policy.Client) time.Duration {
	return declared.Cap(s.iss.Config().TokenLifetime)
}

// AuthorizeClientIDSecret implements [op.OPStorage].
func (s *Storage) AuthorizeClientIDSecret(ctx context.Context, clientID, secret string) error {
	declared, ok := s.iss.Policy().Client(clientID)
	if !ok {
		// A client that describes itself is public and reaches the token
		// endpoint with PKCE and no credentials, which the library handles
		// without ever asking here. So arriving here means it sent a
		// secret -- and saying "unknown client" would send somebody
		// looking for a typo in an id that is correct.
		if target, err := documentURL(clientID); err == nil && s.documents.allow.Permits(target) {
			return errors.New("a client that registers itself by document is public and presents no secret")
		}
		return fmt.Errorf("%w: %q", ErrUnknownTarget, clientID)
	}
	// A public client holds no secret, and the library asks all the same,
	// including for a token exchange. That is the right question with the
	// wrong premise: in an exchange the subject token is the credential —
	// a GitHub identity token verified against GitHub's keys — and the
	// client id only names who is asking. So a public client presenting
	// nothing is authenticated, and one presenting a secret is refused,
	// because it should not have one to present.
	// An EXCHANGE client is the same case and was missing from it. It is
	// an audience -- a cloud role, a cluster -- and the design gives it
	// no secret anywhere: `Client.Secret` is refused on that kind. But
	// the library authenticates the caller of an exchange by HTTP Basic,
	// so falling through to the secret path meant a declared exchange
	// client could never authenticate at all, and every exchange failed
	// with "the client secret does not match" while the policy looked
	// correct. Found by trying the first real exchange this issuer has
	// ever been asked to do.
	if declared.Kind == policy.KindPublic || declared.Kind == policy.KindExchange {
		if secret != "" {
			return errors.New("a public client presents no secret")
		}

		return nil
	}
	want, ok := s.secrets.Resolve(ctx, clientID)
	if slot := s.matchSecret(want, ok, secret); slot != clientcreds.SlotNone {
		clientcreds.CountAuth(ctx, slot)
		return nil
	}
	// A replica that cached the pair before a rotation would refuse the old
	// secret during the overlap, or the new one at once: read it fresh, once,
	// before refusing (ADR 0041, "Rotation").
	if r, can := s.secrets.(interface {
		Reread(ctx context.Context, clientID string) (clientcreds.Secrets, bool)
	}); can {
		if again, found := r.Reread(ctx, clientID); found {
			if slot := s.matchSecret(again, found, secret); slot != clientcreds.SlotNone {
				clientcreds.CountAuth(ctx, slot)
				return nil
			}
		}
	}
	clientcreds.CountAuth(ctx, clientcreds.SlotNone)
	return errors.New("the client secret does not match")
}

// matchSecret says which slot of want the presented secret matches.
//
// Constant time: the comparison must not tell a caller how much of a guess was
// right, nor whether the client has a previous secret. Both slots are compared
// every time; with no previous one the guess is compared with a buffer of its
// own length, whose answer is ignored.
func (s *Storage) matchSecret(want clientcreds.Secrets, ok bool, secret string) string {
	got := []byte(secret)
	current := subtle.ConstantTimeCompare([]byte(want.Current), got) == 1
	previous := want.Previous
	hasPrevious := previous != ""
	var other []byte
	if hasPrevious {
		other = []byte(previous)
	} else {
		other = make([]byte, len(got))
	}
	previousMatch := subtle.ConstantTimeCompare(other, got) == 1
	previousLive := hasPrevious && s.now().Before(want.PreviousValidUntil)
	switch {
	case ok && want.Current != "" && current:
		return clientcreds.SlotCurrent
	case ok && previousLive && previousMatch:
		return clientcreds.SlotPrevious
	}
	return clientcreds.SlotNone
}

// --------------------------------------------------------- auth requests

// CreateAuthRequest implements [op.AuthStorage].
func (s *Storage) CreateAuthRequest(
	ctx context.Context, req *oidc.AuthRequest, subject string,
) (op.AuthRequest, error) {
	out := &authRequest{
		ID: uuid.NewString(), Req: req, Subject: subject, AuthTime: time.Now(),
		// Validated against the declared resources before the library was
		// reached; this is the first place there is anywhere to put it.
		Resource: resourceFromContext(ctx),
	}
	if err := setJSON(ctx, s.state, requestKey(out.ID), out, authRequestTTL); err != nil {
		return nil, err
	}
	return out, nil
}

// AuthRequestByID implements [op.AuthStorage].
func (s *Storage) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	req, err := s.request(ctx, id)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func (s *Storage) request(ctx context.Context, id string) (*authRequest, error) {
	req, err := getJSON[authRequest](ctx, s.state, requestKey(id))
	switch {
	case err != nil:
		return nil, err
	case req == nil:
		// Expired or never existed, and the two are the same answer to
		// the caller: there is nothing to continue.
		return nil, errors.New("no such authorization request")
	}
	return req, nil
}

// AuthRequestByCode implements [op.AuthStorage].
func (s *Storage) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	raw, found, err := s.state.Get(ctx, codeKey(code))
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, errors.New("no such authorization code")
	}

	request, err := s.request(ctx, string(raw))
	if err != nil {
		// The code is known and its request is gone, which is what a
		// SECOND redemption looks like: the first one deleted the
		// request. RFC 6749 4.1.2 says deny it and revoke what it
		// already issued, and the second half is the one that matters —
		// a code presented twice is a code somebody else has, and the
		// tokens from its first use are the ones now in doubt.
		s.revokeCodeSession(ctx, string(raw))

		return nil, err
	}

	return request, nil
}

// errSignInEnded refuses a code whose request was completed under a
// browser sign-in that has ended since. A fresh error each time: the
// library's errors are pointers it may annotate.
func errSignInEnded() error {
	return oidc.ErrInvalidGrant().WithDescription("the sign-in this code was issued under has ended")
}

// signInEnded reports whether the browser sign-in a code was completed
// under has ended, read AFTER what the code opens has been written.
//
// A silent sign-in reads the browser's sign-in and then completes the
// request; a sign-out in another tab can land in between, or anywhere
// before the relying party redeems the code. What the code opens after
// the sign-out has listed the sessions would be filed under a sign-in
// nothing can end any more: it would outlive the sign-out the person just
// made, and no later sign-out would reach it.
//
// So the order is what closes it, on both sides. Sign-out ends the sign-in
// first and then lists and revokes its sessions ([endSignIn]); a code
// writes its session first and then reads the sign-in. Either this read
// comes after the end, and the code ends what it wrote, or it comes
// before, and so did the write, which the sign-out's listing then finds.
// A read BEFORE the write would leave the write after a sign-out that had
// read it as live. The same for a client that asked for `openid` alone:
// the client is recorded among the sign-in's first, then the sign-in is
// read, and only a sign-out whose two adjacent store calls (reading the
// clients, ending the sign-in) straddle both goes untold.
//
// One read per code redemption under a sign-in, and none on a refresh: a
// session that exists was filed while its sign-in stood, and sign-out
// reaches it.
func (s *Storage) signInEnded(ctx context.Context, sso, clientID string) (bool, error) {
	if sso == "" || s.iss.SSO() == nil {
		return false, nil
	}

	_, live, err := s.iss.SSO().Get(ctx, sso)
	if err != nil {
		return false, err
	}

	if !live {
		s.logger().InfoContext(ctx, "refused an authorization code: the sign-in it was completed under has ended",
			slog.String("client", clientID))
	}

	return !live, nil
}

// revokeCodeSession ends the session one authorization code opened, on
// learning that the code was presented a second time.
//
// Best effort and silent about it: this runs while answering a request
// that is being refused anyway, and a failure here must not turn a
// refusal into a server error.
func (s *Storage) revokeCodeSession(ctx context.Context, request string) {
	recordReuse(ctx, "authorization_code")
	raw, found, err := s.state.Get(ctx, codeSessionKey(request))
	if err != nil || !found {
		return
	}

	gone, err := s.iss.Sessions().RevokeID(ctx, string(raw))
	if err != nil {
		s.logger().WarnContext(ctx, "an authorization code was reused and its session could not be ended",
			slog.Any("error", err))

		return
	}

	// WARN and not INFO: a code presented twice is either a broken client
	// or a stolen code, and both are worth seeing in a log.
	s.logger().WarnContext(ctx, "an authorization code was reused; the session it opened has been ended",
		slog.Bool("ended", gone))

	_ = s.state.Delete(ctx, codeSessionKey(request))
}

// SaveAuthCode implements [op.AuthStorage].
func (s *Storage) SaveAuthCode(ctx context.Context, id, code string) error {
	if _, err := s.request(ctx, id); err != nil {
		return err
	}
	return s.state.Set(ctx, codeKey(code), []byte(id), authCodeTTL)
}

// DeleteAuthRequest implements [op.AuthStorage].
//
// The code is not deleted with it, and does not need to be: it expires on
// its own, and it resolves to a request that is already gone. Hunting for
// it would mean an index from request to code kept only to tidy up.
func (s *Storage) DeleteAuthRequest(ctx context.Context, id string) error {
	return s.state.Delete(ctx, requestKey(id))
}

// Complete marks a login as finished, which is what the issuer's own
// sign-in page calls once an identity provider has said who is there.
//
// The context is the sign-in's request, for what its record keeps of that
// request; the work is not cancelled with it, because a browser that went
// away mid-completion must not leave a request half marked.
func (s *Storage) Complete(ctx context.Context, id string, who Authenticated) error {
	ctx = context.WithoutCancel(ctx)
	req, err := s.request(ctx, id)
	if err != nil {
		return err
	}

	// A request already completed is completed again only by the same
	// person under the same sign-in, as a page answered twice does. Never by
	// another: a holder of somebody else's request id who signs in
	// themselves must not swap their identity into a request whose code is
	// on its way to the first person's client.
	if req.IsDone && (req.Subject != strings.ToLower(who.Subject) || req.SSO != who.SSO) {
		s.logger().WarnContext(ctx, "refused to complete an authorization request again as somebody else",
			logattr.SafeString("client", req.Req.ClientID))

		return ErrCompletedByAnother
	}

	authTime := who.AuthTime
	if authTime.IsZero() {
		authTime = time.Now()
	}

	// Before the request is marked done, because a request marked done is
	// one a code can be issued for.
	if err = s.entitled(ctx, req.Req.ClientID, req.Resource, who.Subject, who.How == RecoveryHow); err != nil {
		if errors.Is(err, ErrNotEntitled) {
			s.iss.record(ctx, signInEvent(who, req.Req.ClientID, "", time.Time{},
				audit.Denied("signed in, and admitted to no group this client requires")))
		}
		return err
	}

	req.Subject, req.IsDone = strings.ToLower(who.Subject), true
	// NOT time.Now(): a request completed silently against a session
	// established an hour ago authenticated an hour ago, and `auth_time`
	// is the one claim that has to say so — it is what a
	// re-authenticate-for-this-action rule reads.
	req.AuthTime, req.SSO, req.How = authTime, who.SSO, who.How
	// The class of the chain this request opens is decided here, once,
	// from the policy as it stands now, and travels on the request to the
	// code's redemption (docs/decisions/0040-agent-class-sessions.md). A
	// recovery sign-in is the way in that bypasses the directory, and it
	// never opens a month-long chain, whatever the client's class.
	req.Class = s.classOf(req.Req.ClientID)
	if who.How == RecoveryHow {
		req.Class = ClassInteractive
	}
	deadline := s.iss.Sessions().deadlineAt(req.Class, authTime, req.Resource)
	if req.Class == ClassAgent && deadline.IsZero() {
		// No agent lifetimes to give it: the session the code opens would be
		// interactive ([Sessions.Record]), so the request is one too.
		req.Class = ClassInteractive
		deadline = s.iss.Sessions().deadlineAt(req.Class, authTime, req.Resource)
	}

	// An agent-class request never completes silently: only on an
	// acceptance made, after authentication, in the browser the consent
	// page was shown in, and verified HERE rather than by the page that
	// received it -- every sign-in converges on this function, so no door
	// can complete one without it (docs/decisions/0040-agent-class-sessions.md,
	// decision 6). Nothing is recorded: the page that asks is not a refusal.
	if req.Class == ClassAgent {
		if err = s.acceptedAgent(id, who); err != nil {
			return err
		}
	}
	signedIn := signInEvent(who, req.Req.ClientID, req.Class, deadline, audit.Succeeded())

	// A recovery sign-in is written down durably before the request is
	// marked done — a request marked done is one a code can be issued for —
	// and is refused when it cannot be. It is the one event that does not
	// fail open: the way in that bypasses the directory must never leave
	// no trace, and the write depends on the audit installation's writer
	// and the pod's own token alone, nothing this service runs.
	recovery := who.How == RecoveryHow
	if recovery {
		if err = s.iss.recordDurable(ctx, signedIn); err != nil {
			s.iss.record(ctx, signInEvent(who, req.Req.ClientID, req.Class, deadline,
				audit.Denied("the audit trail could not be written, and a recovery sign-in is refused without its record")))
			return fmt.Errorf("%w: %w", ErrUnaudited, err)
		}
	}

	if err = setJSON(ctx, s.state, requestKey(id), req, authRequestTTL); err != nil {
		if recovery {
			// Its record already says it succeeded; this says it did not.
			s.iss.record(ctx, signInEvent(who, req.Req.ClientID, req.Class, deadline, audit.Failed("the sign-in could not be saved")))
		}
		return err
	}
	if !recovery {
		s.iss.record(ctx, signedIn)
	}
	return nil
}

// ErrUnaudited is a recovery sign-in refused because its record could not
// be written.
var ErrUnaudited = errors.New("the audit trail could not be written")

// ErrCompletedByAnother is an authorization request that was already
// completed by a different person, or under a different sign-in.
var ErrCompletedByAnother = errors.New("this sign-in was already completed by somebody else")

// ErrAgentConsentRequired is an agent-class request that cannot complete
// without the person's acceptance in this browser: none was presented, or
// the one presented does not verify. The sign-in pages answer it with the
// consent page after authentication, and an acceptance post with a refusal.
var ErrAgentConsentRequired = errors.New("an agent connection needs to be accepted in this browser")

// UseSignInState gives the storage the codec the sign-in pages sign their
// states with, which is what [Storage.Complete] verifies an agent-consent
// acceptance against. A storage given none completes no agent-class request
// at all. [HandlerWithSignIn] wires it from the sign-in dependencies.
func (s *Storage) UseSignInState(codec *access.StateCodec) { s.signInState = codec }

// acceptedAgent verifies the acceptance a completion carries for this
// request and this person: the token's purpose, the request it is bound
// to, the subject and sign-in it was shown to, and the cookie beside it,
// compared in constant time ([access.StateCodec.VerifyAgentConsent]).
func (s *Storage) acceptedAgent(request string, who Authenticated) error {
	if s.signInState == nil {
		return fmt.Errorf("%w: this issuer has no sign-in state to verify an acceptance with", ErrAgentConsentRequired)
	}

	if err := s.signInState.VerifyAgentConsent(who.Consent, request, access.AgentConsentActor(who.Subject, who.SSO)); err != nil {
		return fmt.Errorf("%w: %w", ErrAgentConsentRequired, err)
	}

	return nil
}

// classOf is the session class the policy gives a client NOW: its own row's
// `session`, or, for a client that describes itself and is admitted by an
// origin, `client_documents.session` as the document resolver holds it. Never anything a document says about
// itself: the class is a grant of time the installation makes, not one a
// client takes. Anything unknown is interactive.
func (s *Storage) classOf(clientID string) SessionClass {
	set := s.iss.Policy()
	if set == nil {
		return ClassInteractive
	}

	if declared, ok := set.Client(clientID); ok {
		if declared.Agent() {
			return ClassAgent
		}

		return ClassInteractive
	}

	// The same client_documents the resolver admitted the client by
	// ([documentClients.allow]), so the origin that let it in and the class
	// it gets can never come from two different policies.
	documents := s.documents.allow
	if target, err := documentURL(clientID); err == nil && documents.Enabled() && documents.Permits(target) && documents.Agent() {
		return ClassAgent
	}

	return ClassInteractive
}

// classOfRequest is the class a token request's chain was recorded with:
// the completed authorization's for a code, the session's for a refresh,
// and interactive for anything else (an exchange opens no agent chain).
func classOfRequest(request op.TokenRequest) SessionClass {
	switch req := request.(type) {
	case *authRequest:
		if req.Class == ClassAgent {
			return ClassAgent
		}
	case *refreshRequest:
		if req.session.Agent() {
			return ClassAgent
		}
	}

	return ClassInteractive
}

// signInEvent is a completed or refused sign-in at one client. A recovery
// sign-in is its own kind, because it is the way in that bypasses the
// directory and has to be findable as such.
//
// A person's carries the class of the chain the sign-in opens and its
// computed deadline (docs/decisions/0040-agent-class-sessions.md, decision
// 8); a refusal decided neither and carries none.
func signInEvent(who Authenticated, clientID string, class SessionClass, deadline time.Time, o audit.Outcome) *record.Record {
	if who.How == RecoveryHow {
		return audit.RecoverySignedIn(audit.RecoveryIdentity(who.Subject), clientID, who.How, o)
	}
	return audit.SignedInSession(audit.Identified(who.Subject), clientID, who.How, string(class), deadline, o)
}

// Pending reports what an authorization request asks of a sign-in, so
// that the sign-in pages can answer it without knowing the protocol.
func (s *Storage) Pending(id string) (Pending, error) {
	req, err := s.request(context.Background(), id)
	if err != nil {
		return Pending{}, err
	}

	out := Pending{
		RedirectURI: req.Req.RedirectURI, State: req.Req.State, ClientID: req.Req.ClientID,
		Resource: req.Resource,
	}

	// The declared row, for the page to name the application by. Looked
	// up here rather than carried in the request so that a name changed
	// in the policy shows on a sign-in already in flight, and an absent
	// row is simply no name.
	if declared, ok := s.iss.Policy().Client(req.Req.ClientID); ok {
		out.Client = declared
	}

	// The same for the resource a token is asked FOR: its declared
	// `display_name`, or the URL itself when it declares none.
	if req.Resource != "" {
		out.ResourceName = req.Resource
		if declared, ok := s.iss.Policy().Resource(req.Resource); ok {
			out.ResourceName = declared.Title(req.Resource)
		}
	}

	// Whether the request would open an agent-class chain as the policy
	// stands now, for the pages to bypass a silent completion and say so.
	// [Storage.Complete] decides the class again, and is what enforces it.
	if s.classOf(req.Req.ClientID) == ClassAgent {
		out.Agent = true
		out.AgentAbsolute = s.iss.Sessions().agentAbsoluteOf(req.Resource)
	}

	for _, prompt := range req.Req.Prompt {
		switch prompt {
		case oidc.PromptLogin, oidc.PromptSelectAccount:
			out.ForcesLogin = true
		case oidc.PromptNone:
			out.ForbidsUI = true
		}
	}

	if req.Req.MaxAge != nil {
		out.MaxAge = time.Duration(*req.Req.MaxAge) * time.Second
		// `max_age=0` is "authenticate now" -- the same demand
		// `prompt=login` makes, said with a different word. Reading it as
		// "no maximum" (which a zero duration otherwise means here) would
		// answer a request for a fresh authentication with an old one.
		if *req.Req.MaxAge == 0 {
			out.ForcesLogin = true
		}
	}

	return out, nil
}

// --------------------------------------------------------------- tokens

// CreateAccessToken implements [op.AuthStorage].
func (s *Storage) CreateAccessToken(ctx context.Context, request op.TokenRequest) (string, time.Time, error) {
	// The library mints a refresh grant through CreateAccessAndRefreshTokens;
	// a reused token must not mint here either, whatever path reaches it.
	if req, isRefresh := request.(*refreshRequest); isRefresh && req.presented.reused != "" {
		return "", time.Time{}, oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
	}
	if err := s.refusalOf(ctx, request); err != nil {
		return "", time.Time{}, err
	}
	if err := resourceWithinGrant(ctx, request); err != nil {
		return "", time.Time{}, err
	}

	// A code that opens no session -- `openid` alone, or a request whose
	// every scope the client was not allowed, which mints no ID token
	// claims and so never reaches [Storage.SetUserinfoFromRequest] -- is
	// held to its sign-in here, before anything is minted: the client is
	// recorded among the sign-in's clients, then the sign-in is read
	// ([Storage.signInEnded]). Recorded here, the ID token's own hook
	// skips both.
	if opened, ok := request.(*authRequest); ok && opened.SSO != "" && s.iss.SSO() != nil {
		involved, err := s.involveAhead(ctx, request, false)
		if err != nil {
			return "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
		}
		opened.involved = involved

		ended, err := s.signInEnded(ctx, opened.SSO, opened.Req.ClientID)
		if err != nil {
			return "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
		}
		if ended {
			return "", time.Time{}, errSignInEnded()
		}
	}

	issued, err := s.issue(ctx, request)
	if err != nil {
		return "", time.Time{}, err
	}
	if err = s.keep(ctx, request, issued); err != nil {
		return "", time.Time{}, err
	}
	return issued.ID, issued.Expires, nil
}

// CreateAccessAndRefreshTokens implements [op.AuthStorage].
func (s *Storage) CreateAccessAndRefreshTokens(
	ctx context.Context, request op.TokenRequest, currentRefreshToken string,
) (string, string, time.Time, error) {
	// A spent token past its grace window, presented by the session's own
	// client: the session ends here, before anything is resolved or minted.
	if req, isRefresh := request.(*refreshRequest); isRefresh && req.presented.reused != "" {
		return "", "", time.Time{}, s.endReuse(ctx, req.presented)
	}

	// A refusal the first reading carried, now that the client is matched:
	// returned as it is, which the library passes to the wire unchanged.
	if err := s.refusalOf(ctx, request); err != nil {
		return "", "", time.Time{}, err
	}
	if err := resourceWithinGrant(ctx, request); err != nil {
		return "", "", time.Time{}, err
	}

	issued, err := s.issue(ctx, request)
	if err != nil {
		return "", "", time.Time{}, err
	}
	refresh := uuid.NewString()

	// A refresh spends the old token: the session carries on, the
	// credential does not, so a stolen refresh token is good for one use
	// before its rightful holder's next refresh reveals the theft.
	//
	// Refreshed decides which token the caller leaves with. Normally that
	// is the one minted above; for a replay inside the grace window it is
	// the successor the winning refresh already produced, so a burst of
	// concurrent refreshes converges on ONE credential instead of
	// refusing all but the first.
	if currentRefreshToken != "" {
		var (
			live      Session
			successor string
			ok        bool
		)
		// The token as the library's first reading of it found it, when
		// that is this request's: the rotation acts on what was read then
		// rather than reading the token and its session a second time.
		if req, isRefresh := request.(*refreshRequest); isRefresh && req.presented.token == currentRefreshToken {
			p := req.presented
			if req.involved, err = s.involveAhead(ctx, request, p.session.Involved); err != nil {
				return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
			}
			p.session.Involved = p.session.Involved || req.involved
			live, successor, ok, err = s.iss.Sessions().rotate(ctx, p, refresh)
		} else {
			live, successor, ok, err = s.iss.Sessions().Refreshed(ctx, currentRefreshToken, refresh)
		}
		if err != nil {
			return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
		}

		if !ok {
			return "", "", time.Time{}, oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
		}

		refresh = successor

		// A renewed access token names its session exactly as the first
		// one does. It did not, so revoking a session stopped `userinfo`
		// for the token minted at sign-in and for none minted after it --
		// and a sign-in exchange, which must see a LIVE session behind
		// the token, could never be offered a token that shows one.
		//
		// Kept only now, once the session is known, so that the record is
		// written once per grant rather than once without the session and
		// again with it.
		issued.Session = live.ID
		if err = s.keep(ctx, request, issued); err != nil {
			return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
		}

		return issued.ID, refresh, issued.Expires, nil
	}

	how := HowCode
	if _, ok := request.(op.TokenExchangeRequest); ok {
		how = HowExchange
	}
	involved, err := s.involveAhead(ctx, request, false)
	if err != nil {
		return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
	}
	if opened, ok := request.(*authRequest); ok {
		opened.involved = involved
	}
	session, err := s.iss.Sessions().Record(ctx, Opened{
		Identity: issued.Subject,
		ClientID: clientOf(request),
		Resource: resourceOf(request),
		How:      how,
		Method:   methodOf(request),
		Token:    refresh,
		Scopes:   request.GetScopes(),
		SSO:      ssoOf(request),
		AuthTime: authTimeOf(request),
		Involved: involved,
		// From the completed request, never from the policy now.
		Class: classOfRequest(request),
	})
	if err != nil {
		return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
	}

	// The sign-in it was opened under, read once the session is filed
	// ([Storage.signInEnded]): a session filed under a sign-in that has
	// ended since is ended here, by the only thing that knows it exists.
	ended, err := s.signInEnded(ctx, session.SSO, session.ClientID)
	if err != nil || ended {
		if _, revokeErr := s.iss.Sessions().RevokeID(ctx, session.ID); revokeErr != nil {
			s.logger().WarnContext(ctx, "a session opened under an ended sign-in could not be ended",
				logattr.SafeError("error", revokeErr))
		}
		if err != nil {
			return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
		}
		return "", "", time.Time{}, errSignInEnded()
	}

	// The ACCESS token names it too, so that revoking the session stops
	// `userinfo` answering with a token already in circulation. Written
	// after the session exists, because that is when its id does -- and
	// only then, once.
	issued.Session = session.ID
	if err = s.keep(ctx, request, issued); err != nil {
		return "", "", time.Time{}, oidc.ErrServerError().WithDescription("%s", err)
	}

	// The ID token minted a moment from now names this session. The
	// library passes this very request object on to CreateIDToken, which
	// is the only reason the id can travel without a second lookup.
	if opened, ok := request.(*authRequest); ok {
		opened.Session = session.ID

		// And remember which session this CODE produced, so that a reuse
		// of it can end that session. RFC 6749 4.1.2: a code used twice
		// must be denied and SHOULD revoke the tokens already issued from
		// it — denying alone leaves a stolen code's first redemption
		// working while telling us it was stolen.
		//
		// Kept only as long as a code lives. After that a second
		// redemption is impossible anyway, and the note would be a record
		// of who signed in with nothing to do.
		if err = s.state.Set(ctx, codeSessionKey(opened.ID), []byte(session.ID), authCodeTTL); err != nil {
			// Not fatal to the sign-in that just succeeded: the person is
			// authenticated, and what is lost is a defence against a reuse
			// that may never come.
			s.logger().WarnContext(ctx, "could not record which session a code opened; "+
				"reusing that code will be denied but will revoke nothing", slog.Any("error", err))
		}
	}

	return issued.ID, refresh, issued.Expires, nil
}

// SetUserinfoFromRequest implements [op.CanSetUserinfoFromRequest]: it is
// what puts a person INTO the ID token.
//
// The library's older hook, SetUserinfoFromScopes, is deprecated and
// empty here — and that emptiness was quietly load-bearing. Every client
// asserts userinfo claims in its ID token, so the library assembles one
// from whatever this storage supplies and assigns the result wholesale.
// Supplying nothing did not leave the ID token's own claims alone; it
// OVERWROTE them, so an ID token arrived carrying no `sub`, no `email`,
// no name and no `groups` — a token that is not merely thin but invalid,
// since `sub` is required of every one. A relying party reading the ID
// token, which is what ArgoCD and Kargo do, saw nobody.
//
// So this fills the same answer the userinfo endpoint gives, plus the one
// claim that belongs to the exchange rather than to the person: `sid`,
// the session this token belongs to. It lets a relying party say WHICH
// of a person's sessions it is holding — the same id the console lists
// and revokes — instead of only that it holds one. A token with no
// session behind it (a workload trading a proof, which opens none)
// carries no `sid` rather than an empty one.
func (s *Storage) SetUserinfoFromRequest(
	ctx context.Context, info *oidc.UserInfo, request op.IDTokenRequest, _ []string,
) error {
	subject := request.GetSubject()

	result, given, family, err := s.resolveSubject(ctx, subject, provedAsServiceAccount(request))
	if err != nil {
		return err
	}
	// An ID token's audience is always the client itself, never a
	// resource — see docs/reference/sluis/policy.md#signing-algorithm-per-audience
	// and [signingAudience]'s own doc comment.
	clientID := request.GetClientID()
	s.reportGroupsScoping(ctx, clientID, clientID, subject, result.Groups)

	claims := scopeClaims(s.iss.Config().GroupsScoping, s.iss.Policy(), Claims(result), clientID, result.Groups)
	claims = applyGroupsDelimiter(s.iss.Policy(), claims, clientID)
	if err = s.fill(ctx, info, subject, claims, given, family); err != nil {
		return err
	}

	if id := sessionOf(request); id != "" {
		info.AppendClaims("sid", id)
	}

	// An ID token is a client being signed somebody in, and the sign-in
	// remembers which clients that happened at, so that ending it can
	// tell each of them. The session index cannot say: a client that
	// asked for `openid` alone holds no refresh token and has no session
	// there, yet it signed somebody in all the same.
	if sso := ssoOf(request); sso != "" && s.iss.SSO() != nil && !involvedAhead(request) {
		if err = s.iss.SSO().Involve(ctx, sso, request.GetClientID()); err != nil {
			return err
		}

		// A code that opened no session is held to its sign-in in
		// [Storage.CreateAccessToken], which records the client first, so
		// this is not reached for one. Kept for an ID token minted with
		// no access token before it: the client is recorded, then the
		// sign-in read ([Storage.signInEnded]), and the whole response
		// refused when it has ended.
		if opened, ok := request.(*authRequest); ok && opened.Session == "" {
			ended, err := s.signInEnded(ctx, sso, opened.Req.ClientID)
			if err != nil {
				return err
			}
			if ended {
				return errSignInEnded()
			}
		}
	}

	return nil
}

// involveAhead records a grant's client among the clients of the sign-in
// it was made under, ahead of the ID token, when the grant opens or
// carries a session: the session record written next can then say so,
// and the refreshes that follow skip the write. It reports whether the
// client is now recorded -- false only for a grant under no sign-in.
//
// Recording it here rather than when the ID token is assembled is the
// same fact a moment earlier: every code redemption and every refresh
// answers with an ID token. One Add already outlasts the sign-in it
// belongs to (both are kept for the sign-in lifetime, the Add from no
// earlier than the sign-in), so adding it again would only keep it past
// the sign-in's end, where nothing reads it.
func (s *Storage) involveAhead(ctx context.Context, request op.TokenRequest, already bool) (bool, error) {
	sso := ssoOf(request)
	if sso == "" || s.iss.SSO() == nil {
		return false, nil
	}
	if !already {
		if err := s.iss.SSO().Involve(ctx, sso, clientOf(request)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// involvedAhead reports whether [Storage.involveAhead] already recorded
// this request's client.
func involvedAhead(request op.IDTokenRequest) bool {
	switch req := request.(type) {
	case *authRequest:
		return req.involved
	case *refreshRequest:
		return req.involved
	default:
		return false
	}
}

// authTimeOf is when the person behind a token request authenticated, and
// the zero time for a request no person is behind. Not every kind of
// token request carries one, which is why it is asked for by shape.
func authTimeOf(request op.TokenRequest) time.Time {
	if with, ok := request.(interface{ GetAuthTime() time.Time }); ok {
		return with.GetAuthTime()
	}

	return time.Time{}
}

// methodOf is how the person behind a token request was proved: a
// provider's kind or [RecoveryHow], as the sign-in recorded it, and
// nothing for an exchange.
func methodOf(request op.TokenRequest) string {
	switch req := request.(type) {
	case *authRequest:
		return req.How
	case *refreshRequest:
		return req.session.Method
	default:
		return ""
	}
}

// provedAsServiceAccount reports whether request's subject was proved as a
// ServiceAccount -- a recovery sign-in, by the method it recorded -- and
// is evaluated by the policy's ServiceAccount matchers rather than asked
// of the directory. Never by the subject's shape: a subject is whatever an
// identity provider said, and one that reads as a ServiceAccount must not
// be granted what the policy grants that ServiceAccount.
//
// An exchange is not asked here: its grant was decided from the verified
// proof ([Storage.ValidateTokenExchangeRequest]) and no subject string is
// evaluated for it.
func provedAsServiceAccount(request any) bool {
	switch req := request.(type) {
	case *authRequest:
		return req.How == RecoveryHow
	case *refreshRequest:
		return req.session.serviceAccount()
	default:
		return false
	}
}

// ssoOf is the browser session a token request was authorized from, and
// nothing for a flow where no browser was involved.
func ssoOf(request op.TokenRequest) string {
	switch req := request.(type) {
	case *authRequest:
		return req.SSO
	case *refreshRequest:
		return req.session.SSO
	default:
		return ""
	}
}

// resourceOf is what a token request asked a token FOR, and nothing when
// it named no resource -- in which case the client is its own audience,
// as it was before resources existed.
func resourceOf(request op.TokenRequest) string {
	switch req := request.(type) {
	case *authRequest:
		return req.Resource
	case *refreshRequest:
		return req.session.Resource
	default:
		return ""
	}
}

// sessionOf is the session a token request belongs to: the one just
// opened for a redeemed code, or the one being renewed.
func sessionOf(request op.IDTokenRequest) string {
	switch req := request.(type) {
	case *authRequest:
		return req.Session
	case *refreshRequest:
		return req.session.ID
	default:
		return ""
	}
}

// issue decides one access token and returns it, unrecorded: [Storage.keep]
// records it, once whatever session it belongs to is known.
func (s *Storage) issue(ctx context.Context, request op.TokenRequest) (*token, error) {
	// Marked before anything else: this is the FIRST storage call the
	// library makes for either a fresh access token or one paired with a
	// refresh (see [op.CreateAccessToken] -> createTokens), and it runs
	// before the library calls [Storage.SigningKey] to actually sign this
	// same access token a moment later — see [signingAudience].
	signingAudienceFrom(ctx).mark(accessAudienceOf(request))
	// And how its subject was proved, for [Storage.GetPrivateClaimsFromScopes],
	// which the library hands the subject string and nothing else.
	signingAudienceFrom(ctx).markSubject(request.GetSubject(), provedAsServiceAccount(request))

	claims, held, given, family, err := s.claimsFor(ctx, request)
	if err != nil {
		return nil, err
	}
	// Covers the ordinary access token, a refresh (this same function,
	// called again with a fresh policy answer — see
	// [Storage.CreateAccessAndRefreshTokens]) and a token exchange's
	// access token alike, because all three reach claims through here.
	// audience is the resource a caller named or the client itself, per
	// [accessAudienceOf]; client is who is ASKING, which differs from
	// audience exactly when a resource was named or an exchange is
	// presenting on somebody else's behalf.
	audience := accessAudienceOf(request)
	s.reportGroupsScoping(ctx, audience, clientOf(request), request.GetSubject(), held)
	// Narrowed BEFORE it is persisted below: this is the claims map
	// [Storage.SetUserinfoFromToken] answers `/userinfo` from later, keyed
	// by this very token's id, so leaving it unscoped here would leave
	// `/userinfo` answering with everything regardless of what the token
	// itself carries — exactly the bypass
	// docs/reference/sluis/policy.md#groups-in-a-token-scoping warns enforce
	// must close. For an exchange, claims is [Grant.Claims], already
	// narrowed once in [Issuer.Exchange]; re-narrowing it here from the
	// SAME held and the SAME audience is a no-op, not a second opinion.
	claims = scopeClaims(s.iss.Config().GroupsScoping, s.iss.Policy(), claims, audience, held)
	claims = applyGroupsDelimiter(s.iss.Policy(), claims, audience)
	lifetime := s.iss.Config().TokenLifetime
	if declared, ok := s.iss.Policy().Client(clientOf(request)); ok {
		lifetime = declared.Cap(lifetime)
	} else if documents := s.documents.allow; documents.Enabled() {
		// A client that describes itself is held to `client_documents`'
		// ttl_cap, as its ID token already is ([Storage.GetClientByClientID]).
		// Read from the policy the resolver admitted it by, as the ID token's
		// is, rather than from the document, which says nothing about its
		// own lifetime.
		if target, err := documentURL(clientOf(request)); err == nil && documents.Permits(target) {
			lifetime = policy.Client{TTLCap: documents.TTLCap}.Cap(lifetime)
		}
	}
	// And the resource's own cap, when the request named one. The SHORTER
	// of the two wins, because each was written by somebody saying "not
	// longer than this" and honouring the longer would answer neither.
	if resource := resourceOf(request); resource != "" {
		if wanted, ok := s.iss.Policy().Resource(resource); ok {
			if capped := wanted.TTLCap.Duration(); capped > 0 && (lifetime == 0 || capped < lifetime) {
				lifetime = capped
			}
		}
	}

	if exchange, ok := request.(op.TokenExchangeRequest); ok {
		if grant, ok := s.grantFor(exchange); ok {
			if capped := time.Duration(s.iss.Lifetime(grant)); capped > 0 && capped < lifetime {
				lifetime = capped
			}
		}
	}

	// An agent chain's tokens are held to the class's own cap, which is
	// mandatory: it is what bounds how long an agent client keeps access
	// after its person is removed, with or without a resource.
	agent := classOfRequest(request) == ClassAgent
	if agent {
		if capped := s.iss.Config().Agent.Access; capped > 0 && (lifetime == 0 || capped < lifetime) {
			lifetime = capped
		}
	}

	now := time.Now()
	expires := now.Add(lifetime)

	// The absolute session limit caps the TOKEN too, not only the session
	// it was minted under: a session capped at auth_time+absolute that
	// still had, say, an hour of TokenLifetime left at the moment it hit
	// the limit would otherwise go on answering `userinfo` and every
	// resource that trusts this token's `exp` for that last hour. A
	// request with no auth_time -- a workload or a machine exchange,
	// which opens no session (see [Sessions.Record]) -- has nothing to
	// cap against, and none applies: the token lives out its ordinary
	// lifetime, exactly as it did before this limit existed.
	//
	// The limit is the one the request's resource allows -- longer than
	// the installation's only for a read-only resource that says so (see
	// [policy.EffectiveAbsolute]) -- so a token never outlives the chain
	// it belongs to, nor is cut short of a chain that was given more.
	//
	// An agent chain's limit is its deadline, as the chain itself is held
	// to it ([Sessions.limitOf]): the one recorded on the session for a
	// refresh, and the one about to be recorded for a code.
	if limit := s.tokenLimit(request, agent); !limit.IsZero() && limit.Before(expires) {
		expires = limit
	}
	if agent {
		// The ID token minted next for this request is held to the same
		// end ([client.IDTokenLifetime]).
		signingAudienceFrom(ctx).markAgentUntil(expires)
	}

	issued := &token{
		ID:       uuid.NewString(),
		Subject:  request.GetSubject(),
		ClientID: clientOf(request),
		Audience: request.GetAudience(),
		Scopes:   request.GetScopes(),
		Claims:   claims,
		Expires:  expires,
	}
	issued.GivenName, issued.FamilyName = given, family
	return issued, nil
}

// tokenLimit is the latest a token for this request may expire: the end of
// the chain it belongs to, or zero for a request with no auth_time (a
// workload or a machine exchange, which opens no session to cap against).
func (s *Storage) tokenLimit(request op.TokenRequest, agent bool) time.Time {
	authTime := authTimeOf(request)
	if authTime.IsZero() {
		return time.Time{}
	}

	if agent {
		if refresh, ok := request.(*refreshRequest); ok {
			return s.iss.Sessions().limitOf(refresh.session)
		}

		if deadline := s.iss.Sessions().agentDeadline(authTime, resourceOf(request)); !deadline.IsZero() {
			return deadline
		}
	}

	if absolute := s.iss.AbsoluteFor(resourceOf(request)); absolute > 0 {
		return authTime.Add(absolute)
	}

	return time.Time{}
}

// keep records an access token [Storage.issue] decided, which is what
// `userinfo` and revocation answer from.
func (s *Storage) keep(ctx context.Context, request op.TokenRequest, issued *token) error {
	// Kept only until it expires: an access token past its lifetime
	// answers nothing, and a store that has to be swept is a store that
	// grows when the sweeper stops.
	if err := setJSON(ctx, s.state, tokenKey(issued.ID), issued, time.Until(issued.Expires)); err != nil {
		return err
	}
	s.recordToken(ctx, request)
	return nil
}

// clientOf reads the client id off whichever kind of request this is.
func clientOf(request op.TokenRequest) string {
	type withClient interface{ GetClientID() string }
	if c, ok := request.(withClient); ok {
		return c.GetClientID()
	}
	return ""
}

// claimsFor is what a token carries beyond its identity fields, what the
// account is called, and which internal groups it holds — from ONE
// answer, because they come from one. held is the FULL held list, not
// what claims["groups"] actually carries: [Storage.issue] reports it to
// [Storage.reportGroupsScoping], which is the one place that narrows it
// for the LOG line, never for the token.
func (s *Storage) claimsFor(
	ctx context.Context, request op.TokenRequest,
) (claims map[string]any, held []string, given, family string, err error) {
	if exchange, ok := request.(op.TokenExchangeRequest); ok {
		// An exchange is usually a job or a workload, which has no name.
		// A person trading one of their own tokens does, and the grant
		// decided what they may have without asking the directory what
		// they are called — so that stays a separate question here.
		claims, err = s.GetPrivateClaimsFromTokenExchangeRequest(ctx, exchange)
		if err != nil {
			return nil, nil, "", "", err
		}
		if grant, ok := s.grantFor(exchange); ok {
			held = grant.Result.Groups
		}

		given, family = s.namesOf(ctx, request.GetSubject())

		return claims, held, given, family, nil
	}

	return s.identityOf(ctx, request.GetSubject(), provedAsServiceAccount(request))
}

// refuseAtAbsoluteLimit ends a session that has reached its absolute limit
// and answers the refresh that found out, with the reason in the log and
// the audit record rather than the generic "not live".
func (s *Storage) refuseAtAbsoluteLimit(ctx context.Context, ended Session) error {
	if revokeErr := s.iss.Sessions().deleteSession(ctx, ended); revokeErr != nil {
		s.logger().WarnContext(ctx, "a session past the absolute limit could not be revoked",
			slog.Any("error", revokeErr))
	}

	s.logger().InfoContext(ctx, "refused a refresh past the absolute session limit",
		logattr.SafeString("client_id", ended.ClientID))
	s.iss.record(ctx, audit.SessionRefreshRefused(ended.Identity, ended.ClientID,
		"the absolute session limit was reached"))

	return oidc.ErrInvalidGrant().WithDescription(
		"this session has reached its absolute limit and must sign in again")
}

// endReuse answers a refresh token spent longer ago than the grace window:
// it ends the session the token was spent in and refuses the grant with
// invalid_grant.
//
// Refresh token rotation exists for this (RFC 9700 section 4.14.2). A
// token stolen and then used by both its thief and its rightful holder is
// presented twice, and the second presentation is of a token the first
// already spent. Which of the two is the thief cannot be told, so the
// session goes: the successor stops refreshing whoever holds it, and the
// person signs in again. Inside the grace window the second presentation
// is a client's own burst of concurrent refreshes, and is answered
// instead ([Sessions.replayedTo]).
//
// It runs here, where the library creates the tokens, and not where it
// first reads the refresh token ([Storage.TokenRequestByRefreshToken]):
// by now the library has authenticated the client and matched it to the
// session's, so a spent token presented under some other client is
// refused there with nothing ended.
//
// The session ends as [Sessions.RevokeID] ends one: gone from every
// listing, every refresh token of it dead, the access tokens naming it
// refused by userinfo. A client that asked for back-channel logout is
// told. The browser sign-in the session was opened under is left alone, so
// that a false positive costs one client's session and not every one.
//
// Ending it is a delete of the record at the revision read
// ([Sessions.endReused]), read again and repeated while refreshes of the
// successor keep writing it, so holding the successor and refreshing it
// does not dodge the revocation. Several presentations of one spent token
// at once all detect the reuse; the one whose delete lands ends the
// session, and only it is audited and announced. The others find the
// record gone and are refused as any spent token is. The audit record
// follows the delete that decides this rather than preceding it: written
// first, every presentation would write one, and a delete that then failed
// would leave a record of an end that did not happen. A delete that fails
// with the session still live is a server error rather than a refusal, so
// that the client tries again and the reuse is met again.
func (s *Storage) endReuse(ctx context.Context, p presented) error {
	recordReuse(ctx, "refresh_token")
	session := p.session

	ended, err := s.iss.Sessions().endReused(ctx, p)
	if !ended {
		if err != nil {
			s.logger().WarnContext(ctx, "a spent refresh token was reused and its session could not be ended",
				logattr.SafeError("error", err))

			return oidc.ErrServerError().WithDescription("%s", err)
		}

		// Another presentation of the same token ended it, or this one's
		// own delete did and was reported missing on a retry: ended,
		// and not this call's to audit.
		return oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
	}

	s.iss.record(ctx, audit.SessionReuseRevoked(session.Identity, session.ClientID))
	if err != nil {
		// Ended -- the record is gone, and every token resolves through it
		// -- with an index set still naming it, which the next listing
		// drops.
		s.logger().WarnContext(ctx, "a reused session was ended and an index set still names it",
			logattr.SafeError("error", err))
	}

	// WARN, as for a reused authorization code: a broken client or a
	// stolen token, and both are worth seeing.
	s.logger().WarnContext(ctx, "a spent refresh token was reused after its grace window; its session has been ended",
		logattr.SafeString("client_id", session.ClientID))
	s.announceLogout(ctx, s.logger(), []Session{session})

	return oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
}

// TokenRequestByRefreshToken implements [op.AuthStorage].
func (s *Storage) TokenRequestByRefreshToken(ctx context.Context, refreshToken string) (op.RefreshTokenRequest, error) {
	// A token confirmed dead is refused again before anything is read: a
	// host looping on an ended chain costs no State call ([deadRefreshes]).
	sum := s.dead.sum(refreshToken)
	if s.dead.refused(sum) {
		recordDeadRefreshHit(ctx)
		recordReuse(ctx, "refresh_token")

		return nil, op.ErrInvalidRefreshToken
	}

	// Resolved as a refresh resolves it, not with ByToken: this is the
	// library's FIRST reading of the token on a refresh, so a replay
	// inside the grace window has to resolve here or it is refused before
	// CreateAccessAndRefreshTokens can answer it. And it is the ONLY
	// reading: what was read travels on the request to the rotation.
	presented, ok, err := s.iss.Sessions().present(ctx, refreshToken)
	if !presented.dead {
		// Anything but a dead verdict -- a live token, a replay, a reuse,
		// a refusal that is not terminal, a failed read -- undoes a
		// pending one.
		s.dead.alive(sum)
	}

	if err != nil {
		// An outage, not an answer about the token: server_error on the
		// wire, which the library would turn into invalid_grant if it were
		// returned here ([refreshRequest.refusal]).
		return refusedLater(ctx, oidc.ErrServerError().WithDescription("%s", err)), nil
	}

	session := presented.session

	if presented.reused != "" {
		// Not ended here: the library has yet to match the client to the
		// session's. The request carries the session to where it has
		// ([Storage.endReuse]), and can never mint anything.
		return &refreshRequest{session: session, presented: presented}, nil
	}

	if !ok {
		// Distinguish a refresh refused BY THE ABSOLUTE LIMIT from one
		// refused because the session is simply gone (revoked, or an
		// ordinary sliding-window timeout): the first is a moment worth
		// its own reason in the log and the audit record, exactly as a
		// refresh refused for a client the identity no longer belongs to
		// is, below. See [Sessions.endedByAbsoluteLimit] for why the two
		// are tellable apart at all.
		//
		// A dead token has no record to find there, so it is not asked.
		if !presented.dead {
			if ended, hit, endErr := s.iss.Sessions().endedByAbsoluteLimit(ctx, refreshToken); endErr == nil && hit {
				return nil, s.refuseAtAbsoluteLimit(ctx, ended)
			}
		}

		// A token that is neither live nor spent in a session that is still
		// known: never issued, expired, spent by the version before this one
		// (whose mark lasted the grace window alone), or spent in a session
		// that has since ended. Counted with the reuses, as it always was.
		recordReuse(ctx, "refresh_token")
		s.refuseDead(ctx, sum, presented.dead)

		return nil, op.ErrInvalidRefreshToken
	}

	// The limit as the policy states it NOW. A record carries the end it
	// was given when last written; a policy that has since withdrawn an
	// extension must end the chain here, not at the longer end it holds.
	if s.iss.Sessions().pastLimit(session) {
		return nil, s.refuseAtAbsoluteLimit(ctx, session)
	}

	// The client's `requires`, again. Checking it only at sign-in would
	// make the gate good for as long as a refresh token lives: somebody
	// taken out of the group would keep renewing for up to twelve hours
	// against a client that is no longer theirs. Here it ends at the next
	// refresh, which for a proxied console is its `ttl_cap`.
	//
	// invalid_grant rather than a quieter refusal, because that is the
	// answer a relying party acts on: it stops renewing and starts a new
	// authorization, which meets the same gate and says why on a page.
	// The description is deliberately plain -- the detail is in the log,
	// and the client is not who needs telling.
	if err = s.entitled(ctx, session.ClientID, session.Resource, session.Identity, session.serviceAccount()); err != nil {
		if errors.Is(err, ErrNotEntitled) {
			s.logger().InfoContext(ctx, "refused a refresh for a client the identity is no longer entitled to",
				logattr.SafeString("client_id", session.ClientID), logattr.SafeError("error", err))
			// A refresh is not an event; a refused one is — it is the
			// moment somebody taken out of a group lost a client.
			s.iss.record(ctx, audit.SessionRefreshRefused(session.Identity, session.ClientID,
				"no longer admitted to any group this client requires"))

			return nil, oidc.ErrInvalidGrant().WithDescription("this identity is not admitted to this client")
		}

		// The directory said, and could vouch for it, that the person is
		// suspended or gone: a removal, which ends the chain rather than
		// leaving it to idle out. A refusal it cannot vouch for is an
		// outage, which ends nothing and asks the client to try again.
		//
		// Both are carried on the request rather than returned here. The
		// library answers every error of this first reading with
		// invalid_grant, which would tell a client to give up in an outage,
		// and it has not yet matched the authenticated client to the
		// session's: a removal must not end a session for whoever holds a
		// copy of its token. [Storage.CreateAccessAndRefreshTokens] acts on
		// them, after that match, and its errors reach the wire as they are.
		var refused *Refused
		if errors.As(err, &refused) && refused.Authoritative {
			return &refreshRequest{session: session, presented: presented, notLive: true}, nil
		}

		return &refreshRequest{
			session: session, presented: presented,
			refusal: oidc.ErrServerError().WithDescription("%s", err),
		}, nil
	}

	return &refreshRequest{session: session, presented: presented}, nil
}

// refusedLater is a request that carries a refusal and nothing else, for a
// token whose session could not be read. It names the client and the scopes
// the request named, so that the library's checks between this reading and
// [Storage.CreateAccessAndRefreshTokens] pass it on to where the refusal is
// returned; it can never mint.
func refusedLater(ctx context.Context, refusal error) *refreshRequest {
	asked := presentedFrom(ctx)

	return &refreshRequest{session: Session{ClientID: asked.clientID, Scopes: asked.scopes}, refusal: refusal}
}

// refusalOf is what a refresh request carries instead of a grant: the
// removal decision 5 ends a session for, or another refusal to return as it
// is. Nil for anything else.
func (s *Storage) refusalOf(ctx context.Context, request op.TokenRequest) error {
	req, isRefresh := request.(*refreshRequest)
	switch {
	case !isRefresh:
		return nil
	case req.notLive:
		return s.refuseNotLive(ctx, req.presented)
	default:
		return req.refusal
	}
}

// refuseDead says that a refresh token naming no live session was refused:
// the client the request named and a fingerprint of the token, never the
// token. A dead verdict makes a cache entry, and the WARN is written when it
// is made; the entry's repeats are refused without it (the library still
// logs its own `request error` line for every invalid_grant), counted, and
// from memory once a second verdict has confirmed it ([deadRefreshes]). A
// refusal that is not dead (a mark in its tolerance band, a successor that
// is no longer live) makes no entry and warns each time. Every such WARN is
// rate-limited across all tokens, and says how many it held back.
func (s *Storage) refuseDead(ctx context.Context, sum [sha256.Size]byte, dead bool) {
	if dead && !s.dead.dead(sum) {
		return
	}

	write, held := s.dead.warn()
	if !write {
		return
	}

	attrs := []slog.Attr{
		logattr.SafeString("client_id", presentedFrom(ctx).clientID),
		slog.String("token_fingerprint", fingerprint(sum)),
		slog.Bool("remembered", dead),
	}
	if held > 0 {
		attrs = append(attrs, slog.Int("suppressed", held))
	}

	s.logger().LogAttrs(ctx, slog.LevelWarn, "refused a refresh token that names no live session", attrs...)
}

// refuseNotLive ends a session whose person the directory authoritatively
// reports suspended or not found, at the refresh that found out, once the
// library has matched the client to the session's: the record goes at the
// revision read, as a reuse ends one ([Sessions.endRemoved]), then its index
// membership and the presented token's pointer (and a grace replay's
// successor's), and the client is told invalid_grant
// (docs/decisions/0040-agent-class-sessions.md, decision 5). Every class
// alike: nothing that ends a session consults it.
//
// Only the call whose delete ended the session audits it and announces it
// over back-channel logout, as every other revocation does: of a burst of
// presentations, the others find it gone and are refused as any dead token
// is. A delete that failed with the session still live is a server error, so
// that the client tries again and the removal is met again.
func (s *Storage) refuseNotLive(ctx context.Context, p presented) error {
	ended, err := s.iss.Sessions().endRemoved(ctx, p)
	if !ended {
		if err != nil {
			s.logger().WarnContext(ctx, "a session whose person the directory no longer has could not be ended",
				logattr.SafeString("client_id", p.session.ClientID), logattr.SafeError("error", err))

			return oidc.ErrServerError().WithDescription("%s", err)
		}

		return oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
	}

	session := p.session
	if err != nil {
		// Ended -- the record is gone, and every token resolves through it
		// -- with an index set still naming it, which the next listing
		// drops.
		s.logger().WarnContext(ctx, "an ended session's index sets still name it",
			logattr.SafeString("client_id", session.ClientID), logattr.SafeError("error", err))
	}

	for _, token := range []string{p.token, p.successor} {
		if token == "" {
			continue
		}

		if err = s.iss.Sessions().state.Delete(ctx, sessionTokenKey(token)); err != nil {
			// Ended all the same: every token resolves through the record.
			s.logger().WarnContext(ctx, "an ended session's refresh token pointer could not be removed",
				logattr.SafeError("error", err))
		}
	}

	s.logger().InfoContext(ctx, "refused a refresh: the directory says the person is not live; the session has been ended",
		logattr.SafeString("client_id", session.ClientID))
	s.iss.record(ctx, audit.SessionRefreshRefused(session.Identity, session.ClientID,
		"the directory says this account is not live"))
	s.announceLogout(ctx, s.logger(), []Session{session})

	return oidc.ErrInvalidGrant().WithDescription("the refresh token is not live")
}

// refreshRequest is a live session presented for renewal.
type refreshRequest struct {
	session Session
	scopes  []string
	// presented is the token as it was read, which the rotation acts on.
	presented presented
	// involved is set once the client is known to be recorded among the
	// sign-in's clients ([Storage.involveAhead]).
	involved bool
	// notLive is set when the directory authoritatively reported the
	// person suspended or not found, and refusal when the refresh is
	// refused for another reason: either is acted on where the library
	// creates the tokens ([Storage.refusalOf]), and the request mints
	// nothing.
	notLive bool
	refusal error
}

var _ op.RefreshTokenRequest = (*refreshRequest)(nil)

func (r *refreshRequest) GetAMR() []string { return []string{"pwd"} }

// GetAudience is the resource this session was opened for, or the client
// itself. Without the first case a refresh would quietly re-mint the
// token for the CLIENT while the original named a resource, changing what
// the token is for halfway through a session.
func (r *refreshRequest) GetAudience() []string {
	if r.session.Resource != "" {
		return []string{r.session.Resource}
	}
	return []string{r.session.ClientID}
}
func (r *refreshRequest) GetClientID() string         { return r.session.ClientID }
func (r *refreshRequest) GetSubject() string          { return r.session.Identity }
func (r *refreshRequest) SetCurrentScopes(s []string) { r.scopes = s }

// GetAuthTime is when the person behind this session authenticated —
// carried down from the browser session at sign-in, not the moment this
// session was opened and never the moment of this refresh. A session
// recorded before the field existed falls back to when it was issued,
// which is what the code answered for every session until now.
func (r *refreshRequest) GetAuthTime() time.Time {
	if !r.session.AuthTime.IsZero() {
		return r.session.AuthTime
	}

	return r.session.IssuedAt
}

// GetScopes is what this session was granted, until a narrower set is
// asked for and accepted.
//
// Answering nil here was wrong twice over: a refresh naming any scope at
// all was refused as though it had asked for more than it held, and one
// naming none produced an ID token with no `email` and no name, because
// the library assembles those from the scopes it is given. A relying
// party that shows who is signed in would have shown an address for an
// hour and then nothing.
func (r *refreshRequest) GetScopes() []string {
	if r.scopes != nil {
		return r.scopes
	}

	return r.session.Scopes
}

// GetRefreshTokenInfo implements [op.AuthStorage].
func (s *Storage) GetRefreshTokenInfo(ctx context.Context, _ string, tok string) (string, string, error) {
	// The same window. Revocation resolves through here too, and a
	// caller revoking with the token it holds must be obeyed even when a
	// refresh rotated it a moment earlier -- refusing would leave the
	// session open, which is the wrong way for a revocation to fail.
	session, ok, err := s.iss.Sessions().ByRefreshToken(ctx, tok)
	if err != nil {
		return "", "", oidc.ErrServerError().WithDescription("%s", err)
	}

	if !ok {
		return "", "", op.ErrInvalidRefreshToken
	}
	return session.Identity, session.ID, nil
}

// RevokeToken ends a session or an access token. It is RFC 7009, and it
// is the mechanism under both Revoke in the console and a person's own
// sign-out-everywhere.
func (s *Storage) RevokeToken(ctx context.Context, tokenOrTokenID, _, _ string) *oidc.Error {
	// The library resolves a refresh token through GetRefreshTokenInfo
	// first and then hands back what that returned, so this is the
	// session id for a refresh token and the raw value for anything else.
	// Both have to work, or revocation silently succeeds while the
	// session lives on — which is the worst possible outcome for a
	// security control whose entire job is to end access.
	// Both, always, and in that order — never one *or* the other. A hit
	// on either is not proof that revocation is done: the access token
	// lives under its own key, and leaving it there would keep it valid
	// at every replica. A security control that reports success and
	// leaves access in place is worse than one that fails.
	if _, err := s.iss.Sessions().RevokeToken(ctx, tokenOrTokenID); err != nil {
		return oidc.ErrServerError().WithDescription("%s", err)
	}

	if _, err := s.iss.Sessions().RevokeID(ctx, tokenOrTokenID); err != nil {
		return oidc.ErrServerError().WithDescription("%s", err)
	}

	if err := s.state.Delete(ctx, tokenKey(tokenOrTokenID)); err != nil {
		return oidc.ErrServerError().WithDescription("%s", err)
	}
	// A token that was already gone is a success: revocation is
	// idempotent, and saying otherwise tells a caller whether a token
	// they do not hold ever existed.
	return nil
}

// TerminateSession is where the library lands RP-initiated logout, and
// it deliberately ends NOTHING. [SignOut] has already run by then and has
// ended the sign-in this browser holds and every session opened under it.
//
// It ends nothing because it cannot tell whose logout this is. The
// library calls it with the subject and client taken from the request,
// and a request can say anything:
//
//   - Nothing at all. No `id_token_hint` and no `client_id` leaves both
//     arguments empty, and an empty [Query] selects EVERY session in the
//     installation. That was live: one unauthenticated GET, by anyone, to
//     an address the discovery document publishes, ended every session
//     every person and every workload held.
//   - An `id_token_hint` belonging to somebody else. The specification
//     calls it a hint, not a credential, and the library accepts an
//     EXPIRED one by design (`IDTokenHintExpiredError` is tolerated in
//     ValidateEndSessionRequest). Old ID tokens sit in logs, in browser
//     history and in referrer headers, so honouring one as authority to
//     revoke hands anybody who finds one a way to sign that person out.
//
// What the request CAN prove is the cookie it carries, and that is what
// [SignOut] acts on. The hint keeps its real job — deciding which
// client's `signed_out` page the person lands on — which the library does
// without any help from here.
//
// The narrower job this used to do, ending one identity's sessions at one
// client, is served by `RevokeSessions` on the session service, which
// authorizes the caller before it acts.
func (s *Storage) TerminateSession(ctx context.Context, identity, clientID string) error {
	s.logger().DebugContext(ctx, "end_session revokes nothing here; the browser's sign-in decides",
		logattr.SafeString("identity", identity), logattr.SafeString("client_id", clientID))

	return nil
}

// --------------------------------------------------------------- claims

// SetUserinfoFromScopes implements [op.OPStorage].
func (s *Storage) SetUserinfoFromScopes(context.Context, *oidc.UserInfo, string, string, []string) error {
	// Deprecated in the library in favour of SetUserinfoFromToken; an
	// empty implementation is what it asks for.
	return nil
}

// SetUserinfoFromToken implements [op.OPStorage].
//
// A revoked session's access token is refused here, which is the one
// place a revocation can reach a token already in circulation. Access
// tokens are JWTs verified offline by everything else, so until they
// expire nothing else can be told to stop honouring one -- and this
// endpoint holds the record anyway, so asking costs a lookup.
//
// Conformance found it through the narrowest door: a reused
// authorization code must revoke what it issued (RFC 6749 4.1.2), we
// revoked the session, and `userinfo` went on answering with the access
// token from the first redemption. Fixing only that case would have left
// every OTHER revocation with the same hole.
func (s *Storage) SetUserinfoFromToken(ctx context.Context, info *oidc.UserInfo, tokenID, _, _ string) error {
	issued, err := getJSON[token](ctx, s.state, tokenKey(tokenID))
	if err != nil {
		return err
	}

	if issued == nil {
		return errors.New("no such token")
	}

	if issued.Session != "" {
		_, live, err := s.iss.Sessions().ByID(ctx, issued.Session)
		if err != nil {
			return err
		}

		if !live {
			return errors.New("the session this token was issued under has ended")
		}
	}

	return s.fill(ctx, info, issued.Subject, issued.Claims, issued.GivenName, issued.FamilyName)
}

// SetIntrospectionFromToken implements [op.OPStorage].
func (s *Storage) SetIntrospectionFromToken(context.Context, *oidc.IntrospectionResponse, string, string, string) error {
	// Introspection is deliberately not served: access tokens are JWTs
	// verified offline against the JWKS, so an endpoint that answers
	// questions about them is surface with no consumer.
	return errors.New("introspection is not served by this issuer")
}

// GetPrivateClaimsFromScopes implements [op.OPStorage]: what an ACCESS
// token carries beyond the registered claims.
//
// The policy's claims, and the person's names beside them. An access
// token is what reaches an application -- a gateway forwards it, a proxy
// forwards it -- and an application showing who is signed in has only
// this token to read it from. Without the names it had to call userinfo
// on every page, which is a round trip to the issuer for a display string
// and a dependency on the issuer for rendering at all. The ID token has
// carried them all along; this is the same answer, from the same one
// directory call.
func (s *Storage) GetPrivateClaimsFromScopes(ctx context.Context, subject, _ string, _ []string) (map[string]any, error) {
	// held is not reported here: this hook computes the SAME answer
	// [Storage.issue] already reported a moment earlier on the same
	// request context — see [signingAudience] — so a second line would
	// only repeat the first, not add a finding. It IS narrowed here,
	// though, unreported or not: this is the map the library actually
	// signs into the access token's own JWT (see op/token.go's
	// CreateJWT), a SEPARATE evaluation from the one [Storage.issue]
	// persisted for `/userinfo` a moment before — so leaving this one
	// unscoped would hand the bearer itself the full, unscoped set even
	// though every other reading of the same token was narrowed.
	//
	// signingAudienceFrom reads back the SAME audience [Storage.issue]
	// marked moments ago on this request's context, for the same reason
	// [Storage.SigningKey] does a moment after this returns: this hook is
	// handed the subject and the scopes, never the audience, so the
	// carrier is the only way to learn it here.
	//
	// How the subject was proved comes the same way, from [Storage.issue]:
	// this hook is handed the subject string alone, and the string's shape
	// is not a proof of anything. A carrier [Storage.issue] did not mark
	// for this subject reads as a person, asked of the directory.
	claims, held, given, family, err := s.identityOf(ctx, subject, signingAudienceFrom(ctx).subjectIsServiceAccount(subject))
	if err != nil {
		return nil, err
	}

	if audience, ok := signingAudienceFrom(ctx).get(); ok {
		claims = scopeClaims(s.iss.Config().GroupsScoping, s.iss.Policy(), claims, audience, held)
		claims = applyGroupsDelimiter(s.iss.Policy(), claims, audience)
	}

	return withNames(claims, given, family), nil
}

// withNames returns claims with the person's names added, leaving the map
// it was given alone. Absent names add nothing: a workload and a recovery
// sign-in have none, and an empty `name` would read as a person called
// nothing.
func withNames(claims map[string]any, given, family string) map[string]any {
	name := displayName(given, family)
	if name == "" {
		return claims
	}

	out := make(map[string]any, len(claims)+3)
	for key, value := range claims {
		out[key] = value
	}

	out["name"] = name
	if given != "" {
		out["given_name"] = given
	}
	if family != "" {
		out["family_name"] = family
	}

	return out
}

// displayName is the one way a person's name is put together, for the ID
// token and the access token alike.
func displayName(given, family string) string {
	switch {
	case given != "" && family != "":
		return given + " " + family
	case given != "":
		return given
	default:
		return family
	}
}

// identityOf asks the directory ONCE and returns everything one answer
// contains: what the policy grants this identity, and what the directory
// calls it.
//
// Once matters. The hub call behind this is the issuer's hottest, and
// every claim a token carries comes from the same answer — so asking
// twice is not only two round trips where the design counted on one, it
// is two answers that can disagree, with the grants from before a change
// and the name from after.
//
// A recovery sign-in (serviceAccount, by how it was proved -- never by
// what its subject looks like) is a ServiceAccount, and the hub is the
// wrong place to ask about it: it holds directories, and this is not a
// person in one. The policy's `service_account` matchers decide, exactly
// as they do for a workload exchanging a token -- one table, one
// evaluation, and nothing here that a matcher did not grant. It has no
// name, which is the truthful answer rather than a missing one.
func (s *Storage) identityOf(
	ctx context.Context, subject string, serviceAccount bool,
) (claims map[string]any, held []string, given, family string, err error) {
	result, given, family, err := s.resolveSubject(ctx, subject, serviceAccount)
	if err != nil {
		return nil, nil, "", "", err
	}

	return Claims(result), result.Groups, given, family, nil
}

// resolveSubject evaluates the policy for one subject and returns the
// RESULT rather than the claims made from it, because two questions are
// asked of it: what a token should say, and whether this identity may
// have a token for a particular client at all.
//
// serviceAccount says the subject was PROVED as a ServiceAccount (see
// [provedAsServiceAccount]); only then do the policy's ServiceAccount
// matchers decide. Anybody else is asked of the directory, whatever their
// subject looks like.
func (s *Storage) resolveSubject(
	ctx context.Context, subject string, serviceAccount bool,
) (result policy.Result, given, family string, err error) {
	if serviceAccount {
		account, ok := serviceAccountSubject(subject)
		if !ok {
			return policy.Result{}, "", "", &Refused{Reason: "a ServiceAccount sign-in whose subject names no ServiceAccount"}
		}
		return s.iss.Policy().Evaluate(policy.Input{ServiceAccount: &account}), "", "", nil
	}

	resolved, err := s.iss.resolver.Resolve(ctx, subject)
	if err != nil {
		return policy.Result{}, "", "", err
	}

	return s.iss.Policy().Evaluate(resolved.Input(subject)),
		resolved.GivenName, resolved.FamilyName, nil
}

// ErrNotEntitled says the person is who they claim to be and may not have
// a token for THIS client: they hold none of the groups it requires.
//
// Distinct from every other refusal on this path, because it is the only
// one where signing in again cannot help and the person has done nothing
// wrong. It is an answer about entitlement, not about the request.
var ErrNotEntitled = errors.New("no group this client requires")

// entitled is the client's `requires`, applied to a browser sign-in.
//
// It was applied on token exchange and nowhere else, so `requires` on a
// browser client was documentation: anybody the issuer would authenticate
// was issued a token for any declared client, and what stopped them was
// whatever the application checked for itself. For a console with no
// authorization of its own -- hubble -- nothing did.
//
// Here rather than at /authorize because there is nobody to judge until
// the sign-in finishes: the request arrives before anyone has proved who
// they are.
func (s *Storage) entitled(ctx context.Context, clientID, resource, subject string, serviceAccount bool) error {
	declared, ok := s.iss.Policy().Client(clientID)
	if !ok {
		// A client that describes itself carries the document policy's
		// groups, which the resolver already put on it -- but it is not in
		// the policy, so it is looked up the same way the lookup does.
		if resolved, err := s.documents.Resolve(ctx, clientID); err == nil {
			declared = resolved
		} else {
			// Not this function's refusal to make: an undeclared client is
			// refused before a person is ever asked to sign in.
			return nil
		}
	}

	result, _, _, err := s.resolveSubject(ctx, subject, serviceAccount)
	if err != nil {
		return err
	}

	if !declared.Admits(result) {
		return fmt.Errorf("%w: %s holds none of %v", ErrNotEntitled, subject, declared.Requires)
	}

	// THE TWO GATES COMPOSE. The client's says who may ask; a resource's
	// says what may be asked for, and a caller has to satisfy both. Only
	// the client's applied while a client was always its own audience --
	// with a resource, checking only the client would let anybody who may
	// use an editor reach every service that editor can name.
	if resource == "" {
		return nil
	}
	wanted, ok := s.iss.Policy().Resource(resource)
	if !ok {
		// Validated before the library was reached, so this is a resource
		// that was withdrawn between the request and the sign-in.
		return fmt.Errorf("%w: %q is no longer a declared resource", ErrNotEntitled, resource)
	}
	if !wanted.Admits(result) {
		return fmt.Errorf("%w: %s holds none of %v, which %q requires",
			ErrNotEntitled, subject, wanted.Requires, resource)
	}
	return nil
}

// serviceAccountSubject reads a ServiceAccount out of a subject, in
// whichever spelling minted it. [policy.ParseServiceAccountSubject] is
// the one place that knows there is more than one.
func serviceAccountSubject(subject string) (policy.ServiceAccountRef, bool) {
	return policy.ParseServiceAccountSubject(subject)
}

func (s *Storage) fill(
	_ context.Context, info *oidc.UserInfo, subject string, claims map[string]any, given, family string,
) error {
	info.Subject = subject

	if strings.Contains(subject, "@") {
		info.Email = subject
		info.EmailVerified = true
		// The address is also the username a relying party shows when it
		// has nothing better, and it is what this issuer's subject IS for
		// a person -- so saying it twice costs nothing and spares every
		// consumer a fallback.
		info.PreferredUsername = subject
	}

	// A person, when the directory said so. `name` is the whole of what a
	// UI usually renders, and given/family are there for the ones that
	// want the halves; none of it is authorization, and all of it is
	// absent for a workload or a recovery sign-in, which have no names to
	// give.
	info.GivenName, info.FamilyName = given, family
	info.Name = displayName(given, family)

	for name, value := range claims {
		info.AppendClaims(name, value)
	}

	return nil
}

// namesOf asks the directory what this account is called, for the claims
// that name a person. A failure is not one: the token is about what the
// identity may do, and a UI that shows an address instead of a name is a
// smaller thing than a login that did not happen.
func (s *Storage) namesOf(ctx context.Context, subject string) (given, family string) {
	if !strings.Contains(subject, "@") {
		return "", ""
	}

	resolved, err := s.iss.resolver.Resolve(ctx, subject)
	if err != nil {
		return "", ""
	}

	return resolved.GivenName, resolved.FamilyName
}

// GetKeyByIDAndClientID implements [op.OPStorage].
func (s *Storage) GetKeyByIDAndClientID(context.Context, string, string) (*jose.JSONWebKey, error) {
	return nil, errors.New("the JWT profile for client authentication is not wired in the spike")
}

// ValidateJWTProfileScopes implements [op.OPStorage].
func (s *Storage) ValidateJWTProfileScopes(_ context.Context, _ string, scopes []string) ([]string, error) {
	return scopes, nil
}

// ------------------------------------------------------------- exchange

// VerifyExchangeSubjectToken is where the issuer's trust in another
// system begins and ends: a GitHub identity token checked against
// GitHub's keys and the organisation allow-list, or a ServiceAccount
// token checked with TokenReview. What comes back is a proof, not a
// permission.
func (s *Storage) VerifyExchangeSubjectToken(
	ctx context.Context, tok string, tokenType oidc.TokenType,
) (string, string, map[string]any, error) {
	if s.verify == nil {
		return "", "", nil, ErrUnverified
	}
	proof, err := s.verify.Verify(ctx, tok, string(tokenType))
	if err != nil {
		return "", "", nil, err
	}
	subject := proof.Subject()
	if subject == "" {
		return "", "", nil, ErrUnverified
	}
	return tok, subject, map[string]any{verifiedKey: verified{proof}}, nil
}

// VerifyExchangeActorToken is not served: delegation, where one party
// acts for another, is a second principal in a token and this design has
// exactly one.
func (s *Storage) VerifyExchangeActorToken(
	context.Context, string, oidc.TokenType,
) (string, string, map[string]any, error) {
	return "", "", nil, errors.New("acting for another party is not served by this issuer")
}

// verified carries a proof from [Storage.VerifyExchangeSubjectToken] to
// [Storage.ValidateTokenExchangeRequest], through the library, as a Go
// value.
//
// A value and never claims, because the library fills the same map from
// tokens THIS issuer signed, and a verifier's proof must not be something
// such a token's contents could spell. Nothing decoded from JSON can be
// this type, so a proof exists only where a verifier made one.
type verified struct{ proof Proof }

// verifiedKey is where that value sits in the claims map.
const verifiedKey = "access-roster:verified-proof"

// ValidateTokenExchangeRequest is the gate. The requested audience is a
// client; the proof resolves to internal groups; the client's `requires`
// decides. Refusing here is the whole of the rule-gated audience design,
// because a cloud trust policy for a custom issuer can see only sub, aud,
// amr and email — so the decision has to ride in the audience, and an
// audience wrongly minted is an audience wrongly trusted.
func (s *Storage) ValidateTokenExchangeRequest(ctx context.Context, request op.TokenExchangeRequest) error {
	audiences := request.GetAudience()
	if len(audiences) != 1 {
		return oidc.ErrInvalidTarget().WithDescription("name exactly one audience: it is the decision")
	}

	proof, err := s.proofOf(ctx, request)
	if err != nil {
		return err
	}

	grant, err := s.iss.Exchange(ctx, proof, audiences[0])
	switch {
	case errors.Is(err, ErrUnknownTarget), errors.Is(err, ErrNoTarget):
		return oidc.ErrInvalidTarget().WithDescription("%s", err)
	case errors.Is(err, ErrRefused):
		return oidc.ErrInvalidTarget().WithDescription("%s", err)
	case err != nil:
		return oidc.ErrInvalidGrant().WithDescription("%s", err)
	}

	// The subject is the proof's, never the caller's, and the token is
	// good for one audience only.
	request.SetSubject(grant.Subject)
	// An access token and nothing else: an exchange must not hand back a
	// refresh token. The proof a job presented is short-lived by design —
	// GitHub mints it per run — and trading it for a credential that
	// outlives the run would undo that, leaving a standing key on a
	// machine whose whole appeal is that it holds none. When the token
	// expires the job asks again, or it is over.
	request.SetRequestedTokenType(oidc.AccessTokenType)
	s.grants.Store(request, grant)
	return nil
}

// proofOf is what the subject token proves, and the only two answers
// there are.
//
// A verifier's proof: a GitHub job or a cluster's workload, checked
// against their own keys on the way in.
//
// Or a person's sign-in, which is a token this issuer signed and the
// library has already checked for signature and expiry -- and nothing
// else. That is not enough to trust it as the person, because most of
// this issuer's tokens are handed to somebody else: an ID token to every
// relying party, an access token to whatever a gateway forwards it to. So
// it is a proof only as the access token of a live session, at a client
// that allows [policy.Client.SignInExchange], presented by that client.
func (s *Storage) proofOf(ctx context.Context, request exchangeSubject) (Proof, error) {
	if carried, ok := request.GetExchangeSubjectTokenClaims()[verifiedKey].(verified); ok {
		return carried.proof, nil
	}

	return s.signInProof(ctx, request)
}

// exchangeSubject is the part of an exchange a proof is read from: the
// subject token as the library verified it, and the client presenting it.
// Narrower than [op.TokenExchangeRequest] so that an exchange the library
// does not serve -- an installation token -- reads its proof by the same
// rules rather than a copy of them.
type exchangeSubject interface {
	GetExchangeSubjectTokenClaims() map[string]any
	GetExchangeSubjectTokenType() oidc.TokenType
	GetExchangeSubjectTokenIDOrToken() string
	GetClientID() string
}

// signInProof accepts one of this issuer's own tokens as a person's proof,
// or says which rule refused it.
func (s *Storage) signInProof(ctx context.Context, request exchangeSubject) (Proof, error) {
	refuse := func(format string, args ...any) (Proof, error) {
		return Proof{}, oidc.ErrInvalidGrant().WithDescription("subject_token: "+format, args...)
	}

	if request.GetExchangeSubjectTokenType() != oidc.AccessTokenType {
		return refuse("a token this issuer signed is exchanged only as the access token of a sign-in, never as %s",
			request.GetExchangeSubjectTokenType())
	}

	issued, err := getJSON[token](ctx, s.state, tokenKey(request.GetExchangeSubjectTokenIDOrToken()))
	if err != nil {
		return Proof{}, oidc.ErrServerError().WithDescription("%s", err)
	}

	if issued == nil {
		return refuse("this issuer holds no record of that access token")
	}

	declared, ok := s.iss.Policy().Client(issued.ClientID)
	if !ok || !declared.SignInExchange {
		return refuse("a sign-in to %q cannot be exchanged", issued.ClientID)
	}

	// Presented by the client it was issued to: the CLI trading its own
	// sign-in, not some other caller holding a copy of it.
	if request.GetClientID() != issued.ClientID {
		return refuse("a sign-in to %q is exchanged only by %q", issued.ClientID, issued.ClientID)
	}

	if !strings.Contains(issued.Subject, "@") {
		return refuse("a sign-in exchange is a person's, and %q is not one", issued.Subject)
	}

	if issued.Session == "" {
		return refuse("no session stands behind that access token")
	}

	session, live, err := s.iss.Sessions().ByID(ctx, issued.Session)
	if err != nil {
		return Proof{}, oidc.ErrServerError().WithDescription("%s", err)
	}

	if !live {
		return refuse("the session behind that access token has ended")
	}

	// A sign-in exchange trades a person's CLI sign-in, and an agent chain
	// is a background host's. The load-time refusal of `session: agent`
	// with `sign_in_exchange` is not enough on its own: a recorded class
	// outlives a policy change, so the class of the session itself decides.
	if session.Agent() {
		return refuse("a sign-in held by an agent-class session cannot be exchanged")
	}

	return Proof{Email: issued.Subject}, nil
}

func (s *Storage) grantFor(request op.TokenExchangeRequest) (Grant, bool) {
	value, ok := s.grants.Load(request)
	if !ok {
		return Grant{}, false
	}
	grant, ok := value.(Grant)
	return grant, ok
}

// CreateTokenExchangeRequest implements [op.TokenExchangeStorage].
func (s *Storage) CreateTokenExchangeRequest(context.Context, op.TokenExchangeRequest) error {
	// Nothing to store: the grant is already decided and held for the
	// life of this request, and an audit record belongs in the log rather
	// than in the token store.
	return nil
}

// GetPrivateClaimsFromTokenExchangeRequest implements [op.TokenExchangeStorage].
func (s *Storage) GetPrivateClaimsFromTokenExchangeRequest(
	_ context.Context, request op.TokenExchangeRequest,
) (map[string]any, error) {
	grant, ok := s.grantFor(request)
	if !ok {
		return nil, errors.New("the exchange was not validated")
	}
	return grant.Claims, nil
}

// SetUserinfoFromTokenExchangeRequest implements [op.TokenExchangeStorage].
func (s *Storage) SetUserinfoFromTokenExchangeRequest(
	ctx context.Context, info *oidc.UserInfo, request op.TokenExchangeRequest,
) error {
	grant, ok := s.grantFor(request)
	if !ok {
		return errors.New("the exchange was not validated")
	}

	// An exchange is usually a job or a workload, which has no names. A
	// person exchanging one of their own tokens does, so ask the same way.
	given, family := s.namesOf(ctx, grant.Subject)

	return s.fill(ctx, info, grant.Subject, grant.Claims, given, family)
}
