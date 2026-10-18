package sluispulumi

import (
	"errors"
	"fmt"
	"slices"
)

// TransitionRole keeps the old IAM role name of a function whose role is renamed
// (the issuer, `sluis` to `sluis-<instance>-issuer`, in v1.76). The renamed role
// is created before the old one is deleted (D-v176-3 a); while both exist the
// key policy admits both names as the same logical role and the audit queue
// accepts records from both. Remove the entry once nothing runs under the old name.
type TransitionRole struct {
	// Role is the logical role renamed: [RoleIssuer], [RoleBackup],
	// [RoleRestore] or one of [ModuleFunctions].
	Role string
	// OldName is the old IAM role name (no path, no ARN). It is not the new name.
	OldName string
}

// functionRole is one function's role as the library names it: the logical role,
// the IAM role name and ARN, and the ARNs it keeps under a transition.
type functionRole struct {
	role       Role
	name       string
	arn        string
	also       []string
	readsOnly  bool // reads other modules' records or blobs without hosting them
	readModule []Module
}

func (a *LambdaArgs) validateTransitionRoles() error {
	known := append([]string{RoleIssuer, RoleBackup, RoleRestore}, ModuleFunctions()...)
	seen := map[string]bool{}
	var errs []error
	for _, t := range a.TransitionRoles {
		switch {
		case !slices.Contains(known, t.Role):
			errs = append(errs, fmt.Errorf("TransitionRoles names role %q, which is not one of %v", t.Role, known))
		case !functionNamePattern.MatchString(t.OldName):
			errs = append(errs, fmt.Errorf("TransitionRoles[%s].OldName %q is letters, digits, - and _, at most 64", t.Role, t.OldName))
		case seen[t.OldName]:
			errs = append(errs, fmt.Errorf("TransitionRoles names the old role %q twice", t.OldName))
		}
		seen[t.OldName] = true
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sluispulumi: LambdaArgs: %w", err)
	}
	return nil
}

func (a *LambdaArgs) restoreFunctionName() string {
	if a.RestoreFunctionName != "" {
		return a.RestoreFunctionName
	}
	return "sluis-" + a.Instance + "-" + RoleRestore
}

// functionRoles are the roles of every function of the installation: the issuer,
// the module functions (layout v5) and the Backup component's backup and
// restore. They are derived from [FunctionHosts] and [CrossGrants], the data the
// policies are rendered from, so a role added there is a role here.
func (a *LambdaArgs) functionRoles() []functionRole {
	named := []struct{ role, name string }{{RoleIssuer, a.FunctionName}}
	if a.hasModuleFunctions() {
		for _, r := range ModuleFunctions() {
			named = append(named, struct{ role, name string }{r, a.moduleFunctionName(r)})
		}
		named = append(named,
			struct{ role, name string }{RoleBackup, a.backupFunctionName()},
			struct{ role, name string }{RoleRestore, a.restoreFunctionName()})
	}
	var out []functionRole
	for _, n := range named {
		fr := functionRole{role: Role{Name: n.role, Hosts: FunctionHosts(n.role)}, name: n.name, arn: a.roleArnOf(n.name)}
		if n.role == RoleBackup || n.role == RoleRestore {
			fr.role.Hosts = []Module{ModuleBackup}
		}
		for _, g := range CrossGrants {
			if g.Role != n.role {
				continue
			}
			switch g.Verb {
			case CrossReadModule:
				fr.readModule = append(fr.readModule, g.Modules...)
				fr.readsOnly = true
			case CrossReadTable, CrossReadSnapshot, CrossReadBlobs:
				fr.readsOnly = true
			}
		}
		for _, t := range a.TransitionRoles {
			if t.Role == n.role {
				fr.also = append(fr.also, a.roleArnOf(t.OldName))
			}
		}
		out = append(out, fr)
	}
	return out
}

// roleSet is the resolved args' roles, validated.
func (a *LambdaArgs) roleSet() ([]functionRole, error) {
	if a.Region == "" || a.AccountID == "" || a.Instance == "" {
		return nil, errors.New("sluispulumi: Region, AccountID and Instance are required to name the roles")
	}
	return a.functionRoles(), nil
}

// Senders are the ARNs of the roles that emit audit records and OTLP: every
// function's role, the old names kept by TransitionRoles included. An estate
// feeds the audit ingest's senders and the OTLP writers from this, not from a
// list of its own. Sorted, without repeats.
func (a *LambdaArgs) senders() ([]string, error) {
	rs, err := a.roleSet()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range rs {
		r := &rs[i]
		out = append(out, r.arn)
		out = append(out, r.also...)
	}
	return dedupe(sortedStrings(out)), nil
}

// keyRoles are the roles as the shared key's policy names them.
func (a *LambdaArgs) keyRoles() ([]KeyRole, error) {
	rs, err := a.roleSet()
	if err != nil {
		return nil, err
	}
	var out []KeyRole
	for i := range rs {
		r := &rs[i]
		kr := KeyRole{Role: r.role, Arn: r.arn, AlsoArns: r.also, ReadModules: r.readModule}
		out = append(out, kr)
	}
	return out, nil
}

// viewerRoles are the ARNs of the roles that read modules' records or blobs
// without hosting them (the issuer's console pages):
// the roles an estate's viewer matchers name. Backup and restore read or write
// everything and are not viewers.
func (a *LambdaArgs) viewerRoles() ([]string, error) {
	rs, err := a.roleSet()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range rs {
		r := &rs[i]
		if r.readsOnly {
			out = append(out, r.arn)
			out = append(out, r.also...)
		}
	}
	return dedupe(sortedStrings(out)), nil
}

// KeyPolicyOptions are what the estate adds to the statements the library
// renders for its own roles.
type KeyPolicyOptions struct {
	// ExternalReaders, Admins, Breakglass, KeyAdmins and DenyOtherActions are those
	// of KeyPolicyArgs. KeyAdmins or Breakglass is required.
	ExternalReaders  []string
	Admins           []string
	Breakglass       []string
	KeyAdmins        []string
	DenyOtherActions bool
	// ReaderStatements are the key statements of [ExternalReaderRole]s
	// (ExternalReaderRole.KeyStatement), merged after the library's.
	ReaderStatements []map[string]any
	// BackupArchive and RestoreArchive are those of KeyPolicyArgs.
	//
	// Deprecated: accepted and ignored; the archive uses no KMS key.
	BackupArchive, RestoreArchive bool
}

func (a *LambdaArgs) keyPolicyStatements(o KeyPolicyOptions) ([]map[string]any, error) {
	roles, err := a.keyRoles()
	if err != nil {
		return nil, err
	}
	st, err := SluisKeyPolicyStatements(KeyPolicyArgs{
		Region: a.Region, Account: a.AccountID, Modules: ModuleSet{Instance: a.Instance, Tables: a.TableNames}, Layout: a.Layout,
		Roles: roles, ExternalReaders: o.ExternalReaders, Admins: o.Admins, Breakglass: o.Breakglass, KeyAdmins: o.KeyAdmins,
		DenyOtherActions: o.DenyOtherActions,
		BackupArchive:    o.BackupArchive, RestoreArchive: o.RestoreArchive,
	})
	if err != nil {
		return nil, err
	}
	st = append(st, o.ReaderStatements...)
	if err := uniqueSids(st); err != nil {
		return nil, err
	}
	return st, nil
}

// withResolved runs f on the validated args.
func resolvedOf[T any](a *LambdaArgs, f func(*LambdaArgs) (T, error)) (T, error) {
	r, err := a.validate()
	if err != nil {
		var zero T
		return zero, err
	}
	return f(&r)
}

// Senders is (*LambdaArgs).senders. It validates the args first, so the estate
// can call it before the component exists (the audit queue and the key are made first).
func (a *LambdaArgs) Senders() ([]string, error) { return resolvedOf(a, (*LambdaArgs).senders) }

// Roles are the roles of the installation as the shared key's policy names them
// (KeyRole): the issuer, each module function, the backup and the restore, with
// the modules each hosts or reads and the old names kept by TransitionRoles.
func (a *LambdaArgs) Roles() ([]KeyRole, error) { return resolvedOf(a, (*LambdaArgs).keyRoles) }

// ViewerRoles are the ARNs of the roles that read modules' records without
// hosting them.
func (a *LambdaArgs) ViewerRoles() ([]string, error) { return resolvedOf(a, (*LambdaArgs).viewerRoles) }

// KeyPolicyStatements are the statements of the shared key's policy for every
// role of the installation (SluisKeyPolicyStatements over Roles), with the
// reader statements the estate passes merged. The roles are named by ARN, which
// is derived from the names, so the key can be made before the functions.
func (a *LambdaArgs) KeyPolicyStatements(o KeyPolicyOptions) ([]map[string]any, error) {
	return resolvedOf(a, func(r *LambdaArgs) ([]map[string]any, error) { return r.keyPolicyStatements(o) })
}

// Senders is the component's audit senders: those of the args it was made from.
func (l *Lambda) Senders() ([]string, error) { return l.resolved.senders() }

// Roles is the component's key roles: those of the args it was made from.
func (l *Lambda) Roles() ([]KeyRole, error) { return l.resolved.keyRoles() }

// ViewerRoles is the component's viewer role ARNs: those of the args it was made from.
func (l *Lambda) ViewerRoles() ([]string, error) { return l.resolved.viewerRoles() }

// KeyPolicyStatements is the component's key policy statements: those of the args it was made from.
func (l *Lambda) KeyPolicyStatements(o KeyPolicyOptions) ([]map[string]any, error) {
	return l.resolved.keyPolicyStatements(o)
}
