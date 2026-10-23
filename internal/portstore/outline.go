package portstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/outlineroster/connection"
)

const (
	wsOutlinePrefix    = "ws.outline."
	outlineGatePrefix  = "gate.outline."
	outlineConfirmKind = ".confirm."
	outlinePassKind    = ".pass"
)

func wsOutlineKey(workspace string) string { return wsOutlinePrefix + seg(workspace) }

func outlineConfirmKey(workspace, scope string) string {
	return outlineGatePrefix + seg(workspace) + outlineConfirmKind + seg(scope)
}

func outlinePassKey(workspace string) string {
	return outlineGatePrefix + seg(workspace) + outlinePassKind
}

// OutlineWorkspaces keeps connected Outline workspaces: `ws.outline.<workspace>`
// holds the connection record, with the API key in Secrets, as one item, so a
// record and its key are never out of step. Beside them are the operators'
// confirmations of held sets and their requests for a pass. It is what the
// console writes and the Outline controller reads (controller.RecordSource).
type OutlineWorkspaces struct{ b *Base }

// NewOutlineWorkspaces returns the store.
func NewOutlineWorkspaces(b *Base) *OutlineWorkspaces { return &OutlineWorkspaces{b: b} }

// Put writes one workspace's record and API key, as one item.
func (s *OutlineWorkspaces) Put(ctx context.Context, record connection.Record, credential connection.Credential) error {
	if record.Workspace != credential.Workspace {
		return fmt.Errorf("a record for %s with a credential for %s", record.Workspace, credential.Workspace)
	}
	rawRecord, err := connection.EncodeRecord(record)
	if err != nil {
		return err
	}
	rawCredential, err := connection.EncodeCredential(credential)
	if err != nil {
		return err
	}
	key := wsOutlineKey(record.Workspace)
	ref, err := s.b.newSecret(ctx, key, rawCredential)
	if err != nil {
		return err
	}
	return s.b.editItem(ctx, key, 0, func(*item) (*item, error) {
		return &item{Record: json.RawMessage(rawRecord), Secret: ref}, nil
	})
}

// List returns every connected workspace's record, sorted. It reads no key.
func (s *OutlineWorkspaces) List(ctx context.Context) ([]connection.Record, error) {
	records, err := s.b.listAll(ctx, wsOutlinePrefix)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the Outline workspaces: %w", err)
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
	slices.SortFunc(out, func(a, b connection.Record) int { return strings.Compare(a.Workspace, b.Workspace) })
	return out, nil
}

// Record is one workspace's record, without its key.
func (s *OutlineWorkspaces) Record(ctx context.Context, workspace string) (connection.Record, bool, error) {
	it, err := s.b.getItem(ctx, wsOutlineKey(workspace))
	if err != nil || it == nil || len(it.Record) == 0 {
		return connection.Record{}, false, err
	}
	record, err := connection.DecodeRecord(string(it.Record))
	if err != nil {
		return connection.Record{}, false, err
	}
	return record, true, nil
}

// Get reads one workspace's record and API key. found is false when there is
// no key. Only the controller calls it.
func (s *OutlineWorkspaces) Get(ctx context.Context, workspace string) (connection.Record, connection.Credential, bool, error) {
	key := wsOutlineKey(workspace)
	it, err := s.b.getItem(ctx, key)
	if err != nil || it == nil || it.Secret == "" {
		return connection.Record{}, connection.Credential{}, false, err
	}
	plain, err := s.b.getSecret(ctx, key, it.Secret)
	if err != nil {
		return connection.Record{}, connection.Credential{}, false, err
	}
	credential, err := connection.DecodeCredential(plain)
	if err != nil {
		return connection.Record{}, connection.Credential{}, false, err
	}
	var record connection.Record
	if len(it.Record) > 0 {
		if record, err = connection.DecodeRecord(string(it.Record)); err != nil {
			return connection.Record{}, connection.Credential{}, false, err
		}
	}
	return record, credential, true, nil
}

// Delete forgets one workspace: its record and key, its confirmations and its
// pass request.
func (s *OutlineWorkspaces) Delete(ctx context.Context, workspace string) error {
	if err := s.b.deleteItem(ctx, wsOutlineKey(workspace)); err != nil {
		return err
	}
	records, err := s.b.listAll(ctx, outlineGatePrefix+seg(workspace)+".")
	if err != nil {
		return err
	}
	for _, rec := range records {
		if err = s.b.State.Delete(ctx, rec.Key); err != nil {
			return err
		}
	}
	return nil
}

// PutConfirmation keeps an operator's confirmation of a held set, for as long
// as it holds.
func (s *OutlineWorkspaces) PutConfirmation(ctx context.Context, confirmation connection.Confirmation) error {
	raw, err := connection.EncodeConfirmation(confirmation)
	if err != nil {
		return err
	}
	_, err = s.b.State.Put(ctx, outlineConfirmKey(confirmation.Workspace, confirmation.Scope), []byte(raw), connection.ConfirmationTTL)
	return err
}

// Confirmations reads every confirmation that still stands.
func (s *OutlineWorkspaces) Confirmations(ctx context.Context) ([]connection.Confirmation, error) {
	records, err := s.b.listAll(ctx, outlineGatePrefix)
	if err != nil {
		return nil, err
	}
	var out []connection.Confirmation
	for _, rec := range records {
		workspace, scope, ok := strings.Cut(strings.TrimPrefix(rec.Key, outlineGatePrefix), outlineConfirmKind)
		if !ok {
			continue
		}
		c, err := connection.DecodeConfirmation(string(rec.Value))
		if err != nil || c.Workspace != unseg(workspace) || c.Scope != unseg(scope) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// RequestPass keeps an operator's request for a pass now, at most one a
// minute per workspace.
func (s *OutlineWorkspaces) RequestPass(ctx context.Context, r connection.PassRequest) (kept bool, last time.Time, err error) {
	raw, err := connection.EncodePassRequest(r)
	if err != nil {
		return false, time.Time{}, err
	}
	return s.b.requestPass(ctx, outlinePassKey(r.Workspace), raw, r.At, func(old string) (time.Time, error) {
		prev, err := connection.DecodePassRequest(old)
		return prev.At, err
	})
}

// PassRequests reads every workspace's last request for a pass.
func (s *OutlineWorkspaces) PassRequests(ctx context.Context) (map[string]connection.PassRequest, error) {
	raws, err := s.b.gates(ctx, outlineGatePrefix, outlinePassKind)
	if err != nil {
		return nil, err
	}
	out := map[string]connection.PassRequest{}
	for workspace, raw := range raws {
		if r, err := connection.DecodePassRequest(string(raw)); err == nil && r.Workspace == workspace {
			out[workspace] = r
		}
	}
	return out, nil
}

// Digest summarises the workspaces' records, so a connect or a new key wakes a
// pass.
func (s *OutlineWorkspaces) Digest(ctx context.Context) ([sha256.Size]byte, error) {
	return s.b.digest(ctx, wsOutlinePrefix)
}
