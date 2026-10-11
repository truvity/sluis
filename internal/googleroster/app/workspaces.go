package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/secrets"
)

// The workspace wiring below is the issuer's (internal/app), narrowed to what a
// module with no console needs. TODO(V03b): the issuer stops adopting and
// reopening readers when its hub only reads; the two copies then become one.

type opener func(ctx context.Context, d *hub.Declared) (backend.Backend, error)

// adoptDeclared brings up the workspaces the deployment owns. One that cannot
// be adopted stops the module: starting without a directory the deployment
// asked for reads, to a consumer, like a tenant that was removed. A workspace
// the store already holds as declared is attached without reading its key; the
// key is read, through [lazyBackend], when it is first used.
func adoptDeclared(
	ctx context.Context, directory *hub.Hub, store hub.Store, declared []config.DirectoryWorkspace,
	src secrets.Source, open opener, log *slog.Logger,
) (map[string]bool, error) {
	adopted := map[string]bool{}
	if len(declared) == 0 {
		return adopted, nil
	}
	stored, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the stored workspaces: %w", err)
	}
	for i := range declared {
		w := &declared[i]
		name := w.Backend + "/" + w.Admin
		if src == nil {
			return nil, fmt.Errorf("declared workspace %q: no secrets source is configured", name)
		}
		d := hub.Declared{ID: w.ID, Backend: w.Backend, Admin: w.Admin, Serve: w.Serve, SyncGroups: w.SyncGroups}
		if ws, ok := storedAsDeclared(stored, w); ok {
			if err = directory.Attach(ctx, ws.ID, newLazyBackend(d, src, w.KeySecret, open)); err != nil {
				return nil, fmt.Errorf("attach declared workspace %q: %w", name, err)
			}
			adopted[ws.ID] = true
			log.InfoContext(ctx, "declared workspace attached; its key is read when it is first used",
				slog.String("workspace", ws.ID), slog.String("backend", ws.Backend), slog.String("admin", ws.Admin))
			continue
		}
		key, err := src.Get(ctx, w.KeySecret)
		if err != nil {
			return nil, fmt.Errorf("declared workspace %q: keySecret: %w", name, err)
		}
		d.Key = []byte(key)
		reader, err := open(ctx, &d)
		if err != nil {
			return nil, fmt.Errorf("declared workspace %q: %w", name, err)
		}
		ws, err := directory.Adopt(ctx, hub.Workspace{
			ID: d.ID, Admin: d.Admin, Credential: hub.CredentialServiceAccountKey,
			Serve: d.Serve, SyncGroups: d.SyncGroups, Declared: true,
		}, reader)
		if err != nil {
			return nil, fmt.Errorf("adopt declared workspace %q: %w", d.Admin, err)
		}
		adopted[ws.ID] = true
		log.InfoContext(ctx, "declared workspace adopted",
			slog.String("workspace", ws.ID), slog.String("backend", ws.Backend), slog.String("admin", ws.Admin))
	}
	return adopted, nil
}

// storedAsDeclared finds the stored record of a declared workspace when it holds
// what the declaration says, so that nothing needs adopting again.
func storedAsDeclared(stored []hub.Workspace, w *config.DirectoryWorkspace) (hub.Workspace, bool) {
	serve := make([]string, 0, len(w.Serve))
	for _, d := range w.Serve {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			serve = append(serve, d)
		}
	}
	slices.Sort(serve)
	serve = slices.Compact(serve)
	for i := range stored {
		ws := &stored[i]
		if !ws.Declared || ws.Backend != w.Backend || !strings.EqualFold(ws.Admin, w.Admin) {
			continue
		}
		if w.ID != "" && !strings.EqualFold(ws.ID, w.ID) {
			continue
		}
		if slices.Equal(ws.Serve, serve) || (len(ws.Serve) == 0 && len(serve) == 0) {
			if slices.Equal(ws.SyncGroups, w.SyncGroups) || (len(ws.SyncGroups) == 0 && len(w.SyncGroups) == 0) {
				return *ws, true
			}
		}
	}
	return hub.Workspace{}, false
}

// reopenStored brings back what a console added: every workspace the store holds
// that the document does not declare is opened from the credential stored beside
// it. A workspace that was declared and is not any more is forgotten. A
// credential that cannot be read does not stop the module: the workspace stays
// with no reader, and the probe reports it, which takes no other directory down.
func reopenStored(
	ctx context.Context, directory *hub.Hub, workspaces hub.Store, credentials hub.CredentialStore, adopted map[string]bool,
	open func(ctx context.Context, kind string, cred backend.Credential) (backend.Backend, error), log *slog.Logger,
) error {
	list, err := workspaces.List(ctx)
	if err != nil {
		return fmt.Errorf("read the stored workspaces: %w", err)
	}
	for i := range list {
		ws := &list[i]
		if adopted[ws.ID] {
			continue
		}
		if ws.Declared {
			log.InfoContext(ctx, "forgetting a workspace the deployment no longer declares", slog.String("workspace", ws.ID))
			if err = workspaces.Delete(ctx, ws.ID); err != nil {
				return fmt.Errorf("forget declared workspace %s: %w", ws.ID, err)
			}
			continue
		}
		cred, found, err := credentials.Load(ctx, ws.ID)
		if err != nil || !found {
			log.WarnContext(ctx, "a connected workspace has no usable credential; it answers for nothing until it is reconnected",
				slog.String("workspace", ws.ID), slog.Any("error", err))
			continue
		}
		reader, err := open(ctx, ws.Backend, cred)
		if err != nil {
			log.WarnContext(ctx, "a connected workspace could not be reopened; it answers for nothing until it is reconnected",
				slog.String("workspace", ws.ID), slog.Any("error", err))
			continue
		}
		if err = directory.Attach(ctx, ws.ID, reader); err != nil {
			return fmt.Errorf("attach workspace %s: %w", ws.ID, err)
		}
	}
	return nil
}

// lazyBackend reads a declared workspace through a service-account key that is
// read when the workspace is first used, and again after [lazy.TTL]: a start that
// attached it reads no secret, and a rotated key is picked up by opening the
// backend again. A key that cannot be read fails that call and the next tries again.
type lazyBackend struct {
	declared hub.Declared
	key      func(context.Context) (string, error)
	open     opener

	mu    sync.Mutex
	inner backend.Backend
	fp    string
	at    time.Time
}

var _ backend.Backend = (*lazyBackend)(nil)

func newLazyBackend(d hub.Declared, src secrets.Source, name string, open opener) *lazyBackend {
	return &lazyBackend{declared: d, open: open, key: func(ctx context.Context) (string, error) { return src.Get(ctx, name) }}
}

func (l *lazyBackend) backend(ctx context.Context) (backend.Backend, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inner != nil && time.Since(l.at) < lazy.TTL {
		return l.inner, nil
	}
	key, err := l.key(ctx)
	if err != nil {
		l.inner = nil
		return nil, fmt.Errorf("declared workspace %q: keySecret: %w", l.declared.Backend+"/"+l.declared.Admin, err)
	}
	if l.inner != nil && key == l.fp {
		l.at = time.Now()
		return l.inner, nil
	}
	d := l.declared
	d.Key = []byte(key)
	inner, err := l.open(ctx, &d)
	if err != nil {
		l.inner = nil
		return nil, fmt.Errorf("declared workspace %q: %w", d.Backend+"/"+d.Admin, err)
	}
	l.inner, l.fp, l.at = inner, key, time.Now()
	return inner, nil
}

// Kind implements [backend.Backend] without opening anything.
func (l *lazyBackend) Kind() string { return l.declared.Backend }

// Tenant implements [backend.Backend].
func (l *lazyBackend) Tenant(ctx context.Context) (backend.Tenant, error) {
	b, err := l.backend(ctx)
	if err != nil {
		return backend.Tenant{}, err
	}
	return b.Tenant(ctx)
}

// Probe implements [backend.Backend].
func (l *lazyBackend) Probe(ctx context.Context) error {
	b, err := l.backend(ctx)
	if err != nil {
		return err
	}
	return b.Probe(ctx)
}

// Accounts implements [backend.Backend].
func (l *lazyBackend) Accounts(ctx context.Context) ([]backend.Account, error) {
	b, err := l.backend(ctx)
	if err != nil {
		return nil, err
	}
	return b.Accounts(ctx)
}

// Groups implements [backend.Backend].
func (l *lazyBackend) Groups(ctx context.Context) ([]backend.Group, error) {
	b, err := l.backend(ctx)
	if err != nil {
		return nil, err
	}
	return b.Groups(ctx)
}

// Account implements [backend.Backend].
func (l *lazyBackend) Account(ctx context.Context, email string) (backend.Account, bool, error) {
	b, err := l.backend(ctx)
	if err != nil {
		return backend.Account{}, false, err
	}
	return b.Account(ctx, email)
}

// GroupsOf implements [backend.Backend].
func (l *lazyBackend) GroupsOf(ctx context.Context, email string) ([]string, error) {
	b, err := l.backend(ctx)
	if err != nil {
		return nil, err
	}
	return b.GroupsOf(ctx, email)
}

// Revoke implements [backend.Backend].
func (l *lazyBackend) Revoke(ctx context.Context) error {
	b, err := l.backend(ctx)
	if err != nil {
		return err
	}
	return b.Revoke(ctx)
}
