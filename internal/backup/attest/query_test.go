package attest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/attest"
	"github.com/truvity/sluis/internal/rpcmount"
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

func (s *trail) Search(ctx context.Context, r *auditv1.SearchRequest) (*auditv1.SearchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches = append(s.searches, r)
	info, _ := connect.CallInfoForServerContext(ctx)
	s.bearers = append(s.bearers, info.RequestHeader().Get("Authorization"))
	if s.failWith != nil {
		return nil, s.failWith
	}
	return page(s.records, r), nil
}

func (s *trail) Get(_ context.Context, r *auditv1.GetRequest) (*auditv1.GetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &auditv1.GetResponse{Provenance: &auditv1.Provenance{DigestId: s.seal[r.GetId()], SealUnknown: s.unknown}}, nil
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
	rpc := rpcmount.NewServer()
	auditv1connect.RegisterQueryServiceHandler(rpc, s)
	_, h := rpcmount.Handler(rpc)
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
		"search fails":       {failWith: connect.NewError(connect.CodeUnavailable, "down")},
		"permission denied":  {failWith: connect.NewError(connect.CodePermissionDenied, "no grant")},
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

// One junk record from any sender of audit records must not make a backup
// unrestorable: with a pinned observer only that observer's records decide,
// and the others are reported.
func TestARecordStampedByAnotherObserverIsIgnoredAndReported(t *testing.T) {
	var logs bytes.Buffer
	s := &trail{records: []*auditv1.Record{
		completed("junk", "b1", func(r *auditv1.Record) {
			r.Observer.Id = "somebody-else"
			r.Data.Fields["object_sha256"] = structpb.NewStringValue("ff")
		}),
		completed("rec-1", "b1"),
	}, seal: map[string]string{"rec-1": "seals/x.jws", "junk": "seals/x.jws"}}
	pinned := setup(t, s, func(c *attest.Config) { c.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	claims, err := pinned.Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 1 || claims[0].RecordID != "rec-1" {
		t.Fatalf("%v, %v", claims, err)
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "somebody-else") || !strings.Contains(out, "junk") {
		t.Errorf("the foreign record was not reported: %q", out)
	}
	// Only a deployment that says so lets anyone's record decide.
	anyone := setup(t, s, func(c *attest.Config) { c.Observer, c.AnyObserver = "", true })
	if claims, err := anyone.Attest(context.Background(), "example", "b1"); err != nil || len(claims) != 2 {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestAFloodOfForeignRecordsDoesNotHideTheRealOne(t *testing.T) {
	var recs []*auditv1.Record
	seal := map[string]string{"rec-real": "seals/x.jws"}
	for i := range 300 {
		recs = append(recs, completed(fmt.Sprintf("junk-%03d", i), "b1", func(r *auditv1.Record) { r.Observer.Id = "somebody-else" }))
	}
	recs = append(recs, completed("rec-real", "b1"))
	for _, r := range recs {
		seal[r.GetId()] = "seals/x.jws"
	}
	var logs bytes.Buffer
	claims, err := setup(t, &trail{records: recs, seal: seal}, func(c *attest.Config) { c.Log = slog.New(slog.NewTextHandler(&logs, nil)) }).
		Attest(context.Background(), "example", "b1")
	if err != nil || len(claims) != 1 || claims[0].RecordID != "rec-real" {
		t.Fatalf("%v, %v", claims, err)
	}
	// Reported, but not 300 lines.
	if n := strings.Count(logs.String(), "level=ERROR"); n == 0 || n > 10 {
		t.Errorf("%d error lines", n)
	}
}

func TestARedirectToAnotherOriginIsRefusedSoTheTokenStaysHome(t *testing.T) {
	var got atomic.Value
	got.Store("")
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got.Store(r.Header.Get("Authorization")) }))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	q, err := attest.New(attest.Config{URL: redirecting.URL, TokenFile: tok, Observer: "o", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Attest(context.Background(), "example", "b1"); !errors.Is(err, backup.ErrAttestUnavailable) {
		t.Errorf("%v", err)
	}
	if got.Load() != "" {
		t.Errorf("the token reached another origin: %q", got.Load())
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
	if len(s.searches) != 3 || s.searches[1].GetCursor() == "" {
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
