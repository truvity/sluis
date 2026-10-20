package console

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"

	"connectrpc.com/connect/v2"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubapp/mints"
	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// The four kinds of App, the two origins, the four states and the four
// words for who moves next — spelled short, because the generated names
// carry the whole enum with them.
const (
	appLink       = directoryrosterv1.AppPurpose_APP_PURPOSE_LINK
	appController = directoryrosterv1.AppPurpose_APP_PURPOSE_CONTROLLER
	appRunners    = directoryrosterv1.AppPurpose_APP_PURPOSE_RUNNERS
	appTokens     = directoryrosterv1.AppPurpose_APP_PURPOSE_TOKENS

	appPreset    = directoryrosterv1.AppOrigin_APP_ORIGIN_PRESET
	appCatalogue = directoryrosterv1.AppOrigin_APP_ORIGIN_CATALOGUE

	appNotCreated = directoryrosterv1.AppState_APP_STATE_NOT_CREATED
	appCreated    = directoryrosterv1.AppState_APP_STATE_CREATED
	appInstalled  = directoryrosterv1.AppState_APP_STATE_INSTALLED
	appDrifted    = directoryrosterv1.AppState_APP_STATE_DRIFTED

	appDone               = directoryrosterv1.AppAttention_APP_ATTENTION_DONE
	appNeedsYou           = directoryrosterv1.AppAttention_APP_ATTENTION_NEEDS_YOU
	appWaitingPerson      = directoryrosterv1.AppAttention_APP_ATTENTION_WAITING_PERSON
	appWaitingController  = directoryrosterv1.AppAttention_APP_ATTENTION_WAITING_CONTROLLER
	linkAppInstalledState = "created; installed nowhere, by design"
)

// githubAppSpec is one App before GitHub is asked: what declares it, what
// this service recorded when it was created, and where its key is kept.
//
// Every kind of App reduces to this, which is the point: the link App,
// an organisation's controller App, a tier's runner App and a catalogue
// App differ in where their record lives, not in what an operator needs
// to be told about them.
type githubAppSpec struct {
	id      string
	purpose directoryrosterv1.AppPurpose
	origin  directoryrosterv1.AppOrigin
	org     string
	tier    string
	// entry is the declaration the App is created from — a catalogue entry,
	// or the one the manifest builder writes for a preset. Zero for an App
	// nothing declares any more: there is then no declaration to compare
	// GitHub against, and none is invented.
	entry    catalogue.App
	declared bool

	created   bool
	installed bool
	appID     int64
	// installationID is 0 for an App installed nowhere, and for the link
	// App, which is installed nowhere by design.
	installationID int64
	appSlug        string
	htmlURL        string
	connectedAt    time.Time
	connectedBy    string
	// webhookURL and hookRotatedAt are where GitHub was last told to deliver
	// the App's events and when it was last given a secret; empty and zero
	// for an App with no webhook.
	webhookURL    string
	hookRotatedAt time.Time
	// owner is the directory workspace recorded as the owner of the
	// organisation this App acts on, empty for none; the link App has none.
	owner string

	// key reads the App's private key, for asking GitHub as the App. Nil
	// where this service keeps none, and keyless then says why.
	key     func() string
	keyless string

	// place is where the App's key is kept, for a deployment copying it.
	place keyPlace

	// linked is how many accounts people linked through the link App.
	linked int
	// reported is whether the controller has reported on the organisation
	// this App acts on; reports is whether it could report at all.
	reported bool
	reports  bool
}

// githubAppsFacts are the answers about the deployment that come with the
// Apps: what it can keep, and what it declares.
type githubAppsFacts struct {
	connecting bool
	linking    bool
	catalogue  bool
	tiers      []string
	bound      []string
	linkURL    string
	// owners are the recorded owners of the connected organisations.
	owners map[string]string
}

// secretNamer is a store that can say which Kubernetes Secret it keeps
// keys in.
type secretNamer interface{ SecretName() string }

// keyPlace is where one App's key is kept: the secrets adapter, the address
// or Secret name in it, and the keys within a Kubernetes Secret.
type keyPlace struct {
	adapter, location string
	keys              []string
}

// placeOf says where an App's key is kept. A store of the ports is asked
// through Places by the App's id in it; a Kubernetes store names its Secret.
// The keys within the Secret are the Kubernetes store's alone. An empty place
// says nothing is known, which is not a claim that nothing is kept.
func (c *Service) placeOf(store any, id string, keys []string) keyPlace {
	if named, ok := store.(secretNamer); ok {
		if name := named.SecretName(); name != "" {
			return keyPlace{adapter: adapterKubernetes, location: name, keys: keys}
		}
	}
	if c.deps.Places != nil && id != "" {
		if adapter, location, ok := c.deps.Places.GitHubAppPlace(id); ok {
			return keyPlace{adapter: adapter, location: location}
		}
	}
	return keyPlace{}
}

// adapterKubernetes is the place of a key kept in a Kubernetes Secret.
const adapterKubernetes = "kubernetes"

// ListGitHubApps implements the Apps list: every App in one shape.
func (c *Service) ListGitHubApps(
	ctx context.Context, _ *directoryrosterv1.ListGitHubAppsRequest,
) (*directoryrosterv1.ListGitHubAppsResponse, error) {
	id, err := c.requireAnyOrg(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	specs, facts, err := c.githubAppSpecs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	// Only the Apps of organisations the caller may view: a scoped viewer
	// never sees another company's organisation, or an App in it.
	specs = slices.DeleteFunc(specs, func(spec githubAppSpec) bool { return !mayApp(id, access.RoleViewer, &spec) })
	facts.bound = slices.DeleteFunc(facts.bound, func(org string) bool {
		owner, connected := facts.owners[org]
		if connected {
			return !mayOwned(id, access.RoleViewer, owner)
		}
		return !id.Can(access.RoleViewer) && !id.CanAnywhere(access.RoleOperator)
	})
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	choices, mayNone := ownerChoices(id, dirs)
	views := c.githubAppViews(ctx, specs)
	for i := range views {
		c.decorateApp(views[i], id, &specs[i], ownerDomains(dirs))
	}
	return &directoryrosterv1.ListGitHubAppsResponse{
		OwnerChoices: choices, MayConnectWithoutOwner: mayNone,
		Apps:                views,
		ConnectingAvailable: facts.connecting,
		LinkingAvailable:    facts.linking,
		CatalogueAvailable:  facts.catalogue,
		RunnerTiers:         facts.tiers,
		BoundOrganisations:  facts.bound,
		LinkUrl:             facts.linkURL,
	}, nil
}

// decorateApp sets what an App's view says of the caller and of the
// organisation's owner: whether it may operate the App, which directory owns
// the organisation (by its primary domain), and whether it may change that.
func (c *Service) decorateApp(app *directoryrosterv1.GitHubApp, id access.Identity, spec *githubAppSpec, domains map[string]string) {
	app.CanOperate = mayApp(id, access.RoleOperator, spec)
	if spec.purpose == appLink {
		return
	}
	app.OwnerDirectory, app.OwnerDomain = spec.owner, domains[spec.owner]
	app.CanChangeOwner = spec.purpose == appController && spec.created && id.Can(access.RoleOperator)
}

// GetGitHubApp implements one App's page.
func (c *Service) GetGitHubApp(
	ctx context.Context, req *directoryrosterv1.GetGitHubAppRequest,
) (*directoryrosterv1.GetGitHubAppResponse, error) {
	if _, err := requireAnywhere(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	id, err := c.requireApp(ctx, access.RoleViewer, &spec)
	if err != nil {
		return nil, err
	}
	app := c.githubAppView(ctx, spec, false)
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	c.decorateApp(app, id, &spec, ownerDomains(dirs))
	return &directoryrosterv1.GetGitHubAppResponse{App: app}, nil
}

// ListGitHubAppTokens implements an App's *Recent tokens*: the last
// installation tokens asked of it, minted or refused, newest first.
//
// From this service's own memory of them rather than from the audit
// trail. The trail is the record and holds every request, but narrowing
// it by kind and target scans object after object of every hour in turn:
// measured against a real bucket the call was still running when the
// gateway gave up on it, and the section on the page was a sentence
// apologising. What it costs to keep the answer to this one question
// beside the process that answers it is a bounded ring per App.
//
// Operator, not viewer: a request names who asked for it, as the Audit
// page does.
func (c *Service) ListGitHubAppTokens(
	ctx context.Context, req *directoryrosterv1.ListGitHubAppTokensRequest,
) (*directoryrosterv1.ListGitHubAppTokensResponse, error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	// The App first, so an id nothing declares is not_found rather than an
	// empty list, and a store that cannot be read says so rather than
	// reporting that nothing has been asked for.
	spec, err := c.githubAppSpec(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	// The organisation's own operator, not any operator: a request names who
	// asked for it, and that is another company's business.
	if _, err = c.requireApp(ctx, access.RoleOperator, &spec); err != nil {
		return nil, err
	}
	switch {
	case spec.purpose != appTokens:
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"%s mints no installation tokens: only an App the catalogue declares does", spec.id)
	case c.deps.Mints == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment mints no installation tokens here, so it keeps none to show")
	}
	return TokensOf(c.deps.Mints, spec.id), nil
}

// TokensOf is the ring's last requests of one App, as the console tells them.
// It is shared by the module and by an issuer that keeps the ring itself: the
// ring is written where the decision to mint is made, which is the issuer.
func TokensOf(ring *mints.Ring, app string) *directoryrosterv1.ListGitHubAppTokensResponse {
	out := &directoryrosterv1.ListGitHubAppTokensResponse{
		Kept:      int32(ring.PerAppKept()), //nolint:gosec // a page's worth
		KeptSince: timestampOf(ring.Since()),
	}
	recent := ring.Recent(app)
	for i := range recent {
		out.Tokens = append(out.Tokens, &directoryrosterv1.GitHubAppToken{
			At: timestampOf(recent[i].At), Subject: recent[i].Subject, Proof: recent[i].Proof, Grant: recent[i].Grant,
			Repositories: slices.Clone(recent[i].Repositories), Permissions: recent[i].Permissions,
			Outcome: recent[i].Outcome, Reason: recent[i].Reason,
		})
	}
	return out
}

// CheckGitHubApp asks GitHub again about one App, whichever kind it is.
func (c *Service) CheckGitHubApp(
	ctx context.Context, req *directoryrosterv1.CheckGitHubAppRequest,
) (*directoryrosterv1.CheckGitHubAppResponse, error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	id, err := c.requireApp(ctx, access.RoleOperator, &spec)
	if err != nil {
		return nil, err
	}
	app := c.githubAppView(ctx, spec, true)
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	c.decorateApp(app, id, &spec, ownerDomains(dirs))
	return &directoryrosterv1.CheckGitHubAppResponse{App: app}, nil
}

// githubAppSpec finds one App by the id the list gives it.
func (c *Service) githubAppSpec(ctx context.Context, id string) (githubAppSpec, error) {
	id = strings.TrimSpace(id)
	specs, _, err := c.githubAppSpecs(ctx)
	if err != nil {
		return githubAppSpec{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	for i := range specs {
		if specs[i].id == id {
			return specs[i], nil
		}
	}
	return githubAppSpec{}, connect.Errorf(connect.CodeNotFound, "no App %q is declared or created here", id)
}

// githubAppViews renders every spec, asking GitHub about each in
// parallel: one App's slow answer must not add to another's. Always
// through the cache — a list of Apps is a page load, and only a re-check
// of one App spends a round trip per App.
func (c *Service) githubAppViews(ctx context.Context, specs []githubAppSpec) []*directoryrosterv1.GitHubApp {
	out := make([]*directoryrosterv1.GitHubApp, len(specs))
	var wait sync.WaitGroup
	for i := range specs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			out[i] = c.githubAppView(ctx, specs[i], false)
		}()
	}
	wait.Wait()
	return out
}

// githubAppSpecs is every App this service keeps a key for or is declared
// to, with the deployment's answers about what it can keep.
//
// Ids are handed out catalogue first: a catalogue id is the operator's
// own name for an App, so it wins a collision and the preset that wanted
// it moves aside.
func (c *Service) githubAppSpecs(ctx context.Context) ([]githubAppSpec, githubAppsFacts, error) {
	facts := githubAppsFacts{
		connecting: c.deps.Orgs != nil,
		linking:    c.deps.LinkApp != nil && c.deps.Links != nil,
		catalogue:  c.deps.CatalogueApps != nil,
	}
	if facts.linking {
		facts.linkURL = c.githubRoot(ctx) + consolewire.LinkPath
	}
	if c.deps.RunnerApps != nil {
		facts.tiers = slices.Clone(c.deps.RunnerTiers)
	}
	bound := c.bindings()
	facts.bound = slices.Sorted(maps.Keys(bound))

	taken := map[string]bool{}
	catalogueSpecs, err := c.catalogueAppSpecs(ctx, taken)
	if err != nil {
		return nil, facts, err
	}
	linkSpecs, err := c.linkAppSpecs(ctx, taken)
	if err != nil {
		return nil, facts, err
	}
	controllerSpecs, err := c.controllerAppSpecs(ctx, bound, taken)
	if err != nil {
		return nil, facts, err
	}
	runnerSpecs, err := c.runnerAppSpecs(ctx, bound, taken)
	if err != nil {
		return nil, facts, err
	}
	specs := slices.Concat(linkSpecs, controllerSpecs, runnerSpecs, catalogueSpecs)
	if facts.owners, err = c.githubOwners(ctx); err != nil {
		return nil, facts, err
	}
	for i := range specs {
		if specs[i].purpose != appLink {
			specs[i].owner = facts.owners[specs[i].org]
		}
	}
	return specs, facts, nil
}

// presetID is the id a preset App takes, unless a catalogue id already
// has it.
func presetID(base string, taken map[string]bool) string {
	id := base
	for taken[id] {
		id += "-preset"
	}
	taken[id] = true
	return id
}

// linkAppSpecs is the link App: one, for every organisation, or none
// where nobody can link here.
func (c *Service) linkAppSpecs(ctx context.Context, taken map[string]bool) ([]githubAppSpec, error) {
	store, links := c.deps.LinkApp, c.deps.Links
	if store == nil || links == nil {
		return nil, nil
	}
	record, connected, err := store.LinkApp(ctx)
	if err != nil {
		return nil, err
	}
	spec := githubAppSpec{
		id: presetID("link", taken), purpose: appLink, origin: appPreset, declared: true,
		// The link App is used by being authorized as a person, never as
		// the App, so GitHub hands over a client secret and this service
		// keeps no App key for it. Nothing here can ask GitHub what it
		// holds, and an App whose declaration cannot be checked says so
		// rather than reporting a match nobody verified.
		keyless: "This service keeps no App key for the link App — it is used by being authorized, not as the App — so GitHub cannot be asked what it holds.",
		place:   c.placeOf(c.deps.LinkApp, appid.LinkID, []string{link.AppKey}),
	}
	if connected {
		spec.org, spec.entry = record.Owner, githubapp.LinkAppNamed(record.Owner, c.deps.LinkAppName)
		spec.created, spec.installed = true, true
		spec.appID, spec.appSlug, spec.htmlURL = record.AppID, record.AppSlug, record.HTMLURL
		spec.connectedAt, spec.connectedBy = record.ConnectedAt, record.ConnectedBy
	} else if name := c.deps.LinkAppName; name != "" {
		// Not created yet: the configured name is what it will be created as.
		spec.entry = catalogue.App{Name: name}
	}
	all, err := links.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].State == link.StateLinked {
			spec.linked++
		}
	}
	return []githubAppSpec{spec}, nil
}

// controllerAppSpecs is one App per organisation the policy binds, and
// one for every organisation an App was created for and the policy has
// since dropped.
func (c *Service) controllerAppSpecs(ctx context.Context, bound map[string]*binding, taken map[string]bool) ([]githubAppSpec, error) {
	records := map[string]connection.Record{}
	if c.deps.Orgs != nil {
		listed, err := c.deps.Orgs.List(ctx)
		if err != nil {
			return nil, err
		}
		for i := range listed {
			records[listed[i].Org] = listed[i]
		}
	}
	reported := map[string]bool{}
	if c.deps.Reports != nil {
		read, err := c.deps.Reports.Reports(ctx)
		if err != nil {
			return nil, err
		}
		for key := range read {
			if org, ok := status.OrgOfKey(key); ok {
				reported[org] = true
			}
		}
	}

	orgs := map[string]bool{}
	for org := range bound {
		orgs[org] = true
	}
	for org := range records {
		orgs[org] = true
	}
	var out []githubAppSpec
	for _, org := range slices.Sorted(maps.Keys(orgs)) {
		_, isBound := bound[org]
		record, created := records[org]
		spec := githubAppSpec{
			id: presetID(org+"-controller", taken), purpose: appController, origin: appPreset, org: org,
			declared: isBound, reported: reported[org], reports: c.deps.Reports != nil,
			place: c.placeOf(c.deps.Orgs, records[org].AppRef, []string{connection.Key(org)}),
		}
		if isBound {
			spec.entry = githubapp.OrganisationAppNamed(org, c.orgAppName(org))
		}
		if created {
			spec.created, spec.installed = true, record.Installed()
			spec.appID, spec.appSlug, spec.htmlURL, spec.installationID = record.AppID, record.AppSlug, record.HTMLURL, record.InstallationID
			spec.connectedAt, spec.connectedBy = record.ConnectedAt, record.ConnectedBy
			spec.key = c.organisationKey(ctx, org)
		}
		out = append(out, spec)
	}
	return out, nil
}

// organisationKey reads one organisation's App key, on a cache miss
// alone: a list of every App carries none.
func (c *Service) organisationKey(ctx context.Context, org string) func() string {
	return func() string {
		credential, found, err := c.deps.Orgs.Credential(ctx, org)
		if err != nil || !found {
			return ""
		}
		return credential.PrivateKey
	}
}

// runnerAppSpecs is one App per bound organisation per declared tier, and
// every App created for a tier or an organisation since dropped.
func (c *Service) runnerAppSpecs(ctx context.Context, bound map[string]*binding, taken map[string]bool) ([]githubAppSpec, error) {
	store := c.deps.RunnerApps
	if store == nil {
		return nil, nil
	}
	records, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	type pair struct{ org, tier string }
	wanted := map[pair]runnerapp.Record{}
	order := make([]pair, 0, len(records))
	for _, org := range slices.Sorted(maps.Keys(bound)) {
		for _, tier := range c.deps.RunnerTiers {
			key := pair{org, tier}
			if _, seen := wanted[key]; !seen {
				wanted[key], order = runnerapp.Record{}, append(order, key)
			}
		}
	}
	for i := range records {
		key := pair{records[i].Org, records[i].Tier}
		if _, seen := wanted[key]; !seen {
			order = append(order, key)
		}
		wanted[key] = records[i]
	}

	out := make([]githubAppSpec, 0, len(order))
	for _, key := range order {
		record := wanted[key]
		_, isBound := bound[key.org]
		declared := isBound && slices.Contains(c.deps.RunnerTiers, key.tier)
		spec := githubAppSpec{
			id: presetID(key.org+"-runners-"+key.tier, taken), purpose: appRunners, origin: appPreset,
			org: key.org, tier: key.tier, declared: declared,
			place: c.placeOf(store, appid.RunnerID(key.tier, key.org), runnerAppKeys(key.tier, key.org)),
		}
		if declared {
			spec.entry = githubapp.RunnerApp(key.org, key.tier)
		}
		if record.AppID != 0 {
			spec.created, spec.installed = true, record.Installed()
			spec.appID, spec.appSlug, spec.htmlURL, spec.installationID = record.AppID, record.AppSlug, record.HTMLURL, record.InstallationID
			spec.connectedAt, spec.connectedBy = record.ConnectedAt, record.ConnectedBy
			spec.key = func() string {
				key, found, err := store.PrivateKey(ctx, key.tier, key.org)
				if err != nil || !found {
					return ""
				}
				return key
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// catalogueAppSpecs is every App the catalogue declares, in declaration
// order, then every App created from an entry it no longer declares.
func (c *Service) catalogueAppSpecs(ctx context.Context, taken map[string]bool) ([]githubAppSpec, error) {
	store := c.deps.CatalogueApps
	if store == nil {
		return nil, nil
	}
	records, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]catalogueapp.Record{}
	for i := range records {
		byID[records[i].ID] = records[i]
	}
	var entries []catalogue.App
	if c.deps.Catalogue != nil {
		entries = c.deps.Catalogue.Apps
	}

	out := make([]githubAppSpec, 0, len(entries))
	for i := range entries {
		record, created := byID[entries[i].ID]
		spec := c.catalogueAppSpec(ctx, entries[i].ID, entries[i], record, created)
		spec.declared = true
		taken[spec.id] = true
		out = append(out, spec)
	}
	// An App an organisation's record names (`app_ref`, layout v5) is that
	// organisation's controller App: its row stands for it, and listing it
	// again as a leftover would invite disconnecting the organisation's App.
	named := map[string]bool{}
	if c.deps.Orgs != nil {
		orgs, err := c.deps.Orgs.List(ctx)
		if err != nil {
			return nil, err
		}
		for i := range orgs {
			if orgs[i].AppRef != "" {
				named[orgs[i].AppRef] = true
			}
		}
	}
	for i := range records {
		if _, declared := c.deps.Catalogue.Get(records[i].ID); declared || named[records[i].ID] {
			continue
		}
		spec := c.catalogueAppSpec(ctx, records[i].ID, catalogue.App{}, records[i], true)
		spec.keyless = "The catalogue no longer declares this App. Disconnect it, then delete it on GitHub."
		taken[spec.id] = true
		out = append(out, spec)
	}
	return out, nil
}

func (c *Service) catalogueAppSpec(
	ctx context.Context, id string, entry catalogue.App, record catalogueapp.Record, created bool,
) githubAppSpec {
	store := c.deps.CatalogueApps
	spec := githubAppSpec{
		id: id, purpose: appTokens, origin: appCatalogue, org: entry.Org, entry: entry,
		place: c.placeOf(store, id, catalogueAppKeys(id)),
	}
	if !created {
		return spec
	}
	spec.org = record.Org
	spec.created, spec.installed = true, record.Installed()
	spec.appID, spec.appSlug, spec.htmlURL, spec.installationID = record.AppID, record.AppSlug, record.HTMLURL, record.InstallationID
	spec.connectedAt, spec.connectedBy = record.ConnectedAt, record.ConnectedBy
	spec.webhookURL, spec.hookRotatedAt = record.WebhookURL, record.HookRotatedAt
	spec.key = func() string {
		_, key, _, _ := store.Get(ctx, id)
		return key
	}
	return spec
}

// The keys one App is kept under, in the order an operator reads them:
// the record, then the three properties a deployment copies.
func catalogueAppKeys(id string) []string {
	return []string{
		catalogueapp.RecordKey(id),
		catalogueapp.Key(id, catalogueapp.AppIDProperty),
		catalogueapp.Key(id, catalogueapp.InstallationIDProperty),
		catalogueapp.Key(id, catalogueapp.PrivateKeyProperty),
	}
}

func runnerAppKeys(tier, org string) []string {
	return []string{
		runnerapp.RecordKey(tier, org),
		runnerapp.Key(tier, org, runnerapp.AppIDProperty),
		runnerapp.Key(tier, org, runnerapp.InstallationIDProperty),
		runnerapp.Key(tier, org, runnerapp.PrivateKeyProperty),
	}
}

// githubAppView is one App as every page reads it: what the deployment
// declares, what the record says, and what GitHub answers — through the
// short cache unless this is a re-check.
func (c *Service) githubAppView(ctx context.Context, spec githubAppSpec, fresh bool) *directoryrosterv1.GitHubApp {
	out := &directoryrosterv1.GitHubApp{
		Id: spec.id, Org: spec.org, Purpose: spec.purpose, Tier: spec.tier, Origin: spec.origin,
		Name: spec.appSlug, Description: spec.entry.Description, Public: spec.entry.Public,
		Installation: spec.entry.InstallationScope(), State: appNotCreated, Declared: spec.declared,
		Events: slices.Clone(spec.entry.Events), SecretKeys: slices.Clone(spec.place.keys),
		LinkedAccounts: int32(spec.linked), //nolint:gosec // a count of people
	}
	if p := spec.place; p.adapter != "" {
		out.SecretPlace = &directoryrosterv1.SecretPlace{Adapter: p.adapter, Location: p.location}
		if p.adapter == adapterKubernetes {
			out.Secret = p.location
		}
	}
	if out.GetName() == "" {
		out.Name = spec.declaredName()
	}
	for _, grant := range spec.entry.Grants {
		out.Grants = append(out.Grants, &directoryrosterv1.GitHubAppGrant{
			Group: grant.Group, Repositories: slices.Clone(grant.Repositories), Permissions: maps.Clone(grant.Permissions),
			GroupDeclared: c.deps.Policy != nil && c.deps.Policy().HasGroup(grant.Group),
		})
	}
	if !spec.created {
		out.Permissions = permissionRows(spec.entry.Permissions, nil, nil)
		out.State = appNotCreated
		out.Attention, out.StateDetail = attentionOf(spec, appNotCreated, "")
		return out
	}

	out.AppId, out.AppSlug, out.InstallationId = spec.appID, spec.appSlug, spec.installationID
	out.HtmlUrl, out.ConnectedAt, out.ConnectedBy = spec.htmlURL, timestampOf(spec.connectedAt), spec.connectedBy
	out.SettingsUrl = appSettingsURL(spec.org, spec.appSlug)
	out.WebhookUrl, out.WebhookRotatedAt = spec.webhookURL, timestampOf(spec.hookRotatedAt)
	state := appCreated
	if spec.installed {
		state = appInstalled
	}

	// Nothing to ask with, or nothing to ask against: the App says why
	// rather than reporting a declaration nobody checked.
	if spec.key == nil || !spec.declared {
		out.Permissions = permissionRows(spec.entry.Permissions, nil, nil)
		out.Reason = spec.keyless
		out.State = state
		out.Attention, out.StateDetail = attentionOf(spec, state, out.GetReason())
		return out
	}

	seen, cached := c.seen.get(spec.id, spec.appID, spec.installationID)
	if fresh || !cached {
		seen = c.observe(ctx, spec.appID, spec.installationID, spec.installed, spec.entry.Webhook != nil, spec.key())
		c.seen.put(spec.id, seen)
	}
	out.CheckedAt, out.Reason = timestampOf(seen.at), seen.err
	var appPermissions, installationPermissions map[string]string
	if seen.app != nil {
		appPermissions = seen.app.Permissions
		if seen.app.HTMLURL != "" {
			out.HtmlUrl = seen.app.HTMLURL
		}
		out.Drift = append(out.Drift, appDrift(spec.entry, *seen.app, spec.declarer())...)
	}
	if seen.hook != nil {
		out.Drift = append(out.Drift, hookDrift(spec.entry, spec.webhookURL, *seen.hook)...)
	}
	switch {
	case seen.installationGone:
		out.Drift = append(out.Drift, "The installation is gone on GitHub: somebody uninstalled the App there. Disconnect it here, then create and install it again.")
	case seen.installation != nil:
		installationPermissions = seen.installation.Permissions
		out.RepositorySelection = seen.installation.RepositorySelection
		if seen.installation.Suspended {
			out.Drift = append(out.Drift, "The installation is suspended on GitHub: no token can be minted until an owner unsuspends it.")
		}
		if seen.app != nil && !samePermissions(seen.app.Permissions, installationPermissions) {
			out.Drift = append(out.Drift, "The installation's accepted permissions differ from the App's: "+
				"approve the permission request on GitHub, in the organisation's installed Apps.")
		}
	}
	out.Permissions = permissionRows(spec.entry.Permissions, appPermissions, installationPermissions)
	if len(out.GetDrift()) > 0 {
		state = appDrifted
	}
	out.State = state
	out.Attention, out.StateDetail = attentionOf(spec, state, out.GetReason())
	return out
}

// declarer is what to call whoever declared this App: the deployment's
// catalogue, or this service's own manifest builder.
func (s *githubAppSpec) declarer() string {
	if s.origin == appCatalogue {
		return "the catalogue"
	}
	return "this service"
}

// declaredName is the name the App is created with, for one not created
// yet. The link App has none until an operator says which organisation it
// is created under.
func (s *githubAppSpec) declaredName() string {
	switch {
	case s.appSlug != "":
		// Once an App exists its slug is its name: the owner may have renamed
		// it on GitHub's create page, and nothing declared can say so. For an
		// App nothing declares any more it is the only name there is.
		return s.appSlug
	case s.entry.Org != "":
		return s.entry.DisplayName()
	case s.purpose == appLink && s.entry.Name != "":
		return s.entry.Name
	case s.purpose == appLink:
		return "link App"
	default:
		return s.id
	}
}

// The four states in words, for a tooltip.
var appStateWords = map[directoryrosterv1.AppState]string{
	appNotCreated: "not created",
	appCreated:    "created, not installed",
	appInstalled:  "installed",
	appDrifted:    "differs on GitHub",
}

// attentionOf is who has to move next, and the exact state in words.
//
// Every App that is not finished needs an operator, with two exceptions
// that are nobody's fault and nothing to act on: a link App nobody has
// linked through yet, and a controller App installed before the
// controller's first pass.
func attentionOf(spec githubAppSpec, state directoryrosterv1.AppState, reason string) (directoryrosterv1.AppAttention, string) {
	attention, detail := appNeedsYou, appStateWords[state]
	switch {
	case !spec.declared:
		detail += "; " + spec.undeclaredWhy()
	case spec.purpose == appLink && state == appInstalled:
		attention, detail = appDone, linkAppInstalledState
		if spec.linked == 0 {
			attention, detail = appWaitingPerson, "created; nobody has linked an account through it yet"
		}
	case spec.purpose == appController && state == appInstalled && spec.reports && !spec.reported:
		attention, detail = appWaitingController, "installed; the controller has not reported on the organisation yet"
	case state == appInstalled:
		attention = appDone
	}
	if reason != "" {
		detail += ": " + reason
	}
	return attention, detail
}

// undeclaredWhy is why an App that exists is no longer wanted.
func (s *githubAppSpec) undeclaredWhy() string {
	switch s.purpose {
	case appController:
		return s.org + " is no longer bound in the policy"
	case appRunners:
		return "the " + s.tier + " tier in " + s.org + " is no longer declared"
	default:
		return "the catalogue no longer declares it"
	}
}

// githubCacheWindow is how long what GitHub said of an App is shown
// without asking again: long enough that a page left open does not spend
// the App's rate limit, short enough that an edit on GitHub shows soon.
const githubCacheWindow = time.Minute

// githubObservations remembers what GitHub last said of each App, by the
// id the Apps list gives it.
type githubObservations struct {
	mu   sync.Mutex
	seen map[string]githubObservation
	// clock is the time the window is measured by; nil is the wall clock.
	clock func() time.Time
}

func (o *githubObservations) now() time.Time {
	if o.clock != nil {
		return o.clock()
	}
	return time.Now()
}

// githubObservation is one answer from GitHub about one App, for the App
// and installation it was asked about.
type githubObservation struct {
	appID, installationID int64
	at                    time.Time
	app                   *githubapp.AppInfo
	installation          *githubapp.InstallationInfo
	installationGone      bool
	// hook is the App's webhook configuration, read only for an App whose
	// entry declares a webhook.
	hook *githubapp.HookConfig
	err  string
}

// get is what GitHub said of this App, if it was this App it was asked
// about and the answer is still fresh.
func (o *githubObservations) get(id string, appID, installationID int64) (githubObservation, bool) {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	seen, ok := o.seen[id]
	if !ok || seen.appID != appID || seen.installationID != installationID || now.Sub(seen.at) >= githubCacheWindow {
		return githubObservation{}, false
	}
	return seen, true
}

func (o *githubObservations) put(id string, seen githubObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = map[string]githubObservation{}
	}
	o.seen[id] = seen
}

func (o *githubObservations) forget(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.seen, id)
}

// observe asks GitHub, as the App, what the App and its installation hold
// now. An error is kept as the observation's reason: it never fails the
// page.
func (c *Service) observe(ctx context.Context, appID, installationID int64, installed, wantHook bool, key string) githubObservation {
	seen := githubObservation{appID: appID, installationID: installationID, at: c.seen.now().UTC()}
	if key == "" {
		seen.err = "The App's key is not kept here, so GitHub cannot be asked about it. Disconnect it and create it again."
		return seen
	}
	token, err := githubapp.AppToken(appID, key, time.Now())
	if err != nil {
		seen.err = "The App's stored key is not usable: " + err.Error()
		return seen
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	app, err := githubapp.GetApp(ctx, c.githubHTTP(), token)
	if err != nil {
		seen.err = "GitHub could not be asked about the App: " + err.Error()
		return seen
	}
	seen.app = &app
	if wantHook {
		hook, err := githubapp.GetHookConfig(ctx, c.githubHTTP(), token)
		if err != nil {
			seen.err = "GitHub could not be asked about the webhook: " + err.Error()
		} else {
			seen.hook = &hook
		}
	}
	if !installed {
		return seen
	}
	installation, err := githubapp.GetInstallation(ctx, c.githubHTTP(), token, installationID)
	switch {
	case errors.Is(err, githubapp.ErrInstallationGone):
		seen.installationGone = true
	case err != nil:
		seen.err = "GitHub could not be asked about the installation: " + err.Error()
	default:
		seen.installation = &installation
	}
	return seen
}

// impliedPermission is the one permission GitHub adds to an App on its
// own: every App may read repository metadata, declared or not.
const impliedPermission = "metadata"

// appDrift is every way the App on GitHub differs from its declaration.
// declarer is what to call whoever declared it: a catalogue App's
// declaration is the deployment's, a preset's is this service's own.
func appDrift(entry catalogue.App, app githubapp.AppInfo, declarer string) []string {
	var out []string
	names := map[string]bool{}
	for name := range entry.Permissions {
		names[name] = true
	}
	for name := range app.Permissions {
		names[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		declared, held := entry.Permissions[name], app.Permissions[name]
		switch {
		case declared == held:
		case declared == "" && name == impliedPermission && held == catalogue.LevelRead:
		case declared == "":
			out = append(out, fmt.Sprintf("The App holds %s: %s, which %s does not declare. Remove it in the App's settings on GitHub.", name, held, declarer))
		case held == "":
			out = append(out, fmt.Sprintf("The App lacks %s: %s. Add it in the App's settings on GitHub.", name, declared))
		default:
			out = append(out, fmt.Sprintf("The App holds %s: %s, and %s declares %s. Change it in the App's settings on GitHub.", name, held, declarer, declared))
		}
	}
	if len(entry.Events) > 0 {
		want, have := slices.Sorted(slices.Values(entry.Events)), slices.Sorted(slices.Values(app.Events))
		if !slices.Equal(want, have) {
			out = append(out, fmt.Sprintf("The App subscribes to %s, and %s declares %s. Change them in the App's settings on GitHub.",
				listOrNone(have), declarer, listOrNone(want)))
		}
	}
	return out
}

// hookDrift is every way the webhook on GitHub differs from what this
// service set. recorded is the URL it last set, which for a Kargo receiver
// is the only place the derived path is known.
func hookDrift(entry catalogue.App, recorded string, hook githubapp.HookConfig) []string {
	if entry.Webhook == nil {
		return nil
	}
	var out []string
	want := entry.Webhook.URL
	if entry.Webhook.Kargo != nil {
		want = recorded
		if recorded != "" && !strings.HasPrefix(recorded, strings.TrimRight(entry.Webhook.Kargo.Base, "/")+"/github/") {
			out = append(out, "The webhook was set for another Kargo base than the catalogue declares now. Rotate its secret to move it.")
		}
	}
	switch {
	case want == "":
		out = append(out, "This service has not set the webhook's URL and secret. Rotate the secret to set them.")
	case hook.URL != want && entry.Webhook.Kargo != nil:
		out = append(out, "The webhook delivers to a URL other than the one this service set. Rotate the secret to set it again.")
	case hook.URL != want:
		out = append(out, fmt.Sprintf("The webhook delivers to %s, and the catalogue declares %s. Rotate the secret to move it.", hook.URL, want))
	}
	if hook.ContentType != githubapp.ContentTypeJSON {
		out = append(out, fmt.Sprintf("The webhook delivers as %q, not %q. Rotate the secret to set it again.", hook.ContentType, githubapp.ContentTypeJSON))
	}
	if hook.Secret == "" {
		out = append(out, "The webhook has no secret, so deliveries are unsigned. Rotate the secret to set one.")
	}
	return out
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "no events"
	}
	return strings.Join(items, ", ")
}

// samePermissions compares two permission sets, treating GitHub's implied
// metadata read as present on both sides when either lacks it.
func samePermissions(a, b map[string]string) bool {
	normal := func(m map[string]string) map[string]string {
		out := maps.Clone(m)
		if out == nil {
			out = map[string]string{}
		}
		if out[impliedPermission] == catalogue.LevelRead {
			delete(out, impliedPermission)
		}
		return out
	}
	return maps.Equal(normal(a), normal(b))
}

// permissionRows is the permission table: every name declared or held,
// sorted.
func permissionRows(declared, app, installation map[string]string) []*directoryrosterv1.GitHubAppPermission {
	names := map[string]bool{}
	for _, m := range []map[string]string{declared, app, installation} {
		for name := range m {
			names[name] = true
		}
	}
	out := make([]*directoryrosterv1.GitHubAppPermission, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		out = append(out, &directoryrosterv1.GitHubAppPermission{
			Name: name, Declared: declared[name], App: app[name], Installation: installation[name],
		})
	}
	return out
}

func (c *Service) catalogueStore() (catalogueapp.Store, error) {
	if c.deps.CatalogueApps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment keeps no state in Kubernetes, so a catalogue App's key would not survive a restart")
	}
	return c.deps.CatalogueApps, nil
}
