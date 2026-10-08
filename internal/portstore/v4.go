package portstore

import (
	"context"
	"errors"
	"strconv"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// WithV4 puts the Apps' exported credentials on layout v4 (ADR 0041): an
// installed App whose key is exported is the whole document at
// external/github/<app>, with the ids from the App's record, and its key is no
// longer an internal credential. A nil v4 (layout v3) changes nothing.
func (b *Base) WithV4(v4 *secretstore.Stores) *Base {
	b.v4 = v4
	return b
}

// ExportGitHubApps says which catalogue GitHub Apps have `export: true`. Unset
// exports none; a runner App is always exported.
func (b *Base) ExportGitHubApps(exported func(id string) bool) *Base {
	b.exportApp = exported
	return b
}

// v4Writes is whether a credential also goes to layout v4.
func (b *Base) v4Writes() bool { return b.v4 != nil && b.v4.Layout.WritesV4() }

// v4Reads is whether layout v4 is read first.
func (b *Base) v4Reads() bool { return b.v4 != nil && b.v4.Layout.ReadsV4() }

// keepInternal is whether an exported credential is also kept as an internal
// one: until the installation is on v4, v3 readers still need it.
func (b *Base) keepInternal() bool { return b.v4 == nil || b.v4.Layout.WritesV3() }

func (b *Base) putGitHub(ctx context.Context, value state.Value[secretstore.GitHubv1], appID, installationID int64, privateKey string) error {
	_, err := b.overwriteGitHub(ctx, value, secretstore.GitHubv1{
		AppID: strconv.FormatInt(appID, 10), InstallationID: strconv.FormatInt(installationID, 10), PrivateKey: privateKey,
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
		case cur.AppID == doc.AppID && cur.InstallationID == doc.InstallationID && cur.PrivateKey == doc.PrivateKey:
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
