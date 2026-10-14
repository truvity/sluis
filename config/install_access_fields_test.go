package config_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
	"github.com/truvity/sluis/policy"
)

// deploymentOnly are the fields of the access document's client that describe
// the relying party's own deployment. They take no part in the policy.
var deploymentOnly = map[string]bool{
	"Clients.SecretKey": true, "Clients.Hostname": true, "Clients.Prefix": true,
	"Clients.Mount": true, "Clients.Cluster": true, "Clients.Proxy": true, "Clients.Deliver": true,
}

// fill sets every field reachable from v to a non-zero value: a slice or map
// holds one element, a string is unique to its path.
func fill(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("s-" + path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int64: // policy.Duration
		v.SetInt(int64(time.Minute))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), path)
	case reflect.Struct:
		for i := range v.NumField() {
			// `groups: all` and a list exclude each other; the list is filled and
			// the flag is a mutation.
			if v.Type() == reflect.TypeFor[policy.GroupsOverride]() && v.Type().Field(i).Name == "All" {
				continue
			}
			fill(v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), path)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, path+".key")
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, path+".value")
		v.SetMapIndex(k, e)
	case reflect.Interface:
		// Free-form deployment values; deploymentOnly lists them.
	default:
		panic(fmt.Sprintf("fill: %s is a %s", path, v.Kind()))
	}
}

// mutations are the single-field edits of a filled value: one per leaf, each
// making the leaf different from what fill wrote.
func mutations(v reflect.Value, path string, yield func(path string, edit func())) {
	switch v.Kind() {
	case reflect.String:
		yield(path, func() { v.SetString(v.String() + "-edited") })
	case reflect.Bool:
		yield(path, func() { v.SetBool(!v.Bool()) })
	case reflect.Int64:
		yield(path, func() { v.SetInt(v.Int() + int64(time.Second)) })
	case reflect.Pointer:
		mutations(v.Elem(), path, yield)
	case reflect.Struct:
		for i := range v.NumField() {
			mutations(v.Field(i), path+"."+v.Type().Field(i).Name, yield)
		}
	case reflect.Slice:
		mutations(v.Index(0), path, yield)
	case reflect.Map:
		k := v.MapKeys()[0]
		e := reflect.New(v.Type().Elem()).Elem()
		e.Set(v.MapIndex(k))
		mutations(e, path+".value", func(p string, edit func()) {
			yield(p, func() { edit(); v.SetMapIndex(k, e) })
		})
		yield(path+".key", func() {
			v.SetMapIndex(k, reflect.Value{})
			v.SetMapIndex(reflect.ValueOf("renamed").Convert(v.Type().Key()), e)
		})
	case reflect.Interface:
	default:
		panic(fmt.Sprintf("mutations: %s is a %s", path, v.Kind()))
	}
}

func filled() policy.Access {
	var a policy.Access
	fill(reflect.ValueOf(&a).Elem(), "")
	return a
}

func section(t *testing.T, a policy.Access) string {
	t.Helper()
	// A filled value is not a valid policy; FromAccess only reshapes it.
	got, err := config.FromAccess(a, config.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(got.Policy)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestFromAccessLosesNoField edits each leaf of a fully filled access part,
// one at a time, and requires the section to change. A field added to
// policy.Access with no place in the result fails here, by name, until it is
// mapped or listed in deploymentOnly.
func TestFromAccessLosesNoField(t *testing.T) {
	base := section(t, filled())
	probe := filled()
	var paths []string
	mutations(reflect.ValueOf(&probe).Elem(), "", func(path string, _ func()) { paths = append(paths, path) })
	if len(paths) < 60 {
		t.Fatalf("only %d fields walked", len(paths))
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			a := filled()
			found := false
			mutations(reflect.ValueOf(&a).Elem(), "", func(p string, edit func()) {
				if p == path && !found {
					found = true
					edit()
				}
			})
			if !found {
				t.Fatal("path not found")
			}
			changed := section(t, a) != base
			listed := deploymentOnly[trimRoot(path)]
			switch {
			case listed && changed:
				t.Errorf("%s is listed as deployment-only but reaches the policy", path)
			case !listed && !changed:
				t.Errorf("%s is lost: editing it does not change the section", path)
			}
		})
	}
}

func trimRoot(path string) string {
	if len(path) > 0 && path[0] == '.' {
		return path[1:]
	}
	return path
}

// TestEveryPolicyTableHasASource requires a decision for each table of the
// policy: it comes from the access part, from the bootstrap, or is set by
// FromAccess itself.
func TestEveryPolicyTableHasASource(t *testing.T) {
	source := map[string]string{
		"Version": "set to 1", "Vocabulary": "access", "Groups": "access, bootstrap", "Claims": "Bootstrap.Claims",
		"Lifetimes": "access", "Resources": "access", "ClientDocuments": "access", "Clients": "access, bootstrap",
		"GitHub": "access", "People": "access", "Slack": "access",
	}
	typ := reflect.TypeFor[policy.Policy]()
	var fields []string
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i).Name)
	}
	for _, f := range fields {
		if _, ok := source[f]; !ok {
			t.Errorf("policy.Policy.%s has no source in FromAccess: map it, or carry it in Bootstrap", f)
		}
	}
	for f := range source {
		if !slices.Contains(fields, f) {
			t.Errorf("%s is no longer a table of policy.Policy", f)
		}
	}
}
