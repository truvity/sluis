package portstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/hub"
)

const wsDirPrefix = "ws.dir."

// DefaultDirectoryProvider is the provider of a workspace whose record is not
// there yet to say (a credential saved before its record) and whose backend
// names none: the one identity directory sluis has.
const DefaultDirectoryProvider = "google"

// wsDirKey is the key of a directory workspace: `ws.dir.<provider>.<id>`, the
// provider being the record's backend, so that the layout puts it at
// `directory/<provider>/<id>` and another identity directory (Entra ID) is a
// provider, not a family.
func wsDirKey(provider, id string) string { return wsDirPrefix + seg(provider) + "." + seg(id) }

// dirKey is the key of workspace id: the one it already has, whichever
// provider it is under, or else the key a new one gets under the backend.
func (b *Base) dirKey(ctx context.Context, id, backend string) (string, error) {
	records, err := b.listAll(ctx, wsDirPrefix)
	if err != nil {
		return "", err
	}
	for _, rec := range records {
		if strings.HasSuffix(rec.Key, "."+seg(id)) {
			return rec.Key, nil
		}
	}
	if backend == "" {
		backend = DefaultDirectoryProvider
	}
	return wsDirKey(backend, id), nil
}

// wsRecord is the stored shape of a directory workspace, written out field by
// field so that no credential can arrive in a record by accident.
type wsRecord struct {
	ID          string    `json:"id"`
	Backend     string    `json:"backend"`
	Domains     []string  `json:"domains,omitempty"`
	Serve       []string  `json:"serve,omitempty"`
	Admin       string    `json:"admin,omitempty"`
	Credential  string    `json:"credential,omitempty"`
	ConnectedBy string    `json:"connectedBy,omitempty"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
	ProbedAt    time.Time `json:"probedAt,omitzero"`
	Healthy     bool      `json:"healthy,omitempty"`
	Error       string    `json:"error,omitempty"`
	Declared    bool      `json:"declared,omitempty"`
	SyncGroups  []string  `json:"syncGroups,omitempty"`
}

func toWSRecord(ws hub.Workspace) wsRecord {
	return wsRecord{
		ID: ws.ID, Backend: ws.Backend, Domains: ws.Domains, Serve: ws.Serve, Admin: ws.Admin,
		Credential: string(ws.Credential), ConnectedBy: ws.ConnectedBy, ConnectedAt: ws.ConnectedAt,
		ProbedAt: ws.Health.ProbedAt, Healthy: ws.Health.OK, Error: ws.Health.Error,
		Declared: ws.Declared, SyncGroups: ws.SyncGroups,
	}
}

func (r wsRecord) workspace() hub.Workspace {
	return hub.Workspace{
		ID: r.ID, Backend: r.Backend, Domains: r.Domains, Serve: r.Serve, Admin: r.Admin,
		Credential: hub.CredentialType(r.Credential), ConnectedBy: r.ConnectedBy, ConnectedAt: r.ConnectedAt,
		Health:   hub.Health{ProbedAt: r.ProbedAt, OK: r.Healthy, Error: r.Error},
		Declared: r.Declared, SyncGroups: r.SyncGroups,
	}
}

// credentialDoc is what a workspace's credential holds.
type credentialDoc struct {
	Type  string `json:"type"`
	Admin string `json:"admin,omitempty"`
	Data  []byte `json:"data"`
}

// Workspaces is a [hub.Store] and Credentials a [hub.CredentialStore] over
// the same items: `ws.dir.<provider>.<id>` holds the record and the credential in Secrets,
// so a probe that rewrites the record every minute carries the credential
// along untouched, and a workspace's record and credential are never out of
// step. An item with a credential and no record (the credential saved first)
// is not a workspace yet and is not listed.
type Workspaces struct{ b *Base }

var _ hub.Store = (*Workspaces)(nil)

// NewWorkspaces returns the record store.
func NewWorkspaces(b *Base) *Workspaces { return &Workspaces{b: b} }

// List implements [hub.Store].
func (w *Workspaces) List(ctx context.Context) ([]hub.Workspace, error) {
	records, err := w.b.listAll(ctx, wsDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the workspaces: %w", err)
	}
	out := make([]hub.Workspace, 0, len(records))
	for _, rec := range records {
		it, err := decodeItem(rec.Value)
		if err != nil {
			// One unreadable item must not hide the rest: the hub would
			// answer "no opinion" for every other workspace's domains.
			return nil, fmt.Errorf("portstore: %s: %w", rec.Key, err)
		}
		if len(it.Record) == 0 {
			continue
		}
		var r wsRecord
		if err = json.Unmarshal(it.Record, &r); err != nil || r.ID == "" {
			return nil, fmt.Errorf("portstore: %s holds no readable workspace", rec.Key)
		}
		out = append(out, r.workspace())
	}
	slices.SortFunc(out, func(a, b hub.Workspace) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get implements [hub.Store].
func (w *Workspaces) Get(ctx context.Context, id string) (hub.Workspace, error) {
	key, err := w.b.dirKey(ctx, id, "")
	if err != nil {
		return hub.Workspace{}, fmt.Errorf("portstore: read workspace %s: %w", id, err)
	}
	it, err := w.b.getItem(ctx, key)
	if err != nil {
		return hub.Workspace{}, fmt.Errorf("portstore: read workspace %s: %w", id, err)
	}
	if it == nil || len(it.Record) == 0 {
		return hub.Workspace{}, fmt.Errorf("%w: %s", hub.ErrNotFound, id)
	}
	var r wsRecord
	if err = json.Unmarshal(it.Record, &r); err != nil || r.ID == "" {
		return hub.Workspace{}, fmt.Errorf("portstore: workspace %s is not readable", id)
	}
	return r.workspace(), nil
}

// Put implements [hub.Store]: the record is replaced, the credential
// beside it stays.
func (w *Workspaces) Put(ctx context.Context, ws hub.Workspace) error {
	if ws.ID == "" {
		return errors.New("portstore: workspace id is required")
	}
	raw, err := json.Marshal(toWSRecord(ws))
	if err != nil {
		return err
	}
	key, err := w.b.dirKey(ctx, ws.ID, ws.Backend)
	if err != nil {
		return fmt.Errorf("portstore: store workspace %s: %w", ws.ID, err)
	}
	err = w.b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		next := &item{Record: raw}
		if cur != nil {
			next.Secret = cur.Secret
		}
		return next, nil
	})
	if err != nil {
		return fmt.Errorf("portstore: store workspace %s: %w", ws.ID, err)
	}
	return nil
}

// Delete implements [hub.Store]: the record goes, and the item with it unless
// a credential is still kept (that is [Credentials.Delete]'s to remove).
func (w *Workspaces) Delete(ctx context.Context, id string) error {
	key, err := w.b.dirKey(ctx, id, "")
	if err != nil {
		return fmt.Errorf("portstore: delete workspace %s: %w", id, err)
	}
	err = w.b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		if cur == nil || cur.Secret == "" {
			return nil, nil
		}
		return &item{Secret: cur.Secret}, nil
	})
	if err != nil {
		return fmt.Errorf("portstore: delete workspace %s: %w", id, err)
	}
	return nil
}

// Credentials is the [hub.CredentialStore] over the same items.
type Credentials struct{ b *Base }

var _ hub.CredentialStore = (*Credentials)(nil)

// NewCredentials returns the credential store.
func NewCredentials(b *Base) *Credentials { return &Credentials{b: b} }

// Load implements [hub.CredentialStore].
func (s *Credentials) Load(ctx context.Context, workspaceID string) (backend.Credential, bool, error) {
	key, err := s.b.dirKey(ctx, workspaceID, "")
	if err != nil {
		return backend.Credential{}, false, fmt.Errorf("portstore: read the credential of %s: %w", workspaceID, err)
	}
	it, err := s.b.getItem(ctx, key)
	if err != nil {
		return backend.Credential{}, false, fmt.Errorf("portstore: read the credential of %s: %w", workspaceID, err)
	}
	if it == nil || it.Secret == "" {
		return backend.Credential{}, false, nil
	}
	plain, err := s.b.getSecret(ctx, key, it.Secret)
	if err != nil {
		return backend.Credential{}, false, fmt.Errorf("portstore: open the credential of %s: %w", workspaceID, err)
	}
	var doc credentialDoc
	if err = json.Unmarshal(plain, &doc); err != nil || doc.Type == "" || len(doc.Data) == 0 {
		return backend.Credential{}, false, fmt.Errorf("portstore: the credential of %s is incomplete: it needs a type and a credential", workspaceID)
	}
	return backend.Credential{Type: doc.Type, Admin: doc.Admin, Data: doc.Data}, true, nil
}

// Save implements [hub.CredentialStore]: the credential is replaced, a record
// kept beside it stays.
func (s *Credentials) Save(ctx context.Context, workspaceID string, cred backend.Credential) error {
	key, err := s.b.dirKey(ctx, workspaceID, "")
	if err != nil {
		return fmt.Errorf("portstore: write the credential of %s: %w", workspaceID, err)
	}
	plain, _ := json.Marshal(credentialDoc{Type: cred.Type, Admin: cred.Admin, Data: cred.Data})
	ref, err := s.b.newSecret(ctx, key, plain)
	if err != nil {
		return fmt.Errorf("portstore: write the credential of %s: %w", workspaceID, err)
	}
	err = s.b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		next := &item{Secret: ref}
		if cur != nil {
			next.Record = cur.Record
		}
		return next, nil
	})
	if err != nil {
		return fmt.Errorf("portstore: store the credential of %s: %w", workspaceID, err)
	}
	return nil
}

// Delete implements [hub.CredentialStore].
func (s *Credentials) Delete(ctx context.Context, workspaceID string) error {
	key, err := s.b.dirKey(ctx, workspaceID, "")
	if err != nil {
		return fmt.Errorf("portstore: delete the credential of %s: %w", workspaceID, err)
	}
	err = s.b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		if cur == nil || len(cur.Record) == 0 {
			return nil, nil
		}
		return &item{Record: cur.Record}, nil
	})
	if err != nil {
		return fmt.Errorf("portstore: delete the credential of %s: %w", workspaceID, err)
	}
	return nil
}
