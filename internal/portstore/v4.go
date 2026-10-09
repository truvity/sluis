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

func (b *Base) putSlack(ctx context.Context, app, token string) error {
	value := b.v4.External.SlackApp(app)
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
	doc, _, err := b.v4.External.SlackApp(app).Get(ctx)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return doc.BotToken, true, nil
}
