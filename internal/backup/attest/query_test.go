package attest_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/attest"
)

// trail is a query service holding some records, and what it was asked.
type trail struct {
	auditv1connect.UnimplementedQueryServiceHandler
	mu       sync.Mutex
	records  []*auditv1.Record
	seal     map[string]string // record id -> seal key
	unknown  bool
	failWith error
	searches []*auditv1.SearchRequest
	bearers  []string
}

// page serves records the way the query service does: by limit and cursor,
// newest first, with a next cursor that never runs out.
func page(all []*auditv1.Record, r *auditv1.SearchRequest) *auditv1.SearchResponse {
	from := 0
	if r.GetCursor() != "" {
		_, _ = fmt.Sscanf(r.GetCursor(), "%d", &from)
	}
	to := from + int(r.GetLimit())
	if from > len(all) {
		from = len(all)
	}
	if to > len(all) {
		to = len(all)
	}
	return &auditv1.SearchResponse{Items: all[from:to], Cursors: &auditv1.Cursors{Next: fmt.Sprint(to)}}
}

func (s *trail) Search(_ context.Context, r *connect.Request[auditv1.SearchRequest]) (*connect.Response[auditv1.SearchResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches = append(s.searches, r.Msg)
	s.bearers = append(s.bearers, r.Header().Get("Authorization"))
	if s.failWith != nil {
		return nil, s.failWith
	}
	return connect.NewResponse(page(s.records, r.Msg)), nil
}

func (s *trail) Get(_ context.Context, r *connect.Request[auditv1.GetRequest]) (*connect.Response[auditv1.GetResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&auditv1.GetResponse{Provenance: &auditv1.Provenance{DigestId: s.seal[r.Msg.GetId()], SealUnknown: s.unknown}}), nil
}

func completed(id, backupID string, mutate ...func(*auditv1.Record)) *auditv1.Record {
	data, _ := structpb.NewStruct(map[string]any{
		"installation": "example", "object_key": "backup/example/" + backupID + ".tar.gz.age", "object_size": 4096,
		"object_sha256": "aa", "sidecar_sha256": "bb", "recipients": []any{"r1", "r2"},
	})
	r := &auditv1.Record{Id: id, Source: "roster", Action: attest.Action, Observer: &auditv1.Observer{Id: "backup-fn"},
		Outcome: &auditv1.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Targets: []*auditv1.Target{{Type: "backup", Id: backupID}}, Data: data}
	for _, m := range mutate {
		m(r)
	}
	return r
}

func setup(t *testing.T, s *trail, mutate ...func(*attest.Config)) *attest.Query {
	t.Helper()
	_, h := auditv1connect.NewQueryServiceHandler(s)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := attest.Config{URL: srv.URL, TokenFile: tok, Timeout: 5 * time.Second, Observer: "backup-fn"}
	for _, m := range mutate {
		m(&c)
	}
	q, err := attest.New(c)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestARecordWithItsSealIsReadAsAClaim(t *testing.T) {
	s := &trail{records: []*auditv1.Record{completed("rec-1", "b1")}, seal: map[string]string{"rec-1": "seals/activity/t/2026/10/10/02.jws"}}
	claims, err := setup(t, s).Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 1 {
		t.Fatalf("%v, %v", claims, err)
	}
	c := claims[0]
	if c.RecordID != "rec-1" || c.Instance != "example" || c.ObjectKey != "backup/example/b1.tar.gz.age" || c.ObjectSize != 4096 ||
		c.ObjectSHA256 != "aa" || c.SidecarSHA256 != "bb" || len(c.Recipients) != 2 || !c.Sealed || c.SealKey == "" {
		t.Errorf("%+v", c)
	}
	// It asked for exactly the completed-backup records of this backup, on the
	// profile configured, as the workload the token names.
	q := s.searches[0]
	f := q.GetFilter()
	if q.GetProfile() != attest.DefaultProfile || len(f) != 1 || f[0].GetAction().GetEqual() != attest.Action || f[0].GetSource().GetEqual() != "roster" ||
		f[0].GetTargets().GetIn().GetValues()[0].GetId() != "b1" || f[0].GetTargets().GetIn().GetValues()[0].GetType() != "backup" {
		t.Errorf("search %v", q)
	}
	if s.bearers[0] != "Bearer tok-1" {
		t.Errorf("authorization %q", s.bearers[0])
	}
}

func TestAnHourThatIsNotSealedIsReportedAsNotSealed(t *testing.T) {
	s := &trail{records: []*auditv1.Record{completed("rec-1", "b1")}}
	claims, err := setup(t, s).Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 1 || claims[0].Sealed {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestNoRecordIsNoClaims(t *testing.T) {
	claims, err := setup(t, &trail{}).Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 0 {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestWhatDoesNotConcernThisBackupIsNotAClaim(t *testing.T) {
	s := &trail{records: []*auditv1.Record{
		completed("other", "b2"),
		completed("failed", "b1", func(r *auditv1.Record) { r.Outcome.Result = auditv1.Outcome_RESULT_FAILURE }),
		completed("pruned", "b1", func(r *auditv1.Record) { r.Action = "roster.backup.pruned" }),
		completed("foreign", "b1", func(r *auditv1.Record) { r.Source = "someone" }),
	}}
	claims, err := setup(t, s).Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 0 {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestATrailThatCannotTellIsUnavailable(t *testing.T) {
	for name, s := range map[string]*trail{
		"search fails":       {failWith: connect.NewError(connect.CodeUnavailable, errors.New("down"))},
		"permission denied":  {failWith: connect.NewError(connect.CodePermissionDenied, errors.New("no grant"))},
		"seal state unknown": {records: []*auditv1.Record{completed("rec-1", "b1")}, unknown: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := setup(t, s).Attest(context.Background(), "example", "b1")
			if !errors.Is(err, backup.ErrAttestUnavailable) || !errors.Is(err, backup.ErrUnverified) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestAnUnreachableServiceIsUnavailable(t *testing.T) {
	q := setup(t, &trail{}, func(c *attest.Config) { c.URL = "http://127.0.0.1:1" })
	if _, err := q.Attest(context.Background(), "example", "b1"); !errors.Is(err, backup.ErrAttestUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestAnObserverPinRefusesARecordStampedByAnother(t *testing.T) {
	s := &trail{records: []*auditv1.Record{completed("rec-1", "b1", func(r *auditv1.Record) { r.Observer.Id = "somebody-else" })},
		seal: map[string]string{"rec-1": "seals/x.jws"}}
	if _, err := setup(t, s).Attest(context.Background(), "example", "b1"); !errors.Is(err, backup.ErrMismatch) {
		t.Fatalf("%v", err)
	}
	// Only a deployment that says so accepts anyone's record.
	anyone := setup(t, s, func(c *attest.Config) { c.Observer, c.AnyObserver = "", true })
	if claims, err := anyone.Attest(context.Background(), "example", "b1"); err != nil || len(claims) != 1 {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestAPinnedObserverIsRequiredUnlessAnyIsAskedFor(t *testing.T) {
	base := attest.Config{URL: "https://audit.example.test", TokenFile: "t"}
	if _, err := attest.New(base); err == nil || !strings.Contains(err.Error(), "observer") {
		t.Errorf("an unpinned attestor was built: %v", err)
	}
	both := base
	both.Observer, both.AnyObserver = "backup-fn", true
	if _, err := attest.New(both); err == nil {
		t.Error("an observer and any observer")
	}
	for _, c := range []attest.Config{{Observer: "backup-fn"}, {AnyObserver: true}} {
		c.URL, c.TokenFile = base.URL, base.TokenFile
		if _, err := attest.New(c); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// A forger posts newer records for a backup: the real one is not on the first
// page, and a record that disagrees on a later page still refuses the backup.
func TestEveryPageOfRecordsIsRead(t *testing.T) {
	var recs []*auditv1.Record
	for i := range 60 {
		recs = append(recs, completed(fmt.Sprintf("rec-%02d", i), "b1"))
	}
	recs[55].Data.Fields["object_sha256"] = structpb.NewStringValue("ff")
	seal := map[string]string{}
	for _, r := range recs {
		seal[r.GetId()] = "seals/x.jws"
	}
	s := &trail{records: recs, seal: seal}
	claims, err := setup(t, s).Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 60 {
		t.Fatalf("%d claims, %v", len(claims), err)
	}
	if len(s.searches) != 2 || s.searches[1].GetCursor() == "" {
		t.Errorf("%d searches", len(s.searches))
	}
	// The verifier sees the one on the second page and refuses.
	f := &backup.Fetched{SidecarSHA256: "bb", Sidecar: &backup.Sidecar{Instance: "example", ID: "b1",
		Object: backup.ObjectInfo{Key: "backup/example/b1.tar.gz.age", Size: 4096, SHA256: "aa"}}}
	v := backup.Verifier{Attestor: backup.AttestorFunc(func(context.Context, string, string) ([]backup.Claim, error) {
		return claims, nil
	})}
	if _, err := v.Check(context.Background(), f); !errors.Is(err, backup.ErrMismatch) {
		t.Errorf("a record on page two: %v", err)
	}
}

func TestAFloodOfRecordsRefusesTheBackup(t *testing.T) {
	var recs []*auditv1.Record
	for i := range 400 {
		recs = append(recs, completed(fmt.Sprintf("rec-%03d", i), "b1"))
	}
	if _, err := setup(t, &trail{records: recs}).Attest(context.Background(), "example", "b1"); !errors.Is(err, backup.ErrMismatch) {
		t.Fatalf("%v", err)
	}
}

func TestAPlainHTTPAddressIsRefusedExceptOnLoopback(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://audit.example.test": true, "http://127.0.0.1:8080": true, "http://localhost:8080": true, "http://[::1]:8080": true,
		"http://audit.example.test": false, "http://10.0.0.5": false, "ftp://audit.example.test": false, "audit.example.test": false,
	} {
		_, err := attest.New(attest.Config{URL: url, TokenFile: "t", Observer: "o"})
		if (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
}

func TestTheAttestorNeedsAnAddressAndAToken(t *testing.T) {
	if _, err := attest.New(attest.Config{TokenFile: "t", Observer: "o"}); err == nil {
		t.Error("no address")
	}
	if _, err := attest.New(attest.Config{URL: "https://audit.example", Observer: "o"}); err == nil {
		t.Error("no token")
	}
	q, err := attest.New(attest.Config{URL: "https://audit.example/", TokenFile: "t", Observer: "o"})
	if err != nil || q == nil {
		t.Fatal(err)
	}
	var _ backup.Attestor = q
}
