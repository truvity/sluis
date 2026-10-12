package sluispulumi_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// roleShapes are the installations the role lists are held to: the plain one,
// one that keeps the issuer's old role name, one with function names of its own,
// and one rendered from an installation.
func roleShapes(t *testing.T) map[string]struct {
	full arp.LambdaArgs
	min  arp.RoleArgs
} {
	t.Helper()
	type shape = struct {
		full arp.LambdaArgs
		min  arp.RoleArgs
	}
	mk := func(e estate) shape {
		a := *e.args(t)
		return shape{full: a, min: arp.RoleArgs{
			Region: a.Region, AccountID: a.AccountID, Instance: a.Instance, Layout: a.Layout,
			FunctionName: a.FunctionName, Modules: a.Modules, BackupFunctionName: a.BackupFunctionName,
			RestoreFunctionName: a.RestoreFunctionName, TransitionRoles: a.TransitionRoles,
		}}
	}
	out := map[string]shape{
		"plain": mk(v5Estate()),
		"switching": mk(v5Estate(func(a *arp.LambdaArgs) {
			a.TransitionRoles = []arp.TransitionRole{{Role: arp.RoleIssuer, OldName: "sluis-old"}}
		})),
		"names": mk(v5Estate(func(a *arp.LambdaArgs) {
			a.FunctionName = "acme-issuer"
			a.BackupFunctionName, a.RestoreFunctionName = "acme-bk", "acme-rs"
			a.Modules = map[string]arp.ModuleArgs{"github": {FunctionName: "acme-gh"}}
		})),
		"v4": mk(withLayout(arp.LayoutV4)),
	}
	in := exampleInstallation(t)
	full := *withInstallation(in, nil).args(t)
	out["installation"] = shape{full: full, min: arp.RoleArgs{
		Region: in.AWS.Region, AccountID: account, Instance: in.Instance, Layout: full.Layout, Installation: in,
		Blobs: full.Storage.External,
	}}
	return out
}

// The role lists of RoleArgs are those of the full args: the same principals
// from just the names.
func TestRoleArgsAnswerWhatTheFullArgsAnswer(t *testing.T) {
	opts := arp.KeyPolicyOptions{Admins: []string{arnp + "iam::" + account + ":role/admin"}, DenyOtherActions: true}
	for name, sh := range roleShapes(t) {
		full, narrow := sh.full, sh.min
		fs, ferr := full.Senders()
		ms, merr := narrow.Senders()
		if ferr != nil || merr != nil {
			t.Fatalf("%s: %v / %v", name, ferr, merr)
		}
		if !slices.Equal(fs, ms) {
			t.Errorf("%s: Senders %v, full args %v", name, ms, fs)
		}
		fr, _ := full.Roles()
		mr, err := narrow.Roles()
		if err != nil || !reflect.DeepEqual(fr, mr) {
			t.Errorf("%s: Roles %+v (%v), full args %+v", name, mr, err, fr)
		}
		fv, _ := full.ViewerRoles()
		mv, err := narrow.ViewerRoles()
		if err != nil || !slices.Equal(fv, mv) {
			t.Errorf("%s: ViewerRoles %v (%v), full args %v", name, mv, err, fv)
		}
		fk, ferr := full.KeyPolicyStatements(opts)
		mk, merr := narrow.KeyPolicyStatements(opts)
		if ferr != nil || merr != nil || !reflect.DeepEqual(fk, mk) {
			t.Errorf("%s: KeyPolicyStatements differ (%v / %v)", name, merr, ferr)
		}
	}
}

// FunctionRoles names every function once, with the names the library defaults
// or the estate set, so an access policy repeats none of them.
func TestFunctionRolesNameEveryFunction(t *testing.T) {
	sh := roleShapes(t)["names"]
	roles, err := sh.min.FunctionRoles()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range roles {
		got[r.Role] = r.Name
		if r.Arn != arnp+"iam::"+account+":role/"+r.Name {
			t.Errorf("%s: arn %s", r.Role, r.Arn)
		}
	}
	want := map[string]string{
		"issuer": "acme-issuer", "github": "acme-gh", "slack": "sluis-staging-slack", "google": "sluis-staging-google",
		"cloudflare": "sluis-staging-cloudflare", "backup": "acme-bk", "restore": "acme-rs",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("function names %v, want %v", got, want)
	}
	// The matcher rows: the viewers of the console, by account, name and path.
	ms, err := sh.min.ViewerMatchers()
	if err != nil {
		t.Fatal(err)
	}
	var viewers []string
	for _, m := range ms {
		if m.AWS == nil || m.AWS.Account != account || m.AWS.Path != "/" {
			t.Errorf("matcher %+v", m)
		}
		viewers = append(viewers, m.AWS.Role)
	}
	vr, _ := sh.min.ViewerRoles()
	var names []string
	for _, arn := range vr {
		names = append(names, arn[strings.LastIndex(arn, "/")+1:])
	}
	if !slices.Equal(viewers, names) {
		t.Errorf("viewer matchers %v, viewer roles %v", viewers, names)
	}
	if len(viewers) == 0 {
		t.Error("no viewer matchers")
	}
	for _, r := range roles {
		if r.Role == "github" && r.Matcher().AWS.Role != "acme-gh" {
			t.Errorf("github matcher %+v", r.Matcher())
		}
	}
}

func TestRoleArgsRefuseWhatNamesNoRole(t *testing.T) {
	for name, a := range map[string]arp.RoleArgs{
		"no instance": {Region: region, AccountID: account},
		"bad layout":  {Region: region, AccountID: account, Instance: "i1", Layout: "v9"},
		"bad name":    {Region: region, AccountID: account, Instance: "i1", FunctionName: "no spaces"},
		"transition":  {Region: region, AccountID: account, Instance: "i1", TransitionRoles: []arp.TransitionRole{{Role: "nope", OldName: "x"}}},
	} {
		if _, err := a.Senders(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A LambdaArgs converts to the RoleArgs of its names, and a Lambda to its own.
func TestLambdaArgsConvertToRoleArgs(t *testing.T) {
	for name, sh := range roleShapes(t) {
		if name == "installation" {
			continue // the installation names the instance; the args do not
		}
		full := sh.full
		got, err := full.RoleArgs().Senders()
		want, _ := full.Senders()
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: %v (%v), want %v", name, got, err, want)
		}
	}
}
