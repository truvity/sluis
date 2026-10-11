package hubwire

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/secrets"
)

// Opener builds a backend from a declared workspace and the key read for it.
type Opener func(ctx context.Context, d *hub.Declared) (backend.Backend, error)

// AdoptDeclared brings up the workspaces the deployment owns.
//
// A declared workspace that cannot be adopted stops the process rather than
// being skipped. The deployment asked for a directory; starting without it
// means answering "no opinion" about every address in it, which reads to a
// consumer exactly like a tenant that was removed.
//
// A workspace the store already holds exactly as declared is attached without
// reading its key: the key is read, through [LazyBackend], when the workspace
// is first used, and the probe loop checks it. Only a workspace the store
// lacks, or holds differently, is adopted now, and that reads its key once.
func AdoptDeclared(
	ctx context.Context, directory *hub.Hub, store hub.Store, workspaces []config.DirectoryWorkspace,
	src secrets.Source, open Opener, log *slog.Logger,
) (map[string]bool, error) {
	adopted := map[string]bool{}
	var stored []hub.Workspace
	if len(workspaces) > 0 && store != nil {
		var err error
		if stored, err = store.List(ctx); err != nil {
			return nil, fmt.Errorf("read the stored workspaces: %w", err)
		}
	}
	for i := range workspaces {
		w := &workspaces[i]
		name := w.Backend + "/" + w.Admin
		if src == nil {
			return nil, fmt.Errorf("declared workspace %q: no secrets source is configured", name)
		}
		d := hub.Declared{ID: w.ID, Backend: w.Backend, Admin: w.Admin, Serve: w.Serve, SyncGroups: w.SyncGroups}
		if ws, ok := StoredAsDeclared(stored, w); ok {
			if err := directory.Attach(ctx, ws.ID, NewLazyBackend(d, src, w.KeySecret, open)); err != nil {
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
			slog.String("workspace", ws.ID), slog.String("backend", ws.Backend),
			slog.String("admin", ws.Admin), slog.Any("domains", ws.Domains), slog.Any("served", ws.Served()))
	}
	return adopted, nil
}

// StoredAsDeclared finds the stored record of a declared workspace when it
// holds what the declaration says, so that nothing needs adopting again.
func StoredAsDeclared(stored []hub.Workspace, w *config.DirectoryWorkspace) (hub.Workspace, bool) {
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

// ReopenStored brings back what a console added before the last restart.
//
// Three cases. A workspace the deployment declares was already opened by
// [AdoptDeclared]. A workspace that was declared when it was last written and
// is not any more has been taken out of the document: its record is stale, and
// leaving it would be a directory nobody could disconnect, so it is deleted.
// Everything else was connected in a console and is opened from the credential
// stored beside it.
//
// A credential that cannot be read does not stop the hub. The workspace stays,
// with no reader, and the console shows it as unhealthy with the reason, which
// is the state of a directory that is refusing the credential. Refusing to
// start would take every other directory down with it.
func ReopenStored(
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
			log.InfoContext(ctx, "forgetting a workspace the deployment no longer declares",
				slog.String("workspace", ws.ID), slog.String("admin", ws.Admin))
			if err = workspaces.Delete(ctx, ws.ID); err != nil {
				return fmt.Errorf("forget declared workspace %s: %w", ws.ID, err)
			}
			continue
		}
		cred, found, err := credentials.Load(ctx, ws.ID)
		if err != nil || !found {
			log.WarnContext(ctx, "a connected workspace has no usable credential; it answers for nothing until it is reconnected",
				slog.String("workspace", ws.ID), slog.String("backend", ws.Backend), slog.Any("error", err))
			continue
		}
		reader, err := open(ctx, ws.Backend, cred)
		if err != nil {
			log.WarnContext(ctx, "a connected workspace could not be reopened; it answers for nothing until it is reconnected",
				slog.String("workspace", ws.ID), slog.String("backend", ws.Backend), slog.Any("error", err))
			continue
		}
		if err = directory.Attach(ctx, ws.ID, reader); err != nil {
			return fmt.Errorf("attach workspace %s: %w", ws.ID, err)
		}
		log.InfoContext(ctx, "connected workspace reopened",
			slog.String("workspace", ws.ID), slog.String("backend", ws.Backend), slog.String("credential", cred.Type),
			slog.String("admin", cred.Admin), slog.Any("served", ws.Served()))
	}
	return nil
}
