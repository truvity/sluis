// Package attest is how a restore asks the audit trail whether a backup is the
// one the backup function made (backup.Attestor), through the audit
// installation's query service.
//
// It searches the profile that keeps the `activity` category for the
// `roster.backup.completed` records whose target is the backup, reads each
// record's data (the object key, size and SHA-256, the sidecar's SHA-256, the
// fingerprints of the recipients) and asks the service whether the hour that
// holds the record is sealed. It decides nothing: [backup.Verifier] compares
// what it returns with the backup that was fetched.
//
// What "sealed" means here is what the query service reports in a record's
// provenance: a seal object exists for the hour. The service does not check
// the seal's signature; `audit verify --root` (the installation's nightly job)
// does, and a failed hour is an incident for the audit installation to raise.
package attest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/truvity/sluis/audit/sdk/auth"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
)

// DefaultProfile is the audit profile that keeps the `activity` category, the
// destination of `roster.backup.completed`.
const DefaultProfile = "activity"

// DefaultTimeout bounds the whole of one attestation, so that a restore with
// the trail down fails in seconds rather than waiting out an invocation.
const DefaultTimeout = 30 * time.Second

// maxRecords is how many records of one backup are read. A backup has one.
const maxRecords = 20

// Action is the record that says a backup was written.
const Action = "roster.backup.completed"

// Config says where the trail is and how to ask it.
type Config struct {
	// URL is the audit installation's query service.
	URL string
	// TokenFile holds the bearer token the query service accepts for this
	// workload or person; it is read on every request.
	TokenFile string
	// Profile is the audit profile to search; [DefaultProfile] when empty.
	Profile string
	// Observer, when set, is the id the audit writer stamps on the backup
	// function's records (the `senders` table of the writer): a record stamped
	// by anyone else is refused.
	Observer string
	// Timeout bounds one attestation; [DefaultTimeout] when zero.
	Timeout time.Duration
	// HTTP is the client to reach the service with; one that presents
	// TokenFile when nil. For tests.
	HTTP connect.HTTPClient
}

// Query is a [backup.Attestor] over the audit query service.
type Query struct {
	client   auditv1connect.QueryServiceClient
	profile  string
	observer string
	timeout  time.Duration
}

var _ backup.Attestor = (*Query)(nil)

// New builds the attestor. It connects to nothing.
func New(c Config) (*Query, error) {
	if strings.TrimSpace(c.URL) == "" {
		return nil, errors.New("attest: no query service URL")
	}
	hc := c.HTTP
	if hc == nil {
		if c.TokenFile == "" {
			return nil, errors.New("attest: a token file is required: the query service answers nobody it cannot name")
		}
		hc = auth.TokenFile(c.TokenFile)
	}
	q := &Query{client: auditv1connect.NewQueryServiceClient(hc, strings.TrimRight(c.URL, "/")),
		profile: c.Profile, observer: c.Observer, timeout: c.Timeout}
	if q.profile == "" {
		q.profile = DefaultProfile
	}
	if q.timeout <= 0 {
		q.timeout = DefaultTimeout
	}
	return q, nil
}

// Attest returns the completed-backup records of the backup.
func (q *Query) Attest(ctx context.Context, _, id string) ([]backup.Claim, error) {
	ctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	res, err := q.client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
		Profile: q.profile,
		Limit:   maxRecords,
		Filter: []*auditv1.Filter{{
			Source: equal(audit.Source),
			Action: equal(Action),
			Targets: &auditv1.TargetPredicate{Operator: &auditv1.TargetPredicate_In{In: &auditv1.TargetList{
				Values: []*auditv1.TargetRef{{Type: "backup", Id: id}}}}},
		}},
	}))
	if err != nil {
		return nil, unavailable("search the trail", err)
	}
	var claims []backup.Claim
	for _, r := range res.Msg.GetItems() {
		if !hasTarget(r, id) || r.GetSource() != audit.Source || r.GetAction() != Action {
			continue
		}
		if r.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
			continue
		}
		if q.observer != "" && r.GetObserver().GetId() != q.observer {
			// Written by something other than the backup function. It is not
			// dropped as noise: a record that cannot be the real one but
			// names this backup is what a forger would send.
			return nil, fmt.Errorf("%w: record %s was stamped by an observer other than the configured one", backup.ErrMismatch, r.GetId())
		}
		c := ClaimOf(r)
		got, err := q.client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: q.profile, Id: r.GetId()}))
		if err != nil {
			return nil, unavailable("read the record's seal", err)
		}
		p := got.Msg.GetProvenance()
		if p.GetSealUnknown() {
			return nil, fmt.Errorf("%w: the query service cannot tell whether the hour of record %s is sealed", backup.ErrAttestUnavailable, r.GetId())
		}
		c.Sealed, c.SealKey = p.GetDigestId() != "", p.GetDigestId()
		claims = append(claims, c)
	}
	// A record of another installation's backup that shares an id is the
	// verifier's to refuse, by installation; it is returned as it is.
	return claims, nil
}

func unavailable(what string, err error) error {
	return fmt.Errorf("%w: %s: %v", backup.ErrAttestUnavailable, what, err)
}

func equal(v string) *auditv1.StringPredicate {
	return &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Equal{Equal: v}}
}

func hasTarget(r *auditv1.Record, id string) bool {
	for _, t := range r.GetTargets() {
		if t.GetType() == "backup" && t.GetId() == id {
			return true
		}
	}
	return false
}

// ClaimOf reads the data of a completed-backup record. A field it lacks (a
// record made before restores checked hashes) is left empty, and the verifier
// refuses the backup for it.
func ClaimOf(r *auditv1.Record) backup.Claim {
	d := r.GetData()
	c := backup.Claim{RecordID: r.GetId()}
	c.Instance = str(d, "installation")
	c.ObjectKey = str(d, "object_key")
	c.ObjectSHA256 = str(d, "object_sha256")
	c.SidecarSHA256 = str(d, "sidecar_sha256")
	if v, ok := d.GetFields()["object_size"]; ok {
		c.ObjectSize = int64(v.GetNumberValue())
	}
	if v, ok := d.GetFields()["recipients"]; ok {
		for _, e := range v.GetListValue().GetValues() {
			c.Recipients = append(c.Recipients, e.GetStringValue())
		}
	}
	return c
}

func str(d *structpb.Struct, key string) string {
	return d.GetFields()[key].GetStringValue()
}
