package store

import (
	"context"
	"errors"
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
		"v4 table, v4 secrets": {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Table: "t"}, SecretsSSM: true, SecretsLayout: "v4"}, ""},
		"v4 table, default":    {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Table: "t"}, SecretsSSM: true}, ""},
		"v5 tables, v5":        {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true, SecretsLayout: "v5"}, ""},
		"v5 tables, no ssm":    {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}}, ""},
		"v5 tables, v4":        {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true, SecretsLayout: "v4"}, "secrets.layout"},
		"v5 tables, default":   {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Tables: v5}, SecretsSSM: true}, "secrets.layout"},
		"v4 table, v5": {
			Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Table: "t"}, SecretsSSM: true, SecretsLayout: "v5"}, "ports.dynamodb.tables",
		},
		"both":           {Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Table: "t", Tables: v5}}, "both set"},
		"tables, memory": {Config{Adapter: AdapterMemory, DynamoDB: dynamoport.Config{Tables: v5}}, "dynamodb adapter's"},
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
	_, err := Open(context.Background(), Config{Adapter: AdapterDynamoDB, DynamoDB: dynamoport.Config{Table: "t", Tables: v5}}, nil)
	if err == nil {
		t.Error("Open accepted a mixed configuration")
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
	if cfg.DynamoDB.Tables[port.ModuleOIDC] != "a" || cfg.DynamoDB.Tables[port.ModuleGoogle] != "b" || cfg.DynamoDB.Table != "" || cfg.SecretsLayout != "v5" {
		t.Errorf("cfg = %+v", cfg)
	}
	_, err = FromServe(&config.Serve{
		IssuerURL: "https://i.example",
		Ports:     &config.Ports{Adapter: "dynamodb", DynamoDB: &config.DynamoDB{Tables: map[string]string{"oidc": "a"}}},
		Secrets:   &config.Secrets{Source: "ssm", Root: "/sluis/example"},
	})
	if err == nil {
		t.Error("v5 tables with the default secrets layout were accepted")
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

	// On layout v4 there is one store and ForModule is the Ports.
	v4, err := Open(ctx, Config{Adapter: AdapterMemory, Module: port.ModuleOIDC}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer v4.Close()
	if v4.Ports.Module != "" {
		t.Errorf("layout v4 memory is split: %q", v4.Ports.Module)
	}
	if set, err := v4.ForModule(port.ModuleGoogle); err != nil || set.Module != "" {
		t.Errorf("ForModule on v4 = %+v, %v", set, err)
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
	c := Config{SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsLayout: "v5", OpenState: open, v4: &v4Holder{}}
	if err := c.overLayout5(ctx, c.SecretsRoot, c.SecretsKMSKey); err != nil {
		t.Fatal(err)
	}
	if c.v4.v5 == nil || c.v4.stores != nil || prefix != "/sluis/example" {
		t.Fatalf("holder = %+v, prefix %q", c.v4, prefix)
	}
	if _, err := c.v4.v5.OIDC().StateSecret().Put(ctx, []byte("s"), ""); err != nil {
		t.Fatal(err)
	}
	// Layout v5 is not layout v4's Open.
	if _, err := secretstore.Open(ctx, &config.Secrets{Source: "ssm", Root: "/sluis/example", Layout: "v5"}, open); err == nil {
		t.Error("secretstore.Open (layout v4) accepted v5")
	}
	// The blob's static credentials are the module's document, internal/<module>/<name>.
	c.Blob = &config.PortsBlob{Adapter: BlobS3, S3: &config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", CredentialsRef: "internal/oidc/blobs-r2"}}
	doc, err := c.v4.v5.S3Credentials("internal/oidc/blobs-r2")
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
	// A v4 address (a kind that is no module) and another module's are refused.
	for ref, want := range map[string]string{"internal/blobs/r2": "not internal/<module>/<name>", "internal/github/blobs-r2": "runs the oidc module"} {
		c.Blob.S3.CredentialsRef = ref
		c.Module = port.ModuleOIDC
		if _, err = c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("s3Blob(%s) = %v, want %q", ref, err, want)
		}
	}
	c.Blob.S3.CredentialsRef = "internal/blobs/r2"
	if err = c.validatePorts(); err == nil {
		t.Error("validatePorts accepted a layout v4 address on layout v5")
	}
}
