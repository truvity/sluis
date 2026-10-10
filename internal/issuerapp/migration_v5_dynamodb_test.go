package issuerapp_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/config"
	ghconn "github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
)

// A layout v4 installation moved to layout v5 on a real DynamoDB and a real SSM
// (LocalStack), through the same code `sluis migrate v5` runs, and the issuer
// started on each side (docs/decisions/0072, point 7):
//
//   - v4: one table, the SSM names of v1.74, an issuer with a KMS-wrapped ring;
//   - v5: a table per module, the module-first SSM paths, the whole-estate Router
//     for the migration and the oidc module's own table for the issuer.
//
// It is the end-to-end proof of what the unit tests of internal/migrate show part
// by part: a refresh token copied across is good on the destination, one spent
// before the copy is still refused, the JWKS names the same keys, the ring opens
// with the KMS encryption context it was made under and is not wrapped again, the
// maintenance record is not carried, and the source is still good afterwards.
//
// hack/dynamodb-conformance.sh sets ACCESS_ROSTER_DYNAMODB_URL (one LocalStack
// serves DynamoDB, SSM and S3) and fails the run if this test skipped.

// wrapKMS is the key service of the installation: one key, here as in AWS the
// same for the v4 issuer, the v5 issuer and the migration. It refuses any
// encryption context but {instance, purpose: sign}, and records what it opened.
type wrapKMS struct {
	*fakeKMS
	instance string

	mu      sync.Mutex
	opened  int
	sealed  int
	refused []map[string]string
}

func (k *wrapKMS) check(ec map[string]string) error {
	if len(ec) == 2 && ec["instance"] == k.instance && ec["purpose"] == "sign" {
		return nil
	}
	k.mu.Lock()
	k.refused = append(k.refused, ec)
	k.mu.Unlock()
	return errors.New("wrapKMS: the encryption context is not {instance, purpose: sign}")
}

func (k *wrapKMS) Encrypt(ctx context.Context, key string, pt []byte, ec map[string]string) ([]byte, error) {
	if err := k.check(ec); err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.sealed++
	k.mu.Unlock()
	return k.fakeKMS.Encrypt(ctx, key, pt, ec)
}

func (k *wrapKMS) Decrypt(ctx context.Context, key string, ct []byte, ec map[string]string) ([]byte, error) {
	if err := k.check(ec); err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.opened++
	k.mu.Unlock()
	return k.fakeKMS.Decrypt(ctx, key, ct, ec)
}

func (k *wrapKMS) counts() (sealed, opened int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sealed, k.opened
}

// e2eSite is one installation's place on the LocalStack: a DynamoDB table or
// tables, and a root in SSM.
type e2eSite struct {
	url, root, name string
}

func newE2ESite(t *testing.T, endpoint, kind string) e2eSite {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(b)
	return e2eSite{url: endpoint, root: "/sluis/e2e-" + kind + "-" + id, name: "e2e-" + kind + "-" + id}
}

func (s e2eSite) secrets(t *testing.T) *secrets.SSM {
	t.Helper()
	src, err := secrets.NewSSM(context.Background(), s.root, "us-east-1", s.url, secrets.DefaultRefresh)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// open is the stores of a process on this site: layout v4 on one table, layout
// v5 on a table per module. A module of "" is the whole-estate view.
func (s e2eSite) open(t *testing.T, layout5 bool, module port.Module) *store.Stores {
	t.Helper()
	ports := &config.Ports{Adapter: "dynamodb", DynamoDB: &config.DynamoDB{Endpoint: s.url, Region: "us-east-1", Create: true}}
	layout := config.SecretsLayoutV4
	if layout5 {
		layout = config.SecretsLayoutV5
		ports.DynamoDB.Tables = map[string]string{}
		for m, name := range dynamoport.DefaultTables(s.name) {
			ports.DynamoDB.Tables[string(m)] = name
		}
	} else {
		ports.DynamoDB.Table = "sluis-" + s.name
	}
	doc := &config.Serve{
		IssuerURL: "https://issuer.example", Ports: ports,
		Secrets:  &config.Secrets{Source: "ssm", Root: s.root, Region: "us-east-1", Endpoint: s.url, Layout: layout},
		Adapters: map[string]config.AdapterChoice{"secrets": {Adapter: "ssm"}},
	}
	e2eSigning(doc, "acme")
	cfg, err := store.FromServe(doc)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Secrets = s.secrets(t)
	st, err := store.Open(context.Background(), cfg.As(module), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open %s: %v", s.name, err)
	}
	t.Cleanup(st.Close)
	return st
}

// putSSM writes a SecureString at root+name, the way an operator's tooling
// does.
func (s e2eSite) putSSM(t *testing.T, name, value string) {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	client := awsssm.NewFromConfig(cfg, func(o *awsssm.Options) { o.BaseEndpoint = aws.String(s.url) })
	_, err = client.PutParameter(context.Background(), &awsssm.PutParameterInput{
		Name: aws.String(s.root + name), Value: aws.String(value), Type: ssmtypes.ParameterTypeSecureString, Overwrite: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

func randomStateSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b) + "\n"
}

// e2eIssuer starts the issuer on stores, with the wrapped ring under the state
// secret the stores name.
func e2eIssuer(t *testing.T, st *store.Stores, kms *wrapKMS) *issuerapp.App {
	t.Helper()
	return bootDeps(t, issuerapp.Deps{Directory: stubHub(t, true, false), Keys: kms, Stores: st},
		func(f *config.Serve) { e2eSigning(f, kms.instance) })
}

// e2eSigning is the signing section of the document, the same on both layouts:
// the state secret's name is a v4 one, and on layout v5 it is read from
// internal/oidc/state-secret.
func e2eSigning(f *config.Serve, instance string) {
	f.Instance = instance
	f.Keys = signKeysBlock("alias/sluis-" + instance + "-sign")
	f.SigningKey = &config.SigningKey{KMSWrapped: &config.SigningKeyKMSWrapped{StateSecret: "issuer/state-secret"}}
}

func kidSet(t *testing.T, app *issuerapp.App) []string {
	t.Helper()
	var out []string
	for _, k := range keysOf(t, app) {
		out = append(out, k.Alg+"/"+k.Kid)
	}
	slices.Sort(out)
	return out
}

// refresh trades a refresh token for the next, as a relying party does.
func refresh(t *testing.T, app *issuerapp.App, token string) (int, string) {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {"console"}, "scope": {"openid profile email"}}
	_, doc := get(t, app.Handler(), "/.well-known/openid-configuration")
	var discovery struct {
		Token string `json:"token_endpoint"`
	}
	if err := json.Unmarshal([]byte(doc), &discovery); err != nil || discovery.Token == "" {
		t.Fatalf("discovery: %v %s", err, doc)
	}
	req := httptest.NewRequest(http.MethodPost, discovery.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("console", "")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		var body struct {
			Refresh string `json:"refresh_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Refresh == "" {
			t.Fatalf("the refresh answered 200 with no next token: %v", err)
		}
		return rec.Code, body.Refresh
	}
	return rec.Code, rec.Body.String()
}

// issuerKeys is the issuer's records under the key ring's prefix, by key.
func ringKeys(t *testing.T, st *store.Stores) []string {
	t.Helper()
	var out []string
	err := st.Ports.State.(port.StateExporter).ExportState(context.Background(), "issuer:keyring:", func(x port.Exported) error {
		out = append(out, x.Key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func e2ePlanOptions() migrate.PlanOptions {
	return migrate.PlanOptions{
		Sessions:          true,
		Blobs:             migrate.BlobsSkip,
		ExportedGitHubApp: func(string) bool { return false },
		AppRef:            func(string) string { return "" },
	}
}

func TestV5MigrationEndToEndOnDynamoDB(t *testing.T) {
	endpoint := os.Getenv("ACCESS_ROSTER_DYNAMODB_URL")
	if endpoint == "" {
		t.Skip("ACCESS_ROSTER_DYNAMODB_URL is not set: no DynamoDB and SSM to migrate on")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_PROFILE", "")
	ctx := context.Background()

	kms := &wrapKMS{fakeKMS: newFakeKMS(t), instance: "acme"}
	site := newE2ESite(t, endpoint, "src")
	stateSecret := randomStateSecret()

	// 1. A v1.74-shaped installation: one table, the v4 SSM names, the state
	// secret as an operator input, a connected workspace and its credential, an
	// issuer with a wrapped ring and two people's sessions.
	site.putSSM(t, "/internal/config/issuer/state-secret", stateSecret)
	v4 := site.open(t, false, "")
	if v4.V4 == nil || v4.V5 != nil {
		t.Fatalf("the source is not on layout v4: V4=%v V5=%v", v4.V4 != nil, v4.V5 != nil)
	}
	domains, err := migrate.OpenDomains(ctx, v4, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = domains.Workspaces.Put(ctx, hub.Workspace{ID: "C01", Backend: "google", Domains: []string{"acme.example"}, Admin: "root@acme.example",
		Credential: hub.CredentialServiceAccountKey, ConnectedBy: "ada@acme.example", ConnectedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// An organisation connected with an App of its own, the shape of a real
	// estate: its key is the organisation's credential on layout v4. No
	// appRefs entry names it (e2ePlanOptions has none).
	const orgKey = "EXAMPLE-ORG-KEY"
	if err = domains.Orgs.Put(ctx,
		ghconn.Record{Org: "example", AppID: 31, AppSlug: "example-access", InstallationID: 41, ConnectedAt: time.Now().UTC(), ConnectedBy: "ada@acme.example"},
		ghconn.Credential{Org: "example", AppID: 31, InstallationID: 41, PrivateKey: orgKey}); err != nil {
		t.Fatal(err)
	}

	srcApp := e2eIssuer(t, v4, kms)
	kidsBefore := kidSet(t, srcApp)
	if len(kidsBefore) == 0 {
		t.Fatal("the source publishes no keys")
	}
	if len(ringKeys(t, v4)) == 0 {
		t.Fatal("the source issuer wrote no ring: it would be generated again on the destination")
	}
	sealedAtSource, _ := kms.counts()

	open := func(app *issuerapp.App, who, token string) {
		t.Helper()
		_, err := app.Issuer().Sessions().Record(ctx, issuer.Opened{
			Identity: who, ClientID: "console", How: issuer.HowCode, Token: token, Scopes: []string{"openid", "profile", "email"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	open(srcApp, "ada@north.example", "refresh-spent-0")
	open(srcApp, "grace@north.example", "refresh-live-0")
	// Ada's token is used once, before the copy: the token she holds now is the
	// next, and "refresh-spent-0" is spent.
	code, ada1 := refresh(t, srcApp, "refresh-spent-0")
	if code != http.StatusOK {
		t.Fatalf("the source refused a good refresh token: %d %s", code, ada1)
	}
	// The source is in maintenance, as it is while its writers are stopped.
	if err = maintenance.Write(ctx, v4.Ports.State, maintenance.Flag{State: maintenance.StateRestoring, By: "migration", Reason: "layout v5"}); err != nil {
		t.Fatal(err)
	}

	// 2. plan, copy and verify through the Router, the way `sluis migrate v5` does.
	dstSite := newE2ESite(t, endpoint, "dst")
	dstSite.root = site.root // the v5 paths are beside the v4 names under one root
	v5all := dstSite.open(t, true, "")
	opt := e2ePlanOptions()
	from, to := migrate.Side{Name: "v4", Stores: v4}, migrate.Side{Name: "v5", Stores: v5all}

	plan, err := migrate.Plan(ctx, from, to, opt)
	if err != nil || plan.Totals.Refused != 0 || plan.Totals.New == 0 || plan.Totals.Same != 0 {
		t.Fatalf("Plan = %v\n%s", err, plan.JSON())
	}
	if _, err = migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt, DryRun: true}); err != nil {
		t.Fatalf("a dry run: %v", err)
	}
	if got := ringKeys(t, v5all); len(got) != 0 {
		t.Fatalf("a dry run wrote %v", got)
	}
	if _, err = migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt}); !errors.Is(err, migrate.ErrWritersRunning) {
		t.Fatalf("a copy that was not told the writers are stopped = %v", err)
	}
	report, err := migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt, WritersStopped: true})
	if err != nil || !report.OK || report.Totals.Copied == 0 || report.Totals.Refused != 0 || report.Totals.Different != 0 {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if v := report.Verify; v == nil || !v.OK || v.Missing != 0 || v.Different != 0 {
		t.Fatalf("the verify after the copy = %+v", report.Verify)
	}
	verified, err := migrate.VerifyV5(ctx, from, to, opt)
	if err != nil || !verified.OK || verified.Totals.Same == 0 {
		t.Fatalf("VerifyV5 = %v\n%s", err, verified.JSON())
	}
	// The organisation names an App made of its own credential, which holds the
	// key once; the report holds no key.
	dstDomains, err := migrate.OpenDomains(ctx, v5all, false)
	if err != nil {
		t.Fatal(err)
	}
	if orgs, err := dstDomains.Orgs.List(ctx); err != nil || len(orgs) != 1 || orgs[0].AppRef != "example-access" {
		t.Fatalf("the destination's organisations = %+v, %v", orgs, err)
	}
	if _, key, found, err := dstDomains.Catalogue.Get(ctx, "example-access"); err != nil || !found || key != orgKey {
		t.Fatalf("the App made of the organisation's credential = found %v, key equal %v, %v", found, key == orgKey, err)
	}
	if strings.Contains(string(report.JSON()), orgKey) {
		t.Error("the copy's report holds the organisation's key")
	}
	again, err := migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt, WritersStopped: true})
	if err != nil || again.Totals.Copied != 0 || again.Totals.New != 0 {
		t.Fatalf("a second copy = %v\n%s", err, again.JSON())
	}
	for _, secret := range []string{strings.TrimSpace(stateSecret), "refresh-live-0", "refresh-spent-0", ada1} {
		if strings.Contains(string(report.JSON()), secret) {
			t.Error("the copy's report holds a secret or a token")
		}
	}

	// The maintenance record is the source's alone, and the destination starts
	// clear: it is in no module's table.
	for _, m := range port.Modules() {
		own, ok := v5all.Tables.Store(m)
		if !ok {
			t.Fatalf("no table for %s", m)
		}
		if _, err = own.Get(ctx, maintenance.Key); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("the %s table holds the maintenance record (%v): the destination must start clear", m, err)
		}
	}
	if _, err = v4.Ports.State.Get(ctx, maintenance.Key); err != nil {
		t.Errorf("the copy took the maintenance record from the source: %v", err)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "maintenance flag") {
		t.Error("the report does not say the maintenance flag is not carried")
	}

	// 3. The v1.75 issuer on layout v5, over the oidc module's table: the copied
	// ring is opened, under the context it was made, and is not made again.
	v5 := dstSite.open(t, true, port.ModuleOIDC)
	if v5.V5 == nil || v5.V4 != nil {
		t.Fatalf("the destination is not on layout v5: V4=%v V5=%v", v5.V4 != nil, v5.V5 != nil)
	}
	_, openedBefore := kms.counts()
	dstApp := e2eIssuer(t, v5, kms)
	sealedAfter, openedAfter := kms.counts()
	if sealedAfter != sealedAtSource {
		t.Errorf("the destination wrapped %d key(s) again; the copied ring has to be used as it is", sealedAfter-sealedAtSource)
	}
	if openedAfter <= openedBefore {
		t.Error("the destination opened no ring entry with the KMS")
	}
	if len(kms.refused) != 0 {
		t.Errorf("the KMS refused encryption contexts: %v", kms.refused)
	}

	// The JWKS is the same set of keys, by kid and algorithm.
	if kidsAfter := kidSet(t, dstApp); !slices.Equal(kidsBefore, kidsAfter) {
		t.Errorf("the key set changed across the migration:\n before %v\n after  %v", kidsBefore, kidsAfter)
	}
	// The destination serves: it is not in maintenance.
	if code, _ := get(t, dstApp.Handler(), "/.well-known/openid-configuration"); code != http.StatusOK {
		t.Errorf("discovery on the destination = %d", code)
	}

	// A copied refresh token is good on the destination, and gives the next.
	code, grace1 := refresh(t, dstApp, "refresh-live-0")
	if code != http.StatusOK {
		t.Fatalf("a refresh token copied across was refused on the destination: %d %s", code, grace1)
	}
	if code, body := refresh(t, dstApp, ada1); code != http.StatusOK {
		t.Fatalf("Ada's current refresh token was refused on the destination: %d %s", code, body)
	}
	// The token spent before the copy is spent on the destination too.
	if code, body := refresh(t, dstApp, "refresh-spent-0"); code == http.StatusOK || !strings.Contains(body, "invalid_grant") {
		t.Errorf("a refresh token spent before the copy = %d %s, want it refused", code, body)
	}
	if code, body := refresh(t, dstApp, "refresh-never-issued"); code == http.StatusOK {
		t.Errorf("an unknown refresh token = %d %s", code, body)
	}

	// 4. Rollback: the source is as it was. Its maintenance record is the
	// operator's to clear; until then it refuses writes, as designed.
	rollback := e2eIssuer(t, v4, kms)
	if code, _ := refresh(t, rollback, "refresh-live-0"); code != http.StatusServiceUnavailable {
		t.Errorf("the source with its maintenance record set answered %d, want 503", code)
	}
	if err = maintenance.Clear(ctx, v4.Ports.State); err != nil {
		t.Fatal(err)
	}
	// Past the gate's cache: a new process.
	rollback = e2eIssuer(t, v4, kms)
	if kids := kidSet(t, rollback); !slices.Equal(kids, kidsBefore) {
		t.Errorf("the source's key set changed: %v, want %v", kids, kidsBefore)
	}
	// The destination spent "refresh-live-0" and the source did not know: on the
	// source it is still good, and so is Ada's token.
	if code, body := refresh(t, rollback, "refresh-live-0"); code != http.StatusOK {
		t.Errorf("the source refused a token after the rollback: %d %s", code, body)
	}
	if code, body := refresh(t, rollback, ada1); code != http.StatusOK {
		t.Errorf("the source refused Ada's token after the rollback: %d %s", code, body)
	}
	if code, _ := refresh(t, rollback, "refresh-spent-0"); code == http.StatusOK {
		t.Error("the source accepted a spent token")
	}
	if _, err = domains.Workspaces.Get(ctx, "C01"); err != nil {
		t.Errorf("the source lost its workspace: %v", err)
	}
	if got, err := migrate.OpenDomains(ctx, v5all, false); err != nil {
		t.Fatal(err)
	} else if w, err := got.Workspaces.Get(ctx, "C01"); err != nil || w.ID != "C01" {
		t.Errorf("the workspace is not on the destination: %v", err)
	}
}

// firstSignIn is the state secret's fingerprint check an issuer makes at its
// first sign-in, over the State of st, with seed: nil when it passes.
func firstSignIn(t *testing.T, st *store.Stores, secret string) error {
	t.Helper()
	seed, err := secrets.ParseStateSecret([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	gated, _ := issuerapp.GateSignInsForTest([]issuer.SignIn{&gateProbe{}},
		issuer.NewPortState(st.Ports.State, st.Ports.Index), seed, st.Ports.State)
	_, err = gated[0].(issuer.ContextSignIn).URLContext(context.Background(), "s")
	return err
}

// The guard conflict: a destination whose issuer already started and recorded
// the fingerprint of its own state secret (at its first sign-in) holds something
// the source may not, so a copy reports it as different and writes nothing until
// it is told --overwrite.
func TestV5CopyOverAStartedDestinationNeedsOverwriteOnDynamoDB(t *testing.T) {
	endpoint := os.Getenv("ACCESS_ROSTER_DYNAMODB_URL")
	if endpoint == "" {
		t.Skip("ACCESS_ROSTER_DYNAMODB_URL is not set: no DynamoDB and SSM to migrate on")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_PROFILE", "")

	for name, sourceSignedIn := range map[string]bool{"the source recorded a fingerprint": true, "the source never signed anyone in": false} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kms := &wrapKMS{fakeKMS: newFakeKMS(t), instance: "acme"}
			srcSite := newE2ESite(t, endpoint, "gsrc")
			srcSecret := randomStateSecret()
			srcSite.putSSM(t, "/internal/config/issuer/state-secret", srcSecret)
			v4 := srcSite.open(t, false, "")
			srcApp := e2eIssuer(t, v4, kms)
			if _, err := srcApp.Issuer().Sessions().Record(ctx, issuer.Opened{
				Identity: "ada@north.example", ClientID: "console", How: issuer.HowCode, Token: "refresh-0", Scopes: []string{"openid"},
			}); err != nil {
				t.Fatal(err)
			}
			if sourceSignedIn {
				if err := firstSignIn(t, v4, srcSecret); err != nil {
					t.Fatal(err)
				}
			}
			kidsBefore := kidSet(t, srcApp)

			// The destination was brought up and signed somebody in before the
			// copy, with a state secret of its own.
			dstSite := newE2ESite(t, endpoint, "gdst")
			dstSite.root = srcSite.root
			v5all := dstSite.open(t, true, "")
			v5 := dstSite.open(t, true, port.ModuleOIDC)
			ownSecret := randomStateSecret()
			if _, err := v5.V5.OIDC().StateSecret().Put(ctx, []byte(ownSecret), ""); err != nil {
				t.Fatal(err)
			}
			early := e2eIssuer(t, v5, kms)
			if err := firstSignIn(t, v5, ownSecret); err != nil {
				t.Fatalf("the destination's first sign-in: %v", err)
			}
			earlyKids := kidSet(t, early)
			if slices.Equal(earlyKids, kidsBefore) {
				t.Fatal("the early destination published the source's keys: the fixture proves nothing")
			}

			opt := e2ePlanOptions()
			from, to := migrate.Side{Name: "v4", Stores: v4}, migrate.Side{Name: "v5", Stores: v5all}
			ringBefore := ringKeys(t, v5all)
			report, err := migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt, WritersStopped: true})
			if !errors.Is(err, migrate.ErrConflict) || report == nil || report.OK || report.Totals.Copied != 0 {
				t.Fatalf("CopyV5 over a started destination = %v, want ErrConflict and nothing written", err)
			}
			if !strings.Contains(err.Error(), "--overwrite") {
				t.Errorf("the refusal does not name --overwrite: %v", err)
			}
			different := map[string]bool{}
			for _, m := range report.Modules {
				for _, it := range m.Items {
					if it.Status == migrate.PlanDifferent {
						different[it.Kind] = true
					}
				}
			}
			for _, kind := range []string{"state-secret", "keyring-index"} {
				if !different[kind] {
					t.Errorf("%s is not among the different items: %v", kind, different)
				}
			}
			// The fingerprint is a different item whether or not the source has
			// one: the destination's guard is to match the source's, absence too.
			if !different["guard"] {
				t.Errorf("the state secret's fingerprint is not among the different items: %v", different)
			}
			if got := ringKeys(t, v5all); !slices.Equal(got, ringBefore) {
				t.Errorf("a refused copy wrote to the destination's ring: %v, was %v", got, ringBefore)
			}

			// With --overwrite the destination takes the source's state secret,
			// ring schedule and fingerprint.
			report, err = migrate.CopyV5(ctx, from, to, migrate.V5Options{PlanOptions: opt, WritersStopped: true, Overwrite: true})
			if err != nil || !report.OK || report.Totals.Copied == 0 {
				t.Fatalf("CopyV5 --overwrite = %v\n%s", err, report.JSON())
			}
			if _, err = migrate.VerifyV5(ctx, from, to, opt); err != nil {
				t.Fatalf("VerifyV5 after --overwrite: %v", err)
			}
			// The destination's own ring entries stay in its table, but the
			// schedule (the index) is the source's: the published keys are the
			// source's alone.
			after := kidSet(t, e2eIssuer(t, dstSite.open(t, true, port.ModuleOIDC), kms))
			if !slices.Equal(after, kidsBefore) {
				t.Errorf("the keys published after --overwrite are %v, want the source's %v", after, kidsBefore)
			}
			// The fingerprint the destination recorded for its own secret is
			// replaced by the source's, or deleted when the source has none, so
			// the first sign-in with the source's secret passes either way.
			if err = firstSignIn(t, dstSite.open(t, true, port.ModuleOIDC), srcSecret); err != nil {
				t.Errorf("the first sign-in on the destination after --overwrite: %v", err)
			}
		})
	}
}

// gateProbe is a sign-in that does nothing but be gated.
type gateProbe struct{}

func (*gateProbe) Kind() string                                     { return "probe" }
func (*gateProbe) URL(string) (string, error)                       { return "https://idp.example", nil }
func (*gateProbe) Identify(context.Context, string) (string, error) { return "a@b.example", nil }
func (*gateProbe) URLContext(context.Context, string) (string, error) {
	return "https://idp.example", nil
}
