package attest_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func (s *trail) Search(_ context.Context, r *connect.Request[auditv1.SearchRequest]) (*connect.Response[auditv1.SearchResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches = append(s.searches, r.Msg)
	s.bearers = append(s.bearers, r.Header().Get("Authorization"))
	if s.failWith != nil {
		return nil, s.failWith
	}
	return connect.NewResponse(&auditv1.SearchResponse{Items: s.records}), nil
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
	c := attest.Config{URL: srv.URL, TokenFile: tok, Timeout: 5 * time.Second}
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
	pinned := setup(t, s, func(c *attest.Config) { c.Observer = "backup-fn" })
	if _, err := pinned.Attest(context.Background(), "example", "b1"); !errors.Is(err, backup.ErrMismatch) {
		t.Fatalf("%v", err)
	}
	// Unpinned, the observer is not asked about.
	if claims, err := setup(t, s).Attest(context.Background(), "example", "b1"); err != nil || len(claims) != 1 {
		t.Fatalf("%v, %v", claims, err)
	}
}

func TestTheAttestorNeedsAnAddressAndAToken(t *testing.T) {
	if _, err := attest.New(attest.Config{TokenFile: "t"}); err == nil {
		t.Error("no address")
	}
	if _, err := attest.New(attest.Config{URL: "https://audit.example"}); err == nil {
		t.Error("no token")
	}
	q, err := attest.New(attest.Config{URL: "https://audit.example/", TokenFile: "t"})
	if err != nil || q == nil {
		t.Fatal(err)
	}
	var _ backup.Attestor = q
}
