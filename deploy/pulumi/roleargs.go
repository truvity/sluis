package sluispulumi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	"github.com/truvity/sluis/policy"
)

// RoleArgs is what the NAMES of an installation's function roles depend on, and
// nothing else: the installation's name, the function names, which module
// functions it declares and whether the Backup and Restore components are in the
// stack. A stack that exists before the sluis stack (the foundation stack, which
// holds the shared key's policy, or the audit stack, which holds the ingest
// queue's policy) builds a RoleArgs from the estate's shared configuration and
// asks it for the principals; it needs no storage, no tables, no package digest
// and no queue, which LambdaArgs validates.
//
// It answers what LambdaArgs.Senders, Roles, ViewerRoles and KeyPolicyStatements
// answer for the same names; [LambdaArgs.RoleArgs] converts the other way.
type RoleArgs struct {
	// Region, AccountID and Instance name the roles. Required.
	Region    string
	AccountID string
	Instance  string
	// Installation is that of LambdaArgs.Installation. It decides which module
	// functions exist (a module the installation renders no document for gets no
	// function) and which roles mint blobs from a preset. Optional: without it,
	// every module function is declared.
	Installation *sluisconfig.Installation
	// Blobs is StorageArgs.Blobs when the blobs are on an S3-compatible store; the
	// installation's module documents differ with it. Nil for a bucket.
	Blobs *ExternalBlobs

	// FunctionName, Modules[...].FunctionName, BackupFunctionName,
	// RestoreFunctionName, TableNames and TransitionRoles are those of
	// LambdaArgs. Of Modules, only FunctionName and Document are read.
	FunctionName        string
	Modules             map[string]ModuleArgs
	BackupFunctionName  string
	RestoreFunctionName string
	TableNames          map[Module]string
	TransitionRoles     []TransitionRole

	// Backup and Restore say that the stack creates the Backup and the Restore
	// component (NewBackup, NewRestore). Their roles are in [FunctionRole]s either
	// way (the shared key may name a role before it exists), but only a declared
	// one is [FunctionRole.Created], and so only a declared one is a sender the
	// audit queue's policy may name before it exists.
	Backup, Restore bool
}

// RoleArgs is the names of this installation's roles, as the Lambda was asked
// for them. Backup and Restore are not known to the Lambda: set them on the
// result when the stack creates those components.
func (a *LambdaArgs) RoleArgs() RoleArgs {
	return RoleArgs{
		Region: a.Region, AccountID: a.AccountID, Instance: a.Instance,
		Installation: a.Installation, Blobs: blobsOf(a.Storage),
		FunctionName: a.FunctionName, Modules: a.Modules,
		BackupFunctionName: a.BackupFunctionName, RestoreFunctionName: a.RestoreFunctionName,
		TableNames: a.TableNames, TransitionRoles: a.TransitionRoles,
	}
}

// RoleArgs is the names of the roles of the Lambda: those of the args it was made from.
func (l *Lambda) RoleArgs() RoleArgs { return l.resolved.RoleArgs() }

func blobsOf(s *StorageGrant) *ExternalBlobs {
	if s == nil {
		return nil
	}
	return s.External
}

// FunctionRole is one function's role as an estate names it: in a key policy, an
// audit queue's policy or an access policy.
type FunctionRole struct {
	// Role is the logical role: [RoleIssuer], [RoleBackup], [RoleRestore] or one
	// of [ModuleFunctions]. Name is the IAM role name (and the function's name),
	// Arn its ARN. Also are the ARNs it keeps under a [TransitionRole]: roles that
	// exist already.
	Role string
	Name string
	Arn  string
	Also []string
	// Created says that the stack creates the role: NewLambda makes the issuer's
	// and the module functions' that host a module (a module the installation
	// renders no document for has no function), and the stack makes the backup
	// and restore roles only when RoleArgs.Backup and Restore say so. SQS refuses
	// a queue policy that names a role that does not exist, so a policy that names
	// a Created role is applied after it; one that names a role that is not
	// Created names it only if it exists some other way.
	Created bool
	// Viewer says that the role reads other modules' records without hosting them
	// (the issuer's console pages): the roles an access policy's viewer matchers name.
	Viewer bool
	// account is the role's account, for Matcher.
	account string
}

// Matcher is the access policy's matcher for the role: its account, its name and
// the path `/`.
func (f FunctionRole) Matcher() policy.Matcher {
	return policy.Matcher{AWS: &policy.AWSMatcher{Account: f.account, Role: f.Name, Path: "/"}}
}

// resolve is the args as the role lists need them: the installation rendered
// (so that the modules it declares and the blobs they mint from are known) and
// the names defaulted and validated, with nothing the roles do not read.
func (r RoleArgs) resolve() (LambdaArgs, error) {
	off := false
	a := LambdaArgs{
		Region: r.Region, AccountID: r.AccountID, Instance: r.Instance,
		Installation: r.Installation, FunctionName: r.FunctionName, Modules: r.Modules,
		BackupFunctionName: r.BackupFunctionName, RestoreFunctionName: r.RestoreFunctionName,
		TableNames: r.TableNames, TransitionRoles: r.TransitionRoles,
		// Stand-ins for what the rendering reads and the roles do not: the grants'
		// values, and no audit queue.
		Storage: &StorageGrant{External: r.Blobs},
		State:   &StateGrant{Tables: map[Module]pulumi.StringInput{}},
		Audit:   &AuditArgs{Enabled: &off},
	}
	// The module functions' zips are named after the issuer's; the roles read no zip.
	a.Package = "sluis-issuer_0.0.0_linux_arm64.zip"
	if in := r.Installation; in != nil && in.AWS != nil && in.AWS.AuditQueueURL != "" {
		// The installation names the queue it sends to; the roles do not read it.
		a.Audit = &AuditArgs{Use: &AuditUse{QueueURL: in.AWS.AuditQueueURL}}
	}
	if r.Blobs == nil {
		a.Storage.BucketArn = pulumi.String(arnPrefix + "s3:::placeholder")
	}
	if a.Region == "" || a.AccountID == "" || a.Instance == "" {
		return a, errors.New("sluispulumi: RoleArgs: Region, AccountID and Instance are required to name the roles")
	}
	for _, m := range Modules() {
		table := ModuleSet{Instance: a.Instance, Tables: a.TableNames}.TableName(m)
		a.State.Tables[m] = pulumi.String(arnPrefix + "dynamodb:" + a.Region + ":" + a.AccountID + ":table/" + table)
	}
	if a.Installation != nil {
		var err error
		if a, err = a.withInstallation(); err != nil {
			return a, err
		}
	}
	if err := a.validateTables(); err != nil {
		return a, err
	}
	if !validInstance(a.Instance) {
		return a, fmt.Errorf("sluispulumi: RoleArgs.Instance %q is lower-case letters, digits and dashes, at most 32, and not private or export", a.Instance)
	}
	if a.FunctionName == "" {
		a.FunctionName = "sluis-" + a.Instance + "-" + RoleIssuer
	}
	if !functionNamePattern.MatchString(a.FunctionName) {
		return a, fmt.Errorf("sluispulumi: RoleArgs.FunctionName %q is letters, digits, - and _, at most 64", a.FunctionName)
	}
	if a.RestoreFunctionName != "" && !functionNamePattern.MatchString(a.RestoreFunctionName) {
		return a, fmt.Errorf("sluispulumi: RoleArgs.RestoreFunctionName %q is letters, digits, - and _, at most 64", a.RestoreFunctionName)
	}
	if err := a.validateTransitionRoles(); err != nil {
		return a, err
	}
	if err := a.validateModules(); err != nil {
		return a, err
	}
	return a, nil
}

// FunctionRoles are the roles of every function of the installation, with their
// names, ARNs and whether the stack creates them. It is the one list an access
// policy's matchers, a queue policy's senders and a key policy's principals are
// derived from; an estate does not spell a default function name.
func (r RoleArgs) FunctionRoles() ([]FunctionRole, error) {
	a, err := r.resolve()
	if err != nil {
		return nil, err
	}
	return a.functionRolesOf(r.Backup, r.Restore), nil
}

func (a *LambdaArgs) functionRolesOf(backup, restore bool) []FunctionRole {
	var out []FunctionRole
	frs := a.functionRoles()
	for i := range frs {
		fr := &frs[i]
		f := FunctionRole{Role: fr.role.Name, Name: fr.name, Arn: fr.arn, Also: fr.also, Viewer: fr.readsOnly, account: a.AccountID}
		switch fr.role.Name {
		case RoleIssuer:
			f.Created = true
		case RoleBackup:
			f.Created = backup
		case RoleRestore:
			f.Created = restore
		default:
			f.Created = a.hostsModule(fr.role.Name)
		}
		out = append(out, f)
	}
	return out
}

// Senders are the ARNs of every function's role, as LambdaArgs.Senders: the
// principals of a key policy. Some may not exist yet; see [RoleArgs.CreatedSenders]
// for a queue policy.
func (r RoleArgs) Senders() ([]string, error) {
	a, err := r.resolve()
	if err != nil {
		return nil, err
	}
	return a.senders()
}

// Roles are the roles as the shared key's policy names them, as LambdaArgs.Roles.
func (r RoleArgs) Roles() ([]KeyRole, error) {
	a, err := r.resolve()
	if err != nil {
		return nil, err
	}
	return a.keyRoles()
}

// ViewerRoles are the ARNs of the roles that read modules' records without
// hosting them, as LambdaArgs.ViewerRoles.
func (r RoleArgs) ViewerRoles() ([]string, error) {
	a, err := r.resolve()
	if err != nil {
		return nil, err
	}
	return a.viewerRoles()
}

// ViewerMatchers are the access policy matchers of the viewer roles (the same
// roles as [RoleArgs.ViewerRoles], the old names of a transition included): add
// them to the group that may read the console. A function that reads nobody's
// records, a controller's for one, is [FunctionRole.Matcher] of the role you pick
// out of [RoleArgs.FunctionRoles] by its Role.
func (r RoleArgs) ViewerMatchers() ([]policy.Matcher, error) {
	roles, err := r.FunctionRoles()
	if err != nil {
		return nil, err
	}
	var out []policy.Matcher
	for i := range roles {
		if !roles[i].Viewer {
			continue
		}
		out = append(out, roles[i].Matcher())
		for _, old := range roles[i].Also {
			f := roles[i]
			f.Name = old[strings.LastIndex(old, "/")+1:]
			out = append(out, f.Matcher())
		}
	}
	return out, nil
}

// KeyPolicyStatements are the statements of the shared key's policy for every
// role of the installation, as LambdaArgs.KeyPolicyStatements.
func (r RoleArgs) KeyPolicyStatements(o KeyPolicyOptions) ([]map[string]any, error) {
	a, err := r.resolve()
	if err != nil {
		return nil, err
	}
	return a.keyPolicyStatements(o)
}
