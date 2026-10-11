package sluispulumi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
)

// The limits of a module function's settings.
const (
	moduleFunctionMaxMemoryMB   = 10240
	moduleFunctionMinMemoryMB   = 128
	moduleFunctionMaxTimeoutSec = 900
)

// ModuleArgs configures the function of one module: [LambdaArgs.Modules] is
// keyed by [ModuleFunctions]. Every field is optional.
type ModuleArgs struct {
	// FunctionName overrides the name of the function, its role, its policy, its
	// log group and its configuration layer. Default `sluis-<instance>-<module>`.
	FunctionName string
	// Package and PackageSHA256 are the module's release zip. Left empty, the zip
	// is the one beside LambdaArgs.Package with the module's name in place of
	// `issuer` (`sluis-slack_<version>_linux_arm64.zip`), with the digest from the
	// release's checksums.txt (LambdaArgs.Release).
	Package       string
	PackageSHA256 string
	// MemoryMB defaults to LambdaArgs.Function.MemoryMB and
	// TimeoutSeconds to LambdaArgs.Function.TimeoutSeconds.
	MemoryMB       int
	TimeoutSeconds int
	// ReservedConcurrency is the function's reserved concurrency. Nil is unreserved.
	// At least 1.
	ReservedConcurrency *int
	// Document is the function's service document. Left empty, the library
	// writes the document of the module for the installation.
	Document string
}

// moduleFunctionPlan is one module function as NewLambda resolves it.
type moduleFunctionPlan struct {
	role         string
	name         string // the function's name
	pkg, pkgSHA  string
	memory, time int
	reserved     *int
	document     string
	// fromInstallation says the document is the one rendered from the
	// installation, which names the policy file it reads.
	fromInstallation bool
}

// functionNameOf is the function's name: the override or `sluis-<instance>-<role>`.
func (a *LambdaArgs) moduleFunctionName(role string) string {
	if n := a.Modules[role].FunctionName; n != "" {
		return n
	}
	return "sluis-" + a.Instance + "-" + role
}

// backupFunctionName is the function the issuer's console calls for the backup.
func (a *LambdaArgs) backupFunctionName() string {
	if a.BackupFunctionName != "" {
		return a.BackupFunctionName
	}
	return "sluis-" + a.Instance + "-" + RoleBackup
}

// functionArnOf is a function's unqualified ARN, from its name: the roles grant
// their calls on its aliases without waiting for the function.
func (a *LambdaArgs) functionArnOf(name string) string {
	return arnPrefix + "lambda:" + a.Region + ":" + a.AccountID + ":function:" + name
}

// roleArnOf is the ARN of a role of the given name (no path).
func (a *LambdaArgs) roleArnOf(name string) string {
	return arnPrefix + "iam::" + a.AccountID + ":role/" + name
}

// hasModuleFunctions reports whether NewLambda makes the module functions: it
// does on every layout that has per-module tables (v5, v4+v5).
func (a *LambdaArgs) hasModuleFunctions() bool { return a.Layout.has5() }

// moduleFunctionArns are the unqualified ARNs of every function a role of the
// issuer invokes, by role name.
func (a *LambdaArgs) moduleFunctionArns() map[string]string {
	m := map[string]string{RoleBackup: a.functionArnOf(a.backupFunctionName())}
	for _, r := range ModuleFunctions() {
		m[r] = a.functionArnOf(a.moduleFunctionName(r))
	}
	return m
}

// validateModules holds Modules to the functions NewLambda makes.
func (a *LambdaArgs) validateModules() error {
	if len(a.Modules) > 0 && !a.hasModuleFunctions() {
		return fmt.Errorf("sluispulumi: LambdaArgs.Modules configures the module functions, which need the per-module tables: Layout %q has none", a.Layout)
	}
	if a.BackupFunctionName != "" && !functionNamePattern.MatchString(a.BackupFunctionName) {
		return fmt.Errorf("sluispulumi: LambdaArgs.BackupFunctionName %q is letters, digits, - and _, at most 64", a.BackupFunctionName)
	}
	var keys []string
	for k := range a.Modules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	known := map[string]bool{}
	for _, r := range ModuleFunctions() {
		known[r] = true
	}
	for _, k := range keys {
		if !known[k] {
			return fmt.Errorf("sluispulumi: LambdaArgs.Modules names %q, which is not a module function (%s)", k, strings.Join(ModuleFunctions(), ", "))
		}
		m := a.Modules[k]
		if m.FunctionName != "" && !functionNamePattern.MatchString(m.FunctionName) {
			return fmt.Errorf("sluispulumi: LambdaArgs.Modules[%s].FunctionName %q is letters, digits, - and _, at most 64", k, m.FunctionName)
		}
		if m.MemoryMB != 0 && (m.MemoryMB < moduleFunctionMinMemoryMB || m.MemoryMB > moduleFunctionMaxMemoryMB) {
			return fmt.Errorf("sluispulumi: LambdaArgs.Modules[%s].MemoryMB %d is %d to %d", k, m.MemoryMB, moduleFunctionMinMemoryMB, moduleFunctionMaxMemoryMB)
		}
		if m.TimeoutSeconds != 0 && (m.TimeoutSeconds < 1 || m.TimeoutSeconds > moduleFunctionMaxTimeoutSec) {
			return fmt.Errorf("sluispulumi: LambdaArgs.Modules[%s].TimeoutSeconds %d is 1 to %d", k, m.TimeoutSeconds, moduleFunctionMaxTimeoutSec)
		}
		if r := m.ReservedConcurrency; r != nil && *r < 1 {
			return fmt.Errorf("sluispulumi: LambdaArgs.Modules[%s].ReservedConcurrency %d is not a ceiling: at least 1, or nil for unreserved", k, *r)
		}
	}
	if !a.hasModuleFunctions() {
		return nil
	}
	// Two functions never share a name: a name is a role, a policy and a log group.
	seen := map[string]string{a.FunctionName: RoleIssuer, a.backupFunctionName(): RoleBackup}
	for _, r := range ModuleFunctions() {
		n := a.moduleFunctionName(r)
		if other, dup := seen[n]; dup {
			return fmt.Errorf("sluispulumi: the function name %q is both the %s function's and the %s function's", n, other, r)
		}
		seen[n] = r
	}
	return nil
}

// modulePackage is the zip of a module function: the module's own override, or
// the issuer's zip with the module's name in place of `issuer`.
func modulePackage(issuer, role string) (string, error) {
	zip := ModuleOfFunction(role)
	i := strings.LastIndex(issuer, "/") + 1
	base := issuer[i:]
	if !strings.HasPrefix(base, "sluis-issuer_") {
		return "", fmt.Errorf("sluispulumi: LambdaArgs.Modules[%s].Package is required: the package %q is not named sluis-issuer_<version>_..., "+
			"so the module's zip cannot be found beside it", role, base)
	}
	return issuer[:i] + "sluis-" + string(zip) + "_" + strings.TrimPrefix(base, "sluis-issuer_"), nil
}

// planModules resolves every module function's settings.
func (a *LambdaArgs) planModules() ([]moduleFunctionPlan, error) {
	var out []moduleFunctionPlan
	for _, r := range ModuleFunctions() {
		m := a.Modules[r]
		p := moduleFunctionPlan{role: r, name: a.moduleFunctionName(r), pkg: m.Package, pkgSHA: m.PackageSHA256,
			memory: m.MemoryMB, time: m.TimeoutSeconds, reserved: m.ReservedConcurrency, document: m.Document}
		if p.pkg == "" {
			var err error
			if p.pkg, err = modulePackage(a.Package, r); err != nil {
				return nil, err
			}
		}
		if p.memory == 0 {
			p.memory = a.Function.MemoryMB
		}
		if p.time == 0 {
			p.time = a.Function.TimeoutSeconds
		}
		if p.document == "" {
			p.document = a.moduleDocs[r]
			p.fromInstallation = p.document != ""
		}
		if p.document == "" {
			if !a.hostsModule(r) {
				continue
			}
			p.document = moduleDocument(r)
		}
		out = append(out, p)
	}
	return out, nil
}

// moduleDocumentVersion is the apiVersion, without the group, of the document a
// module function reads: the GitHub module reads the
// controller's, the Cloudflare module the service document with `cloudflare.serve`.
func moduleDocumentVersion(role string) string {
	switch role {
	case RoleGitHub:
		return "controller-github/v2"
	case RoleSlack:
		return "controller-slack/v2"
	case RoleGoogle:
		return "sluis-google/v1"
	}
	return "sluis/v3"
}

// moduleDocument is what a module function reads when the library has no
// installation to render its document from (the deprecated Config and Policy):
// its apiVersion alone, which the function refuses to start on. The module's
// own settings come from LambdaArgs.Installation or ModuleArgs.Document.
func moduleDocument(role string) string {
	return "apiVersion: " + sluisconfig.Group + "/" + moduleDocumentVersion(role) + "\n"
}

// readsPolicy is whether the module's function reads the policy document: the
// controllers decide by it and the Cloudflare module holds the grants to its
// presets. The Google module reads none.
func readsPolicy(role string) bool { return role != RoleGoogle }

// moduleRole is the role of a module function, for the policies that name it.
func moduleRole(role string) Role {
	return Role{Name: role, Hosts: FunctionHosts(role)}
}

// moduleRoles are the roles of every function NewLambda makes.
func moduleRoles() []Role {
	roles := []Role{moduleRole(RoleIssuer)}
	for _, r := range ModuleFunctions() {
		roles = append(roles, moduleRole(r))
	}
	return roles
}

// moduleFunctions is what NewLambda makes of the module functions.
type moduleFunctions struct {
	fns map[string]*moduleFunction
}

// functions, roleArns and aliasArns are the maps NewLambda exports, keyed by
// the role: `issuer` and every [ModuleFunctions].
func (m *moduleFunctions) outputs(
	issuerFn, issuerRole pulumi.StringOutput, issuerAliases pulumi.StringMapInput,
) (fns, roles pulumi.StringMapOutput, aliases pulumi.StringMapMapOutput) {
	f := pulumi.StringMap{RoleIssuer: issuerFn}
	r := pulumi.StringMap{RoleIssuer: issuerRole}
	al := pulumi.StringMapMap{RoleIssuer: issuerAliases.ToStringMapOutput()}
	for k, fn := range m.fns {
		f[k], r[k], al[k] = fn.fn.Arn, fn.role.Arn, fn.aliasArns()
	}
	return f.ToStringMapOutput(), r.ToStringMapOutput(), al.ToStringMapMapOutput()
}

// tablesNeeded are the modules whose tables the roles of NewLambda are granted:
// those the functions host, and those the issuer reads.
func tablesNeeded() []Module {
	seen := map[Module]bool{}
	var out []Module
	add := func(ms ...Module) {
		for _, m := range ms {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	for _, r := range append([]string{RoleIssuer}, ModuleFunctions()...) {
		add(FunctionHosts(r)...)
		for _, g := range CrossGrants {
			if g.Role == r && (g.Verb == CrossReadTable || g.Verb == CrossReadAll || g.Verb == CrossReadModule) {
				add(g.Modules...)
			}
		}
	}
	return sortedModules(out)
}

// issuerFunctionArns are the functions the issuer's role invokes, by role; nil
// where there are no module functions.
func issuerFunctionArns(a *LambdaArgs) map[string]string {
	if !a.hasModuleFunctions() {
		return nil
	}
	return a.moduleFunctionArns()
}

// moduleSenders are the module functions' roles, by their ARNs: the audit queue
// admits each of them, and a role it does not name has its records refused.
func (a *LambdaArgs) moduleSenders() []pulumi.StringInput {
	var out []pulumi.StringInput
	if a.hasModuleFunctions() {
		for _, r := range ModuleFunctions() {
			if a.hostsModule(r) {
				out = append(out, pulumi.String(a.roleArnOf(a.moduleFunctionName(r))))
			}
		}
	}
	// The old names of renamed roles stay senders while the switch is made.
	for _, t := range a.TransitionRoles {
		out = append(out, pulumi.String(a.roleArnOf(t.OldName)))
	}
	return out
}

// hostsModule is whether NewLambda makes the module's function. It makes all
// five, but for the module the installation rendered no document for: it has
// nothing to do (the Cloudflare module of an installation with no preset), and a
// function that could not start would fail the deploy's check. A role that does
// not exist is not a sender the audit queue can name.
func (a *LambdaArgs) hostsModule(role string) bool {
	if a.moduleDocs == nil || a.Modules[role].Document != "" {
		return true
	}
	_, ok := a.moduleDocs[role]
	return ok
}

// newModuleFunctions makes the function of each module of [ModuleFunctions] on
// newModuleFunction, with the role ModuleRoleStatements renders for it and the
// aliases `live` and `live-issuer` (and, for google, `live-console`): the one the schedules and the issuer's runs
// invoke and the one its calls go through. Nothing is shared with the issuer's
// function but the account.
func newModuleFunctions(ctx *pulumi.Context, name string, a *LambdaArgs, child pulumi.ResourceOption) (*moduleFunctions, error) {
	plans, err := a.planModules()
	if err != nil {
		return nil, err
	}
	roles := moduleRoles()
	out := &moduleFunctions{fns: map[string]*moduleFunction{}}
	for _, p := range plans {
		p := p
		callers := []string{ClassIssuer}
		if p.role == RoleGoogle {
			// The console's Workspace pages and consent are the Google module's
			// (D-v176-1 b): its alias for the class `console` is the one the
			// issuer's console calls through.
			callers = append(callers, ClassConsole)
		}
		fa := &ModuleFunctionArgs{
			Name: name + "-" + p.role, FunctionName: p.name,
			Package: p.pkg, PackageSHA256: p.pkgSHA, PackageVersion: a.PackageVersion, Release: a.Release, Artifacts: a.Artifacts,
			Documents:       a.moduleLayerDocuments(&p),
			DocumentVersion: moduleDocumentVersion(p.role),
			MemoryMB:        p.memory, TimeoutSeconds: p.time, ReservedConcurrency: p.reserved,
			LogRetentionDays: a.LogRetentionDays, PermissionsBoundaryArn: a.PermissionsBoundaryArn, Tags: a.Tags,
			Callers: callers,
			Policy:  a.modulePolicy(p.role, roles),
		}
		// A pass that failed is the next one's: no retry by the platform.
		fa.NoRetry = []string{LiveAlias, CallerAlias(ClassIssuer)}
		if a.Check == nil || *a.Check {
			fa.Check = &ModuleCheck{}
			if p.fromInstallation {
				// The check re-runs when what the function declares changes.
				if fa.Check.Declared, err = moduleParameters(p.role, p.document, a.Policy); err != nil {
					return nil, err
				}
			}
		}
		fn, err := newModuleFunction(ctx, fa, child)
		if err != nil {
			return nil, fmt.Errorf("sluis %s function: %w", p.role, err)
		}
		out.fns[p.role] = fn
	}
	return out, nil
}

// moduleLayerDocuments are the files of a module function's configuration layer:
// its service document and, where the function decides by it, the policy
// document its service document names (the same file as the issuer's).
func (a *LambdaArgs) moduleLayerDocuments(p *moduleFunctionPlan) map[string]string {
	docs := map[string]string{docSluis: p.document}
	if p.fromInstallation && readsPolicy(p.role) && strings.TrimSpace(a.Policy) != "" {
		docs[docPolicy] = a.Policy
	}
	return docs
}

// modulePolicy is the policy of a module function's role: ModuleRoleStatements
// of its role, and the read of the external blobs' credentials where the role
// has blobs.
func (a *LambdaArgs) modulePolicy(role string, roles []Role) func(pulumi.StringOutput) pulumi.StringInput {
	return func(logGroupArn pulumi.StringOutput) pulumi.StringInput {
		bucket := pulumi.StringInput(pulumi.String(""))
		if a.Storage != nil && a.Storage.BucketArn != nil {
			bucket = a.Storage.BucketArn
		}
		tableKey := pulumi.StringInput(pulumi.String(""))
		if a.State != nil && a.State.KeyArn != nil {
			tableKey = a.State.KeyArn
		}
		queue := pulumi.StringInput(pulumi.String(""))
		if a.AuditQueueArn != nil {
			queue = a.AuditQueueArn
		}
		return pulumi.All(bucket, tableKey, queue, logGroupArn, moduleTableArns(a.State)).ApplyT(func(v []any) (string, error) {
			env := ModuleEnv{
				Region: a.Region, Account: a.AccountID, Modules: ModuleSet{Instance: a.Instance, Tables: a.TableNames},
				BucketArn: v[0].(string), TableKeyArn: v[1].(string), ParameterKeyArn: a.ParameterKeyArn, QueueArn: v[2].(string),
				TableArns:    moduleTablesOf(v[4]),
				LogGroupArns: map[string]string{role: v[3].(string)},
				FunctionArns: a.moduleFunctionArns(), MinterRefs: a.minterRefs, Roles: roles,
			}
			if a.Storage != nil && a.Storage.External != nil {
				env.BucketArn = ""
			}
			st, err := ModuleRoleStatements(env, moduleRole(role))
			if err != nil {
				return "", err
			}
			if a.Storage != nil {
				st = appendNew(st, credentialsStatements(a.Storage.External, a.Region, a.AccountID, a.Instance, a.ParameterKeyArn)...)
			}
			return document(st)
		}).(pulumi.StringOutput)
	}
}
