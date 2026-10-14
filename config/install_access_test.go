package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
	"github.com/truvity/sluis/policy"
)

const (
	groupOperator   = "all:roster:operator"
	groupAuditor    = "east:k8s:auditor"
	groupViewer     = "all:roster:viewer"
	groupController = "all:sluis:controller"
	groupProbe      = "west:probe:exchange"
)

func loadAccess(t *testing.T) policy.Access {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "install", "access.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc policy.AccessDocument
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc.Access
}

// accessYAML writes the section as the installation document does.
func accessYAML(t *testing.T, a config.Access) string {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func kubernetesBootstrap() config.Bootstrap {
	return config.Bootstrap{
		Recovery: &config.BootstrapRecovery{
			ServiceAccount: config.BootstrapServiceAccount{Namespace: "sluis", Name: "sluis-recovery"},
			Groups:         []string{groupOperator, groupAuditor},
		},
		Viewers:     &config.BootstrapViewers{Group: groupViewer, EmailDomain: "example.com"},
		Controllers: &config.BootstrapControllers{ServiceAccounts: []config.BootstrapServiceAccount{{Cluster: "east", Namespace: "sluis", Name: "sluis"}}},
		Probe: &config.BootstrapProbe{
			Group: groupProbe, Client: "exchange-probe",
			ServiceAccount: config.BootstrapServiceAccount{Cluster: "west", Namespace: "probe", Name: "exchange-probe"},
		},
		GenerateSecrets: true,
	}
}

func lambdaBootstrap() config.Bootstrap {
	return config.Bootstrap{
		Viewers: &config.BootstrapViewers{
			Group: groupViewer, EmailDomain: "example.com",
			RoleARNs: []string{"arn:aws:iam::111122223333:role/sluis-issuer"},
		},
		Controllers: &config.BootstrapControllers{
			Group: groupController,
			RoleARNs: []string{
				"arn:aws:iam::111122223333:role/sluis-github",
				"arn:aws:iam::111122223333:role/service/sluis-slack",
			},
		},
		Probe: &config.BootstrapProbe{
			Group: groupProbe, Client: "exchange-probe",
			ServiceAccount: config.BootstrapServiceAccount{Cluster: "west", Namespace: "probe", Name: "exchange-probe"},
		},
		GenerateSecrets: true,
	}
}

// The golden files are the shape an estate's own renderer writes for the same
// inputs: snake_case keys, one table per concern, the bootstrap matchers after
// the derived ones, generated secrets and the probe client.
func TestFromAccessParity(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    config.Bootstrap
	}{
		{"kubernetes", kubernetesBootstrap()},
		{"lambda", lambdaBootstrap()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.b
			b.Claims = map[string]policy.Fragment{}
			got, err := config.FromAccess(loadAccess(t), b)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "install", tc.name+".golden.yaml")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(golden, []byte(accessYAML(t, got)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if g := accessYAML(t, got); g != string(want) {
				t.Fatalf("access section differs from %s:\n%s", golden, g)
			}
			// The section is one the installation document reads back.
			var back config.Access
			raw, _ := json.Marshal(got)
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if err := back.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestFromAccessEmptyBootstrapIsTheAccessDocument(t *testing.T) {
	access := loadAccess(t)
	got, err := config.FromAccess(access, config.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := policy.AccessDocument{Version: 1, Access: access}.Policy()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := yaml.Marshal(got.Policy)
	b, _ := yaml.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Fatalf("an empty bootstrap changed the policy:\n%s\n%s", a, b)
	}
}

func TestFromAccessClaimsAreCarried(t *testing.T) {
	var claims map[string]policy.Fragment
	if err := yaml.Unmarshal([]byte("all:roster:viewer: {role: viewer}\n"), &claims); err != nil || len(claims) != 1 {
		t.Fatalf("claims: %v %v", claims, err)
	}
	got, err := config.FromAccess(loadAccess(t), config.Bootstrap{Claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Claims) != 1 {
		t.Fatalf("claims lost: %v", got.Claims)
	}
}

func TestFromAccessRefuses(t *testing.T) {
	sa := config.BootstrapServiceAccount{Namespace: "sluis", Name: "sluis"}
	for _, tc := range []struct {
		name string
		edit func(b *config.Bootstrap)
		want string
	}{
		{"recovery without namespace", func(b *config.Bootstrap) {
			b.Recovery = &config.BootstrapRecovery{ServiceAccount: config.BootstrapServiceAccount{Name: "x"}, Groups: []string{"g"}}
		}, "namespace and a name"},
		{"recovery namespace not a name", func(b *config.Bootstrap) {
			b.Recovery = &config.BootstrapRecovery{ServiceAccount: config.BootstrapServiceAccount{Namespace: "Not_A_Name", Name: "x"}, Groups: []string{"g"}}
		}, "not a Kubernetes namespace"},
		{"recovery without groups", func(b *config.Bootstrap) { b.Recovery = &config.BootstrapRecovery{ServiceAccount: sa} }, "no groups"},
		{"recovery group twice", func(b *config.Bootstrap) {
			b.Recovery = &config.BootstrapRecovery{ServiceAccount: sa, Groups: []string{groupOperator, groupOperator}}
		}, "listed twice"},
		{"empty group name", func(b *config.Bootstrap) { b.Viewers = &config.BootstrapViewers{} }, "has no name"},
		{"group with a space", func(b *config.Bootstrap) { b.Viewers = &config.BootstrapViewers{Group: "a b"} }, "white space"},
		{"group with an empty segment", func(b *config.Bootstrap) { b.Viewers = &config.BootstrapViewers{Group: "a::b"} }, "empty segment"},
		{"bad email domain", func(b *config.Bootstrap) {
			b.Viewers = &config.BootstrapViewers{Group: "v", EmailDomain: "@example.com"}
		}, "not a domain"},
		{"bad role ARN", func(b *config.Bootstrap) {
			b.Viewers = &config.BootstrapViewers{Group: "v", RoleARNs: []string{"arn:aws:iam::111122223333:role"}}
		}, "not an IAM role ARN"},
		{"role listed twice", func(b *config.Bootstrap) {
			arn := "arn:aws:iam::111122223333:role/x"
			b.Viewers = &config.BootstrapViewers{Group: "v", RoleARNs: []string{arn, arn}}
		}, "listed twice"},
		{"service account listed twice", func(b *config.Bootstrap) {
			b.Controllers = &config.BootstrapControllers{ServiceAccounts: []config.BootstrapServiceAccount{sa, sa}}
		}, "listed twice"},
		{"controller group of the access document", func(b *config.Bootstrap) {
			b.Controllers = &config.BootstrapControllers{Group: groupAuditor, ServiceAccounts: []config.BootstrapServiceAccount{sa}}
		}, "also a group of the access document"},
		{"controller group without workloads", func(b *config.Bootstrap) { b.Controllers = &config.BootstrapControllers{Group: "c"} }, "no workload"},
		{"probe group of the access document", func(b *config.Bootstrap) {
			b.Probe = &config.BootstrapProbe{Group: groupViewer, Client: "p", ServiceAccount: config.BootstrapServiceAccount{Cluster: "c", Namespace: "n", Name: "p"}}
		}, "also a group of the access document"},
		{"probe without cluster", func(b *config.Bootstrap) {
			b.Probe = &config.BootstrapProbe{Group: "p", Client: "p", ServiceAccount: sa}
		}, "cluster is required"},
		{"probe client of the access document", func(b *config.Bootstrap) {
			b.Probe = &config.BootstrapProbe{Group: "p", Client: "console", ServiceAccount: config.BootstrapServiceAccount{Cluster: "c", Namespace: "n", Name: "p"}}
		}, "also a client of the access document"},
		{"probe and controllers share a group", func(b *config.Bootstrap) {
			b.Controllers = &config.BootstrapControllers{Group: "same", ServiceAccounts: []config.BootstrapServiceAccount{sa}}
			b.Probe = &config.BootstrapProbe{Group: "same", Client: "p", ServiceAccount: config.BootstrapServiceAccount{Cluster: "c", Namespace: "n", Name: "p"}}
		}, "is also the controllers group"},
		{"probe group is the viewers group", func(b *config.Bootstrap) {
			b.Viewers = &config.BootstrapViewers{Group: "same"}
			b.Probe = &config.BootstrapProbe{Group: "same", Client: "p", ServiceAccount: config.BootstrapServiceAccount{Cluster: "c", Namespace: "n", Name: "p"}}
		}, "viewers or of recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b config.Bootstrap
			tc.edit(&b)
			_, err := config.FromAccess(loadAccess(t), b)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestFromAccessAdmitsAControllerOnceAsViewer(t *testing.T) {
	arn := "arn:aws:iam::111122223333:role/sluis-github"
	got, err := config.FromAccess(policy.Access{}, config.Bootstrap{
		Viewers:     &config.BootstrapViewers{Group: "v", RoleARNs: []string{arn}},
		Controllers: &config.BootstrapControllers{Group: "c", RoleARNs: []string{arn}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Groups["v"].Matchers); n != 1 {
		t.Fatalf("viewers hold %d matchers, want the role once", n)
	}
	if n := len(got.Groups["c"].Matchers); n != 1 {
		t.Fatalf("controllers hold %d matchers, want the role", n)
	}
}
