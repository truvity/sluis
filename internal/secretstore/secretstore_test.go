package secretstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

func newStores(t *testing.T) (*secretstore.Stores, state.Store) {
	t.Helper()
	root := memory.New()
	return secretstore.FromStore(root, ""), root
}

func readFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each external kind: the encoder's output for a fixture is the golden file,
// every field the schema requires is present and a string, the schema field is
// the schema's const, and no field is outside the schema's properties.
func TestExternalDocumentsAreGoldenAndMatchTheirSchema(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)

	cases := []struct {
		name    string
		address string // where the kind encodes to, under external/
		put     func() error
	}{
		{"oidc", "oidc/example-rp", func() error {
			_, err := st.External.OIDC("example-rp").Put(ctx, secretstore.OIDCv1{ClientID: "example-rp", ClientSecret: "example-secret-value"}, "")
			return err
		}},
		{"github", "github/example-app", func() error {
			_, err := st.External.GitHubApp("example-app").Put(ctx, secretstore.GitHubv1{
				AppID: "12345", InstallationID: "67890",
				PrivateKey:    "-----BEGIN EXAMPLE KEY-----\nexample\n-----END EXAMPLE KEY-----\n",
				WebhookSecret: "example-webhook-secret",
			}, "")
			return err
		}},
		{"slack", "slack/example-bot", func() error {
			_, err := st.External.SlackApp("example-bot").Put(ctx, secretstore.Slackv1{BotToken: "example-bot-token"}, "")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.put(); err != nil {
				t.Fatal(err)
			}
			it, err := root.Child("external").Get(ctx, tc.address)
			if err != nil {
				t.Fatalf("the document is not at external/%s: %v", tc.address, err)
			}
			golden := readFile(t, "testdata", tc.name+".v1.golden.json")
			if string(it.Value) != string(golden) {
				t.Fatalf("encoder output changed:\n got %s\nwant %s\nchanging a field is breaking: move the schema version", it.Value, golden)
			}

			var schema struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(readFile(t, "..", "..", "schemas", "external", tc.name+".v1.schema.json"), &schema); err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(it.Value, &doc); err != nil {
				t.Fatal(err)
			}
			for _, field := range schema.Required {
				v, ok := doc[field]
				if !ok {
					t.Errorf("required field %q is missing", field)
				} else if _, isString := v.(string); !isString {
					t.Errorf("field %q is %T, want a string", field, v)
				}
			}
			for field := range doc {
				if _, ok := schema.Properties[field]; !ok {
					t.Errorf("field %q is not in the schema", field)
				}
			}
			if want := schema.Properties["schema"]["const"]; doc["schema"] != want {
				t.Errorf("schema field is %v, the schema's const is %v", doc["schema"], want)
			}

			// And it reads back through the typed value.
			if tc.name == "oidc" {
				got, _, err := st.External.OIDC("example-rp").Get(ctx)
				if err != nil || got.Schema != secretstore.SchemaOIDCv1 || got.ClientSecret != "example-secret-value" {
					t.Fatalf("read back = %+v, %v", got, err)
				}
			}
		})
	}
}

func TestDocumentsRefuseWhatTheSchemaRefuses(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)

	if _, err := st.External.OIDC("c").Put(ctx, secretstore.OIDCv1{ClientID: "c"}, ""); !errors.Is(err, secretstore.ErrSchema) {
		t.Fatalf("put without a secret = %v, want ErrSchema", err)
	}
	// A document of another schema at the address is refused on read.
	if _, err := root.Child("external").Put(ctx, "slack/x", []byte(`{"schema":"oidc/v1","client-id":"a","client-secret":"b"}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.External.SlackApp("x").Get(ctx); !errors.Is(err, secretstore.ErrSchema) {
		t.Fatalf("read of another schema = %v, want ErrSchema", err)
	}
	// A field a later version adds is not breaking.
	if _, err := root.Child("external").Put(ctx, "slack/y", []byte(`{"schema":"slack/v1","bot_token":"t","extra":"later"}`), ""); err != nil {
		t.Fatal(err)
	}
	if got, _, err := st.External.SlackApp("y").Get(ctx); err != nil || got.BotToken != "t" {
		t.Fatalf("read with an added field = %+v, %v", got, err)
	}
}

func TestRunnerNamesAreReserved(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)

	app := secretstore.GitHubv1{AppID: "1", InstallationID: "2", PrivateKey: "k"}
	if _, err := st.External.GitHubApp("runner-stable-acme").Put(ctx, app, ""); !errors.Is(err, secretstore.ErrReservedName) {
		t.Fatalf("catalogue App named runner-… = %v, want ErrReservedName", err)
	}
	if _, _, err := st.External.GitHubApp("runner-x").Get(ctx); !errors.Is(err, secretstore.ErrReservedName) {
		t.Fatalf("read = %v, want ErrReservedName", err)
	}
	if _, err := st.External.GitHubRunnerApp("stable", "acme").Put(ctx, app, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Child("external").Get(ctx, "github/runner-stable-acme"); err != nil {
		t.Fatalf("a runner App is not at github/runner-<tier>-<org>: %v", err)
	}
	if err := secretstore.CheckAppName("runner-x"); !errors.Is(err, secretstore.ErrReservedName) {
		t.Fatal(err)
	}
	if err := secretstore.CheckAppName("link"); err != nil {
		t.Fatal(err)
	}
}

// Every domain method has a fixed address under its namespace, pinned here:
// the external ones are a contract, the internal ones keep v3's names.
func TestAddresses(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)

	must := func(_ state.Rev, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Internal.ConsoleSessionKey().Put(ctx, []byte("k"), ""))
	must(st.Internal.PersonToken("4711", "github").Put(ctx, secretstore.PersonToken{AccessToken: "a", RefreshToken: "r"}, ""))
	must(st.Internal.Directory("example.org").Put(ctx, []byte(`{}`), ""))
	must(st.Internal.Directory("a/b").Put(ctx, []byte(`{}`), ""))
	must(st.External.OIDC("rp").Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "s"}, ""))
	must(st.External.SlackApp("alerts").Put(ctx, secretstore.Slackv1{BotToken: "t"}, ""))

	walk := func(prefix string) []string {
		var out []string
		rec := func(p string) {
			names, err := root.Child(p).List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range names {
				out = append(out, p+"/"+n)
			}
		}
		for _, p := range []string{
			prefix + "/credentials/console", prefix + "/credentials/github-link",
			prefix + "/credentials/directory/google", prefix + "/oidc", prefix + "/slack",
		} {
			rec(p)
		}
		sort.Strings(out)
		return out
	}
	want := []string{
		"external/oidc/rp",
		"external/slack/alerts",
		"internal/credentials/console/session-key",
		"internal/credentials/directory/google/example.org",
		"internal/credentials/directory/google/u-612f62",
		"internal/credentials/github-link/4711",
	}
	got := append(walk("internal"), walk("external")...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses:\n got %v\nwant %v", got, want)
	}
}

func TestInternalValuesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, _ := newStores(t)

	exp := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tok := secretstore.PersonToken{AccessToken: "a", AccessExpires: exp, RefreshToken: "r", RefreshExpires: exp.Add(time.Hour)}
	if _, err := st.Internal.PersonToken("1", "github").Put(ctx, tok, ""); err != nil {
		t.Fatal(err)
	}
	if got, _, err := st.Internal.PersonToken("1", "github").Get(ctx); err != nil || got != tok {
		t.Fatalf("person token = %+v, %v", got, err)
	}
	if _, _, err := st.Internal.PersonToken("2", "github").Get(ctx); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("absent = %v, want ErrNotFound", err)
	}
}

// The OIDC document rotates with one Put and Rotating hands the token check
// both secrets inside the grace period.
func TestOIDCRotation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clock := now
	root := memory.New(memory.WithClock(func() time.Time { return clock }))
	st := secretstore.FromStore(root, "")
	v := st.External.OIDC("rp").WithClock(func() time.Time { return clock })

	rev, err := v.Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "one"}, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if _, err := v.Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "two"}, rev); err != nil {
		t.Fatal(err)
	}
	cur, prev, err := v.Rotating(ctx, 24*time.Hour)
	if err != nil || cur.ClientSecret != "two" || prev == nil || prev.ClientSecret != "one" {
		t.Fatalf("Rotating = %+v, %+v, %v", cur, prev, err)
	}
	clock = clock.Add(25 * time.Hour)
	_, prev, err = v.Rotating(ctx, 24*time.Hour)
	if err != nil || prev != nil {
		t.Fatalf("after the grace: previous = %+v, %v", prev, err)
	}
}

func TestCheckLayout(t *testing.T) {
	for _, in := range []string{"", "v4"} {
		if err := secretstore.CheckLayout(in); err != nil {
			t.Errorf("CheckLayout(%q) = %v", in, err)
		}
	}
	for _, in := range []string{"v3", "transition", "v5"} {
		if err := secretstore.CheckLayout(in); err == nil {
			t.Errorf("CheckLayout(%q) accepted", in)
		}
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	var gotPrefix string
	var gotOpts state.Options
	open := func(_ context.Context, prefix string, opts ...state.Option) (state.Store, error) {
		gotPrefix = prefix
		gotOpts = state.ResolveOptions(state.Options{}, opts...)
		return memory.New(), nil
	}
	_, err := secretstore.Open(ctx, &config.Secrets{
		Source: "ssm", Root: "/sluis/example", Region: "eu-west-1", Endpoint: "http://localhost:4566",
		KMSKeyID: "alias/example", Layout: "v4",
	}, open)
	if err != nil {
		t.Fatal(err)
	}
	if gotPrefix != "/sluis/example" || gotOpts.Region != "eu-west-1" || gotOpts.Endpoint != "http://localhost:4566" {
		t.Fatalf("opened %q with %+v", gotPrefix, gotOpts)
	}
	// The default layout is v4.
	if _, err = secretstore.Open(ctx, &config.Secrets{Source: "ssm", Root: "/sluis/example"}, open); err != nil {
		t.Fatalf("default layout: %v", err)
	}

	for name, cfg := range map[string]*config.Secrets{
		"nil":        nil,
		"env":        {Source: "env"},
		"file":       {Source: "file", Root: "/run/secrets"},
		"no root":    {Source: "ssm"},
		"bad root":   {Source: "ssm", Root: "sluis/x"},
		"layout":     {Source: "ssm", Root: "/sluis/x", Layout: "v9"},
		"v3":         {Source: "ssm", Root: "/sluis/x", Layout: "v3"},
		"transition": {Source: "ssm", Root: "/sluis/x", Layout: "transition"},
	} {
		if _, err := secretstore.Open(ctx, cfg, open); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := secretstore.Open(ctx, nil, open); !errors.Is(err, secretstore.ErrNoStore) {
		t.Errorf("nil = %v, want ErrNoStore", err)
	}
}

// The static credentials of an S3-compatible store: golden, matching their
// schema, and reachable only through an internal address.
func TestS3CredentialsDocument(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)
	doc, err := st.Internal.S3Credentials("internal/blobs/example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "example-access-key", SecretAccessKey: "example-secret-key"}, ""); err != nil {
		t.Fatal(err)
	}
	it, err := root.Child("internal").Get(ctx, "blobs/example")
	if err != nil {
		t.Fatal(err)
	}
	if golden := readFile(t, "testdata", "s3-credentials.v1.golden.json"); string(it.Value) != string(golden) {
		t.Fatalf("encoder output changed:\n got %s\nwant %s", it.Value, golden)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err = json.Unmarshal(readFile(t, "..", "..", "schemas", "internal", "s3-credentials.v1.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(it.Value, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range schema.Required {
		if _, ok := m[f].(string); !ok {
			t.Errorf("required field %q is missing or not a string", f)
		}
	}
	for f := range m {
		if _, ok := schema.Properties[f]; !ok {
			t.Errorf("field %q is not in the schema", f)
		}
	}
	got, _, err := doc.Get(ctx)
	if err != nil || got.SecretAccessKey != "example-secret-key" {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "id"}, ""); !errors.Is(err, secretstore.ErrSchema) {
		t.Errorf("a half document: %v, want ErrSchema", err)
	}
	for _, ref := range []string{"external/s3/x", "internal/../x", "internal/a", "x"} {
		if _, err = st.Internal.S3Credentials(ref); !errors.Is(err, secretstore.ErrRef) {
			t.Errorf("%q: %v, want ErrRef", ref, err)
		}
	}
}

// A Cloudflare credential is one schema with two shapes: a token, or R2
// credentials. Both are golden and held to the one schema's properties.
func TestCloudflareDocumentsAreGoldenAndMatchTheirSchema(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(readFile(t, "..", "..", "schemas", "external", "cloudflare.v1.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		golden, preset string
		doc            secretstore.Cloudflarev1
	}{
		{"cloudflare.v1.golden.json", "dns-example", secretstore.Cloudflarev1{Token: "example-cloudflare-token", ExpiresOn: "2026-10-08T12:15:00Z"}},
		{"cloudflare-r2.v1.golden.json", "r2-example", secretstore.Cloudflarev1{
			AccessKeyID: "example-token-id", SecretAccessKey: "example-derived-secret",
			Endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com", ExpiresOn: "2026-10-08T12:15:00Z",
		}},
	} {
		if _, err := st.External.Cloudflare(tc.preset).Put(ctx, tc.doc, ""); err != nil {
			t.Fatalf("%s: %v", tc.preset, err)
		}
		it, err := root.Child("external").Get(ctx, "cloudflare/"+tc.preset)
		if err != nil {
			t.Fatalf("not at external/cloudflare/%s: %v", tc.preset, err)
		}
		if golden := readFile(t, "testdata", tc.golden); string(it.Value) != string(golden) {
			t.Fatalf("encoder output changed:\n got %s\nwant %s\nchanging a field is breaking: move the schema version", it.Value, golden)
		}
		var m map[string]any
		if err = json.Unmarshal(it.Value, &m); err != nil {
			t.Fatal(err)
		}
		for _, f := range schema.Required {
			if _, ok := m[f].(string); !ok {
				t.Errorf("%s: required field %q is missing or not a string", tc.preset, f)
			}
		}
		for f := range m {
			if _, ok := schema.Properties[f]; !ok {
				t.Errorf("%s: field %q is not in the schema", tc.preset, f)
			}
		}
		if got, _, err := st.External.Cloudflare(tc.preset).Get(ctx); err != nil || got.Token != tc.doc.Token || got.SecretAccessKey != tc.doc.SecretAccessKey {
			t.Fatalf("%s: read back = %+v, %v", tc.preset, got, err)
		}
	}
	for name, doc := range map[string]secretstore.Cloudflarev1{
		"no expiry":                 {Token: "t"},
		"an expiry that is no time": {Token: "t", ExpiresOn: "tomorrow"},
		"neither shape":             {ExpiresOn: "2026-10-08T12:15:00Z"},
		"both shapes":               {Token: "t", AccessKeyID: "a", SecretAccessKey: "s", Endpoint: "https://e", ExpiresOn: "2026-10-08T12:15:00Z"},
		"half an R2 shape":          {AccessKeyID: "a", ExpiresOn: "2026-10-08T12:15:00Z"},
	} {
		if _, err := st.External.Cloudflare("bad").Put(ctx, doc, ""); !errors.Is(err, secretstore.ErrSchema) {
			t.Errorf("%s: %v, want ErrSchema", name, err)
		}
	}
}

// The minter credential: golden, matching its schema, internal only.
func TestCloudflareMinterDocument(t *testing.T) {
	ctx := context.Background()
	st, root := newStores(t)
	doc, err := st.Internal.CloudflareMinter("internal/cloudflare/main/minter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = doc.Put(ctx, secretstore.CloudflareMinterv1{Token: "example-minter-token"}, ""); err != nil {
		t.Fatal(err)
	}
	it, err := root.Child("internal").Get(ctx, "cloudflare/main/minter")
	if err != nil {
		t.Fatal(err)
	}
	if golden := readFile(t, "testdata", "cloudflare-minter.v1.golden.json"); string(it.Value) != string(golden) {
		t.Fatalf("encoder output changed:\n got %s\nwant %s", it.Value, golden)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err = json.Unmarshal(readFile(t, "..", "..", "schemas", "internal", "cloudflare-minter.v1.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(it.Value, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range schema.Required {
		if _, ok := m[f].(string); !ok {
			t.Errorf("required field %q is missing or not a string", f)
		}
	}
	for f := range m {
		if _, ok := schema.Properties[f]; !ok {
			t.Errorf("field %q is not in the schema", f)
		}
	}
	if _, err = doc.Put(ctx, secretstore.CloudflareMinterv1{}, ""); !errors.Is(err, secretstore.ErrSchema) {
		t.Errorf("an empty minter: %v, want ErrSchema", err)
	}
	for _, ref := range []string{"external/cloudflare/main", "internal/../x/y", "internal/a", "x"} {
		if _, err = st.Internal.CloudflareMinter(ref); !errors.Is(err, secretstore.ErrRef) {
			t.Errorf("%q: %v, want ErrRef", ref, err)
		}
	}
}
