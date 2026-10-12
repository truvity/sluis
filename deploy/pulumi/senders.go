package sluispulumi

import (
	"sort"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CreatedSenders are the ARNs a queue policy may name: the roles the stack
// creates (which the policy must be applied after, see [DeferredSenders]) and
// the old names kept by TransitionRoles (which exist already). A role the stack
// does not create, the restore function's when RoleArgs.Restore is false, is not
// in it: SQS refuses a policy that names a role that does not exist. Sorted,
// without repeats.
func (r RoleArgs) CreatedSenders() ([]string, error) {
	roles, err := r.FunctionRoles()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range roles {
		if roles[i].Created {
			out = append(out, roles[i].Arn)
			out = append(out, roles[i].Also...)
		}
	}
	return dedupe(sortedStrings(out)), nil
}

// DeferredSenders are the audit ingest queue's senders for a stack that makes
// the queue before the roles: SQS refuses a queue policy that names a role that
// does not exist, so each role the stack creates is an OUTPUT, resolved when the
// component that makes it is declared, and the policy that takes them as inputs
// is applied after the roles. The old names of a transition stay plain strings.
//
//	s, err := roles.DeferredSenders(ctx)         // before the queue
//	audit, err := auditpulumi.New(ctx, "audit", &auditpulumi.Args{Ingest: auditpulumi.IngestArgs{Senders: s.Inputs}})
//	l, err := sluispulumi.NewLambda(ctx, "sluis", args)
//	s.Lambda(l)                                  // after the roles are declared
//
// Every Created role must be resolved or the queue policy never applies: Reject
// releases them with an error, for the path where the stack stops early.
type DeferredSenders struct {
	// Inputs are the senders, for auditpulumi.IngestArgs.Senders.
	Inputs []pulumi.StringInput

	resolve map[string]func(pulumi.StringInput)
	reject  map[string]func(error)
}

// DeferredSenders are the queue senders of the installation as outputs; see [DeferredSenders].
func (r RoleArgs) DeferredSenders(ctx *pulumi.Context) (*DeferredSenders, error) {
	roles, err := r.FunctionRoles()
	if err != nil {
		return nil, err
	}
	d := &DeferredSenders{resolve: map[string]func(pulumi.StringInput){}, reject: map[string]func(error){}}
	for i := range roles {
		if !roles[i].Created {
			continue
		}
		in, resolve, reject := deferredArn(ctx)
		d.Inputs = append(d.Inputs, in)
		d.resolve[roles[i].Role], d.reject[roles[i].Role] = resolve, reject
		for _, old := range roles[i].Also {
			d.Inputs = append(d.Inputs, pulumi.String(old))
		}
	}
	return d, nil
}

// deferredArn is a role ARN that is not known yet: the input, and what resolves
// or releases it. Resolving twice is harmless.
func deferredArn(ctx *pulumi.Context) (in pulumi.StringInput, resolve func(pulumi.StringInput), reject func(error)) {
	out, resolveAny, rejectAny := ctx.NewOutput()
	done := false
	return out.ApplyT(func(v any) string { return v.(string) }).(pulumi.StringOutput),
		func(arn pulumi.StringInput) {
			if !done {
				done = true
				resolveAny(arn.ToStringOutput())
			}
		},
		func(err error) {
			if !done {
				done = true
				rejectAny(err)
			}
		}
}

// Lambda resolves the roles NewLambda creates: the issuer's and the module functions'.
func (d *DeferredSenders) Lambda(l *Lambda) {
	for role, resolve := range d.resolve {
		if role == RoleBackup || role == RoleRestore {
			continue
		}
		role := role
		resolve(l.RoleArns.ApplyT(func(m map[string]string) string { return m[role] }).(pulumi.StringOutput))
	}
}

// Backup resolves the backup role, when RoleArgs.Backup was set.
func (d *DeferredSenders) Backup(b *Backup) {
	if resolve, ok := d.resolve[RoleBackup]; ok {
		resolve(b.RoleArn)
	}
}

// Restore resolves the restore role, when RoleArgs.Restore was set.
func (d *DeferredSenders) Restore(x *Restore) {
	if resolve, ok := d.resolve[RoleRestore]; ok {
		resolve(x.RoleArn)
	}
}

// Reject releases every role not resolved yet with the error. Call it (deferred)
// when the stack may stop before it declares them.
func (d *DeferredSenders) Reject(err error) {
	roles := make([]string, 0, len(d.reject))
	for role := range d.reject {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		d.reject[role](err)
	}
}
