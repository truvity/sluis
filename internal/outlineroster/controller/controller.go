// Package controller is the Outline controller: for every Outline workspace
// the policy declares, read what Outline holds and what the directory says of
// each of its people, decide, act where the workspace is enabled, and report.
//
// It is built on the rails the GitHub and Slack controllers share
// (internal/rails, docs/decisions/0024): the pass loop, the policy guard on
// every answer of the console, the tick lease, the breaker fingerprints, the
// held-once ledger, the last-good-report journal and the dry-run switch.
//
// # One tick
//
//  1. The workspace's connection record and API key are read from the
//     console's records. A workspace with none is waiting, not failed.
//  2. Outline is read whole: the key's own user and the team settings, every
//     account (the suspended included), the groups and the managed groups'
//     members, the collections and the bound collections' grants and direct
//     members. A read cut short of a group or a collection holds that one; of
//     anything else, it fails the tick. A 429 fails the tick, which the next
//     one tries again.
//  3. Each account's address is put to the console's Explain, which resolves
//     it as the issuer does (the directory's standing, then the policy), and
//     every answer is held to this pass's policy digest.
//  4. The decision (package reconcile), and, when the workspace is enabled
//     ([rails.Switch]), the changes, each recorded to the audit trail.
//  5. The workspace's report is published. A failed tick keeps its previous
//     report with the failure on it.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/reconcile"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// LeaseKind is the kind of lease on a workspace's tick.
const LeaseKind = "outline-tick"

// DefaultInterval is the time between two passes: short, because a person
// who signs in for the first time sees nothing but their own documents until
// the pass after.
const DefaultInterval = 2 * time.Minute

// DefaultShareScan is how often the single-document shares are read: each
// document is two calls, so not every pass.
const DefaultShareScan = time.Hour

// MaxScannedDocuments bounds a share scan; a workspace past it is reported and
// not scanned.
const MaxScannedDocuments = 5000

// ErrUnknownTarget is a target that is not a workspace the policy declares.
var ErrUnknownTarget = errors.New("not a target of this controller")

// Directory is the console as this controller asks it: the three questions,
// each answered with the digest of the policy it was computed under. The
// generated AccessService client is one.
type Directory interface {
	Explain(context.Context, *directoryrosterv1.ExplainRequest) (*directoryrosterv1.ExplainResponse, error)
	ListServedDomains(context.Context, *directoryrosterv1.ListServedDomainsRequest) (*directoryrosterv1.ListServedDomainsResponse, error)
	ResolveDirectoryGroups(context.Context, *directoryrosterv1.ResolveDirectoryGroupsRequest) (*directoryrosterv1.ResolveDirectoryGroupsResponse, error)
}

// RecordSource is where the console keeps the workspaces' records
// (internal/portstore.OutlineWorkspaces).
type RecordSource interface {
	// Get is one workspace's record and API key; found is false when it is
	// not connected.
	Get(ctx context.Context, workspace string) (connection.Record, connection.Credential, bool, error)
	// Confirmations are the operators' confirmations that still stand, of
	// every workspace.
	Confirmations(ctx context.Context) ([]connection.Confirmation, error)
	// PassRequests are the operators' last requests for a pass now, by
	// workspace.
	PassRequests(ctx context.Context) (map[string]connection.PassRequest, error)
	// Digest summarises the records, so a change wakes a pass.
	Digest(ctx context.Context) ([32]byte, error)
}

// Config is what a deployment decides.
type Config struct {
	// Interval is the time between passes. Zero is [DefaultInterval].
	Interval    time.Duration
	PolicyRetry time.Duration
	// Enabled are the workspaces the controller acts in. Every other is a
	// dry run.
	Enabled map[string]bool
	// ShareScan is how often the single-document shares are read. Zero is
	// [DefaultShareScan]; negative never reads them.
	ShareScan time.Duration
	// RecordPoll is how often the records are looked at for a change or a
	// request for a pass. Zero is thirty seconds; negative turns it off.
	RecordPoll time.Duration
}

// Deps are what the controller talks to.
type Deps struct {
	Log       *slog.Logger
	Directory Directory
	// Audit is where the controller records what it did. Nil records
	// nothing.
	Audit   audit.Recorder
	Status  rails.Store
	Records RecordSource
	// Policy is the declared policy the controller decides with, and Digest
	// its digest: an answer of the console under another is not acted on.
	Policy policy.Policy
	Digest string
	// Outline makes the client of a workspace. Nil is Outline's own API.
	Outline func(url, key string) *outlineapi.Client
	// Leases takes a lease on each workspace before its tick. Nil runs every
	// tick without one: a single runner.
	Leases *rails.Leases
	Now    func() time.Time
}

// Controller is the loop and what it remembers between passes.
type Controller struct {
	cfg  Config
	deps Deps
	// recorded is what each workspace's ledger has put in the audit trail:
	// the holds, the tripped breakers and the adopted collections, each once.
	recorded rails.Ledger
	journal  *rails.Journal[status.Workspace]
	metrics  instruments
}

// New returns a controller.
func New(cfg Config, deps Deps) *Controller {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Outline == nil {
		deps.Outline = func(url, key string) *outlineapi.Client { return outlineapi.New(url, key) }
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.ShareScan == 0 {
		cfg.ShareScan = DefaultShareScan
	}
	return &Controller{
		cfg: cfg, deps: deps, metrics: newInstruments(),
		journal: &rails.Journal[status.Workspace]{
			Store: deps.Status, Key: status.Key, Encode: status.Encode, Decode: status.Decode, Log: deps.Log, Label: "workspace",
		},
	}
}

// Targets are the declared workspaces, by key.
func (c *Controller) Targets() []string {
	return slices.Sorted(maps.Keys(c.deps.Policy.Outline.Workspaces))
}

// Run passes now and then every interval until the context ends. A changed
// record wakes a pass at once, and an operator's request ticks its workspace.
func (c *Controller) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.watch(watchCtx, wake)
	return rails.Run(ctx, c.deps.Log, rails.Pacing{Interval: c.cfg.Interval, PolicyRetry: c.cfg.PolicyRetry, Wake: wake}, c.Pass)
}

func (c *Controller) watch(ctx context.Context, wake chan<- struct{}) {
	src := c.deps.Records
	if src == nil {
		return
	}
	var last [32]byte
	rails.Watch{
		Poll: c.cfg.RecordPoll,
		Digest: func() [32]byte {
			digest, err := src.Digest(ctx)
			if err != nil {
				c.deps.Log.WarnContext(ctx, "the Outline records could not be read for the change check", logattr.SafeError("error", err))
				return last
			}
			last = digest
			return digest
		},
		Requests: func() map[string]time.Time {
			out := map[string]time.Time{}
			requests, err := src.PassRequests(ctx)
			if err != nil {
				return out
			}
			for workspace, r := range requests {
				out[workspace] = r.At
			}
			return out
		},
		Changed: "an Outline workspace's connection changed",
		OnRequest: func(workspace string) {
			if _, _, err := c.RunTarget(ctx, workspace); err != nil && !errors.Is(err, ErrUnknownTarget) {
				c.deps.Log.WarnContext(ctx, "a requested tick failed", logattr.SafeString("workspace", workspace), logattr.SafeError("error", err))
			}
		},
	}.Run(ctx, c.deps.Log, wake)
}

// Pass is a sweep: every declared workspace once, each under its own lease.
// It says whether any answer came from a console under another policy.
func (c *Controller) Pass(ctx context.Context) (otherPolicy bool) {
	for _, target := range c.Targets() {
		_, differs, err := c.RunTarget(ctx, target)
		if err != nil {
			c.deps.Log.WarnContext(ctx, "a tick failed", logattr.SafeString("workspace", target), logattr.SafeError("error", err))
		}
		otherPolicy = otherPolicy || differs
	}
	c.journal.Prune(ctx, c.Targets())
	return otherPolicy
}

// RunTarget ticks one workspace under its lease and says whether it ran:
// false, with no error, when another runner holds the lease.
func (c *Controller) RunTarget(ctx context.Context, target string) (ran, otherPolicy bool, err error) {
	if _, declared := c.deps.Policy.Outline.Workspaces[target]; !declared {
		return false, false, fmt.Errorf("%w: %q", ErrUnknownTarget, target)
	}
	tick := func(ctx context.Context) { otherPolicy = c.Tick(ctx, target) }
	if c.deps.Leases == nil {
		tick(ctx)
		return true, otherPolicy, nil
	}
	ran, err = c.deps.Leases.Do(ctx, LeaseKind, target, tick)
	return ran, otherPolicy, err
}

// Tick is one workspace's work, with no lease: its pass, ending in its own
// report whatever happened, which it publishes.
func (c *Controller) Tick(ctx context.Context, target string) (otherPolicy bool) {
	ctx, done := rails.StartTick(ctx, LeaseKind, target)
	report, differs := c.workspace(ctx, target)
	c.metrics.recordPass(ctx, &report)
	c.journal.PublishOne(ctx, target, report)
	outcome := rails.OutcomeOK
	if report.Tick.Outcome == status.OutcomeFailed {
		outcome = rails.OutcomeFailed
	}
	done(outcome)
	return differs
}

// errNotConnected marks a workspace with no connection record yet.
var errNotConnected = errors.New("not connected")

// workspace is one workspace's pass, ending in its report.
func (c *Controller) workspace(ctx context.Context, key string) (status.Workspace, bool) {
	enabled := c.cfg.Enabled[key]
	started := c.deps.Now().UTC()
	previous := c.journal.Previous(ctx, key)
	fail := func(err error) (status.Workspace, bool) {
		c.deps.Log.WarnContext(ctx, "a pass over an Outline workspace failed", logattr.SafeString("workspace", key), logattr.SafeError("error", err))
		report := previous
		report.Workspace, report.Enabled = key, enabled
		report.Tick = status.Tick{At: started, Outcome: status.OutcomeFailed, Error: err.Error()}
		return report, errors.Is(err, rails.ErrPolicyDiffers)
	}
	record, credential, err := c.connection(ctx, key)
	if errors.Is(err, errNotConnected) {
		c.deps.Log.InfoContext(ctx, "an Outline workspace is waiting to be connected", logattr.SafeString("workspace", key))
		return status.Workspace{Version: status.Version, Workspace: key, Enabled: enabled,
			Tick: status.Tick{At: started, Outcome: status.OutcomeWaiting}}, false
	}
	if err != nil {
		return fail(err)
	}
	bound := c.deps.Policy.Outline.Workspaces[key]
	client := c.deps.Outline(record.URL, credential.APIKey)
	observed, team, err := observe(ctx, client, bound)
	if err != nil {
		return fail(err)
	}
	if record.Team != "" && team != record.Team {
		return fail(fmt.Errorf("the API key belongs to another Outline team (%s) than the one this workspace was connected to (%s): connect it again", team, record.Team))
	}
	scanned := false
	if c.cfg.ShareScan > 0 && started.Sub(previous.SharesScannedAt) >= c.cfg.ShareScan {
		shares, err := scanShares(ctx, client, bound, observed)
		switch {
		case outlineapi.RateLimited(err):
			return fail(fmt.Errorf("Outline's rate limit was reached reading the shares; the next pass tries again: %w", err))
		case err != nil:
			c.deps.Log.WarnContext(ctx, "the single-document shares could not be read; the last findings stand", logattr.SafeString("workspace", key),
				logattr.SafeError("error", err))
		default:
			observed.Shares, observed.SharesScanned, scanned = shares, true, true
		}
	}
	people, otherPolicy, err := c.people(ctx, bound, observed.Users)
	if err != nil {
		return fail(err)
	}
	in := reconcile.Input{
		Workspace: key, Policy: bound, Groups: c.deps.Policy.Groups, Client: bound.Client,
		Observed: observed, People: people, Confirmed: c.confirmed(ctx, key, started),
		Findings: reconcile.ShareFindings(previous),
	}
	c.directories(ctx, bound, &in)
	if errors.Is(in.DirectoryErr, rails.ErrPolicyDiffers) {
		otherPolicy = true
	}
	decision := reconcile.Decide(in)
	report := decision.Report
	report.Version, report.Enabled, report.URL = status.Version, enabled, record.URL
	report.Tick.At = started
	report.SharesScannedAt = previous.SharesScannedAt
	if scanned {
		report.SharesScannedAt = started
	}
	report.Recorded = previous.Recorded

	rails.Switch(enabled).Act(func() {
		results := c.apply(ctx, client, key, decision.Actions)
		fold(&report, results)
		report.Recorded = c.recordOnce(ctx, key, &report, previous.Recorded)
	})
	report.Tick.Outcome = status.OutcomeOf(rails.Switch(enabled).Decide(rails.Tick{
		Changes: report.Tick.Changes, Held: report.Tick.Held, Retrying: report.Tick.Retrying,
	}))
	c.deps.Log.InfoContext(ctx, "passed over an Outline workspace", logattr.SafeString("workspace", key), slog.Bool("enabled", enabled),
		slog.Any("outcome", report.Tick.Outcome), slog.Int("changes", report.Tick.Changes), slog.Int("held", report.Tick.Held),
		slog.Int("retrying", report.Tick.Retrying))
	c.journal.Remember(key, report)
	return report, otherPolicy
}

// connection is one workspace's record and API key.
func (c *Controller) connection(ctx context.Context, key string) (connection.Record, connection.Credential, error) {
	if c.deps.Records == nil {
		return connection.Record{}, connection.Credential{}, errNotConnected
	}
	record, credential, found, err := c.deps.Records.Get(ctx, key)
	switch {
	case err != nil:
		return connection.Record{}, connection.Credential{}, fmt.Errorf("read the workspace's connection: %w", err)
	case !found:
		return connection.Record{}, connection.Credential{}, errNotConnected
	case record.URL == "":
		return connection.Record{}, connection.Credential{}, errors.New("the workspace's connection record names no URL: connect it again")
	}
	return record, credential, nil
}

// confirmed are a workspace's confirmations that still stand, by scope.
func (c *Controller) confirmed(ctx context.Context, key string, now time.Time) map[string]string {
	out := map[string]string{}
	if c.deps.Records == nil {
		return out
	}
	all, err := c.deps.Records.Confirmations(ctx)
	if err != nil {
		c.deps.Log.WarnContext(ctx, "the confirmations could not be read; every breaker holds", logattr.SafeError("error", err))
		return out
	}
	for _, confirmation := range all {
		if confirmation.Workspace == key && confirmation.Current(now) {
			out[confirmation.Scope] = confirmation.Fingerprint
		}
	}
	return out
}

// people asks the console about every account's address, one at a time, each
// answer held to this pass's policy. An account that could not be asked is
// absent, and nothing changes for it.
func (c *Controller) people(ctx context.Context, bound policy.OutlineWorkspace, users []reconcile.User) (map[string]reconcile.Person, bool, error) {
	var emails []string
	for _, u := range users {
		if u.Email != "" {
			emails = append(emails, u.Email)
		}
	}
	slices.Sort(emails)
	guard := rails.PolicyGuard{Digest: c.deps.Digest}
	answers, otherPolicy := rails.Confirm(slices.Compact(emails), guard,
		func(email string) (reconcile.Person, string, error) {
			response, err := c.deps.Directory.Explain(ctx, &directoryrosterv1.ExplainRequest{Email: email})
			if err != nil {
				return reconcile.Person{}, "", err
			}
			p := reconcile.Person{
				Asked: true, Authoritative: response.GetAuthoritative(), Found: response.GetFound(), Suspended: response.GetSuspended(),
			}
			for _, held := range response.GetHeld() {
				p.Groups = append(p.Groups, held.GetGroup())
			}
			for _, client := range response.GetClients() {
				if client.GetId() == bound.Client && client.GetAdmitted() {
					p.Admitted = true
				}
			}
			return p, response.GetPolicyDigest(), nil
		},
		func(email string, err error) {
			c.deps.Log.WarnContext(ctx, "the directory could not be asked about an account; nothing changes for it this pass",
				logattr.SafeString("email", email), logattr.SafeError("error", err))
		})
	if ctx.Err() != nil {
		return nil, otherPolicy, ctx.Err()
	}
	return answers, otherPolicy, nil
}

// directories reads what the `directory` checks need, when a collection names
// a directory: the connected directories and their domains, and the owner of
// every directory group a guarded collection's groups name. Anything that
// cannot be read is DirectoryErr, which holds those collections.
func (c *Controller) directories(ctx context.Context, bound policy.OutlineWorkspace, in *reconcile.Input) {
	var members []string
	guarded := false
	for _, col := range bound.Collections {
		if col.Directory == "" {
			continue
		}
		guarded = true
		for group := range col.Grants() {
			for _, m := range c.deps.Policy.Groups[group].Members {
				members = append(members, strings.ToLower(m))
			}
		}
	}
	if !guarded {
		return
	}
	served, err := c.deps.Directory.ListServedDomains(ctx, &directoryrosterv1.ListServedDomainsRequest{})
	if err != nil {
		in.DirectoryErr = err
		return
	}
	for _, dir := range served.GetDirectories() {
		d := reconcile.Directory{ID: dir.GetWorkspaceId(), Primary: strings.ToLower(dir.GetPrimaryDomain())}
		for _, domain := range dir.GetDomains() {
			d.Domains = append(d.Domains, strings.ToLower(domain))
		}
		in.Directories = append(in.Directories, d)
	}
	slices.Sort(members)
	members = slices.Compact(members)
	in.DirGroups = map[string]reconcile.DirGroup{}
	for len(members) > 0 {
		n := min(len(members), 200)
		batch := members[:n]
		members = members[n:]
		response, err := c.deps.Directory.ResolveDirectoryGroups(ctx, &directoryrosterv1.ResolveDirectoryGroupsRequest{Groups: batch})
		if err == nil {
			err = rails.PolicyGuard{Digest: c.deps.Digest}.Check(response.GetPolicyDigest())
		}
		if err != nil {
			in.DirectoryErr = err
			return
		}
		for _, g := range response.GetGroups() {
			in.DirGroups[strings.ToLower(g.GetEmail())] = reconcile.DirGroup{Found: g.GetFound(), Owner: g.GetWorkspaceId()}
		}
	}
}

// observe reads one workspace whole.
func observe(ctx context.Context, client *outlineapi.Client, bound policy.OutlineWorkspace) (reconcile.Observed, string, error) {
	var out reconcile.Observed
	info, err := client.AuthInfo(ctx)
	if err != nil {
		return out, "", fmt.Errorf("ask Outline who the API key is: %w", err)
	}
	out.KeyUser = user(info.User)
	out.Settings = &reconcile.Settings{
		Sharing: info.Team.Sharing, MemberCollectionCreate: info.Team.MemberCollectionCreate,
		MemberInvites: info.Team.MembersCanInvite(), DefaultRole: info.Team.DefaultUserRole,
	}
	users, err := client.Users(ctx)
	if err != nil {
		return out, "", fmt.Errorf("list Outline's accounts: %w", err)
	}
	for _, u := range users {
		out.Users = append(out.Users, user(u))
	}
	groups, err := client.Groups(ctx)
	if err != nil {
		return out, "", fmt.Errorf("list Outline's groups: %w", err)
	}
	byName := map[string][]outlineapi.Group{}
	for _, g := range groups {
		out.Groups = append(out.Groups, reconcile.Group{ID: g.ID, Name: g.Name})
		byName[strings.ToLower(g.Name)] = append(byName[strings.ToLower(g.Name)], g)
	}
	out.Members = map[string][]string{}
	for _, name := range bound.Groups() {
		found := byName[strings.ToLower(name)]
		if len(found) != 1 {
			continue
		}
		members, err := client.GroupMembers(ctx, found[0].ID)
		if outlineapi.RateLimited(err) {
			return out, "", fmt.Errorf("Outline's rate limit was reached; the next pass tries again: %w", err)
		}
		if err == nil {
			out.Members[found[0].ID] = members
		}
	}
	collections, err := client.Collections(ctx)
	if err != nil {
		return out, "", fmt.Errorf("list Outline's collections: %w", err)
	}
	for _, col := range collections {
		// Another person's personal collection is never listed to an admin;
		// the key user's own is never bound.
		if col.Personal() {
			continue
		}
		out.Collections = append(out.Collections, reconcile.Collection{ID: col.ID, Name: col.Name})
	}
	out.Grants, out.Direct = map[string][]reconcile.Grant{}, map[string][]reconcile.Membership{}
	for _, name := range slices.Sorted(maps.Keys(bound.Collections)) {
		b := bound.Collections[name]
		for _, col := range out.Collections {
			if (b.Adopt != "" && col.ID != b.Adopt) || (b.Adopt == "" && col.Name != name) {
				continue
			}
			grants, err := client.CollectionGroups(ctx, col.ID)
			if err == nil {
				var members []outlineapi.Membership
				members, err = client.CollectionMembers(ctx, col.ID)
				if err == nil {
					for _, g := range grants {
						out.Grants[col.ID] = append(out.Grants[col.ID], reconcile.Grant{GroupID: g.GroupID, Permission: g.Permission})
					}
					if out.Grants[col.ID] == nil {
						out.Grants[col.ID] = []reconcile.Grant{}
					}
					out.Direct[col.ID] = []reconcile.Membership{}
					for _, m := range members {
						out.Direct[col.ID] = append(out.Direct[col.ID], reconcile.Membership{UserID: m.UserID, Permission: m.Permission})
					}
				}
			}
			if outlineapi.RateLimited(err) {
				return out, "", fmt.Errorf("Outline's rate limit was reached; the next pass tries again: %w", err)
			}
		}
	}
	if integrations, err := client.Integrations(ctx); err == nil {
		for _, i := range integrations {
			channel, _ := i.Settings["channel"].(string)
			ri := reconcile.Integration{ID: i.ID, Service: i.Service, CollectionID: i.CollectionID}
			if channel != "" {
				ri.Service += " channel " + channel
			}
			out.Integrations = append(out.Integrations, ri)
		}
	}
	return out, info.Team.ID, nil
}

func user(u outlineapi.User) reconcile.User {
	return reconcile.User{ID: u.ID, Name: u.Name, Email: strings.ToLower(strings.TrimSpace(u.Email)), Role: u.Role, Suspended: u.IsSuspended}
}

// scanShares reads the direct single-document shares in every bound
// collection that names a directory. Outline answers documents.memberships
// only to someone who may edit the document, so a collection the key's user
// is not a member of has its documents left out (documents.list does not list
// them either).
func scanShares(ctx context.Context, client *outlineapi.Client, bound policy.OutlineWorkspace, observed reconcile.Observed) ([]reconcile.Share, error) {
	var out []reconcile.Share
	documents := 0
	for _, name := range slices.Sorted(maps.Keys(bound.Collections)) {
		b := bound.Collections[name]
		if b.Directory == "" {
			continue
		}
		for _, col := range observed.Collections {
			if (b.Adopt != "" && col.ID != b.Adopt) || (b.Adopt == "" && col.Name != name) {
				continue
			}
			docs, err := client.Documents(ctx, col.ID)
			if err != nil {
				return nil, err
			}
			documents += len(docs)
			if documents > MaxScannedDocuments {
				return nil, fmt.Errorf("more than %d documents to scan", MaxScannedDocuments)
			}
			for _, doc := range docs {
				users, err := client.DocumentMembers(ctx, doc.ID)
				if err != nil {
					return nil, err
				}
				groups, err := client.DocumentGroups(ctx, doc.ID)
				if err != nil {
					return nil, err
				}
				for _, m := range users {
					out = append(out, reconcile.Share{CollectionID: col.ID, DocumentID: doc.ID, Title: doc.Title, UserID: m.UserID, Permission: m.Permission})
				}
				for _, m := range groups {
					out = append(out, reconcile.Share{CollectionID: col.ID, DocumentID: doc.ID, Title: doc.Title, GroupID: m.GroupID, Permission: m.Permission})
				}
			}
		}
	}
	return out, nil
}
