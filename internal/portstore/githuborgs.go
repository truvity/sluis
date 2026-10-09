package portstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/port"
)

const (
	ghOrgPrefix  = "gh.org."
	ghGatePrefix = "gate.github."
	ghLinkAppKey = "app.gh.link"
)

func ghOrgKey(org string) string { return ghOrgPrefix + seg(org) }

// GitHubOrgs keeps connected GitHub organisations: `gh.org.<org>` holds the
// record in State and the App credential in Secrets, and the link App is
// `app.gh.link`, built the same way. It is a server.GitHubConnections, a
// server.GitHubLinkApp and a server.GitHubConfirmations.
//
// The kube store keeps a copy of each record inside its credential so that
// the Secret alone restores a connection; one item needs no copy.
type GitHubOrgs struct{ b *Base }

// NewGitHubOrgs returns the store.
func NewGitHubOrgs(b *Base) *GitHubOrgs { return &GitHubOrgs{b: b} }

// Put writes one organisation's record and credential, as one item.
func (s *GitHubOrgs) Put(ctx context.Context, record connection.Record, credential connection.Credential) error {
	if record.Org != credential.Org {
		return fmt.Errorf("a record for %s with a credential for %s", record.Org, credential.Org)
	}
	rawRecord, err := connection.EncodeRecord(record)
	if err != nil {
		return err
	}
	if record.AppRef != "" {
		// The key is the App's, kept once: the item holds the record only.
		if _, err = NewGitHubApps(s.b).requireKey(ctx, record.AppRef); err != nil {
			return err
		}
		return s.b.editItem(ctx, ghOrgKey(record.Org), 0, func(*item) (*item, error) {
			return &item{Record: json.RawMessage(rawRecord)}, nil
		})
	}
	if s.b.v5 != nil {
		return fmt.Errorf("portstore: on layout v5 the key is the App's, not %s's; name the App in app_ref", record.Org)
	}
	credential.Record = nil
	rawCredential, err := connection.EncodeCredential(credential)
	if err != nil {
		return err
	}
	key := ghOrgKey(record.Org)
	ref, err := s.b.newSecret(ctx, key, rawCredential)
	if err != nil {
		return err
	}
	return s.b.editItem(ctx, key, 0, func(*item) (*item, error) {
		return &item{Record: json.RawMessage(rawRecord), Secret: ref}, nil
	})
}

// SetOwner changes one organisation's recorded owner and nothing else, under
// the item's revision: a conflict is retried against what a concurrent write
// left, so a record a concurrent install just wrote is never written over.
func (s *GitHubOrgs) SetOwner(ctx context.Context, org, owner string) (previous string, found bool, err error) {
	err = s.b.editItem(ctx, ghOrgKey(org), 0, func(cur *item) (*item, error) {
		found, previous = false, ""
		if cur == nil || len(cur.Record) == 0 {
			return nil, nil
		}
		record, err := connection.DecodeRecord(string(cur.Record))
		if err != nil {
			return nil, fmt.Errorf("the stored record of %s cannot be read: %w", org, err)
		}
		found, previous = true, record.Owner
		if previous == owner {
			return nil, errKeep
		}
		record.Owner = owner
		raw, err := connection.EncodeRecord(record)
		if err != nil {
			return nil, err
		}
		return &item{Record: json.RawMessage(raw), Secret: cur.Secret}, nil
	})
	return previous, found && err == nil, err
}

// List returns every connected organisation's record, sorted. A record that
// does not decode is skipped: one bad entry must not hide every good one.
func (s *GitHubOrgs) List(ctx context.Context) ([]connection.Record, error) {
	records, err := s.b.listAll(ctx, ghOrgPrefix)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the organisations: %w", err)
	}
	var out []connection.Record
	for _, rec := range records {
		it, err := decodeItem(rec.Value)
		if err != nil || len(it.Record) == 0 {
			continue
		}
		if record, err := connection.DecodeRecord(string(it.Record)); err == nil {
			out = append(out, record)
		}
	}
	slices.SortFunc(out, func(a, b connection.Record) int { return strings.Compare(a.Org, b.Org) })
	return out, nil
}

// Credential reads one organisation's credential, which Disconnect needs to
// revoke the installation before forgetting it.
func (s *GitHubOrgs) Credential(ctx context.Context, org string) (connection.Credential, bool, error) {
	key := ghOrgKey(org)
	it, err := s.b.getItem(ctx, key)
	if err != nil {
		return connection.Credential{}, false, err
	}
	if it != nil && len(it.Record) > 0 {
		if record, err := connection.DecodeRecord(string(it.Record)); err == nil && record.AppRef != "" {
			return s.appCredential(ctx, record)
		}
	}
	if it == nil || it.Secret == "" {
		return connection.Credential{}, false, nil
	}
	plain, err := s.b.getSecret(ctx, key, it.Secret)
	if err != nil {
		return connection.Credential{}, false, err
	}
	credential, err := connection.DecodeCredential(plain)
	return credential, err == nil, err
}

// appCredential is the credential of an organisation that names an App: the
// App's key, and the ids the organisation's record holds.
func (s *GitHubOrgs) appCredential(ctx context.Context, record connection.Record) (connection.Credential, bool, error) {
	key, ok, err := NewGitHubApps(s.b).PrivateKey(ctx, record.AppRef)
	if err != nil || !ok {
		return connection.Credential{}, false, err
	}
	return connection.Credential{
		Version: connection.Version, Org: record.Org, AppID: record.AppID, InstallationID: record.InstallationID, PrivateKey: key,
	}, true, nil
}

// Delete forgets one organisation, its pass request and its confirmation.
func (s *GitHubOrgs) Delete(ctx context.Context, org string) error {
	if err := s.b.deleteItem(ctx, ghOrgKey(org)); err != nil {
		return err
	}
	for _, key := range []string{ghPassKey(org), ghConfirmKey(org)} {
		if err := s.b.State.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// PutLinkApp keeps the connected link App: record in State, credential in Secrets.
func (s *GitHubOrgs) PutLinkApp(ctx context.Context, record link.App, credential link.AppCredential) error {
	rawRecord, err := link.EncodeApp(record)
	if err != nil {
		return err
	}
	credential.Record = nil
	rawCredential, err := link.EncodeAppCredential(credential)
	if err != nil {
		return err
	}
	ref, err := s.b.newSecret(ctx, ghLinkAppKey, rawCredential)
	if err != nil {
		return err
	}
	return s.b.editItem(ctx, ghLinkAppKey, 0, func(*item) (*item, error) {
		return &item{Record: json.RawMessage(rawRecord), Secret: ref}, nil
	})
}

// LinkApp reads the link App's record, if one is connected.
func (s *GitHubOrgs) LinkApp(ctx context.Context) (link.App, bool, error) {
	it, err := s.b.getItem(ctx, ghLinkAppKey)
	if err != nil || it == nil || len(it.Record) == 0 {
		return link.App{}, false, err
	}
	record, err := link.DecodeApp(string(it.Record))
	return record, err == nil, err
}

// LinkAppCredential reads the link App's credential, which redeeming a
// person's authorization needs.
func (s *GitHubOrgs) LinkAppCredential(ctx context.Context) (link.AppCredential, bool, error) {
	it, err := s.b.getItem(ctx, ghLinkAppKey)
	if err != nil || it == nil || it.Secret == "" {
		return link.AppCredential{}, false, err
	}
	plain, err := s.b.getSecret(ctx, ghLinkAppKey, it.Secret)
	if err != nil {
		return link.AppCredential{}, false, err
	}
	credential, err := link.DecodeAppCredential(plain)
	return credential, err == nil, err
}

// DeleteLinkApp forgets the link App.
func (s *GitHubOrgs) DeleteLinkApp(ctx context.Context) error {
	return s.b.deleteItem(ctx, ghLinkAppKey)
}

// ReconcileRecords has nothing to reconcile: a record and its credential are
// one item. It exists so the wiring can call the same step either store has.
func (s *GitHubOrgs) ReconcileRecords(context.Context) ([]string, error) { return nil, nil }

func ghConfirmKey(org string) string { return ghGatePrefix + seg(org) + ".confirm" }
func ghPassKey(org string) string    { return ghGatePrefix + seg(org) + ".pass" }

// PutConfirmation keeps an operator's confirmation of a removal set, for as
// long as it holds.
func (s *GitHubOrgs) PutConfirmation(ctx context.Context, confirmation connection.Confirmation) error {
	raw, err := connection.EncodeConfirmation(confirmation)
	if err != nil {
		return err
	}
	_, err = s.b.State.Put(ctx, ghConfirmKey(confirmation.Org), []byte(raw), connection.ConfirmationTTL)
	return err
}

// Confirmations reads every organisation's confirmation that still stands.
func (s *GitHubOrgs) Confirmations(ctx context.Context) (map[string]connection.Confirmation, error) {
	raws, err := s.b.gates(ctx, ghGatePrefix, ".confirm")
	if err != nil {
		return nil, err
	}
	out := map[string]connection.Confirmation{}
	for org, raw := range raws {
		if confirmation, err := connection.DecodeConfirmation(string(raw)); err == nil && confirmation.Org == org {
			out[org] = confirmation
		}
	}
	return out, nil
}

// RequestPass keeps an operator's request for a pass now, replacing the
// organisation's last one. It keeps nothing, and reports false with when the
// last request was, if that is under connection.PassGap before r.At. The
// check and the write are one compare-and-swap.
func (s *GitHubOrgs) RequestPass(ctx context.Context, r connection.PassRequest) (kept bool, last time.Time, err error) {
	raw, err := connection.EncodePassRequest(r)
	if err != nil {
		return false, time.Time{}, err
	}
	return s.b.requestPass(ctx, ghPassKey(r.Org), raw, r.At, func(old string) (time.Time, error) {
		prev, err := connection.DecodePassRequest(old)
		return prev.At, err
	})
}

// PassRequests reads every organisation's last request for a pass.
func (s *GitHubOrgs) PassRequests(ctx context.Context) (map[string]connection.PassRequest, error) {
	raws, err := s.b.gates(ctx, ghGatePrefix, ".pass")
	if err != nil {
		return nil, err
	}
	out := map[string]connection.PassRequest{}
	for org, raw := range raws {
		if r, err := connection.DecodePassRequest(string(raw)); err == nil && r.Org == org {
			out[org] = r
		}
	}
	return out, nil
}

// Digest summarises the organisations and the link App, by key and revision,
// for a controller that wakes when a connection changes (controller.AppSource).
func (s *GitHubOrgs) Digest(ctx context.Context) ([sha256.Size]byte, error) {
	h := sha256.New()
	records, err := s.b.listAll(ctx, ghOrgPrefix)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	for _, rec := range records {
		h.Write([]byte(rec.Key + "\x00" + string(rec.Revision) + "\x00"))
	}
	switch rec, err := s.b.State.Get(ctx, ghLinkAppKey); {
	case err == nil:
		h.Write([]byte(rec.Key + "\x00" + string(rec.Revision) + "\x00"))
	case !errors.Is(err, port.ErrNotFound):
		return [sha256.Size]byte{}, err
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
