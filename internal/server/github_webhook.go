package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/storage/logattr"
)

// The defaults of a rotation's wait for the new target: long enough for an
// External Secrets refresh and a consumer's reload, short enough that an
// operator is not left staring at a spinner.
const (
	webhookPingWindow = 2 * time.Minute
	webhookPingEvery  = 5 * time.Second
	webhookTimeout    = 10 * time.Second
)

// RotateGitHubAppWebhook replaces the secret a catalogue App's webhook
// deliveries are signed with.
//
// Neither Argo CD nor Kargo accepts two secrets, so a rotation cannot
// overlap: the consumers must hold the new secret before GitHub signs with
// it, and GitHub holds exactly one. The order is therefore: keep the new
// secret where the consumers read it; wait until the (new) target accepts a
// ping signed with it, which proves the consumer reloaded; only then tell
// GitHub, the secret and a Kargo receiver's derived URL in one call. A
// failure before that restores the old secret, so deliveries go on exactly
// as they were.
func (c *Console) RotateGitHubAppWebhook(
	ctx context.Context, req *connect.Request[directoryrosterv1.RotateGitHubAppWebhookRequest],
) (*connect.Response[directoryrosterv1.RotateGitHubAppWebhookResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id, err := c.requireApp(ctx, access.RoleOperator, &spec)
	if err != nil {
		return nil, err
	}
	if err = c.rotateWebhook(ctx, id.Who(), spec); err != nil {
		return nil, err
	}
	// The record changed: read the App again.
	if spec, err = c.githubAppSpec(ctx, spec.id); err != nil {
		return nil, err
	}
	app := c.githubAppView(ctx, spec, true)
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	c.decorateApp(app, id, &spec, ownerDomains(dirs))
	return connect.NewResponse(&directoryrosterv1.RotateGitHubAppWebhookResponse{App: app}), nil
}

func (c *Console) rotateWebhook(ctx context.Context, actor string, spec githubAppSpec) error {
	entry := spec.entry
	if spec.origin != appCatalogue || !spec.declared || entry.Webhook == nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s declares no webhook: only a catalogue App with a `webhook` has a secret to rotate", spec.id))
	}
	store, err := c.catalogueStore()
	if err != nil {
		return err
	}
	if !c.beginRotation(spec.id) {
		return connect.NewError(connect.CodeAborted, fmt.Errorf("the webhook of %s is being rotated already", spec.id))
	}
	defer c.endRotation(spec.id)

	record, key, created, err := store.Get(ctx, spec.id)
	switch {
	case err != nil:
		return connect.NewError(connect.CodeUnavailable, err)
	case !created:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no App %s has been created", spec.id))
	case key == "":
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the key of %s is not kept here, so GitHub cannot be told anything as the App", spec.id))
	}
	old, hadOld, err := store.WebhookSecret(ctx, spec.id)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the stored key of %s is not usable: %w", spec.id, err))
	}
	secret, err := clientcreds.Generate()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	target := entry.Webhook.Target(secret)
	who, app := audit.Identified(actor), audit.App{Name: spec.id, ID: record.AppID, Slug: record.AppSlug}
	step := func(name string, outcome audit.Outcome) {
		c.record(ctx, audit.CatalogueAppWebhookChanged(who, record.Org, app, name, outcome))
	}

	// 1. The new secret is kept where the consumers read it.
	if err = store.PutWebhookSecret(ctx, spec.id, secret); err != nil {
		step(audit.WebhookStaged, audit.Failed(err.Error()))
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("the new webhook secret of %s could not be kept: %w", spec.id, err))
	}
	step(audit.WebhookStaged, audit.Succeeded())

	// back puts the old secret back, so the consumers return to the one
	// GitHub still signs with. It outlives a cancelled request.
	back := func(cause error) error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookTimeout)
		defer cancel()
		if !hadOld {
			return cause // there was no earlier secret to return to
		}
		if rerr := store.PutWebhookSecret(restoreCtx, spec.id, old); rerr != nil {
			c.log().ErrorContext(ctx, "a webhook rotation failed and the previous secret could not be put back",
				slog.String("id", spec.id), logattr.SafeError("error", rerr))
			c.record(ctx, audit.CatalogueAppWebhookChanged(who, record.Org, app, audit.WebhookRestored, audit.Failed(rerr.Error())))
			return fmt.Errorf("%w; the previous secret could not be put back (%v): the consumers may hold a secret GitHub does not sign with", cause, rerr)
		}
		c.record(ctx, audit.CatalogueAppWebhookChanged(who, record.Org, app, audit.WebhookRestored, audit.Succeeded()))
		return cause
	}

	// 2. The new target accepts a ping signed with the new secret.
	if err = c.awaitPing(ctx, target, secret); err != nil {
		step(audit.WebhookVerified, audit.Failed(err.Error()))
		return connect.NewError(connect.CodeFailedPrecondition, back(fmt.Errorf(
			"%w. GitHub was not told, and the previous secret is back in place. Check that the consumer reads the secret the console keeps, then rotate again", err)))
	}
	step(audit.WebhookVerified, audit.Succeeded())

	// 3. GitHub is told: secret and URL in one call.
	if _, err = githubapp.PatchHookConfig(ctx, c.githubHTTP(), token, githubapp.HookUpdate{URL: target, Secret: secret}); err != nil {
		step(audit.WebhookRotated, audit.Failed(err.Error()))
		return connect.NewError(connect.CodeUnavailable, back(fmt.Errorf("GitHub would not take the new webhook: %w", err)))
	}

	// 4. What GitHub now holds is recorded. A failure here leaves everything
	// consistent except the display and the drift baseline.
	record.WebhookURL, record.HookRotatedAt = target, time.Now().UTC()
	if err = store.Put(ctx, record, key); err != nil {
		step(audit.WebhookRotated, audit.Failed(err.Error()))
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf(
			"the webhook of %s was rotated on GitHub and the console could not record it: %w", spec.id, err))
	}
	c.githubSeen.forget(spec.id)
	step(audit.WebhookRotated, audit.Succeeded())
	return nil
}

// awaitPing sends the signed ping until the target answers 2xx, or the
// window ends. It is bounded: a consumer that never reloads is an error the
// operator reads, not a rotation that hangs.
func (c *Console) awaitPing(ctx context.Context, target, secret string) error {
	window, every := c.deps.WebhookPingWindow, c.deps.WebhookPingEvery
	if window <= 0 {
		window = webhookPingWindow
	}
	if every <= 0 {
		every = webhookPingEvery
	}
	client := c.deps.WebhookHTTP
	if client == nil {
		client = &http.Client{
			Timeout:       webhookTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	deadline := time.Now().Add(window)
	for {
		attempt, cancel := context.WithTimeout(ctx, webhookTimeout)
		err := githubapp.Ping(attempt, client, target, secret)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("the rotation was cancelled while waiting for the consumer: %w", ctx.Err())
		}
		if time.Until(deadline) < every {
			return fmt.Errorf("the webhook target did not accept the new secret within %s (last answer: %w)", window, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the rotation was cancelled while waiting for the consumer: %w", ctx.Err())
		case <-time.After(every):
		}
	}
}

func (c *Console) beginRotation(id string) bool {
	c.rotatingMu.Lock()
	defer c.rotatingMu.Unlock()
	if c.rotating[id] {
		return false
	}
	if c.rotating == nil {
		c.rotating = map[string]bool{}
	}
	c.rotating[id] = true
	return true
}

func (c *Console) endRotation(id string) {
	c.rotatingMu.Lock()
	defer c.rotatingMu.Unlock()
	delete(c.rotating, id)
}
