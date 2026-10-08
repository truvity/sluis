package profile

import (
	"fmt"
	"slices"
	"strings"
)

// CheckFrameworksLock holds a deployment's lock mode to the built-in framework profiles a
// profile is composed from: a framework profile whose framework demands an Object Lock
// stricter than the deployment's refuses the composition, as CheckLockMode
// does for the composed profile. The mode is spelled either way (NONE or none),
// so a Pulumi stack's NONE/GOVERNANCE/COMPLIANCE can be passed as it is.
//
// It lets a deployment's own configuration be refused before anything is
// built, without composing a whole profile document.
func CheckFrameworksLock(names []string, deployment string) error {
	mode := strings.ToLower(deployment)
	if LockRank(mode) < 0 {
		return fmt.Errorf("lock mode %q is not one of none, governance or compliance", deployment)
	}

	builtin, err := Builtin()
	if err != nil {
		return err
	}

	var weaker []string

	for _, name := range names {
		p, ok := builtin[name]
		if !ok {
			return fmt.Errorf("framework profile %q is not one of the built-in framework profiles", name)
		}

		if LockRank(p.Integrity.ObjectLockMode) > LockRank(mode) {
			weaker = append(weaker, name)
		}
	}

	if len(weaker) > 0 {
		return fmt.Errorf("framework profile(s) %s may demand an Object Lock stricter than %s, which this deployment has (framework profiles that demand none: %s)",
			strings.Join(weaker, ", "), mode, strings.Join(LockFreeFrameworks(builtin, mode), ", "))
	}

	return nil
}

// LockFreeFrameworks are the names of the framework profiles whose frameworks demand no
// stricter a lock than mode, sorted.
func LockFreeFrameworks(frameworks map[string]*Framework, mode string) []string {
	var out []string

	for name, p := range frameworks {
		if LockRank(p.Integrity.ObjectLockMode) <= LockRank(mode) {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return out
}
