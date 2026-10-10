package sluispulumi

import (
	"fmt"
	"slices"
	"testing"
)

// The maintenance flag is one record in each module's own table, in the
// partition MaintenancePartition. These tests read the rendered statements the
// way IAM does (an explicit Deny wins; a leading-keys condition is matched
// against the item's partition key) and hold three promises:
//
//   - only the restore role can write the flag, in any module's table;
//   - every role can read the flag of the modules it hosts, from its own table;
//   - no role reads a flag out of a table it is not granted for the flag's sake
//     (there is no shared maintenance grant on the backup table).

// iamAllows evaluates statements for one action on one resource whose item has
// the partition key leading.
func iamAllows(t *testing.T, st []map[string]any, action, resource, leading string) bool {
	t.Helper()
	allowed := false
	for _, s := range st {
		if !slices.Contains(strs(s["Action"]), action) || !slices.Contains(strs(s["Resource"]), resource) {
			continue
		}
		if cond, ok := s["Condition"].(map[string]any); ok && !conditionHolds(t, cond, leading) {
			continue
		}
		switch s["Effect"] {
		case "Deny":
			return false
		case "Allow":
			allowed = true
		}
	}
	return allowed
}

func conditionHolds(t *testing.T, cond map[string]any, leading string) bool {
	t.Helper()
	for op, v := range cond {
		if op != "ForAnyValue:StringEquals" && op != "ForAllValues:StringEquals" {
			t.Fatalf("a condition operator this test does not evaluate: %s", op)
		}
		for key, vals := range v.(map[string]any) {
			if key != "dynamodb:LeadingKeys" {
				t.Fatalf("a condition key this test does not evaluate: %s", key)
			}
			if !slices.Contains(strs(vals), leading) {
				return false
			}
		}
	}
	return true
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

var ddbWrites = []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem", "dynamodb:TransactWriteItems"}

func TestOnlyTheRestoreRoleCanWriteTheMaintenanceFlag(t *testing.T) {
	env := testModuleEnv()
	for _, role := range testRoles() {
		st, err := ModuleRoleStatements(env, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range Modules() {
			table := env.TableArns[m]
			for _, action := range ddbWrites {
				got := iamAllows(t, st, action, table, MaintenancePartition)
				if want := role.Name == RoleRestore && action != "dynamodb:BatchWriteItem" && action != "dynamodb:TransactWriteItems"; got != want {
					t.Errorf("role %s: %s of the flag in %s's table = %v, want %v", role.Name, action, m, got, want)
				}
			}
		}
	}
}

// Denying the flag must not cost a role its ordinary records: the deny names the
// partition and nothing else.
func TestTheDenyOfTheFlagLeavesTheRolesOwnRecordsWritable(t *testing.T) {
	env := testModuleEnv()
	for _, role := range testRoles() {
		st, err := ModuleRoleStatements(env, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range role.Hosts {
			for _, partition := range []string{"lease", "session", "token"} {
				if !iamAllows(t, st, "dynamodb:PutItem", env.TableArns[m], partition) {
					t.Errorf("role %s lost the write of %q in its own %s table", role.Name, partition, m)
				}
			}
		}
	}
}

func TestEveryRoleReadsTheFlagOfItsOwnModulesFromTheirTables(t *testing.T) {
	env := testModuleEnv()
	for _, role := range testRoles() {
		st, err := ModuleRoleStatements(env, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range role.Hosts {
			if !iamAllows(t, st, "dynamodb:GetItem", env.TableArns[m], MaintenancePartition) {
				t.Errorf("role %s cannot read the flag in its own %s table", role.Name, m)
			}
		}
	}
}

// There is no shared grant: a role that does not hold a module's table (as its
// own or by a named cross-grant) reads nothing of it, the flag included.
func TestNoRoleReadsAFlagOutsideItsOwnOrGrantedTables(t *testing.T) {
	env := testModuleEnv()
	granted := func(role Role) map[Module]bool {
		out := map[Module]bool{}
		for _, m := range role.Hosts {
			out[m] = true
		}
		for _, g := range CrossGrants {
			if g.Role == role.Name && g.Verb != CrossInvoke {
				for _, m := range g.Modules {
					out[m] = true
				}
			}
		}
		return out
	}
	for _, role := range testRoles() {
		st, err := ModuleRoleStatements(env, role)
		if err != nil {
			t.Fatal(err)
		}
		held := granted(role)
		for _, m := range Modules() {
			got := iamAllows(t, st, "dynamodb:GetItem", env.TableArns[m], MaintenancePartition)
			if got != held[m] {
				t.Errorf("role %s: read of the flag in %s's table = %v, want %v", role.Name, m, got, held[m])
			}
		}
	}
	// The cloudflare role, in particular, never touches the backup table.
	st, err := ModuleRoleStatements(env, Role{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}})
	if err != nil {
		t.Fatal(err)
	}
	if iamAllows(t, st, "dynamodb:GetItem", env.TableArns[ModuleBackup], MaintenancePartition) {
		t.Error("the cloudflare role reads the backup table")
	}
}

// A role named for a split-out module is denied the flag like any other.
func TestASplitOutRoleIsDeniedTheFlagToo(t *testing.T) {
	env := testModuleEnv()
	role := Role{Name: "google", Hosts: []Module{ModuleGoogle}}
	st, err := ModuleRoleStatements(env, role)
	if err != nil {
		t.Fatal(err)
	}
	if iamAllows(t, st, "dynamodb:PutItem", env.TableArns[ModuleGoogle], MaintenancePartition) {
		t.Error("a split-out role writes the flag")
	}
	if !iamAllows(t, st, "dynamodb:PutItem", env.TableArns[ModuleGoogle], "workspace") {
		t.Error("a split-out role cannot write its records")
	}
}
