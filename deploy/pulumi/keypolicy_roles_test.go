package sluispulumi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

var kpEstateAdmin = arnp + "iam::" + account + ":role/aws-reserved/sso.example/*Admin_*"

// kpShapes are two installations: the plain one, and one that keeps the issuer's
// old role name during the switch, with admins, breakglass and readers.
func kpShapes() map[string]struct {
	args func(*arp.LambdaArgs)
	opts arp.KeyPolicyOptions
} {
	return map[string]struct {
		args func(*arp.LambdaArgs)
		opts arp.KeyPolicyOptions
	}{
		"plain": {args: func(a *arp.LambdaArgs) {}, opts: arp.KeyPolicyOptions{
			ExternalReaders: []string{arnp + "iam::" + account + ":role/cluster-a-reader"},
			Admins:          []string{kpEstateAdmin},
		}},
		"switching": {
			args: func(a *arp.LambdaArgs) {
				a.TransitionRoles = []arp.TransitionRole{{Role: arp.RoleIssuer, OldName: "sluis-old"}}
			},
			opts: arp.KeyPolicyOptions{
				ExternalReaders: []string{
					arnp + "iam::" + account + ":role/cluster-a-reader", arnp + "iam::" + account + ":role/cluster-b-reader",
				},
				Admins:           []string{kpEstateAdmin},
				Breakglass:       []string{arnp + "iam::" + account + ":role/aws-reserved/sso.example/*Glass_*"},
				DenyOtherActions: true,
			},
		},
	}
}

func kpLambdaArgs(t *testing.T, mutate func(*arp.LambdaArgs)) *arp.LambdaArgs {
	t.Helper()
	if mutate == nil {
		mutate = func(*arp.LambdaArgs) {}
	}
	return v5Estate(mutate).args(t)
}

func TestLibraryKeyPolicyIsItsGoldenAndFitsKMS(t *testing.T) {
	for name, sh := range kpShapes() {
		st, err := kpLambdaArgs(t, sh.args).KeyPolicyStatements(sh.opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		doc, err := arp.KeyPolicyDocument("sluis", st)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s: key policy statements %d bytes of 32768", name, len(doc))
		if len(doc) > 32768/2 {
			t.Errorf("%s: %d bytes leave the estate less than half of KMS's limit", name, len(doc))
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, []byte(doc), "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		path := filepath.Join("testdata", "keypolicy-"+name+".json")
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
			t.Errorf("%s: the key policy is not its golden %s:\n%s", name, path, pretty.String())
		}
	}
}

func principals(st map[string]any) []string {
	c, _ := st["Condition"].(map[string]any)
	ae, _ := c["ArnEquals"].(map[string]any)
	switch v := ae["aws:PrincipalArn"].(type) {
	case string:
		return []string{v}
	case []string:
		return v
	}
	return nil
}

func TestNoRoleButOIDCReachesTheSignContext(t *testing.T) {
	for name, sh := range kpShapes() {
		a := kpLambdaArgs(t, sh.args)
		st, err := a.KeyPolicyStatements(sh.opts)
		if err != nil {
			t.Fatal(err)
		}
		roles, _ := a.Roles()
		allowed := map[string]bool{}
		for _, r := range roles {
			if slices.Contains(r.Role.Hosts, arp.ModuleOIDC) {
				allowed[r.Arn] = true
				for _, x := range r.AlsoArns {
					allowed[x] = true
				}
			}
		}
		if len(allowed) == 0 {
			t.Fatalf("%s: no role hosts oidc", name)
		}
		signs := 0
		for _, s := range st {
			raw, _ := json.Marshal(s)
			if s["Effect"] != "Allow" || !strings.Contains(string(raw), `"kms:EncryptionContext:purpose":"sign"`) {
				continue
			}
			signs++
			for _, p := range principals(s) {
				if !allowed[p] {
					t.Errorf("%s: %v lets %s into the sign context", name, s["Sid"], p)
				}
			}
		}
		if signs != 1 {
			t.Errorf("%s: %d sign grants, want 1", name, signs)
		}
		// A non-oidc role encrypts only through SSM (its parameters): a direct
		// Encrypt or GenerateDataKey, the way signing calls the key, is oidc's.
		for _, s := range st {
			raw, _ := json.Marshal(s["Action"])
			full, _ := json.Marshal(s)
			if s["Effect"] != "Allow" || !strings.Contains(string(raw), "kms:Encrypt") && !strings.Contains(string(raw), "GenerateDataKey") {
				continue
			}
			for _, p := range principals(s) {
				if !allowed[p] && !strings.Contains(string(full), `"kms:ViaService":"ssm.`) {
					t.Errorf("%s: %v lets non-oidc %s encrypt", name, s["Sid"], p)
				}
			}
		}
	}
}

func TestTheMintOnlyRoleDecryptsGitHubParametersAlone(t *testing.T) {
	a := kpLambdaArgs(t, nil)
	st, err := a.KeyPolicyStatements(arp.KeyPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mint := arnp + "iam::" + account + ":role/sluis-staging-github-mint"
	var n int
	for _, s := range st {
		if !slices.Contains(principals(s), mint) {
			continue
		}
		n++
		raw, _ := json.Marshal(s)
		for _, other := range []string{"slack", "google", "cloudflare", "oidc", "backup"} {
			if strings.Contains(string(raw), "/"+other+"/") {
				t.Errorf("%v names %s: %s", s["Sid"], other, raw)
			}
		}
		if !strings.Contains(string(raw), "/internal/github/") || !strings.Contains(string(raw), "/external/github/") {
			t.Errorf("%v does not name the github parameters: %s", s["Sid"], raw)
		}
		if fmt.Sprint(s["Action"]) != "[kms:Decrypt kms:DescribeKey]" {
			t.Errorf("%v actions %v", s["Sid"], s["Action"])
		}
	}
	if n != 1 {
		t.Errorf("%d statements name the mint role, want 1", n)
	}
}

func TestSendersRolesAndViewersFollowTheNames(t *testing.T) {
	a := kpLambdaArgs(t, func(a *arp.LambdaArgs) {
		a.TransitionRoles = []arp.TransitionRole{{Role: arp.RoleIssuer, OldName: "sluis-old"}}
	})
	senders, err := a.Senders()
	if err != nil {
		t.Fatal(err)
	}
	roleArn := func(n string) string { return arnp + "iam::" + account + ":role/" + n }
	for _, n := range []string{"sluis-old", "sluis-staging-github-mint", "sluis-staging-backup", "sluis-staging-restore"} {
		if !slices.Contains(senders, roleArn(n)) {
			t.Errorf("senders lack %s: %v", n, senders)
		}
	}
	if !slices.IsSorted(senders) {
		t.Errorf("senders unsorted: %v", senders)
	}
	roles, err := a.Roles()
	if err != nil || len(roles) != 8 {
		t.Fatalf("roles: %d %v", len(roles), err)
	}
	viewers, err := a.ViewerRoles()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(viewers, roleArn("sluis-staging-github-mint")) {
		t.Errorf("the mint role reads github without hosting it and is a viewer: %v", viewers)
	}
	if slices.Contains(viewers, roleArn("sluis-staging-backup")) || slices.Contains(viewers, roleArn("sluis-staging-slack")) {
		t.Errorf("viewers include a role that hosts its module or backs up: %v", viewers)
	}
	// The same through the built component.
	args := v5Estate(func(x *arp.LambdaArgs) { x.TransitionRoles = a.TransitionRoles }).args(t)
	var got []string
	if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		l, err := arp.NewLambda(ctx, "staging", args)
		if err != nil {
			return err
		}
		got, err = l.Senders()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, senders) {
		t.Errorf("component senders %v, args %v", got, senders)
	}
}

func TestTransitionRolesAreRefusedWhenWrong(t *testing.T) {
	for name, tr := range map[string][]arp.TransitionRole{
		"unknown role": {{Role: "nope", OldName: "x"}},
		"bad name":     {{Role: arp.RoleIssuer, OldName: "arn:aws:iam::1:role/x"}},
		"twice":        {{Role: arp.RoleIssuer, OldName: "x"}, {Role: arp.RoleBackup, OldName: "x"}},
	} {
		if _, err := kpLambdaArgs(t, func(a *arp.LambdaArgs) { a.TransitionRoles = tr }).Senders(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExternalReaderRoleGrantsExactlyItsAddresses(t *testing.T) {
	addrs := []string{"external/slack/t0acme", "external/github/acme-bot"}
	keyArn := arnp + "kms:" + region + ":" + account + ":key/k"
	var rec *recorder
	var out map[string]string
	var stmt map[string]any
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		r, err := arp.NewExternalReaderRole(ctx, "eso", &arp.ExternalReaderRoleArgs{
			Region: region, AccountID: account, Instance: "acme", Addresses: addrs, RoleName: "acme-eso-reader",
			TrustPrincipalArn: arnp + "iam::" + account + ":role/eso-irsa", SecretsKeyArn: pulumi.String(keyArn),
		})
		if err != nil {
			return err
		}
		stmt = r.KeyStatement
		collect("policy", r.PolicyJSON)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	role := rec.one(t, "aws:iam/role:Role", "eso-role")
	if prop(role, "name").StringValue() != "acme-eso-reader" || !strings.Contains(prop(role, "assumeRolePolicy").StringValue(), "eso-irsa") {
		t.Errorf("role: %v", role.Inputs)
	}
	ssm := arnp + "ssm:" + region + ":" + account + ":parameter/sluis/acme/"
	want := []string{ssm + "external/github/acme-bot", ssm + "external/slack/t0acme"}
	// The key statement: Decrypt, through SSM, for those parameters, for that role.
	raw, _ := json.Marshal(stmt)
	for _, w := range want {
		if !strings.Contains(string(raw), w) {
			t.Errorf("key statement lacks %s: %s", w, raw)
		}
	}
	if strings.Count(string(raw), "parameter/") != len(want) || strings.Contains(fmt.Sprint(stmt["Condition"]), "*") {
		t.Errorf("key statement is wider than its addresses: %s", raw)
	}
	if fmt.Sprint(stmt["Action"]) != "[kms:Decrypt]" ||
		!slices.Equal(principals(stmt), []string{arnp + "iam::" + account + ":role/acme-eso-reader"}) {
		t.Errorf("key statement: %s", raw)
	}
	if !strings.Contains(out["policy"], "external/github/acme-bot") || strings.Contains(out["policy"], "*") {
		t.Errorf("policy: %s", out["policy"])
	}
	// It merges into the library's key policy without a Sid collision.
	a := kpLambdaArgs(t, nil)
	if _, err := a.KeyPolicyStatements(arp.KeyPolicyOptions{ReaderStatements: []map[string]any{stmt}}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalReaderRoleRefusesWhatIsNotExactOrNotTrusted(t *testing.T) {
	base := arp.ExternalReaderRoleArgs{Region: region, AccountID: account, Instance: "acme",
		Addresses: []string{"external/github/bot"}, TrustPrincipalArn: arnp + "iam::" + account + ":role/x"}
	for name, mutate := range map[string]func(*arp.ExternalReaderRoleArgs){
		"wildcard address": func(a *arp.ExternalReaderRoleArgs) { a.Addresses = []string{"external/github/*"} },
		"no trust":         func(a *arp.ExternalReaderRoleArgs) { a.TrustPrincipalArn = "" },
		"wildcard trust":   func(a *arp.ExternalReaderRoleArgs) { a.TrustPrincipalArn = arnp + "iam::" + account + ":root*" },
		"two trusts":       func(a *arp.ExternalReaderRoleArgs) { a.TrustPolicy = pulumi.String("{}") },
		"bad role name":    func(a *arp.ExternalReaderRoleArgs) { a.RoleName = "a/b" },
	} {
		a := base
		mutate(&a)
		if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			_, err := arp.NewExternalReaderRole(ctx, "eso", &a)
			return err
		}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
