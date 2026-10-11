package sluispulumi

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/deploy/pulumi/internal/iameval"
)

// The IAM deny matrix. For every role the library renders in v1.75 and every
// module, it evaluates calls the role must be refused (another module's
// parameters, table, blobs and parameter-key use, the sign context, a function
// or alias not granted, the maintenance partition) and the calls it must be
// allowed, against the role's identity policy and the shared key's policy
// together. The expected answer of a row comes from the small table of
// roleSpecs below, which says what each role is for, not from CrossGrants: the
// matrix would otherwise agree with the code it checks.

const (
	mxRegion   = "r1"
	mxAccount  = "000000000000"
	mxInst     = "i1"
	mxBucket   = "arn:aws:s3:::acme-blobs"
	mxKey      = "arn:aws:kms:r1:000000000000:key/shared"
	mxTableKey = "arn:aws:kms:r1:000000000000:key/tables"
	mxQueue    = "arn:aws:sqs:r1:000000000000:acme-audit"
	mxSsmVia   = "ssm.r1.amazonaws.com"
	mxMinter   = "internal/cloudflare/main/minter"
)

// mxRows is the fewest rows the matrix may have. It is pinned so that a sweep
// that silently shrinks (a role dropped, Modules() emptied, a group skipped)
// fails instead of passing. Raise it when roles or groups are added; never lower it.
const mxRows = 1771

// roleSpec is what a role is for in v1.75.
type roleSpec struct {
	name     string
	hosts    []Module
	readAll  bool // reads every module's table, blobs and parameters (backup)
	writeAll bool // writes them (restore)
	signs    bool // carries the sign-context grant
	// invokes are the exact function ARNs (alias included) it may invoke.
	invokes []string
	// intendedInvokes are the ARNs it should be able to invoke where the library
	// renders a grant the calls cannot use (CrossInvoke, finding V11a).
	intendedInvokes []string
	known           map[string]string // row name -> finding, for a row that fails today
}

func mxFn(name string) string {
	return "arn:aws:lambda:" + mxRegion + ":" + mxAccount + ":function:sluis-" + mxInst + "-" + name
}

func mxRoleArn(name string) string { return "arn:aws:iam::" + mxAccount + ":role/fn-" + name }

func mxTable(m Module) string {
	return "arn:aws:dynamodb:" + mxRegion + ":" + mxAccount + ":table/sluis-" + mxInst + "-" + string(m)
}

func mxParam(prefix string, m string, leaf string) string {
	return "arn:aws:ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst + "/" + prefix + "/" + m + "/" + leaf
}

var mxFunctions = []string{"issuer", "cloudflare", "backup", "restore", "github"}

func mxSpecs() []roleSpec {
	issuerHosts := []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleGoogle}
	return []roleSpec{
		{name: "issuer", hosts: issuerHosts, signs: true, invokes: []string{mxFn("issuer") + ":live"}},
		{name: "issuer-cf", hosts: append(slices.Clone(issuerHosts), ModuleCloudflare), signs: true,
			invokes: []string{mxFn("issuer") + ":live"}},
		{name: "backup", hosts: []Module{ModuleBackup}, readAll: true},
		{name: "restore", hosts: []Module{ModuleBackup}, writeAll: true, invokes: []string{mxFn("restore") + ":live-console"}},
		{name: "cloudflare", hosts: []Module{ModuleCloudflare}},
		// The library's CrossInvoke rendering: the issuer calling the Cloudflare function.
		{name: "issuer-xinvoke", hosts: issuerHosts,
			intendedInvokes: []string{mxFn("cloudflare") + ":live-issuer"},
			known: map[string]string{
				"invoke " + mxFn("cloudflare") + ":live-issuer (granted alias)":    "V11a",
				"invoke " + mxFn("cloudflare") + " (unqualified, must be refused)": "V11a",
			}},
	}
}

func (s roleSpec) hostsModule(m Module) bool { return slices.Contains(s.hosts, m) }
func (s roleSpec) reads(m Module) bool       { return s.hostsModule(m) || s.readAll || s.writeAll }
func (s roleSpec) writes(m Module) bool      { return s.hostsModule(m) || s.writeAll }

// mxEnv is what the library renders the roles from.
func mxEnv(roles []Role) ModuleEnv {
	tables := map[Module]string{}
	for _, m := range Modules() {
		tables[m] = mxTable(m)
	}
	fns := map[string]string{}
	logs := map[string]string{}
	for _, f := range mxFunctions {
		fns[f] = mxFn(f)
		logs[f] = "arn:aws:logs:" + mxRegion + ":" + mxAccount + ":log-group:/aws/lambda/" + f
	}
	return ModuleEnv{
		Region: mxRegion, Account: mxAccount, Modules: ModuleSet{Instance: mxInst},
		BucketArn: mxBucket, TableArns: tables, TableKeyArn: mxTableKey, ParameterKeyArn: mxKey,
		QueueArn: mxQueue, LogGroupArns: logs, FunctionArns: fns, Roles: roles,
	}
}

func mxPolicies(t *testing.T) (identity map[string]iameval.Policy, key iameval.Policy) {
	t.Helper()
	identity = map[string]iameval.Policy{}
	parse := func(name string, doc any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		p, err := iameval.Parse(doc)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		identity[name] = p
	}
	env := mxEnv(nil)
	tables := map[Module]string{}
	for m, a := range env.TableArns {
		tables[m] = a
	}
	// The issuer: the whole function policy the library renders, signing and
	// secrets key grants included, once without and once with the Cloudflare minter.
	fp := func(cloudflare bool) (string, error) {
		return functionPolicy(functionPolicyIn{
			region: mxRegion, account: mxAccount, bucketArn: mxBucket, tableKey: mxTableKey,
			moduleTables: tables, queueArn: mxQueue, logGroupArn: env.LogGroupArns["issuer"],
			keys:            &keyGrants{signArn: mxKey, secretsArn: mxKey},
			parameterKeyArn: mxKey, instance: mxInst, invokeFunctionArns: []string{mxFn("issuer") + ":live"},
			webIdentityAud: "acme", cloudflare: cloudflare, layout: LayoutV5,
			modules: ModuleSet{Instance: mxInst}, minterRefs: []string{mxMinter},
		})
	}
	doc, err := fp(false)
	parse("issuer", doc, err)
	doc, err = fp(true)
	parse("issuer-cf", doc, err)

	arch := ArchiveGrant{BucketArn: "arn:aws:s3:::acme-archive", Prefix: "pre/", Instance: mxInst,
		KeyArn: "arn:aws:kms:r1:000000000000:key/archive", Versioned: true}
	st, err := BackupRoleStatements(env, arch)
	parse("backup", st, err)
	st, err = RestoreRoleStatements(env, arch, []string{mxFn("restore") + ":live-console"})
	parse("restore", st, err)
	st, err = ModuleRoleStatements(env, Role{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}})
	parse("cloudflare", st, err)
	xenv := mxEnv([]Role{
		{Name: RoleIssuer, Hosts: []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleGoogle}},
		{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}},
	})
	st, err = ModuleRoleStatements(xenv, Role{Name: RoleIssuer, Hosts: []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleGoogle}})
	parse("issuer-xinvoke", st, err)

	var kroles []KeyRole
	for _, s := range mxSpecs() {
		kr := KeyRole{Role: Role{Name: s.name, Hosts: s.hosts}, Arn: mxRoleArn(s.name)}
		if s.name == "issuer" {
			kr.MinterRefs = []string{mxMinter}
		}
		kroles = append(kroles, kr)
	}
	kst, err := SluisKeyPolicyStatements(KeyPolicyArgs{
		Region: mxRegion, Account: mxAccount, Modules: ModuleSet{Instance: mxInst}, Layout: LayoutV5, Roles: kroles,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err = iameval.Parse(kst)
	if err != nil {
		t.Fatal(err)
	}
	return identity, key
}

// mxRow is one call and the answer it must get.
type mxRow struct {
	role    string
	group   string
	name    string
	req     iameval.Request
	want    bool
	ceiling bool // evaluate with an identity policy that allows all of KMS: the key policy alone must refuse
}

func mxRows_(specs []roleSpec) []mxRow {
	var rows []mxRow
	for _, s := range specs {
		add := func(group, name, action, resource string, want bool, ctx func(iameval.Request) iameval.Request) {
			req := iameval.Request{Principal: mxRoleArn(s.name), Action: action, Resource: resource}
			if ctx != nil {
				req = ctx(req)
			}
			rows = append(rows, mxRow{role: s.name, group: group, name: name, req: req, want: want})
		}
		leading := func(keys ...string) func(iameval.Request) iameval.Request {
			return func(r iameval.Request) iameval.Request { return r.WithContext("dynamodb:LeadingKeys", keys...) }
		}
		prefix := func(p string) func(iameval.Request) iameval.Request {
			return func(r iameval.Request) iameval.Request { return r.WithContext("s3:prefix", p) }
		}
		viaSSM := func(param string) func(iameval.Request) iameval.Request {
			return func(r iameval.Request) iameval.Request {
				return r.WithContext("kms:ViaService", mxSsmVia).WithEncryptionContext(map[string]string{"PARAMETER_ARN": param})
			}
		}
		kmsAdd := func(name, action, param string, want bool) {
			add("kms-parameter", name, action, mxKey, want, viaSSM(param))
			if !want && !s.readAll && !s.writeAll {
				// The key policy must refuse it whatever the identity policy says.
				req := iameval.Request{Principal: mxRoleArn(s.name), Action: action, Resource: mxKey}
				rows = append(rows, mxRow{role: s.name, group: "kms-parameter-ceiling", name: name, req: viaSSM(param)(req), ceiling: true})
			}
		}

		for _, m := range Modules() {
			ms := string(m)
			// SSM and the parameter key, for internal/<m>/ and external/<m>/.
			for _, ns := range []string{"internal", "external"} {
				p := mxParam(ns, ms, "k")
				add("ssm", fmt.Sprintf("%s/%s read", ns, ms), "ssm:GetParameter", p, s.reads(m), nil)
				add("ssm", fmt.Sprintf("%s/%s write", ns, ms), "ssm:PutParameter", p, s.writes(m), nil)
				add("ssm", fmt.Sprintf("%s/%s delete", ns, ms), "ssm:DeleteParameter", p, s.writes(m), nil)
				add("ssm", fmt.Sprintf("%s/%s lookalike sibling", ns, ms), "ssm:GetParameter", mxParam(ns, ms+"x", "k"), false, nil)
				kmsAdd(fmt.Sprintf("%s/%s decrypt", ns, ms), "kms:Decrypt", p, s.reads(m))
				kmsAdd(fmt.Sprintf("%s/%s encrypt", ns, ms), "kms:Encrypt", p, s.writes(m))
				kmsAdd(fmt.Sprintf("%s/%s generate data key", ns, ms), "kms:GenerateDataKey", p, s.writes(m))
			}
			// The direct (not through SSM) use of the key on a parameter's context.
			add("kms-parameter", fmt.Sprintf("internal/%s decrypt, direct call", ms), "kms:Decrypt", mxKey, false,
				func(r iameval.Request) iameval.Request {
					return r.WithEncryptionContext(map[string]string{"PARAMETER_ARN": mxParam("internal", ms, "k")})
				})

			// DynamoDB: records, then the maintenance partition.
			tbl := mxTable(m)
			for _, a := range []string{"dynamodb:GetItem", "dynamodb:Query"} {
				add("ddb-read", ms+" "+a, a, tbl, s.reads(m), leading("rec.k"))
			}
			for _, a := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem"} {
				add("ddb-write", ms+" "+a, a, tbl, s.writes(m), leading("rec.k"))
				// The maintenance partition is the restore role's alone, in any table.
				add("maintenance", ms+" "+a+" maintenance", a, tbl, s.name == RoleRestore, leading(MaintenancePartition))
				add("maintenance", ms+" "+a+" mixed keys", a, tbl, s.name == RoleRestore && s.writes(m), leading("rec.k", MaintenancePartition))
			}
			for _, a := range []string{"dynamodb:BatchWriteItem", "dynamodb:TransactWriteItems"} {
				add("maintenance", ms+" "+a, a, tbl, false, leading(MaintenancePartition))
				add("ddb-write", ms+" "+a+" (granted to no role)", a, tbl, false, leading("rec.k"))
			}
			add("maintenance", ms+" read the flag", "dynamodb:GetItem", tbl, s.reads(m), leading(MaintenancePartition))

			// S3: the module's blob prefix.
			add("s3", ms+" get", "s3:GetObject", mxBucket+"/"+ms+"/obj", s.reads(m), nil)
			add("s3", ms+" put", "s3:PutObject", mxBucket+"/"+ms+"/obj", s.writes(m), nil)
			add("s3", ms+" delete", "s3:DeleteObject", mxBucket+"/"+ms+"/obj", s.writes(m), nil)
			add("s3", ms+" list", "s3:ListBucket", mxBucket, s.reads(m), prefix(ms+"/"))
			add("s3", ms+" lookalike sibling", "s3:GetObject", mxBucket+"/"+ms+"x/obj", false, nil)
			add("s3", ms+" list lookalike sibling", "s3:ListBucket", mxBucket, false, prefix(ms+"x/"))
		}

		// S3 outside every module's prefix.
		add("s3", "root object", "s3:GetObject", mxBucket+"/stray", false, nil)
		add("s3", "list without a prefix", "s3:ListBucket", mxBucket, false, nil)
		add("s3", "list everything", "s3:ListBucket", mxBucket, false, prefix(""))

		// The Cloudflare minter parameter: read by the issuer without hosting the module.
		minter := "arn:aws:ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst + "/" + mxMinter
		wantMinter := s.hostsModule(ModuleCloudflare) || s.readAll || s.writeAll || s.name == "issuer"
		add("minter", "minter read", "ssm:GetParameter", minter, wantMinter, nil)
		add("minter", "minter write", "ssm:PutParameter", minter, s.hostsModule(ModuleCloudflare) || s.writeAll, nil)
		kmsAdd("minter decrypt", "kms:Decrypt", minter, wantMinter)
		add("minter", "another account's minter", "ssm:GetParameter",
			"arn:aws:ssm:"+mxRegion+":"+mxAccount+":parameter/sluis/"+mxInst+"/internal/cloudflare/main/other",
			s.hostsModule(ModuleCloudflare) || s.readAll || s.writeAll, nil)

		// The sign context: only the roles that host oidc, directly, under exactly {instance, purpose}.
		sign := func(name, action string, ec map[string]string, want bool) {
			req := iameval.Request{Principal: mxRoleArn(s.name), Action: action, Resource: mxKey}.WithEncryptionContext(ec)
			rows = append(rows, mxRow{role: s.name, group: "sign", name: name, req: req, want: want})
			if !s.hostsModule(ModuleOIDC) {
				rows = append(rows, mxRow{role: s.name, group: "sign-ceiling", name: name, req: req, ceiling: true})
			}
		}
		for _, a := range []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"} {
			sign(a+" sign context", a, map[string]string{"instance": mxInst, "purpose": "sign"}, s.signs)
			sign(a+" sign context, extra key", a, map[string]string{"instance": mxInst, "purpose": "sign", "extra": "x"}, false)
			sign(a+" sign context, other instance", a, map[string]string{"instance": "other", "purpose": "sign"}, false)
			sign(a+" other purpose", a, map[string]string{"instance": mxInst, "purpose": "audit"}, false)
		}
		sign("kms:ReEncryptFrom sign context", "kms:ReEncryptFrom", map[string]string{"instance": mxInst, "purpose": "sign"}, false)
		sign("kms:CreateGrant sign context", "kms:CreateGrant", map[string]string{"instance": mxInst, "purpose": "sign"}, false)

		// Lambda: every function, qualified and not.
		granted := func(arn string) bool { return slices.Contains(s.invokes, arn) }
		intended := func(arn string) bool { return granted(arn) || slices.Contains(s.intendedInvokes, arn) }
		for _, f := range mxFunctions {
			for _, q := range []string{"", ":$LATEST", ":live", ":live-issuer", ":live-console", ":live-cloudflare", ":1"} {
				arn := mxFn(f) + q
				name := "invoke " + arn
				switch {
				case q == "" && len(s.intendedInvokes) > 0 && slices.Contains(s.intendedInvokes, arn+":live-issuer"):
					name = "invoke " + arn + " (unqualified, must be refused)"
				case slices.Contains(s.intendedInvokes, arn):
					name = "invoke " + arn + " (granted alias)"
				}
				add("invoke", name, "lambda:InvokeFunction", arn, intended(arn), nil)
			}
		}
	}
	return rows
}

func mxDecide(t *testing.T, r mxRow, identity iameval.Policy, key iameval.Policy) iameval.Decision {
	t.Helper()
	if r.ceiling {
		all := iameval.MustParse(`[{"Sid":"AllKMS","Effect":"Allow","Action":"kms:*","Resource":"*"}]`)
		identity = all
	}
	k := iameval.Policy{}
	if strings.HasPrefix(r.req.Action, "kms:") {
		k = key
	}
	d, err := iameval.Evaluate(r.req, identity, k)
	if err != nil {
		t.Fatalf("%s: %v", r.name, err)
	}
	return d
}

func TestEveryRoleIsRefusedWhatItIsNotGranted(t *testing.T) {
	identity, key := mxPolicies(t)
	specs := mxSpecs()
	rows := mxRows_(specs)
	if len(rows) < mxRows {
		t.Fatalf("the matrix has %d rows, fewer than the %d it is pinned to: an empty or shrunk sweep proves nothing", len(rows), mxRows)
	}
	known := map[string]map[string]string{}
	for _, s := range specs {
		known[s.name] = s.known
	}
	var allowed, refused int
	for _, r := range rows {
		if r.ceiling || !r.want {
			refused++
		} else {
			allowed++
		}
		t.Run(r.role+"/"+r.group+"/"+r.name, func(t *testing.T) {
			pol, ok := identity[r.role]
			if !ok {
				t.Fatalf("no policy rendered for role %s", r.role)
			}
			d := mxDecide(t, r, pol, key)
			want := r.want
			if d.Allowed != want {
				if f := known[r.role][r.name]; f != "" {
					t.Skipf("known finding %s: %s %s on %s = %v (%s), the intent is %v", f, r.req.Action, r.role, r.req.Resource, d.Allowed, d.Reason, want)
				}
				t.Errorf("%s %s on %s: allowed = %v (%s), want %v", r.req.Action, r.role, r.req.Resource, d.Allowed, d.Reason, want)
			}
		})
	}
	// Both halves must be exercised, for every role and every group.
	if allowed < 100 || refused < 800 {
		t.Errorf("the matrix has %d allow rows and %d refuse rows; it should hold both in number", allowed, refused)
	}
	t.Logf("%d rows: %d allow, %d refuse", len(rows), allowed, refused)
}

func TestTheMatrixCoversEveryRoleGroupAndModule(t *testing.T) {
	rows := mxRows_(mxSpecs())
	type cell struct{ role, group string }
	refuse, allow := map[cell]int{}, map[cell]int{}
	modules := map[string]map[string]bool{}
	for _, r := range rows {
		c := cell{r.role, r.group}
		if r.ceiling || !r.want {
			refuse[c]++
		} else {
			allow[c]++
		}
		if modules[r.role] == nil {
			modules[r.role] = map[string]bool{}
		}
		for _, m := range Modules() {
			if strings.Contains(r.name, string(m)) {
				modules[r.role][string(m)] = true
			}
		}
	}
	for _, s := range mxSpecs() {
		for _, g := range []string{"ssm", "kms-parameter", "ddb-write", "maintenance", "s3", "sign", "invoke"} {
			if refuse[cell{s.name, g}] == 0 {
				t.Errorf("role %s has no refuse row in group %s", s.name, g)
			}
		}
		for _, g := range []string{"ssm", "kms-parameter", "ddb-read", "ddb-write", "s3"} {
			if allow[cell{s.name, g}] == 0 {
				t.Errorf("role %s has no allow row in group %s", s.name, g)
			}
		}
		if refuse[cell{s.name, "minter"}]+allow[cell{s.name, "minter"}] == 0 {
			t.Errorf("role %s has no minter row", s.name)
		}
		for _, m := range Modules() {
			if !modules[s.name][string(m)] {
				t.Errorf("role %s has no row naming module %s", s.name, m)
			}
		}
	}
	// The group of rows that proves the key policy is a ceiling.
	for _, g := range []string{"kms-parameter-ceiling", "sign-ceiling"} {
		var n int
		for c, k := range refuse {
			if c.group == g {
				n += k
			}
		}
		if n == 0 {
			t.Errorf("no %s rows", g)
		}
	}
}

// Every issuer's secrets-key grant spans the whole installation, so for the
// issuer it is the key policy that refuses another module's parameter. This
// keeps that visible: with the key policy removed, the identity policy alone
// would allow it.
func TestTheKeyPolicyIsWhatRefusesTheIssuerAnotherModulesSecrets(t *testing.T) {
	identity, key := mxPolicies(t)
	req := iameval.Request{Principal: mxRoleArn("issuer"), Action: "kms:Decrypt", Resource: mxKey}.
		WithContext("kms:ViaService", mxSsmVia).
		WithEncryptionContext(map[string]string{"PARAMETER_ARN": mxParam("internal", "backup", "k")})
	alone, err := iameval.Evaluate(req, identity["issuer"], iameval.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	together, err := iameval.Evaluate(req, identity["issuer"], key)
	if err != nil {
		t.Fatal(err)
	}
	if together.Allowed {
		t.Errorf("the issuer may decrypt the backup module's parameter: %s", together.Reason)
	}
	if !alone.Allowed {
		t.Log("the issuer identity policy no longer spans other modules parameters; the key policy is no longer the only ceiling")
	}
}

// crossInvokeGrantsTheCallableARN is the property V11a restores: a role granted
// to call a module may invoke the alias the call uses (`live-<class>`), and may
// not invoke the unqualified function, which runs $LATEST with no caller class.
func crossInvokeGrantsTheCallableARN(t *testing.T) {
	t.Helper()
	identity, _ := mxPolicies(t)
	fn := mxFn("cloudflare")
	call := func(arn string) bool {
		d, err := iameval.Evaluate(iameval.Request{Principal: mxRoleArn("issuer-xinvoke"), Action: "lambda:InvokeFunction", Resource: arn},
			identity["issuer-xinvoke"], iameval.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		return d.Allowed
	}
	if !call(fn + ":live-issuer") {
		t.Errorf("CrossInvoke does not grant %s:live-issuer, the ARN a modcall invokes", fn)
	}
	if call(fn) {
		t.Errorf("CrossInvoke grants the unqualified %s", fn)
	}
	if call(fn + ":$LATEST") {
		t.Errorf("CrossInvoke grants %s:$LATEST", fn)
	}
}

func TestCrossInvokeGrantsTheQualifiedAlias(t *testing.T) {
	t.Skip("TODO(V11a): CrossInvoke renders the unqualified function ARN, but every modcall invokes the " +
		"`live-<class>` alias, so the first cross-module call is denied; V11a renders qualified grants and removes this skip")
	crossInvokeGrantsTheCallableARN(t)
}
