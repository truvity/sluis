package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// tablesOf names a table for each module, with no estate in the names.
func tablesOf(mods ...port.Module) map[port.Module]string {
	out := map[port.Module]string{}
	for _, m := range mods {
		out[m] = dynamoport.TableName("example", m)
	}
	return out
}

// fakeAWS points the SDK at an address nothing listens on, with credentials
// that are not any account's: opening makes no request, and a test that makes
// one fails at once.
func fakeAWS(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_PROFILE", "")
}

func TestTheLayoutsDoNotMix(t *testing.T) {
	v5 := tablesOf(port.ModuleOIDC, port.ModuleGoogle)
	for name, tc := range map[string]struct {
		cfg  Config
		want string // "" is accepted
	}{
		"v5 tables, v5":       {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true, SecretsLayout: "v5"}, ""},
		"v5 tables, no ssm":   {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}}, ""},
		"v5 tables, v4":       {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true, SecretsLayout: "v4"}, "secrets.layout"},
		"v5 tables, unnamed":  {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true}, "secrets.layout"},
		"memory, v4":          {Config{Adapter: AdapterMemory, SecretsSSM: true, SecretsLayout: "v4"}, "secrets.layout"},
		"dynamodb, no tables": {Config{Adapter: AdapterDynamoDB}, "ports.dynamodb.tables"},
		"tables, memory":      {Config{Adapter: AdapterMemory, DynamoDB: dynamoport.Config{Tables: v5}}, "dynamodb adapter's"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.cfg.layout5()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	// Open refuses the same before it opens anything.
	_, err := Open(context.Background(), Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true, SecretsLayout: "v4"}, nil)
	if err == nil {
		t.Error("Open accepted layout v4")
	}
}

func TestFromServeReadsTheTablesAndTheLayout(t *testing.T) {
	cfg, err := FromServe(&config.Serve{
		IssuerURL: "https://i.example",
		Ports:     &config.Ports{Adapter: "dynamodb", DynamoDB: &config.DynamoDB{Tables: map[string]string{"oidc": "a", "google": "b"}}},
		Secrets:   &config.Secrets{Source: "ssm", Root: "/sluis/example", Layout: "v5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DynamoDB.Tables[port.ModuleOIDC] != "a" || cfg.DynamoDB.Tables[port.ModuleGoogle] != "b" || cfg.SecretsLayout != "v5" {
		t.Errorf("cfg = %+v", cfg)
	}
	_, err = FromServe(&config.Serve{
		IssuerURL: "https://i.example",
		Ports:     &config.Ports{Adapter: "dynamodb", DynamoDB: &config.DynamoDB{Tables: map[string]string{"oidc": "a"}}},
		Secrets:   &config.Secrets{Source: "ssm", Root: "/sluis/example"},
	})
	if err == nil {
		t.Error("v5 tables with no secrets layout were accepted")
	}
}

func TestOpenOnLayout5GivesTheModuleItsTableAndItsPeersAView(t *testing.T) {
	fakeAWS(t)
	ctx := context.Background()
	all := tablesOf(port.Modules()...)
	cfg := Config{
		Adapter: AdapterDynamoDB, Kube: KubeNone, Module: port.ModuleOIDC,
		Peers:    []port.Module{port.ModuleGoogle, port.ModuleGitHub},
		DynamoDB: dynamoport.Config{Tables: all, Endpoint: "http://127.0.0.1:1"},
		Blob:     &config.PortsBlob{Adapter: BlobS3, S3: &config.PortsBlobS3{Bucket: "example", Endpoint: "http://127.0.0.1:1", Region: "eu-west-1"}},
	}
	st, err := Open(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Tables == nil || st.Ports.Module != port.ModuleOIDC {
		t.Fatalf("Tables = %v, Module = %q", st.Tables, st.Ports.Module)
	}
	for _, m := range []port.Module{port.ModuleGoogle, port.ModuleGitHub} {
		if _, ok := st.Ports.Peer(m); !ok {
			t.Errorf("no %s peer", m)
		}
	}
	if _, ok := st.Ports.Peer(port.ModuleSlack); ok {
		t.Error("a peer nobody named")
	}
	// A write of another module's key is refused before any request.
	if _, err = st.Ports.State.Put(ctx, "ws.dir.google.w1", []byte("{}"), 0); !errors.Is(err, port.ErrNotOwner) {
		t.Errorf("a foreign write = %v, want ErrNotOwner", err)
	}
	// ForModule is another module's table of the same process, with the same
	// blob; asking for one with no table is an error.
	g, err := st.ForModule(port.ModuleGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if g.Module != port.ModuleGoogle || g.Blob == nil || len(g.Peers) != 0 {
		t.Errorf("ForModule(google) = %+v", g)
	}
	if _, err = st.ForModule(port.ModuleGoogle, port.ModuleSlack, port.ModuleBackup); err != nil {
		t.Error(err)
	}
	narrow := cfg
	narrow.DynamoDB.Tables = tablesOf(port.ModuleOIDC)
	narrow.Peers = nil
	ns, err := Open(ctx, narrow, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()
	if _, err = ns.ForModule(port.ModuleGoogle); err == nil {
		t.Error("ForModule of a module with no table succeeded")
	}
	// A peer named without a table is refused at open.
	narrow.Peers = []port.Module{port.ModuleGoogle}
	if _, err = Open(ctx, narrow, quiet); err == nil {
		t.Error("a peer with no table was accepted")
	}
}

func TestOpenOnLayout5WithNoModuleIsTheWholeEstate(t *testing.T) {
	fakeAWS(t)
	cfg := Config{
		Adapter: AdapterDynamoDB, Kube: KubeNone,
		DynamoDB: dynamoport.Config{Tables: tablesOf(port.Modules()...), Endpoint: "http://127.0.0.1:1"},
		Blob:     &config.PortsBlob{Adapter: BlobS3, S3: &config.PortsBlobS3{Bucket: "example", Endpoint: "http://127.0.0.1:1", Region: "eu-west-1"}},
	}
	st, err := Open(context.Background(), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Ports.Module != "" || len(st.Ports.Peers) != 0 || st.Ports.State == nil || st.Ports.Index == nil || st.Ports.Trigger == nil {
		t.Errorf("Ports = %+v", st.Ports)
	}
}

func TestMemoryOnLayout5IsATablePerModule(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		Adapter: AdapterMemory, Module: port.ModuleOIDC, Peers: []port.Module{port.ModuleGoogle},
		SecretsSSM: true, SecretsLayout: "v5",
	}
	st, err := Open(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Ports.Module != port.ModuleOIDC {
		t.Fatalf("Module = %q", st.Ports.Module)
	}
	if _, err = st.Ports.State.Put(ctx, "ws.dir.google.w1", []byte("{}"), 0); !errors.Is(err, port.ErrNotOwner) {
		t.Errorf("a foreign write = %v, want ErrNotOwner", err)
	}
	g, err := st.ForModule(port.ModuleGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.State.Put(ctx, "ws.dir.google.w1", []byte("{}"), 0); err != nil {
		t.Fatal(err)
	}
	// What google wrote, the issuer's peer view reads; its own table has none.
	p, _ := st.Ports.Peer(port.ModuleGoogle)
	if _, err = p.Get(ctx, "ws.dir.google.w1"); err != nil {
		t.Errorf("peer read: %v", err)
	}
	if _, err = st.Ports.State.Get(ctx, "ws.dir.google.w1"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("own table: %v", err)
	}

	// Memory with no tables and no ssm secrets is one store, and ForModule is
	// the Ports.
	one, err := Open(ctx, Config{Adapter: AdapterMemory, Module: port.ModuleOIDC}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if one.Ports.Module != "" {
		t.Errorf("one memory store is split: %q", one.Ports.Module)
	}
	if set, err := one.ForModule(port.ModuleGoogle); err != nil || set.Module != "" {
		t.Errorf("ForModule on one store = %+v, %v", set, err)
	}
}

func TestSecretsOnLayout5AreModuleFirst(t *testing.T) {
	ctx := context.Background()
	var prefix string
	root := statememory.New()
	open := func(_ context.Context, p string, _ ...state.Option) (state.Store, error) {
		prefix = p
		return root, nil
	}
	c := Config{SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsLayout: "v5", OpenState: open, held: &secretsHolder{}}
	if err := c.overLayout5(ctx, c.SecretsRoot, c.SecretsKMSKey); err != nil {
		t.Fatal(err)
	}
	if c.held.v5 == nil || prefix != "/sluis/example" {
		t.Fatalf("holder = %+v, prefix %q", c.held, prefix)
	}
	if _, err := c.held.v5.OIDC().StateSecret().Put(ctx, []byte("s"), ""); err != nil {
		t.Fatal(err)
	}
	// The blob's static credentials are the module's document, internal/<module>/<name>.
	c.Blob = &config.PortsBlob{Adapter: BlobS3, S3: &config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", CredentialsRef: "internal/oidc/blobs-r2"}}
	doc, err := c.held.v5.S3Credentials("internal/oidc/blobs-r2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "k", SecretAccessKey: "s"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = c.s3Blob(ctx); err != nil {
		t.Errorf("s3Blob = %v", err)
	}
	if _, err = root.Child("internal").Child("oidc").Get(ctx, "blobs-r2"); err != nil {
		t.Errorf("the document is not at internal/oidc/blobs-r2: %v", err)
	}
	// An address whose first segment is no module, and another module's, are refused.
	for ref, want := range map[string]string{"internal/blobs/r2": "not internal/<module>/<name>", "internal/github/blobs-r2": "runs the oidc module"} {
		c.Blob.S3.CredentialsRef = ref
		c.Module = port.ModuleOIDC
		if _, err = c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("s3Blob(%s) = %v, want %q", ref, err, want)
		}
	}
	c.Blob.S3.CredentialsRef = "internal/blobs/r2"
	if err = c.validatePorts(); err == nil {
		t.Error("validatePorts accepted an address whose first segment is no module")
	}
}

// The credential of the blob port is read by every process that has the port,
// from the one address the blob prefix has: internal/google/<name>. The oidc
// process (the issuer, the hub and the console in one function), the roster
// controllers and the backup are not the google module, and each reads it; a
// credential in another module's prefix is still refused.
func TestTheBlobCredentialOfTheOwnerIsReadableByEveryModule(t *testing.T) {
	ctx := context.Background()
	const ref = "internal/google/blobs-r2"
	for _, m := range []port.Module{port.ModuleOIDC, port.ModuleGitHub, port.ModuleSlack, port.ModuleGoogle, port.ModuleBackup} {
		root := statememory.New()
		open := func(context.Context, string, ...state.Option) (state.Store, error) { return root, nil }
		c := Config{SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsLayout: "v5", OpenState: open, held: &secretsHolder{}}
		if err := c.overLayout5(ctx, c.SecretsRoot, c.SecretsKMSKey); err != nil {
			t.Fatal(err)
		}
		c = c.As(m)
		c.Blob = &config.PortsBlob{Adapter: BlobS3, S3: &config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", CredentialsRef: ref}}
		doc, err := c.held.v5.S3Credentials(ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "k", SecretAccessKey: "s"}, ""); err != nil {
			t.Fatal(err)
		}
		if err = c.validatePorts(); err != nil {
			t.Errorf("%s: validatePorts = %v", m, err)
		}
		if _, err = c.s3Blob(ctx); err != nil {
			t.Errorf("%s: s3Blob(%s) = %v", m, ref, err)
		}
		if m != port.ModuleGitHub {
			c.Blob.S3.CredentialsRef = "internal/github/blobs-r2"
			if _, err = c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), "is the github module's") {
				t.Errorf("%s: another module's credential = %v, want a refusal", m, err)
			}
		}
	}
}

// The service document the layout v5 library renders for an estate with an
// external blob store is one the oidc process starts on. The library's own test
// (deploy/pulumi, TestTheRenderedV5DocumentStartsTheRuntime) renders it and runs
// this test over it, so that the library, the migration and the runtime cannot
// name different addresses for the credential again.
func TestTheRenderedLayoutV5DocumentPassesTheRuntimeChecks(t *testing.T) {
	file := os.Getenv("SLUIS_RENDERED_SERVICE")
	if file == "" {
		t.Skip("SLUIS_RENDERED_SERVICE names the document deploy/pulumi renders; it is set by that test")
	}
	ctx := context.Background()
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := FromServe(&svc.Serve)
	if err != nil {
		t.Fatalf("FromServe: %v", err)
	}
	if c.Blob == nil || c.Blob.S3 == nil || c.Blob.S3.CredentialsRef == "" {
		t.Fatalf("the rendered document has no ports.blob.s3.credentialsRef: %+v", c.Blob)
	}
	ref := c.Blob.S3.CredentialsRef
	root := statememory.New()
	open := func(context.Context, string, ...state.Option) (state.Store, error) { return root, nil }
	c.OpenState = open
	c.held = &secretsHolder{}
	if err = c.overLayout5(ctx, c.SecretsRoot, c.SecretsKMSKey); err != nil {
		t.Fatal(err)
	}
	// The oidc process, with the peers the issuer reads.
	c = c.As(port.ModuleOIDC, port.ModuleGoogle, port.ModuleGitHub, port.ModuleSlack)
	doc, err := c.held.v5.S3Credentials(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "k", SecretAccessKey: "s"}, ""); err != nil {
		t.Fatal(err)
	}
	if err = c.validatePorts(); err != nil {
		t.Fatalf("validatePorts: %v", err)
	}
	if _, err = c.s3Blob(ctx); err != nil {
		t.Fatalf("the oidc process cannot start on the rendered ref %s: %v", ref, err)
	}
}
