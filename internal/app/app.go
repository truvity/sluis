// Package app assembles the directory hub from its configuration: the
// stores, the snapshot cache, the backends, the policy, the connectors,
// the recovery path and the three listeners.
//
// It is a package rather than the body of main because this is where the
// service's behaviour is decided — which store, which shape of recovery,
// who may call the API listener, what URL the OAuth redirects are built
// from — and every one of those is somewhere a deployment can be quietly
// wrong. Four of them were, and none of it was reachable by a test while
// it lived in main().
//
// The API listener carries DirectoryService for consumers on the cluster
// network. The console listener carries the operator services, the login
// routes and the consent callback, behind an authenticating gateway or
// behind the hub's own sign-in. `demo` gives two tenants held in memory,
// which need no credential and no network.
package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/google"
	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/frontend"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/connector"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubapp/mints"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/httpsec"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/hubwire"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/recoverygate"
	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/server"
	"github.com/truvity/sluis/internal/settings"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/srp"
	"github.com/truvity/sluis/storage/logattr"
)

// Config is the whole of the hub's configuration. The chart sets it from
// the values; the defaults are what a laptop needs.
//
// Built from the service's configuration file (see [FromConfig]).
type Config struct {
	// Reader makes the hub only read snapshots: the directory is another
	// module's (`google.remote`), whose schedule refreshes them. No workspace is
	// adopted, attached or reopened here, and no sign-in, request or loop
	// refreshes or reads a directory. Unset keeps the hub that owns its
	// directory, which is a local install with no remote.
	Reader bool
	// GoogleIssuer and GoogleConsole reach the Google module when it is another
	// function (`google.remote`): the first signs people in (`google.identify`,
	// caller class `issuer`), the second asks it the console's questions (caller
	// class `console`). Nil is the module in this process. They are set with
	// Reader: a hub that holds no workspaces has no connector of its own.
	GoogleIssuer, GoogleConsole modcall.Caller
	// GitHubRemote, SlackRemote and CloudflareRemote say that the module runs as
	// another function (`<module>.remote`). The hub then opens none of that
	// module's stores, blobs, credentials or maintenance gate: every operation on
	// them is the module's, asked over modcall, and this process holds no access
	// to its data (v1.76 E-FIN). Unset is the module in this process, as the
	// demonstration and the tests have it.
	GitHubRemote, SlackRemote, CloudflareRemote bool
	// RecoveryGate is the switch an operator throws, from the console, to turn
	// the recovery sign-in off. The merged service builds one over the issuer's
	// State and hands the same to the issuer half; nil builds one over the
	// stores this hub is given.
	RecoveryGate *recoverygate.Gate
	// ReadsNoSecretAtStart says a start must read no secret (a function: a herd of
	// cold starts would read each of them in every new environment). The check of
	// the password a verifier is derived from then waits for readiness or the
	// first sign-in, and is not run at start.
	ReadsNoSecretAtStart bool

	consolePort int
	healthPort  int

	freshness hub.Config

	demo      bool
	publicURL string
	// publicRootURL is the host's root, never carrying route.pathPrefix
	// even where publicURL does: the bootstrap surface -- the
	// admin-consent callback, and this hub's own sign-in when it runs
	// one -- stays at the domain root on its own HTTPRoute. Empty falls
	// back to publicURL in the console, which is exactly right wherever
	// no prefix is configured.
	publicRootURL    string
	recoveryEnabled  bool
	recoveryAccount  string
	recoveryAudience string
	loginDirectory   bool
	// recoveryLogin names the recovery password's secret, read instead of
	// being generated, outside a cluster.
	recoveryLogin string
	// recoveryVerifier names the recovery account's SRP verifier
	// (`recovery.verifierSecret`): what the password sign-in checks the
	// password against, so that the hub holds a verifier and not the password
	// (docs/decisions/0076). Unset derives one from the password.
	recoveryVerifier string
	// recoveryGeneration is `recovery.generation`: which decision to have
	// recovery on a disable from the console covers (internal/recoverygate).
	recoveryGeneration string
	// instance names this installation in the audit trail's records.
	instance        string
	sessionLifetime time.Duration
	// absoluteLifetime caps the console's own session the same way it caps
	// a per-client one in the issuer: read from the SAME key
	// the issuer's config reads (lifetimes.absolute), because the
	// two run in one process and one pod sets it once. See
	// issuer.DefaultAbsoluteLifetime for why 24h.
	absoluteLifetime  time.Duration
	secureCookies     bool
	hsts              httpsec.HSTS
	forwardedHeader   string
	forwardedIssuer   string
	signOutURL        string
	forwardedAudience string
	// policy is the policy document, nil for the built-in policy (or the
	// demonstration's).
	policy *config.PolicyDocument
	// workspaces are the directories the deployment declares.
	workspaces      []config.DirectoryWorkspace
	store           string
	release         string
	oauthSecretName string
	oauthIDKey      string
	oauthSecretKey  string
	// oauthDeclared serves the client the document declares: set off-cluster,
	// where there is no Secret to read it from. It reads its secrets when a
	// request first asks for the client.
	oauthDeclared settings.Store
	// oauthClient is the client the document declares, served by
	// oauthDeclared through the secrets source.
	oauthClient *config.OAuthClient
	// audit is the audit installation this service connects to, if any.
	audit audit.Config
	// auditQuery is its query service, for the console's Audit page, and
	// auditAudience the client whose audience the page's tokens carry.
	auditQuery    string
	auditAudience string
	// auditForwardedForTrustedHops is how many of the deployment's own
	// proxies append to X-Forwarded-For; zero records the peer.
	auditForwardedForTrustedHops int
	holdWindow                   time.Duration
	logLevel                     slog.Level
	// githubRunnerTiers are the tiers an operator may create a runner App
	// for, from the runner entries of the policy's apps.github.apps. Empty keeps none.
	githubRunnerTiers []string
	// githubCatalogue is every GitHub App the deployment declares, the
	// catalogue entries of the policy's apps.github.apps. Empty declares none.
	githubCatalogue *catalogue.Catalogue
	// slackCatalogue is every Slack App the deployment declares, the
	// policy's apps.slack.catalogue. Empty declares none.
	slackCatalogue *slackcatalogue.Catalogue
}

// The listeners the hub's own Run serves. The merged service serves none of
// them (it runs the loops and mounts the console on the issuer's listener), so
// they are not configuration.
const (
	defaultConsolePort = 8081
	defaultHealthPort  = 7070
)

// FromConfig builds the hub's settings from the service document and the
// policy document, which the caller has already held to their schemas and
// checks. p is nil for the built-in policy (or the demonstration's). What a
// schema cannot say is checked here, before anything starts.
func FromConfig(f *config.Serve, p *config.PolicyDocument) (Config, error) {
	c := Config{
		consolePort:   defaultConsolePort,
		healthPort:    defaultHealthPort,
		demo:          f.Demo,
		publicURL:     strings.TrimSuffix(f.PublicURL, "/"),
		publicRootURL: strings.TrimSuffix(f.PublicRootURL, "/"),
		// The hub's own defaults, which differ from the issuer's: recovery is
		// on, as a local run needs it.
		recoveryEnabled:  true,
		recoveryAccount:  "directory-roster-recovery",
		recoveryAudience: "directory-roster-recovery",
		loginDirectory:   true,
		policy:           p,
		store:            orDefault(f.Store, storeMemory),
		release:          orDefault(f.Release, "sluis"),
	}
	if r := f.Recovery; r != nil {
		if r.Enabled != nil {
			c.recoveryEnabled = *r.Enabled
		}
		c.recoveryAccount = orDefault(r.ServiceAccount, c.recoveryAccount)
		c.recoveryAudience = orDefault(r.Audience, c.recoveryAudience)
		c.recoveryLogin = r.LoginSecret
		c.recoveryVerifier = r.VerifierSecret
		c.recoveryGeneration = r.Generation
	}
	c.instance = orDefault(f.Instance, orDefault(f.Release, "sluis"))
	if d := f.Directory; d != nil {
		c.workspaces = d.Workspaces
	}
	if l := f.Login; l != nil {
		if l.Directory != nil {
			c.loginDirectory = *l.Directory
		}
		c.signOutURL = l.SignOutURL
		if fw := l.Forwarded; fw != nil {
			c.forwardedHeader = fw.EmailHeader
			c.forwardedIssuer = fw.Issuer
			c.forwardedAudience = fw.Audience
		}
	}
	var err error
	if o := f.OAuthClient; o != nil {
		c.oauthSecretName = o.SecretName
		c.oauthIDKey = o.IDKey
		c.oauthSecretKey = o.SecretKey
		c.oauthClient = o
	}
	// The instance is the pod, which is its hostname in a cluster: it names
	// this replica on every audit record.
	instance, _ := os.Hostname()
	c.audit = audit.Config{Instance: instance, Version: version.String()}
	if a := f.Audit; a != nil {
		c.audit.Writer = a.Writer
		c.audit.TokenFile = a.TokenFile
		c.auditQuery = a.QueryURL
		c.auditAudience = orDefault(a.Audience, "audit")
		c.auditForwardedForTrustedHops = max(a.ForwardedForTrustedHops, 0)
	} else {
		c.auditAudience = "audit"
	}
	// What the storage ports need is read from the same file by [store.FromServe]
	// at start; read here too so a bad value (an unset password variable)
	// is refused with the rest of the configuration.
	if _, err = store.FromServe(f); err != nil {
		return Config{}, err
	}
	// Secure follows the scheme the BROWSER will use, which the service
	// knows because it is told its own public URL. Defaulting to false
	// meant an installation that merely forgot to say so served session
	// cookies a proxy could strip onto a plain-http hop, and the alert
	// CodeQL raised was about that default rather than about this line.
	// secureCookies still overrides, in either direction, for the local
	// http listener and for a TLS terminator that is not in the URL.
	c.secureCookies = strings.HasPrefix(c.publicRootURL, "https://")
	if f.SecureCookies != nil {
		c.secureCookies = *f.SecureCookies
	}

	fr := f.Freshness
	if fr == nil {
		fr = &config.Freshness{}
	}
	c.freshness.RefreshInterval = dur(fr.RefreshInterval, hub.DefaultRefreshInterval)
	c.freshness.FreshnessWindow = dur(fr.FreshnessWindow, hub.DefaultFreshnessWindow)
	c.freshness.ProbeInterval = dur(fr.ProbeInterval, hub.DefaultProbeInterval)
	l := f.Lifetimes
	if l == nil {
		l = &config.Lifetimes{}
	}
	c.sessionLifetime = dur(l.Session, 12*time.Hour)
	// Defaulted and never refused here: lifetimes.absolute's own validation
	// (positive, at least lifetimes.token) runs once, in issuerapp.FromConfig,
	// which the same pod always loads beside this. A value that failed it
	// never reaches here in a real deployment; a test building [Config]
	// directly gets the ordinary 24h default rather than a second copy of
	// a check it may not care about.
	c.absoluteLifetime = dur(l.Absolute, 24*time.Hour)
	c.holdWindow = dur(l.Hold, 4*time.Hour)
	if c.publicURL == "" {
		c.publicURL = fmt.Sprintf("http://localhost:%d", c.consolePort)
	}
	if c.hsts, err = f.HSTS.Policy(c.publicURL); err != nil {
		return Config{}, err
	}
	level := "info"
	if f.Log != nil && f.Log.Level != "" {
		level = f.Log.Level
	}
	if err = c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	switch c.store {
	case storeMemory, storeKubernetes:
	default:
		return Config{}, fmt.Errorf("store: %q is neither %q nor %q", c.store, storeMemory, storeKubernetes)
	}
	for _, tier := range p.RunnerTiers() {
		if tier = strings.TrimSpace(tier); tier != "" && !slices.Contains(c.githubRunnerTiers, tier) {
			c.githubRunnerTiers = append(c.githubRunnerTiers, tier)
		}
	}
	// The catalogues were held to their rules, and to the policy's groups and
	// workspaces, when the policy document was loaded: an App created from a
	// wrong declaration holds the wrong permissions, and nothing here can
	// change them afterwards.
	c.githubCatalogue = p.GitHubCatalogue()
	c.slackCatalogue = p.SlackCatalogue()
	// A demonstration run declares its own tiers and catalogue, unless the
	// run declares some: the Apps page is otherwise the two Apps this
	// service makes for itself, and half the page cannot be walked through.
	if c.demo {
		if len(c.githubRunnerTiers) == 0 {
			c.githubRunnerTiers = demo.GitHubRunnerTiers()
		}
		if len(c.githubCatalogue.Apps) == 0 {
			c.githubCatalogue = demo.GitHubCatalogue()
		}
	}
	return c, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// dur reads a duration the file may leave unset.
func dur(d *config.Duration, fallback time.Duration) time.Duration {
	if d == nil {
		return fallback
	}
	return d.D()
}

// The two places the hub can keep what a console changed.
const (
	// storeMemory keeps nothing: a restart is a fresh installation. It is
	// what a local run and the demonstration want, and it is the default
	// so that neither needs a cluster.
	storeMemory = "memory"
	// storeKubernetes keeps it in the hub's own namespace, which is what
	// a deployment wants: a workspace connected in the console has no
	// other home.
	storeKubernetes = "kubernetes"
)

// openSnapshots decides where snapshots live.
//
// In memory unless the ports hold state every replica sees (a Valkey is
// configured, or the memory adapter is chosen), and the difference matters at
// more than one replica: two hubs each holding their own snapshots answer
// the same question two ways and read the same directory twice, and a
// directory's API quota is per tenant, not per reader. So a deployment
// running more than one replica without a Valkey is a mistake worth
// saying out loud, rather than one that shows up as somebody's quota.
func openSnapshots(ctx context.Context, st *store.Stores, log *slog.Logger) hub.SnapshotStore {
	if !st.Usable {
		log.InfoContext(ctx, "keeping snapshots in memory: correct for one replica, "+
			"wasteful and inconsistent for more", slog.String("cache", "memory"))
		return hub.NewMemorySnapshots()
	}
	log.InfoContext(ctx, "keeping snapshots and the refresh lease in the state ports",
		slog.String("cache", st.Name()), slog.String("adapter", st.Adapter))
	return hub.NewBlobSnapshots(st.Ports.Blob, st.Ports.State)
}

// openRecovery builds the way in for the day the ordinary one is broken.
//
// In a cluster there is already an authority that says who is trusted —
// the API server — so recovery proves access to it and the hub keeps no
// credential at all: nothing to rotate, nothing to leak, nothing to find
// in an etcd backup, and an audit trail that names who recovered rather
// than "admin". Anywhere else there is nothing to prove access to, so a
// password is what is left: the one recovery.passwordFile holds (a platform
// delivers as the secret recovery.passwordSecret names), or one generated and
// printed once.
func openRecovery(
	ctx context.Context, cfg Config, kept stores, src secrets.Source, v5 *secretstore.StoresV5, log *slog.Logger,
) (server.Recovery, error) {
	if !cfg.recoveryEnabled {
		log.InfoContext(ctx, "no recovery sign-in: this hub can only be entered through the directory")
		return nil, nil //nolint:nilnil // no recovery is a configuration, not a failure
	}
	if kept.reviewToken == nil {
		where := ""
		var source srpauth.Source
		// check, set where the hub derives the verifier from a password, asks whether
		// the password may sit behind one: it is run at start and by readiness.
		var check func(context.Context) error
		if cfg.recoveryLogin != "" || cfg.recoveryVerifier != "" {
			// The credential is read when the first proof arrives, and again after
			// its time to live: opening the recovery reads no secret.
			if v5 != nil {
				// Layout v5: the account's verifier is at one fixed address, whatever
				// the document names it, and the password is not read at all. The
				// password's address is only what an operator is told.
				where = v5.OIDC().Prefix() + "/recovery-password"
				source = srpauth.SourceOf(v5, nil, srpauth.RecoveryUser, "")
			} else {
				if src == nil {
					return nil, errors.New("recovery.passwordSecret: no secrets source is configured")
				}
				if cfg.recoveryLogin != "" {
					where = src.Describe(cfg.recoveryLogin)
				}
				if cfg.recoveryVerifier != "" {
					source = srpauth.SourceOf(nil, src, srpauth.RecoveryUser, cfg.recoveryVerifier)
				} else {
					source = derivedCredential(src, cfg.recoveryLogin, kept.sessionKey)
					check = func(ctx context.Context) error { _, err := recoveryPassword(ctx, src, cfg.recoveryLogin); return err }
				}
			}
			log.InfoContext(ctx, "recovery sign-in is by the secret the configuration names", slog.String("secret", where))
		}
		if source != nil {
			recovery := server.NewPasswordRecovery(source, where)
			recovery.Check = check
			return recovery, nil
		}
		if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
			// A function instance would generate a password nobody can read except
			// from its own log, and print it there. Fail closed instead: no recovery,
			// and no secret in any line.
			log.ErrorContext(ctx, "recovery sign-in is enabled but no password is configured on this "+
				"function (recovery.passwordSecret): recovery is NOT available. "+
				"Set the password, or set recovery.enabled: false to silence this")
			return nil, nil //nolint:nilnil // no recovery is a configuration, not a failure
		}
		generated, err := generatedPassword()
		if err != nil {
			return nil, err
		}
		announceRecoveryPassword(generated)
		return server.NewPasswordRecovery(srpauth.Fixed(srp.Default.NewCredential(srp.NewSalt(), srpauth.RecoveryUser, generated)), ""), nil
	}

	subject := access.ServiceAccountSubject(kept.namespace, cfg.recoveryAccount)
	log.InfoContext(ctx, "recovery is by cluster access; nothing is stored",
		slog.String("service_account", cfg.recoveryAccount), slog.String("audience", cfg.recoveryAudience), slog.String("subject", subject))
	return &server.TokenRecovery{
		Review:    kept.reviewToken,
		Namespace: kept.namespace,
		Account:   cfg.recoveryAccount,
		Audience:  cfg.recoveryAudience,
		Subjects:  []string{subject},
	}, nil
}

// derivedCredential is the recovery account's credential made from the password
// the secrets source holds, for an installation that keeps the password and not
// a verifier (layout v4, a file): the hub reads the password only to derive the
// verifier, in memory, and never compares one with what a client sends.
//
// The password is used as the source delivers it (a file's trailing newline is
// the source's to remove) and is refused when it is too weak to sit behind a
// verifier ([srp.CheckPassword]). The salt is random in effect and has nothing
// to do with the password: an HMAC of the shared session key, so every container
// derives the same one and a password guess cannot be tested against it.
func derivedCredential(src secrets.Source, name string, key access.KeyFunc) srpauth.Source {
	return srpauth.Cached(lazy.TTL, func(ctx context.Context) (srp.Credential, error) {
		password, err := recoveryPassword(ctx, src, name)
		if err != nil {
			return srp.Credential{}, err
		}
		if key == nil {
			return srp.Credential{}, errors.New("recovery.passwordSecret: this console has no key to derive the salt from")
		}
		k, err := key()
		if err != nil {
			return srp.Credential{}, fmt.Errorf("recovery.passwordSecret: %w", err)
		}
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte("sluis srp salt\x00" + srpauth.RecoveryUser))
		return srp.Default.NewCredential(mac.Sum(nil), srpauth.RecoveryUser, password), nil
	})
}

// recoveryPassword reads the recovery password the hub derives its verifier from
// and holds it to [srp.CheckPassword]: a weak one is refused here, at start and by
// readiness, and not found out by the first sign-in of an incident.
func recoveryPassword(ctx context.Context, src secrets.Source, name string) (string, error) {
	password, err := src.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("recovery.passwordSecret: %w", err)
	}
	if password == "" {
		return "", errors.New("recovery.passwordSecret: the password is empty")
	}
	if err = srp.CheckPassword(password); err != nil {
		return "", fmt.Errorf("recovery.passwordSecret: %w", err)
	}
	return password, nil
}

// recoveryReadiness is the readiness of a recovery whose verifier is derived from
// a password: not ready while the password is too weak to sit behind one. Asked
// per probe, it reads the secret once in [lazy.TTL].
func recoveryReadiness(check func(context.Context) error) health.Dependency {
	cached := lazy.New(lazy.TTL, func(ctx context.Context) (struct{}, error) { return struct{}{}, check(ctx) })
	return health.Dependency{Name: "the recovery password", Check: func(ctx context.Context) error {
		_, err := cached.Get(ctx)
		return err
	}}
}

// bothReady is the readiness of two dependencies, either of which may be absent
// (a zero [health.Dependency]): the first failure answers.
func bothReady(first, second health.Dependency) health.Dependency {
	switch {
	case first.Check == nil:
		return second
	case second.Check == nil:
		return first
	}
	return health.Dependency{Name: first.Name + " and " + second.Name, Check: func(ctx context.Context) error {
		if err := first.Check(ctx); err != nil {
			return fmt.Errorf("%s: %w", first.Name, err)
		}
		if err := second.Check(ctx); err != nil {
			return fmt.Errorf("%s: %w", second.Name, err)
		}
		return nil
	}}
}

// githubOrgStore is what the console asks of where organisations are kept.
type githubOrgStore interface {
	server.GitHubConnections
	server.GitHubLinkApp
	server.GitHubConfirmations
}

// secretReadTimeout bounds a lazy read of a secret that has no request to run
// under.
const secretReadTimeout = 15 * time.Second

// stores is everything the hub writes down, and where.
type stores struct {
	workspaces  hub.Store
	credentials hub.CredentialStore
	settings    settings.Store
	// sessionKey is read when a session or a sign-in state is first signed
	// or checked, and again after [lazy.TTL]: opening the stores reads no
	// secret.
	sessionKey access.KeyFunc
	// reviewToken asks the cluster's API server who a token
	// authenticates. Nil outside a cluster, which is what selects the
	// password shape of recovery.
	reviewToken func(ctx context.Context, token string, audiences []string) (string, error)
	namespace   string
	// githubReports is where the GitHub controller reports. Nil with the
	// memory store, which has nowhere a separate process could write to.
	githubReports server.GitHubReports
	// githubOrgs is where connected GitHub organisations are kept. Nil
	// with the memory store, for the same reason.
	githubOrgs githubOrgStore
	// githubLinks is where people's linked GitHub accounts are kept. Nil
	// with the memory store, for the same reason.
	githubLinks server.GitHubLinks
	// githubRunnerApps is where runner Apps are kept. Nil with the memory
	// store, for the same reason.
	githubRunnerApps server.GitHubRunnerApps
	// githubCatalogueApps is where catalogue Apps are kept. Nil with the
	// memory store, for the same reason.
	githubCatalogueApps server.GitHubCatalogueApps
	// slackCatalogueApps is where catalogue Slack Apps are kept. Nil with
	// the memory store, for the same reason.
	slackCatalogueApps server.SlackCatalogueApps
	// slackShared keeps Slack Connect channel definitions, and slackStatus is
	// what the Slack controller reported. Nil with the memory store.
	slackShared server.SlackSharedRecords
	// slackChannels keeps console channels' records, in the same ConfigMap.
	slackChannels server.SlackChannelRecords
	// slackReports is what the Slack controller reported.
	slackReports server.SlackStatusReports
	// slackWorkspaces is where Slack workspaces are connected.
	slackWorkspaces server.SlackWorkspaces
	// maintenance are the gates over each module's maintenance flag, in the
	// module's own table. Nil outside the port stores.
	maintenance maintenance.Set
}

// openStores builds them, and says plainly in the log which was chosen.
// The memory store losing everything on restart is correct for a
// prototype and catastrophic for a deployment, so it is never silent.
func openStores(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) (stores, error) {
	// The one switch between the two storages: any adapter but `legacy` keeps
	// the domain records in State and their credentials in Secrets. `legacy` keeps
	// today's ConfigMaps and Secrets, unchanged. A demonstration has fixed
	// stores of its own and keeps them.
	if st.Adapter != store.AdapterLegacy && !cfg.demo {
		return openPortStores(ctx, cfg, st, log)
	}
	if cfg.store == storeMemory {
		key, err := access.NewSessionKey()
		if err != nil {
			return stores{}, err
		}
		log.WarnContext(ctx, "keeping state in memory: a restart loses every connected workspace, "+
			"the memberships added here and every session", slog.String("store", storeMemory))
		return stores{
			workspaces: hub.NewMemoryStore(),
			settings:   cfg.oauthDeclared,
			sessionKey: access.StaticKey(key),
		}, nil
	}

	return openKubeStores(ctx, cfg, st, log)
}

// openPortStores keeps the domain stores on the ports: records in State,
// every credential in Secrets (internal/portstore). Nothing here is
// a ConfigMap or a Secret, so the cluster's objects are needed only for what
// is still the cluster's: the token review, and the declared OAuth client
// a deployment mounts, which is an input and not a record this service writes.
func openPortStores(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) (stores, error) {
	// What the policy document's apps.github.apps declares: which Apps are
	// exported, their labels, and the App an organisation uses.
	exported := func(id string) bool { return cfg.policy.GitHubAppExported(id) || cfg.githubCatalogue.Exported(id) }
	declared := portstore.DeclaredGitHubApps{
		Labels: cfg.policy.GitHubAppLabels, RunnerLabels: cfg.policy.RunnerLabels, AppRef: cfg.policy.AppRef,
	}
	baseOf := func(set port.Set) *portstore.Base {
		return portstore.New(set).WithV5(st.V5).ExportGitHubApps(exported).DeclareGitHubApps(declared)
	}
	base := baseOf(st.Ports)
	// On layout v5 each module's records are in its own table, and this process
	// holds the oidc one (and read-only views of the modules it reads): a module
	// that still runs here writes through its own table (ADR 0072). On layout v4
	// there is one table and the three are the one base.
	forModule := func(m port.Module) (*portstore.Base, error) {
		if st.Ports.Module == "" {
			return base, nil
		}
		set, err := st.ForModule(m)
		if err != nil {
			return nil, fmt.Errorf("store: ports.adapter %s: %w", st.Adapter, err)
		}
		return baseOf(set), nil
	}
	google, err := forModule(port.ModuleGoogle)
	if err != nil {
		return stores{}, err
	}
	if cfg.Reader && st.Ports.Module != "" {
		// The Google module is another function's: this process reads its records
		// and writes none, whatever its role would allow (v1.76 E-IAM).
		set, err := st.ForModule(port.ModuleGoogle)
		if err != nil {
			return stores{}, fmt.Errorf("store: ports.adapter %s: %w", st.Adapter, err)
		}
		set.State, set.Blob, set.Secrets = port.ReadOnlyState(set.State), port.ReadOnlyBlob(set.Blob), nil
		google = baseOf(set)
	}
	// A module that is another function's is not opened here: its tables, blobs
	// and credentials are its own, and the issuer's role holds no grant on them.
	var github, slack *portstore.Base
	if !cfg.GitHubRemote {
		if github, err = forModule(port.ModuleGitHub); err != nil {
			return stores{}, err
		}
	}
	if !cfg.SlackRemote {
		if slack, err = forModule(port.ModuleSlack); err != nil {
			return stores{}, err
		}
	}
	// That Secrets is configured is checked now; the session key is read, or
	// created the first time, when a session or a state is first signed.
	if err := base.RequireSecrets(); err != nil {
		return stores{}, fmt.Errorf("store: ports.adapter %s: %w", st.Adapter, err)
	}
	sessionKey := lazy.New(lazy.TTL, func(ctx context.Context) ([]byte, error) {
		key, err := base.SessionKey(ctx, access.NewSessionKey)
		if err != nil {
			return nil, fmt.Errorf("the Secrets port does not answer: %w", err)
		}
		return key, nil
	})
	log.InfoContext(ctx, "keeping the domain records in the state port, credentials in Secrets",
		slog.String("adapter", st.Adapter), slog.Bool("shared", st.Shared))
	out := stores{
		workspaces: portstore.NewWorkspaces(google),
		settings:   cfg.oauthDeclared,
		sessionKey: func() ([]byte, error) {
			// Signing has no request context to read under; the read is bounded.
			ctx, cancel := context.WithTimeout(context.Background(), secretReadTimeout)
			defer cancel()
			return sessionKey.Get(ctx)
		},
	}
	if !cfg.Reader {
		// The workspaces' credentials are the Google module's; a hub that only
		// reads its snapshots holds none (`google.remote`).
		out.credentials = portstore.NewCredentials(google)
	}
	if github != nil {
		out.githubReports = rails.NewBlobReports(st.Ports.Blob, blobprefix.GitHubReports)
		out.githubOrgs = portstore.NewGitHubOrgs(github)
		out.githubLinks = portstore.NewGitHubLinks(github)
		out.githubRunnerApps = portstore.NewGitHubRunnerApps(github)
		out.githubCatalogueApps = portstore.NewGitHubCatalogueApps(github)
	}
	if slack != nil {
		out.slackReports = rails.NewBlobReports(st.Ports.Blob, blobprefix.SlackReports)
		out.slackCatalogueApps = portstore.NewSlackCatalogueApps(slack)
		out.slackShared = portstore.NewSlackShared(slack)
		out.slackChannels = portstore.NewSlackChannels(slack)
		out.slackWorkspaces = portstore.NewSlackWorkspaces(slack)
	}
	// The flag of a module that is another function's is read by that module.
	out.maintenance = maintenance.Set{
		port.ModuleOIDC:   st.Maintenance(),
		port.ModuleGoogle: st.MaintenanceOf(port.ModuleGoogle),
	}
	if !cfg.GitHubRemote {
		out.maintenance[port.ModuleGitHub] = st.MaintenanceOf(port.ModuleGitHub)
	}
	if !cfg.SlackRemote {
		out.maintenance[port.ModuleSlack] = st.MaintenanceOf(port.ModuleSlack)
	}
	if !cfg.CloudflareRemote {
		out.maintenance[port.ModuleCloudflare] = st.MaintenanceOf(port.ModuleCloudflare)
	}
	useCluster(&out, cfg, st)
	return out, nil
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// App is an assembled hub: three handlers and the background loops.
type App struct {
	// fatal carries the one error that ends the process from outside a
	// request: the audit installation refusing the catalogue after the start.
	fatal   chan error
	console http.Handler
	health  http.Handler
	ready   health.Dependency
	server  *server.ConsoleServer
	policy  *policy.Set
	hub     *hub.Hub
	cfg     Config
	log     *slog.Logger
	close   func()
	audit   audit.Recorder
	// catalogueApps is where created catalogue Apps are kept, nil where
	// the deployment keeps no state in Kubernetes.
	catalogueApps server.GitHubCatalogueApps
	// githubMints is the last installation tokens asked of each App: made
	// here, read by the console's Apps pages, written by the half that
	// mints them. Nil where the deployment declares no App.
	githubMints *mints.Ring
}

// Audit is the service's one recorder, for the half assembled after this
// one: both halves write one history.
func (a *App) Audit() audit.Recorder { return a.audit }

// AuditQuery is the audit installation's query service and the audience
// the console's tokens for it carry, or an empty URL when none is
// connected. The merged service wires it to the issuer, which mints them.
func (a *App) AuditQuery() (queryURL, audience string) { return a.cfg.auditQuery, a.cfg.auditAudience }

// AuditRequests puts what an audit record keeps of each request into the
// context of everything next serves, with this deployment's decision on
// X-Forwarded-For. The merged service wraps the issuer's whole origin in
// it, so a token exchange records where it came from as a console call
// does.
func (a *App) AuditRequests(next http.Handler) http.Handler {
	return server.AuditRequests(a.cfg.auditForwardedForTrustedHops, next)
}

// ConsoleHandler is the operator listener: services, login, the SPA.
func (a *App) ConsoleHandler() http.Handler { return a.console }

// HealthHandler is liveness and readiness.
func (a *App) HealthHandler() http.Handler { return a.health }

// Hub is the directory hub itself, for a caller that drives it directly.
func (a *App) Hub() *hub.Hub { return a.hub }

// ConsoleServer is the console's own server, for a caller that has to
// finish wiring it after both halves exist.
func (a *App) ConsoleServer() *server.ConsoleServer { return a.server }

// Policy is the policy in force, for a caller that has to act on the
// SAME one.
//
// The merged service loads it once and hands it to both halves.
// Two halves loading it independently is precisely the class
// of failure the merge existed to end: they read the same file today,
// but their fallbacks differ, so a deployment that configured neither
// would run a directory answering from a built-in policy and an issuer
// refusing to start — or worse, two policies that agree until one of
// them is changed.
func (a *App) Policy() *policy.Set { return a.policy }

// GitHubCatalogue is every GitHub App the deployment declares, for the
// half that mints their installation tokens under its grants.
func (a *App) GitHubCatalogue() *catalogue.Catalogue { return a.cfg.githubCatalogue }

// GitHubCatalogueApps is where created catalogue Apps and their keys are
// kept, or nil where the deployment keeps none: what the issuer half
// reads an App's key from to mint a token.
func (a *App) GitHubCatalogueApps() server.GitHubCatalogueApps { return a.catalogueApps }

// GitHubMints is the ring of recent installation token requests, for the
// half that mints them to write into. The console reads the same ring:
// an App's page shows what this service minted without asking the audit
// trail for a listing narrowed to one App, which is a scan.
func (a *App) GitHubMints() *mints.Ring { return a.githubMints }

// Readiness is the snapshot store as a dependency, for a caller that
// assembles a health endpoint of its own. The merged service has one
// /readyz answering for both halves, and a half that cannot read a
// snapshot cannot answer anything.
func (a *App) Readiness() health.Dependency { return a.ready }

// RunLoops drives the refresher and the probes, and serves nothing.
//
// It is what the merged service runs: one process, one set of
// listeners, and this half contributing its background work rather than
// three listeners of its own.
func (a *App) RunLoops(ctx context.Context) error {
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.hub.Run(gctx) })
	return group.Wait()
}

// Close releases what New opened.
func (a *App) Close() {
	if a.close != nil {
		a.close()
	}
}

// New assembles the hub. The caller runs it with [App.Run] and releases
// it with [App.Close].
func New(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	oauthDeclared, err := declaredOAuthSettings(cfg.oauthClient, st.Secrets)
	if err != nil {
		return nil, fmt.Errorf("oauthClient: %w", err)
	}
	cfg.oauthDeclared = oauthDeclared
	kept, err := openStores(ctx, cfg, st, log)
	if err != nil {
		return nil, err
	}
	snapshots := openSnapshots(ctx, st, log)
	// The audit trail is an installation of this service's own, in its
	// namespace; without one, every record is a log line and nothing more.
	// A refusal found after the start is fatal the way one at the start is:
	// the process ends rather than running with records nobody accepts.
	fatal := make(chan error, 1)
	cfg.audit.Log = log
	cfg.audit.OnFatal = func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	// The audit concern of the resolved table decides the sink; the legacy
	// `audit.writer` is what `connect` means.
	if cfg.audit, err = audit.FromPlan(cfg.audit, st.Plan); err != nil {
		return nil, err
	}
	recorder, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return nil, err
	}
	closeStores := func() {
		if err := recorder.Close(); err != nil {
			log.WarnContext(ctx, "the audit emitter could not be closed cleanly; what its queue held is dropped",
				slog.Any("error", err))
		}
	}

	directory := hub.New(kept.workspaces, snapshots, cfg.freshness, log)
	if kept.credentials != nil {
		directory.UseCredentials(kept.credentials)
	}
	if g := kept.maintenance.Of(port.ModuleGoogle); g != nil {
		// The snapshots are the google module's: a restore is writing them.
		directory.UseMaintenance(g)
	}

	if n := cfg.policy.LegacyAppEntries(); n > 0 {
		log.WarnContext(ctx, "apps.github.runnerTiers and apps.github.catalogue are deprecated, and still read as apps.github.apps",
			slog.Int("entries", n))
	}
	declared, err := declaredPolicy(cfg.policy, cfg.demo)
	if err != nil {
		return nil, err
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		return nil, err
	}

	// A name is a convention, not a rule, so this is said and not
	// refused: the policy works either way, and an installation mid-
	// rename holds both shapes at once. What the convention buys is that
	// a reader tells a grant from an identity by looking, and the place
	// to notice a name that broke it is here, at load, rather than in a
	// token six months later.
	if odd := declared.UnconventionalGroups(); len(odd) > 0 {
		log.WarnContext(ctx, "some group names are neither a grant nor an identity",
			slog.Any("groups", odd),
			slog.String("grant", "<scope>:<thing>:<role>"),
			slog.String("identity", "rung:<name>, emp:<slug>"))
	}

	authorizer := access.NewAuthorizer(set, directory, cfg.holdWindow)

	sessionKey := kept.sessionKey
	sessions := access.NewSessionsWith(sessionKey, cappedSessionLifetime(cfg.sessionLifetime, cfg.absoluteLifetime), cfg.secureCookies)

	var recoveryDependency health.Dependency
	recovery, err := openRecovery(ctx, cfg, kept, st.Secrets, st.V5, log)
	if err != nil {
		return nil, err
	}
	if password, ok := recovery.(*server.PasswordRecovery); ok {
		// The exchanges of the password sign-in are kept in the State, so that any
		// container may answer the second request. Without a shared State this
		// process is the only one there is.
		var exchanges port.State = memory.New()
		if st.Usable {
			exchanges = st.Ports.State
		}
		password.Open(exchanges, sessionKey)
		if password.Check != nil {
			// The verifier is derived from a password this hub reads: held to the
			// strength a verifier needs now, so a weak one is an ERROR at start and
			// a hub that is not ready, not a 503 found during an incident.
			if !cfg.ReadsNoSecretAtStart {
				checkCtx, cancel := context.WithTimeout(ctx, secretReadTimeout)
				if err = password.Check(checkCtx); err != nil {
					log.ErrorContext(ctx, "recovery sign-in will be refused: its password cannot sit behind a verifier",
						logattr.SafeError("error", err))
				}
				cancel()
			}
			recoveryDependency = recoveryReadiness(password.Check)
		}
	}
	recoveryGate := cfg.RecoveryGate
	if recoveryGate == nil {
		recoveryGate = recoverygate.New(st.RecoveryState(), cfg.recoveryGeneration, recoverygate.WithLog(log))
	}

	oauthClient := func() (google.OAuthClient, error) {
		// The client is read when /connect first needs it; the read is bounded.
		rctx, cancel := context.WithTimeout(context.Background(), secretReadTimeout)
		defer cancel()
		stored, err := kept.settings.OAuthClient(rctx)
		if err != nil {
			return google.OAuthClient{}, err
		}
		if !stored.Configured() {
			return google.OAuthClient{}, errors.New(
				"no OAuth client is registered yet: add one in Settings, or declare it in the deployment")
		}
		return google.OAuthClient{ID: stored.ID, Secret: stored.Secret, BaseURL: cfg.publicURL}, nil
	}

	connectors := []server.Connector{connector.NewGoogle(oauthClient)}
	if cfg.GoogleIssuer != nil {
		// The OAuth client and its secret are the Google module's: this hub
		// offers the kind and signs people in through it.
		connectors = []server.Connector{googleconsole.NewRemoteConnector(cfg.GoogleIssuer)}
	}
	loginSources := []string{}
	if cfg.demo {
		connectors = append(connectors, seedDemo(ctx, directory, cfg.publicURL, log))
	}
	if !cfg.loginDirectory {
		log.InfoContext(ctx, "the hub's own sign-in is off: the ways in are a gateway that "+
			"forwards an identity, and recovery. Connecting a directory is unaffected")
	}
	if cfg.Reader {
		// The workspaces, their readers and their credentials are the Google
		// module's; the snapshots it writes are all this hub reads.
		directory.UseReader()
		log.InfoContext(ctx, "the directory is read from the Google module's snapshots; this hub refreshes nothing")
	}
	if !cfg.Reader {
		adopted, err := hubwire.AdoptDeclared(ctx, directory, kept.workspaces, cfg.workspaces, st.Secrets, openBackend, log)
		if err != nil {
			return nil, err
		}
		if kept.credentials != nil {
			reopen := func(ctx context.Context, kind string, cred backend.Credential) (backend.Backend, error) {
				return openStored(ctx, connectors, kind, cred)
			}
			if err = hubwire.ReopenStored(ctx, directory, kept.workspaces, kept.credentials, adopted, reopen, log); err != nil {
				return nil, err
			}
			// And from here the hub can open a workspace on its own. Reopening
			// at start covers what existed at start; this covers a workspace
			// another replica connected a minute ago, which start-up cannot
			// know about.
			directory.UseReopener(func(ctx context.Context, ws hub.Workspace, cred backend.Credential) (backend.Backend, error) {
				return openStored(ctx, connectors, ws.Backend, cred)
			})
		}
	}
	// Two forwarded paths, reported apart: an operator reading the start-up
	// line should be able to tell a console that VERIFIES a gateway's token
	// from one that TRUSTS a gateway's header, because the second is only
	// as strong as whatever keeps other pods off this port.
	if cfg.forwardedIssuer != "" {
		loginSources = append(loginSources, "forwarded-bearer:"+cfg.forwardedIssuer)
	}
	if cfg.forwardedHeader != "" {
		loginSources = append(loginSources, "forwarded-header:"+cfg.forwardedHeader)
	}
	if cfg.recoveryEnabled {
		loginSources = append(loginSources, "recovery")
	}
	if cfg.loginDirectory {
		loginSources = append(loginSources, "directory")
	}

	// A demonstration run's Apps are signed for with a key made here, so
	// nothing outside this process ever accepts it.
	var demoAppKey string
	if cfg.demo {
		if demoAppKey, err = demo.GitHubAppKey(); err != nil {
			return nil, fmt.Errorf("a key for the demonstration Apps: %w", err)
		}
	}

	// The last installation tokens asked of each App, kept beside the half
	// that mints them so that an App's page need not ask the audit trail
	// for a listing narrowed to one App -- which is a scan of every hour's
	// objects, and outlived a gateway's patience. Only where an App is
	// declared: nothing else can mint one.
	var githubMints *mints.Ring
	if cfg.githubCatalogue != nil && len(cfg.githubCatalogue.Apps) > 0 {
		githubMints = mints.New(0, time.Now())
		if cfg.demo {
			demo.SeedGitHubMints(githubMints, time.Now())
		}
	}

	console, err := server.NewConsole(ctx, server.ConsoleDeps{
		Trigger:      st.Ports.Trigger,
		Log:          log,
		Hub:          directory,
		Authorizer:   authorizer,
		Settings:     kept.settings,
		State:        access.NewStateCodecWith(sessionKey, 10*time.Minute),
		Connectors:   connectors,
		Modules:      consolecall.Callers{Google: cfg.GoogleConsole},
		Recovery:     recovery,
		RecoveryGate: recoveryGate,
		Instance:     cfg.instance,
		LoginSources: loginSources,
		CacheBackend: st.Name(),
		SecureCookie: cfg.secureCookies,
		PublicURL:    cfg.publicURL,
		RootURL:      cfg.publicRootURL,
		IssuerURL:    cfg.forwardedIssuer,
		SignIn:       cfg.loginDirectory,
		GitHub:       githubReports(kept.githubReports, cfg.demo),
		GitHubOrgs:   githubConnections(kept.githubOrgs, cfg.demo, demoAppKey),
		// Typed nils again: an interface holding a nil store is not nil.
		GitHubLinkApp:       githubLinkApp(kept.githubOrgs, cfg.demo),
		GitHubLinks:         githubLinks(kept.githubLinks, cfg.demo),
		GitHubConfirmations: githubConfirmations(kept.githubOrgs),
		GitHubRunnerApps:    githubRunnerApps(kept.githubRunnerApps, cfg.demo, demoAppKey),
		GitHubRunnerTiers:   cfg.githubRunnerTiers,
		GitHubOrgAppName:    cfg.policy.OrganisationAppName,
		GitHubLinkAppName:   cfg.policy.LinkAppName(),
		GitHubCatalogue:     cfg.githubCatalogue,
		GitHubCatalogueApps: githubCatalogueApps(kept.githubCatalogueApps, cfg.demo, demoAppKey),
		GitHubMints:         githubMints,
		SlackCatalogue:      cfg.slackCatalogue,
		SlackCatalogueApps:  kept.slackCatalogueApps,
		SlackShared:         kept.slackShared,
		SlackChannels:       kept.slackChannels,
		SlackStatus:         kept.slackReports,
		SlackWorkspaces:     kept.slackWorkspaces,
		GitHubHTTP:          demoGitHub(cfg.demo && kept.githubCatalogueApps == nil),
		Audit:               recorder,
	})
	if err != nil {
		return nil, err
	}

	consoleServer := server.NewConsoleServer(server.ConsoleServerDeps{
		Console:      console,
		Authorizer:   authorizer,
		Sessions:     sessions,
		State:        access.NewStateCodecWith(sessionKey, 10*time.Minute),
		Connectors:   connectors,
		Hub:          directory,
		Recovery:     recovery,
		RecoveryGate: recoveryGate,
		Maintenance:  kept.maintenance,
		Forwarded: server.ForwardedIdentity{
			Issuer:      cfg.forwardedIssuer,
			Audience:    cfg.forwardedAudience,
			EmailHeader: cfg.forwardedHeader,
		},
		SignIn:     cfg.loginDirectory,
		SignOutURL: cfg.signOutURL,
		// Where this console sits on its origin, read from the address it
		// is published at rather than configured twice. The handlers
		// never see the prefix — the gateway strips it, and so does the
		// merged process — but every link they hand a browser has to
		// carry it.
		Mount:                   mountOf(cfg.publicURL),
		ForwardedForTrustedHops: cfg.auditForwardedForTrustedHops,
		HSTS:                    cfg.hsts,
		Log:                     log,
		UI:                      frontend.FS(),
	})

	// Readiness follows the snapshot store; liveness does not. A hub
	// that cannot read a snapshot cannot answer anything, and saying
	// ready through that is how a moved Valkey became a half-hour of
	// hanging requests on 2026-09-10 with every pod green.
	ready := bothReady(health.Follow("the snapshot store", st.Readiness()), recoveryDependency)
	healthMux := health.Mux(0, ready)

	// "the directory", not the old service name: in the merged process
	// this is one half of one deployment, and a line naming a service
	// reads as a second one having started.
	log.InfoContext(ctx, "the directory is assembled",
		slog.Int("console", cfg.consolePort), slog.Int("health", cfg.healthPort),
		slog.Bool("demo", cfg.demo), slog.String("recovery", recoveryKind(recovery)), slog.String("public", cfg.publicURL),
		slog.Bool("sign_in", cfg.loginDirectory), slog.String("cache", st.Name()), slog.String("store", cfg.store),
		slog.String("version", version.String()), slog.String("policy", policySource(cfg.policy, cfg.demo)))

	return &App{
		fatal:   fatal,
		console: consoleServer.Handler(),
		health:  healthMux,
		ready:   ready,
		server:  consoleServer,
		policy:  set,
		hub:     directory,
		cfg:     cfg,
		log:     log,
		close:   closeStores,
		audit:   recorder,

		catalogueApps: githubCatalogueApps(kept.githubCatalogueApps, cfg.demo, demoAppKey),
		githubMints:   githubMints,
	}, nil
}

// Run serves the three listeners and drives the hub's background loops
// until the context is done.
func (a *App) Run(ctx context.Context) error {
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return serve(gctx, a.cfg.consolePort, a.console, "console", a.log) })
	group.Go(func() error { return serve(gctx, a.cfg.healthPort, a.health, "health", a.log) })
	group.Go(func() error { return a.RunLoops(gctx) })
	group.Go(func() error {
		select {
		case err := <-a.fatal:
			return fmt.Errorf("audit: %w", err)
		case <-gctx.Done():
			return nil
		}
	})
	return group.Wait()
}

// serve runs one listener until the context is done, then drains it.
func serve(ctx context.Context, port int, handler http.Handler, name string, log *slog.Logger) error {
	// Bounded in what it reads and how long it waits: see [httpsec].
	srv := httpsec.NewServer(fmt.Sprintf(":%d", port),
		refusal.Middleware(httpsec.LimitBody(handler, httpsec.MaxRequestBody)))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.WarnContext(ctx, "listener did not drain", slog.String("listener", name), slog.Any("error", err))
		}
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s listener: %w", name, err)
	}
	return nil
}

// mountOf is the path part of the address the console is published at:
// "/console" from https://access.example/console, and empty from
// https://console.example, which is a console with an origin to itself.
//
// An unparseable address yields no prefix, which is the standalone shape
// and the one that was right before any of this existed.
func mountOf(publicURL string) string {
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(parsed.Path, "/")
}

// builtinPolicy is what a hub with no declared policy starts from: the two
// groups it is a relying party of, empty. Nobody is in them, so nobody but
// the break-glass admin can act until an operator attaches the first
// directory group in the console — which is day one, exactly as the
// runbook describes it.
const builtinPolicy = `
version: 1
groups:
  all:access-roster:operator: {}
  all:access-roster:viewer: {}
lifetimes:
  default: 12h
`

// declaredPolicy is the deployment's policy: the policy document's tables.
// With none, a demonstration run gets one rich enough to watch every mechanic
// work, and anything else gets the built-in two groups.
func declaredPolicy(p *config.PolicyDocument, demonstration bool) (policy.Policy, error) {
	if p != nil {
		return p.Policy, nil
	}
	return FallbackPolicy(demonstration)
}

// FallbackPolicy is what a service whose document names no policy decides by:
// the demonstration's policy under `demo`, and the built-in two groups
// otherwise. The composition root hands it to [config.LoadConfig].
func FallbackPolicy(demonstration bool) (policy.Policy, error) {
	if demonstration {
		return policy.Parse([]byte(demo.Policy))
	}
	return policy.Parse([]byte(builtinPolicy))
}

// openStored turns a stored credential back into a reader, by asking
// whichever connector this build registered for that kind: the same list
// the console offers an operator to connect a workspace the first time.
//
// Dispatching through that list rather than a hardcoded kind is what lets
// a second backend (Entra, say) come back after a restart the moment it
// is registered next to Google above, with nothing here to touch. A kind
// with no connector, or a connector wired in without a stored-credential
// side, is refused by name: widening what a restart accepts beyond what a
// fresh connection accepts would mean a directory could be read here that
// could never have been connected through the console at all.
func openStored(
	ctx context.Context, connectors []server.Connector, kind string, cred backend.Credential,
) (backend.Backend, error) {
	for _, conn := range connectors {
		if conn.Kind() != kind {
			continue
		}
		reopener, ok := conn.(server.CredentialReopener)
		if !ok {
			return nil, fmt.Errorf("this build cannot reopen a %q workspace from a stored credential", kind)
		}
		return reopener.OpenStored(ctx, cred)
	}
	known := make([]string, 0, len(connectors))
	for _, conn := range connectors {
		known = append(known, conn.Kind())
	}
	slices.Sort(known)
	return nil, fmt.Errorf("unknown backend %q; this build connects %v", kind, known)
}

// backendOpeners is how a build declares which directories it can read.
//
// It is a registry rather than a switch so that adding a backend is a
// registration next to the backend itself, and so that a build without
// one fails by naming exactly what it lacks. The map is empty today: the
// hub reads real directories from its 1.0, and until then a deployment
// that declares a workspace is told so at start rather than left to
// discover it from a console with nothing in it.
var backendOpeners = map[string]func(ctx context.Context, d *hub.Declared) (backend.Backend, error){
	"google": func(ctx context.Context, d *hub.Declared) (backend.Backend, error) {
		return google.Open(ctx, d.Key, d.Admin)
	},
}

// openBackend builds the reader for a declared workspace.
//
// A backend this build does not carry is an error naming what was asked
// for. The alternative — ignoring the entry — is how a deployment ends up
// believing it reads a directory it has never once opened.
func openBackend(ctx context.Context, declared *hub.Declared) (backend.Backend, error) {
	open, ok := backendOpeners[declared.Backend]
	if !ok {
		known := slices.Sorted(maps.Keys(backendOpeners))
		if len(known) == 0 {
			return nil, fmt.Errorf("this build reads no directory backend yet, and %q was declared", declared.Backend)
		}
		return nil, fmt.Errorf("unknown backend %q; this build reads %v", declared.Backend, known)
	}
	return open(ctx, declared)
}

// seedDemo adopts the demonstration tenants and returns their connector.
func seedDemo(ctx context.Context, directory *hub.Hub, publicURL string, log *slog.Logger) *demo.Connector {
	connector := demo.NewConnector(publicURL)
	tenants := demo.Tenants(time.Now())
	for i := range tenants {
		tenant := &tenants[i]
		connector.Adopt(tenant.Workspace.ID, tenant.Backend)
		if _, err := directory.Adopt(ctx, tenant.Workspace, tenant.Backend); err != nil {
			log.WarnContext(ctx, "demonstration tenant could not be adopted",
				slog.String("workspace", tenant.Workspace.ID), slog.Any("error", err))
		}
	}
	log.InfoContext(ctx, "demonstration tenants adopted; no credential and no network is involved")
	return connector
}

// policySource says where the policy came from, for the startup line.
func policySource(p *config.PolicyDocument, demonstration bool) string {
	switch {
	case p != nil:
		return "declared"
	case demonstration:
		return "demonstration"
	default:
		return "built-in"
	}
}

func generatedPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate the admin password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// announceRecoveryPassword prints the generated password once. It is only
// reached outside a cluster: in one, recovery proves cluster access and
// there is no password to print.
func announceRecoveryPassword(password string) {
	fmt.Fprintf(os.Stderr, "\n  recovery password (generated for this run): %s\n"+
		"  Sign in at /login, under Recovery sign-in.\n\n",
		password)
}

// cappedSessionLifetime is how long the console's OWN session cookie is
// issued for: lifetimes.session, or the absolute session limit, whichever
// is shorter.
//
// This cookie is issued once, at sign-in, with a fixed expiry -- unlike a
// per-client refresh token it never slides -- so its issue time already
// IS its auth_time, and capping it at the limit is exactly capping the
// lifetime it is issued for. Zero or negative means no limit, which a
// deployment can only reach by way of a bug: issuerapp.Load refuses that
// value outright, but this package has no such check of its own (see
// [Config.absoluteLifetime]), so a caller with a broken value is still
// answered sanely rather than capping every session to nothing.
func cappedSessionLifetime(session, absolute time.Duration) time.Duration {
	if absolute > 0 && absolute < session {
		return absolute
	}
	return session
}

// recoveryKind names the shape of the recovery path for the startup line.
func recoveryKind(r server.Recovery) string {
	if r == nil {
		return "none"
	}
	return r.Kind()
}

// githubReports is the store as the console's interface, or nil. A typed
// nil stored in an interface is not nil, and the console reads nil as
// "this deployment keeps no reports" — so the conversion is explicit.
//
// A demonstration run with no Kubernetes gets the demonstration report
// instead, so the GitHub page can be walked through like every other.
func githubReports(store server.GitHubReports, demonstration bool) server.GitHubReports {
	switch {
	case store != nil:
		return store
	case demonstration:
		return fixedReports(demo.GitHubReports(time.Now()))
	default:
		return nil
	}
}

// fixedReports is a report that never changes.
type fixedReports map[string]string

func (r fixedReports) Reports(context.Context) (map[string]string, error) { return r, nil }

// githubConnections is the store as the console's interface, or nil — for
// the same typed-nil reason as githubReports. A demonstration run with no
// Kubernetes shows its one organisation connected, and connects nothing.
func githubConnections(store githubOrgStore, demonstration bool, key string) server.GitHubConnections {
	switch {
	case store != nil:
		return store
	case demonstration:
		return demoConnections{record: demo.GitHubConnection(time.Now()), key: key}
	default:
		return nil
	}
}

// githubRunnerApps is the store as the console's interface, or nil. A
// demonstration run shows one tier's App created and one left to create.
func githubRunnerApps(store server.GitHubRunnerApps, demonstration bool, key string) server.GitHubRunnerApps {
	switch {
	case store != nil:
		return store
	case demonstration:
		return demoRunnerApps{records: demo.GitHubRunnerApps(time.Now()), key: key}
	default:
		return nil
	}
}

// githubCatalogueApps is the store as the console's interface, or nil. A
// demonstration run shows Apps created from its catalogue, one of them
// edited on GitHub since.
func githubCatalogueApps(store server.GitHubCatalogueApps, demonstration bool, key string) server.GitHubCatalogueApps {
	switch {
	case store != nil:
		return store
	case demonstration:
		return demoCatalogueApps{records: demo.GitHubCatalogueApps(time.Now()), key: key}
	default:
		return nil
	}
}

// demoRunnerApps are fixed runner Apps that nobody can create or forget.
type demoRunnerApps struct {
	records []runnerapp.Record
	key     string
}

func (demoRunnerApps) Put(context.Context, runnerapp.Record, string) error { return errDemoConnect }

func (d demoRunnerApps) List(context.Context) ([]runnerapp.Record, error) { return d.records, nil }

func (d demoRunnerApps) PrivateKey(_ context.Context, tier, org string) (string, bool, error) {
	for i := range d.records {
		if d.records[i].Tier == tier && d.records[i].Org == org {
			return d.key, true, nil
		}
	}
	return "", false, nil
}

func (demoRunnerApps) Delete(context.Context, string, string) error { return errDemoConnect }

// demoCatalogueApps are fixed catalogue Apps, with a key this process
// made: the console asks the demonstration's GitHub about them as it would
// ask the real one.
type demoCatalogueApps struct {
	records []catalogueapp.Record
	key     string
}

func (demoCatalogueApps) Put(context.Context, catalogueapp.Record, string) error {
	return errDemoConnect
}

func (d demoCatalogueApps) List(context.Context) ([]catalogueapp.Record, error) {
	return d.records, nil
}

func (d demoCatalogueApps) Get(_ context.Context, id string) (catalogueapp.Record, string, bool, error) {
	for i := range d.records {
		if d.records[i].ID == id {
			return d.records[i], d.key, true, nil
		}
	}
	return catalogueapp.Record{}, "", false, nil
}

func (demoCatalogueApps) Delete(context.Context, string) error { return errDemoConnect }

func (demoCatalogueApps) PutWebhookSecret(context.Context, string, string) error {
	return errDemoConnect
}

func (demoCatalogueApps) WebhookSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// githubLinkApp is the store as the console's interface, or nil. A
// demonstration run shows a link App already created.
func githubLinkApp(store githubOrgStore, demonstration bool) server.GitHubLinkApp {
	switch {
	case store != nil:
		return store
	case demonstration:
		return demoLinkApp{app: demo.GitHubLinkApp(time.Now())}
	default:
		return nil
	}
}

// githubLinks is the store as the console's interface, or nil. A
// demonstration run shows a few links, and links nobody.
func githubLinks(store server.GitHubLinks, demonstration bool) server.GitHubLinks {
	switch {
	case store != nil:
		return store
	case demonstration:
		return demoLinks{links: demo.GitHubLinks(time.Now())}
	default:
		return nil
	}
}

// demoGitHub is the demonstration's GitHub, or nil for the real one: the
// console asks it about the demonstration's Apps exactly as it asks GitHub
// about real ones, so the Apps page shows an App matching its declaration
// beside one an owner edited since.
func demoGitHub(demonstration bool) *http.Client {
	if !demonstration {
		return nil
	}
	return &http.Client{Transport: demo.GitHubAPI(), Timeout: 10 * time.Second}
}

// demoLinkApp is a fixed link App that nobody can authorize.
type demoLinkApp struct{ app link.App }

func (d demoLinkApp) LinkApp(context.Context) (link.App, bool, error) { return d.app, true, nil }

func (demoLinkApp) LinkAppCredential(context.Context) (link.AppCredential, bool, error) {
	return link.AppCredential{}, false, nil
}

func (demoLinkApp) PutLinkApp(context.Context, link.App, link.AppCredential) error {
	return errDemoConnect
}

func (demoLinkApp) DeleteLinkApp(context.Context) error { return errDemoConnect }

// demoLinks are fixed links.
type demoLinks struct{ links []link.Link }

func (d demoLinks) List(context.Context) ([]link.Link, error) { return d.links, nil }

func (demoLinks) Claim(context.Context, link.Link, time.Time) ([]link.Link, error) {
	return nil, errDemoConnect
}

func (demoLinks) Invalidate(context.Context, string, time.Time) (int, error) {
	return 0, errDemoConnect
}

func (demoLinks) Adopt(context.Context, []link.Link) ([]link.Link, map[int64]string, error) {
	return nil, nil, errDemoConnect
}

// githubConfirmations is the store as the console's interface, or nil. A
// demonstration run confirms nothing: there is nothing to remove.
func githubConfirmations(store githubOrgStore) server.GitHubConfirmations {
	if store == nil {
		return nil
	}
	return store
}

// errDemoConnect is what a demonstration run says when asked to change a
// connection: there is no GitHub organisation behind it.
var errDemoConnect = errors.New("a demonstration run connects and disconnects nothing: there is no GitHub organisation behind it")

// demoConnections is one fixed, connected organisation, with a key the
// console asks the demonstration's GitHub about the App with.
type demoConnections struct {
	record connection.Record
	key    string
}

func (d demoConnections) List(context.Context) ([]connection.Record, error) {
	return []connection.Record{d.record}, nil
}

func (d demoConnections) Credential(_ context.Context, org string) (connection.Credential, bool, error) {
	if org != d.record.Org {
		return connection.Credential{}, false, nil
	}
	return connection.Credential{
		Org: d.record.Org, AppID: d.record.AppID, InstallationID: d.record.InstallationID, PrivateKey: d.key,
	}, true, nil
}

func (demoConnections) Put(context.Context, connection.Record, connection.Credential) error {
	return errDemoConnect
}

func (demoConnections) Delete(context.Context, string) error { return errDemoConnect }

func (demoConnections) RequestPass(context.Context, connection.PassRequest) (bool, time.Time, error) {
	return false, time.Time{}, errDemoConnect
}

func (demoConnections) PassRequests(context.Context) (map[string]connection.PassRequest, error) {
	return nil, nil
}

func (demoConnections) SetOwner(context.Context, string, string) (string, bool, error) {
	return "", false, errDemoConnect
}

// AuditConfig is the audit configuration the legacy `audit` keys say, before the
// resolved adapter table is applied to it (audit.FromPlan).
func (c Config) AuditConfig() audit.Config { return c.audit }
