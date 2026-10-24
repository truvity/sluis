package auditpulumi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The lock guard of an archive bucket (ArchiveLockGuard) is written into the
// policy of every bucket the library manages. A preset's EXISTING bucket (no
// Create, no Adopt) is its owner's, and the library writes nothing to it, so a
// role with a broad S3 allow could delete a version of the archive or switch
// its lock off there unless the owner's own policy says otherwise. The library
// cannot write that policy, so it reads it before anything is deployed and says
// what is missing (audit 2026-10-18, A5).

// The values of ArchiveArgs.ExistingBucketLockGuard.
const (
	// LockGuardReport reads each existing bucket's policy and Object Lock
	// configuration and warns about what is missing. The default.
	LockGuardReport = "report"
	// LockGuardRequire refuses to deploy while an existing bucket's policy has no
	// statement equivalent to ArchiveLockGuard, or cannot be read. A bucket with
	// no Object Lock is still only reported: Object Lock is enabled when a bucket
	// is made, or through AWS support, and no apply can turn it on.
	LockGuardRequire = "require"
	// LockGuardOff does not look.
	LockGuardOff = "off"
)

// ArchiveGuardActions are what the lock guard denies to every principal but the
// break-glass roles.
var ArchiveGuardActions = []string{"s3:BypassGovernanceRetention", "s3:DeleteObjectVersion", "s3:PutBucketObjectLockConfiguration"}

func checkLockGuardMode(mode string) error {
	switch mode {
	case "", LockGuardReport, LockGuardRequire, LockGuardOff:
		return nil
	}
	return fmt.Errorf("auditpulumi: Archive.ExistingBucketLockGuard %q must be %q (the default), %q or %q",
		mode, LockGuardReport, LockGuardRequire, LockGuardOff)
}

// HasArchiveLockGuard says whether a bucket policy denies each of
// ArchiveGuardActions on the bucket and its objects to every principal, as
// ArchiveLockGuard does. A statement counts when it is a Deny to `*`, its
// actions and resources cover the action on the bucket (the lock
// configuration) or on its objects (the bypass and the version delete), and its
// condition at most exempts principals by aws:PrincipalArn (ArnNotLike,
// ArnNotEquals, StringNotLike, StringNotEquals), optionally for callers that are
// not AWS services. A Deny under any other condition (the TLS-only statement's
// aws:SecureTransport, say) does not: it denies only some requests.
func HasArchiveLockGuard(policy, bucketArn string) bool {
	return len(missingLockGuard(policy, bucketArn)) == 0
}

// missingLockGuard are the actions of ArchiveGuardActions the policy does not
// deny as the guard does; all of them for a policy that is not JSON.
func missingLockGuard(policy, bucketArn string) []string {
	var doc struct {
		Statement json.RawMessage
	}
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		return append([]string(nil), ArchiveGuardActions...)
	}
	var sts []map[string]any
	if err := json.Unmarshal(doc.Statement, &sts); err != nil {
		var one map[string]any
		if json.Unmarshal(doc.Statement, &one) != nil {
			return append([]string(nil), ArchiveGuardActions...)
		}
		sts = []map[string]any{one}
	}
	var missing []string
	for _, action := range ArchiveGuardActions {
		target := bucketArn + "/*"
		if action == "s3:PutBucketObjectLockConfiguration" {
			target = bucketArn
		}
		covered := false
		for _, st := range sts {
			if guardStatement(st) && anyGlob(stringList(st["Action"]), action, true) && anyGlob(stringList(st["Resource"]), target, false) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, action)
		}
	}
	return missing
}

// guardStatement is a Deny to every principal whose condition at most exempts
// principals by their ARN.
func guardStatement(st map[string]any) bool {
	if st["Effect"] != "Deny" || st["NotAction"] != nil || st["NotResource"] != nil || st["NotPrincipal"] != nil {
		return false
	}
	switch p := st["Principal"].(type) {
	case string:
		if p != "*" {
			return false
		}
	case map[string]any:
		if aws := stringList(p["AWS"]); len(p) != 1 || len(aws) != 1 || aws[0] != "*" {
			return false
		}
	default:
		return false
	}
	cond, _ := st["Condition"].(map[string]any)
	for op, v := range cond {
		keys, _ := v.(map[string]any)
		for k, val := range keys {
			switch {
			case strings.EqualFold(k, "aws:PrincipalArn") &&
				(op == "ArnNotLike" || op == "ArnNotEquals" || op == "StringNotLike" || op == "StringNotEquals"):
			case strings.EqualFold(k, "aws:PrincipalIsAWSService") && op == "Bool" && fmt.Sprint(val) == "false":
			default:
				return false
			}
		}
	}
	return true
}

// stringList is a policy element that is a string or a list of strings.
func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// anyGlob says whether one of the patterns (IAM's `*` and `?`) matches s.
// Actions compare without case, as IAM does.
func anyGlob(patterns []string, s string, fold bool) bool {
	for _, p := range patterns {
		if fold {
			p, s = strings.ToLower(p), strings.ToLower(s)
		}
		if glob(p, s) {
			return true
		}
	}
	return false
}

func glob(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := 0; i <= len(s); i++ {
				if glob(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}

// existingBuckets are the AWS buckets of the presets the library neither
// creates nor adopts, by name, with whether one of their presets is locked.
func (a *Args) existingBuckets() (names []string, locked map[string]bool, region map[string]string) {
	locked, region = map[string]bool{}, map[string]string{}
	for _, s := range a.stores { //nolint:gocritic // read-only configuration
		if s.managed() || s.external() {
			continue
		}
		if _, seen := region[s.Bucket]; !seen {
			names = append(names, s.Bucket)
			region[s.Bucket] = s.Region
		}
		locked[s.Bucket] = locked[s.Bucket] || s.Locked
	}
	sort.Strings(names)
	return names, locked, region
}

// checkExistingBuckets reads the policy and the Object Lock configuration of
// each existing bucket and reports what the library would have written into a
// bucket it manages: the lock guard, and for the attested preset Object Lock.
// With LockGuardRequire a policy without the guard, or one that cannot be read,
// is a refusal; Object Lock is only ever reported.
func checkExistingBuckets(ctx *pulumi.Context, a *Args, opts ...pulumi.InvokeOption) error {
	mode := a.Archive.ExistingBucketLockGuard
	if mode == LockGuardOff {
		return nil
	}
	names, locked, region := a.existingBuckets()
	var refusals []string
	for _, b := range names {
		var reg *string
		if region[b] != "" {
			reg = pulumi.StringRef(region[b])
		}
		gap := ""
		pol, err := s3.LookupBucketPolicy(ctx, &s3.LookupBucketPolicyArgs{Bucket: b, Region: reg}, opts...)
		switch {
		case err != nil && (absent(err) || strings.Contains(err.Error(), "NoSuchBucketPolicy")):
			gap = "has no bucket policy"
		case err != nil:
			gap = fmt.Sprintf("has a policy this deployment could not read (%v; it needs s3:GetBucketPolicy)", err)
		default:
			if m := missingLockGuard(pol.Policy, "arn:aws:s3:::"+b); len(m) > 0 {
				gap = "has a policy that does not deny " + strings.Join(m, ", ") + " to every principal but the break-glass roles"
			}
		}
		if gap != "" {
			msg := fmt.Sprintf("auditpulumi: the archive bucket %s %s. The library writes the lock guard only into a bucket it creates or "+
				"adopts; add ArchiveLockGuard(bucketArn, breakglassRoleArns) to this bucket's policy, or adopt the bucket "+
				"(PresetStorage.Adopt), so that a role with a broad S3 allow cannot delete a version of the archive or change its lock", b, gap)
			if mode == LockGuardRequire {
				refusals = append(refusals, msg)
			} else if err := ctx.Log.Warn(msg+" (Archive.ExistingBucketLockGuard: report)", nil); err != nil {
				return err
			}
		}
		if !locked[b] {
			continue
		}
		lock, err := s3.LookupBucketObjectLockConfiguration(ctx, &s3.LookupBucketObjectLockConfigurationArgs{Bucket: b, Region: reg}, opts...)
		state := ""
		switch {
		case err != nil && !denied(err):
			state = "has no Object Lock"
		case err != nil:
			state = fmt.Sprintf("has an Object Lock configuration this deployment could not read (%v)", err)
		case lock.ObjectLockEnabled != "Enabled":
			state = "has no Object Lock"
		}
		if state != "" {
			if err := ctx.Log.Warn(fmt.Sprintf("auditpulumi: the attested preset's archive bucket %s %s: its records are not held "+
				"against deletion by S3, only made detectable by their seals. Object Lock is enabled when a bucket is made, or through "+
				"AWS support for one that exists; see docs/guides/audit/operate/aws-turn-on-object-lock.md", b, state), nil); err != nil {
				return err
			}
		}
	}
	if len(refusals) > 0 {
		return fmt.Errorf("%s (Archive.ExistingBucketLockGuard: require)", strings.Join(refusals, "; "))
	}
	return nil
}
