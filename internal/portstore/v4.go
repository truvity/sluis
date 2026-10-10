package portstore

import (
	"context"
	"errors"
	"strconv"

	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// WithV4 puts the Apps' exported credentials on layout v4 (ADR 0041): an
// installed App whose key is exported is the whole document at
// external/github/<app>, with the ids from the App's record, and its key is no
// longer an internal credential. A nil v4 changes nothing.
func (b *Base) WithV4(v4 *secretstore.Stores) *Base {
	b.v4 = v4
	return b
}

// WithV5 puts the GitHub Apps on layout v5 (ADR 0072): the credential of an
// App of any purpose is `internal/github/apps/<id>/<ref>` under a fresh ref per
// write, and an exported App's document is `external/github/<id>`. The State
// keys do not change; the adapter locates them (port.Locate5). The other
// stores keep using the Secrets port. v4 and v5 are exclusive: a Base with
// both reads and writes layout v5.
func (b *Base) WithV5(v5 *secretstore.StoresV5) *Base {
	b.v5 = v5
	return b
}

// v5Cred is where the credential of an item is kept on layout v5.
type v5Cred struct {
	// at is the credential's value for a ref.
	at func(ref string) state.Value[[]byte]
	// drop removes the credential for a ref.
	drop func(ctx context.Context, ref string) error
	// fixed is the one ref the credential has, for a kind that is replaced in
	// place under the store's version history (a Google Workspace's key). Empty
	// is a fresh random ref per write.
	fixed string
}

// v5CredOfKey is where the credential of the item with the key lives when the
// secrets are on layout v5 (ADR 0072): github/apps/<id>/<ref>,
// github/links/<account>/<ref>, slack/workspaces/<team>/<ref>,
// slack/apps/<id>/<ref> and google/workspaces/<id>/key. ok is false on layout
// v4 and for a key that keeps no credential of its own (an organisation's key is
// its App's).
func (b *Base) v5CredOfKey(key string) (c v5Cred, ok bool) {
	if b.v5 == nil {
		return v5Cred{}, false
	}
	addr, err := port.Locate5(key)
	if err != nil {
		return v5Cred{}, false
	}
	switch {
	case addr.Module == port.ModuleGitHub && addr.Kind == "app":
		return v5Cred{
			at: func(ref string) state.Value[[]byte] { return b.v5.GitHub().AppCredential(addr.ID, ref) },
			drop: func(ctx context.Context, ref string) error {
				return b.v5.GitHub().DeleteAppCredential(ctx, addr.ID, ref)
			},
		}, true
	case addr.Module == port.ModuleGitHub && addr.Kind == "link":
		return v5Cred{
			at: func(ref string) state.Value[[]byte] { return b.v5.GitHub().LinkCredential(addr.ID, ref) },
			drop: func(ctx context.Context, ref string) error {
				return b.v5.GitHub().DeleteLinkCredential(ctx, addr.ID, ref)
			},
		}, true
	case addr.Module == port.ModuleSlack && addr.Kind == "workspace":
		return v5Cred{
			at: func(ref string) state.Value[[]byte] { return b.v5.Slack().WorkspaceCredential(addr.ID, ref) },
			drop: func(ctx context.Context, ref string) error {
				return b.v5.Slack().DeleteWorkspaceCredential(ctx, addr.ID, ref)
			},
		}, true
	case addr.Module == port.ModuleSlack && addr.Kind == "app":
		return v5Cred{
			at: func(ref string) state.Value[[]byte] { return b.v5.Slack().AppCredential(addr.ID, ref) },
			drop: func(ctx context.Context, ref string) error {
				return b.v5.Slack().DeleteAppCredential(ctx, addr.ID, ref)
			},
		}, true
	case addr.Module == port.ModuleGoogle && addr.Kind == "workspace":
		return v5Cred{
			at:    func(string) state.Value[[]byte] { return b.v5.Google().WorkspaceKey(addr.ID) },
			drop:  func(ctx context.Context, _ string) error { return b.v5.Google().DeleteWorkspaceKey(ctx, addr.ID) },
			fixed: googleKeyRef,
		}, true
	}
	return v5Cred{}, false
}

// googleKeyRef is the ref an item names a Google Workspace's key by: the key
// has one address, google/workspaces/<id>/key.
const googleKeyRef = "key"

// appOfKey is the id of the GitHub App whose item key it is, when the
// credentials of Apps are on layout v5. An organisation's key is not an App's.
func (b *Base) appOfKey(key string) (id string, ok bool) {
	if b.v5 == nil {
		return "", false
	}
	addr, err := port.Locate5(key)
	if err != nil || addr.Module != port.ModuleGitHub || addr.Kind != "app" {
		return "", false
	}
	return addr.ID, true
}

// ExportGitHubApps says which catalogue GitHub Apps have `export: true`. Unset
// exports none; a runner App is always exported.
func (b *Base) ExportGitHubApps(exported func(id string) bool) *Base {
	b.exportApp = exported
	return b
}

// DeclaredGitHubApps is what the configuration's `apps.github.apps` says of the
// Apps, so that a record is written the way the configuration declares it. A
// nil func declares nothing.
type DeclaredGitHubApps struct {
	// Labels are the labels of the link or catalogue App with the id.
	Labels func(id string) map[string]string
	// RunnerLabels are the labels of the runner App of a tier in an organisation.
	RunnerLabels func(tier, org string) map[string]string
	// AppRef is the id of the App an organisation uses. It applies on layout v5
	// only, where an organisation must refer to an App.
	AppRef func(org string) string
}

// DeclareGitHubApps sets what the configuration declares. The labels it gives
// replace those of the record being written (the configuration is where they
// are set, so a change of it reaches the next write); an App it declares none
// for keeps the record's own. The App an organisation uses is filled in when
// the record names none.
func (b *Base) DeclareGitHubApps(d DeclaredGitHubApps) *Base {
	b.declared = d
	return b
}

// labelsFor are the labels a record is written with: the declared ones when
// there are any, and otherwise its own.
func labelsFor(declared, own map[string]string) map[string]string {
	if len(declared) > 0 {
		return declared
	}
	return own
}

// v4Writes is whether a credential also goes to layout v4: whenever the
// installation has v4 stores.
func (b *Base) v4Writes() bool { return b.v4 != nil || b.v5 != nil }

// v4Reads is whether layout v4 is read first.
func (b *Base) v4Reads() bool { return b.v4 != nil || b.v5 != nil }

// keepInternal is whether an exported credential is also kept as an internal
// one: only when there are no v4 stores to export it to.
func (b *Base) keepInternal() bool { return b.v4 == nil && b.v5 == nil }

// externalApp is the exported document of the App with the id, wherever the
// layout keeps it: external/github/<id> on layout v5 and github/<id> on v4.
// A catalogue id that begins "runner-" fails every call on v4 (see
// [secretstore.CheckAppName]); layout v5 has one id space, so the runner Apps
// are told apart by the id alone.
func (b *Base) externalApp(id string) state.Value[secretstore.GitHubv1] {
	if b.v5 != nil {
		return b.v5.GitHubExternal().App(id)
	}
	return b.v4.External.GitHubApp(id)
}

// externalRunnerApp is [Base.externalApp] of a runner App.
func (b *Base) externalRunnerApp(tier, org string) state.Value[secretstore.GitHubv1] {
	if b.v5 != nil {
		return b.v5.GitHubExternal().App(appid.RunnerID(tier, org))
	}
	return b.v4.External.GitHubRunnerApp(tier, org)
}

// deleteExternalApp removes the exported document of the App with the id.
func (b *Base) deleteExternalApp(ctx context.Context, id string) error {
	if b.v5 != nil {
		return b.v5.GitHubExternal().DeleteApp(ctx, id)
	}
	return b.deleteExternal(ctx, b.v4.External.Store(), "github/"+id)
}

func (b *Base) putGitHub(
	ctx context.Context, value state.Value[secretstore.GitHubv1], appID, installationID int64, privateKey, webhookSecret string,
) error {
	_, err := b.overwriteGitHub(ctx, value, secretstore.GitHubv1{
		AppID: strconv.FormatInt(appID, 10), InstallationID: strconv.FormatInt(installationID, 10), PrivateKey: privateKey,
		WebhookSecret: webhookSecret,
	})
	return err
}

func (b *Base) overwriteGitHub(ctx context.Context, value state.Value[secretstore.GitHubv1], doc secretstore.GitHubv1) (state.Rev, error) {
	for range attempts {
		cur, rev, err := value.Get(ctx)
		switch {
		case errors.Is(err, state.ErrNotFound):
			rev = ""
		case err != nil:
			return "", err
		case cur.AppID == doc.AppID && cur.InstallationID == doc.InstallationID && cur.PrivateKey == doc.PrivateKey &&
			cur.WebhookSecret == doc.WebhookSecret:
			return rev, nil // identical: no new revision
		}
		out, err := value.Put(ctx, doc, rev)
		if errors.Is(err, state.ErrConflict) {
			continue
		}
		return out, err
	}
	return "", ErrBusy
}

func (b *Base) getGitHub(ctx context.Context, value state.Value[secretstore.GitHubv1]) (secretstore.GitHubv1, bool, error) {
	doc, _, err := value.Get(ctx)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return secretstore.GitHubv1{}, false, nil
	case err != nil:
		return secretstore.GitHubv1{}, false, err
	}
	return doc, true, nil
}

func (b *Base) deleteExternal(ctx context.Context, st state.Store, key string) error {
	if err := st.Delete(ctx, key); err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// externalSlackApp is the exported bot token of the Slack App: external/slack/<app>
// wherever the layout keeps it, which is the same address on v4 and v5.
func (b *Base) externalSlackApp(app string) state.Value[secretstore.Slackv1] {
	if b.v5 != nil {
		return b.v5.SlackExternal().App(app)
	}
	return b.v4.External.SlackApp(app)
}

// deleteExternalSlackApp removes the exported bot token of the Slack App.
func (b *Base) deleteExternalSlackApp(ctx context.Context, app string) error {
	if b.v5 != nil {
		return b.v5.SlackExternal().DeleteApp(ctx, app)
	}
	return b.deleteExternal(ctx, b.v4.External.Store(), "slack/"+app)
}

func (b *Base) putSlack(ctx context.Context, app, token string) error {
	value := b.externalSlackApp(app)
	for range attempts {
		cur, rev, err := value.Get(ctx)
		switch {
		case errors.Is(err, state.ErrNotFound):
			rev = ""
		case err != nil:
			return err
		case cur.BotToken == token:
			return nil
		}
		if _, err = value.Put(ctx, secretstore.Slackv1{BotToken: token}, rev); errors.Is(err, state.ErrConflict) {
			continue
		} else if err != nil {
			return err
		}
		return nil
	}
	return ErrBusy
}

func (b *Base) getSlack(ctx context.Context, app string) (string, bool, error) {
	doc, _, err := b.externalSlackApp(app).Get(ctx)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return doc.BotToken, true, nil
}
