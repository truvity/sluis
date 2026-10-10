package sluispulumi

import (
	"fmt"
	"sort"
	"strings"
)

// Module is one of the parts of a sluis installation that keeps its own
// state: its own table, its own SSM paths (`internal/<module>/...` and
// `external/<module>/...`) and its own blob prefix. A function hosts one or
// more modules, and its role is the union of what those modules need, so that
// splitting a module out of a function later only removes statements from the
// role it leaves.
type Module string

// The modules of layout v5.
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
)

// Modules is every module, in a fixed order.
func Modules() []Module {
	return []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleCloudflare, ModuleGoogle, ModuleBackup}
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
func BlobPrefix(m Module) string { return string(m) + "/" }

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

// The roles of v1.75. Until a module is split out, the issuer hosts github,
// slack and google besides oidc; the Cloudflare minter is its own function.
const (
	RoleIssuer     = "issuer"
	RoleCloudflare = "cloudflare"
	RoleBackup     = "backup"
	RoleRestore    = "restore"
)

// CrossVerb is what one role may do to a module it does not host.
type CrossVerb string

const (
	// CrossReadTable reads the module's table.
	CrossReadTable CrossVerb = "read-table"
	// CrossInvoke invokes the function that hosts the module.
	CrossInvoke CrossVerb = "invoke"
	// CrossReadAll reads the table, the blobs and the parameters of the module.
	CrossReadAll CrossVerb = "read-all"
	// CrossWriteAll writes them too.
	CrossWriteAll CrossVerb = "write-all"
)

// CrossGrant is one named grant between a role and the modules it does not host.
// It is data, so the docs and the tests read the same list the policies are
// rendered from.
type CrossGrant struct {
	Role    string
	Verb    CrossVerb
	Modules []Module
}

// CrossGrants are the grants beyond a role's own modules.
//
//   - the issuer reads the google, github and slack tables (the console writes
//     the records the issuer reads) and invokes the functions that host them;
//   - the backup role reads everything; the restore role writes everything and
//     is invokable only by the administrators the stack names (RestoreInvokeStatement).
var CrossGrants = []CrossGrant{
	{Role: RoleIssuer, Verb: CrossReadTable, Modules: []Module{ModuleGoogle, ModuleGitHub, ModuleSlack}},
	{Role: RoleIssuer, Verb: CrossInvoke, Modules: []Module{ModuleGoogle, ModuleGitHub, ModuleSlack, ModuleCloudflare}},
	{Role: RoleBackup, Verb: CrossReadAll, Modules: Modules()},
	{Role: RoleRestore, Verb: CrossWriteAll, Modules: Modules()},
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
