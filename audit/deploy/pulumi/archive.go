package auditpulumi

import (
	"errors"
	"fmt"
)

// checkAWSArchive holds an archive on an AWS S3 bucket the library creates to
// what that bucket can be.
func (c *Args) checkAWSArchive() error {
	ar := &c.Archive
	switch ar.ObjectLockMode {
	case None, Governance, Compliance:
	default:
		return fmt.Errorf("auditpulumi: Archive.ObjectLockMode %q must be NONE, GOVERNANCE or COMPLIANCE", ar.ObjectLockMode)
	}
	if ar.ObjectLockMode == Compliance && !ar.AcknowledgeCompliance {
		return errors.New("auditpulumi: Archive.ObjectLockMode is COMPLIANCE and Archive.AcknowledgeCompliance is false: " +
			"compliance retention cannot be shortened by anyone, including the account's root, and a retention wrong in the long " +
			"direction is paid for until it expires. Run the governance trial first, sign off the retentions, then set " +
			"AcknowledgeCompliance on a NEW bucket (docs/decisions/0023-archive-retention-and-lifecycle.md)")
	}
	if ar.DefaultRetentionDays < 0 {
		return errors.New("auditpulumi: Archive.DefaultRetentionDays is not negative")
	}
	if ar.ObjectLockMode != None && ar.DefaultRetentionDays == 0 {
		return fmt.Errorf("auditpulumi: Archive.DefaultRetentionDays is required with Archive.ObjectLockMode %s: "+
			"a lock with no default rule leaves an object put without its own retention unprotected (set it, as the floor)", ar.ObjectLockMode)
	}
	if ar.ObjectLockMode == None && ar.DefaultRetentionDays > 0 {
		return errors.New("auditpulumi: Archive.DefaultRetentionDays needs a lock: Archive.ObjectLockMode is NONE")
	}
	switch ar.Encryption {
	case "":
		ar.Encryption = EncryptionKMS
	case EncryptionKMS, EncryptionS3, EncryptionAWSManaged:
	default:
		return fmt.Errorf("auditpulumi: Archive.Encryption %q must be %q (the default), %q or %q",
			ar.Encryption, EncryptionKMS, EncryptionAWSManaged, EncryptionS3)
	}
	if ar.KeyArn != "" {
		if ar.Encryption != EncryptionKMS {
			return fmt.Errorf("auditpulumi: Archive.KeyArn is only for Archive.Encryption %q, not %q", EncryptionKMS, ar.Encryption)
		}
		if !keyArn.MatchString(ar.KeyArn) {
			return fmt.Errorf("auditpulumi: Archive.KeyArn %q is not a KMS key ARN (arn:<partition>:kms:<region>:<account>:key/<id>); "+
				"an alias ARN cannot be granted in IAM", ar.KeyArn)
		}
	}
	switch {
	case c.Keys.Archive != "" && ar.Encryption != EncryptionKMS:
		return fmt.Errorf("auditpulumi: Keys.Archive is only for Archive.Encryption %q, not %q: the other encryptions use no key of yours",
			EncryptionKMS, ar.Encryption)
	case c.Keys.Archive != "" && ar.KeyArn != "":
		return errors.New("auditpulumi: Keys.Archive and Archive.KeyArn both name the archive key: use Keys.Archive (by alias)")
	case c.Keys.Archive == "" && ar.KeyArn == "" && ar.Encryption == EncryptionKMS:
		return errors.New("auditpulumi: Keys.Archive is required with Archive.Encryption \"kms\": the library creates no key, so name the " +
			"estate's archive key by alias (alias/<name>), or choose Archive.Encryption \"aws-managed\" or \"s3\"")
	}
	if len(ar.Profiles) == 0 {
		return errors.New("auditpulumi: Archive.Profiles is required: a lifecycle rule is written for each profile's prefix (or give Writer.DeploymentYAML, whose profiles they are)")
	}
	if err := checkArchiveProfiles(ar.Profiles); err != nil {
		return err
	}
	setInt(&ar.GlacierIRDays, 30)
	setInt(&ar.DeepArchiveDays, 365)
	if ar.DeepArchiveDays <= ar.GlacierIRDays {
		return fmt.Errorf("auditpulumi: Archive.DeepArchiveDays (%d) must be after GlacierIRDays (%d)", ar.DeepArchiveDays, ar.GlacierIRDays)
	}
	return nil
}

func checkArchiveProfiles(profiles []string) error {
	seen := map[string]bool{}
	for _, p := range profiles {
		if err := CheckProfile(p); err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("auditpulumi: Archive.Profiles names %q twice", p)
		}
		seen[p] = true
	}
	return nil
}

// external reports whether the archive is on an S3-compatible store.
func (c *Args) external() bool { return c.Archive.Endpoint != "" }

// checkExternalArchive holds an archive on an S3-compatible store to what such
// a store gives: the bucket, its lifecycle, its encryption and its lock are the
// store's, not the library's.
func (c *Args) checkExternalArchive() error {
	ar := &c.Archive
	if err := checkEndpoint(ar.Endpoint); err != nil {
		return err
	}
	// The preset has already refused a lock (applyPreset); this holds what it
	// leaves: settings of an AWS bucket.
	for field, set := range map[string]bool{
		"Archive.Encryption":            ar.Encryption != "",
		"Archive.KeyArn":                ar.KeyArn != "",
		"Keys.Archive":                  c.Keys.Archive != "",
		"Archive.DefaultRetentionDays":  ar.DefaultRetentionDays != 0,
		"Archive.AcknowledgeCompliance": ar.AcknowledgeCompliance,
		"Archive.GlacierIRDays":         ar.GlacierIRDays != 0,
		"Archive.DeepArchiveDays":       ar.DeepArchiveDays != 0,
	} {
		if set {
			return fmt.Errorf("auditpulumi: %s is set with Archive.Endpoint: it is a setting of an AWS S3 bucket, and this archive is on "+
				"the store at %s, whose encryption, lifecycle and lock are its own", field, ar.Endpoint)
		}
	}
	if c.Observe != nil || c.Query != nil || c.ArchiveWriter != nil {
		return errors.New("auditpulumi: Observe, Query and ArchiveWriter are IAM roles over an AWS S3 bucket, and the archive is on " +
			"a store at Archive.Endpoint: those workloads read it with the credentials in the state store, not with a role")
	}
	if ar.StoreRegion == "" {
		ar.StoreRegion = "auto"
	}
	if ar.CredentialsAddress == "" {
		ar.CredentialsAddress = defaultCredentialsAddress
	}
	if !addressRE.MatchString(ar.CredentialsAddress) {
		return fmt.Errorf("auditpulumi: Archive.CredentialsAddress %q must be below internal/ (internal/archive): the library's own "+
			"parameters are there, and the grant is on that address only", ar.CredentialsAddress)
	}
	return checkArchiveProfiles(ar.Profiles)
}
