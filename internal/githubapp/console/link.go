package console

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// BeginGitHubLinkAppConnect starts creating the link App. The URL carries the
// state placeholder the issuer signs.
func (c *Service) BeginGitHubLinkAppConnect(ctx context.Context, owner string) (consolewire.Begun, error) {
	if _, err := requireRole(ctx, access.RoleOperator); err != nil {
		return consolewire.Begun{}, err
	}
	begun, err := c.beginLinkAppConnect(ctx, strings.TrimSpace(owner))
	if err != nil {
		return consolewire.Begun{}, err
	}
	return begun.wire(), nil
}

// beginLinkAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Service) beginLinkAppConnect(ctx context.Context, owner string) (githubBegin, error) {
	switch {
	case !status.ValidOrg(owner):
		return githubBegin{}, connect.Errorf(connect.CodeInvalidArgument, "%q is not an organisation login", owner)
	case c.deps.LinkApp == nil || c.deps.Links == nil:
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment keeps no state in Kubernetes, so a link would not survive a restart")
	}
	if existing, connected, err := c.deps.LinkApp.LinkApp(ctx); err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	} else if connected {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"the link App %s is already connected: disconnect it first", existing.AppSlug)
	}
	root := c.githubRoot(ctx)
	manifest, err := json.Marshal(githubapp.NewLinkManifestNamed(
		owner, c.deps.LinkAppName, c.publicURL(ctx), root+consolewire.LinkAppCallbackPath, root+consolewire.LinkCallbackPath))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	state := consolewire.StatePlaceholder
	return githubBegin{url: githubapp.CreateURL(owner, state), manifest: string(manifest), state: state, bind: consolewire.BindLinkApp + owner}, nil
}

// DisconnectGitHubLinkApp forgets the link App, and makes every link made
// with it unverifiable — before forgetting, so that no pass can ever check
// one of those tokens with some later App's credentials and read GitHub's
// "unknown token" as a revocation.
func (c *Service) DisconnectGitHubLinkApp(
	ctx context.Context, _ *directoryrosterv1.DisconnectGitHubLinkAppRequest,
) (*directoryrosterv1.DisconnectGitHubLinkAppResponse, error) {
	if _, err := requireRole(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	gone, err := c.disconnectLinkApp(ctx)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.DisconnectGitHubLinkAppResponse{
		Invalidated:    int32(gone.invalidated), //nolint:gosec // a count of people
		AppSettingsUrl: gone.settingsURL,
	}, nil
}

// disconnectLinkApp is the work of it, which [Console.DisconnectGitHubApp]
// does too.
func (c *Service) disconnectLinkApp(ctx context.Context) (githubDisconnect, error) {
	if c.deps.LinkApp == nil || c.deps.Links == nil {
		return githubDisconnect{}, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no link App")
	}
	record, connected, err := c.deps.LinkApp.LinkApp(ctx)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	_, hasCredential, err := c.deps.LinkApp.LinkAppCredential(ctx)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !connected && !hasCredential {
		return githubDisconnect{}, connect.NewError(connect.CodeNotFound, "no link App is connected")
	}
	invalidated, err := c.deps.Links.Invalidate(ctx, "the link App was disconnected: link the account again", time.Now().UTC())
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if err = c.deps.LinkApp.DeleteLinkApp(ctx); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	// Nothing to uninstall: the link App is installed nowhere, by design.
	out := githubDisconnect{
		invalidated: invalidated,
		settingsURL: appSettingsURL(record.Owner, record.AppSlug),
		detail:      "The link App is installed nowhere, so there was nothing to uninstall.",
	}
	c.record(ctx, audit.LinkAppDisconnected(actorOf(ctx), audit.App{ID: record.AppID, Slug: record.AppSlug}, invalidated))
	return out, nil
}

// linkableAddresses sorts an account's verified addresses into the ones a
// link may prove — an account the directory has, live, and can vouch for
// — and a reason for each work address that is not. A personal address is
// neither: it is nobody's business here, and is not repeated back. The
// directory is the console's word in the call's facts ([consolecall.FactsFrom]),
// resolved before the call: the module reads none itself and asks nobody. An
// address the facts say nothing of is not a work address the directory knows.
func (c *Service) linkableAddresses(ctx context.Context, verified []string) (accepted, refused []string) {
	facts := consolecall.FactsFrom(ctx)
	for _, email := range link.Normalise(verified) {
		var u consolecall.UserFact
		if facts != nil {
			u = facts.Users[email]
		}
		switch {
		case u.Err != "":
			refused = append(refused, email+": the directory could not be asked just now; try again in a minute")
		case !u.InDomain:
		case !u.Authoritative:
			refused = append(refused, email+": the directory cannot vouch for it just now; try again in a minute")
		case !u.Found:
			refused = append(refused, email+": the directory has no such account")
		case u.Suspended:
			refused = append(refused, email+": the account is suspended")
		default:
			accepted = append(accepted, email)
		}
	}
	return accepted, refused
}

// CompleteGitHubLinkApp is where GitHub sends the owner after creating the
// link App. There is nothing to install: the App is used by being authorized,
// never by being installed.
func (c *Service) CompleteGitHubLinkApp(ctx context.Context, f consolewire.Flow) (consolewire.Done, error) {
	owner, err := flowOrg(f, consolewire.BindLinkApp)
	if err != nil {
		return consolewire.Done{}, err
	}
	actor, err := c.flowActor(ctx, f)
	if err != nil {
		return consolewire.Done{}, err
	}
	store := c.deps.LinkApp
	if store == nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "This deployment keeps no link App.", "")
	}
	registration, err := githubapp.Convert(ctx, c.githubHTTP(), f.Code)
	if err != nil {
		c.log().WarnContext(ctx, "the link App was created and its credentials could not be collected",
			logattr.SafeString("owner", owner), logattr.SafeError("error", err))
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			"GitHub created the link App, and then would not hand over its credentials.", err.Error(),
			"The page was reloaded: the code GitHub returns can be exchanged once.",
			"This service cannot reach api.github.com: the cluster's egress policy has to allow it.")
	}
	switch {
	case !strings.EqualFold(registration.Owner, owner):
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("The App was created under %s, not %s.", registration.Owner, owner), "")
	case registration.ClientID == "" || registration.ClientSecret == "":
		return consolewire.Done{}, flowProblem(http.StatusConflict, "GitHub created the App and returned no client credentials for it.", "")
	}
	record := link.App{
		Owner: owner, AppID: registration.ID, AppSlug: registration.Slug, ClientID: registration.ClientID,
		HTMLURL: registration.HTMLURL, ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	credential := link.AppCredential{AppID: registration.ID, ClientID: registration.ClientID, ClientSecret: registration.ClientSecret}
	if err = store.PutLinkApp(ctx, record, credential); err != nil {
		c.log().ErrorContext(ctx, "the link App was created and could not be kept",
			logattr.SafeString("owner", owner), logattr.SafeError("error", err))
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			"GitHub created the link App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error())
	}
	c.log().InfoContext(ctx, "link App created", logattr.SafeString("owner", owner), slog.Int64("app", registration.ID),
		logattr.SafeString("slug", registration.Slug), logattr.SafeString("by", actor))
	c.record(ctx, audit.LinkAppConnected(audit.Identified(actor), audit.App{ID: registration.ID, Slug: registration.Slug}, owner))
	return consolewire.Done{}, nil
}

// linkAppCredential is the link App's credential, or the problem saying linking
// is not set up.
func (c *Service) linkAppCredential(ctx context.Context) (link.AppCredential, error) {
	if c.deps.LinkApp == nil || c.deps.Links == nil {
		return link.AppCredential{}, flowProblem(http.StatusNotFound, "This installation does not link GitHub accounts.", "")
	}
	credential, found, err := c.deps.LinkApp.LinkAppCredential(ctx)
	switch {
	case err != nil:
		return link.AppCredential{}, flowProblem(http.StatusConflict, "Linking is not available just now. Try again in a minute.", err.Error())
	case !found:
		return link.AppCredential{}, flowProblem(http.StatusConflict,
			"Linking GitHub accounts is not set up here yet: an operator creates the link App on the console's GitHub page.", "")
	}
	return credential, nil
}

// GitHubLinkPage is where a person starts linking: the address GitHub's
// authorize page is at. The page has no session, so the call carries no
// caller; the issuer signs the state the URL carries and pins it to the
// person's browser.
func (c *Service) GitHubLinkPage(ctx context.Context) (consolewire.LinkPage, error) {
	credential, err := c.linkAppCredential(ctx)
	if err != nil {
		return consolewire.LinkPage{}, err
	}
	return consolewire.LinkPage{
		AuthorizeURL: githubapp.AuthorizeURL(credential.ClientID, c.githubRoot(ctx)+consolewire.LinkCallbackPath, consolewire.StatePlaceholder),
	}, nil
}

// GitHubLinkLogins indexes every address a link proves against the GitHub
// account that proves it, lowercased. Only a link that counts is in it: one
// GitHub still verifies an address for. Known is false where this module keeps
// no links.
func (c *Service) GitHubLinkLogins(ctx context.Context) (consolewire.LinkLogins, error) {
	if _, err := requireAnywhere(ctx, access.RoleViewer); err != nil {
		return consolewire.LinkLogins{}, err
	}
	if c.deps.Links == nil {
		return consolewire.LinkLogins{}, nil
	}
	links, err := c.deps.Links.List(ctx)
	if err != nil {
		return consolewire.LinkLogins{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	out := consolewire.LinkLogins{Known: true, Logins: make(map[string]string, len(links))}
	for i := range links {
		if !links[i].Active() {
			continue
		}
		for _, email := range links[i].Emails {
			out.Logins[strings.ToLower(email)] = links[i].Login
		}
	}
	return out, nil
}

// linkRate bounds how many links the module finishes in a minute, whoever asks:
// finishing one spends a handful of calls to GitHub and a write, and the page
// that leads here has no session.
const linkRate = 60

// GitHubLinkComplete finishes a link: GitHub's word on who the account is and
// which addresses it verifies, and the directory's word on which of those it
// knows. The issuer verified the signed state and the browser's cookie.
func (c *Service) GitHubLinkComplete(ctx context.Context, r consolewire.LinkComplete) (consolewire.Linked, error) {
	if !c.limit.allow(time.Now(), linkRate) {
		return consolewire.Linked{}, flowProblem(http.StatusTooManyRequests, "Too many accounts are being linked just now. Try again in a minute.", "")
	}
	if problem := r.GitHubError; problem != "" {
		return consolewire.Linked{}, flowProblem(http.StatusBadRequest, "GitHub did not authorize the link.", r.GitHubErrorDescription)
	}
	credential, err := c.linkAppCredential(ctx)
	if err != nil {
		return consolewire.Linked{}, err
	}
	web, now := c.githubHTTP(), time.Now().UTC()

	var (
		tokens   githubapp.UserTokens
		user     githubapp.User
		verified []string
	)
	if r.Continue != "" {
		st, err := openLinkState(credential.ClientSecret, r.Continue, now)
		if err != nil {
			return consolewire.Linked{}, flowProblem(http.StatusBadRequest, "This link took too long to finish. Start again.", "")
		}
		tokens, user, verified = st.Tokens, githubapp.User{ID: st.UserID, Login: st.Login}, st.Verified
	} else {
		tokens, err = githubapp.ExchangeCode(ctx, web, credential.ClientID, credential.ClientSecret,
			r.Code, c.githubRoot(ctx)+consolewire.LinkCallbackPath, now)
		if err != nil {
			return consolewire.Linked{}, flowProblem(http.StatusConflict, "GitHub would not complete the link. Start again.", err.Error())
		}
		user, err = githubapp.CurrentUser(ctx, web, tokens.AccessToken)
		if err != nil {
			return consolewire.Linked{}, flowProblem(http.StatusConflict, "GitHub would not say which account this is. Start again.", err.Error())
		}
		verified, err = githubapp.VerifiedEmails(ctx, web, tokens.AccessToken)
		if err != nil {
			return consolewire.Linked{}, flowProblem(http.StatusConflict, "GitHub would not list the account's addresses. Start again.", err.Error())
		}
		// The directory's word on these is the console's to give: ask for it
		// before anything is decided, with the state to resume from.
		if need := c.unresolved(ctx, verified); len(need) > 0 {
			sealed, err := sealLinkState(credential.ClientSecret, linkState{
				Expires: now.Add(linkContinueWindow), Tokens: tokens, UserID: user.ID, Login: user.Login, Verified: verified,
			})
			if err != nil {
				return consolewire.Linked{}, flowProblem(http.StatusConflict, "The link could not be finished here. Try again in a minute.", err.Error())
			}
			return consolewire.Linked{Login: user.Login, Resolve: need, Continue: sealed}, nil
		}
	}

	accepted, refused := c.linkableAddresses(ctx, verified)
	if len(accepted) == 0 {
		return consolewire.Linked{Login: user.Login, Refused: refused}, nil
	}

	written, err := c.deps.Links.Claim(ctx, link.Link{
		ID: user.ID, Login: user.Login, AppID: credential.AppID, Emails: accepted, State: link.StateLinked,
		LinkedAt: now, CheckedAt: now, ChangedAt: now,
		AccessToken: tokens.AccessToken, AccessExpires: tokens.AccessExpires,
		RefreshToken: tokens.RefreshToken, RefreshExpires: tokens.RefreshExpires,
	}, now)
	if err != nil {
		c.log().ErrorContext(ctx, "a link could not be kept", slog.Int64("account", user.ID), logattr.SafeError("error", err))
		return consolewire.Linked{}, flowProblem(http.StatusConflict, "The link could not be saved here. Try again in a minute.", err.Error())
	}
	for k := range written {
		l := &written[k]
		if l.ID != user.ID {
			c.record(ctx, audit.GitHubLinkMoved(firstOf(l.Emails), l.Login, l.Reason))
			continue
		}
		c.record(ctx, audit.GitHubLinkCreated(accepted[0], l.Login))
	}
	c.log().InfoContext(ctx, "a GitHub account was linked", slog.Int64("account", user.ID), logattr.SafeString("login", user.Login),
		logattr.SafeString("emails", strings.Join(accepted, ",")))
	return consolewire.Linked{Login: user.Login, Accepted: accepted, Refused: refused}, nil
}

// unresolved are the verified addresses the call's facts say nothing of.
func (c *Service) unresolved(ctx context.Context, verified []string) []string {
	facts := consolecall.FactsFrom(ctx)
	var need []string
	for _, email := range link.Normalise(verified) {
		if facts == nil {
			need = append(need, email)
			continue
		}
		if _, ok := facts.Users[email]; !ok {
			need = append(need, email)
		}
	}
	return need
}

func firstOf(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// limiter is a token bucket over a minute: at most n finished in any minute.
type limiter struct {
	mu     sync.Mutex
	window time.Time
	used   int
}

func (l *limiter) allow(now time.Time, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.window) >= time.Minute {
		l.window, l.used = now, 0
	}
	if l.used >= n {
		return false
	}
	l.used++
	return true
}
