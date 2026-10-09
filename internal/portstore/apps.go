package portstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/secretstore"
	slackcatalogueapp "github.com/truvity/sluis/internal/slackapp/catalogueapp"
)

// Apps are `app.<kind>.<name>`: the record in State and the credential in
// Secrets (`credentials/<kind>/<id>/<ref>`)
const (
	ghRunnerPrefix   = "app.gh.runner."
	ghCataloguePfx   = "app.gh.cat."
	slackCataloguePf = "app.slack.cat."
)

// GitHubRunnerApps keeps the runner Apps, `app.gh.runner.<tier>.<org>`.
type GitHubRunnerApps struct{ b *Base }

// NewGitHubRunnerApps returns the store.
func NewGitHubRunnerApps(b *Base) *GitHubRunnerApps { return &GitHubRunnerApps{b: b} }

func runnerKey(tier, org string) string { return ghRunnerPrefix + seg(tier) + "." + seg(org) }

// Put writes one App, replacing what was kept for its tier and organisation.
func (s *GitHubRunnerApps) Put(ctx context.Context, record runnerapp.Record, privateKey string) error {
	keys, err := runnerapp.Encode(record, privateKey)
	if err != nil {
		return err
	}
	key := runnerKey(record.Tier, record.Org)
	// An installed runner App is exported (ADR 0041): its document is the one
	// copy on layout v4, with the ids from the record.
	exported := record.Installed() && s.b.v4Writes()
	if exported {
		doc := s.b.v4.External.GitHubRunnerApp(record.Tier, record.Org)
		if err = s.b.putGitHub(ctx, doc, record.AppID, record.InstallationID, privateKey, ""); err != nil {
			return err
		}
	}
	var ref string
	if !exported || s.b.keepInternal() {
		if ref, err = s.b.newSecret(ctx, key, []byte(privateKey)); err != nil {
			return err
		}
	}
	raw := json.RawMessage(keys[runnerapp.RecordKey(record.Tier, record.Org)])
	return s.b.editItem(ctx, key, 0, func(*item) (*item, error) { return &item{Record: raw, Secret: ref}, nil })
}

// List returns every App's record, sorted by organisation, then tier.
func (s *GitHubRunnerApps) List(ctx context.Context) ([]runnerapp.Record, error) {
	records, err := s.b.listAll(ctx, ghRunnerPrefix)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the runner Apps: %w", err)
	}
	var out []runnerapp.Record
	for _, rec := range records {
		it, err := decodeItem(rec.Value)
		if err != nil || len(it.Record) == 0 {
			continue
		}
		if record, err := runnerapp.DecodeRecord(it.Record); err == nil && runnerKey(record.Tier, record.Org) == rec.Key {
			out = append(out, record)
		}
	}
	slices.SortFunc(out, func(a, b runnerapp.Record) int {
		if c := strings.Compare(a.Org, b.Org); c != 0 {
			return c
		}
		return strings.Compare(a.Tier, b.Tier)
	})
	return out, nil
}

// PrivateKey reads one App's key.
func (s *GitHubRunnerApps) PrivateKey(ctx context.Context, tier, org string) (string, bool, error) {
	if s.b.v4Reads() {
		if doc, ok, err := s.b.getGitHub(ctx, s.b.v4.External.GitHubRunnerApp(tier, org)); err != nil || ok {
			return doc.PrivateKey, ok, err
		}
	}
	key := runnerKey(tier, org)
	it, err := s.b.getItem(ctx, key)
	if err != nil || it == nil || it.Secret == "" {
		return "", false, err
	}
	plain, err := s.b.getSecret(ctx, key, it.Secret)
	if err != nil {
		return "", false, err
	}
	return string(plain), len(plain) > 0, nil
}

// Delete forgets one App.
func (s *GitHubRunnerApps) Delete(ctx context.Context, tier, org string) error {
	if s.b.v4Writes() {
		if err := s.b.deleteExternal(ctx, s.b.v4.External.Store(), "github/runner-"+tier+"-"+org); err != nil {
			return err
		}
	}
	return s.b.deleteItem(ctx, runnerKey(tier, org))
}

// GitHubCatalogueApps keeps the catalogue GitHub Apps, `app.gh.cat.<id>`.
type GitHubCatalogueApps struct{ b *Base }

// NewGitHubCatalogueApps returns the store.
func NewGitHubCatalogueApps(b *Base) *GitHubCatalogueApps { return &GitHubCatalogueApps{b: b} }

func ghCatalogueKey(id string) string { return ghCataloguePfx + seg(id) }

// Put writes one App, replacing what was kept for its id.
func (s *GitHubCatalogueApps) Put(ctx context.Context, record catalogueapp.Record, privateKey string) error {
	keys, err := catalogueapp.Encode(record, privateKey)
	if err != nil {
		return err
	}
	key := ghCatalogueKey(record.ID)
	// The webhook secret belongs to the App, not to this write: whatever was
	// kept stays kept, wherever it was.
	hook, _, err := s.webhookSecret(ctx, record.ID)
	if err != nil {
		return err
	}
	// An installed App with `export: true` is external on layout v4; a pending
	// one, or one not exported, stays an internal credential.
	exported := s.exported(record)
	if exported {
		doc := s.b.v4.External.GitHubApp(record.ID)
		if err = s.b.putGitHub(ctx, doc, record.AppID, record.InstallationID, privateKey, hook); err != nil {
			return err
		}
	}
	var ref string
	if !exported || s.b.keepInternal() {
		if ref, err = s.b.newSecret(ctx, key, packCatalogueSecret(privateKey, hook)); err != nil {
			return err
		}
	}
	raw := json.RawMessage(keys[catalogueapp.RecordKey(record.ID)])
	return s.b.editItem(ctx, key, 0, func(*item) (*item, error) { return &item{Record: raw, Secret: ref}, nil })
}

// exported is whether an App's document is external on layout v4.
func (s *GitHubCatalogueApps) exported(record catalogueapp.Record) bool {
	return record.Installed() && s.b.v4Writes() && s.b.exportApp != nil && s.b.exportApp(record.ID)
}

// PutWebhookSecret sets the secret GitHub signs one App's webhook with,
// replacing the one before, wherever the App's credential is kept. The App
// must have been Put: a secret with no App is nobody's.
func (s *GitHubCatalogueApps) PutWebhookSecret(ctx context.Context, id, secret string) error {
	if secret == "" {
		return errors.New("portstore: a webhook secret is not empty")
	}
	record, privateKey, ok, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("portstore: no App %s to keep a webhook secret for", id)
	}
	key := ghCatalogueKey(id)
	exported := s.exported(record)
	if exported {
		doc := s.b.v4.External.GitHubApp(id)
		if err = s.b.putGitHub(ctx, doc, record.AppID, record.InstallationID, privateKey, secret); err != nil {
			return err
		}
	}
	if exported && !s.b.keepInternal() {
		return nil
	}
	ref, err := s.b.newSecret(ctx, key, packCatalogueSecret(privateKey, secret))
	if err != nil {
		return err
	}
	return s.b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		if cur == nil || len(cur.Record) == 0 {
			return nil, fmt.Errorf("portstore: App %s was forgotten while its webhook secret was being kept", id)
		}
		return &item{Record: cur.Record, Secret: ref}, nil
	})
}

// WebhookSecret reads one App's webhook secret.
func (s *GitHubCatalogueApps) WebhookSecret(ctx context.Context, id string) (string, bool, error) {
	return s.webhookSecret(ctx, id)
}

func (s *GitHubCatalogueApps) webhookSecret(ctx context.Context, id string) (string, bool, error) {
	if s.b.v4Reads() && secretstore.CheckAppName(id) == nil {
		doc, ok, err := s.b.getGitHub(ctx, s.b.v4.External.GitHubApp(id))
		if err != nil && !errors.Is(err, secretstore.ErrReservedName) {
			return "", false, err
		}
		if ok && doc.WebhookSecret != "" {
			return doc.WebhookSecret, true, nil
		}
	}
	key := ghCatalogueKey(id)
	it, err := s.b.getItem(ctx, key)
	if err != nil || it == nil || it.Secret == "" {
		return "", false, err
	}
	plain, err := s.b.getSecret(ctx, key, it.Secret)
	if err != nil {
		return "", false, err
	}
	_, hook := unpackCatalogueSecret(plain)
	return hook, hook != "", nil
}

// packCatalogueSecret is what an App's internal credential holds: the key as
// it always was, or, with a webhook secret, both as JSON. A reader tells them
// apart by the first byte: a PEM key never begins with a brace.
func packCatalogueSecret(privateKey, webhookSecret string) []byte {
	if webhookSecret == "" {
		return []byte(privateKey)
	}
	raw, _ := json.Marshal(struct {
		PrivateKey    string `json:"private_key"`
		WebhookSecret string `json:"webhook_secret"`
	}{privateKey, webhookSecret})
	return raw
}

func unpackCatalogueSecret(plain []byte) (privateKey, webhookSecret string) {
	if len(plain) > 0 && plain[0] == '{' {
		var packed struct {
			PrivateKey    string `json:"private_key"`
			WebhookSecret string `json:"webhook_secret"`
		}
		if json.Unmarshal(plain, &packed) == nil {
			return packed.PrivateKey, packed.WebhookSecret
		}
	}
	return string(plain), ""
}

// List returns every App's record, sorted by id.
func (s *GitHubCatalogueApps) List(ctx context.Context) ([]catalogueapp.Record, error) {
	records, err := s.b.listAll(ctx, ghCataloguePfx)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the catalogue Apps: %w", err)
	}
	var out []catalogueapp.Record
	for _, rec := range records {
		it, err := decodeItem(rec.Value)
		if err != nil || len(it.Record) == 0 {
			continue
		}
		if record, err := catalogueapp.DecodeRecord(it.Record); err == nil && ghCatalogueKey(record.ID) == rec.Key {
			out = append(out, record)
		}
	}
	slices.SortFunc(out, func(a, b catalogueapp.Record) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get reads one App's record and its key, installed or pending.
func (s *GitHubCatalogueApps) Get(ctx context.Context, id string) (catalogueapp.Record, string, bool, error) {
	key := ghCatalogueKey(id)
	it, err := s.b.getItem(ctx, key)
	if err != nil || it == nil || len(it.Record) == 0 {
		return catalogueapp.Record{}, "", false, err
	}
	record, err := catalogueapp.DecodeRecord(it.Record)
	if err != nil {
		return catalogueapp.Record{}, "", false, err
	}
	var privateKey string
	if s.b.v4Reads() && record.Installed() {
		doc, ok, err := s.b.getGitHub(ctx, s.b.v4.External.GitHubApp(record.ID))
		if err != nil && !errors.Is(err, secretstore.ErrReservedName) {
			return catalogueapp.Record{}, "", false, err
		}
		if ok {
			return record, doc.PrivateKey, true, nil
		}
	}
	if it.Secret != "" {
		plain, err := s.b.getSecret(ctx, key, it.Secret)
		if err != nil {
			return catalogueapp.Record{}, "", false, err
		}
		privateKey, _ = unpackCatalogueSecret(plain)
	}
	return record, privateKey, true, nil
}

// Delete forgets one App.
func (s *GitHubCatalogueApps) Delete(ctx context.Context, id string) error {
	if s.b.v4Writes() && secretstore.CheckAppName(id) == nil {
		if err := s.b.deleteExternal(ctx, s.b.v4.External.Store(), "github/"+id); err != nil {
			return err
		}
	}
	return s.b.deleteItem(ctx, ghCatalogueKey(id))
}

// SlackCatalogueApps keeps the catalogue Slack Apps, `app.slack.cat.<id>`.
type SlackCatalogueApps struct{ b *Base }

// NewSlackCatalogueApps returns the store.
func NewSlackCatalogueApps(b *Base) *SlackCatalogueApps { return &SlackCatalogueApps{b: b} }

func slackCatalogueKey(id string) string { return slackCataloguePf + seg(id) }

// Put writes one App, replacing what was kept for its id: the client secret
// and the bot token are kept together as one secret.
func (s *SlackCatalogueApps) Put(ctx context.Context, record slackcatalogueapp.Record, credentials slackcatalogueapp.Credentials) error {
	keys, err := slackcatalogueapp.Encode(record, credentials)
	if err != nil {
		return err
	}
	key := slackCatalogueKey(record.ID)
	// The bot token is external (slack/v1) on layout v4; the client secret
	// stays an internal credential.
	internal := credentials
	if credentials.BotToken != "" && s.b.v4Writes() {
		if err = s.b.putSlack(ctx, record.ID, credentials.BotToken); err != nil {
			return err
		}
		if !s.b.keepInternal() {
			internal.BotToken = ""
		}
	}
	plain, _ := json.Marshal(internal)
	ref, err := s.b.newSecret(ctx, key, plain)
	if err != nil {
		return err
	}
	raw := json.RawMessage(keys[slackcatalogueapp.RecordKey(record.ID)])
	return s.b.editItem(ctx, key, 0, func(*item) (*item, error) { return &item{Record: raw, Secret: ref}, nil })
}

// List returns every App's record, sorted by id.
func (s *SlackCatalogueApps) List(ctx context.Context) ([]slackcatalogueapp.Record, error) {
	records, err := s.b.listAll(ctx, slackCataloguePf)
	if err != nil {
		return nil, fmt.Errorf("portstore: list the Slack catalogue Apps: %w", err)
	}
	var out []slackcatalogueapp.Record
	for _, rec := range records {
		it, err := decodeItem(rec.Value)
		if err != nil || len(it.Record) == 0 {
			continue
		}
		if record, err := slackcatalogueapp.DecodeRecord(it.Record); err == nil && slackCatalogueKey(record.ID) == rec.Key {
			out = append(out, record)
		}
	}
	slices.SortFunc(out, func(a, b slackcatalogueapp.Record) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get reads one App's record and credentials.
func (s *SlackCatalogueApps) Get(ctx context.Context, id string) (slackcatalogueapp.Record, slackcatalogueapp.Credentials, bool, error) {
	key := slackCatalogueKey(id)
	it, err := s.b.getItem(ctx, key)
	if err != nil || it == nil || len(it.Record) == 0 {
		return slackcatalogueapp.Record{}, slackcatalogueapp.Credentials{}, false, err
	}
	record, err := slackcatalogueapp.DecodeRecord(it.Record)
	if err != nil {
		return slackcatalogueapp.Record{}, slackcatalogueapp.Credentials{}, false, err
	}
	var credentials slackcatalogueapp.Credentials
	if it.Secret != "" {
		plain, err := s.b.getSecret(ctx, key, it.Secret)
		if err != nil {
			return slackcatalogueapp.Record{}, slackcatalogueapp.Credentials{}, false, err
		}
		if err = json.Unmarshal(plain, &credentials); err != nil {
			return slackcatalogueapp.Record{}, slackcatalogueapp.Credentials{}, false, fmt.Errorf("portstore: the credentials of %s do not decode", id)
		}
	}
	if s.b.v4Reads() && record.Installed() {
		token, ok, err := s.b.getSlack(ctx, id)
		if err != nil {
			return slackcatalogueapp.Record{}, slackcatalogueapp.Credentials{}, false, err
		}
		if ok {
			credentials.BotToken = token
		}
	}
	return record, credentials, true, nil
}

// Delete forgets one App.
func (s *SlackCatalogueApps) Delete(ctx context.Context, id string) error {
	if s.b.v4Writes() {
		if err := s.b.deleteExternal(ctx, s.b.v4.External.Store(), "slack/"+id); err != nil {
			return err
		}
	}
	return s.b.deleteItem(ctx, slackCatalogueKey(id))
}
