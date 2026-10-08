package auditpulumi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/audit/profile"
)

// checkArchive holds the shared tuning of the archive (ArchiveArgs) to the
// presets: it applies to the buckets the library creates and to the key objects
// are encrypted with, so it is refused where no preset has anything for it to
// apply to.
func (c *Args) checkArchive() error {
	ar := &c.Archive
	created := c.created()
	aws := c.awsStores()

	// Object Lock is the attested preset's bucket alone, and only the library's
	// own bucket is configured by it.
	var lockedHere bool
	for _, s := range created {
		if s.Locked {
			lockedHere = true
		}
	}
	if ar.ObjectLockMode != "" {
		if err := CheckLockMode(ar.ObjectLockMode); err != nil {
			return err
		}
	}
	if !lockedHere {
		for field, set := range map[string]bool{
			"Archive.ObjectLockMode":        ar.ObjectLockMode != "",
			"Archive.AcknowledgeCompliance": ar.AcknowledgeCompliance,
			"Archive.DefaultRetentionDays":  ar.DefaultRetentionDays != 0,
		} {
			if set {
				return fmt.Errorf("auditpulumi: %s is set and no preset is the attested preset with Create: Object Lock is configured for "+
					"the attested preset's bucket the library creates (Presets[%q] with Create), and is written for no other. "+
					"Leave it out, or create the attested bucket", field, profile.Attested)
			}
		}
	} else {
		switch ar.ObjectLockMode {
		case "":
			ar.ObjectLockMode = Compliance
		case None:
			return errors.New("auditpulumi: Archive.ObjectLockMode is NONE and Presets[\"attested\"] is a bucket the library creates: " +
				"the attested preset is kept under Object Lock. Use GOVERNANCE for the trial of the lock, or COMPLIANCE")
		}
		if ar.ObjectLockMode == Compliance && !ar.AcknowledgeCompliance {
			return errors.New("auditpulumi: the attested preset's bucket is under COMPLIANCE (Archive.ObjectLockMode) and " +
				"Archive.AcknowledgeCompliance is false: compliance retention cannot be shortened by anyone, including the account's root, " +
				"and a retention wrong in the long direction is paid for until it expires. Run the governance trial first, sign off the " +
				"retentions, then set AcknowledgeCompliance on a NEW bucket (docs/decisions/0065-archive-retention-and-lifecycle.md)")
		}
		if ar.DefaultRetentionDays <= 0 {
			return fmt.Errorf("auditpulumi: Archive.DefaultRetentionDays is required with Archive.ObjectLockMode %s: "+
				"a lock with no default rule leaves an object put without its own retention unprotected (set it, as the floor)", ar.ObjectLockMode)
		}
	}
	if ar.DefaultRetentionDays < 0 {
		return errors.New("auditpulumi: Archive.DefaultRetentionDays is not negative")
	}

	if len(created) == 0 {
		for field, set := range map[string]bool{
			"Archive.GlacierIRDays":   ar.GlacierIRDays != 0,
			"Archive.DeepArchiveDays": ar.DeepArchiveDays != 0,
		} {
			if set {
				return fmt.Errorf("auditpulumi: %s is set and no preset has Create: it is a setting of a bucket the library creates, "+
					"and the lifecycle of any other is its owner's", field)
			}
		}
	}
	setInt(&ar.GlacierIRDays, 30)
	setInt(&ar.DeepArchiveDays, 365)
	if ar.DeepArchiveDays <= ar.GlacierIRDays {
		return fmt.Errorf("auditpulumi: Archive.DeepArchiveDays (%d) must be after GlacierIRDays (%d)", ar.DeepArchiveDays, ar.GlacierIRDays)
	}

	switch ar.Encryption {
	case "":
		if len(aws) > 0 {
			ar.Encryption = EncryptionKMS
		}
	case EncryptionKMS, EncryptionS3, EncryptionAWSManaged:
	default:
		return fmt.Errorf("auditpulumi: Archive.Encryption %q must be %q (the default), %q or %q",
			ar.Encryption, EncryptionKMS, EncryptionAWSManaged, EncryptionS3)
	}
	if len(aws) == 0 {
		for field, set := range map[string]bool{
			"Archive.Encryption": ar.Encryption != "",
			"Archive.KeyArn":     ar.KeyArn != "",
			"Keys.Archive":       c.Keys.Archive != "",
		} {
			if set {
				var at []string
				for _, s := range c.endpointStores() {
					at = append(at, s.Endpoint)
				}
				return fmt.Errorf("auditpulumi: %s is set and every preset is on a store at an endpoint (%s): it is a setting of an AWS S3 "+
					"bucket, and such a store's encryption is its own", field, strings.Join(at, ", "))
			}
		}
		return nil
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
	for _, s := range aws {
		if s.KeyAlias != "" && ar.Encryption != EncryptionKMS {
			return fmt.Errorf("auditpulumi: Presets[%q].KeyAlias is set and Archive.Encryption is %q: a preset's key is a KMS key, "+
				"so the archive's encryption must be %q", s.Preset, ar.Encryption, EncryptionKMS)
		}
	}
	switch {
	case c.Keys.Archive != "" && ar.Encryption != EncryptionKMS:
		return fmt.Errorf("auditpulumi: Keys.Archive is only for Archive.Encryption %q, not %q: the other encryptions use no key of yours",
			EncryptionKMS, ar.Encryption)
	case c.Keys.Archive != "" && ar.KeyArn != "":
		return errors.New("auditpulumi: Keys.Archive and Archive.KeyArn both name the archive key: use Keys.Archive (by alias)")
	}
	if ar.Encryption == EncryptionKMS && c.Keys.Archive == "" && ar.KeyArn == "" {
		for _, s := range aws {
			if s.managed() && s.KeyAlias == "" {
				return fmt.Errorf("auditpulumi: Keys.Archive is required with Archive.Encryption \"kms\" for the bucket of Presets[%q], which names "+
					"no KeyAlias: the library creates no key, so name the estate's archive key by alias (alias/<name>), give the preset a KeyAlias, "+
					"or choose Archive.Encryption \"aws-managed\" or \"s3\"", s.Preset)
			}
		}
	}
	return nil
}
