package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A backup is two objects in a bucket that more than the backup function may
// write: the age recipients are public keys and the sidecar is plain JSON, so
// anyone who can put an object under backup/<instance>/ can plant a backup
// that [Open] accepts. [Verifier] closes that. The backup function records, in
// the audit trail, the hashes of what it wrote ([Claim]); a restore asks the
// trail for them ([Attestor]) and refuses a backup whose bytes are not the
// ones the record names, or whose record is missing or not yet sealed.

// Errors a caller can match with errors.Is. Each of the first four wraps
// [ErrUnverified], which is what `allowUnverified` overrides.
var (
	// ErrUnverified is a backup the sealed audit trail does not vouch for.
	ErrUnverified = errors.New("backup: not vouched for by the sealed audit trail")
	// ErrNotAttested is a backup of which the trail holds no completed-backup
	// record: planted in the bucket, or its record never arrived.
	ErrNotAttested = fmt.Errorf("%w: the trail holds no record that this backup was made", ErrUnverified)
	// ErrMismatch is a backup whose record names other bytes or other
	// recipients, or whose records disagree with each other.
	ErrMismatch = fmt.Errorf("%w: the record of this backup disagrees with what the archive holds", ErrUnverified)
	// ErrNotSealed is a backup whose record is in the trail but whose hour is
	// not sealed yet. Wait for the notary's next run and try again.
	ErrNotSealed = fmt.Errorf("%w: the hour holding this backup's record is not sealed yet", ErrUnverified)
	// ErrAttestUnavailable is a trail that could not be asked, or a restore
	// that is not configured to ask.
	ErrAttestUnavailable = fmt.Errorf("%w: the trail could not be asked", ErrUnverified)
	// ErrRecipients is a backup encrypted to other recipients than the restore
	// is configured with. It is not an [ErrUnverified]: no flag overrides it.
	ErrRecipients = errors.New("backup: the sidecar's recipients are not the configured ones")
	// ErrNoRecipients is an unverified restore with no recipients to compare
	// the sidecar with: it would have no check left.
	ErrNoRecipients = errors.New("backup: an unverified restore needs backup.recipients to compare the sidecar with")
)

// The verification words, in the audit record of an unverified restore.
const (
	ReasonUnattested  = "unattested"
	ReasonMismatch    = "mismatch"
	ReasonUnsealed    = "unsealed"
	ReasonUnavailable = "unavailable"
)

// ReasonOf is the short word for why err is an [ErrUnverified]; "" for any
// other error.
func ReasonOf(err error) string {
	switch {
	case errors.Is(err, ErrNotAttested):
		return ReasonUnattested
	case errors.Is(err, ErrMismatch):
		return ReasonMismatch
	case errors.Is(err, ErrNotSealed):
		return ReasonUnsealed
	case errors.Is(err, ErrAttestUnavailable):
		return ReasonUnavailable
	}
	return ""
}

// Claim is what the audit trail says one backup is: the data of a
// completed-backup record.
type Claim struct {
	// RecordID is the record's id in the trail, for the log.
	RecordID string
	// Instance is the installation the record says the backup is of.
	Instance string
	// ObjectKey, ObjectSize and ObjectSHA256 describe the encrypted object;
	// SidecarSHA256 is the hash of the sidecar as written.
	ObjectKey     string
	ObjectSize    int64
	ObjectSHA256  string
	SidecarSHA256 string
	// Recipients are the [Fingerprints] of the recipients it was encrypted to.
	Recipients []string
	// Sealed is whether the hour holding the record is sealed, and SealKey the
	// seal that covers it.
	Sealed  bool
	SealKey string
}

// Attestor finds the completed-backup records of a backup in the audit trail.
type Attestor interface {
	// Attest returns every record of the backup, none when there is none. It
	// returns an [ErrAttestUnavailable] when it cannot tell.
	Attest(ctx context.Context, instance, id string) ([]Claim, error)
}

// AttestorFunc is an [Attestor] that is a function.
type AttestorFunc func(ctx context.Context, instance, id string) ([]Claim, error)

// Attest calls f.
func (f AttestorFunc) Attest(ctx context.Context, instance, id string) ([]Claim, error) {
	return f(ctx, instance, id)
}

// NoAttestor is the attestor of a restore that is not configured to verify: it
// can tell nothing, so every backup is [ErrAttestUnavailable] until an
// operator passes allowUnverified.
type NoAttestor struct{}

// Attest refuses.
func (NoAttestor) Attest(context.Context, string, string) ([]Claim, error) {
	return nil, fmt.Errorf("%w: backup.verify is not configured on this restore", ErrAttestUnavailable)
}

// Hashes are the hashes of a backup's two objects.
type Hashes struct{ Object, Sidecar string }

// Verification is what was established about a fetched backup.
type Verification struct {
	// Unverified is set when the backup went ahead without a sealed record of
	// it, and Reason says why (the Reason... words).
	Unverified bool
	Reason     string
	// Hashes are those of the backup that was checked.
	Hashes Hashes
	// RecordID and SealKey name the record and seal that vouched for it.
	RecordID, SealKey string
}

// Verifier decides whether a fetched backup is the one the backup function
// made.
type Verifier struct {
	// Attestor asks the trail. Nil is [NoAttestor].
	Attestor Attestor
	// Recipients are the age recipients (age1...) the restore is configured
	// with. When set the sidecar must list exactly these, verified or not.
	Recipients []string
	// AllowUnverified lets a backup the trail does not vouch for go ahead,
	// with [Verification.Unverified] set. It needs Recipients, since the
	// comparison with them is then the only check left. It does not override
	// [ErrRecipients].
	AllowUnverified bool
	// Pinned, when set, is the hashes an earlier slice of a restore checked.
	// The trail is not asked again: the fetched backup must be those bytes,
	// and Was is the verification that slice reached.
	Pinned *Hashes
	Was    Verification
}

// Check holds f to the rules, in this order:
//
//  1. The sidecar's recipients are the configured ones, if any are
//     configured. Always.
//  2. Pinned, when set: f has those hashes, and nothing more is asked.
//  3. The trail holds a record of this backup; every record of it agrees;
//     its object and sidecar hashes, its object key and size and its
//     recipients are those of f; and the hour holding it is sealed.
//
// A failure of 3 is an [ErrUnverified]; with AllowUnverified the backup goes
// ahead and the verification says why it was not verified.
func (v Verifier) Check(ctx context.Context, f *Fetched) (Verification, error) {
	sc := f.Sidecar
	got := Hashes{Object: sc.Object.SHA256, Sidecar: f.SidecarSHA256}
	out := Verification{Hashes: got}

	if len(v.Recipients) > 0 && !slices.Equal(Fingerprints(sc.Recipients), Fingerprints(v.Recipients)) {
		return out, fmt.Errorf("%w: the backup is encrypted to %d recipient(s), the restore is configured with %d, and they differ",
			ErrRecipients, len(Fingerprints(sc.Recipients)), len(Fingerprints(v.Recipients)))
	}

	if v.Pinned != nil {
		if *v.Pinned != got {
			return out, fmt.Errorf("%w: the backup in the archive is not the one this restore began with", ErrMismatch)
		}
		was := v.Was
		was.Hashes = got
		return was, nil
	}

	err := v.attest(ctx, f, &out)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, ErrUnverified) || !v.AllowUnverified {
		return out, err
	}
	if len(v.Recipients) == 0 {
		return out, fmt.Errorf("%w (%w)", ErrNoRecipients, err)
	}
	out.Unverified, out.Reason = true, ReasonOf(err)
	return out, nil
}

// attest is rule 3. It fills in the record and seal that vouched.
func (v Verifier) attest(ctx context.Context, f *Fetched, out *Verification) error {
	sc := f.Sidecar
	at := v.Attestor
	if at == nil {
		at = NoAttestor{}
	}
	claims, err := at.Attest(ctx, sc.Instance, sc.ID)
	if err != nil {
		if errors.Is(err, ErrUnverified) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrAttestUnavailable, err)
	}
	if len(claims) == 0 {
		return fmt.Errorf("%w (backup %s)", ErrNotAttested, sc.ID)
	}
	want := Claim{Instance: sc.Instance, ObjectKey: sc.Object.Key, ObjectSize: sc.Object.Size, ObjectSHA256: sc.Object.SHA256,
		SidecarSHA256: f.SidecarSHA256, Recipients: Fingerprints(sc.Recipients)}
	for i := range claims {
		if what := differs(&claims[i], &want); what != "" {
			return fmt.Errorf("%w: the record %s names a different %s", ErrMismatch, claims[i].RecordID, what)
		}
	}
	for i := range claims {
		if !claims[i].Sealed {
			return fmt.Errorf("%w (record %s)", ErrNotSealed, claims[i].RecordID)
		}
	}
	out.RecordID, out.SealKey = claims[0].RecordID, claims[0].SealKey
	return nil
}

// differs names the first thing in which a record's claim is not want, or "".
func differs(c, want *Claim) string {
	got := slices.Clone(c.Recipients)
	slices.Sort(got)
	switch {
	case c.Instance != want.Instance:
		return "installation"
	case c.ObjectKey != want.ObjectKey:
		return "object key"
	case c.ObjectSize != want.ObjectSize:
		return "object size"
	case c.ObjectSHA256 == "" || !strings.EqualFold(c.ObjectSHA256, want.ObjectSHA256):
		return "object hash"
	case c.SidecarSHA256 == "" || !strings.EqualFold(c.SidecarSHA256, want.SidecarSHA256):
		return "sidecar hash"
	case !slices.Equal(slices.Compact(got), want.Recipients):
		return "recipient list"
	}
	return ""
}
