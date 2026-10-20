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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/truvity/sluis/audit/sdk/auth"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/storage/logattr"
)

// DefaultProfile is the audit profile that keeps the `activity` category, the
// destination of `roster.backup.completed`.
const DefaultProfile = "activity"

// DefaultTimeout bounds the whole of one attestation, so that a restore with
// the trail down fails in seconds rather than waiting out an invocation.
const DefaultTimeout = 30 * time.Second

// maxRecords bounds the records of the configured observer read for one
// backup: a backup has one. More than that is a flood, and the backup is
// refused rather than judged on the first page. maxPages bounds the pages
// read in all, foreign records included, so that a flood from another sender
// costs time and not the restore.
const (
	pageSize   = 50
	maxRecords = 200
	maxPages   = 40
	// maxLogged is how many foreign records are logged one by one.
	maxLogged = 5
)

// Action is the record that says a backup was written.
const Action = "roster.backup.completed"

// Config says where the trail is and how to ask it.
type Config struct {
	// URL is the audit installation's query service: https, or http on a
	// loopback address.
	URL string
	// TokenFile holds the bearer token the query service accepts for this
	// workload or person; it is read on every request.
	TokenFile string
	// Profile is the audit profile to search; [DefaultProfile] when empty.
	Profile string
	// Observer is the id the audit writer stamps on the backup function's
	// records (the `senders` table of the writer): a record stamped by anyone
	// else is refused. Required unless AnyObserver is set.
	Observer string
	// AnyObserver accepts a record stamped by anyone. Whoever may send to the
	// audit queue, or write as the roster source, can then vouch for a forged
	// backup.
	AnyObserver bool
	// Timeout bounds one attestation; [DefaultTimeout] when zero.
	Timeout time.Duration
	// HTTP is the client to reach the service with; one that presents
	// TokenFile when nil. For tests.
	HTTP connecthttp.HTTPClient
	// Log is where a record of another observer is reported; slog.Default()
	// when nil.
	Log *slog.Logger
}

// Query is a [backup.Attestor] over the audit query service.
type Query struct {
	client   auditv1connect.QueryServiceClient
	profile  string
	observer string
	timeout  time.Duration
	log      *slog.Logger
}

var _ backup.Attestor = (*Query)(nil)

// New builds the attestor. It connects to nothing.
func New(c Config) (*Query, error) {
	if err := config.VerifyQueryURL(c.URL); err != nil {
		return nil, fmt.Errorf("attest: the query service address: %w", err)
	}
	switch {
	case c.Observer == "" && !c.AnyObserver:
		return nil, errors.New("attest: an observer is required: the id the audit writer stamps on the backup function's records, " +
			"or AnyObserver, which lets any sender of audit records vouch for a backup")
	case c.Observer != "" && c.AnyObserver:
		return nil, errors.New("attest: Observer and AnyObserver are one or the other")
	}
	hc := c.HTTP
	if hc == nil {
		if c.TokenFile == "" {
			return nil, errors.New("attest: a token file is required: the query service answers nobody it cannot name")
		}
		hc = noRedirectAcrossOrigins(auth.TokenFile(c.TokenFile))
	}
	q := &Query{client: auditv1connect.NewQueryServiceClient(connect.NewClient(
		connecthttp.NewTransport(hc, strings.TrimRight(c.URL, "/"), connecthttp.WithReadMaxBytes(0)))),
		profile: c.Profile, observer: c.Observer, timeout: c.Timeout, log: c.Log}
	if q.log == nil {
		q.log = slog.Default()
	}
	if q.profile == "" {
		q.profile = DefaultProfile
	}
	if q.timeout <= 0 {
		q.timeout = DefaultTimeout
	}
	return q, nil
}

// noRedirectAcrossOrigins refuses a redirect to another scheme or host: the
// client presents a bearer token, and a redirect must not carry it elsewhere.
func noRedirectAcrossOrigins(c *http.Client) *http.Client {
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("attest: too many redirects")
		}
		if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
			return fmt.Errorf("attest: the query service redirected to another origin (%s://%s); refused", req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
	return c
}

// Attest returns the completed-backup records of the backup. It reads every
// page of the search, up to a bound: a record that disagrees on a later page is
// as much the verifier's to see as one on the first. With a pinned observer
// only that observer's records decide; any other's is skipped and reported,
// since one junk record from any sender of audit records must not make a backup
// permanently unrestorable.
func (q *Query) Attest(ctx context.Context, _, id string) ([]backup.Claim, error) {
	ctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	items, err := q.search(ctx, id)
	if err != nil {
		return nil, err
	}
	var claims []backup.Claim
	for _, r := range items {
		c := ClaimOf(r)
		got, err := q.client.Get(ctx, &auditv1.GetRequest{Profile: q.profile, Id: r.GetId()})
		if err != nil {
			return nil, unavailable("read the record's seal", err)
		}
		p := got.GetProvenance()
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

// search reads the pages of the completed-backup records naming the backup, and
// keeps the ones that may decide: with a pinned observer, that observer's. A
// page with no records or no next cursor is the last.
func (q *Query) search(ctx context.Context, id string) ([]*auditv1.Record, error) {
	var keep []*auditv1.Record
	foreign := 0
	cursor := ""
	for page := 0; page < maxPages; page++ {
		res, err := q.client.Search(ctx, &auditv1.SearchRequest{
			Profile: q.profile,
			Limit:   pageSize,
			Cursor:  cursor,
			Filter: []*auditv1.Filter{{
				Source: equal(audit.Source),
				Action: equal(Action),
				Targets: &auditv1.TargetPredicate{Operator: &auditv1.TargetPredicate_In{In: &auditv1.TargetList{
					Values: []*auditv1.TargetRef{{Type: "backup", Id: id}}}}},
			}},
		})
		if err != nil {
			return nil, unavailable("search the trail", err)
		}
		for _, r := range res.GetItems() {
			if !hasTarget(r, id) || r.GetSource() != audit.Source || r.GetAction() != Action ||
				r.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
				continue
			}
			if q.observer != "" && r.GetObserver().GetId() != q.observer {
				foreign++
				if foreign <= maxLogged {
					q.log.ErrorContext(ctx, "a completed-backup record was stamped by an observer other than the configured one; it is ignored",
						slog.String("backup", id), slog.String("record", r.GetId()), logattr.SafeString("observer", r.GetObserver().GetId()))
				}
				continue
			}
			if keep = append(keep, r); len(keep) > maxRecords {
				return nil, fmt.Errorf("%w: more than %d records name backup %s; a backup has one", backup.ErrMismatch, maxRecords, id)
			}
		}
		cursor = res.GetCursors().GetNext()
		if len(res.GetItems()) == 0 || cursor == "" {
			if foreign > maxLogged {
				q.log.ErrorContext(ctx, "more completed-backup records by other observers were ignored", slog.String("backup", id), slog.Int("ignored", foreign-maxLogged))
			}
			return keep, nil
		}
	}
	return nil, fmt.Errorf("%w: the search of the trail for backup %s did not end within %d pages", backup.ErrAttestUnavailable, id, maxPages)
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
