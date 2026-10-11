package sluispulumi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roleGolden is the policy document of every role the library renders, as the
// module pattern makes it (the issuer's is the module part of its function
// policy). A change to a grant is a change to a file here, which a reviewer reads.
// SLUIS_UPDATE_GOLDEN=1 rewrites the files.
func TestEveryRolesPolicyIsItsGolden(t *testing.T) {
	env := mxEnv(moduleRoles())
	arch := ArchiveGrant{BucketArn: "arn:aws:s3:::acme-archive", Prefix: "pre/", Instance: mxInst,
		KeyArn: "arn:aws:kms:r1:111122223333:key/archive", Versioned: true}
	docs := map[string]any{}
	for _, r := range append([]string{RoleIssuer}, ModuleFunctions()...) {
		st, err := ModuleRoleStatements(env, moduleRole(r))
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		docs[r] = st
	}
	bk, err := BackupRoleStatements(env, arch)
	if err != nil {
		t.Fatal(err)
	}
	docs[RoleBackup] = bk
	rs, err := RestoreRoleStatements(env, arch, []string{mxFn("restore") + ":live-console"})
	if err != nil {
		t.Fatal(err)
	}
	docs[RoleRestore] = rs

	if len(docs) != 7 {
		t.Fatalf("%d roles, want the issuer, the four module functions, the backup and the restore", len(docs))
	}
	for name, st := range docs {
		raw, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": st})
		if err != nil {
			t.Fatal(err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		path := filepath.Join("testdata", "role-"+name+".policy.json")
		if os.Getenv("SLUIS_UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (SLUIS_UPDATE_GOLDEN=1 writes it)", name, err)
		}
		if !bytes.Equal(pretty.Bytes(), want) {
			t.Errorf("the policy of the %s role is not its golden %s:\n%s", name, path, pretty.String())
		}
	}
}

// The issuer's golden holds none of what v1.76 E-FIN removed: no github or slack
// table, no provider parameter or parameter-key statement, no minter reference,
// no unqualified or issuer-class invoke of slack, and a Google table read that
// is by key only.
func TestTheIssuerGoldenHoldsNoProviderAccessBeyondTheGoogleSnapshot(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "role-issuer.policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden := string(raw)
	for _, not := range []string{
		"SluisCrossGithub", "SluisCrossSlack", "SluisCrossCloudflareMinter", "SluisCrossGoogleParameters", "SluisParameterKeyCross",
		"table/sluis-i1-github", "table/sluis-i1-slack", "table/sluis-i1-cloudflare",
		"parameter/sluis/i1/internal/github", "parameter/sluis/i1/internal/slack", "parameter/sluis/i1/internal/google",
		"parameter/sluis/i1/internal/cloudflare", "parameter/sluis/i1/external/github", "parameter/sluis/i1/external/google",
		"sluis-i1-slack:live-issuer", "/github/", "/slack/",
	} {
		if strings.Contains(golden, not) {
			t.Errorf("the issuer's golden holds %q", not)
		}
	}
	for _, want := range []string{"SluisCrossGoogleTable", "SluisCrossGoogleBlobs", "sluis-i1-cloudflare:live-console"} {
		if !strings.Contains(golden, want) {
			t.Errorf("the issuer's golden lacks %q", want)
		}
	}
}
