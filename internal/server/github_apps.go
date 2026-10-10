// The GitHub Apps handler: every kind of App — the link App, an
// organisation's controller App, a tier's runner App and a catalogue App —
// is one App with a purpose, listed, read, checked, created, installed and
// disconnected by the calls below. The per-kind calls that came before
// (GetGitHubStatus's catalogue and runner sections, the catalogue and runner
// Begin, Disconnect and Check) are served from the same code, so a client
// written for them reads what it always read.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/storage/logattr"
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

	// secret and secretKeys are where the App's key is kept, for a
	// deployment copying it.
	secret     string
	secretKeys []string

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
// keys in. A deployment keeping no state in Kubernetes has none, and the
// App then names no Secret rather than naming one that does not exist.
type secretNamer interface{ SecretName() string }

func secretNameOf(store any) string {
	if named, ok := store.(secretNamer); ok {
		return named.SecretName()
	}
	return ""
}

// ListGitHubApps implements the Apps list: every App in one shape.
func (c *Console) ListGitHubApps(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListGitHubAppsRequest],
) (*connect.Response[directoryrosterv1.ListGitHubAppsResponse], error) {
	id, err := c.requireAnyOrg(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	specs, facts, err := c.githubAppSpecs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
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
	return connect.NewResponse(&directoryrosterv1.ListGitHubAppsResponse{
		OwnerChoices: choices, MayConnectWithoutOwner: mayNone,
		Apps:                views,
		ConnectingAvailable: facts.connecting,
		LinkingAvailable:    facts.linking,
		CatalogueAvailable:  facts.catalogue,
		RunnerTiers:         facts.tiers,
		BoundOrganisations:  facts.bound,
		LinkUrl:             facts.linkURL,
	}), nil
}

// decorateApp sets what an App's view says of the caller and of the
// organisation's owner: whether it may operate the App, which directory owns
// the organisation (by its primary domain), and whether it may change that.
func (c *Console) decorateApp(app *directoryrosterv1.GitHubApp, id access.Identity, spec *githubAppSpec, domains map[string]string) {
	app.CanOperate = mayApp(id, access.RoleOperator, spec)
	if spec.purpose == appLink {
		return
	}
	app.OwnerDirectory, app.OwnerDomain = spec.owner, domains[spec.owner]
	app.CanChangeOwner = spec.purpose == appController && spec.created && id.Can(access.RoleOperator)
}

// GetGitHubApp implements one App's page.
func (c *Console) GetGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetGitHubAppRequest],
) (*connect.Response[directoryrosterv1.GetGitHubAppResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
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
	return connect.NewResponse(&directoryrosterv1.GetGitHubAppResponse{App: app}), nil
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
func (c *Console) ListGitHubAppTokens(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListGitHubAppTokensRequest],
) (*connect.Response[directoryrosterv1.ListGitHubAppTokensResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	// The App first, so an id nothing declares is not_found rather than an
	// empty list, and a store that cannot be read says so rather than
	// reporting that nothing has been asked for.
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
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
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s mints no installation tokens: only an App the catalogue declares does", spec.id))
	case c.deps.GitHubMints == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"this deployment mints no installation tokens here, so it keeps none to show"))
	}
	out := &directoryrosterv1.ListGitHubAppTokensResponse{
		Kept:      int32(c.deps.GitHubMints.PerAppKept()), //nolint:gosec // a page's worth
		KeptSince: timestampOf(c.deps.GitHubMints.Since()),
	}
	recent := c.deps.GitHubMints.Recent(spec.id)
	for i := range recent {
		out.Tokens = append(out.Tokens, &directoryrosterv1.GitHubAppToken{
			At: timestampOf(recent[i].At), Subject: recent[i].Subject, Proof: recent[i].Proof, Grant: recent[i].Grant,
			Repositories: slices.Clone(recent[i].Repositories), Permissions: recent[i].Permissions,
			Outcome: recent[i].Outcome, Reason: recent[i].Reason,
		})
	}
	return connect.NewResponse(out), nil
}

// CheckGitHubApp asks GitHub again about one App, whichever kind it is.
func (c *Console) CheckGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CheckGitHubAppRequest],
) (*connect.Response[directoryrosterv1.CheckGitHubAppResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
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
	return connect.NewResponse(&directoryrosterv1.CheckGitHubAppResponse{App: app}), nil
}

// BeginGitHubAppConnect starts creating any App, or finishing installing
// one created before.
//
// It hands the work to the same code the per-kind calls run, so the
// signed state, the cookie it is pinned to and the callback that finishes
// the flow are the ones that kind of App has always used: a browser
// part-way through a flow is unaffected by which call started it.
func (c *Console) BeginGitHubAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubAppConnectResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id, err := c.requireApp(ctx, access.RoleOperator, &spec)
	if err != nil {
		return nil, err
	}
	var begun githubBegin
	switch spec.purpose {
	case appLink:
		begun, err = c.beginLinkAppConnect(ctx, id.Who(), strings.TrimSpace(req.Msg.GetOwner()))
	case appController:
		begun, err = c.beginOrganisationConnect(ctx, id, spec.org, req.Msg.GetOwnerDirectory())
	case appRunners:
		begun, err = c.beginRunnerAppConnect(ctx, id.Who(), spec.org, spec.tier)
	default:
		begun, err = c.beginCatalogueAppConnect(ctx, id.Who(), spec.id)
	}
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// DisconnectGitHubApp uninstalls any App, then forgets it.
func (c *Console) DisconnectGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubAppResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if _, err = c.requireApp(ctx, access.RoleOperator, &spec); err != nil {
		return nil, err
	}
	var gone githubDisconnect
	switch spec.purpose {
	case appLink:
		gone, err = c.disconnectLinkApp(ctx)
	case appController:
		gone, err = c.disconnectOrganisation(ctx, spec.org)
	case appRunners:
		gone, err = c.disconnectRunnerApp(ctx, spec.org, spec.tier)
	default:
		gone, err = c.disconnectCatalogueApp(ctx, spec.id)
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubAppResponse{
		Uninstalled:    gone.uninstalled,
		Detail:         gone.detail,
		AppSettingsUrl: gone.settingsURL,
		Invalidated:    int32(gone.invalidated), //nolint:gosec // a count of people
	}), nil
}

// githubAppSpec finds one App by the id the list gives it.
func (c *Console) githubAppSpec(ctx context.Context, id string) (githubAppSpec, error) {
	id = strings.TrimSpace(id)
	specs, _, err := c.githubAppSpecs(ctx)
	if err != nil {
		return githubAppSpec{}, connect.NewError(connect.CodeUnavailable, err)
	}
	for i := range specs {
		if specs[i].id == id {
			return specs[i], nil
		}
	}
	return githubAppSpec{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no App %q is declared or created here", id))
}

// githubAppViews renders every spec, asking GitHub about each in
// parallel: one App's slow answer must not add to another's. Always
// through the cache — a list of Apps is a page load, and only a re-check
// of one App spends a round trip per App.
func (c *Console) githubAppViews(ctx context.Context, specs []githubAppSpec) []*directoryrosterv1.GitHubApp {
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
func (c *Console) githubAppSpecs(ctx context.Context) ([]githubAppSpec, githubAppsFacts, error) {
	facts := githubAppsFacts{
		connecting: c.deps.GitHubOrgs != nil,
		linking:    c.deps.GitHubLinkApp != nil && c.deps.GitHubLinks != nil,
		catalogue:  c.deps.GitHubCatalogueApps != nil,
	}
	if facts.linking {
		facts.linkURL = c.githubRoot() + githubLinkPath
	}
	if c.deps.GitHubRunnerApps != nil {
		facts.tiers = slices.Clone(c.deps.GitHubRunnerTiers)
	}
	bound := boundOrganisations(c.deps.Authorizer.Policy())
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
func (c *Console) linkAppSpecs(ctx context.Context, taken map[string]bool) ([]githubAppSpec, error) {
	store, links := c.deps.GitHubLinkApp, c.deps.GitHubLinks
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
		secret:  secretNameOf(c.deps.GitHubLinkApp), secretKeys: []string{link.AppKey},
	}
	if connected {
		spec.org, spec.entry = record.Owner, githubapp.LinkApp(record.Owner)
		spec.created, spec.installed = true, true
		spec.appID, spec.appSlug, spec.htmlURL = record.AppID, record.AppSlug, record.HTMLURL
		spec.connectedAt, spec.connectedBy = record.ConnectedAt, record.ConnectedBy
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
func (c *Console) controllerAppSpecs(ctx context.Context, bound map[string]*binding, taken map[string]bool) ([]githubAppSpec, error) {
	records := map[string]connection.Record{}
	if c.deps.GitHubOrgs != nil {
		listed, err := c.deps.GitHubOrgs.List(ctx)
		if err != nil {
			return nil, err
		}
		for i := range listed {
			records[listed[i].Org] = listed[i]
		}
	}
	reported := map[string]bool{}
	if c.deps.GitHub != nil {
		read, err := c.deps.GitHub.Reports(ctx)
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
			declared: isBound, reported: reported[org], reports: c.deps.GitHub != nil,
			secret: secretNameOf(c.deps.GitHubOrgs), secretKeys: []string{connection.Key(org)},
		}
		if isBound {
			spec.entry = githubapp.OrganisationApp(org)
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
func (c *Console) organisationKey(ctx context.Context, org string) func() string {
	return func() string {
		credential, found, err := c.deps.GitHubOrgs.Credential(ctx, org)
		if err != nil || !found {
			return ""
		}
		return credential.PrivateKey
	}
}

// runnerAppSpecs is one App per bound organisation per declared tier, and
// every App created for a tier or an organisation since dropped.
func (c *Console) runnerAppSpecs(ctx context.Context, bound map[string]*binding, taken map[string]bool) ([]githubAppSpec, error) {
	store := c.deps.GitHubRunnerApps
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
		for _, tier := range c.deps.GitHubRunnerTiers {
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
		declared := isBound && slices.Contains(c.deps.GitHubRunnerTiers, key.tier)
		spec := githubAppSpec{
			id: presetID(key.org+"-runners-"+key.tier, taken), purpose: appRunners, origin: appPreset,
			org: key.org, tier: key.tier, declared: declared,
			secret: secretNameOf(store), secretKeys: runnerAppKeys(key.tier, key.org),
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
func (c *Console) catalogueAppSpecs(ctx context.Context, taken map[string]bool) ([]githubAppSpec, error) {
	store := c.deps.GitHubCatalogueApps
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
	if c.deps.GitHubCatalogue != nil {
		entries = c.deps.GitHubCatalogue.Apps
	}

	out := make([]githubAppSpec, 0, len(entries))
	for i := range entries {
		record, created := byID[entries[i].ID]
		spec := c.catalogueAppSpec(ctx, entries[i].ID, entries[i], record, created)
		spec.declared = true
		taken[spec.id] = true
		out = append(out, spec)
	}
	for i := range records {
		if _, declared := c.deps.GitHubCatalogue.Get(records[i].ID); declared {
			continue
		}
		spec := c.catalogueAppSpec(ctx, records[i].ID, catalogue.App{}, records[i], true)
		spec.keyless = "The catalogue no longer declares this App. Disconnect it, then delete it on GitHub."
		taken[spec.id] = true
		out = append(out, spec)
	}
	return out, nil
}

func (c *Console) catalogueAppSpec(
	ctx context.Context, id string, entry catalogue.App, record catalogueapp.Record, created bool,
) githubAppSpec {
	store := c.deps.GitHubCatalogueApps
	spec := githubAppSpec{
		id: id, purpose: appTokens, origin: appCatalogue, org: entry.Org, entry: entry,
		secret: secretNameOf(store), secretKeys: catalogueAppKeys(id),
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
func (c *Console) githubAppView(ctx context.Context, spec githubAppSpec, fresh bool) *directoryrosterv1.GitHubApp {
	out := &directoryrosterv1.GitHubApp{
		Id: spec.id, Org: spec.org, Purpose: spec.purpose, Tier: spec.tier, Origin: spec.origin,
		Name: spec.appSlug, Description: spec.entry.Description, Public: spec.entry.Public,
		Installation: spec.entry.InstallationScope(), State: appNotCreated, Declared: spec.declared,
		Events: slices.Clone(spec.entry.Events), Secret: spec.secret, SecretKeys: slices.Clone(spec.secretKeys),
		LinkedAccounts: int32(spec.linked), //nolint:gosec // a count of people
	}
	if out.GetName() == "" {
		out.Name = spec.declaredName()
	}
	for _, grant := range spec.entry.Grants {
		out.Grants = append(out.Grants, &directoryrosterv1.GitHubAppGrant{
			Group: grant.Group, Repositories: slices.Clone(grant.Repositories), Permissions: maps.Clone(grant.Permissions),
			GroupDeclared: c.deps.Authorizer != nil && c.deps.Authorizer.Policy().HasGroup(grant.Group),
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

	seen, cached := c.githubSeen.get(spec.id, spec.appID, spec.installationID)
	if fresh || !cached {
		seen = c.observe(ctx, spec.appID, spec.installationID, spec.installed, spec.entry.Webhook != nil, spec.key())
		c.githubSeen.put(spec.id, seen)
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
	case s.entry.Org != "":
		return s.entry.DisplayName()
	case s.appSlug != "":
		// Nothing declares it any more: what it was created as is the only
		// name there is.
		return s.appSlug
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

// githubGroupGrants is the reverse of an App's grants: which Apps each
// internal group may mint tokens of, from the catalogue alone.
//
// From the catalogue and nothing else, on purpose. A group's page is read
// far more often than the Apps list, and what it needs — the App, its
// repositories and the most a token may carry — is declared here; asking
// GitHub about every App to colour a chip would put a network call on
// every read of the policy.
func (c *Console) githubGroupGrants() map[string][]*directoryrosterv1.GitHubGroupGrant {
	out := map[string][]*directoryrosterv1.GitHubGroupGrant{}
	if c.deps.GitHubCatalogue == nil {
		return out
	}
	for i := range c.deps.GitHubCatalogue.Apps {
		app := &c.deps.GitHubCatalogue.Apps[i]
		for _, grant := range app.Grants {
			row := &directoryrosterv1.GitHubGroupGrant{
				AppId: app.ID, AppName: app.DisplayName(), Org: app.Org,
				Repositories: slices.Clone(grant.Repositories), Permissions: maps.Clone(grant.Permissions),
			}
			// When the grant was last used, where this service still
			// remembers. It comes from the ring beside it — no store is
			// read and no call is made to GitHub — so it stays as cheap as
			// the rest of a group's page, and it is absent rather than
			// "never" where nothing is remembered.
			if at, minted := c.deps.GitHubMints.LastMinted(app.ID, grant.Group); minted {
				row.LastMinted = timestampOf(at)
			}
			out[grant.Group] = append(out[grant.Group], row)
		}
	}
	return out
}

// githubBegin is what starting any connect returns: where the browser
// goes, the manifest it POSTs there when the App is still to be created,
// and the signed state both are pinned to.
type githubBegin struct{ url, manifest, state string }

// githubDisconnect is what disconnecting any App returns.
type githubDisconnect struct {
	uninstalled bool
	detail      string
	settingsURL string
	// invalidated is how many links became unverifiable. The link App
	// alone.
	invalidated int
}

// pinFlow pins a flow to this browser for as long as the two clicks have.
func (c *Console) pinFlow(header http.Header, state string) {
	header.Add("Set-Cookie", access.ConnectCookie(state, c.deps.SecureCookie, githubFlowWindow).String())
}

// uninstall is the revoke every Disconnect makes: GitHub's API cannot
// delete an App, so the registration stays for its owner to delete and
// nothing can act through it once the installation is gone.
func (c *Console) uninstall(ctx context.Context, appID, installationID int64, key string) githubDisconnect {
	var out githubDisconnect
	if key == "" || installationID == 0 {
		out.detail = "The App was never installed, so there was nothing to uninstall."
		return out
	}
	token, err := githubapp.AppToken(appID, key, time.Now())
	if err == nil {
		err = githubapp.DeleteInstallation(ctx, c.githubHTTP(), token, installationID)
	}
	if err != nil {
		out.detail = "The App could not be uninstalled, so uninstall it on GitHub: " + err.Error()
		return out
	}
	out.uninstalled = true
	return out
}

// GitHubCatalogueApps is where catalogue Apps are kept: every App's record
// and key, by its catalogue id.
type GitHubCatalogueApps interface {
	Put(ctx context.Context, record catalogueapp.Record, privateKey string) error
	List(ctx context.Context) ([]catalogueapp.Record, error)
	// Get is one App's record and its key, installed or pending.
	Get(ctx context.Context, id string) (catalogueapp.Record, string, bool, error)
	Delete(ctx context.Context, id string) error
	// PutWebhookSecret keeps the secret GitHub signs one App's webhook with,
	// beside its key and surviving every later Put of the App. The App must
	// have been Put. Where the App is exported the secret is in its document
	// (`webhook_secret`), which is where a consumer reads it.
	PutWebhookSecret(ctx context.Context, id, secret string) error
	// WebhookSecret reads it back; false is an App with none.
	WebhookSecret(ctx context.Context, id string) (string, bool, error)
}

// Where GitHub sends the browser back to while a catalogue App is created:
// after Create, and after Install.
const (
	githubCatalogueCallbackPath = "/connect/github/catalogue/callback"
	githubCatalogueSetupPath    = "/connect/github/catalogue/setup"
)

// githubCatalogueBind prefixes the App's id in a catalogue flow's signed
// state, so no other GitHub flow's state can finish it, and the other way
// round.
const githubCatalogueBind = "github-catalogue:"

// The states a catalogue App is shown in.
const (
	catalogueNotCreated = "not_created"
	catalogueCreated    = "created"
	catalogueInstalled  = "installed"
	catalogueDrifted    = "drifted"
)

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

// catalogueStore refuses where the deployment keeps no catalogue Apps.
func (c *Console) catalogueStore() (GitHubCatalogueApps, error) {
	if c.deps.GitHubCatalogueApps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment keeps no state in Kubernetes, so a catalogue App's key would not survive a restart"))
	}
	return c.deps.GitHubCatalogueApps, nil
}

// requireCatalogueApp is [Console.requireOrg] for a catalogue App, by the
// organisation its entry declares — or, for one created from an entry since
// dropped, the organisation it was created under. An id nothing names is
// the installation-wide operator's to be told so: nobody owns it.
func (c *Console) requireCatalogueApp(ctx context.Context, want access.Role, id string) (access.Identity, error) {
	org := ""
	if c.deps.GitHubCatalogue != nil {
		if entry, declared := c.deps.GitHubCatalogue.Get(id); declared {
			org = entry.Org
		}
	}
	if org == "" && c.deps.GitHubCatalogueApps != nil {
		if record, _, created, err := c.deps.GitHubCatalogueApps.Get(ctx, id); err == nil && created {
			org = record.Org
		}
	}
	return c.requireOrg(ctx, want, org)
}

// BeginGitHubCatalogueAppConnect starts creating a catalogue App, or
// finishing installing one created before.
func (c *Console) BeginGitHubCatalogueAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubCatalogueAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubCatalogueAppConnectResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	who, err := c.requireCatalogueApp(ctx, access.RoleOperator, id)
	if err != nil {
		return nil, err
	}
	begun, err := c.beginCatalogueAppConnect(ctx, who.Who(), id)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubCatalogueAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// beginCatalogueAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Console) beginCatalogueAppConnect(ctx context.Context, actor, id string) (githubBegin, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubBegin{}, err
	}
	entry, declared := c.deps.GitHubCatalogue.Get(id)
	if !declared {
		return githubBegin{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the catalogue declares no App %q", id))
	}
	existing, _, created, err := store.Get(ctx, id)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s is already created and installed as %s: disconnect it first, or a second App would sit beside the first", id, existing.AppSlug))
	}

	state, err := c.deps.State.IssueAs(access.Binding{Bind: githubCatalogueBind + id, Actor: actor})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out := githubBegin{state: state}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot()
	manifest, err := json.Marshal(githubapp.ManifestFor(entry, c.deps.PublicURL, root+githubCatalogueCallbackPath, root+githubCatalogueSetupPath, nil))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out.url, out.manifest = githubapp.CreateURL(entry.Org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubCatalogueApp uninstalls a catalogue App, then forgets
// it. A failed uninstall still forgets, and says what is left to do by
// hand. The App stays on GitHub: the API cannot delete one.
func (c *Console) DisconnectGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubCatalogueAppResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	gone, err := c.disconnectCatalogueApp(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubCatalogueAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}), nil
}

// disconnectCatalogueApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Console) disconnectCatalogueApp(ctx context.Context, id string) (githubDisconnect, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubDisconnect{}, err
	}
	if !catalogue.ValidID(id) {
		return githubDisconnect{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a catalogue id", id))
	}
	record, key, created, err := store.Get(ctx, id)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if !created {
		return githubDisconnect{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no App %s has been created", id))
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(record.Org, record.AppSlug)
	if err = store.Delete(ctx, id); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	c.githubSeen.forget(id)
	c.record(ctx, audit.CatalogueAppDisconnected(actorOf(ctx), record.Org,
		audit.App{Name: id, ID: record.AppID, Slug: record.AppSlug}, out.uninstalled, out.detail))
	return out, nil
}

// CheckGitHubCatalogueApp asks GitHub again about one App.
func (c *Console) CheckGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CheckGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.CheckGitHubCatalogueAppResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	if _, err := c.catalogueStore(); err != nil {
		return nil, err
	}
	specs, err := c.catalogueAppSpecs(ctx, map[string]bool{})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	for i := range specs {
		if specs[i].id != id {
			continue
		}
		app := legacyCatalogueApp(specs[i], c.githubAppView(ctx, specs[i], true))
		return connect.NewResponse(&directoryrosterv1.CheckGitHubCatalogueAppResponse{App: app}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("the catalogue declares no App %q and none was created", id))
}

// catalogueStatus adds every declared App and every App created from an
// entry no longer declared, never with a key.
//
// The Apps themselves are [Console.githubAppView]'s, turned back into the
// shape GetGitHubStatus has always carried: one reading of an App, two
// ways of saying it, for as long as both calls are served.
func (c *Console) catalogueStatus(ctx context.Context, out *directoryrosterv1.GetGitHubStatusResponse) error {
	if c.deps.GitHubCatalogueApps == nil {
		return nil
	}
	out.CatalogueAvailable = true
	specs, err := c.catalogueAppSpecs(ctx, map[string]bool{})
	if err != nil {
		return err
	}
	views := c.githubAppViews(ctx, specs)
	for i := range specs {
		out.CatalogueApps = append(out.CatalogueApps, legacyCatalogueApp(specs[i], views[i]))
	}
	return nil
}

// legacyCatalogueApp is one App in the shape GetGitHubStatus and
// CheckGitHubCatalogueApp answer in.
func legacyCatalogueApp(spec githubAppSpec, app *directoryrosterv1.GitHubApp) *directoryrosterv1.GitHubCatalogueApp {
	return &directoryrosterv1.GitHubCatalogueApp{
		Id: app.GetId(), Org: app.GetOrg(), Name: spec.declaredName(), Description: app.GetDescription(), Public: app.GetPublic(),
		Installation: app.GetInstallation(), Permissions: app.GetPermissions(), Events: app.GetEvents(), Grants: app.GetGrants(),
		State: legacyState(app.GetState()), HtmlUrl: app.GetHtmlUrl(), Drift: app.GetDrift(), Reason: app.GetReason(),
		AppId: app.GetAppId(), AppSlug: app.GetAppSlug(), InstallationId: app.GetInstallationId(),
		ConnectedAt: app.GetConnectedAt(), ConnectedBy: app.GetConnectedBy(), CheckedAt: app.GetCheckedAt(),
		RepositorySelection: app.GetRepositorySelection(), Declared: app.GetDeclared(),
	}
}

func legacyState(state directoryrosterv1.AppState) string {
	switch state {
	case appCreated:
		return catalogueCreated
	case appInstalled:
		return catalogueInstalled
	case appDrifted:
		return catalogueDrifted
	default:
		return catalogueNotCreated
	}
}

// observe asks GitHub, as the App, what the App and its installation hold
// now. An error is kept as the observation's reason: it never fails the
// page.
func (c *Console) observe(ctx context.Context, appID, installationID int64, installed, wantHook bool, key string) githubObservation {
	seen := githubObservation{appID: appID, installationID: installationID, at: c.githubSeen.now().UTC()}
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

// appSettingsURL is where an organisation's owner edits or deletes an App.
func appSettingsURL(org, slug string) string {
	if org == "" || slug == "" {
		return ""
	}
	return githubapp.WebBase + "/organizations/" + url.PathEscape(org) + "/settings/apps/" + url.PathEscape(slug)
}

// githubCatalogueFlow checks a catalogue flow's redirect and names its
// entry.
func (s *ConsoleServer) githubCatalogueFlow(w http.ResponseWriter, r *http.Request) (catalogue.App, string, bool) {
	bind, actor, _, ok := s.githubBound(w, r)
	if !ok {
		return catalogue.App{}, "", false
	}
	id, isCatalogue := strings.CutPrefix(bind, githubCatalogueBind)
	if !isCatalogue || !catalogue.ValidID(id) {
		s.githubProblem(w, r, http.StatusBadRequest, "This is not a catalogue App's connect.", "", nil)
		return catalogue.App{}, "", false
	}
	entry, declared := s.console.deps.GitHubCatalogue.Get(id)
	if s.console.deps.GitHubCatalogueApps == nil || !declared {
		s.githubProblem(w, r, http.StatusConflict, fmt.Sprintf("This deployment's catalogue declares no App %s.", id), "", nil)
		return catalogue.App{}, "", false
	}
	return entry, actor, true
}

// githubCatalogueCallback is where GitHub sends the owner after creating a
// catalogue App, with a one-time code for its key. The key is kept and the
// owner is sent straight on to Install.
func (s *ConsoleServer) githubCatalogueCallback(w http.ResponseWriter, r *http.Request) {
	entry, actor, ok := s.githubCatalogueFlow(w, r)
	if !ok {
		return
	}
	registration, err := githubapp.Convert(r.Context(), s.console.githubHTTP(), r.URL.Query().Get("code"))
	if err != nil {
		s.log.WarnContext(r.Context(), "a catalogue App was created and its key could not be collected",
			slog.String("id", entry.ID), slog.String("org", entry.Org), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and then would not hand over its key.", err.Error(), []string{
				"The page was reloaded: the code GitHub returns can be exchanged once.",
				"More than an hour passed between creating the App and returning here.",
				"This service cannot reach api.github.com: the cluster's egress policy has to allow it.",
			})
		return
	}
	if !strings.EqualFold(registration.Owner, entry.Org) {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("The App was created under %s, and the catalogue declares it under %s. Delete it on GitHub and create it again.",
				registration.Owner, entry.Org), "", nil)
		return
	}
	record := catalogueapp.Record{
		ID: entry.ID, Org: entry.Org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	// A declared webhook gets a secret this service makes, never the one the
	// conversion returns: it is generated before anything is kept, so a
	// failure to generate leaves the App as it was.
	var hookSecret string
	if entry.Webhook != nil {
		if hookSecret, err = clientcreds.Generate(); err != nil {
			s.githubProblem(w, r, http.StatusConflict,
				"GitHub created the App, and a webhook secret could not be generated. Delete it on GitHub and create it again.", err.Error(), nil)
			return
		}
	}
	if err = s.console.deps.GitHubCatalogueApps.Put(r.Context(), record, registration.PEM); err != nil {
		s.log.ErrorContext(r.Context(), "a catalogue App was created and could not be kept", slog.String("id", entry.ID), slog.String("org", entry.Org),
			logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error(), nil)
		return
	}
	s.console.githubSeen.forget(entry.ID)
	s.log.InfoContext(r.Context(), "catalogue App created", slog.String("id", entry.ID), slog.String("org", entry.Org), slog.Int64("app", registration.ID),
		slog.String("slug", registration.Slug), logattr.SafeString("by", actor))
	app := audit.App{Name: entry.ID, ID: registration.ID, Slug: registration.Slug}
	s.console.record(r.Context(), audit.CatalogueAppCreated(audit.Identified(actor), entry.Org, app))
	if hookSecret != "" && !s.configureWebhook(w, r, entry, actor, record, registration.PEM, hookSecret, app) {
		return
	}

	state, err := s.state.IssueAs(access.Binding{Bind: githubCatalogueBind + entry.ID, Actor: actor})
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App was created; start Create again to install it.", err.Error(), nil)
		return
	}
	http.SetCookie(w, access.ConnectCookie(state, s.sessions.Secure(), githubFlowWindow))
	http.Redirect(w, r, githubapp.InstallURL(registration.Slug, state), http.StatusFound)
}

// configureWebhook gives a new App its webhook: the secret is kept first, so
// a failure after GitHub has it never leaves GitHub holding a secret nobody
// has, then GitHub is told it and the URL in one call. It says why on the
// page and returns false when it could not; the App then exists with no
// working webhook, which a rotation finishes.
func (s *ConsoleServer) configureWebhook(
	w http.ResponseWriter, r *http.Request, entry catalogue.App, actor string,
	record catalogueapp.Record, key, secret string, app audit.App,
) bool {
	ctx, store, who := r.Context(), s.console.deps.GitHubCatalogueApps, audit.Identified(actor)
	fail := func(step, summary string, err error) bool {
		s.log.WarnContext(ctx, "a catalogue App's webhook could not be set", slog.String("id", entry.ID), slog.String("org", entry.Org),
			slog.String("step", step), logattr.SafeError("error", err))
		s.console.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, step, audit.Failed(err.Error())))
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), []string{
			"The App exists and its key is kept. Rotate its webhook secret from the console once it is installed to finish.",
		})
		return false
	}
	if err := store.PutWebhookSecret(ctx, entry.ID, secret); err != nil {
		return fail(audit.WebhookStaged, "GitHub created the App, and its webhook secret could not be kept here.", err)
	}
	target := entry.Webhook.Target(secret)
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	if _, err = githubapp.PatchHookConfig(ctx, s.console.githubHTTP(), token, githubapp.HookUpdate{URL: target, Secret: secret}); err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	record.WebhookURL, record.HookRotatedAt = target, time.Now().UTC()
	if err = store.Put(ctx, record, key); err != nil {
		return fail(audit.WebhookConfigured, "The App's webhook was set, and could not be recorded here.", err)
	}
	s.console.githubSeen.forget(entry.ID)
	s.console.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, audit.WebhookConfigured, audit.Succeeded()))
	return true
}

// githubCatalogueSetup is where GitHub sends the owner after installing a
// catalogue App. What is kept is where GitHub says the App is installed,
// asked as the App.
func (s *ConsoleServer) githubCatalogueSetup(w http.ResponseWriter, r *http.Request) {
	entry, actor, ok := s.githubCatalogueFlow(w, r)
	if !ok {
		return
	}
	http.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	store := s.console.deps.GitHubCatalogueApps
	record, key, created, err := store.Get(r.Context(), entry.ID)
	if err != nil || !created || key == "" {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("There is no App %s to finish installing. Start Create again.", entry.ID), errString(err), nil)
		return
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error(), nil)
		return
	}
	installation, err := githubapp.FindInstallation(r.Context(), s.console.githubHTTP(), token, record.Org)
	if err != nil {
		summary := "GitHub could not say where the App is installed."
		if errors.Is(err, githubapp.ErrNotInstalled) {
			summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", record.Org)
		}
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), nil)
		return
	}
	record.InstallationID = installation
	if err = store.Put(r.Context(), record, key); err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App is installed and could not be recorded here.", err.Error(), nil)
		return
	}
	s.console.githubSeen.forget(entry.ID)
	s.log.InfoContext(r.Context(), "catalogue App installed", slog.String("id", entry.ID), slog.String("org", record.Org),
		slog.Int64("installation", installation),
		logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.CatalogueAppInstalled(audit.Identified(actor), record.Org,
		audit.App{Name: entry.ID, ID: record.AppID, Slug: record.AppSlug}, installation))
	http.Redirect(w, r, s.at("/#/github/apps/catalogue"), http.StatusFound)
}

// GitHubRunnerApps is where runner Apps are kept: every App's record and
// key, which the deployment hands to its runners.
type GitHubRunnerApps interface {
	Put(ctx context.Context, record runnerapp.Record, privateKey string) error
	List(ctx context.Context) ([]runnerapp.Record, error)
	PrivateKey(ctx context.Context, tier, org string) (string, bool, error)
	Delete(ctx context.Context, tier, org string) error
}

// Where GitHub sends the browser back to while a runner App is created:
// after Create, and after Install.
const (
	githubRunnerCallbackPath = "/connect/github/runner/callback"
	githubRunnerSetupPath    = "/connect/github/runner/setup"
)

// githubRunnerBind prefixes `<tier>:<org>` in a runner flow's signed state,
// so an organisation connect's state can never finish a runner App's flow,
// and the other way round.
const githubRunnerBind = "github-runner:"

// runnerTier checks a tier against the ones the deployment declares.
func (c *Console) runnerTier(tier string) error {
	switch {
	case c.deps.GitHubRunnerApps == nil || len(c.deps.GitHubRunnerTiers) == 0:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment declares no runner tiers"))
	case !slices.Contains(c.deps.GitHubRunnerTiers, tier):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("%q is not a runner tier here; the deployment declares %s", tier, strings.Join(c.deps.GitHubRunnerTiers, ", ")))
	}
	return nil
}

// BeginGitHubRunnerAppConnect starts creating a runner App, or finishing
// installing one created before.
//
// Only in an organisation the policy binds, for the same reason as an
// organisation's App: a typo in the login would otherwise meet GitHub's
// 404 after the operator has left this page.
func (c *Console) BeginGitHubRunnerAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubRunnerAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubRunnerAppConnectResponse], error) {
	org := strings.TrimSpace(req.Msg.GetOrg())
	id, err := c.requireOrg(ctx, access.RoleOperator, org)
	if err != nil {
		return nil, err
	}
	begun, err := c.beginRunnerAppConnect(ctx, id.Who(), org, strings.TrimSpace(req.Msg.GetTier()))
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubRunnerAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// beginRunnerAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Console) beginRunnerAppConnect(ctx context.Context, actor, org, tier string) (githubBegin, error) {
	if !status.ValidOrg(org) {
		return githubBegin{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not an organisation login", org))
	}
	if err := c.runnerTier(tier); err != nil {
		return githubBegin{}, err
	}
	if _, bound := boundOrganisations(c.deps.Authorizer.Policy())[org]; !bound {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the policy binds no organisation %s: bind its teams first", org))
	}
	existing, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s already has a %s runner App, %s: disconnect it first, or a second App would sit beside the first", org, tier, existing.AppSlug))
	}

	state, err := c.deps.State.IssueAs(access.Binding{Bind: githubRunnerBind + tier + ":" + org, Actor: actor})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out := githubBegin{state: state}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot()
	manifest, err := json.Marshal(githubapp.NewRunnerManifest(org, tier, c.deps.PublicURL, root+githubRunnerCallbackPath, root+githubRunnerSetupPath))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out.url, out.manifest = githubapp.CreateURL(org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubRunnerApp uninstalls a runner App, then forgets it. A
// failed uninstall still forgets, and says what is left to do by hand.
func (c *Console) DisconnectGitHubRunnerApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubRunnerAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubRunnerAppResponse], error) {
	org := strings.TrimSpace(req.Msg.GetOrg())
	if _, err := c.requireOrg(ctx, access.RoleOperator, org); err != nil {
		return nil, err
	}
	gone, err := c.disconnectRunnerApp(ctx, org, strings.TrimSpace(req.Msg.GetTier()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubRunnerAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}), nil
}

// disconnectRunnerApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Console) disconnectRunnerApp(ctx context.Context, org, tier string) (githubDisconnect, error) {
	switch {
	case !status.ValidOrg(org) || !runnerapp.ValidTier(tier):
		return githubDisconnect{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q/%q is not an organisation and a tier", org, tier))
	case c.deps.GitHubRunnerApps == nil:
		return githubDisconnect{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment keeps no runner Apps"))
	}
	record, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	key, hasKey, err := c.deps.GitHubRunnerApps.PrivateKey(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if !created && !hasKey {
		return githubDisconnect{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s has no %s runner App", org, tier))
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(org, record.AppSlug)
	if err = c.deps.GitHubRunnerApps.Delete(ctx, tier, org); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	c.record(ctx, audit.RunnerAppDisconnected(actorOf(ctx), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, out.uninstalled, out.detail))
	return out, nil
}

// runnerApp finds one runner App's record.
func (c *Console) runnerApp(ctx context.Context, tier, org string) (runnerapp.Record, bool, error) {
	records, err := c.deps.GitHubRunnerApps.List(ctx)
	if err != nil {
		return runnerapp.Record{}, false, err
	}
	for i := range records {
		if records[i].Tier == tier && records[i].Org == org {
			return records[i], true, nil
		}
	}
	return runnerapp.Record{}, false, nil
}

// runnerStatus adds the declared tiers and every runner App, never a key.
// An App whose tier the deployment no longer declares is still listed, so
// it can be disconnected.
func (c *Console) runnerStatus(ctx context.Context, out *directoryrosterv1.GetGitHubStatusResponse) error {
	if c.deps.GitHubRunnerApps == nil {
		return nil
	}
	out.RunnerTiers = slices.Clone(c.deps.GitHubRunnerTiers)
	records, err := c.deps.GitHubRunnerApps.List(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		record := &records[i]
		out.RunnerApps = append(out.RunnerApps, &directoryrosterv1.GitHubRunnerApp{
			Org: record.Org, Tier: record.Tier, AppId: record.AppID, AppSlug: record.AppSlug,
			Installed: record.Installed(), HtmlUrl: record.HTMLURL,
			ConnectedAt: timestampOf(record.ConnectedAt), ConnectedBy: record.ConnectedBy,
		})
	}
	return nil
}

// githubRunnerFlow checks a runner flow's redirect and names its tier and
// organisation.
func (s *ConsoleServer) githubRunnerFlow(w http.ResponseWriter, r *http.Request) (tier, org, actor string, ok bool) {
	bind, actor, _, ok := s.githubBound(w, r)
	if !ok {
		return "", "", "", false
	}
	rest, isRunner := strings.CutPrefix(bind, githubRunnerBind)
	tier, org, split := strings.Cut(rest, ":")
	if !isRunner || !split || !runnerapp.ValidTier(tier) || !status.ValidOrg(org) {
		s.githubProblem(w, r, http.StatusBadRequest, "This is not a runner App's connect.", "", nil)
		return "", "", "", false
	}
	if s.console.deps.GitHubRunnerApps == nil || !slices.Contains(s.console.deps.GitHubRunnerTiers, tier) {
		s.githubProblem(w, r, http.StatusConflict, fmt.Sprintf("This deployment keeps no %s runner Apps.", tier), "", nil)
		return "", "", "", false
	}
	return tier, org, actor, true
}

// githubRunnerCallback is where GitHub sends the owner after creating a
// runner App, with a one-time code for its key. The key is kept and the
// owner is sent straight on to Install.
func (s *ConsoleServer) githubRunnerCallback(w http.ResponseWriter, r *http.Request) {
	tier, org, actor, ok := s.githubRunnerFlow(w, r)
	if !ok {
		return
	}
	registration, err := githubapp.Convert(r.Context(), s.console.githubHTTP(), r.URL.Query().Get("code"))
	if err != nil {
		s.log.WarnContext(r.Context(), "a runner App was created and its key could not be collected",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and then would not hand over its key.", err.Error(), []string{
				"The page was reloaded: the code GitHub returns can be exchanged once.",
				"More than an hour passed between creating the App and returning here.",
				"This service cannot reach api.github.com: the cluster's egress policy has to allow it.",
			})
		return
	}
	if !strings.EqualFold(registration.Owner, org) {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("The App was created under %s, not %s.", registration.Owner, org), "", nil)
		return
	}
	record := runnerapp.Record{
		Tier: tier, Org: org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	if err = s.console.deps.GitHubRunnerApps.Put(r.Context(), record, registration.PEM); err != nil {
		s.log.ErrorContext(r.Context(), "a runner App was created and could not be kept",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "runner App created", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("app", registration.ID), logattr.SafeString("slug", registration.Slug), logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.RunnerAppCreated(audit.Identified(actor), org,
		audit.App{ID: registration.ID, Slug: registration.Slug, Tier: tier}))

	state, err := s.state.IssueAs(access.Binding{Bind: githubRunnerBind + tier + ":" + org, Actor: actor})
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App was created; start Create again to install it.", err.Error(), nil)
		return
	}
	http.SetCookie(w, access.ConnectCookie(state, s.sessions.Secure(), githubFlowWindow))
	http.Redirect(w, r, githubapp.InstallURL(registration.Slug, state), http.StatusFound)
}

// githubRunnerSetup is where GitHub sends the owner after installing a
// runner App. What is kept is where GitHub says the App is installed, asked
// as the App.
func (s *ConsoleServer) githubRunnerSetup(w http.ResponseWriter, r *http.Request) {
	tier, org, actor, ok := s.githubRunnerFlow(w, r)
	if !ok {
		return
	}
	http.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	store := s.console.deps.GitHubRunnerApps
	record, created, err := s.console.runnerApp(r.Context(), tier, org)
	key, hasKey, keyErr := store.PrivateKey(r.Context(), tier, org)
	if err = errors.Join(err, keyErr); err != nil || !created || !hasKey {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("There is no %s runner App for %s to finish installing. Start Create again.", tier, org), errString(err), nil)
		return
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error(), nil)
		return
	}
	installation, err := githubapp.FindInstallation(r.Context(), s.console.githubHTTP(), token, org)
	if err != nil {
		summary := "GitHub could not say where the App is installed."
		if errors.Is(err, githubapp.ErrNotInstalled) {
			summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", org)
		}
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), nil)
		return
	}
	record.InstallationID = installation
	if err = store.Put(r.Context(), record, key); err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App is installed and could not be recorded here.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "runner App installed", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("installation", installation), logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.RunnerAppInstalled(audit.Identified(actor), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, installation))
	http.Redirect(w, r, s.at("/#/github/apps"), http.StatusFound)
}
