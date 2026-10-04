// Package issuerapp assembles the token service from its configuration:
// the policy, the door to the hub, the signing key, the verifiers that
// turn somebody else's token into a proof, and the OpenID surface over
// all of it.
//
// A package rather than the body of main, for the reason the hub's
// equivalent is: this is where the decisions a deployment can get wrong
// are made, and main() cannot be tested.
package issuerapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/errgroup"

	"github.com/truvity/sluis/backend/google"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/telemetry"
	"github.com/truvity/sluis/internal/verify"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/policy"
)

// Config is what a deployment decides. It is built from the configuration
// file, which is what the chart renders.
type Config struct {
	port       string
	healthPort string

	issuerURL     string
	allowInsecure bool

	policyPath string

	inCluster         bool
	release           string
	oauthClientID     string
	oauthClientSecret string
	secureCookies     bool
	oauthSecretFile   string
	oauthIDFile       string
	recoveryEnabled   bool
	recoveryAccount   string
	cluster           string
	recoveryAudience  string
	clientSecretsDir  string
	clustersPath      string
	awsPath           string
	// consoleAWSAudience is the audience an AWS role's token must carry to be a
	// bearer at the console, distinct from the exchange's.
	consoleAWSAudience string
	consoleClientID    string
	audience           string
	githubOwners       []string
	consoleOrigin      string
	signingKeyFile     string
	// additionalSigningKeyFiles are every OTHER algorithm this
	// installation signs with at once, one file per algorithm, beside the
	// primary [Config.signingKeyFile] -- see [issuer.KeyRings] and the
	// chart's `signingKey.additional`.
	additionalSigningKeyFiles []string
	// kmsKeys, when set, replace signingKeyFile: AWS KMS keys, oldest
	// first, the last signing. kmsStateSecretFile is what the sign-in state
	// derives from, since a KMS key has no private bytes of its own.
	kmsKeys            []string
	kmsAdditional      []config.SigningKeyKMSAlg
	kmsRegion          string
	kmsStateSecretFile string
	// kmsWrapped, when set, replaces signingKeyFile: key pairs KMS generates
	// and wraps under one symmetric key (the `kms-wrapped` adapter).
	kmsWrapped *config.SigningKeyKMSWrapped

	tokenLifetime    time.Duration
	refreshLifetime  time.Duration
	absoluteLifetime time.Duration
	holdWindow       time.Duration

	// keyActivationDelay, keyOverlap and keyPollInterval govern live
	// signing-key rotation; see [issuer.KeyRingConfig] and
	// [watchSigningKey].
	keyActivationDelay time.Duration
	keyOverlap         time.Duration
	keyPollInterval    time.Duration

	logLevel slog.Level

	// groupsScoping is how far this installation has moved toward
	// per-audience `groups` scoping -- see [issuer.GroupsScopingMode].
	// Validated by [issuer.CheckGroupsScopingMode] below, so a [Config]
	// past [Load] never carries a value that check refuses.
	groupsScoping issuer.GroupsScopingMode
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// FromConfig builds the process's settings from its configuration file,
// which the caller has already held to its schema. What a schema cannot say
// is checked here, before anything starts: lifetimes that contradict each
// other, a rotation schedule that cannot work, a scoping mode this build does
// not know.
func FromConfig(f *config.Serve) (Config, error) {
	c := Config{
		port:       listenOr(f.Listen, ":8080"),
		healthPort: listenOr(f.Probes, ":7070"),
		issuerURL:  strings.TrimSuffix(f.IssuerURL, "/"),

		allowInsecure:    f.AllowInsecure,
		policyPath:       f.PolicyDir,
		inCluster:        f.InCluster,
		release:          orDefault(f.Release, "sluis"),
		consoleOrigin:    "",
		clientSecretsDir: f.ClientSecretsDir,
		// Names this cluster in a ServiceAccount's subject. A pod cannot
		// discover it, and the same namespace and name exist on every
		// cluster, so an installation that leaves it empty keeps the older
		// unqualified subject rather than an invented one.
		cluster: f.Cluster,
	}
	if f.GitHub != nil {
		c.githubOwners = f.GitHub.Owners
	}
	if f.Console != nil {
		c.consoleOrigin = f.Console.Origin
		c.consoleClientID = f.Console.Client
		c.consoleAWSAudience = strings.TrimSpace(f.Console.AWSAudience)
	}
	if f.Exchange != nil {
		c.clustersPath = f.Exchange.ClustersFile
		c.awsPath = f.Exchange.AWSFile
		c.audience = f.Exchange.Audience
	}
	if r := f.Recovery; r != nil {
		c.recoveryEnabled = r.Enabled != nil && *r.Enabled
		c.recoveryAccount = r.ServiceAccount
		c.recoveryAudience = r.Audience
	}
	var err error
	if o := f.OAuthClient; o != nil {
		c.oauthClientID = o.ID
		c.oauthIDFile = o.IDFile
		c.oauthSecretFile = o.SecretFile
		if o.SecretEnv != "" {
			if c.oauthClientSecret, err = config.Secret(o.SecretEnv); err != nil {
				return Config{}, fmt.Errorf("oauthClient.secretEnv: %w", err)
			}
		}
	}
	// What the storage ports need is read from the same file by [store.FromServe]
	// at start; read here too so a bad value (an unset password variable)
	// is refused with the rest of the configuration.
	if _, err = store.FromServe(f); err != nil {
		return Config{}, err
	}
	if k := f.SigningKey; k != nil {
		c.signingKeyFile = k.File
		if k.KMS != nil {
			if k.File != "" {
				return Config{}, errors.New("signingKey.file and signingKey.kms are exclusive: " +
					"a key is a file or a KMS key, not both")
			}
			if len(k.KMS.Keys) == 0 || k.KMS.StateSecretFile == "" {
				return Config{}, errors.New("signingKey.kms needs keys and stateSecretFile")
			}
			c.kmsKeys = k.KMS.Keys
			c.kmsAdditional = k.KMS.Additional
			c.kmsRegion = k.KMS.Region
			c.kmsStateSecretFile = k.KMS.StateSecretFile
		}
		if k.KMSWrapped != nil {
			if k.File != "" || k.KMS != nil {
				return Config{}, errors.New("signingKey.kmsWrapped is exclusive with signingKey.file and signingKey.kms: " +
					"a key is a file, a KMS key or a KMS-wrapped key pair")
			}
			c.kmsWrapped = k.KMSWrapped
		}
		// The files the deployment expects, named exactly: a deployment
		// naming them is a deployment a missing mount fails LOUDLY for, at
		// start, rather than one that silently signs with fewer algorithms
		// than its policy assumes.
		c.additionalSigningKeyFiles = k.AdditionalFiles
	}
	// Secure follows the scheme the BROWSER will use, which the service
	// knows because it is told its own public URL. Defaulting to false
	// meant an installation that merely forgot to say so served session
	// cookies a proxy could strip onto a plain-http hop, and the alert
	// CodeQL raised was about that default rather than about this line.
	// secureCookies still overrides, in either direction, for the local
	// http listener and for a TLS terminator that is not in the URL.
	c.secureCookies = strings.HasPrefix(c.issuerURL, "https://")
	if f.SecureCookies != nil {
		c.secureCookies = *f.SecureCookies
	}

	l := f.Lifetimes
	if l == nil {
		l = &config.Lifetimes{}
	}
	c.tokenLifetime = dur(l.Token, issuer.DefaultTokenLifetime)
	c.refreshLifetime = dur(l.Refresh, issuer.DefaultRefreshLifetime)
	c.absoluteLifetime = dur(l.Absolute, issuer.DefaultAbsoluteLifetime)
	c.holdWindow = dur(l.Hold, issuer.DefaultHoldWindow)
	// Refused here rather than defaulted, unlike an unset lifetimes.token or
	// lifetimes.refresh: those treat unset as "use the default", but a
	// deployment that sets lifetimes.absolute to zero has said something
	// specific and wrong -- a session that ends before or the instant it
	// begins is not a limit, it is a login that can never complete -- and
	// defaulting past that would hide the mistake instead of refusing it.
	if c.absoluteLifetime <= 0 {
		return Config{}, errors.New(
			"lifetimes.absolute must be positive: a session has to end SOMETIME after sign-in, not before it")
	}
	// And it must be able to outlive at least one access token, or a token
	// minted at the very start of a session would already be past the
	// limit that is meant to end the SESSION, not pre-empt its first
	// token.
	if c.absoluteLifetime < c.tokenLifetime {
		return Config{}, fmt.Errorf(
			"lifetimes.absolute (%s) must be at least lifetimes.token (%s): "+
				"an access token cannot outlive the session that grants it",
			c.absoluteLifetime, c.tokenLifetime)
	}
	k := f.SigningKey
	if k == nil {
		k = &config.SigningKey{}
	}
	c.keyActivationDelay = dur(k.ActivationDelay, issuer.DefaultKeyActivationDelay)
	// Overlap defaults to this deployment's OWN token lifetime plus a margin
	// for clock skew, rather than the package's constant: the whole point of
	// the setting is that it must cover whatever this installation actually
	// mints, and a fixed default cannot know the token lifetime was raised.
	// The clock-skew margin accounts for differences between the issuer and
	// verifiers' clocks.
	c.keyOverlap = dur(k.Overlap, 0)
	if c.keyOverlap <= 0 {
		c.keyOverlap = c.tokenLifetime + issuer.KeyOverlapSkew
	}
	c.keyPollInterval = dur(k.PollInterval, issuer.DefaultKeyPollInterval)
	// The activation delay must be longer than the poll interval so that a
	// newly published key has at least one complete poll cycle to be seen
	// and re-read before any replica signs with it.
	if c.keyActivationDelay < c.keyPollInterval {
		return Config{}, fmt.Errorf("signingKey.activationDelay (%v) must be at least signingKey.pollInterval (%v)", c.keyActivationDelay, c.keyPollInterval)
	}
	if c.kmsWrapped != nil {
		if _, err = c.wrappedConfig(); err != nil {
			return Config{}, err
		}
	}
	level := "info"
	if f.Log != nil && f.Log.Level != "" {
		level = f.Log.Level
	}
	if err = c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	// Checked here, at load, rather than left to [issuer.Config.withDefaults]
	// to default quietly: an installation that asks for something this
	// build does not recognise must be told so, not silently downgraded
	// to report. See [issuer.CheckGroupsScopingMode].
	c.groupsScoping = issuer.GroupsScopingMode(orDefault(f.GroupsScoping, string(issuer.GroupsScopingReport)))
	if err = issuer.CheckGroupsScopingMode(c.groupsScoping); err != nil {
		return Config{}, fmt.Errorf("groupsScoping: %w", err)
	}

	// Every relying party's trust is anchored on this string, and it goes
	// into every token. A default would be a value nobody chose baked
	// into an installation's whole estate.
	if c.issuerURL == "" {
		return Config{}, errors.New("issuerURL is required: it is baked into every token and every relying party")
	}
	if c.consoleAWSAudience == "" {
		c.consoleAWSAudience = strings.TrimSuffix(c.issuerURL, "/") + "/console"
	}
	if c.audience == "" {
		c.audience = c.release
	}
	return c, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func listenOr(a *config.Address, fallback string) string {
	if a != nil && a.Address != "" {
		return a.Address
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

// Deps are the things a caller supplies instead of letting this package
// build them. Both are how the merged service is assembled:
// one process holds the directory, so the issuer calls it rather than
// dialling it, and the console is served from the issuer's own origin
// instead of a listener of its own.
//
// Directory is REQUIRED. It was optional while the hub was a service of
// its own, reached over the network at HUB_ADDRESS; the merge folded it
// into this process and nothing has dialled it since. What was left was
// a branch that could not run, two settings nothing set, and a network
// client with no caller -- config that reads as a supported deployment
// and is not one.
type Deps struct {
	// KMS is the client for signingKey.kms. Nil builds one from the AWS
	// default credential chain; a test supplies a fake.
	KMS issuer.KMSAPI
	// KMSWrapped is the client for signingKey.kmsWrapped. Nil builds one from
	// the AWS default credential chain; a test supplies a fake.
	KMSWrapped issuer.KMSWrapAPI
	// Stores is the storage ports, built once from configuration and shared
	// with the directory half. Nil is a process with no shared state and no
	// cluster: logins in progress are kept in this process, and recovery is
	// off.
	Stores *store.Stores
	// Directory answers "who is this address", in this process.
	Directory issuer.Directory
	// Policy is the policy in force. Supplying it is how the merged
	// service guarantees both halves act on the SAME one: they read the
	// same file, but their fallbacks differ, and two halves that can
	// disagree about the policy is the class of failure the merge existed
	// to end. Nil loads it from policyDir, which is the split shape.
	Policy *policy.Set
	// UseSignInEntry is called with a function saying where to send a
	// browser that has NO session. Together with UseSignedIn it is the
	// whole of how the console authenticates on one origin: one says how
	// to come by a session, the other reads it.
	UseSignInEntry func(func() string)
	// UseSignedIn is called with a reader of the issuer's own browser
	// session, once this half exists. It is how the console on this
	// origin learns who is signed in without a proxy in front of it and
	// without a login of its own.
	UseSignedIn func(func(*http.Request) (access.Principal, bool))
	// Audit is the service's recorder, shared with the directory half so
	// both write one history. Nil records to this process's log alone.
	Audit audit.Recorder
	// UseWorkloads is called with a reader of ServiceAccount bearers,
	// verified against the same cluster key sets token exchange uses. It
	// is how a controller beside this issuer reads the console's API with
	// its own projected token. Nil is never called with, and a deployment
	// federating no cluster hands it nothing.
	UseWorkloads func(func(*http.Request) (access.Principal, bool))
	// UseIssuerURL is called with this issuer's own URL, so the console
	// can tell a browser where the SessionService is.
	//
	// The console used to learn that from the proxy in front of it, and
	// on one origin there is no proxy -- so without this it concluded it
	// had no issuer and hid its sessions pages on the deployment where
	// they work best.
	UseIssuerURL func(string)
	// Ready are dependencies the caller's half of the process needs
	// answering for, added to this one's on /readyz. The merged service
	// has one readiness endpoint and two stores behind it.
	Ready []health.Dependency
	// Console is the operator UI, written as if it were at the root of an
	// origin. It is mounted under /console/ with the prefix stripped,
	// which is exactly what the gateway used to do for it.
	Console http.Handler
	// GitHubApps are the catalogue Apps this issuer mints installation
	// tokens for, under the catalogue's grants: the directory half's
	// catalogue and the Secret it keeps each App's key in. Nil mints none,
	// and every such request is refused as naming an App that cannot.
	GitHubApps *issuer.GitHubApps
	// Around wraps everything this issuer serves on its port, the console
	// included. It is applied to the one handler both [App.Handler] and
	// [App.Run] use, so what a caller tests through the first is what the
	// second serves. The merged service puts what an audit event keeps of
	// each request into its context here. Nil wraps nothing.
	Around func(http.Handler) http.Handler
}

// App is an assembled issuer.
type App struct {
	// kms is set when the primary key lives in AWS KMS; Run polls it.
	kms []*issuer.KMSKeyRefs
	// wrapped is set when the keys are KMS-wrapped; Run keeps them rotating.
	wrapped bool
	// state is the shared state the secret fingerprint lives in.
	state   issuer.State
	handler http.Handler
	health  http.Handler
	issuer  *issuer.Issuer
	storage *issuer.Storage
	cfg     Config
	log     *slog.Logger
}

// MintFor signs a short-lived access token for a person signed in to this
// service, for one declared client: how the console reads another service
// as that person. See [issuer.Storage.MintFor].
func (a *App) MintFor(ctx context.Context, email, audience string, lifetime time.Duration) (string, time.Time, error) {
	return a.storage.MintFor(ctx, email, audience, lifetime)
}

// Handler is the OpenID surface: discovery, keys, authorize, token,
// exchange, revocation, the device flow.
func (a *App) Handler() http.Handler { return a.handler }

// HealthHandler is liveness and readiness.
func (a *App) HealthHandler() http.Handler { return a.health }

// Issuer is the decision core, for a caller that drives it directly.
func (a *App) Issuer() *issuer.Issuer { return a.issuer }

// catalogueGrantGroups is the groups a GitHub App catalogue's grants name,
// for policy.Policy.Unconsumed's catalogueGroups parameter -- nil when
// this issuer mints no catalogue Apps' tokens at all (apps nil) or its
// catalogue is empty (a nil Catalogue field), which is the ordinary shape
// for a deployment that declares no GitHub Apps.
func catalogueGrantGroups(apps *issuer.GitHubApps) []string {
	if apps == nil || apps.Catalogue == nil {
		return nil
	}
	return apps.Catalogue.GrantGroups()
}

// New assembles the issuer.
func New(ctx context.Context, cfg Config, deps Deps, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}

	var err error

	set := deps.Policy
	if set == nil {
		declared, loadErr := policy.LoadDeclared(cfg.policyPath)
		if loadErr != nil {
			return nil, loadErr
		}
		if set, err = policy.NewSet(declared); err != nil {
			return nil, err
		}
		// deps.GitHubApps' catalogue, when this issuer mints installation
		// tokens under one, names groups this package cannot otherwise
		// see any reference to: a grant is what actually consumes such a
		// group, exactly as a client's Requires would. Without a
		// catalogue (deps.GitHubApps nil, or a nil Catalogue within it —
		// see [issuer.GitHubApps]) nothing is added here, and this
		// behaves exactly as it always did.
		if unconsumed := declared.Unconsumed(catalogueGrantGroups(deps.GitHubApps)...); len(unconsumed) > 0 {
			log.WarnContext(ctx, "internal groups are declared but nothing consumes them",
				"groups", unconsumed)
		}
	}

	// A resource's absolute_cap against this installation's own limit: a
	// longer one needs the resource to say it is read-only. Refused here
	// because policy alone cannot know lifetimes.absolute, and a row that
	// could never be honoured should stop the service, not be clamped.
	warnBroadAWSMatchers(ctx, set, log)
	rows := set.Resources()
	for i := range rows {
		if err = rows[i].CheckAbsoluteCap(rows[i].ID, cfg.absoluteLifetime); err != nil {
			return nil, err
		}
	}

	directory := deps.Directory
	if directory == nil {
		return nil, errors.New(
			"a directory is required: this service asks it about every person, and it is " +
				"supplied in-process -- there is no longer a network hub to dial")
	}

	// The shared store first: the issuer's session index lives in it, so
	// there is no issuer to build until it is open.
	stores := deps.Stores
	if stores == nil {
		stores = &store.Stores{}
	}
	shared := openState(ctx, stores, log)

	core := issuer.New(issuer.Config{
		URL:              cfg.issuerURL,
		TokenLifetime:    cfg.tokenLifetime,
		RefreshLifetime:  cfg.refreshLifetime,
		AbsoluteLifetime: cfg.absoluteLifetime,
		HoldWindow:       cfg.holdWindow,
		AllowInsecure:    cfg.allowInsecure,
		GroupsScoping:    cfg.groupsScoping,
	}, set, directory, shared)
	// The service's one audit trail, opened by the directory half. A
	// split deployment with none still validates every record and logs it.
	if deps.Audit != nil {
		core.UseAudit(deps.Audit)
	} else {
		trail, err := audit.Open(ctx, audit.Config{Log: log})
		if err != nil {
			return nil, err
		}
		core.UseAudit(trail)
	}

	if deps.GitHubApps != nil {
		core.UseGitHubApps(*deps.GitHubApps)
	}

	if err := applySigningPlan(ctx, &cfg, stores.Plan); err != nil {
		return nil, err
	}
	var (
		key     *issuer.SigningKey
		kmsRefs []*issuer.KMSKeyRefs
		kmsRest []*issuer.SigningKey
		kmsMore []*issuer.SigningKey
	)
	var wrapped *issuer.WrappedSigning
	switch {
	case cfg.kmsWrapped != nil:
		wrapped, key, kmsMore, err = wrappedSigningKeys(ctx, cfg, deps, stores, shared, log)
	case len(cfg.kmsKeys) > 0:
		kmsRefs, kmsRest, key, kmsMore, err = kmsSigningKeys(ctx, cfg, deps.KMS, log)
	default:
		key, err = signingKey(ctx, cfg, log)
	}
	if err != nil {
		return nil, err
	}
	if kmsRefs != nil {
		if err = checkStateSecret(ctx, shared, kmsRefs[0].Seed); err != nil {
			return nil, err
		}
	}
	additionalKeys, err := additionalSigningKeys(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	if len(kmsRefs) > 0 {
		for _, f := range additionalKeys {
			log.WarnContext(ctx, "signingKey.kms is set but this algorithm is still signed by a file key",
				"algorithm", f.SignatureAlgorithm(), "kid", f.ID())
		}
	}
	// Each KMS algorithm's newest key is its ring's primary, beside any files
	// (a file and a KMS key for the same algorithm clash, as two files do).
	additionalKeys = append(additionalKeys, kmsMore...)
	if err = readClient(&cfg); err != nil {
		return nil, err
	}
	verifiers, clusters, err := openVerifiers(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	storage, err := issuer.NewStorage(core, verifiers, clientSecrets(cfg, log), key, additionalKeys, shared)
	if err != nil {
		return nil, err
	}
	// So the storage's own log lines -- a reused authorization code, the
	// groups-scoping report -- carry this deployment's level and
	// attributes rather than depending on [slog.SetDefault] alone.
	storage.UseLog(log)
	// The delays a deployment named, or their defaults: NewStorage seeded
	// the ring before either was known, so this is applied before the
	// poller in Run starts feeding it anything more.
	storage.ConfigureKeyRotation(issuer.KeyRingConfig{
		ActivationDelay: cfg.keyActivationDelay,
		Overlap:         cfg.keyOverlap,
	})
	if wrapped != nil {
		// The wrapped schedule's own pre-publish and retention, which default to
		// the two settings above.
		wc, wcErr := cfg.wrappedConfig()
		if wcErr != nil {
			return nil, wcErr
		}
		storage.ConfigureKeyRotation(issuer.KeyRingConfig{ActivationDelay: wc.Prepublish, Overlap: wc.Retain})
		storage.UseWrappedSigning(wrapped)
	}
	// The earlier KMS keys, in order: refreshed if the installation knows
	// them, never newly adopted.
	for _, extra := range kmsRest {
		if err = storage.RotateKnown(ctx, extra); err != nil {
			return nil, fmt.Errorf("adopt a KMS signing key: %w", err)
		}
	}
	signIn, err := openSignIn(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	handler, err := issuer.HandlerWithSignIn(core, storage, issuer.SignInDeps{
		Providers:     signIn,
		Recovery:      openRecovery(ctx, cfg, stores, log),
		State:         access.NewStateCodec(key.Derive("sluis/sign-in-state"), signInWindow),
		ConsoleOrigin: cfg.consoleOrigin,
		// Where an old /account bookmark is sent. Empty when this
		// deployment serves no console, and then the route is not served
		// at all rather than redirecting somebody to a 404.
		ConsoleMount: consoleMount(deps),
		// Signing out leads back to signing in, where there is a console
		// to sign in to. Not `/login`: its buttons carry the id of a
		// pending authorization request, so visiting it without one is a
		// page that looks like a sign-in and cannot finish. The console's
		// front page sends an unauthenticated browser through
		// `/authorize`, which MAKES that request — so this is the address
		// that actually reaches a working sign-in.
		//
		// Empty where no console is mounted, and then the signed-out page
		// is the honest ending.
		AfterSignOut: afterSignOut(deps),
		Secure:       cfg.secureCookies,
		Log:          log,
	})
	if err != nil {
		return nil, err
	}
	handler = mount(handler, deps.Console)
	if deps.UseSignInEntry != nil {
		// The console becomes a client of this issuer: one door, and it
		// holds nothing special. Empty when no client is declared for it,
		// and then the console keeps a sign-in page of its own.
		deps.UseSignInEntry(signInEntry(cfg.issuerURL, cfg.consoleClientID, consoleMount(deps)))
	}
	if deps.UseIssuerURL != nil {
		// Same origin, so the browser reaches the SessionService with the
		// SSO cookie it already holds: no bearer in JavaScript, no CORS.
		deps.UseIssuerURL(cfg.issuerURL)
	}
	if deps.UseSignedIn != nil {
		// The console is on this origin and in this process, so it reads
		// the browser's issuer session directly rather than being told by
		// a proxy that ran an OpenID flow against this very service.
		deps.UseSignedIn(signedIn(core))
	}
	if deps.UseWorkloads != nil {
		// The SAME verifiers token exchange uses, and only the clusters and AWS accounts:
		// a CI job has no business reading the console's API, and a
		// second copy of the cluster rows would be a second place for one
		// installation's trust to be configured.
		if read := workloadBearer(clusters, log); read != nil {
			deps.UseWorkloads(read)
		}
	}

	// Readiness follows the state store; liveness does not. An issuer
	// that cannot reach it can neither mint nor find a session, and
	// reporting ready through that is how a moved Valkey became a
	// fifteen-second hang at every callback on 2026-09-10.
	healthMux := health.Mux(0, append([]health.Dependency{
		health.Follow("the session store", stores.Readiness()),
	}, deps.Ready...)...)

	log.InfoContext(ctx, "sluis assembled",
		"issuer", cfg.issuerURL, "directory", directorySource(deps, cfg), "inCluster", cfg.inCluster,
		"exchangeAudience", cfg.audience, "port", cfg.port, "health", cfg.healthPort,
		"tokenLifetime", cfg.tokenLifetime, "refreshLifetime", cfg.refreshLifetime,
		"holdWindow", cfg.holdWindow, "version", version.String())
	if cfg.allowInsecure {
		log.WarnContext(ctx, "the issuer URL may be plaintext: every token this service signs is a "+
			"bearer credential, and an issuer reached over http can be impersonated by anyone on the path")
	}
	if deps.Around != nil {
		handler = deps.Around(handler)
	}
	// Outermost, so that the span covers everything the listener answers and
	// the request metrics see the status the client got. The route is a fixed
	// set of names (issuer.Route), never the path.
	handler = telemetry.HTTPHandler(handler, "access-issuer", issuer.Route)
	return &App{handler: handler, health: healthMux, issuer: core, storage: storage, cfg: cfg, log: log, kms: kmsRefs, wrapped: wrapped != nil, state: shared}, nil
}

// directorySource says where the answer about a person comes from. There
// is one answer now, and it is logged rather than dropped because an
// operator reading the startup line should not have to know that.
func directorySource(Deps, Config) string { return "in-process" }

// consoleMount is where the console sits when there is one. It is a
// constant because the mount is not configurable: the issuer owns the
// origin ROOT — discovery must sit there — and the console takes this
// path beside it.
// afterSignOut is where a person lands once their sign-in has ended: the
// console's front page where one is mounted, so that signing out leads
// back to signing in rather than to a page they have to leave.
func afterSignOut(deps Deps) string {
	mount := consoleMount(deps)
	if mount == "" {
		return ""
	}

	return mount + "/"
}

func consoleMount(deps Deps) string {
	if deps.Console == nil {
		return ""
	}
	return "/console"
}

// mount puts the console under /console/ on the issuer's own origin.
//
// Same origin is the point, not a convenience: the console's session
// pages then call the issuer with the browser's own cookie and no bearer
// in JavaScript, and discovery keeps the origin ROOT, which is where
// every relying party's `iss` says it is. The console's handler is
// written as if it were at a root, so the prefix is stripped here —
// exactly what the gateway's URLRewrite used to do for it.
func mount(issuerHandler, console http.Handler) http.Handler {
	if console == nil {
		return issuerHandler
	}
	mux := http.NewServeMux()
	// "/console" without the trailing slash is a DIFFERENT page to a
	// browser: the bundle references its assets relatively so that one
	// build serves at any mount point, and "./assets/..." on a page at
	// "/console" resolves against the root — where every asset asks the
	// issuer and gets a 404.
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusFound)
	})
	mux.Handle("/console/", http.StripPrefix("/console", console))
	// The admin-consent callback stays at the origin ROOT, because it is
	// the one flow that runs before anybody can be signed in: the
	// operator who connects the FIRST directory is by definition one no
	// directory can vouch for yet. Its redirect URI is registered with
	// the corporate IdP, so moving it would mean re-registering it in
	// every tenant.
	mux.Handle("/connect/", console)
	mux.Handle("/", issuerHandler)
	return mux
}

// Run serves the two listeners until the context is done, alongside the
// background poll that lets the signing key rotate without a restart.
func (a *App) Run(ctx context.Context) error {
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return serve(gctx, a.cfg.port, a.handler, "issuer", a.log) })
	group.Go(func() error { return serve(gctx, a.cfg.healthPort, a.health, "health", a.log) })
	if a.kms != nil {
		group.Go(func() error {
			watchKMSKeys(gctx, a.kms, a.cfg.keyPollInterval, a.storage, a.log, func(c context.Context) error {
				return checkStateSecret(c, a.state, a.kms[0].Seed)
			})
			return nil
		})
	}
	if a.wrapped {
		group.Go(func() error {
			ticker := time.NewTicker(a.cfg.keyPollInterval)
			defer ticker.Stop()
			for {
				a.storage.MaintainKeys(gctx)
				select {
				case <-gctx.Done():
					return nil
				case <-ticker.C:
				}
			}
		})
	}
	group.Go(func() error {
		paths := append([]string{a.cfg.signingKeyFile}, a.cfg.additionalSigningKeyFiles...)
		watchSigningKey(gctx, paths, a.cfg.keyPollInterval, a.storage, a.log)
		return nil
	})
	return group.Wait()
}

// clientSecrets resolves a confidential client's secret from the files
// the deployment mounted, one per client id.
//
// From FILES and not from the API, for the same reason the signing key
// comes from one: this service holds no RBAC to read a Secret, so a
// compromise of it cannot become a read of every credential in its
// namespace. The chart projects each declared client's Secret to a file
// named after the client.
//
// Read per call rather than once at start, so that rotating a client's
// Secret takes effect when the kubelet refreshes the mount instead of
// needing a restart.
func clientSecrets(cfg Config, log *slog.Logger) func(string) (string, bool) {
	if cfg.clientSecretsDir == "" {
		return nil
	}
	return func(clientID string) (string, bool) {
		// A client id is a path SEGMENT here. One containing a separator
		// would read a file the deployment never mounted, so it is
		// refused rather than cleaned: there is no reading of "../" that
		// the author could have meant.
		if clientID == "" || strings.ContainsAny(clientID, `/\`) || clientID == "." || clientID == ".." {
			return "", false
		}
		raw, err := os.ReadFile(filepath.Join(cfg.clientSecretsDir, clientID)) //nolint:gosec // the id is checked above and the directory is deployment configuration
		if err != nil {
			log.Warn("a declared client's secret could not be read; that client cannot authenticate",
				"client", clientID, "error", err)
			return "", false
		}
		return strings.TrimSpace(string(raw)), true
	}
}

// openRecovery builds the way in that needs no directory, or nothing.
//
// Out of a cluster there is no API server to prove access to, so there is
// nothing to build: unlike the hub, this service has no password shape to
// fall back on, and inventing one would be inventing a standing
// credential for a service whose whole point is not to hold any.
func openRecovery(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) issuer.Recovery {
	if !cfg.recoveryEnabled {
		return nil
	}
	if !cfg.inCluster || cfg.recoveryAccount == "" || cfg.recoveryAudience == "" {
		log.WarnContext(ctx, "recovery is asked for but cannot be built: it proves access to "+
			"a cluster, and this service is not running in one with an account and audience named")
		return nil
	}
	namespace, ok := clusterNamespace(st)
	if !ok {
		log.WarnContext(ctx, "recovery could not be built: the namespace's objects are not available")
		return nil
	}
	log.InfoContext(ctx, "recovery sign-in is available: a token for this account signs in "+
		"without a directory, and the policy's service_account matchers decide what it gets",
		"namespace", namespace, "account", cfg.recoveryAccount, "audience", cfg.recoveryAudience)
	return &issuer.TokenRecovery{
		Review:    st.Ports.Identity.Verify,
		Namespace: namespace,
		Account:   cfg.recoveryAccount,
		Audience:  cfg.recoveryAudience,
		Subjects:  []string{access.ServiceAccountSubject(namespace, cfg.recoveryAccount)},
		Cluster:   cfg.cluster,
	}
}

// signInWindow is how long a person has to finish signing in, and the
// life of the state that carries their half-finished request.
const signInWindow = 10 * time.Minute

// openSignIn builds the directories a person may prove themselves with.
//
// A deployment with none issues tokens to machines and to nobody else,
// which is a real posture — a cluster's workload exchange with no human
// login — and says so rather than serving a chooser with no buttons.
func openSignIn(ctx context.Context, cfg Config, log *slog.Logger) ([]issuer.SignIn, error) {
	if cfg.oauthClientID == "" || cfg.oauthClientSecret == "" {
		log.WarnContext(ctx, "nobody can sign in: no OAuth client is configured, so this issuer "+
			"serves token exchange and nothing else")
		return nil, nil
	}
	client := google.OAuthClient{
		ID:      cfg.oauthClientID,
		Secret:  cfg.oauthClientSecret,
		BaseURL: cfg.issuerURL,
	}
	log.InfoContext(ctx, "people sign in with Google", "redirect", client.SignInRedirect())
	return []issuer.SignIn{&googleSignIn{client: client}}, nil
}

// googleSignIn adapts the backend's client to what the issuer's pages
// need. It is three lines because the issuer's half of a login is three
// things: where to send them, what came back, and nothing else.
type googleSignIn struct{ client google.OAuthClient }

func (g *googleSignIn) Kind() string { return "google" }

func (g *googleSignIn) URL(state string) (string, error) { return g.client.SignInURL(state), nil }

func (g *googleSignIn) Identify(ctx context.Context, code string) (string, error) {
	return google.Identify(ctx, g.client, code)
}

// signingKey reads the key this installation was given.
//
// It is a mounted file, not a Secret this service reads through the API,
// and that is deliberate twice over. The issuer needs no permission to
// read Secrets at all — the one credential it holds arrives the way every
// other credential in this estate arrives, from cert-manager or from
// external-secrets, and this service only opens the file. And it does not
// mint one: a key generated here would be a different key in every
// replica and after every restart, and a service that creates its own
// credential is an exception to how everything else here gets one.
//
// No file configured means a local run, which generates one and says so.
func signingKey(ctx context.Context, cfg Config, log *slog.Logger) (*issuer.SigningKey, error) {
	if cfg.signingKeyFile == "" {
		log.WarnContext(ctx, "generating a signing key for this process: every restart invalidates "+
			"every token it signed, and two replicas would sign with two keys. "+
			"A deployment sets signingKey.file")
		return issuer.NewSigningKey()
	}
	encoded, err := os.ReadFile(cfg.signingKeyFile) //nolint:gosec // the path is deployment configuration
	if err != nil {
		// Starting without it would mean signing with a key nobody else
		// has, which is worse than not starting: the tokens would look
		// fine and verify nowhere.
		return nil, fmt.Errorf("read the signing key: %w — a deployment provides it as a Secret, "+
			"issued by cert-manager or delivered by external-secrets, mounted at that path", err)
	}
	key, err := issuer.ParseSigningKey(encoded)
	if err != nil {
		return nil, err
	}
	log.InfoContext(ctx, "signing with the key this installation was given",
		"file", cfg.signingKeyFile, "kid", key.ID())
	return key, nil
}

// additionalSigningKeys reads every OTHER algorithm this installation
// signs with at once, beside the primary [signingKey] — one file per
// algorithm, named in signingKey.additionalFiles, matching the chart's
// `signingKey.additional`.
//
// Read eagerly, at start, and not lazily on first use: [issuer.NewStorage]
// needs every configured algorithm in hand before it can refuse a policy
// naming one that has no key (requirement 2), and a deployment whose
// mount is broken should fail before it serves a single request rather
// than the first time somebody reaches a client that names it.
func additionalSigningKeys(ctx context.Context, cfg Config, log *slog.Logger) ([]*issuer.SigningKey, error) {
	out := make([]*issuer.SigningKey, 0, len(cfg.additionalSigningKeyFiles))
	for _, path := range cfg.additionalSigningKeyFiles {
		encoded, err := os.ReadFile(path) //nolint:gosec // the path is deployment configuration
		if err != nil {
			return nil, fmt.Errorf("read an additional signing key: %w — a deployment provides it as a "+
				"Secret, issued by cert-manager or delivered by external-secrets, mounted at that path", err)
		}
		key, err := issuer.ParseSigningKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		log.InfoContext(ctx, "signing with an additional key this installation was given",
			"file", path, "algorithm", key.SignatureAlgorithm(), "kid", key.ID())
		out = append(out, key)
	}
	return out, nil
}

// watchSigningKey re-reads every mounted key file on an interval and
// feeds every version this replica reads to the storage's key rings — see
// [issuer.KeyRings] for the schedule that turns that into rotation with no
// restart, one file routed to its OWN algorithm's track (requirement six
// of live rotation: rotating one never disturbs another's). It runs
// until ctx is done, which happens together with the two listeners in
// [App.Run].
//
// A local run with no file configured has nothing to poll: the one key
// [signingKey] generated for it is the only key there will ever be.
//
// A read or parse failure after the first is logged and the previous key
// kept, never returned: a Secret Kubernetes is mid-projecting can be
// observed for an instant while `..data` is being swapped, and a poller
// that failed loudly over a read that would have succeeded thirty seconds
// later would turn a non-event into an incident. The same is true of a
// path that names an algorithm nothing was configured for at start
// ([issuer.KeyRings.Rotate] refuses it) — logged and skipped, not fatal,
// because the deployment is already running with what it started with.
func watchSigningKey(ctx context.Context, paths []string, interval time.Duration, storage *issuer.Storage, log *slog.Logger) {
	paths = nonEmpty(paths)
	if len(paths) == 0 {
		return
	}
	if interval <= 0 {
		interval = issuer.DefaultKeyPollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, path := range paths {
				pollSigningKeyFile(ctx, path, storage, log)
			}
		}
	}
}

// pollSigningKeyFile is one file, one poll tick: read, parse and hand to
// the storage's key rings, which route it to the track for its own
// algorithm. Split out of [watchSigningKey] so that one bad file's
// `continue` cannot accidentally skip the others sharing its tick.
func pollSigningKeyFile(ctx context.Context, path string, storage *issuer.Storage, log *slog.Logger) {
	encoded, err := os.ReadFile(path) //nolint:gosec // the path is deployment configuration
	if err != nil {
		log.WarnContext(ctx, "could not re-read a signing key; keeping the previous one",
			"file", path, "error", err)
		return
	}
	key, err := issuer.ParseSigningKey(encoded)
	if err != nil {
		log.WarnContext(ctx, "a re-read signing key could not be parsed; keeping the previous one",
			"file", path, "error", err)
		return
	}
	if err := storage.Rotate(ctx, key); err != nil {
		log.WarnContext(ctx, "a re-read signing key could not be adopted", "file", path, "error", err)
	}
}

// nonEmpty drops blank paths, which is what a primary [Config.signingKeyFile]
// left unset by a local run looks like once joined with the additional
// ones.
func nonEmpty(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

// readClient takes the OAuth client from the files a Secret is mounted
// at, for the same reason the signing key comes from one: a credential in
// an environment variable is a credential in every process listing and
// every crash dump. The variables stay for a local run.
//
// The ID comes from the same Secret as the secret, rather than from a
// chart value, so that both halves of one credential travel together and
// the hub and this service read it the same way. It is not itself a
// secret -- every browser sent to the provider carries it -- but a client
// whose halves are configured in two places is a client that can be half
// rotated.
func readClient(cfg *Config) error {
	for _, from := range []struct {
		path string
		into *string
		what string
	}{
		{cfg.oauthIDFile, &cfg.oauthClientID, "id"},
		{cfg.oauthSecretFile, &cfg.oauthClientSecret, "secret"},
	} {
		if from.path == "" {
			continue
		}
		raw, err := os.ReadFile(from.path) //nolint:gosec // the path is deployment configuration
		if err != nil {
			return fmt.Errorf("read the OAuth client %s: %w", from.what, err)
		}
		*from.into = strings.TrimSpace(string(raw))
	}
	return nil
}

// openState decides where a login in progress lives.
//
// In memory unless a Valkey is configured, and at more than one replica
// that difference is not a nicety: a browser starts at /authorize on one
// replica, comes back from the provider at another, and the client
// redeems the code at a third. Each of those is a coin toss that looks
// like an intermittent failure, so a deployment running more than one
// replica without a Valkey is a mistake worth saying out loud.
func openState(ctx context.Context, st *store.Stores, log *slog.Logger) issuer.State {
	if !st.Usable {
		log.WarnContext(ctx, "keeping logins in progress in memory: correct for one replica, "+
			"and at more than one a browser that comes back to a different pod finds nothing",
			"state", "memory")
		return issuer.NewMemoryState()
	}
	log.InfoContext(ctx, "keeping logins in progress in the state ports",
		"state", st.Name(), "adapter", st.Adapter)
	return issuer.NewPortState(st.Ports.State, st.Ports.Index)
}

// openVerifiers builds what can turn somebody else's token into a proof.
//
// A deployment with none can still sign people in; it simply exchanges
// nothing, and says so, because an exchange endpoint that refuses
// everything with "unverified" is indistinguishable from one that is
// misconfigured.
//
// The clusters and the AWS accounts are also returned on their own: they are the
// two kinds of proof the console accepts as a bearer, from a workload in a
// federated cluster or a Lambda function's role calling its API.
func openVerifiers(ctx context.Context, cfg Config, log *slog.Logger) (all, clusters issuer.Verifiers, err error) {
	var verifiers issuer.Verifiers

	// Clusters, by their own published key set and NEVER by asking them.
	// The other way to check a ServiceAccount token is a
	// TokenReview, which means holding a kubeconfig for every cluster
	// whose workloads may exchange — inside the service whose whole point
	// is to hold almost no credential. A key set is public, so a remote
	// cluster's workload proves itself exactly the way a GitHub job does,
	// and adding a cluster is one row naming a URL.
	//
	// This service's OWN cluster is a row like any other. There is no
	// special case for it, because a special case is a second code path
	// that only one installation exercises.
	federation, err := verify.LoadFederation(cfg.clustersPath)
	if err != nil {
		return nil, nil, err
	}
	for _, cluster := range federation.Verifiers(cfg.audience, nil) {
		verifiers = append(verifiers, cluster)
		clusters = append(clusters, cluster)
	}
	if len(federation.Clusters) > 0 {
		log.InfoContext(ctx, "workload tokens are verified against each cluster's own key set",
			"clusters", federation.Names(), "audience", cfg.audience)
	} else {
		log.InfoContext(ctx, "no workload token can be verified: no cluster's key set is declared")
	}

	// AWS accounts, by each account's own published key set. Like a
	// cluster, an account is a row naming where its keys are; unlike one
	// there is no default, because any AWS account can mint a valid token
	// for a role of its own.
	awsFederation, err := verify.LoadAWSFederation(cfg.awsPath)
	if err != nil {
		return nil, nil, err
	}
	for _, account := range awsFederation.Verifiers(nil) {
		verifiers = append(verifiers, account)
	}
	// And a role may present its token to the console directly, as a
	// ServiceAccount does: a Lambda controller has no projected token. By the
	// console's OWN audience, so that a token minted for token exchange is no
	// bearer here and the reverse: each door is one decision.
	if len(awsFederation.Accounts) > 0 {
		if cfg.consoleAWSAudience == awsFederation.Audience {
			return nil, nil, fmt.Errorf("console.awsAudience %q is the AWS federation file's audience: "+
				"the console and the token exchange are two doors and each takes its own", cfg.consoleAWSAudience)
		}
		for _, account := range consoleAWSVerifiers(awsFederation, cfg.consoleAWSAudience, nil) {
			clusters = append(clusters, account)
		}
		log.InfoContext(ctx, "AWS role tokens are bearers at the console by their own audience",
			"audience", cfg.consoleAWSAudience)
	}
	if len(awsFederation.Accounts) > 0 {
		log.InfoContext(ctx, "AWS role tokens are verified against each account's own key set",
			"accounts", awsFederation.Names(), "audience", awsFederation.Audience)
	} else {
		log.InfoContext(ctx, "no AWS role token can be verified: no account is declared")
	}

	// GitHub, only when this installation has said whose repositories it
	// runs jobs for. There is no default and there cannot be one: anybody
	// may run a workflow in their own repository and get a valid token
	// from GitHub, so an empty list would admit every repository there is
	// rather than none.
	if len(cfg.githubOwners) > 0 {
		log.InfoContext(ctx, "CI tokens are verified against GitHub",
			"owners", cfg.githubOwners, "audience", cfg.issuerURL)
		verifiers = append(verifiers, &verify.GitHub{
			Owners: cfg.githubOwners,
			// The audience a workflow must request is this issuer's own
			// URL. A token minted for a cloud provider is a valid GitHub
			// token, and one audience per relying party is what keeps it
			// from being replayed here.
			Audience: cfg.issuerURL,
		})
	} else {
		log.InfoContext(ctx, "no CI token can be verified: github.owners names no organisation")
	}

	// An exchange endpoint that refuses everything with "unverified" is
	// indistinguishable from one that is misconfigured, so a deployment
	// that can verify nothing says so rather than looking broken later.
	if len(verifiers) == 0 {
		log.WarnContext(ctx, "no proof can be verified: token exchange will refuse everything")
	}

	return verifiers, clusters, nil
}

// serve runs one listener until the context is done, then drains it.
func serve(ctx context.Context, addr string, handler http.Handler, name string, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.WarnContext(ctx, "listener did not drain", "listener", name, "error", err)
		}
	}()
	log.InfoContext(ctx, "listening", "listener", name, "address", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s listener: %w", name, err)
	}
	return nil
}

// kmsSigningKeys reads every KMS key in signingKey.kms at start, in order.
//
// The LAST is returned as the primary, the only key ever recorded as new; the
// earlier ones are returned apart and fed through RotateKnown, which refreshes
// a key the installation already knows and never adopts one it does not. So
// state lost without tombstones cannot make a replica start by signing with
// the oldest key. Only ever append a key.
//
// A key the role cannot read stops the start, with the missing permission
// named; starting without it would sign with fewer keys than the deployment
// declared.
func kmsSigningKeys(
	ctx context.Context, cfg Config, api issuer.KMSAPI, log *slog.Logger,
) (refs []*issuer.KMSKeyRefs, earlier []*issuer.SigningKey, primary *issuer.SigningKey, more []*issuer.SigningKey, err error) {
	raw, err := os.ReadFile(cfg.kmsStateSecretFile) //nolint:gosec // the path is deployment configuration
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("read signingKey.kms.stateSecretFile: %w", err)
	}
	seed, err := parseStateSecret(raw)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("signingKey.kms.stateSecretFile: %w", err)
	}
	if api == nil {
		var loaders []func(*awsconfig.LoadOptions) error
		if cfg.kmsRegion != "" {
			loaders = append(loaders, awsconfig.WithRegion(cfg.kmsRegion))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("load the AWS configuration for signingKey.kms: %w", err)
		}
		api = kms.NewFromConfig(awsCfg)
	}
	sets := []*issuer.KMSKeyRefs{{Alg: jose.ES384, API: api, Refs: cfg.kmsKeys, Seed: seed}}
	for _, a := range cfg.kmsAdditional {
		if jose.SignatureAlgorithm(a.Alg) != jose.RS256 || len(a.Keys) == 0 {
			return nil, nil, nil, nil, fmt.Errorf("signingKey.kms.additional: %q needs alg RS256 and keys", a.Alg)
		}
		sets = append(sets, &issuer.KMSKeyRefs{Alg: jose.SignatureAlgorithm(a.Alg), API: api, Refs: a.Keys, Seed: seed})
	}
	seen := map[string]string{}
	for n, set := range sets {
		keys, err := set.Load(ctx)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for i, key := range keys {
			if prev, dup := seen[key.ID()]; dup {
				return nil, nil, nil, nil, fmt.Errorf("signingKey.kms: %q and %q are the same key", prev, set.Refs[i])
			}
			seen[key.ID()] = set.Refs[i]
			log.InfoContext(ctx, "signing with an AWS KMS key",
				"key", set.Refs[i], "kid", key.ID(), "algorithm", key.SignatureAlgorithm(), "active", i == len(keys)-1)
		}
		// The LAST of each list is that algorithm's ring primary; the rest are
		// only ever refreshed.
		last := len(keys) - 1
		earlier = append(earlier, keys[:last]...)
		if n == 0 {
			primary = keys[last]
		} else {
			more = append(more, keys[last])
		}
	}
	return sets, earlier, primary, more, nil
}

// watchKMSKeys re-reads every KMS key's public half on an interval and feeds
// it to the key rings, the KMS twin of [watchSigningKey]. Each key is read on
// its own, so one that fails does not hide the others, and a failure keeps the
// previous key as a file read that fails does. Re-reading is what notices an
// alias moved to another key: it reads as a new kid and is scheduled as one.
func watchKMSKeys(
	ctx context.Context, sets []*issuer.KMSKeyRefs, interval time.Duration, storage *issuer.Storage, log *slog.Logger,
	checkSecret func(context.Context) error,
) {
	if interval <= 0 {
		interval = issuer.DefaultKeyPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Re-written if the record lapsed; a mismatch is logged, not fatal.
			if err := checkSecret(ctx); err != nil {
				log.WarnContext(ctx, "the KMS state secret check failed", "error", err)
			}
			for _, refs := range sets {
				pollKMSRefs(ctx, refs, storage, log)
			}
		}
	}
}

// pollKMSRefs re-reads one algorithm's list.
func pollKMSRefs(ctx context.Context, refs *issuer.KMSKeyRefs, storage *issuer.Storage, log *slog.Logger) {
	for i, ref := range refs.Refs {
		one := issuer.KMSKeyRefs{Alg: refs.Alg, API: refs.API, Refs: []string{ref}, Seed: refs.Seed}
		keys, err := one.Load(ctx)
		if err != nil {
			log.WarnContext(ctx, "could not re-read a KMS signing key; keeping the previous one",
				"key", ref, "error", err)
			continue
		}
		// List order is age: only the LAST key may be newly recorded. The
		// earlier ones are re-read to refresh a key the ring holds, and a
		// retired one is never brought back by being listed.
		rotate := storage.RotateKnown
		if i == len(refs.Refs)-1 {
			rotate = storage.Rotate
		}
		if err := rotate(ctx, keys[0]); err != nil {
			log.WarnContext(ctx, "a re-read KMS signing key could not be adopted", "key", ref, "error", err)
		}
	}
}

// applySigningPlan makes the registry's resolved signing adapter the one that
// is used. The legacy `signingKey` keys already resolve to it (file by
// default, kms when `signingKey.kms` is written), so a deployment that
// names no adapter runs exactly as before; the choice differs only when a
// preset or `adapters.signing` names one, and then it must agree with what
// `signingKey` says rather than be ignored.
func applySigningPlan(ctx context.Context, cfg *Config, plan port.Table) error {
	choice, ok := plan[port.ConcernSigning]
	if !ok {
		return nil
	}
	desc, found := port.Default.Lookup(port.ConcernSigning, choice.Adapter)
	if !found || desc.Factory == nil {
		return fmt.Errorf("adapters: the signing adapter %q cannot be built", choice.Adapter)
	}
	built, err := desc.Factory(ctx, choice.Settings)
	if err != nil {
		return fmt.Errorf("adapters.signing: %w", err)
	}
	switch choice.Adapter {
	case "kms-wrapped":
		k, _ := built.(*port.KMSWrappedSigning)
		if cfg.signingKeyFile != "" || len(cfg.kmsKeys) > 0 {
			return errors.New("the signing adapter is kms-wrapped and signingKey.file or signingKey.kms is set: " +
				"a key is a file, a KMS key or a KMS-wrapped key pair, not several")
		}
		if cfg.kmsWrapped == nil && k != nil {
			w, err := wrappedFromPort(k)
			if err != nil {
				return fmt.Errorf("adapters.signing: %w", err)
			}
			cfg.kmsWrapped = w
		}
		if _, err := cfg.wrappedConfig(); err != nil {
			return err
		}
	case "kms":
		k, _ := built.(*port.KMSSigning)
		if cfg.signingKeyFile != "" {
			return errors.New("the signing adapter is kms and signingKey.file is set: a key is a file or a KMS key, not both")
		}
		if cfg.kmsWrapped != nil {
			return errors.New("the signing adapter is kms and signingKey.kmsWrapped is set: a key is a KMS key or a KMS-wrapped key pair, not both")
		}
		if len(cfg.kmsKeys) == 0 && k != nil {
			cfg.kmsKeys, cfg.kmsRegion, cfg.kmsStateSecretFile = k.Keys, k.Region, k.StateSecretFile
			for _, a := range k.Additional {
				cfg.kmsAdditional = append(cfg.kmsAdditional, config.SigningKeyKMSAlg{Alg: a.Alg, Keys: a.Keys})
			}
		}
	case "file":
		if len(cfg.kmsKeys) > 0 {
			return errors.New("the signing adapter is file and signingKey.kms is set: a key is a file or a KMS key, not both")
		}
		if cfg.kmsWrapped != nil {
			return errors.New("the signing adapter is file and signingKey.kmsWrapped is set: a key is a file or a KMS-wrapped key pair, not both")
		}
	}
	return nil
}

// wrappedFromPort is the adapter's settings as the file's `signingKey.kmsWrapped`.
func wrappedFromPort(k *port.KMSWrappedSigning) (*config.SigningKeyKMSWrapped, error) {
	out := &config.SigningKeyKMSWrapped{KeyID: k.KeyID, Region: k.Region, StateSecretFile: k.StateSecretFile, Algorithms: k.Algorithms}
	for name, v := range map[string]struct {
		in  string
		out **config.Duration
	}{
		"rotateEvery": {k.RotateEvery, &out.RotateEvery},
		"prepublish":  {k.Prepublish, &out.Prepublish},
		"retain":      {k.Retain, &out.Retain},
	} {
		if v.in == "" {
			continue
		}
		d, err := time.ParseDuration(v.in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		cd := config.Duration(d)
		*v.out = &cd
	}
	return out, nil
}

// wrappedConfig resolves `signingKey.kmsWrapped` against the rest of the
// configuration: the algorithms default to ES384 and RS256, the rotation to
// every 24h, the pre-publish to `signingKey.activationDelay` and the retention
// to `signingKey.overlap` (the token lifetime plus a skew margin), and then the
// schedule is held to what makes it safe (see [issuer.WrappedConfig.Validate]).
func (c Config) wrappedConfig() (issuer.WrappedConfig, error) {
	k := c.kmsWrapped
	out := issuer.WrappedConfig{
		KeyID:       strings.TrimSpace(k.KeyID),
		RotateEvery: dur(k.RotateEvery, issuer.DefaultWrappedRotateEvery),
		Prepublish:  dur(k.Prepublish, c.keyActivationDelay),
		Retain:      dur(k.Retain, c.keyOverlap),
		Interval:    c.keyPollInterval,
	}
	if k.StateSecretFile == "" {
		return out, errors.New("signingKey.kmsWrapped.stateSecretFile is required")
	}
	algs := k.Algorithms
	if len(algs) == 0 {
		algs = []string{string(jose.ES384), string(jose.RS256)}
	}
	for _, a := range algs {
		out.Algorithms = append(out.Algorithms, jose.SignatureAlgorithm(a))
	}
	if out.Prepublish < c.keyPollInterval {
		return out, fmt.Errorf("signingKey.kmsWrapped.prepublish (%v) must be at least signingKey.pollInterval (%v)", out.Prepublish, c.keyPollInterval)
	}
	return out, out.Validate(c.tokenLifetime)
}

// wrappedSigningKeys opens the KMS-wrapped signing at start: the client, the
// state secret, the lease that serialises key generation, and the key each
// algorithm signs with (read from the shared state, or generated now). The
// first algorithm's key is the primary.
func wrappedSigningKeys(
	ctx context.Context, cfg Config, deps Deps, stores *store.Stores, state issuer.State, log *slog.Logger,
) (*issuer.WrappedSigning, *issuer.SigningKey, []*issuer.SigningKey, error) {
	wcfg, err := cfg.wrappedConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err := os.ReadFile(cfg.kmsWrapped.StateSecretFile) //nolint:gosec // the path is deployment configuration
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read signingKey.kmsWrapped.stateSecretFile: %w", err)
	}
	seed, err := parseStateSecret(raw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("signingKey.kmsWrapped.stateSecretFile: %w", err)
	}
	if err = checkStateSecret(ctx, state, seed); err != nil {
		return nil, nil, nil, err
	}
	api := deps.KMSWrapped
	if api == nil {
		var loaders []func(*awsconfig.LoadOptions) error
		if cfg.kmsWrapped.Region != "" {
			loaders = append(loaders, awsconfig.WithRegion(cfg.kmsWrapped.Region))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load the AWS configuration for signingKey.kmsWrapped: %w", err)
		}
		api = kms.NewFromConfig(awsCfg)
	}
	// The lease is on the State port, as every controller's is: a Create with a
	// lifetime. Without a shared store (a local run) it is this process's own.
	leaseState := port.State(memory.New())
	if stores != nil && stores.Usable {
		leaseState = stores.Ports.State
	}
	leases := &rails.Leases{State: leaseState, Holder: rails.NewHolder(), Log: log}
	lease := func(ctx context.Context, alg jose.SignatureAlgorithm, fn func(context.Context) error) (bool, error) {
		var inner error
		ran, err := leases.Do(ctx, "signing-keygen", string(alg), func(lctx context.Context) { inner = fn(lctx) })
		if err != nil {
			return false, err
		}
		return ran, inner
	}
	ws, err := issuer.NewWrappedSigning(wcfg, api, seed, lease, log)
	if err != nil {
		return nil, nil, nil, err
	}
	primary, more, err := ws.Bootstrap(ctx, state)
	if err != nil {
		return nil, nil, nil, err
	}
	log.InfoContext(ctx, "signing with KMS-wrapped keys", "key", wcfg.KeyID, "algorithms", wcfg.Algorithms,
		"rotateEvery", wcfg.RotateEvery, "prepublish", wcfg.Prepublish, "retain", wcfg.Retain,
		"kid", primary.ID(), "algorithm", primary.SignatureAlgorithm())
	return ws, primary, more, nil
}

// parseStateSecret reads the state secret file: one trailing newline trimmed,
// then hex or base64 for at least 32 bytes that look random. A file of one
// repeated character, or of a few distinct bytes, is a placeholder and not a
// secret, and is refused.
func parseStateSecret(raw []byte) ([]byte, error) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	var decoded []byte
	if b, err := hex.DecodeString(text); err == nil {
		decoded = b
	} else {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(text); err == nil {
				decoded = b
				break
			}
		}
	}
	if len(decoded) < 32 {
		return nil, errors.New("must be hex or base64 of at least 32 bytes (for example `openssl rand -base64 32`)")
	}
	distinct := map[byte]bool{}
	for _, b := range decoded {
		distinct[b] = true
	}
	if len(distinct) < 8 {
		return nil, errors.New("looks like a placeholder, not a secret: it has too few distinct bytes")
	}
	return decoded, nil
}

// checkStateSecret publishes a short HMAC fingerprint of the state secret in
// the shared state and refuses to start when it differs from the one already
// there: replicas with different secrets would each reject the other's
// sign-in state. The fingerprint reveals nothing of the secret.
func checkStateSecret(ctx context.Context, state issuer.State, seed []byte) error {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte("sluis/kms-state-secret-fingerprint"))
	fingerprint := []byte(hex.EncodeToString(mac.Sum(nil)[:8]))
	const key = "issuer:kms:state-secret-fingerprint"
	won, err := state.SetIfAbsent(ctx, key, fingerprint, 365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("record the state secret's fingerprint: %w", err)
	}
	if won {
		return nil
	}
	stored, _, err := state.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read the state secret's fingerprint: %w", err)
	}
	if !hmac.Equal(stored, fingerprint) {
		return errors.New("signingKey.kms.stateSecretFile differs from the secret other replicas use " +
			"(fingerprint mismatch): every replica needs the same file. To rotate it deliberately, " +
			"delete the shared state key " + key)
	}
	return nil
}

// BroadAWSMatchers names the groups whose `aws` matchers admit every role of
// their account: one with no `role`, or a bare `*`. That is a rule somebody may
// have meant, and one that grows by itself with the account, so it is said at
// start and never refused (an existing policy may rely on it).
func BroadAWSMatchers(set *policy.Set) []string {
	var out []string
	declared := set.Declared()
	names := make([]string, 0, len(declared.Groups))
	for name := range declared.Groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, m := range declared.Groups[name].Matchers {
			if m.AWS != nil && (m.AWS.Role == "" || m.AWS.Role == "*") {
				out = append(out, name+" (account "+m.AWS.Account+")")
			}
		}
	}
	return out
}

func warnBroadAWSMatchers(ctx context.Context, set *policy.Set, log *slog.Logger) {
	if broad := BroadAWSMatchers(set); len(broad) > 0 {
		log.WarnContext(ctx, "aws matchers with no role (or a bare *) admit EVERY role of the account, "+
			"including roles created later: name the role unless that is meant", "groups", broad)
	}
}

// consoleAWSVerifiers are the AWS accounts' verifiers for the console door: the
// same rows as token exchange's, with the console's own audience.
func consoleAWSVerifiers(f verify.AWSFederation, audience string, client *http.Client) []*verify.AWSAccount {
	f.Audience = audience
	return f.Verifiers(client)
}
