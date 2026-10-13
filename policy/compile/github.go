package compile

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// GitHub Apps held by the issuer, and the CI jobs that may ask it for an
// installation token of one. Three declarations, because they answer three
// questions:
//
//	jobs     who: a CI job, by what GitHub's token for it says --
//	         repository, ref, event and the workflow files. Rendered as
//	         `github` matchers on an internal group of the job's name.
//	grants   who may have what: App id to the job groups that may ask for a
//	         token, for which repositories, with at most which permissions.
//	apps     what exists: the Apps, rendered into the issuer's catalogue.

const (
	// GitHubAppNameSuffix is appended to an App's id to name it on GitHub
	// when no name is declared. GitHub App names are global, so the suffix
	// says who holds it.
	GitHubAppNameSuffix = "-roster"

	// gitHubAppNameLimit is GitHub's length limit on an App's name.
	gitHubAppNameLimit = 34

	// The two installation scopes: every repository, or those picked.
	gitHubInstallAll      = "all"
	gitHubInstallSelected = "selected"

	gitHubWorkflowsDir = ".github/workflows/"

	// The two kinds an entry's `push` may name its store, as the chart
	// spells them.
	gitHubPushStoreKind        = "SecretStore"
	gitHubPushClusterStoreKind = "ClusterSecretStore"
)

type (
	// GitHubJob is one CI job identity: every field must hold for the job's
	// token. Keyed by the internal group it becomes.
	GitHubJob struct {
		// Repository is the repository the run belongs to, `<owner>/<name>`:
		// for a reusable workflow, the CALLER's.
		Repository string `yaml:"repository"`
		// Ref is what the run is on: one branch, `refs/heads/<name>`, or one
		// tag pattern, `refs/tags/<prefix>*`, for a job a tag starts.
		Ref string `yaml:"ref"`
		// Events are what may start the run, one matcher each: GitHub's
		// `event_name`, and a pattern cannot say "one of".
		Events []string `yaml:"events"`
		// Workflows are the files, one matcher each.
		Workflows []GitHubJobWorkflow `yaml:"workflows"`
	}

	// GitHubJobWorkflow is one workflow file pair.
	GitHubJobWorkflow struct {
		// Caller is the file the run starts from, a path in Repository: the
		// token's `workflow_ref`, at Ref.
		Caller string `yaml:"caller"`
		// Reusable is the file the job is defined in when the caller calls
		// one, `<owner>/<repo>/.github/workflows/<file>`: the token's
		// `job_workflow_ref`. It is matched at any COMMIT (`@*`, which cannot
		// match `refs/...` since `*` does not cross a `/`): which commit is
		// the caller's reviewed choice. Empty means the job is defined in
		// Caller itself.
		Reusable string `yaml:"reusable,omitempty"`
	}

	// GitHubApp is one App the issuer creates and keeps the key of.
	GitHubApp struct {
		// ID names the App at the issuer: its storage key and what a token
		// request names. Never changes.
		ID string `yaml:"id"`
		// Org is the organisation the App is created under.
		Org string `yaml:"org"`
		// Name is the App's name on GitHub; empty is ID + "-roster".
		Name        string `yaml:"name,omitempty"`
		Description string `yaml:"description"`
		// Permissions is what the App may do, stated here.
		Permissions map[string]string `yaml:"permissions"`
		Events      []string          `yaml:"events,omitempty"`
		// Installation is all or selected.
		Installation string `yaml:"installation"`
		// Export places the installed App's document where a consumer
		// (External Secrets) reads it. A webhook needs it: the consumer
		// reads its `webhook_secret` there.
		Export bool `yaml:"export,omitempty"`
		// Webhook is where GitHub delivers Events. The issuer generates the
		// secret, sets it when the owner creates the App and rotates it.
		Webhook *GitHubAppWebhook `yaml:"webhook,omitempty"`
		// Push copies this App's credential out to a secret store, for the
		// one consumer that cannot ask the issuer for a token.
		Push *GitHubAppPush `yaml:"push,omitempty"`
	}

	// GitHubAppWebhook is where an App's events go: exactly one of URL (a
	// plain endpoint) and Kargo (a Kargo receiver, whose path moves with
	// the secret).
	GitHubAppWebhook struct {
		URL   string                 `yaml:"url,omitempty"`
		Kargo *GitHubAppWebhookKargo `yaml:"kargo,omitempty"`
	}

	// GitHubAppWebhookKargo is a Kargo GitHub receiver: Base is where Kargo
	// serves its receivers, Receiver the receiver's name, Project the Kargo
	// project (empty for a cluster-scoped receiver).
	GitHubAppWebhookKargo struct {
		Base     string `yaml:"base"`
		Receiver string `yaml:"receiver"`
		Project  string `yaml:"project,omitempty"`
	}

	// GitHubAppPush is one App's credential projected into a secret store:
	// the App's id, its installation id and its private key, copied to
	// RemoteKey as app_id, installation_id and private_key.
	//
	// What lands there IS the App's private key, a second durable copy that
	// acts as the App without the issuer knowing. It exists for a consumer
	// that cannot exchange a token, and for nothing else.
	GitHubAppPush struct {
		SecretStore GitHubAppPushStore `yaml:"secretStore"`
		// RemoteKey is the path inside the store.
		RemoteKey string `yaml:"remoteKey"`
	}

	// GitHubAppPushStore names the store the chart pushes through.
	GitHubAppPushStore struct {
		Name string `yaml:"name"`
		// Kind is SecretStore or ClusterSecretStore; empty is the chart's
		// default, SecretStore.
		Kind string `yaml:"kind,omitempty"`
	}

	// GitHubAppGrant is what one group may ask of an App.
	GitHubAppGrant struct {
		Group        string            `yaml:"group"`
		Repositories []string          `yaml:"repositories"`
		Permissions  map[string]string `yaml:"permissions"`
	}

	// CatalogApp is one entry of the issuer's GitHub App list, in the
	// chart's own keys.
	CatalogApp struct {
		ID           string            `yaml:"id"`
		Org          string            `yaml:"org"`
		Name         string            `yaml:"name"`
		Description  string            `yaml:"description"`
		Permissions  map[string]string `yaml:"permissions"`
		Events       []string          `yaml:"events,omitempty"`
		Installation string            `yaml:"installation"`
		Export       bool              `yaml:"export,omitempty"`
		Webhook      *GitHubAppWebhook `yaml:"webhook,omitempty"`
		Grants       []CatalogGrant    `yaml:"grants,omitempty"`
		Push         *GitHubAppPush    `yaml:"push,omitempty"`
	}

	// CatalogGrant is one grant of a CatalogApp.
	CatalogGrant struct {
		Group        string            `yaml:"group"`
		Repositories []string          `yaml:"repositories"`
		Permissions  map[string]string `yaml:"permissions"`
	}
)

// DisplayName is the App's name on GitHub.
func (a *GitHubApp) DisplayName() string {
	if a.Name != "" {
		return a.Name
	}

	return a.ID + GitHubAppNameSuffix
}

var (
	// The same shapes the issuer refuses at start.
	gitHubAppIDPattern      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	gitHubLoginPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)
	gitHubNamePattern       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	gitHubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._*?\[\]^!-]+$`)
	// A literal repository, workflow path or branch: no pattern characters,
	// because a matcher reads each as a glob.
	gitHubLiteralPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// A tag ref a release job is pinned to, after `refs/tags/`: literal
	// characters and at most a trailing `*`, which is what a version suffix
	// needs (`v*`, `app/v*`). A bare `*` is refused: it would admit every
	// tag of the repository. `*` does not cross a `/`, so `app/v*` cannot
	// match another project's tag.
	gitHubTagPattern = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9._/-]*\*?$`)
	gitHubLevels     = map[string]int{"read": 1, "write": 2, "admin": 3}
)

// The two kinds of ref a job may be pinned to, spelled as GitHub's
// `ref_type` claim spells them.
const (
	GitHubRefBranch = "branch"
	GitHubRefTag    = "tag"
)

// GitHubRefType says which kind of ref this is, and whether it is one a job
// may be pinned to.
//
// A BRANCH ref is literal: the reviewed file on the reviewed branch.
//
// A TAG ref may end in `*`, because a release job's ref carries the version
// it is releasing and nobody can declare that in advance. A tag is weaker
// than a branch -- whoever may push a tag chooses the commit, and so the
// file -- which is why it suits only jobs whose reach is a release.
func GitHubRefType(ref string) (string, bool) {
	if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return GitHubRefBranch, branch != "" && gitHubLiteralPattern.MatchString(branch)
	}

	if tag, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		return GitHubRefTag, gitHubTagPattern.MatchString(tag)
	}

	return "", false
}

// ValidateGitHubJobs refuses a job whose matchers would admit more than one
// reviewed file on one ref, started one of the declared ways. where names
// the table in the messages (`jobs` gives `jobs[<name>]: ...`).
func ValidateGitHubJobs(where string, jobs map[string]GitHubJob) []error {
	var errs []error

	for _, name := range sortedKeys(jobs) {
		job := jobs[name]
		row := fmt.Sprintf("%s[%s]", where, name)
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: "+format, append([]any{row}, args...)...))
		}

		if segments := strings.Split(name, ":"); len(segments) != 3 || slices.Contains(segments, "") {
			fail("a job is an internal group, <scope>:<thing>:<role>")
		}

		if owner, repo, ok := strings.Cut(job.Repository, "/"); !ok || !gitHubLoginPattern.MatchString(owner) ||
			repo == "" || strings.Contains(repo, "/") || !gitHubLiteralPattern.MatchString(repo) {
			fail("repository %q is not <owner>/<name>", job.Repository)
		}

		refType, refOK := GitHubRefType(job.Ref)

		switch {
		case refOK:
		case refType == GitHubRefBranch:
			fail("ref %q is not one branch, refs/heads/<name>", job.Ref)
		case refType == GitHubRefTag:
			fail("ref %q is not one tag pattern, refs/tags/<prefix> with at most a trailing *", job.Ref)
		default:
			fail("ref %q is neither one branch (refs/heads/<name>) nor one tag pattern (refs/tags/<prefix>*)", job.Ref)
		}

		if len(job.Events) == 0 {
			fail("events is empty, which would admit a run of these files started by anything")
		}

		// A tag is pushed, or dispatched from GitHub's ref picker.
		// `schedule` runs on the default branch only, so a matcher pairing it
		// with a tag is one nothing can satisfy.
		if refType == GitHubRefTag {
			for _, event := range job.Events {
				if event != "push" && event != "workflow_dispatch" {
					fail("event %q cannot run on a tag; a tag ref admits push and workflow_dispatch", event)
				}
			}
		}

		for _, event := range job.Events {
			if !gitHubNamePattern.MatchString(event) {
				fail("event %q is not an event name", event)
			}

			// `pull_request` runs the PROPOSED version of the workflow file,
			// so a matcher on it would hand an identity to an edit nobody
			// reviewed. `pull_request_target` is the one exception: GitHub runs
			// the file from the BASE branch and the token says so.
			if event != "pull_request_target" && strings.HasPrefix(event, "pull_request") {
				fail("event %q runs a proposed change of the files, not the reviewed ones", event)
			}
		}

		if len(job.Workflows) == 0 {
			fail("workflows is empty, which would admit every workflow on the branch")
		}

		for i, workflow := range job.Workflows {
			if !strings.HasPrefix(workflow.Caller, gitHubWorkflowsDir) || !gitHubLiteralPattern.MatchString(workflow.Caller) {
				fail("workflows[%d]: caller %q is not a file under %s", i, workflow.Caller, gitHubWorkflowsDir)
			}

			if workflow.Reusable != "" && !validReusableWorkflow(workflow.Reusable) {
				fail("workflows[%d]: reusable %q is not <owner>/<repo>/%s<file>, without a ref", i, workflow.Reusable, gitHubWorkflowsDir)
			}
		}
	}

	return errs
}

// validReusableWorkflow is `<owner>/<repo>/.github/workflows/<file>`, with
// no ref: the matcher adds the commit glob.
func validReusableWorkflow(reusable string) bool {
	parts := strings.SplitN(reusable, "/", 3)

	return len(parts) == 3 && gitHubLoginPattern.MatchString(parts[0]) && parts[1] != "" &&
		strings.HasPrefix(parts[2], gitHubWorkflowsDir) && gitHubLiteralPattern.MatchString(reusable)
}

// ValidateGitHubApps checks each App on its own, and returns them by id.
// where names the list in the messages (`apps` gives `apps[0] (<id>): ...`).
func ValidateGitHubApps(where string, declared []GitHubApp) (map[string]*GitHubApp, []error) {
	var errs []error

	apps := map[string]*GitHubApp{}
	names := map[string]string{}
	pushed := map[string]string{}

	for i := range declared {
		app := &declared[i]
		row := fmt.Sprintf("%s[%d] (%s)", where, i, app.ID)
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: "+format, append([]any{row}, args...)...))
		}

		if !gitHubAppIDPattern.MatchString(app.ID) {
			fail("id is not lower-case letters, digits and dashes, at most 32")
		}

		if _, dup := apps[app.ID]; dup {
			fail("id is declared twice")
		}

		apps[app.ID] = app

		if !gitHubLoginPattern.MatchString(app.Org) {
			fail("org %q is not an organization login", app.Org)
		}

		name := app.DisplayName()
		if len(name) > gitHubAppNameLimit {
			fail("name %q is longer than GitHub's %d characters", name, gitHubAppNameLimit)
		}

		if other, dup := names[strings.ToLower(name)]; dup {
			fail("name %q is %s's too, and GitHub App names are global", name, other)
		}

		names[strings.ToLower(name)] = app.ID

		if strings.TrimSpace(app.Description) == "" {
			fail("description is empty; it is what the owner reads on GitHub's create page")
		}

		if len(app.Permissions) == 0 {
			fail("permissions are required: what the App may do is stated here, not copied from elsewhere")
		}

		errs = append(errs, gitHubPermissionErrors(row+".permissions", app.Permissions)...)

		switch app.Installation {
		case "":
			fail("installation is required")
		case gitHubInstallAll, gitHubInstallSelected:
		default:
			fail("installation %q is neither all nor selected", app.Installation)
		}

		errs = append(errs, gitHubWebhookErrors(row, app)...)

		if app.Push == nil {
			continue
		}

		if strings.TrimSpace(app.Push.SecretStore.Name) == "" {
			fail("push.secretStore.name is empty; the chart invents no store")
		}

		switch app.Push.SecretStore.Kind {
		case "", gitHubPushStoreKind, gitHubPushClusterStoreKind:
		default:
			fail("push.secretStore.kind %q is neither %s nor %s", app.Push.SecretStore.Kind, gitHubPushStoreKind, gitHubPushClusterStoreKind)
		}

		key := strings.TrimSpace(app.Push.RemoteKey)
		if key == "" || key != app.Push.RemoteKey || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
			fail("push.remoteKey %q is not one path in the store", app.Push.RemoteKey)
		}

		// Two Apps pushing to one path in one store is one credential
		// overwriting the other, and whoever read that path could not say
		// which App's key they held.
		at := app.Push.SecretStore.Kind + "/" + app.Push.SecretStore.Name + "/" + key
		if other, dup := pushed[at]; dup {
			fail("push.remoteKey %q in store %s is %s's too", key, app.Push.SecretStore.Name, other)
		}

		pushed[at] = app.ID
	}

	return apps, errs
}

// ValidateGitHubAppGrants checks each grant's shape and App, and returns the
// groups granted anything. where names the table in the messages.
func ValidateGitHubAppGrants(where string, grants map[string][]GitHubAppGrant, apps map[string]*GitHubApp) (map[string]bool, []error) {
	var errs []error

	granted := map[string]bool{}

	for _, id := range sortedKeys(grants) {
		if _, ok := apps[id]; !ok {
			errs = append(errs, fmt.Errorf("%s[%s]: no App has that id", where, id))
		}

		for i, grant := range grants[id] {
			row := fmt.Sprintf("%s[%s][%d] (%s)", where, id, i, grant.Group)
			fail := func(format string, args ...any) {
				errs = append(errs, fmt.Errorf("%s: "+format, append([]any{row}, args...)...))
			}

			if strings.TrimSpace(grant.Group) == "" {
				fail("group is empty")
			}

			granted[grant.Group] = true

			if len(grant.Repositories) == 0 {
				fail(`repositories is empty; name some, or ["*"] for every one`)
			}

			for _, repository := range grant.Repositories {
				if !gitHubRepositoryPattern.MatchString(repository) {
					fail("repository %q is not a name or glob in the App's organization", repository)

					continue
				}

				if _, err := path.Match(repository, ""); err != nil {
					fail("repository %q does not compile: %v", repository, err)
				}
			}

			if len(grant.Permissions) == 0 {
				fail("permissions is empty, a grant of nothing")
			}

			errs = append(errs, gitHubPermissionErrors(row+".permissions", grant.Permissions)...)
		}
	}

	return granted, errs
}

func gitHubPermissionErrors(where string, permissions map[string]string) []error {
	var errs []error

	for _, name := range slices.Sorted(maps.Keys(permissions)) {
		if !gitHubNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: %q is not a permission name", where, name))
		}

		if gitHubLevels[permissions[name]] == 0 {
			errs = append(errs, fmt.Errorf("%s: %s: %q is not read, write or admin", where, name, permissions[name]))
		}
	}

	return errs
}

// gitHubWebhookErrors refuses a webhook the issuer would refuse later: it
// needs events and an export (the consumer reads the secret there), names
// exactly one of url and kargo, and the Kargo receiver needs a base and a
// name. Events without a webhook are delivered to nobody.
func gitHubWebhookErrors(where string, app *GitHubApp) []error {
	var errs []error

	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{where}, args...)...))
	}

	w := app.Webhook
	if w == nil {
		if app.Export && len(app.Events) > 0 {
			fail("events are delivered only to an App that declares a webhook")
		}

		return errs
	}

	if len(app.Events) == 0 {
		fail("a webhook needs events")
	}

	if !app.Export {
		fail("a webhook needs export: true, or no consumer can read the secret")
	}

	if (w.URL == "") == (w.Kargo == nil) {
		fail("webhook names exactly one of url and kargo")
	}

	if w.URL != "" && !strings.HasPrefix(w.URL, "https://") {
		fail("webhook.url %q is not an https URL", w.URL)
	}

	if k := w.Kargo; k != nil && (!strings.HasPrefix(k.Base, "https://") || k.Receiver == "") {
		fail("webhook.kargo needs an https base and a receiver")
	}

	return errs
}

// Fields is the matcher as the policy's `github` keys, in a stable order,
// the unset ones left out.
func (m GitHubMatcher) Fields() [][2]string {
	all := [][2]string{
		{"repository", m.Repository},
		{"owner", m.Owner},
		{"visibility", m.Visibility},
		{"ref", m.Ref},
		{"ref_type", m.RefType},
		{"event_name", m.EventName},
		{"workflow_ref", m.WorkflowRef},
		{"job_workflow_ref", m.JobWorkflowRef},
	}

	out := make([][2]string, 0, len(all))

	for _, field := range all {
		if field[1] != "" {
			out = append(out, field)
		}
	}

	return out
}

// GitHubJobMatchers renders one job as `github` matchers: one per event per
// workflow file, each pinning everything.
func GitHubJobMatchers(job GitHubJob) []GitHubMatcher {
	out := make([]GitHubMatcher, 0, len(job.Workflows)*len(job.Events))

	// Validation has already refused anything else; an unknown ref here
	// would render a matcher without a ref_type, which is a matcher that
	// checks one thing less.
	refType, _ := GitHubRefType(job.Ref)

	for _, workflow := range job.Workflows {
		callerRef := job.Repository + "/" + workflow.Caller + "@" + job.Ref

		jobRef := callerRef
		if workflow.Reusable != "" {
			jobRef = workflow.Reusable + "@*"
		}

		for _, event := range job.Events {
			out = append(out, GitHubMatcher{
				Repository:     job.Repository,
				Ref:            job.Ref,
				RefType:        refType,
				EventName:      event,
				WorkflowRef:    callerRef,
				JobWorkflowRef: jobRef,
			})
		}
	}

	return out
}

// GitHubCatalog resolves the Apps and their grants into the issuer's
// catalogue, in declaration order (the issuer picks the first grant that
// covers a request, in catalogue order). declared says whether the rendered
// policy declares a group: the issuer refuses to start on a grant naming
// one it does not. appsWhere and grantsWhere name the two tables in the
// messages.
func GitHubCatalog(
	appsWhere, grantsWhere string, apps []GitHubApp, grants map[string][]GitHubAppGrant, declared func(group string) bool,
) ([]CatalogApp, error) {
	var errs []error

	out := make([]CatalogApp, 0, len(apps))

	for i := range apps {
		app := &apps[i]
		entry := CatalogApp{
			ID:           app.ID,
			Org:          app.Org,
			Name:         app.DisplayName(),
			Description:  strings.TrimSpace(app.Description),
			Permissions:  app.Permissions,
			Events:       app.Events,
			Installation: app.Installation,
			Export:       app.Export,
			Webhook:      app.Webhook,
			Push:         app.Push,
		}

		if entry.Installation != gitHubInstallAll && entry.Installation != gitHubInstallSelected {
			errs = append(errs, fmt.Errorf("%s (%s): installation %q is neither all nor selected", appsWhere, app.ID, entry.Installation))
		}

		for j, grant := range grants[app.ID] {
			row := fmt.Sprintf("%s[%s][%d] (%s)", grantsWhere, app.ID, j, grant.Group)

			if !declared(grant.Group) {
				errs = append(errs, fmt.Errorf("%s: the policy declares no such group, and the issuer refuses to start on it", row))
			}

			for _, name := range slices.Sorted(maps.Keys(grant.Permissions)) {
				have, ok := entry.Permissions[name]

				switch {
				case !ok:
					errs = append(errs, fmt.Errorf("%s: %s is not a permission the App has", row, name))
				case gitHubLevels[have] < gitHubLevels[grant.Permissions[name]]:
					errs = append(errs, fmt.Errorf("%s: %s: %s is more than the App's %s", row, name, grant.Permissions[name], have))
				}
			}

			entry.Grants = append(entry.Grants, CatalogGrant(grant))
		}

		out = append(out, entry)
	}

	return out, errors.Join(errs...)
}
