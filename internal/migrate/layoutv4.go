package migrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	slackcatalogueapp "github.com/truvity/sluis/internal/slackapp/catalogueapp"
	"github.com/truvity/sluis/storage/state"
)

// The move of an installation's secrets from layout v3 (`private/`) to layout
// v4 (`internal/` and `external/`, docs/decisions/0041). It is the step that
// sits between `secrets.layout: transition` and `secrets.layout: v4`:
//
//  1. Plan: read every v3 item, say for each where it goes in v4 and whether
//     v4 lacks it, holds it equal, or holds something else. Something else
//     refuses the run before a single write, naming both revisions.
//  2. Write what v4 lacks, create-only: nothing is ever overwritten.
//  3. Read back: every address is read again and compared with the source.
//
// A re-run plans again, so it writes exactly what is missing (the move is
// idempotent and resumable); an item v4 holds equal is not rewritten. The
// report names sources and addresses and versions, never a value. v3 is left
// as it is: [DeleteV3] is a separate, later step.

// Errors a layout run ends with. The report says the rest.
var (
	// ErrLayoutConflict is a v4 address that already holds something other
	// than what the v3 item says. Nothing is written.
	ErrLayoutConflict = errors.New("migrate: a v4 address already holds a different value")
	// ErrLayoutMismatch is a write that did not read back equal.
	ErrLayoutMismatch = errors.New("migrate: a v4 address does not read back as the v3 item")
	// ErrNotV4 is a request to delete v3 while a consumer could still read it.
	ErrNotV4 = errors.New("migrate: v3 is deleted only once the installation runs on layout v4: " +
		"set secrets.layout to v4, roll every consumer, and run this again")
	// ErrV4Incomplete is a v3 item with nothing at its v4 address.
	ErrV4Incomplete = errors.New("migrate: v3 holds items that v4 does not: run the move again before deleting v3")
)

// ConfigStore is `<root>/internal/config/`, the parameters an operator seeds.
// The secrets source reads them as plain text, so they are not documents and
// do not go through a [state.Store].
type ConfigStore interface {
	// Get returns the text of name; ok is false when it is absent.
	Get(ctx context.Context, name string) (value string, ok bool, err error)
	// Create writes name only if it is absent.
	Create(ctx context.Context, name, value string) error
}

// SecretsLayoutOptions is one run of the move to layout v4.
type SecretsLayoutOptions struct {
	// V3 is the layout-v3 Secrets port of the installation (its `private/`
	// tree, not the layout-aware one), State the port its App records are in.
	V3    port.Secrets
	State port.State
	// Dest are the v4 stores, Config the plain-text parameters.
	Dest   *secretstore.Stores
	Config ConfigStore
	// ExportedGitHubApp says which catalogue GitHub Apps have `export: true`
	// (a runner App is always exported). Nil exports none.
	ExportedGitHubApp func(id string) bool
	// DryRun plans and writes nothing.
	DryRun bool
	// Layout is the installation's `secrets.layout` (DeleteV3 only).
	Layout string
	// Now is the clock a rotated client's previous secret is judged by.
	Now func() time.Time
}

// LayoutItem is one v3 item and what the run found or did.
type LayoutItem struct {
	// Source is the v3 Secrets path, or `state:<record>` for an App whose ids
	// are in State.
	Source string `json:"source"`
	// Address is where it goes in v4: `internal/...`, `external/<kind>/<id>`
	// or `internal/config/...`.
	Address string `json:"address"`
	// Action is new, resume (a rotation half written), unchanged, conflict,
	// or, for the deletion, delete or missing.
	Action string `json:"action"`
	// V3Version and V4Revision are the versions a conflict is between.
	V3Version  string `json:"v3Version,omitempty"`
	V4Revision string `json:"v4Revision,omitempty"`
	Note       string `json:"note,omitempty"`
}

// SecretsLayoutReport is what a run says it did: sources, addresses and
// versions, never a value.
type SecretsLayoutReport struct {
	To        string       `json:"to,omitempty"`
	DryRun    bool         `json:"dryRun"`
	Items     []LayoutItem `json:"items"`
	New       int          `json:"new"`
	Unchanged int          `json:"unchanged"`
	Conflicts int          `json:"conflicts,omitempty"`
	Written   int          `json:"written"`
	Verified  int          `json:"verified"`
	Deleted   int          `json:"deleted,omitempty"`
	// Skipped are v3 paths left alone on purpose (the exports copies).
	Skipped []string `json:"skipped,omitempty"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
}

// JSON is the report as the command prints it.
func (r *SecretsLayoutReport) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return append(b, '\n')
}

type destState int

const (
	stAbsent destState = iota
	stEqual
	stResume
	stDiffers
)

// unit is one thing to put in v4, with how to look at its address.
type unit struct {
	source, addr, srcVer string
	// want is a digest of what is to be written, to tell two v3 sources of one
	// address apart without keeping the value.
	want string
	// v3 are the v3 paths the unit replaces; DeleteV3 removes them.
	v3 []string
	// probe reports the address now and its revision.
	probe func(ctx context.Context) (destState, string, error)
	// write creates the address (st is stAbsent) or completes a half-written
	// rotation (stResume, on rev).
	write func(ctx context.Context, st destState, rev string) error
}

type planner struct {
	o     SecretsLayoutOptions
	units []*unit
	byAdr map[string]*unit
	skip  []string
	// conflicts found while planning, before the destination is looked at.
	clash []LayoutItem
	// externalised maps a v3 credential prefix to the unit that replaces it.
	externalised map[string]*unit
	// slackTokens are the Slack Apps whose bot token is external.
	slackTokens map[string]bool
	// residue are credentials of an installed App whose key is already gone
	// from v3 or not readable: they are not copied, and are deleted with v3.
	residue []string
}

func digest(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (p *planner) add(u *unit) {
	if prev, ok := p.byAdr[u.addr]; ok {
		if prev.want == u.want {
			prev.v3 = append(prev.v3, u.v3...)
			return
		}
		p.clash = append(p.clash, LayoutItem{
			Source: u.source, Address: u.addr, Action: "conflict",
			Note: "two v3 items, " + prev.source + " and " + u.source + ", name this address with different values",
		})
		return
	}
	p.byAdr[u.addr] = u
	p.units = append(p.units, u)
}

func (p *planner) now() time.Time {
	if p.o.Now != nil {
		return p.o.Now()
	}
	return time.Now()
}

// build lists v3 and the Apps' records and makes a unit for each item.
func (p *planner) build(ctx context.Context) error {
	if p.o.V3 == nil || p.o.Dest == nil {
		return errors.New("migrate: the v3 Secrets port and the v4 stores are required")
	}
	paths, err := p.o.V3.List(ctx, "")
	if err != nil {
		return fmt.Errorf("migrate: listing v3: %w", err)
	}
	sort.Strings(paths)
	if err = p.apps(ctx); err != nil {
		return err
	}
	for _, path := range paths {
		if strings.HasPrefix(path, port.ExportPrefix) {
			p.skip = append(p.skip, path)
			continue
		}
		if err = p.item(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func ref2(path string) (string, string, bool) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", "", false
	}
	return path[:i], path[i+1:], true
}

func (p *planner) item(ctx context.Context, path string) error {
	// A credential an App's document replaces is not copied as an internal one.
	if prefix, _, ok := ref2(path); ok {
		if u, ok := p.externalised[prefix]; ok {
			if u.addr == "" {
				p.residue = append(p.residue, path)
			} else {
				u.v3 = append(u.v3, path)
			}
			return nil
		}
	}
	sec, err := p.o.V3.Get(ctx, path)
	if errors.Is(err, port.ErrNotFound) {
		return nil // deleted since the listing
	}
	if err != nil {
		return fmt.Errorf("migrate: reading v3 %s: %w", path, err)
	}
	switch {
	case strings.HasPrefix(path, "config/clients/") && strings.HasSuffix(path, "/secret"):
		id := secretstore.Unsegment(strings.TrimSuffix(strings.TrimPrefix(path, "config/clients/"), "/secret"))
		if id == "" || strings.Contains(id, "/") {
			return p.config(path, sec)
		}
		p.add(p.oidc(path, sec.Version, id, string(sec.Value), ""))
		return nil
	case strings.HasPrefix(path, "config/"):
		return p.config(path, sec)
	case strings.HasPrefix(path, port.CredentialsPrefix+clientcreds.Kind+"/") && strings.HasSuffix(path, "/secret"):
		id := secretstore.Unsegment(strings.TrimSuffix(strings.TrimPrefix(path, port.CredentialsPrefix+clientcreds.Kind+"/"), "/secret"))
		rec, err := clientcreds.DecodeRecord(sec.Value)
		if err != nil || id == "" || strings.Contains(id, "/") {
			return fmt.Errorf("migrate: v3 %s is not a client record that decodes", path)
		}
		live := rec.Previous != "" && rec.PreviousValidUntil.After(p.now())
		prev := ""
		if live {
			prev = rec.Previous
		}
		p.add(p.oidc(path, sec.Version, id, rec.Current, prev))
		return nil
	case strings.HasPrefix(path, "credentials/slack-app/"):
		return p.slackCredential(path, sec)
	}
	p.add(p.raw(path, sec.Version, sec.Value))
	return nil
}

func (p *planner) raw(path, ver string, val []byte) *unit {
	v := state.NewValue(p.o.Dest.Internal.Store(), path, state.Raw())
	return &unit{
		source: path, addr: "internal/" + path, srcVer: ver, want: digest([]byte("raw"), val), v3: []string{path},
		probe: func(ctx context.Context) (destState, string, error) {
			got, rev, err := v.Get(ctx)
			switch {
			case errors.Is(err, state.ErrNotFound):
				return stAbsent, "", nil
			case err != nil:
				return 0, "", err
			case bytes.Equal(got, val):
				return stEqual, string(rev), nil
			}
			return stDiffers, string(rev), nil
		},
		write: func(ctx context.Context, _ destState, _ string) error {
			_, err := v.Put(ctx, val, "")
			return err
		},
	}
}

func (p *planner) config(path string, sec port.Secret) error {
	name := strings.TrimPrefix(path, "config/")
	if !utf8.Valid(sec.Value) {
		return fmt.Errorf("migrate: v3 %s is not text, and the secrets source reads %s as text", path, "internal/config/"+name)
	}
	val := string(sec.Value)
	cfg := p.o.Config
	if cfg == nil {
		return errors.New("migrate: the v3 tree holds config parameters and no store for internal/config was given")
	}
	p.add(&unit{
		source: path, addr: "internal/config/" + name, srcVer: sec.Version, want: digest([]byte("config"), sec.Value), v3: []string{path},
		probe: func(ctx context.Context) (destState, string, error) {
			got, ok, err := cfg.Get(ctx, name)
			switch {
			case err != nil:
				return 0, "", err
			case !ok:
				return stAbsent, "", nil
			case got == val:
				return stEqual, "", nil
			}
			return stDiffers, "", nil
		},
		write: func(ctx context.Context, _ destState, _ string) error { return cfg.Create(ctx, name, val) },
	})
	return nil
}

// oidc is the document of a confidential client's secret. prev is the secret a
// rotation replaced and is still accepted: written first, so that it is the
// document's previous revision.
func (p *planner) oidc(source, ver, id, current, prev string) *unit {
	v := p.o.Dest.External.OIDC(id)
	key := "oidc/" + secretstore.Segment(id)
	want := secretstore.OIDCv1{ClientID: id, ClientSecret: current}
	return &unit{
		source: source, addr: "external/" + key, srcVer: ver, want: digest([]byte("oidc"), []byte(id), []byte(current)), v3: []string{source},
		probe: func(ctx context.Context) (destState, string, error) {
			got, rev, err := v.Get(ctx)
			switch {
			case errors.Is(err, state.ErrNotFound):
				return stAbsent, "", nil
			case err != nil:
				return 0, "", err
			case got.ClientID == id && got.ClientSecret == current:
				return stEqual, string(rev), nil
			case prev != "" && got.ClientID == id && got.ClientSecret == prev:
				// A rotation written as far as its previous secret.
				if it, ierr := p.o.Dest.External.Store().Get(ctx, key); ierr == nil && it.Previous == "" {
					return stResume, string(rev), nil
				}
			}
			return stDiffers, string(rev), nil
		},
		write: func(ctx context.Context, st destState, rev string) error {
			if st == stResume {
				_, err := v.Put(ctx, want, state.Rev(rev))
				return err
			}
			next := state.Rev("")
			if prev != "" {
				r, err := v.Put(ctx, secretstore.OIDCv1{ClientID: id, ClientSecret: prev}, "")
				if err != nil {
					return err
				}
				next = r
			}
			_, err := v.Put(ctx, want, next)
			return err
		},
	}
}

func docUnit[T any](source, addr, ver, want string, v state.Value[T], doc T, same func(a, b T) bool) *unit {
	return &unit{
		source: source, addr: addr, srcVer: ver, want: want,
		probe: func(ctx context.Context) (destState, string, error) {
			got, rev, err := v.Get(ctx)
			switch {
			case errors.Is(err, state.ErrNotFound):
				return stAbsent, "", nil
			case err != nil:
				return 0, "", err
			case same(got, doc):
				return stEqual, string(rev), nil
			}
			return stDiffers, string(rev), nil
		},
		write: func(ctx context.Context, _ destState, _ string) error {
			_, err := v.Put(ctx, doc, "")
			return err
		},
	}
}

func sameGitHub(a, b secretstore.GitHubv1) bool {
	return a.AppID == b.AppID && a.InstallationID == b.InstallationID && a.PrivateKey == b.PrivateKey
}

// apps makes the units of the Apps whose credentials are external in v4: their
// ids are in State, the key in v3.
func (p *planner) apps(ctx context.Context) error {
	p.externalised = map[string]*unit{}
	p.slackTokens = map[string]bool{}
	if p.o.State == nil {
		return nil
	}
	base := portstore.New(port.Set{State: p.o.State, Secrets: p.o.V3})
	runners := portstore.NewGitHubRunnerApps(base)
	records, err := runners.List(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		r := &records[i]
		if !r.Installed() {
			continue
		}
		key, ok, err := runners.PrivateKey(ctx, r.Tier, r.Org)
		if errors.Is(err, port.ErrNotFound) { // the key is already gone from v3
			key, ok, err = "", false, nil
		}
		if err != nil {
			return fmt.Errorf("migrate: reading the key of runner App %s/%s: %w", r.Tier, r.Org, err)
		}
		p.externalised[port.CredentialsPrefix+"github-runner-app/"+secretstore.Segment(r.Tier)+"/"+secretstore.Segment(r.Org)] = &unit{}
		if !ok {
			continue
		}
		name := "runner-" + r.Tier + "-" + r.Org
		doc := secretstore.GitHubv1{AppID: fmt.Sprint(r.AppID), InstallationID: fmt.Sprint(r.InstallationID), PrivateKey: key}
		u := docUnit("state:app.gh.runner."+r.Tier+"."+r.Org, "external/github/"+secretstore.Segment(name), "",
			digest([]byte("github"), []byte(name), []byte(doc.AppID), []byte(doc.InstallationID), []byte(key)),
			p.o.Dest.External.GitHubRunnerApp(r.Tier, r.Org), doc, sameGitHub)
		p.add(u)
		p.externalised[port.CredentialsPrefix+"github-runner-app/"+secretstore.Segment(r.Tier)+"/"+secretstore.Segment(r.Org)] = u
	}
	cat := portstore.NewGitHubCatalogueApps(base)
	crecs, err := cat.List(ctx)
	if err != nil {
		return err
	}
	for i := range crecs {
		r := &crecs[i]
		if !r.Installed() || p.o.ExportedGitHubApp == nil || !p.o.ExportedGitHubApp(r.ID) || secretstore.CheckAppName(r.ID) != nil {
			continue
		}
		_, key, ok, err := cat.Get(ctx, r.ID)
		if errors.Is(err, port.ErrNotFound) {
			key, ok, err = "", true, nil
		}
		if err != nil {
			return fmt.Errorf("migrate: reading the key of App %s: %w", r.ID, err)
		}
		if !ok || key == "" {
			p.externalised[port.CredentialsPrefix+"github-app/"+secretstore.Segment(r.ID)] = &unit{}
			continue
		}
		doc := secretstore.GitHubv1{AppID: fmt.Sprint(r.AppID), InstallationID: fmt.Sprint(r.InstallationID), PrivateKey: key}
		u := docUnit("state:app.gh.cat."+r.ID, "external/github/"+secretstore.Segment(r.ID), "",
			digest([]byte("github"), []byte(r.ID), []byte(doc.AppID), []byte(doc.InstallationID), []byte(key)),
			p.o.Dest.External.GitHubApp(r.ID), doc, sameGitHub)
		p.add(u)
		p.externalised[port.CredentialsPrefix+"github-app/"+secretstore.Segment(r.ID)] = u
	}
	sl := portstore.NewSlackCatalogueApps(base)
	srecs, err := sl.List(ctx)
	if err != nil {
		return err
	}
	for i := range srecs {
		r := &srecs[i]
		if !r.Installed() {
			continue
		}
		_, creds, ok, err := sl.Get(ctx, r.ID)
		if errors.Is(err, port.ErrNotFound) {
			ok, err = false, nil
		}
		if err != nil {
			return fmt.Errorf("migrate: reading the credentials of Slack App %s: %w", r.ID, err)
		}
		if !ok || creds.BotToken == "" {
			continue
		}
		doc := secretstore.Slackv1{BotToken: creds.BotToken}
		p.add(docUnit("state:app.slack.cat."+r.ID, "external/slack/"+secretstore.Segment(r.ID), "",
			digest([]byte("slack"), []byte(r.ID), []byte(doc.BotToken)),
			p.o.Dest.External.SlackApp(r.ID), doc, func(a, b secretstore.Slackv1) bool { return a.BotToken == b.BotToken }))
		p.slackTokens[secretstore.Segment(r.ID)] = true
	}
	return nil
}

// slackCredential copies a Slack App's credential without its bot token when
// the token is the external document's; the client secret stays internal.
func (p *planner) slackCredential(path string, sec port.Secret) error {
	val := sec.Value
	prefix, _, _ := ref2(path)
	if p.slackTokens[strings.TrimPrefix(prefix, "credentials/slack-app/")] {
		var creds slackcatalogueapp.Credentials
		if err := json.Unmarshal(sec.Value, &creds); err == nil && creds.BotToken != "" {
			creds.BotToken = ""
			if val, err = json.Marshal(creds); err != nil {
				return err
			}
		}
	}
	p.add(p.raw(path, sec.Version, val))
	return nil
}

func (p *planner) report(ctx context.Context) (*SecretsLayoutReport, map[*unit]destState, map[*unit]string, error) {
	rep := &SecretsLayoutReport{DryRun: p.o.DryRun, Skipped: p.skip}
	states := map[*unit]destState{}
	revs := map[*unit]string{}
	rep.Items = append(rep.Items, p.clash...)
	rep.Conflicts += len(p.clash)
	for _, u := range p.units {
		st, rev, err := u.probe(ctx)
		if err != nil {
			return rep, nil, nil, fmt.Errorf("migrate: reading %s: %w", u.addr, err)
		}
		states[u], revs[u] = st, rev
		it := LayoutItem{Source: u.source, Address: u.addr}
		switch st {
		case stAbsent:
			it.Action = "new"
			rep.New++
		case stResume:
			it.Action, it.Note = "resume", "a rotation written as far as its previous secret"
			rep.New++
		case stEqual:
			it.Action = "unchanged"
			rep.Unchanged++
		default:
			it.Action, it.V3Version, it.V4Revision = "conflict", u.srcVer, rev
			it.Note = "v4 holds a different value; it is never overwritten"
			rep.Conflicts++
		}
		rep.Items = append(rep.Items, it)
	}
	return rep, states, revs, nil
}

func conflictError(rep *SecretsLayoutReport) error {
	var b strings.Builder
	for _, it := range rep.Items {
		if it.Action != "conflict" {
			continue
		}
		fmt.Fprintf(&b, "\n  %s (v3 %s", it.Address, it.Source)
		if it.V3Version != "" {
			fmt.Fprintf(&b, " version %s", it.V3Version)
		}
		if it.V4Revision != "" {
			fmt.Fprintf(&b, ", v4 revision %s", it.V4Revision)
		}
		b.WriteString(")")
		if it.Note != "" && it.V4Revision == "" {
			b.WriteString(": " + it.Note)
		}
	}
	return fmt.Errorf("%w (%d), nothing was written; compare them, remove the one that is wrong, and run again:%s",
		ErrLayoutConflict, rep.Conflicts, b.String())
}

// MoveSecretsLayout backfills every v3 item into its v4 address.
func MoveSecretsLayout(ctx context.Context, o SecretsLayoutOptions) (*SecretsLayoutReport, error) {
	p := &planner{o: o, byAdr: map[string]*unit{}}
	if err := p.build(ctx); err != nil {
		return nil, err
	}
	rep, states, revs, err := p.report(ctx)
	rep.To = "v4"
	if err != nil {
		rep.Error = err.Error()
		return rep, err
	}
	if rep.Conflicts > 0 {
		err = conflictError(rep)
		rep.Error = err.Error()
		return rep, err
	}
	if o.DryRun {
		rep.OK = true
		return rep, nil
	}
	for _, u := range p.units {
		st := states[u]
		if st != stAbsent && st != stResume {
			continue
		}
		if err = ctx.Err(); err != nil {
			rep.Error = err.Error()
			return rep, err
		}
		werr := u.write(ctx, st, revs[u])
		if errors.Is(werr, state.ErrConflict) || errors.Is(werr, port.ErrConflict) {
			// Somebody wrote the address since the plan: equal is fine.
			if now, _, perr := u.probe(ctx); perr == nil && now == stEqual {
				werr = nil
			}
		}
		if werr != nil {
			err = fmt.Errorf("migrate: writing %s: %w", u.addr, werr)
			rep.Error = err.Error()
			return rep, err
		}
		rep.Written++
	}
	for _, u := range p.units {
		st, _, perr := u.probe(ctx)
		if perr != nil || st != stEqual {
			err = fmt.Errorf("%w: %s", ErrLayoutMismatch, u.addr)
			rep.Error = err.Error()
			return rep, err
		}
		rep.Verified++
	}
	rep.OK = true
	return rep, nil
}

// DeleteV3 removes the v3 items once v4 holds them. It refuses unless the
// installation is on layout v4 and every v3 item has its v4 address.
func DeleteV3(ctx context.Context, o SecretsLayoutOptions) (*SecretsLayoutReport, error) {
	if o.Layout != config.SecretsLayoutV4 {
		return nil, fmt.Errorf("%w (secrets.layout is %q)", ErrNotV4, o.Layout)
	}
	p := &planner{o: o, byAdr: map[string]*unit{}}
	if err := p.build(ctx); err != nil {
		return nil, err
	}
	rep := &SecretsLayoutReport{DryRun: o.DryRun, Skipped: p.skip}
	var missing []string
	for _, u := range p.units {
		st, _, err := u.probe(ctx)
		if err != nil {
			return rep, fmt.Errorf("migrate: reading %s: %w", u.addr, err)
		}
		it := LayoutItem{Source: strings.Join(u.v3, ", "), Address: u.addr, Action: "delete"}
		if len(u.v3) == 0 {
			it.Source = u.source
		}
		if st == stAbsent {
			it.Action = "missing"
			missing = append(missing, u.addr)
		}
		rep.Items = append(rep.Items, it)
	}
	if len(missing) > 0 {
		err := fmt.Errorf("%w: %s", ErrV4Incomplete, strings.Join(missing, ", "))
		rep.Error = err.Error()
		return rep, err
	}
	if o.DryRun {
		rep.OK = true
		return rep, nil
	}
	all := slices.Clone(p.residue)
	for _, u := range p.units {
		all = append(all, u.v3...)
	}
	for _, path := range all {
		if err := o.V3.Delete(ctx, path); err != nil {
			err = fmt.Errorf("migrate: deleting v3 %s: %w", path, err)
			rep.Error = err.Error()
			return rep, err
		}
		rep.Deleted++
	}
	rep.OK = true
	return rep, nil
}
