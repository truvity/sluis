package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

// What the Slack pages that have not moved into the module yet still ask of it.
// Each of these goes when the page that uses it does.

type slackBook = slackconsole.Book

func (c *Console) slackBook(ctx context.Context) (slackBook, error) { return c.slackSvc().Book(ctx) }

func (c *Console) slackWorkspaceStore() (SlackWorkspaces, error) {
	return c.slackSvc().WorkspaceStore()
}

func (c *Console) requireSlack(ctx context.Context, want access.Role, workspace string) (access.Identity, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return id, err
	}
	got, err := c.slackSvc().RequireSlack(ctx, consolecall.Snapshot(id), want, workspace)
	return got, slackError(err)
}

func (c *Console) requireAnySlack(ctx context.Context, want access.Role) (access.Identity, slackBook, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return id, nil, err
	}
	got, book, err := c.slackSvc().RequireAnySlack(ctx, consolecall.Snapshot(id), want)
	return got, book, slackError(err)
}

func connectMessage(err error) string {
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Message()
	}
	return err.Error()
}

// slackWorkspaceOfBind is the Slack workspace a catalogue flow's signed state
// binds, so its callback can ask the role question again, now.
func (c *Console) slackWorkspaceOfBind(bind string) string {
	id, ok := strings.CutPrefix(bind, slackconsole.CatalogueBind)
	if !ok || c.deps.SlackCatalogue == nil {
		return ""
	}
	entry, _ := c.deps.SlackCatalogue.Get(id)
	return entry.Workspace
}
