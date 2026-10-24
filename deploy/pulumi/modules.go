package sluispulumi

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/truvity/sluis/blobprefix"
)

// Module is one of the parts of a sluis installation that keeps its own
// state: its own table, its own SSM paths (`internal/<module>/...` and
// `external/<module>/...`) and its own blob prefix. A function hosts one or
// more modules, and its role is the union of what those modules need, so that
// splitting a module out of a function later only removes statements from the
// role it leaves.
type Module string

// The modules of the storage layout (docs/decisions/0072).
const (
	// ModuleOIDC is the issuer: sign-in, tokens, clients, the console's sessions.
	ModuleOIDC       Module = "oidc"
	ModuleGitHub     Module = "github"
	ModuleSlack      Module = "slack"
	ModuleCloudflare Module = "cloudflare"
	// ModuleGoogle is the Google workspaces. The Go interface that reads them is
	// still called Directory; storage, paths and policies say google.
	ModuleGoogle Module = "google"
	// ModuleBackup holds the backup and restore state.
	ModuleBackup Module = "backup"
	// ModuleOutline is the Outline workspaces (docs/decisions/0077). It comes
	// last, as in the storage layout's module list.
	ModuleOutline Module = "outline"
)

// Modules is every module, in a fixed order.
func Modules() []Module {
	return []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleCloudflare, ModuleGoogle, ModuleBackup, ModuleOutline}
}

// MaintenancePartition is the partition key of the maintenance item. Every
// module's own table holds one; the module reads it through the grant on that
// table, and only the restore role may write it.
const MaintenancePartition = "maintenance"

// ModuleSet names an installation's modules: the instance and, optionally, a
// table name per module. The default table name is `sluis-<instance>-<module>`.
type ModuleSet struct {
	Instance string
	// Tables overrides the table name of a module. A module not listed gets the
	// default.
	Tables map[Module]string
}

// TableName is the module's table name: the override or `sluis-<instance>-<module>`.
func (s ModuleSet) TableName(m Module) string {
	if n := s.Tables[m]; n != "" {
		return n
	}
	return "sluis-" + s.Instance + "-" + string(m)
}

// InternalPrefix is the module's own secrets: `/sluis/<instance>/internal/<module>`.
func (s ModuleSet) InternalPrefix(m Module) string {
	return InternalParameterPrefix(s.Instance) + "/" + string(m)
}

// rootOf is the installation's SSM root, `/sluis/<instance>`.
func (s ModuleSet) rootOf() string { return SSMRoot(s.Instance) }

// ExternalPrefix is the typed documents the module publishes:
// `/sluis/<instance>/external/<module>`.
func (s ModuleSet) ExternalPrefix(m Module) string {
	return ExternalParameterPrefix(s.Instance) + "/" + string(m)
}

// BlobPrefix is the module's key prefix in the blob bucket, with the trailing slash.
func BlobPrefix(m Module) string { return blobprefix.Root(string(m)) }

func (s ModuleSet) validate() error {
	if s.Instance == "" {
		return fmt.Errorf("sluispulumi: ModuleSet.Instance is required")
	}
	for m := range s.Tables {
		if !validModule(m) {
			return fmt.Errorf("sluispulumi: ModuleSet.Tables names unknown module %q", m)
		}
	}
	return nil
}

func validModule(m Module) bool {
	for _, k := range Modules() {
		if k == m {
			return true
		}
	}
	return false
}

// Role is the identity of one function: its name and the modules it hosts.
type Role struct {
	Name  string
	Hosts []Module
}

func (r Role) hosts(m Module) bool {
	for _, h := range r.Hosts {
		if h == m {
			return true
		}
	}
	return false
}

// The roles of v1.76: one per function. The issuer hosts the oidc module alone;
// each of the other modules is hosted by a function of its own. The github
// function also serves the issuer's token mint (D-v176-7 a), so there is no
// second, mint-only github role.
const (
	RoleIssuer     = "issuer"
	RoleCloudflare = "cloudflare"
	RoleBackup     = "backup"
	RoleRestore    = "restore"
	RoleGitHub     = "github"
	RoleSlack      = "slack"
	RoleGoogle     = "google"
	// RoleOutline is the Outline module's function. NewLambda makes it only for
	// an installation that declares `controllers.outline` (see hostsModule).
	RoleOutline = "outline"
)

// ModuleFunctions are the roles NewLambda makes a function for besides the
// issuer, in a fixed order. Each is the key of [LambdaArgs.Modules] and of the
// outputs Functions, RoleArns and AliasArns.
func ModuleFunctions() []string {
	return []string{RoleCloudflare, RoleGitHub, RoleGoogle, RoleOutline, RoleSlack}
}

// ModuleOfFunction is the module a function of [ModuleFunctions] serves.
func ModuleOfFunction(role string) Module {
	return Module(role)
}

// FunctionHosts is the modules a role of NewLambda hosts: the issuer's oidc, and
// a module function's own module.
func FunctionHosts(role string) []Module {
	if role == RoleIssuer {
		return []Module{ModuleOIDC}
	}
	return []Module{ModuleOfFunction(role)}
}

// roleTables are the modules whose table the role's function opens: the ones it
// hosts and the ones a cross-grant lets it read or write. The role's document
// lists exactly these in `ports.dynamodb.tables`, because the function's
// readiness probe describes every table the document lists, and a table the role
// has no grant on would fail it. The modules come in the order of [Modules].
func roleTables(role string) []Module {
	has := map[Module]bool{}
	hosts := FunctionHosts(role)
	if role == RoleBackup || role == RoleRestore {
		hosts = []Module{ModuleBackup}
	}
	for _, m := range hosts {
		has[m] = true
	}
	for _, g := range CrossGrants {
		if g.Role != role || g.Verb == CrossInvoke || g.Verb == CrossReadBlobs {
			continue
		}
		for _, m := range g.Modules {
			has[m] = true
		}
	}
	var out []Module
	for _, m := range Modules() {
		if has[m] {
			out = append(out, m)
		}
	}
	return out
}

// CrossVerb is what one role may do to a module it does not host.
type CrossVerb string

const (
	// CrossReadTable reads the module's table.
	CrossReadTable CrossVerb = "read-table"
	// CrossReadSnapshot reads the module's table by key (GetItem, Query) and
	// describes it, which the readiness probe does, and nothing else of it: no Scan, no write, no parameter and no parameter key.
	// With [CrossReadBlobs] it is the issuer's whole access to the google
	// module, the snapshots it serves (v1.76 D-v176-6 a).
	CrossReadSnapshot CrossVerb = "read-snapshot"
	// CrossReadBlobs reads the module's blob prefix and lists exactly that prefix.
	CrossReadBlobs CrossVerb = "read-blobs"
	// CrossInvoke invokes the functions CrossGrant.Functions names, through the
	// alias of the grant's caller class: `live-<class>`, never the function
	// unqualified and never $LATEST.
	CrossInvoke CrossVerb = "invoke"
	// CrossReadModule reads the table, the blobs and the parameters of the named
	// modules and nothing of the rest. It is [CrossReadAll] for a role that
	// reads a module or two: the shared key's policy (KeyPolicyArgs) grants
	// CrossReadAll roles every module's parameters and so does not know this
	// verb; a role with it is admitted to the key by a statement of its own.
	CrossReadModule CrossVerb = "read-module"
	// CrossReadAll reads the table, the blobs and the parameters of the module.
	CrossReadAll CrossVerb = "read-all"
	// CrossWriteAll writes them too.
	CrossWriteAll CrossVerb = "write-all"
)

// CrossGrant is one named grant between a role and the modules it does not host.
// It is data, so the docs and the tests read the same list the policies are
// rendered from.
type CrossGrant struct {
	Role string
	Verb CrossVerb
	// Modules are the modules a table, read-all or write-all grant is on.
	Modules []Module
	// Functions are the roles whose functions a [CrossInvoke] grant invokes, and
	// Class the caller class the invocation is made as: the grant is on the
	// alias `live-<Class>` of each.
	Functions []string
	Class     string
}

// CrossGrants are the grants beyond a role's own modules.
//
//   - the issuer reads the google snapshots only (table GetItem and Query, the
//     `google/` blobs): no github or slack table, no parameter and no parameter
//     key of any module but its own. Everything else a provider owns is asked of
//     its function. It invokes, as the class issuer, the functions that serve
//     sign-in and the token endpoint (github for a mint, google for
//     `identify`, cloudflare for a mint), and, as the class console, the
//     functions behind the console pages (github, slack, google, cloudflare,
//     outline and backup; v1.76 D-v176-1 b). It reads nothing of the outline
//     module: its console pages are the outline function's. Every invoke is on
//     the alias `live-<class>`, never the unqualified function;
//   - the backup role reads everything; the restore role writes everything and
//     is invokable only by the administrators the stack names (RestoreInvokeStatement).
var CrossGrants = []CrossGrant{
	{Role: RoleIssuer, Verb: CrossReadSnapshot, Modules: []Module{ModuleGoogle}},
	{Role: RoleIssuer, Verb: CrossInvoke, Class: ClassIssuer,
		Functions: []string{RoleCloudflare, RoleGitHub, RoleGoogle}},
	{Role: RoleIssuer, Verb: CrossInvoke, Class: ClassConsole,
		Functions: []string{RoleBackup, RoleCloudflare, RoleGoogle, RoleSlack, RoleGitHub, RoleOutline}},
	{Role: RoleIssuer, Verb: CrossReadBlobs, Modules: []Module{ModuleGoogle}},
	{Role: RoleBackup, Verb: CrossReadAll, Modules: Modules()},
	{Role: RoleRestore, Verb: CrossWriteAll, Modules: Modules()},
}

// ConsoleCallers are the roles whose function calls the issuer's console over
// HTTP and proves itself with its role's outbound web identity token
// (`sts:GetWebIdentityToken`, internal/consoleauth): the GitHub, Slack and
// Outline controllers, which ask the console who holds a group, or which roster
// to apply, on every pass. Their documents carry `console.auth.aws`
// (config.moduleBuilder.rosterOf). The role of each holds the token grant, on the
// console's audience, and no other module role does.
var ConsoleCallers = []string{RoleGitHub, RoleSlack, RoleOutline}

// ModuleCall is one call from a function to another, as the policies and the
// aliases are derived from it.
type ModuleCall struct {
	// Caller and Callee are roles.
	Caller, Callee string
	// Alias is the alias of the callee the call goes through, `live-<class>`;
	// empty for a call that is not a Lambda invocation (the controllers' HTTP
	// calls to the console).
	Alias string
	// WebIdentity is whether the caller authenticates to the callee with its
	// role's web identity token, so that its role needs sts:GetWebIdentityToken.
	WebIdentity bool
}

// ModuleCalls is the routing table, derived from [CrossGrants] (every Lambda
// invocation between functions) and [ConsoleCallers] (every call to the
// console). The callee's aliases, the callers' invoke grants and the callers'
// token grants are rendered from it and nothing else.
func ModuleCalls() []ModuleCall {
	var out []ModuleCall
	for _, g := range CrossGrants {
		if g.Verb != CrossInvoke {
			continue
		}
		for _, f := range g.Functions {
			out = append(out, ModuleCall{Caller: g.Role, Callee: f, Alias: CallerAlias(g.Class)})
		}
	}
	for _, c := range ConsoleCallers {
		out = append(out, ModuleCall{Caller: c, Callee: RoleIssuer, WebIdentity: true})
	}
	return out
}

// CallerClasses are the caller classes that call the function of the role, in
// the order of [CrossGrants]: the function has the alias `live-<class>` of each.
func CallerClasses(role string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range CrossGrants {
		if g.Verb != CrossInvoke || seen[g.Class] || !slices.Contains(g.Functions, role) {
			continue
		}
		seen[g.Class] = true
		out = append(out, g.Class)
	}
	return out
}

func capitalize(m Module) string {
	s := string(m)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func sortedModules(in []Module) []Module {
	out := append([]Module(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
