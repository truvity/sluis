package policy_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

// outlineBase is a valid policy with one Outline workspace of three
// collections; each refusal below starts from it and breaks one thing.
const outlineBase = `
version: 1
groups:
  all:wiki:admin: { members: [wiki-admins@acme.example] }
  all:wiki:acme: { matchers: [{ email_domain: acme.example }] }
  all:wiki:globex: { matchers: [{ email_domain: globex.example }] }
  acme:wiki:manager: { members: [wiki-managers@acme.example] }
clients:
  wiki: { kind: confidential, secret: wiki-oidc, redirects: [https://wiki.example.com/auth/oidc.callback], requires: [all:wiki:acme, all:wiki:globex] }
outline:
  workspaces:
    wiki:
      client: wiki
      roles: { admin: [all:wiki:admin] }
      ignore: [Key.Owner@acme.example]
      collections:
        General:
          mode: strict
          read_write: [all:wiki:acme, all:wiki:globex]
          admin: [all:wiki:admin]
        Acme:
          mode: strict
          directory: acme.example
          read: [all:wiki:acme]
          read_write: [all:wiki:acme]
          admin: [acme:wiki:manager]
        Globex:
          read_write: [all:wiki:globex]
          adopt: 0b6f4c2e-1a2b-4c3d-8e9f-0123456789ab
`

func TestOutlineRoundTrips(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte(outlineBase))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	if !set.OutlineWorkspaceDeclared("wiki") || set.OutlineWorkspaceDeclared("other") {
		t.Error("OutlineWorkspaceDeclared does not follow the table")
	}
	if got := set.OutlineWorkspaceKeys(); !slices.Equal(got, []string{"wiki"}) {
		t.Errorf("OutlineWorkspaceKeys = %v", got)
	}
	ws := declared.Outline.Workspaces["wiki"]
	if !ws.Collections["General"].Strict() || ws.Collections["Globex"].Strict() {
		t.Errorf("modes read as %+v", ws.Collections)
	}
	want := []string{"acme:wiki:manager", "all:wiki:acme", "all:wiki:admin", "all:wiki:globex"}
	if got := ws.Groups(); !slices.Equal(got, want) {
		t.Errorf("Groups = %v, want %v", got, want)
	}
	if !ws.Ignored("key.owner@ACME.example") || ws.Ignored("someone@acme.example") {
		t.Error("Ignored must match the list case-insensitively and nothing else")
	}
	again, err := policy.Parse([]byte(outlineBase))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := declared.Digest()
	if second, _ := again.Digest(); first != second || !reflect.DeepEqual(declared, again) {
		t.Errorf("round trip changed the policy: %s vs %s", first, second)
	}
	// Every bound group is consumed: none is reported as declared and unused.
	if unused := declared.Unconsumed(); len(unused) != 0 {
		t.Errorf("Unconsumed = %v, want none: the Outline bindings consume their groups", unused)
	}
}

// A group listed at two levels takes the higher one.
func TestOutlineGrantsTakeTheHigherLevel(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte(outlineBase))
	if err != nil {
		t.Fatal(err)
	}
	grants := declared.Outline.Workspaces["wiki"].Collections["Acme"].Grants()
	want := map[string]string{"all:wiki:acme": policy.OutlineReadWrite, "acme:wiki:manager": policy.OutlineAdmin}
	if !reflect.DeepEqual(grants, want) {
		t.Errorf("Grants = %v, want %v", grants, want)
	}
}

func TestOutlineRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"a group nothing declares", "admin: [acme:wiki:manager]", "admin: [acme:wiki:nobody]", "not a declared group"},
		{"an admin role group nothing declares", "roles: { admin: [all:wiki:admin] }", "roles: { admin: [all:wiki:none] }", "not a declared group"},
		{"a collection that names no group", "read_write: [all:wiki:globex]\n          adopt:", "adopt:", "names no group at any level"},
		{"a malformed workspace key", "    wiki:\n", "    Wiki_Space:\n", "not a usable workspace key"},
		{"a malformed collection name", "        Globex:\n", "        \" Globex\":\n", "not a collection name"},
		{"a name too long", "        Globex:\n", "        " + strings.Repeat("g", 101) + ":\n", "not a collection name"},
		{"a name declared twice in another case", "        Globex:\n", "        GENERAL:\n", "declared twice"},
		{"an unknown mode", "mode: strict\n          directory", "mode: exact\n          directory", "neither extend nor strict"},
		{"an adopt that is not an id", "adopt: 0b6f4c2e-1a2b-4c3d-8e9f-0123456789ab", "adopt: C0123ABCD", "not an Outline collection id"},
		{"an ignore that is not an address", "ignore: [Key.Owner@acme.example]", "ignore: [key-owner]", "not an address"},
		{"an ignore listed twice", "ignore: [Key.Owner@acme.example]", "ignore: [Key.Owner@acme.example, key.owner@acme.example]", "twice"},
		{"a malformed directory", "directory: acme.example", "directory: 'acme example'", "directory"},
		{"a client nothing declares", "client: wiki", "client: nobody", "not a declared client"},
		{"a url on the workspace", "      roles:", "      url: https://wiki.example.com\n      roles:", "lives on the console"},
		{"a token on the workspace", "      roles:", "      token: secret\n      roles:", "lives on the console"},
		{"an owner on the workspace", "      roles:", "      owner: someone\n      roles:", "lives on the console"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text := strings.Replace(outlineBase, tc.from, tc.to, 1)
			if text == outlineBase {
				t.Fatalf("the fixture does not contain %q", tc.from)
			}
			declared, err := policy.Parse([]byte(text))
			if err == nil {
				_, err = policy.NewSet(declared)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// Two adopts of one collection id are refused, naming both collections.
func TestOutlineAdoptsOneIDOnce(t *testing.T) {
	t.Parallel()
	text := strings.Replace(outlineBase, "          directory: acme.example\n",
		"          directory: acme.example\n          adopt: 0b6f4c2e-1a2b-4c3d-8e9f-0123456789ab\n", 1)
	declared, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.NewSet(declared); err == nil || !strings.Contains(err.Error(), "adopts") {
		t.Errorf("err = %v, want two adopts of one id refused", err)
	}
}
