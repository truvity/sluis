package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// This file is `sluis migrate v5 plan` (ADR 0072, point 7): it reads a layout v4
// installation and a layout v5 one and says, per module, what a copy would find.
// It writes nothing, to either side, and prints names and versions and never a
// value. The mapping it applies is v5map.go.

// PlanStatus is what a copy would find for one item.
type PlanStatus string

// The statuses.
const (
	// PlanNew is an item the destination lacks.
	PlanNew PlanStatus = "new"
	// PlanSame is an item the destination holds with the same value.
	PlanSame PlanStatus = "same"
	// PlanDifferent is an item the destination holds with another value.
	PlanDifferent PlanStatus = "different"
	// PlanRefused is an item that cannot be carried: layout v5 has no address
	// for it, the destination would refuse it, or the source cannot read it.
	PlanRefused PlanStatus = "refused"
)

// PlanItem is one thing the plan found, or, for the issuer's records whose
// keys are bearer values, a count of the records of one kind in one status.
type PlanItem struct {
	// Concern is `state`, `secret` or `blob`.
	Concern string `json:"concern"`
	// Kind and ID are the item's kind and id on layout v5.
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// From is the v4 logical key (State) or the v4 name below the root (secret).
	From string `json:"from"`
	// To is the v5 address: `<module>/<kind>/<id>` for State, the name below
	// the root for a secret. A name ending in `<ref>` has a ref chosen at each
	// write.
	To     string     `json:"to,omitempty"`
	Status PlanStatus `json:"status"`
	// Count is how many records the row stands for; absent is one.
	Count int `json:"count,omitempty"`
	// Secrets is how many credentials the record carries; their v4 and v5
	// families are FromSecret and ToSecret.
	Secrets    int    `json:"secrets,omitempty"`
	FromSecret string `json:"fromSecret,omitempty"`
	ToSecret   string `json:"toSecret,omitempty"`
	// External is the document a consumer reads, which keeps its name.
	External string `json:"external,omitempty"`
	// FromVersion and ToVersion are the versions of a secret on each side.
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion,omitempty"`
	// Reason says why an item is refused, or Note what the operator must know.
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

func (i PlanItem) count() int { return max(i.Count, 1) }

// PlanCounts are the items of a module by status.
type PlanCounts struct {
	New       int `json:"new"`
	Same      int `json:"same"`
	Different int `json:"different"`
	Refused   int `json:"refused"`
}

func (c *PlanCounts) add(s PlanStatus, n int) {
	switch s {
	case PlanNew:
		c.New += n
	case PlanSame:
		c.Same += n
	case PlanDifferent:
		c.Different += n
	case PlanRefused:
		c.Refused += n
	}
}

// ModulePlan is what the plan found for one module.
type ModulePlan struct {
	Module string `json:"module"`
	PlanCounts
	Items []PlanItem `json:"items"`
}

// PlanReport is the plan: no value, no timestamp, and the same text for the
// same installations.
type PlanReport struct {
	From    string       `json:"from"`
	To      string       `json:"to"`
	Modules []ModulePlan `json:"modules"`
	Totals  PlanCounts   `json:"totals"`
	Notes   []string     `json:"notes,omitempty"`
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
}

// JSON renders the plan, indented.
func (r *PlanReport) JSON() []byte {
	raw, _ := json.MarshalIndent(r, "", "  ")
	return append(raw, '\n')
}

// PlanOptions is what a plan is told beyond the two sides.
type PlanOptions struct {
	// Skip names domains left out (see the Domain constants).
	Skip []string
	// Sessions also plans the issuer's sessions, refresh tokens, single
	// sign-on records, codes in flight and Index sets. Without it only the key
	// ring and the state secret's fingerprint are planned.
	Sessions bool
	// Blobs says whether the controllers' reports are planned.
	Blobs BlobMode
	// ExportedGitHubApp says which catalogue GitHub Apps have `export: true`.
	ExportedGitHubApp func(id string) bool
	// AppRef is the App each GitHub organisation uses on layout v5
	// (`controllers.github.appRefs`). Layout v5 gives an organisation no key of
	// its own, so an organisation without one is refused.
	AppRef func(org string) string
	// ConfigNames are the secrets the documents declare below internal/config
	// beyond the fixed ones: `providers/google/<provider>/client-id`,
	// `directory/<id>/key`.
	ConfigNames []string
	// CloudflareAccounts are the accounts whose minter credential is planned.
	CloudflareAccounts []string
	// S3Ref and S3RefTo are the `credentialsRef` of the source's and the
	// destination's blob, when they have one.
	S3Ref, S3RefTo string
	// Log receives progress. Nil is silent.
	Log *slog.Logger
}

// planner is one plan in progress.
type planner struct {
	opt      PlanOptions
	from, to Side
	src, dst kit
	report   *PlanReport
	mods     map[string]*ModulePlan
	notes    []string
}

// Plan reads a layout v4 installation (from) and a layout v5 one (to) and
// reports what a copy would do, per module. It makes no write.
//
// The report is returned with ErrRefused or ErrUnreadable when anything is
// refused, so that a caller can still show it.
func Plan(ctx context.Context, from, to Side, opt PlanOptions) (*PlanReport, error) {
	switch {
	case from.Stores == nil || to.Stores == nil:
		return nil, errors.New("migrate: a plan needs both sides")
	case from.Stores.V5 != nil:
		return nil, errors.New("migrate: --from is already on layout v5; the source of `migrate v5` is a layout v4 installation")
	case to.Stores.V5 == nil:
		return nil, errors.New("migrate: --to is not on layout v5 (secrets.layout: v5 and ports.dynamodb.tables)")
	}
	if opt.Blobs == "" {
		opt.Blobs = BlobsAuto
	}
	skip := slices.Clone(opt.Skip)
	for i, name := range skip {
		if name == DomainDirectory {
			skip[i] = DomainGoogle
			name = DomainGoogle
		}
		if !contains(AllDomains, name) {
			return nil, fmt.Errorf("migrate: --skip %q is none of %s", name, strings.Join(AllDomains, ", "))
		}
	}
	opt.Skip = skip
	srcDomains, err := OpenDomainsExporting(ctx, from.Stores, false, opt.ExportedGitHubApp)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dstDomains, err := OpenDomainsExporting(ctx, to.Stores, false, opt.ExportedGitHubApp)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	p := &planner{
		opt: opt, from: from, to: to,
		src:    kit{d: srcDomains, ports: from.Stores.Ports},
		dst:    kit{d: dstDomains, ports: to.Stores.Ports},
		report: &PlanReport{From: from.Name, To: to.Name},
		mods:   map[string]*ModulePlan{},
	}
	if err = p.records(ctx); err != nil {
		return nil, err
	}
	if err = p.secrets(ctx); err != nil {
		return nil, err
	}
	if !contains(opt.Skip, DomainIssuer) {
		if err = p.issuer(ctx); err != nil {
			return nil, err
		}
	} else {
		p.note("the issuer is skipped (--skip)")
	}
	if !contains(opt.Skip, DomainBlobs) {
		if err = p.blobs(ctx); err != nil {
			return nil, err
		}
	}
	return p.finish()
}

func (p *planner) note(format string, args ...any) {
	p.notes = append(p.notes, fmt.Sprintf(format, args...))
}

func (p *planner) add(module string, it PlanItem) {
	m := p.mods[module]
	if m == nil {
		m = &ModulePlan{Module: module}
		p.mods[module] = m
	}
	m.Items = append(m.Items, it)
	m.add(it.Status, it.count())
	p.report.Totals.add(it.Status, it.count())
}

func (p *planner) finish() (*PlanReport, error) {
	order := map[string]int{}
	for i, m := range port.Modules() {
		order[string(m)] = i
	}
	order[sharedModule] = len(order)
	for _, m := range p.mods {
		sort.SliceStable(m.Items, func(i, j int) bool {
			a, b := m.Items[i], m.Items[j]
			if a.Concern != b.Concern {
				return concernOrder(a.Concern) < concernOrder(b.Concern)
			}
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.ID != b.ID {
				return a.ID < b.ID
			}
			return a.From < b.From
		})
		p.report.Modules = append(p.report.Modules, *m)
	}
	sort.Slice(p.report.Modules, func(i, j int) bool { return order[p.report.Modules[i].Module] < order[p.report.Modules[j].Module] })
	p.note("leases, notifications, caches, dedupe records, Slack Connect shares and user caches, and the hub's blob snapshots " +
		"are not copied: they start fresh")
	p.note("internal/config/valkey/password is delivered to the cluster only and is not carried")
	p.report.Notes = p.notes
	var err error
	if n := p.report.Totals.Refused; n > 0 {
		first := p.firstRefused()
		err = fmt.Errorf("%w: %d items, the first %s: %s", ErrRefused, n, first.From, first.Reason)
		if strings.HasPrefix(first.Reason, "unreadable") {
			err = fmt.Errorf("%w: %d items, the first %s: %s", ErrUnreadable, n, first.From, first.Reason)
		}
		p.report.Error = err.Error()
	}
	p.report.OK = err == nil
	return p.report, err
}

func concernOrder(c string) int {
	switch c {
	case "state":
		return 0
	case "secret":
		return 1
	}
	return 2
}

func (p *planner) firstRefused() PlanItem {
	for i := range p.report.Modules {
		for j := range p.report.Modules[i].Items {
			if it := &p.report.Modules[i].Items[j]; it.Status == PlanRefused {
				return *it
			}
		}
	}
	return PlanItem{}
}

// domainModule is the module a domain's records belong to.
func domainModule(domain string) string {
	if domain == DomainConsole {
		return "oidc"
	}
	return domain
}

// planCanon is the form two sides are compared in. An organisation's credential
// is not compared: on layout v5 the key is its App's, and the record's `app_ref`
// is what the destination adds.
func planCanon(domain, name string, e entry) []byte {
	if doc, ok := e.val.(orgDoc); ok && domain == "github" && name == "organisations" {
		doc.Record.AppRef = ""
		raw, _ := json.Marshal(doc.Record)
		return raw
	}
	return e.canon
}

type readKind struct {
	k        kind
	src, dst []entry
}

func (p *planner) records(ctx context.Context) error {
	var all []readKind
	appIDs := map[string]int64{}
	for _, k := range kinds() {
		if contains(p.opt.Skip, k.domain) {
			p.note("%s is skipped (--skip)", k.domain)
			continue
		}
		s := kindStep(k)
		src, err := s.read(ctx, p.src)
		if err != nil {
			return fmt.Errorf("read the source's %s/%s: %w", k.domain, k.name, err)
		}
		dst, err := s.read(ctx, p.dst)
		if err != nil {
			return fmt.Errorf("read the destination's %s/%s: %w", k.domain, k.name, err)
		}
		all = append(all, readKind{k, src, dst})
		collectApps(k, src, appIDs)
		collectApps(k, dst, appIDs)
	}
	runners := map[string]string{}
	for _, rk := range all {
		p.kind(rk, appIDs, runners)
	}
	return nil
}

// collectApps notes the GitHub Apps a side holds, by their layout v5 id, with the
// App's own (numeric) id.
func collectApps(k kind, entries []entry, ids map[string]int64) {
	if k.domain != "github" {
		return
	}
	for _, e := range entries {
		switch doc := e.val.(type) {
		case linkAppDoc:
			ids["link"] = doc.App.AppID
		case catalogueDoc:
			ids[doc.Record.ID] = doc.Record.AppID
		case runnerDoc:
			ids["runner-"+doc.Record.Tier+"-"+doc.Record.Org] = doc.Record.AppID
		}
	}
}

func (p *planner) kind(rk readKind, appIDs map[string]int64, runners map[string]string) {
	k := rk.k
	row, _ := rowOf(k.domain, k.name)
	have := map[string]*entry{}
	for i := range rk.dst {
		have[rk.dst[i].id] = &rk.dst[i]
	}
	s := kindStep(k)
	for _, e := range rk.src {
		it := item{id: e.id, val: e.val}
		out := PlanItem{Concern: "state", Kind: k.name, ID: e.id}
		module := domainModule(k.domain)

		key, kerr := "", error(nil)
		if row.key == nil {
			kerr = errors.New("the mapping has no row for this collection")
		} else {
			key, kerr = row.key(it)
		}
		out.From = key
		if out.From == "" {
			out.From = k.domain + "/" + k.name + "/" + e.id
		}
		var t Target
		if kerr == nil {
			t, kerr = StateTarget(key)
		}
		if kerr == nil {
			module, out.Kind, out.ID, out.To = t.Module, t.Kind, t.ID, t.String()
		}
		switch {
		case e.unreadable != "":
			out.Status, out.Reason = PlanRefused, "unreadable: "+e.unreadable
		case kerr != nil:
			out.Status, out.Reason = PlanRefused, kerr.Error()
		default:
			p.checkItem(&out, s, e, row, it, have[e.id], appIDs, runners)
		}
		p.add(module, out)
	}
}

// checkItem decides the status of a readable record and fills in what it
// carries.
func (p *planner) checkItem(out *PlanItem, s step, e entry, row stateRow, it item, old *entry, appIDs map[string]int64, runners map[string]string) {
	if why := refusal(s, e); why != "" {
		out.Status, out.Reason = PlanRefused, why
		return
	}
	if why := p.appRefusal(s, e, appIDs, runners); why != "" {
		out.Status, out.Reason = PlanRefused, why
		return
	}
	out.Secrets = len(e.secrets)
	if l, ok := e.val.(link.Link); ok && l.AccessToken == "" && l.RefreshToken == "" {
		out.Secrets = 0 // a link that holds no token keeps no credential
	}
	p.exported(out, e)
	if out.Secrets > 0 && row.credential != nil && out.External == "" {
		out.FromSecret = row.credential(it)
		to, err := MapSecret(out.FromSecret)
		switch {
		case errors.Is(err, ErrNotMigrated):
			out.Note = strings.TrimPrefix(err.Error(), ErrNotMigrated.Error()+": ")
		case err != nil:
			out.Status, out.Reason = PlanRefused, err.Error()
			return
		default:
			out.ToSecret = to.V5
		}
	}
	switch {
	case old == nil:
		out.Status = PlanNew
	case old.unreadable == "" && bytes.Equal(planCanon(s.domain, s.name, *old), planCanon(s.domain, s.name, e)):
		out.Status = PlanSame
	default:
		out.Status = PlanDifferent
	}
}

// exported names the document a consumer reads for an App that is exported. An
// exported App's key is kept once, there, and not as an internal credential.
func (p *planner) exported(out *PlanItem, e entry) {
	switch doc := e.val.(type) {
	case runnerDoc:
		out.External = "external/github/runner-" + doc.Record.Tier + "-" + doc.Record.Org
	case catalogueDoc:
		if p.opt.ExportedGitHubApp != nil && p.opt.ExportedGitHubApp(doc.Record.ID) {
			out.External = "external/github/" + doc.Record.ID
		}
	}
}

// appRefusal is the refusals that belong to the GitHub Apps and the
// organisations that use them.
func (p *planner) appRefusal(s step, e entry, appIDs map[string]int64, runners map[string]string) string {
	if s.domain != "github" {
		return ""
	}
	switch doc := e.val.(type) {
	case runnerDoc:
		id := "runner-" + doc.Record.Tier + "-" + doc.Record.Org
		if prev, dup := runners[id]; dup {
			return fmt.Sprintf("the runner Apps %s and %s/%s both join to the id %s", prev, doc.Record.Tier, doc.Record.Org, id)
		}
		runners[id] = doc.Record.Tier + "/" + doc.Record.Org
	case orgDoc:
		return p.orgRefusal(doc, appIDs)
	}
	return ""
}

func (p *planner) orgRefusal(doc orgDoc, appIDs map[string]int64) string {
	ref := doc.Record.AppRef
	if ref == "" && p.opt.AppRef != nil {
		ref = p.opt.AppRef(doc.Record.Org)
	}
	if ref == "" {
		return "layout v5 gives an organisation no key of its own: name the App it uses (controllers.github.appRefs)"
	}
	appID, ok := appIDs[ref]
	if !ok {
		return fmt.Sprintf("the App %s that the organisation uses is not among the source's Apps", ref)
	}
	if doc.Record.AppID != 0 && appID != 0 && doc.Record.AppID != appID {
		return fmt.Sprintf("the organisation is installed by GitHub App %d and %s is GitHub App %d: its key would not sign for the installation",
			doc.Record.AppID, ref, appID)
	}
	return ""
}

// secretRead is one secret as a side holds it.
type secretRead struct {
	value   []byte
	version string
	found   bool
}

func (p *planner) secrets(ctx context.Context) error {
	v4 := p.from.Stores.V4
	if v4 == nil {
		p.note("the source keeps no layout v4 secrets of its own, so no standalone secret is planned")
		return nil
	}
	type named struct {
		name     string
		explicit bool
	}
	var names []named
	add := func(name string, explicit bool) {
		if !slices.ContainsFunc(names, func(n named) bool { return n.name == name }) {
			names = append(names, named{name, explicit})
		}
	}
	add("internal/config/issuer/state-secret", false)
	add("internal/config/recovery/password", false)
	for _, n := range p.opt.ConfigNames {
		add("internal/config/"+n, true)
	}
	clients, err := v4.External.Store().Child("oidc").List(ctx)
	if err != nil {
		return fmt.Errorf("list the generated clients: %w", err)
	}
	for _, id := range clients {
		add("internal/config/clients/"+id+"/secret", false)
		add("external/oidc/"+id, false)
	}
	for _, a := range p.opt.CloudflareAccounts {
		add("internal/cloudflare/"+a+"/minter", true)
	}
	minted, err := v4.Internal.Store().Child("cloudflare-minted").List(ctx)
	if err != nil {
		return fmt.Errorf("list the minted-token records: %w", err)
	}
	for _, preset := range minted {
		add("internal/cloudflare-minted/"+preset, false)
	}
	presets, err := v4.External.Store().Child("cloudflare").List(ctx)
	if err != nil {
		return fmt.Errorf("list the Cloudflare presets: %w", err)
	}
	for _, preset := range presets {
		add("external/cloudflare/"+preset, false)
	}
	for _, n := range names {
		t, err := MapSecret(n.name)
		if err != nil {
			p.add("oidc", PlanItem{Concern: "secret", Kind: "unmapped", From: n.name, Status: PlanRefused, Reason: err.Error()})
			continue
		}
		p.secret(ctx, n.name, t, n.explicit)
	}
	if p.opt.S3Ref != "" {
		return p.s3(ctx)
	}
	return nil
}

func (p *planner) s3(ctx context.Context) error {
	to, err := MapS3Ref(p.opt.S3Ref)
	if err != nil {
		p.add(S3Module, PlanItem{Concern: "secret", Kind: "s3-credentials", From: p.opt.S3Ref, Status: PlanRefused, Reason: err.Error()})
		return nil
	}
	if p.opt.S3RefTo != "" && p.opt.S3RefTo != to {
		p.note("the destination's ports.blob.s3.credentialsRef is %s; the plan carries %s to %s", p.opt.S3RefTo, p.opt.S3Ref, to)
	}
	rest, _ := strings.CutPrefix(to, "internal/"+S3Module+"/")
	t := SecretTarget{Module: S3Module, Kind: "s3-credentials", ID: rest, V5: to}
	p.secret(ctx, p.opt.S3Ref, t, true)
	return nil
}

// secret plans one standalone secret.
func (p *planner) secret(ctx context.Context, from string, t SecretTarget, explicit bool) {
	out := PlanItem{Concern: "secret", Kind: t.Kind, ID: t.ID, From: from, To: t.V5}
	src, err := p.readV4(ctx, from, t)
	if err != nil {
		out.Status, out.Reason = PlanRefused, "unreadable: "+err.Error()
		p.add(t.Module, out)
		return
	}
	if !src.found {
		if explicit {
			p.note("%s is named by the configuration and the source does not hold it", from)
		}
		return
	}
	out.FromVersion = src.version
	if len(src.value) > port.MaxSecret {
		out.Status, out.Reason = PlanRefused, fmt.Sprintf("a credential of %d bytes is over the Secrets limit of %d (port.MaxSecret)", len(src.value), port.MaxSecret)
		p.add(t.Module, out)
		return
	}
	dst, err := p.readV5(ctx, t)
	switch {
	case err != nil:
		out.Status, out.Reason = PlanRefused, "the destination cannot be read: "+err.Error()
	case !dst.found:
		out.Status = PlanNew
	case bytes.Equal(dst.value, src.value):
		out.Status, out.ToVersion = PlanSame, dst.version
	default:
		out.Status, out.ToVersion = PlanDifferent, dst.version
	}
	p.add(t.Module, out)
}

func (p *planner) readV4(ctx context.Context, name string, t SecretTarget) (secretRead, error) {
	v4 := p.from.Stores.V4
	switch {
	case t.Config:
		src := p.from.Stores.Secrets
		if src == nil {
			return secretRead{}, nil
		}
		v, err := src.Get(ctx, strings.TrimPrefix(name, "internal/config/"))
		if errors.Is(err, secrets.ErrNotFound) {
			return secretRead{}, nil
		}
		return secretRead{value: []byte(v), found: err == nil}, err
	case strings.HasPrefix(name, "external/"):
		return itemOf(v4.External.Store().Get(ctx, strings.TrimPrefix(name, "external/")))
	}
	return itemOf(v4.Internal.Store().Get(ctx, strings.TrimPrefix(name, "internal/")))
}

func (p *planner) readV5(ctx context.Context, t SecretTarget) (secretRead, error) {
	v5 := p.to.Stores.V5
	m := secretstore.Module(t.Module)
	if rest, ok := strings.CutPrefix(t.V5, "external/"+t.Module+"/"); ok {
		return itemOf(v5.ExternalStore(m).Get(ctx, rest))
	}
	rest := strings.TrimPrefix(t.V5, "internal/"+t.Module+"/")
	if t.Config {
		v, rev, err := state.NewValue(v5.InternalStore(m), rest, state.Raw()).Get(ctx)
		if errors.Is(err, state.ErrNotFound) {
			return secretRead{}, nil
		}
		return secretRead{value: v, version: string(rev), found: err == nil}, err
	}
	return itemOf(v5.InternalStore(m).Get(ctx, rest))
}

func itemOf(it state.Item, err error) (secretRead, error) {
	if errors.Is(err, state.ErrNotFound) {
		return secretRead{}, nil
	}
	if err != nil {
		return secretRead{}, err
	}
	return secretRead{value: it.Value, version: string(it.Rev), found: true}, nil
}

// issuerRing are the issuer kinds a copy without sessions still carries.
var issuerRing = []string{"keyring", "keyring-retired", "keyring-index", "guard"}

// issuerGroup is a row of the issuer's records: their keys carry bearer values,
// so the plan counts them per kind and status.
type issuerGroup struct {
	module, kind, from string
	status             PlanStatus
	reason             string
}

func (p *planner) issuer(ctx context.Context) error {
	srcEx, ok := p.src.ports.State.(port.StateExporter)
	if !ok {
		p.note("the issuer's records are not planned: the source's State cannot say how long a record has left")
		return nil
	}
	groups := map[issuerGroup]int{}
	var dstState map[string]port.Exported
	if dstEx, ok := p.dst.ports.State.(port.StateExporter); ok {
		dstState = map[string]port.Exported{}
		if err := dstEx.ExportState(ctx, issuerPrefix, func(x port.Exported) error { dstState[x.Key] = x; return nil }); err != nil {
			return fmt.Errorf("read the destination's issuer records: %w", err)
		}
	} else {
		p.note("the destination's State cannot list the issuer's records, so they are all counted as new")
	}
	err := srcEx.ExportState(ctx, issuerPrefix, func(x port.Exported) error {
		g := issuerGroup{module: "oidc", from: issuerFamily(x.Key)}
		t, terr := StateTarget(x.Key)
		switch {
		case terr != nil:
			g.kind, g.status, g.reason = "unknown", PlanRefused, terr.Error()
		case !p.wanted(t.Kind):
			return nil
		default:
			g.kind = t.Kind
			g.status, g.reason = statusOf(x, dstState, x.TTL <= 0)
		}
		groups[g]++
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the source's issuer records: %w", err)
	}
	if err = p.indexes(ctx, groups); err != nil {
		return err
	}
	if !p.opt.Sessions {
		p.note("the issuer's sessions, refresh tokens, single sign-on records and codes in flight are not planned (--sessions skip): " +
			"people would sign in again; the key ring and the state secret's fingerprint are")
	}
	keys := make([]issuerGroup, 0, len(groups))
	for g := range groups {
		keys = append(keys, g)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.kind+"|"+string(a.status)+"|"+a.from < b.kind+"|"+string(b.status)+"|"+b.from
	})
	for _, g := range keys {
		p.add(g.module, PlanItem{Concern: "state", Kind: g.kind, From: g.from, To: g.module + "/" + g.kind, Status: g.status, Count: groups[g], Reason: g.reason})
	}
	return nil
}

func (p *planner) wanted(kind string) bool {
	return p.opt.Sessions || slices.Contains(issuerRing, kind)
}

func (p *planner) indexes(ctx context.Context, groups map[issuerGroup]int) error {
	srcEx, ok := p.src.ports.Index.(port.IndexExporter)
	if !ok {
		return nil
	}
	have := map[string][]string{}
	if dstEx, ok := p.dst.ports.Index.(port.IndexExporter); ok {
		err := dstEx.ExportIndex(ctx, issuerPrefix, func(x port.Exported) error {
			have[x.Key] = sortedMembers(x.Members)
			return nil
		})
		if err != nil {
			return fmt.Errorf("read the destination's issuer Index sets: %w", err)
		}
	}
	err := srcEx.ExportIndex(ctx, issuerPrefix, func(x port.Exported) error {
		g := issuerGroup{module: "oidc", from: issuerFamily(x.Key)}
		t, terr := IndexTarget(x.Key)
		switch {
		case terr != nil:
			g.kind, g.status, g.reason = "unknown", PlanRefused, terr.Error()
		case !p.wanted(t.Kind):
			return nil
		default:
			g.kind = t.Kind
			switch got, ok := have[x.Key]; {
			case !ok:
				g.status = PlanNew
			case slices.Equal(got, sortedMembers(x.Members)):
				g.status = PlanSame
			default:
				g.status = PlanDifferent
			}
		}
		groups[g]++
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the source's issuer Index sets: %w", err)
	}
	return nil
}

func sortedMembers(m []string) []string {
	out := slices.Clone(m)
	slices.Sort(out)
	return out
}

// statusOf compares a source record with the destination's.
func statusOf(x port.Exported, dst map[string]port.Exported, noLifetime bool) (PlanStatus, string) {
	if noLifetime {
		return PlanRefused, "the record has no lifetime, and the issuer's state always has one"
	}
	got, ok := dst[x.Key]
	switch {
	case !ok:
		return PlanNew, ""
	case bytes.Equal(got.Value, x.Value):
		return PlanSame, ""
	}
	return PlanDifferent, ""
}

// issuerFamily is a key's family, which names no bearer value.
func issuerFamily(key string) string {
	shown := shownKey(DomainIssuer, key)
	if i := strings.Index(shown, ":#"); i >= 0 {
		shown = shown[:i]
	}
	return shown + ":*"
}

func (p *planner) blobs(ctx context.Context) error {
	if ok, why := blobsCopied(p.opt.Blobs, p.from, p.to); !ok {
		p.note("%s", why)
		return nil
	}
	s := blobSteps()[0]
	src, err := s.read(ctx, p.src)
	if err != nil {
		if errors.Is(err, port.ErrUnsupported) {
			p.note("the source has no Blob port, so no report is planned")
			return nil
		}
		return fmt.Errorf("read the source's reports: %w", err)
	}
	dst, err := s.read(ctx, p.dst)
	if err != nil && !errors.Is(err, port.ErrUnsupported) {
		return fmt.Errorf("read the destination's reports: %w", err)
	}
	have := map[string]entry{}
	for _, e := range dst {
		have[e.id] = e
	}
	for _, e := range src {
		module := "github"
		if strings.HasPrefix(e.id, "reports/slack/") {
			module = "slack"
		}
		out := PlanItem{Concern: "blob", Kind: "report", ID: e.id, From: e.id, To: e.id}
		old, ok := have[e.id]
		switch why := refusal(s, e); {
		case why != "":
			out.Status, out.Reason = PlanRefused, why
		case !ok:
			out.Status = PlanNew
		case bytes.Equal(old.canon, e.canon):
			out.Status = PlanSame
		default:
			out.Status = PlanDifferent
		}
		p.add(module, out)
	}
	return nil
}

// String is a one-line summary per module, for a terminal.
func (c PlanCounts) String() string {
	return "new " + strconv.Itoa(c.New) + ", same " + strconv.Itoa(c.Same) +
		", different " + strconv.Itoa(c.Different) + ", refused " + strconv.Itoa(c.Refused)
}
