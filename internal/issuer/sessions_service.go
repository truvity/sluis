package issuer

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"google.golang.org/protobuf/types/known/timestamppb"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/gen/accessissuer/v1/accessissuerv1connect"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/policy"
)

// SessionsService is the only thing this issuer answers about itself,
// and it can only ever remove: list what is open, end some of it. There is no call
// here that grants anything, which is what makes it safe to point a
// console at.
//
// The console is served by the hub, and the hub must not depend on this
// service — it answers "is this account live" for consumers, and that has
// to keep working when the issuer is down. So the BROWSER calls this at
// the issuer's own host and the hub's code learns nothing about it.
type SessionsService struct {
	sessions *Sessions
	// record writes a revoke down: ending somebody's access is the event
	// an audit exists to find.
	record func(context.Context, *record.Record)
	// sso is the browser-session store, for ending the sign-in when a
	// revoke means "everywhere". A call is authorized by cookie through
	// signedIn, below.
	sso *SSO
	// secure is the flag the browser cookies were set with; it decides their name.
	secure bool
	// signedIn is who a browser's sign-in cookie proves the caller is, and
	// in which groups, when the sign-in still stands by the issuer's own
	// decision ([Issuer.checkSignIn]): live, inside the absolute limit,
	// and admitted by the directory. A cookie says only who, so the groups
	// are what the policy makes of the directory's answer. Nil where there
	// are no browser sessions to read.
	signedIn func(ctx context.Context, cookie string) (caller, bool)
	// verify turns the caller's own bearer into who they are. It is this
	// issuer's token, verified against this issuer's key: the one caller
	// whose identity it can establish without asking anyone.
	verify func(ctx context.Context, bearer string) (identity string, groups []string, err error)
	// announce tells clients their sign-in ended (Back-Channel Logout).
	// It is the announcer the sign-out path uses; nil tells nobody.
	announce func(context.Context, []Session)
	// policy is what names a declared client or resource; nil names none.
	policy func() *policy.Set
	// documents resolves a client that identifies itself by URL, through
	// the cache the authorize path already fills; nil resolves none.
	documents *documentClients
}

var _ accessissuerv1connect.SessionServiceHandler = (*SessionsService)(nil)

// NewSessionsService returns the handler over an issuer.
func NewSessionsService(iss *Issuer, verifier *op.AccessTokenVerifier, secure bool) *SessionsService {
	return &SessionsService{
		secure:   secure,
		sessions: iss.Sessions(),
		record:   iss.record,
		sso:      iss.SSO(),
		policy:   iss.Policy,
		signedIn: func(ctx context.Context, cookie string) (caller, bool) {
			if iss.SSO() == nil {
				return caller{}, false
			}
			return cookieCaller(iss, iss.checkSignIn(ctx, iss.SSO(), cookie, "", 0))
		},
		verify: func(ctx context.Context, bearer string) (string, []string, error) {
			claims, err := op.VerifyAccessToken[*oidc.AccessTokenClaims](ctx, bearer, verifier)
			if err != nil {
				return "", nil, err
			}

			return claims.Subject, groupsOf(claims), nil
		},
	}
}

// groupsOf reads the `groups` claim, which is the whole of what this
// service authorizes on — the same string a cluster binds and a client
// gates with, re-mapped nowhere.
func groupsOf(claims *oidc.AccessTokenClaims) []string {
	raw, ok := claims.Claims["groups"]
	if !ok {
		return nil
	}

	values, ok := raw.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(values))

	for _, value := range values {
		if name, ok := value.(string); ok {
			out = append(out, name)
		}
	}

	return out
}

// caller is who is asking, and whether they may ask about anyone.
type caller struct {
	identity string
	operator bool
}

// may reports whether this caller may act on that identity's sessions.
// Your own, always; anyone's, only as an operator. That asymmetry is the
// whole authorization model here, and it is why "sign out everywhere"
// needs no special case: it is this rule applied to yourself.
func (c caller) may(identity string) bool {
	return c.operator || sameIdentity(c.identity, identity)
}

// sameIdentity reports whether two identities are one, compared exactly
// as the session index keys them ([sessionOfKey]: lower-cased): not by
// Unicode case folding, under which "ſ" and "s" are one letter while the
// index holds them apart, so that "your own" means the sessions filed
// under your own key and nobody else's.
func sameIdentity(a, b string) bool { return identityKey(a) == identityKey(b) }

// identityKey is an identity as the index keys it. Not strings.EqualFold,
// which staticcheck would suggest for the comparison spelled out: that is
// exactly the folding this exists to avoid.
func identityKey(identity string) string { return strings.ToLower(strings.TrimSpace(identity)) }

// who establishes the caller, from either of the two ways a browser or a
// console can prove itself here.
//
// The cookie is first because it is the primary path: the account page is
// served by this issuer at this host, so the browser already holds this
// issuer's session and needs no bearer, no CORS and no token in
// JavaScript. The bearer is the cross-origin path, for a console that
// weaves the operator's view into its own pages.
func (s *SessionsService) who(ctx context.Context, header http.Header) (caller, error) {
	// A cookie whose sign-in does not stand -- past the absolute limit, a
	// person the directory no longer admits -- proves nobody here, not
	// even who they are: this service revokes sessions, and a sign-in the
	// issuer would end at its next /authorize must not be used to do so
	// in the meantime. It is refused, not ended (that is the issuer's
	// pages' and the console's to do), and a bearer may still prove the
	// caller.
	if cookie := cookieIn(header, SSOCookieName, s.secure); cookie != "" && s.signedIn != nil {
		if held, ok := s.signedIn(ctx, cookie); ok {
			return held, nil
		}
	}

	bearer := strings.TrimSpace(header.Get("Authorization"))
	if len(bearer) < 7 || !strings.EqualFold(bearer[:7], "bearer ") {
		return caller{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("this service needs a token from this issuer, or its session"))
	}

	identity, groups, err := s.verify(ctx, strings.TrimSpace(bearer[7:]))
	if err != nil {
		// Why it failed goes nowhere near the caller: telling an
		// unauthenticated client what was wrong with its token helps it
		// produce a better one.
		return caller{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("that token was not accepted"))
	}

	return withGroups(identity, groups), nil
}

// cookieCaller is who a standing sign-in is, and in which groups: the
// same evaluation a token gets, so a cookie and a bearer cannot come to
// mean different things. A recovery sign-in -- by what the sign-in
// recorded, not the shape of its subject -- has no directory answer, and
// the policy's matchers decide it exactly as for a workload.
func cookieCaller(iss *Issuer, standing signInStanding) (caller, bool) {
	if standing.Verdict != signInStands {
		return caller{}, false
	}
	identity := standing.Session.Identity
	if standing.Session.recovered() {
		account, ok := serviceAccountSubject(identity)
		if !ok {
			return caller{identity: identity}, true
		}
		return withGroups(identity, iss.Policy().Evaluate(policy.Input{ServiceAccount: &account}).Groups), true
	}

	return withGroups(identity, iss.Policy().Evaluate(standing.Resolution.Input(identity)).Groups), true
}

// withGroups is the one place a group list becomes a decision.
func withGroups(identity string, groups []string) caller {
	held := caller{identity: identity}

	for _, group := range groups {
		if policy.IsOperators(group) {
			held.operator = true

			break
		}
	}

	return held
}

// cookieIn reads one cookie out of a header, which is all a Connect
// request exposes.
func cookieIn(header http.Header, base string, secure bool) string {
	cookie, err := access.ReadCookie(&http.Request{Header: header}, base, secure)
	if err != nil {
		return ""
	}

	return cookie.Value
}

// defaultSessionsPageSize and maxSessionsPageSize bound the global listing.
// Narrowed to one identity or one client a listing is already
// small — an account or a client does not hold thousands of open
// sessions — so only the unnarrowed, operator-only listing needs a cap at
// all; applying the same cap there too keeps one rule rather than two.
const (
	defaultSessionsPageSize = 50
	maxSessionsPageSize     = 500
)

// ListSessions implements the contract.
func (s *SessionsService) ListSessions(
	ctx context.Context, req *connect.Request[accessissuerv1.ListSessionsRequest],
) (*connect.Response[accessissuerv1.ListSessionsResponse], error) {
	who, err := s.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}

	identity := strings.TrimSpace(req.Msg.GetIdentity())
	clientID := strings.TrimSpace(req.Msg.GetClientId())
	contains := req.Msg.GetContains()

	// A substring is operator-only, and this refusal is the whole of why.
	// Every rule below decides what a caller may see from the identity
	// they NAMED -- `may` admits the caller's own and nobody else's. A
	// substring names an unknown set: "globex.example" is everybody, and the
	// checks underneath would pass it because the string is not anyone's
	// identity to refuse.
	//
	// Refused rather than narrowed to their own, because a filter that
	// quietly means something different depending on who is asking is
	// worse than one that says no.
	if contains && !who.operator {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("matching sessions by substring is an operator's"))
	}

	// Naming neither is the global listing: every session in the
	// installation, newest first. It exists for the incident where you do
	// not know WHOSE session to look for, and it is operator-only: an
	// operator who can already list any identity by name and read the
	// whole policy gains no disclosure from it that they could not
	// already piece together, one identity at a time.
	if identity == "" && clientID == "" && !who.operator {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("listing every session is an operator's"))
	}

	// Listing by client alone would name everybody on it, so it is an
	// operator's question. Listing your own is anyone's.
	if identity == "" && clientID != "" && !who.operator {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("listing a client's sessions is an operator's"))
	}

	if identity != "" && !who.may(identity) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("that is somebody else's session"))
	}

	names := s.namer(ctx)
	query := Query{Identity: identity, ClientID: clientID, Contains: contains}

	// A client substring matches what a person reads -- the client's name
	// and the resource -- as well as its id, so the index (which knows only
	// ids) is asked for the identity's sessions and the match is made here.
	byName := contains && clientID != ""
	if byName {
		query.ClientID = ""
	}

	found, err := s.sessions.List(ctx, query)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if byName {
		found = slices.DeleteFunc(found, func(held Session) bool { return !names.matches(held, clientID) })
	}

	page, nextToken := paginate(found, int(req.Msg.GetPageSize()), strings.TrimSpace(req.Msg.GetPageToken()))

	out := &accessissuerv1.ListSessionsResponse{
		Sessions:      make([]*accessissuerv1.Session, 0, len(page)),
		NextPageToken: nextToken,
	}

	for i := range page {
		row := described(page[i], s.sessions.DeadlineOf(page[i]))
		row.ClientName = names.client(page[i].ClientID)
		row.ResourceName = names.resource(page[i].Resource)
		out.Sessions = append(out.Sessions, row)
	}

	// The sign-ins behind them, on the FIRST page only: they are not
	// paged, and repeating them under every page would say a person has
	// more browsers the further you scroll.
	//
	// This is the half the console was missing. Every per-client session
	// was listed and no sign-in was, so revoking every row emptied the
	// page and left the thing that admits a browser untouched — and the
	// console's own sign-in appears as no session at all, because it
	// never redeems the code it gets back.
	if s.sso != nil && strings.TrimSpace(req.Msg.GetPageToken()) == "" {
		// The sign-ins are filtered the same way the sessions were, or
		// the page contradicts itself: four rows for one person and a
		// browser summary listing everybody who is signed in.
		list := s.sso.List
		if contains {
			list = s.sso.Like
		}

		signIns, err := list(ctx, identity)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		for i := range signIns {
			out.SignIns = append(out.SignIns, &accessissuerv1.SignIn{
				Id:        signIns[i].ID,
				Identity:  signIns[i].Identity,
				How:       signIns[i].How,
				AuthTime:  timestamppb.New(signIns[i].AuthTime),
				ExpiresAt: timestamppb.New(signIns[i].ExpiresAt),
			})
		}
	}

	return connect.NewResponse(out), nil
}

// paginate slices an already-sorted (newest first) list into one page.
// The token is the id of the last session already seen, not an offset: an
// offset is invalidated by every session that opens or closes between two
// calls, and this index is exactly the thing that changes constantly. An
// id that has since expired or been revoked is simply not found, and the
// listing falls back to the start rather than erroring on a page an
// operator is still allowed to ask for.
func paginate(sessions []Session, size int, token string) ([]Session, string) {
	switch {
	case size <= 0:
		size = defaultSessionsPageSize
	case size > maxSessionsPageSize:
		size = maxSessionsPageSize
	}

	start := 0

	if token != "" {
		for i := range sessions {
			if sessions[i].ID == token {
				start = i + 1

				break
			}
		}
	}

	if start >= len(sessions) {
		return nil, ""
	}

	end := start + size
	if end >= len(sessions) {
		return sessions[start:], ""
	}

	return sessions[start:end], sessions[end-1].ID
}

// RevokeSessions implements the contract.
func (s *SessionsService) RevokeSessions(
	ctx context.Context, req *connect.Request[accessissuerv1.RevokeSessionsRequest],
) (*connect.Response[accessissuerv1.RevokeSessionsResponse], error) {
	who, err := s.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}

	identity := strings.TrimSpace(req.Msg.GetIdentity())

	// One client for everybody: the incident lever for a client whose
	// tokens are in doubt (docs/decisions/0040-agent-class-sessions.md,
	// decision 7). Asked for by name, never inferred from a missing
	// identity, and an operator's.
	if req.Msg.GetEveryIdentity() {
		return s.revokeClientEverywhere(ctx, who, req.Msg)
	}

	if identity == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("name the identity whose sessions to end"))
	}

	if !who.may(identity) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("that is somebody else's session"))
	}

	// One session by id, when the console offers a row its own button.
	// Still checked against the identity above, so an id alone is never
	// enough to end a session belonging to somebody else.
	if id := strings.TrimSpace(req.Msg.GetSessionId()); id != "" {
		one, found, err := s.sessions.ByID(ctx, id)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		if !found || !sameIdentity(one.Identity, identity) {
			// A session that is not there and one that is somebody
			// else's are the same answer, so that an id cannot be probed.
			return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{}), nil
		}

		gone, err := s.sessions.RevokeID(ctx, id)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		s.revoked(ctx, who.identity, identity, "", "one session", int(boolToCount(gone)))
		return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{Ended: boolToCount(gone)}), nil
	}

	// One BROWSER: everything it opened, and the sign-in that let it.
	//
	// Ending the sessions alone is what the console was doing one row at
	// a time, and it is the half sign-out that looks exactly like a whole
	// one: every session gone, the sign-in intact, and the next
	// /authorize completed silently with no password. Reported from the
	// console — "I revoked all sessions, but still has access
	// everywhere" — and it was right.
	if sso := strings.TrimSpace(req.Msg.GetSso()); sso != "" {
		// Whose browser it is, checked before anything is ended. The
		// permission check above is against the IDENTITY the caller
		// named, so without this an id alone would end somebody else's
		// sign-in — the same hole the by-id path above guards, and the
		// reason that one reads the record before acting on it.
		//
		// A sign-in that is ABSENT still has its sessions revoked: one
		// that already ended -- expired, or replaced by another sign-in
		// in the same browser -- leaves sessions filed under its id, and
		// this is the only way to end them as a browser's. The query is
		// narrowed to the identity the caller was allowed above, so it
		// reaches nobody else's sessions, and it answers what a listing
		// of that identity would already show: no probe.
		if s.sso != nil {
			record, found, err := s.sso.Get(ctx, sso)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}

			if found && !sameIdentity(record.Identity, identity) {
				return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{}), nil
			}
		}

		ended, err := s.sessions.Revoke(ctx, Query{Identity: identity, SSO: sso})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// The sign-in goes last: if ending the sessions fails halfway,
		// the sign-in is still there and the person can try again. The
		// other order would leave sessions running with nothing listing
		// the browser they belong to.
		if s.sso != nil {
			if err = s.sso.End(ctx, sso); err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
		}

		s.revoked(ctx, who.identity, identity, "", "one browser", ended)
		return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{Ended: int32(ended)}), nil
	}

	clientID := strings.TrimSpace(req.Msg.GetClientId())

	// A person's own sign-out everywhere, narrowed to one class: "Sign out
	// all browsers and apps" or "Disconnect all agents". Everything else --
	// "Sign out everything", an operator's revoke of somebody else, any
	// narrower revoke -- falls through and ends every class.
	if class, scoped := ownScope(who, identity, clientID, req.Msg.GetScope()); scoped {
		return s.revokeClass(ctx, who, identity, class)
	}

	// Naming no client means everywhere, and everywhere includes the
	// sign-in itself. Ending only the running sessions would leave the
	// browser able to open new ones with no password, which is the
	// half-sign-out that looks exactly like a whole one. Naming a client
	// is narrower on purpose and leaves the sign-in alone.
	//
	// The sign-ins FIRST, then the sessions, as [endSignIn] and
	// [SessionsService.revokeClass] do: a code redeemed under one of them
	// after this either filed its session before the listing below, or
	// finds its sign-in ended and ends its own. Revoking first left a
	// window in which a code opened a session under a sign-in still live,
	// which "Sign out everything" -- the lever for a suspected compromise
	// -- then never reached.
	var (
		involved []Session
		endErr   error
	)
	if clientID == "" && s.sso != nil {
		_, involved, endErr = s.sso.EndForInvolving(ctx, identity)
	}

	// Read before revoking, as at an ordinary sign-out: once the sessions
	// are gone nothing says which clients held them.
	var held []Session
	if clientID == "" && s.announce != nil {
		held, _ = s.sessions.List(ctx, Query{Identity: identity})
	}

	ended, err := s.sessions.Revoke(ctx, Query{Identity: identity, ClientID: clientID})

	// Told as an ordinary sign-out tells them: the sessions the revoke
	// ended as they were held, and, by subject, the clients signed in
	// under these browsers without a refresh token (`openid` alone),
	// which only the sign-in knows. One token per client and sign-in.
	// Best effort: the sign-ins have ended either way, and whatever was
	// collected before a failure is still told, because a retry cannot
	// find what was already dropped. Every live chain was just revoked, so
	// none is spared.
	if clientID == "" && s.sso != nil && s.announce != nil {
		s.announce(ctx, withInvolved(held, involved))
	}

	// Recorded before any error is answered, with what did end: a sign-out
	// that stopped halfway has still ended those sessions and sign-ins.
	scope := audit.ScopeEverywhere
	if clientID != "" {
		scope = "one client"
	}
	s.revoked(ctx, who.identity, identity, clientID, scope, ended)

	switch {
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	case endErr != nil:
		return nil, connect.NewError(connect.CodeInternal, endErr)
	}

	return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{Ended: int32(ended)}), nil
}

// ownScope reports which class a revoke ends when it is a person's own
// sign-out everywhere narrowed by scope: their own identity, no client
// named, and a scope of INTERACTIVE or AGENTS. Anything else -- no scope,
// EVERYTHING, a value this issuer does not know, somebody else's identity
// (an operator's revoke), a client named -- is not scoped, and ends every
// class: a scope only ever ends less by being understood, and only for
// the person asking.
func ownScope(who caller, identity, clientID string, scope accessissuerv1.RevokeScope) (SessionClass, bool) {
	if clientID != "" || !sameIdentity(who.identity, identity) {
		return "", false
	}

	switch scope {
	case accessissuerv1.RevokeScope_REVOKE_SCOPE_INTERACTIVE:
		return ClassInteractive, true
	case accessissuerv1.RevokeScope_REVOKE_SCOPE_AGENTS:
		return ClassAgent, true
	default:
		return "", false
	}
}

// revokeClass is a person's own sign-out everywhere for one class
// (docs/decisions/0040-agent-class-sessions.md, decision 7, amended):
//
//   - interactive, "Sign out all browsers and apps": every browser sign-in
//     ends, then every interactive session; agent sessions keep working;
//   - agent, "Disconnect all agents": every agent session ends; the
//     browsers stay signed in.
//
// Back-Channel Logout goes to exactly the sessions that ended, and the
// subject-only logout token to the `openid`-only clients of the ended
// sign-ins that hold no session that keeps running. The record states the
// scope, the class ended and the class kept.
func (s *SessionsService) revokeClass(
	ctx context.Context, who caller, identity string, class SessionClass,
) (*connect.Response[accessissuerv1.RevokeSessionsResponse], error) {
	scope, kept := audit.ScopeEveryBrowserAndApp, ClassAgent
	if class == ClassAgent {
		scope, kept = audit.ScopeEveryAgent, ClassInteractive
	}

	// The sign-ins first, as at an ordinary sign-out ([endSignIn]): a code
	// redeemed under one of them after this either files its session
	// before the listing below, or finds its sign-in ended and ends its
	// own. Read with the clients that used them, which ending deletes.
	var (
		involved []Session
		endErr   error
	)
	if class == ClassInteractive && s.sso != nil {
		_, involved, endErr = s.sso.EndForInvolving(ctx, identity)
	}

	held, err := s.sessions.List(ctx, Query{Identity: identity})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var ending, running []Session
	for i := range held {
		if (class == ClassAgent) == held[i].Agent() {
			ending = append(ending, held[i])
		} else {
			running = append(running, held[i])
		}
	}

	ended := 0
	for i := range ending {
		gone, revokeErr := s.sessions.RevokeID(ctx, ending[i].ID)
		if revokeErr != nil {
			err = revokeErr

			break
		}

		if gone {
			ended++
		}
	}

	if s.announce != nil {
		s.announce(ctx, withInvolved(ending, notRunning(involved, running)))
	}

	s.record(ctx, audit.SessionsRevokedByClass(audit.Identified(who.identity), identity, scope, ended,
		string(class), string(kept)))

	switch {
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	case endErr != nil:
		return nil, connect.NewError(connect.CodeInternal, endErr)
	}

	return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{Ended: int32(ended)}), nil
}

// notRunning drops from the `openid`-only clients of the ended sign-ins
// those that hold a session under the same sign-in that keeps running: the
// subject-only logout token would end it at the relying party.
func notRunning(involved, running []Session) []Session {
	type key struct{ client, sso string }

	keep := make(map[key]bool, len(running))
	for i := range running {
		keep[key{running[i].ClientID, running[i].SSO}] = true
	}

	var out []Session
	for i := range involved {
		if !keep[key{involved[i].ClientID, involved[i].SSO}] {
			out = append(out, involved[i])
		}
	}

	return out
}

// ScopeClientEverywhere is the scope of the revoke that ends one client's
// sessions for every identity ([audit.ScopeClientEveryIdentity]).
const ScopeClientEverywhere = audit.ScopeClientEveryIdentity

// revokeClientEverywhere ends every session of one client, whoever holds
// it, for an operator: [Query] by client alone, which the
// `issuer:sessions-for:` index already answers. Nothing about a session's
// class is asked: it ends agent and interactive sessions alike. Nobody is
// told by Back-Channel Logout, as for the per-client revoke of one person.
func (s *SessionsService) revokeClientEverywhere(
	ctx context.Context, who caller, msg *accessissuerv1.RevokeSessionsRequest,
) (*connect.Response[accessissuerv1.RevokeSessionsResponse], error) {
	if !who.operator {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("ending a client for everybody is an operator's"))
	}

	clientID := strings.TrimSpace(msg.GetClientId())
	if clientID == "" || strings.TrimSpace(msg.GetIdentity()) != "" ||
		strings.TrimSpace(msg.GetSso()) != "" || strings.TrimSpace(msg.GetSessionId()) != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("every_identity takes a client_id and nothing else"))
	}

	ended, err := s.sessions.Revoke(ctx, Query{ClientID: clientID})
	if err != nil {
		// Recorded with what did end: a revocation that stops halfway
		// has still ended those.
		s.revoked(ctx, who.identity, "", clientID, ScopeClientEverywhere, ended)

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	s.revoked(ctx, who.identity, "", clientID, ScopeClientEverywhere, ended)

	return connect.NewResponse(&accessissuerv1.RevokeSessionsResponse{Ended: int32(ended)}), nil
}

// withInvolved adds to the sessions held those involved clients that held
// none in the same sign-in, so a client gets one logout token per sign-in.
func withInvolved(held, involved []Session) []Session {
	type key struct{ client, sso string }

	seen := make(map[key]bool, len(held))
	for i := range held {
		seen[key{held[i].ClientID, held[i].SSO}] = true
	}

	out := held
	for i := range involved {
		if k := (key{involved[i].ClientID, involved[i].SSO}); !seen[k] {
			seen[k] = true
			out = append(out, involved[i])
		}
	}

	return out
}

// revoked records one revoke: who did it, whose sessions, where, and how
// many ended.
func (s *SessionsService) revoked(ctx context.Context, actor, identity, clientID, scope string, ended int) {
	if s.record == nil {
		return
	}
	s.record(ctx, audit.SessionRevoked(audit.Identified(actor), identity, clientID, scope, ended))
}

func boolToCount(gone bool) int32 {
	if gone {
		return 1
	}

	return 0
}

// described puts one session on the wire, with its class and the deadline
// it is held to now ([Sessions.DeadlineOf]): zero for none.
func described(s Session, deadline time.Time) *accessissuerv1.Session {
	out := &accessissuerv1.Session{
		Id:           s.ID,
		Identity:     s.Identity,
		ClientId:     s.ClientID,
		How:          howOf(s.How),
		IssuedAt:     timestamppb.New(s.IssuedAt),
		ExpiresAt:    timestamppb.New(s.ExpiresAt),
		Sso:          s.SSO,
		SessionClass: accessissuerv1.SessionClass_SESSION_CLASS_INTERACTIVE,
		Resource:     s.Resource,
		Scopes:       slices.Clone(s.Scopes),
	}

	if s.Agent() {
		out.SessionClass = accessissuerv1.SessionClass_SESSION_CLASS_AGENT
	}

	if !deadline.IsZero() {
		out.Deadline = timestamppb.New(deadline)
	}

	if !s.LastRefreshed.IsZero() {
		out.LastRefreshed = timestamppb.New(s.LastRefreshed)
	}

	return out
}

func howOf(how How) accessissuerv1.How {
	switch how {
	case HowCode:
		return accessissuerv1.How_HOW_CODE
	case HowDevice:
		return accessissuerv1.How_HOW_DEVICE
	case HowExchange:
		return accessissuerv1.How_HOW_EXCHANGE
	default:
		return accessissuerv1.How_HOW_UNSPECIFIED
	}
}

// sessionNamer turns the ids a listing carries into names a person can
// read. It remembers each answer for the one listing, so a page of a dozen
// sessions of one client asks once.
type sessionNamer struct {
	ctx       context.Context
	policy    *policy.Set
	documents *documentClients
	clients   map[string]string
}

func (s *SessionsService) namer(ctx context.Context) *sessionNamer {
	n := &sessionNamer{ctx: ctx, documents: s.documents, clients: map[string]string{}}
	if s.policy != nil {
		n.policy = s.policy()
	}

	return n
}

// client is the client's display name: the policy's for a declared
// client, the metadata document's `client_name` for one that is a URL
// (through the issuer's own cache and allow-list), otherwise the host of
// that URL, otherwise the id.
func (n *sessionNamer) client(id string) string {
	if id == "" {
		return ""
	}

	if name, ok := n.clients[id]; ok {
		return name
	}

	name := id

	switch declared, ok := n.lookupClient(id); {
	case ok:
		name = declared.Title(id)
	case n.documents != nil:
		if resolved, err := n.documents.Resolve(n.ctx, id); err == nil {
			name = resolved.Title(id)
		} else if host := hostOf(id); host != "" {
			name = host
		}
	default:
		if host := hostOf(id); host != "" {
			name = host
		}
	}

	n.clients[id] = name

	return name
}

func (n *sessionNamer) lookupClient(id string) (policy.Client, bool) {
	if n.policy == nil {
		return policy.Client{}, false
	}

	return n.policy.Client(id)
}

// resource is the policy's display name for a resource, or its host.
func (n *sessionNamer) resource(id string) string {
	if id == "" {
		return ""
	}

	if n.policy != nil {
		if declared, ok := n.policy.Resource(id); ok {
			return declared.Title(id)
		}
	}

	if host := hostOf(id); host != "" {
		return host
	}

	return id
}

// hostOf is the host of an https URL, empty for anything else.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	return u.Host
}

// matches reports whether a session answers a client filter: the needle
// is a case-insensitive substring of its client id, the client's name, its
// resource, or the resource's name.
func (n *sessionNamer) matches(held Session, needle string) bool {
	needle = strings.ToLower(needle)

	for _, text := range []string{held.ClientID, n.client(held.ClientID), held.Resource, n.resource(held.Resource)} {
		if strings.Contains(strings.ToLower(text), needle) {
			return true
		}
	}

	return false
}
