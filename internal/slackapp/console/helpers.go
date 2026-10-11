package console

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/truvity/sluis/internal/slackapp"
)

// setup is the calls made before a workspace has a bot token.
func (c *Service) slackSetup() *slackapp.Setup {
	return slackapp.NewSetup(c.d().SlackAPI...)
}

// slackRevoke takes back a bot token this service will not keep: one minted for
// the wrong workspace, or one it is disconnecting. A token already dead is
// success (see [slackapp.Client.Revoke]).
func (c *Service) slackRevoke(ctx context.Context, token string) error {
	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	return slackapp.New(token, c.d().SlackAPI...).Revoke(callCtx)
}

// scopeList splits Slack's comma-separated grant.
func scopeList(granted string) []string {
	return strings.FieldsFunc(granted, func(r rune) bool { return r == ',' || r == ' ' })
}

// AppSettingsURL is where an App is managed and deleted.
func AppSettingsURL(appID string) string {
	if appID == "" {
		return ""
	}
	return "https://api.slack.com/apps/" + url.PathEscape(appID)
}

// slackAuthorizeURL is the authorize URL Slack gave at creation, with what only
// this flow knows: where to come back to, the scopes and the workspace to
// preselect. The signed state is the console's to add.
func slackAuthorizeURL(authorize, redirect, team string, scopes []string) (string, error) {
	u, err := url.Parse(authorize)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("the App's authorize URL %q is not usable", authorize)
	}
	q := u.Query()
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(scopes, ","))
	if team != "" {
		q.Set("team", team)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// revokedWords is what became of a refused token, for the audit record.
func revokedWords(revokeErr error) string {
	if revokeErr != nil {
		return "the token was dropped but Slack would not revoke it"
	}
	return "the token was revoked and dropped"
}

// refusedToken is the sentence that ends a refusal of a token minted for the
// wrong workspace: what became of the token.
func refusedToken(revokeErr error) string {
	if revokeErr != nil {
		return "Nothing was kept, but Slack would not revoke the token: remove the App from that workspace in its Slack settings."
	}
	return "Nothing was kept, and the token was revoked."
}

// A configuration token is one word ("xoxe.xoxp-1-..."): anything with
// whitespace in it is a pasted sentence, refused before it travels.
func plausibleConfigurationToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, " \t\r\n")
}

// The states a workspace's connection is shown in, and a catalogue App's.
const (
	// CatalogueCallbackPath is where Slack sends the browser back to after an
	// owner installs a catalogue App: a route of the console.
	CatalogueCallbackPath = "/connect/slack/catalogue/callback"
	// CatalogueBind prefixes the App's id in a catalogue flow's signed state,
	// so no other flow's state can finish it, and the other way round.
	CatalogueBind = "slack-catalogue:"
)

// slackAppScopesMissing is the state of an installation that was granted fewer
// scopes than are declared.
const slackAppScopesMissing = "scopes_missing"
