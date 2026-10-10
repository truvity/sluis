package deploycheck_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
)

// sentinel is a value no output may contain.
const sentinel = "SENTINEL-VALUE-9f2c"

type fake map[string]deploycheck.Entry

func (f fake) Read(_ context.Context, name string) (deploycheck.Entry, bool, error) {
	e, ok := f[name]
	return e, ok, nil
}

type failing struct{}

func (failing) Read(context.Context, string) (deploycheck.Entry, bool, error) {
	return deploycheck.Entry{}, false, errors.New("AccessDeniedException")
}

// goodState is a state secret of the right shape.
func goodState() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// raw is a layout v5 raw value as the store keeps it.
func raw(v string) []byte {
	b, _ := json.Marshal(map[string][]byte{"value": []byte(v)})
	return b
}

var declared = []config.DeclaredSecret{
	{Kind: config.SecretClient, Subject: "console", Name: "clients/console/secret"},
	{Kind: config.SecretRecoveryPassword, Name: "recovery/password"},
	{Kind: config.SecretSignInClientID, Subject: "prod", Name: "providers/google/prod/client-id"},
	{Kind: config.SecretSignInClientSecret, Subject: "prod", Name: "providers/google/prod/client-secret"},
	{Kind: config.SecretStateSecret, Name: "issuer/state-secret"},
	{Kind: config.SecretWorkspaceKey, Subject: "C0example", Name: "directory/C0example/key"},
	{Kind: config.SecretMinter, Subject: "main", Name: "internal/cloudflare/main/minter", Ref: true},
}

func TestTheAddressesOfEachLayout(t *testing.T) {
	v4 := []string{
		"internal/config/clients/console/secret",
		"internal/config/recovery/password",
		"internal/config/providers/google/prod/client-id",
		"internal/config/providers/google/prod/client-secret",
		"internal/config/issuer/state-secret",
		"internal/config/directory/C0example/key",
		"internal/cloudflare/main/minter",
	}
	v5 := []string{
		"internal/oidc/clients/console",
		"internal/oidc/recovery-password",
		"internal/oidc/signin/prod/client-id",
		"internal/oidc/signin/prod/client-secret",
		"internal/oidc/state-secret",
		"internal/google/workspaces/C0example/key",
		"internal/cloudflare/main/minter",
	}
	for i, d := range declared {
		if got := deploycheck.Address(d, config.SecretsLayoutV4); got != v4[i] {
			t.Errorf("v4 %s: %q, want %q", d.Kind, got, v4[i])
		}
		if got := deploycheck.Address(d, config.SecretsLayoutV5); got != v5[i] {
			t.Errorf("v5 %s: %q, want %q", d.Kind, got, v5[i])
		}
	}
	// A v5 workspace key is by the workspace's id: a declaration without one
	// cannot be placed.
	if got := deploycheck.Address(config.DeclaredSecret{Kind: config.SecretWorkspaceKey, Name: "k"}, config.SecretsLayoutV5); got != "" {
		t.Errorf("a workspace with no id has the address %q", got)
	}
}

func store(layout string, state string) fake {
	f := fake{}
	for i, d := range declared {
		v := sentinel
		if d.Kind == config.SecretStateSecret {
			v = state
		}
		var body []byte
		switch {
		case d.Ref:
			body = []byte(`{"token":"` + sentinel + `"}`)
		case layout == config.SecretsLayoutV5:
			body = raw(v)
		default:
			body = []byte(v)
		}
		f["/sluis/prod/"+deploycheck.Address(d, layout)] = deploycheck.Entry{Value: body, Version: int64(i + 1)}
	}
	return f
}

func TestEveryDeclaredNamePresentPasses(t *testing.T) {
	for _, layout := range []string{config.SecretsLayoutV4, config.SecretsLayoutV5} {
		rep := deploycheck.Run(t.Context(), store(layout, goodState()), "/sluis/prod", layout, declared)
		if !rep.OK || rep.Err() != nil {
			t.Fatalf("%s: %+v", layout, rep.Failed())
		}
		if len(rep.Results) != len(declared) {
			t.Fatalf("%s: %d results", layout, len(rep.Results))
		}
	}
}

func TestAMissingNameFails(t *testing.T) {
	for _, layout := range []string{config.SecretsLayoutV4, config.SecretsLayoutV5} {
		st := store(layout, goodState())
		gone := "/sluis/prod/" + deploycheck.Address(declared[3], layout)
		delete(st, gone)
		rep := deploycheck.Run(t.Context(), st, "/sluis/prod", layout, declared)
		if rep.OK {
			t.Fatalf("%s: a missing name passed", layout)
		}
		failed := rep.Failed()
		if len(failed) != 1 || failed[0].Address != gone || failed[0].Problem != "missing" {
			t.Fatalf("%s: %+v", layout, failed)
		}
		if err := rep.Err(); err == nil || !strings.Contains(err.Error(), gone) {
			t.Errorf("%s: the error does not name %s: %v", layout, gone, err)
		}
	}
}

func TestAnEmptyValueFails(t *testing.T) {
	for _, layout := range []string{config.SecretsLayoutV4, config.SecretsLayoutV5} {
		for _, empty := range []string{"", " \n"} {
			st := store(layout, goodState())
			addr := "/sluis/prod/" + deploycheck.Address(declared[1], layout)
			body := []byte(empty)
			if layout == config.SecretsLayoutV5 {
				body = raw(empty)
			}
			st[addr] = deploycheck.Entry{Value: body, Version: 4}
			rep := deploycheck.Run(t.Context(), st, "/sluis/prod", layout, declared)
			if failed := rep.Failed(); len(failed) != 1 || failed[0].Address != addr || failed[0].Problem != "empty" {
				t.Fatalf("%s %q: %+v", layout, empty, failed)
			}
		}
	}
	// A credential document with no field is empty too.
	st := store(config.SecretsLayoutV4, goodState())
	st["/sluis/prod/internal/cloudflare/main/minter"] = deploycheck.Entry{Value: []byte(`{}`), Version: 1}
	if rep := deploycheck.Run(t.Context(), st, "/sluis/prod", config.SecretsLayoutV4, declared); rep.OK {
		t.Error("an empty minter document passed")
	}
}

func TestTheStateSecretIsHeldToItsFormat(t *testing.T) {
	for name, bad := range map[string]string{
		"short":       base64.StdEncoding.EncodeToString([]byte("too short")),
		"placeholder": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 40)),
		"not encoded": "this is not hex or base64 " + sentinel,
	} {
		for _, layout := range []string{config.SecretsLayoutV4, config.SecretsLayoutV5} {
			rep := deploycheck.Run(t.Context(), store(layout, bad), "/sluis/prod", layout, declared)
			failed := rep.Failed()
			if len(failed) != 1 || failed[0].Kind != config.SecretStateSecret {
				t.Fatalf("%s %s: %+v", name, layout, failed)
			}
		}
	}
	// Hex is as good as base64.
	hex := strings.Repeat("0123456789abcdef", 4)
	if rep := deploycheck.Run(t.Context(), store(config.SecretsLayoutV4, hex), "/sluis/prod", config.SecretsLayoutV4, declared); !rep.OK {
		t.Errorf("hex refused: %+v", rep.Failed())
	}
}

func TestAnUnreadableNameFailsAndNamesNoValue(t *testing.T) {
	rep := deploycheck.Run(t.Context(), failing{}, "/sluis/prod", config.SecretsLayoutV4, declared)
	if rep.OK || len(rep.Failed()) != len(declared) {
		t.Fatalf("%+v", rep.Failed())
	}
}

// Nothing a report prints, encodes or returns as an error holds a value: only
// names, versions and fixed reasons.
func TestTheOutputHoldsNamesAndVersionsOnly(t *testing.T) {
	for _, layout := range []string{config.SecretsLayoutV4, config.SecretsLayoutV5} {
		for _, state := range []string{goodState(), "not a state secret " + sentinel} {
			st := store(layout, state)
			delete(st, "/sluis/prod/"+deploycheck.Address(declared[0], layout))
			rep := deploycheck.Run(t.Context(), st, "/sluis/prod", layout, declared)

			var text bytes.Buffer
			rep.Write(&text)
			js, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			all := text.String() + string(js) + rep.Describe()
			if e := rep.Err(); e != nil {
				all += e.Error()
			}
			for _, leaked := range []string{sentinel, goodState(), base64.StdEncoding.EncodeToString([]byte(sentinel))} {
				if strings.Contains(all, leaked) {
					t.Fatalf("%s: the output holds a value:\n%s", layout, all)
				}
			}
			for _, want := range []string{"/sluis/prod/", "version 2", "missing"} {
				if !strings.Contains(all, want) {
					t.Errorf("%s: the output lacks %q:\n%s", layout, want, text.String())
				}
			}
		}
	}
}

func TestOnlySSMLayoutsAreChecked(t *testing.T) {
	for _, s := range []*config.Secrets{nil, {Source: "env"}, {Source: "ssm", Layout: "v3"}} {
		if _, err := deploycheck.Layout(s); err == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
	if l, err := deploycheck.Layout(&config.Secrets{Source: "ssm"}); err != nil || l != config.SecretsLayoutV4 {
		t.Errorf("the default is v4: %q %v", l, err)
	}
}
