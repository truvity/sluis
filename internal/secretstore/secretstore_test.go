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
	return secretstore.FromStore(root, secretstore.LayoutV4, ""), root
}

func keys(t *testing.T, s state.Store, prefix string) []string {
	t.Helper()
	got, err := s.Child(prefix).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
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
				PrivateKey: "-----BEGIN EXAMPLE KEY-----\nexample\n-----END EXAMPLE KEY-----\n",
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
	must(st.Internal.RecoveryPassword().Put(ctx, "pw", ""))
	must(st.Internal.StateSecret().Put(ctx, []byte{1, 2, 3}, ""))
	must(st.Internal.OAuthProvider("default").Put(ctx, secretstore.OAuthClient{ClientID: "i", ClientSecret: "s"}, ""))
	must(st.Internal.ConsoleSessionKey().Put(ctx, []byte("k"), ""))
	must(st.Internal.PersonToken("4711", "github").Put(ctx, secretstore.PersonToken{AccessToken: "a", RefreshToken: "r"}, ""))
	must(st.Internal.Directory("example.org").Put(ctx, []byte(`{}`), ""))
	must(st.Internal.Directory("a/b").Put(ctx, []byte(`{}`), ""))
	must(st.External.OIDC("rp").Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "s"}, ""))
	must(st.External.SlackApp("alerts").Put(ctx, secretstore.Slackv1{BotToken: "t"}, ""))

	walk := func(prefix string) []string {
		var out []string
		var rec func(p string)
		rec = func(p string) {
			names, err := root.Child(p).List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range names {
				out = append(out, p+"/"+n)
			}
		}
		for _, p := range []string{
			prefix + "/config/recovery", prefix + "/config/issuer", prefix + "/config/providers/google",
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
		"internal/config/issuer/state-secret",
		"internal/config/providers/google/default",
		"internal/config/recovery/password",
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

	if _, err := st.Internal.RecoveryPassword().Put(ctx, "p w", ""); err != nil {
		t.Fatal(err)
	}
	if got, _, err := st.Internal.RecoveryPassword().Get(ctx); err != nil || got != "p w" {
		t.Fatalf("recovery password = %q, %v", got, err)
	}
	if _, err := st.Internal.StateSecret().Put(ctx, []byte{0, 255, 7}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _, err := st.Internal.StateSecret().Get(ctx); err != nil || !reflect.DeepEqual(got, []byte{0, 255, 7}) {
		t.Fatalf("state secret = %v, %v", got, err)
	}
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
	st := secretstore.FromStore(root, secretstore.LayoutV4, "")
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

func TestParseLayout(t *testing.T) {
	for in, want := range map[string]secretstore.Layout{
		"": secretstore.LayoutV3, "v3": secretstore.LayoutV3,
		"transition": secretstore.LayoutTransition, "v4": secretstore.LayoutV4,
	} {
		if got, err := secretstore.ParseLayout(in); err != nil || got != want {
			t.Errorf("ParseLayout(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := secretstore.ParseLayout("v5"); err == nil {
		t.Fatal("v5 accepted")
	}
	tr := secretstore.LayoutTransition
	if !tr.ReadsV4() || !tr.WritesV4() || !tr.WritesV3() {
		t.Error("transition reads v4 and writes both")
	}
	if secretstore.LayoutV3.ReadsV4() || secretstore.LayoutV4.WritesV3() || !secretstore.LayoutV4.WritesV4() {
		t.Error("v3 and v4 each write their own")
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
	st, err := secretstore.Open(ctx, &config.Secrets{
		Source: "ssm", Root: "/sluis/example", Region: "eu-west-1", Endpoint: "http://localhost:4566",
		KMSKeyID: "alias/example", Layout: "transition",
	}, open)
	if err != nil {
		t.Fatal(err)
	}
	if gotPrefix != "/sluis/example" || gotOpts.Region != "eu-west-1" || gotOpts.Endpoint != "http://localhost:4566" {
		t.Fatalf("opened %q with %+v", gotPrefix, gotOpts)
	}
	if st.Layout != secretstore.LayoutTransition {
		t.Fatalf("layout = %q", st.Layout)
	}
	// The default layout is v3.
	st, err = secretstore.Open(ctx, &config.Secrets{Source: "ssm", Root: "/sluis/example"}, open)
	if err != nil || st.Layout != secretstore.LayoutV3 {
		t.Fatalf("default layout = %v, %v", st, err)
	}

	for name, cfg := range map[string]*config.Secrets{
		"nil":      nil,
		"env":      {Source: "env"},
		"file":     {Source: "file", Root: "/run/secrets"},
		"no root":  {Source: "ssm"},
		"bad root": {Source: "ssm", Root: "sluis/x"},
		"layout":   {Source: "ssm", Root: "/sluis/x", Layout: "v9"},
	} {
		if _, err := secretstore.Open(ctx, cfg, open); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := secretstore.Open(ctx, nil, open); !errors.Is(err, secretstore.ErrNoStore) {
		t.Errorf("nil = %v, want ErrNoStore", err)
	}
}
