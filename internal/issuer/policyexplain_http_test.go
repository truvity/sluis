package issuer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// snapshotOnlyDirectory is a directory whose ONLY allowed read is the snapshot:
// a call to ResolveUser stands for a directory read or a refresh, and fails the
// test, as a fake hub that fails on refresh would.
type snapshotOnlyDirectory struct {
	t         *testing.T
	standing  map[string]issuer.Standing
	snapshots atomic.Int32
}

func (d *snapshotOnlyDirectory) ResolveUser(context.Context, string) (issuer.Standing, error) {
	d.t.Error("explain asked the directory (a read or a refresh) instead of the snapshot")
	return issuer.Standing{}, nil
}

func (d *snapshotOnlyDirectory) SnapshotUser(_ context.Context, email string) (issuer.Standing, error) {
	d.snapshots.Add(1)
	return d.standing[email], nil
}

type explainRig struct {
	handler http.Handler
	trail   *audittest.Recorder
	dir     *snapshotOnlyDirectory
}

const adaFacts = "ada@north.example"

func newExplainRig(t *testing.T) *explainRig {
	t.Helper()
	r := &explainRig{dir: &snapshotOnlyDirectory{t: t, standing: map[string]issuer.Standing{
		adaFacts: {
			Found: true, Authoritative: true,
			Groups: []string{"directory-admins@north.example", "engineering@north.example"},
		},
		"cleo@north.example": {Found: true, Suspended: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	}}}
	iss := newIssuer(t, r.dir)
	r.trail = audittest.New(t)
	iss.UseAudit(r.trail)
	r.handler = issuer.PolicyExplainHandlerForTest(iss, verifier())
	return r
}

func (r *explainRig) call(bearer, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, issuer.PolicyExplainPath, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// The explain action discloses a person's group memberships, so only an
// operator may ask: a viewer, a look-alike group and a token for another
// client are refused, each with one denied record and no snapshot read.
func TestPolicyExplainRefusesACallerWhoIsNotAnOperator(t *testing.T) {
	t.Parallel()
	for name, bearer := range map[string]string{
		"a viewer":                  viewerBearer,
		"the person asking of self": adaFacts + "|engineering,all:access-roster:viewer",
		"a look-alike group":        "bob@north.example|" + policy.GroupOperators + "-x,x" + policy.GroupOperators,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newExplainRig(t)
			code, body := r.call(bearer, `{"audience":"argocd","email":"`+adaFacts+`"}`)
			if code != http.StatusForbidden {
				t.Fatalf("status = %d (%s), want 403", code, body)
			}
			if strings.Contains(body, "engineering") {
				t.Errorf("a refused caller was told groups: %s", body)
			}
			recs := r.trail.Records()
			if len(recs) != 1 {
				t.Fatalf("records = %d, want exactly one", len(recs))
			}
			rec := recs[0]
			if rec.GetAction() != "roster.policy.explained" ||
				rec.GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED || rec.GetOutcome().GetReason() != "forbidden" {
				t.Errorf("record = %v", rec)
			}
			if rec.GetSubject() != nil || len(rec.GetTargets()) != 0 {
				t.Errorf("a refusal before the body names no subject or audience: %v", rec)
			}
			if n := r.dir.snapshots.Load(); n != 0 {
				t.Errorf("snapshot reads = %d, want 0 for a refused caller", n)
			}
		})
	}
}

func TestPolicyExplainRefusesAnUnauthenticatedCallerWithoutARecord(t *testing.T) {
	t.Parallel()
	r := newExplainRig(t)
	if code, _ := r.call("", `{}`); code != http.StatusUnauthorized {
		t.Errorf("no bearer: status = %d, want 401", code)
	}
	if code, _ := r.call("|no-identity", `{}`); code != http.StatusUnauthorized {
		t.Errorf("an unverified bearer: status = %d, want 401", code)
	}
	if n := len(r.trail.Records()); n != 0 {
		t.Errorf("records = %d: with no verified identity there is no actor to name", n)
	}
}

func TestPolicyExplainRefusesATokenForAnotherClient(t *testing.T) {
	t.Parallel()
	r := newExplainRig(t)
	iss := newIssuer(t, r.dir)
	iss.UseAudit(r.trail)
	h := issuer.PolicyExplainHandlerForAudienceTest(iss, verifier(), "grafana")
	req := httptest.NewRequest(http.MethodPost, issuer.PolicyExplainPath, strings.NewReader(`{"audience":"argocd","email":"`+adaFacts+`"}`))
	req.Header.Set("Authorization", "Bearer "+operatorBearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if recs := r.trail.Records(); len(recs) != 1 || recs[0].GetOutcome().GetReason() != "wrong_audience" {
		t.Errorf("records = %v", recs)
	}
}

// One ask is one record: the actor, the subject, the audience, the outcome and
// counts, never the groups. And the directory is asked about only through its
// snapshot (the directory itself fails the test on any other call).
func TestPolicyExplainRecordsExactlyOneRecord(t *testing.T) {
	t.Parallel()
	r := newExplainRig(t)
	code, body := r.call(operatorBearer, `{"audience":"argocd","email":"`+adaFacts+`"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	recs := r.trail.Records()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly one", len(recs))
	}
	rec := recs[0]
	if rec.GetAction() != "roster.policy.explained" {
		t.Errorf("action = %q", rec.GetAction())
	}
	if a := rec.GetActor(); a.GetKind() != "person" || a.GetId() != "ada@north.example" {
		t.Errorf("actor = %v", a)
	}
	if s := rec.GetSubject(); s.GetKind() != "person" || s.GetId() != adaFacts {
		t.Errorf("subject = %v", s)
	}
	if ts := rec.GetTargets(); len(ts) != 1 || ts[0].GetType() != "client" || ts[0].GetId() != "argocd" {
		t.Errorf("targets = %v", ts)
	}
	if rec.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
		t.Errorf("outcome = %v", rec.GetOutcome())
	}
	fields := rec.GetData().GetFields()
	if fields["proof"].GetStringValue() != "person" || !fields["admitted"].GetBoolValue() || fields["held"].GetNumberValue() == 0 {
		t.Errorf("data = %v", fields)
	}
	assertNoGroupsInRecord(t, rec)
	if n := r.dir.snapshots.Load(); n != 1 {
		t.Errorf("snapshot reads = %d, want 1", n)
	}
}

func assertNoGroupsInRecord(t *testing.T, rec *record.Record) {
	t.Helper()
	for _, secret := range []string{"mgmt:k8s", "devel:k8s", "tailnet", "engineering@"} {
		if strings.Contains(rec.GetData().String(), secret) {
			t.Errorf("the record carries %q: %v", secret, rec.GetData())
		}
	}
}

// A workload is explained from its subject alone, with no directory at all.
func TestPolicyExplainOfAWorkloadAsksNoDirectory(t *testing.T) {
	t.Parallel()
	r := newExplainRig(t)
	code, body := r.call(operatorBearer, `{"audience":"directory-roster","subject":"devel:k8s:identity-system:authorization-webhook"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	var got policy.Explanation
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Admitted || got.AdmittedBy != "all:directory-roster:reader" {
		t.Errorf("explanation = %+v", got)
	}
	if n := r.dir.snapshots.Load(); n != 0 {
		t.Errorf("snapshot reads = %d, want 0", n)
	}
	recs := r.trail.Records()
	if len(recs) != 1 || recs[0].GetSubject().GetKind() != "workload" {
		t.Errorf("records = %v", recs)
	}
}

// The live answer and the offline answer are one value: given the same facts
// the policy and the snapshot hold, the bytes are the same.
func TestPolicyExplainLiveEqualsOffline(t *testing.T) {
	t.Parallel()
	r := newExplainRig(t)
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{"argocd", "k8s:mgmt", "k8s:devel", "local-dev"} {
		code, live := r.call(operatorBearer, `{"audience":"`+audience+`","email":"`+adaFacts+`"}`)
		if code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", audience, code, live)
		}
		offline, err := declared.Explain(audience, policy.ExplainInput(adaFacts, r.dir.standing[adaFacts].Groups))
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(offline)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(live) != string(want) {
			t.Errorf("%s: live and offline differ\nlive:    %s\noffline: %s", audience, live, want)
		}
	}
	// A client's lifetime cap shows: argocd caps at two hours.
	if _, body := r.call(operatorBearer, `{"audience":"argocd","email":"`+adaFacts+`"}`); !strings.Contains(body, `"lifetime":"2h0m0s"`) {
		t.Errorf("the explanation does not carry the lifetime: %s", body)
	}
}

func TestPolicyExplainRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body   string
		status int
		reason string // "" means no record
	}{
		"an unknown client":        {`{"audience":"nowhere","email":"` + adaFacts + `"}`, http.StatusNotFound, "unknown_client"},
		"a suspended account":      {`{"audience":"argocd","email":"cleo@north.example"}`, http.StatusUnprocessableEntity, "not_live"},
		"no subject":               {`{"audience":"argocd"}`, http.StatusBadRequest, ""},
		"both subjects":            {`{"audience":"argocd","email":"a@north.example","subject":"x"}`, http.StatusBadRequest, ""},
		"a subject that is no one": {`{"audience":"argocd","subject":"nonsense"}`, http.StatusBadRequest, ""},
		"an unknown field":         {`{"audience":"argocd","email":"a@north.example","groups":["x"]}`, http.StatusBadRequest, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newExplainRig(t)
			code, body := r.call(operatorBearer, tc.body)
			if code != tc.status {
				t.Fatalf("status = %d (%s), want %d", code, body, tc.status)
			}
			recs := r.trail.Records()
			switch {
			case tc.reason == "" && len(recs) != 0:
				t.Errorf("records = %v, want none for a malformed request", recs)
			case tc.reason != "" && (len(recs) != 1 || recs[0].GetOutcome().GetReason() != tc.reason):
				t.Errorf("records = %v, want one denied %q", recs, tc.reason)
			}
		})
	}
}

// A snapshot the hub cannot vouch for, with no last-known answer inside the
// hold window, is what a token request would be refused on; the explanation
// says so rather than inventing groups, and (the directory failing the test on
// any read) asks nothing but the snapshot.
func TestPolicyExplainOfAnUnvouchedSnapshotIsRefusedLikeAToken(t *testing.T) {
	t.Parallel()
	dir := &snapshotOnlyDirectory{t: t, standing: map[string]issuer.Standing{
		adaFacts: {Found: true, Authoritative: false, Groups: []string{"engineering@north.example"}},
	}}
	iss := newIssuer(t, dir)
	iss.UseAudit(audittest.New(t))
	h := issuer.PolicyExplainHandlerForTest(iss, verifier())
	req := httptest.NewRequest(http.MethodPost, issuer.PolicyExplainPath, strings.NewReader(`{"audience":"argocd","email":"`+adaFacts+`"}`))
	req.Header.Set("Authorization", "Bearer "+operatorBearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want 422", rec.Code, rec.Body.String())
	}
}
