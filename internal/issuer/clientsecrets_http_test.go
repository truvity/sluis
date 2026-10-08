package issuer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/policy"
)

var (
	operatorBearer = "ada@north.example|" + policy.GroupOperators
	viewerBearer   = "bob@north.example|engineers,all:access-roster:viewer"
)

type secretsRig struct {
	handler http.Handler
	store   *memory.Secrets
	trail   *audittest.Recorder
	manager *clientcreds.Manager
	iss     *issuer.Issuer
	// bodies is every response body so far.
	bodies []string
}

// newSecretsRig has `grafana` as a generated client with a stored secret, and
// `gone` as a stored secret whose client left the policy.
func newSecretsRig(t *testing.T) *secretsRig {
	t.Helper()
	r := &secretsRig{store: memory.NewSecrets(), iss: newIssuer(t, &fakeDirectory{})}
	r.trail = audittest.New(t)
	r.iss.UseAudit(r.trail)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clientcreds.Reconcile(context.Background(), []string{"grafana", "gone"}, r.store, nil, now, slog.New(slog.DiscardHandler), clientcreds.Hooks{})
	r.manager = &clientcreds.Manager{
		Store:     r.store,
		Generated: func(id string) bool { return id == "grafana" || id == "norecord" },
	}
	r.iss.UseClientSecrets(r.manager)
	r.handler = issuer.ClientSecretsHandlerForTest(r.iss, verifier())
	return r
}

func (r *secretsRig) call(t *testing.T, action, bearer, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, issuer.ClientSecretsPath+"/"+action, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	r.bodies = append(r.bodies, rec.Body.String())
	return rec.Code, rec.Body.String()
}

func (r *secretsRig) record(t *testing.T, id string) clientcreds.Record {
	t.Helper()
	got, err := r.store.Get(context.Background(), clientcreds.Path(id))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := clientcreds.DecodeRecord(got.Value)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// assertDenied checks one roster.client.secret.denied record: the verified
// caller as actor, the reason as outcome and in the payload, the action, the
// client as target (none when it was refused before the body was read), and the
// overlap where one was asked.
func assertDenied(t *testing.T, what string, rec *record.Record, actor, action, reason, client string, overlap *float64) {
	t.Helper()
	if a := rec.GetActor(); a.GetKind() != "person" || a.GetId() != actor {
		t.Errorf("%s: actor = %v, want person %s", what, a, actor)
	}
	if got := rec.GetOutcome(); got.GetResult() != auditv1.Outcome_RESULT_DENIED || got.GetReason() != reason {
		t.Errorf("%s: outcome = %v, want denied %q", what, got, reason)
	}
	fields := rec.GetData().GetFields()
	if fields["action"].GetStringValue() != action || fields["reason"].GetStringValue() != reason {
		t.Errorf("%s: payload = %v", what, fields)
	}
	if o, has := fields["overlap_seconds"]; (overlap != nil) != has || (has && o.GetNumberValue() != *overlap) {
		t.Errorf("%s: overlap_seconds = %v (present %v), want %v", what, o, has, overlap)
	}
	for k := range fields {
		if k != "action" && k != "reason" && k != "overlap_seconds" {
			t.Errorf("%s: unexpected payload field %q", what, k)
		}
	}
	var targets []string
	for _, tg := range rec.GetTargets() {
		targets = append(targets, tg.GetId())
	}
	switch {
	case client == "" && len(targets) != 0, client != "" && (len(targets) != 1 || targets[0] != client):
		t.Errorf("%s: targets = %v, want %q", what, targets, client)
	}
}

func TestTheClientSecretsEndpointAdmitsOnlyOperators(t *testing.T) {
	t.Parallel()
	lookAlike := "bob@north.example|" + policy.GroupOperators + "-x,x" + policy.GroupOperators
	for _, action := range []string{"rotate", "show", "purge"} {
		for name, tc := range map[string]struct {
			bearer string
			want   int
			// denied is the audited reason; "" is not audited at all.
			denied string
			actor  string
		}{
			"no bearer":                     {"", http.StatusUnauthorized, "", ""},
			"a bearer the verifier refuses": {"|" + policy.GroupOperators, http.StatusUnauthorized, "", ""},
			"a viewer":                      {viewerBearer, http.StatusForbidden, "forbidden", "bob@north.example"},
			"no groups at all":              {"bob@north.example", http.StatusForbidden, "forbidden", "bob@north.example"},
			"a group that only looks alike": {lookAlike, http.StatusForbidden, "forbidden", "bob@north.example"},
		} {
			r := newSecretsRig(t)
			before := r.record(t, "grafana")
			code, body := r.call(t, action, tc.bearer, `{"client":"grafana"}`)
			if code != tc.want {
				t.Errorf("%s, %s: %d %q, want %d", action, name, code, body, tc.want)
			}
			if r.record(t, "grafana") != before {
				t.Errorf("%s, %s: the record changed", action, name)
			}
			got := r.trail.Records()
			if tc.denied == "" {
				if len(got) != 0 {
					t.Errorf("%s, %s: audited %v; a request with no accepted token has no actor to name", action, name, r.trail.Actions())
				}
				continue
			}
			if len(got) != 1 || got[0].GetAction() != "roster.client.secret.denied" {
				t.Errorf("%s, %s: audited %v", action, name, r.trail.Actions())
				continue
			}
			// The body was not read: nothing says which client was asked about.
			assertDenied(t, action+", "+name, got[0], tc.actor, action, tc.denied, "", nil)
		}
	}
}

// Whatever the groups, the token must have been issued to a client that
// manages secrets: the audience or the authorized party is accessctl or the
// console.
func TestTheClientSecretsEndpointRequiresATokenIssuedToAccessctlOrTheConsole(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		audiences []string
		groups    []string
		want      int
		denied    string
	}{
		"accessctl":                     {[]string{"accessctl"}, []string{policy.GroupOperators}, http.StatusOK, ""},
		"the console":                   {[]string{"console"}, []string{policy.GroupOperators}, http.StatusOK, ""},
		"authorized party among others": {[]string{"https://issuer.example", "console"}, []string{policy.GroupOperators}, http.StatusOK, ""},
		"another client":                {[]string{"grafana"}, []string{policy.GroupOperators}, http.StatusForbidden, "wrong_audience"},
		"a look-alike":                  {[]string{"accessctl-2", "Console"}, []string{policy.GroupOperators}, http.StatusForbidden, "wrong_audience"},
		"no audience at all":            {nil, []string{policy.GroupOperators}, http.StatusForbidden, "wrong_audience"},
		"the issuer itself":             {[]string{"https://issuer.example"}, []string{policy.GroupOperators}, http.StatusForbidden, "wrong_audience"},
		"wrong audience, not operator":  {[]string{"grafana"}, []string{"engineers"}, http.StatusForbidden, "wrong_audience"},
		"right audience, not operator":  {[]string{"accessctl"}, []string{"engineers"}, http.StatusForbidden, "forbidden"},
	} {
		for _, action := range []string{"rotate", "show", "purge"} {
			r := newSecretsRig(t)
			r.handler = issuer.ClientSecretsHandlerWithAudiencesForTest(r.iss, func(_ context.Context, bearer string) (string, []string, []string, error) {
				if bearer == "" {
					return "", nil, nil, errors.New("no token")
				}
				return "ada@north.example", tc.groups, tc.audiences, nil
			})
			// A client that is not generated would be refused further in; the
			// audience is refused first, whatever is asked.
			code, body := r.call(t, action, "a-token", `{"client":"grafana"}`)
			if tc.want == http.StatusOK {
				// purge of a live client is a 422 further in: past the gate is enough.
				if code == http.StatusForbidden || code == http.StatusUnauthorized {
					t.Errorf("%s %s: refused at the gate: %d %s", action, name, code, body)
				}
				continue
			}
			if code != tc.want {
				t.Errorf("%s %s: %d %s, want %d", action, name, code, body, tc.want)
			}
			got := r.trail.Records()
			if len(got) != 1 {
				t.Errorf("%s %s: audited %v", action, name, r.trail.Actions())
				continue
			}
			assertDenied(t, action+" "+name, got[0], "ada@north.example", action, tc.denied, "", nil)
		}
	}
}

func TestTheClientSecretsEndpointDoesNotAnswerWithoutAnAdminOrAMethod(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t, &fakeDirectory{})
	h := issuer.ClientSecretsHandlerForTest(iss, verifier())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, issuer.ClientSecretsPath+"/show", strings.NewReader(`{"client":"x"}`))
	req.Header.Set("Authorization", "Bearer "+operatorBearer)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("no secrets store mounted: %d", rec.Code)
	}

	r := newSecretsRig(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(method, issuer.ClientSecretsPath+"/show", nil)
		req.Header.Set("Authorization", "Bearer "+operatorBearer)
		r.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served", method)
		}
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, issuer.ClientSecretsPath+"/frobnicate", strings.NewReader(`{"client":"x"}`))
	req.Header.Set("Authorization", "Bearer "+operatorBearer)
	r.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown action: %d", rec.Code)
	}
}

func TestTheClientSecretsEndpointRefusesABadBody(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"an unknown field":    `{"client":"grafana","secret":"mine"}`,
		"a misspelt overlap":  `{"client":"grafana","overlap":3600}`,
		"no client":           `{}`,
		"a blank client":      `{"client":"  "}`,
		"not json":            `client=grafana`,
		"empty":               ``,
		"overlap as a string": `{"client":"grafana","overlap_seconds":"3600"}`,
		"two documents":       `{"client":"grafana"}{"client":"grafana"}x`,
	} {
		for _, action := range []string{"rotate", "show", "purge"} {
			r := newSecretsRig(t)
			code, _ := r.call(t, action, operatorBearer, body)
			if name == "two documents" {
				// The first document is read; what follows it is not this
				// endpoint's to judge.
				continue
			}
			if code != http.StatusBadRequest {
				t.Errorf("%s %s: %d, want 400", action, name, code)
			}
			if got := r.trail.Records(); len(got) != 0 {
				t.Errorf("%s %s: a malformed request was audited: %v", action, name, r.trail.Actions())
			}
		}
	}
	// An over-long body is refused too.
	r := newSecretsRig(t)
	if code, _ := r.call(t, "show", operatorBearer, `{"client":"`+strings.Repeat("a", 8<<10)+`"}`); code != http.StatusBadRequest {
		t.Errorf("an 8 KiB client id: %d", code)
	}
}

// stubAdmin answers each call with the same error.
type stubAdmin struct{ err error }

func (s stubAdmin) Rotate(context.Context, string, time.Duration) (clientcreds.Rotation, error) {
	return clientcreds.Rotation{}, s.err
}
func (s stubAdmin) Show(context.Context, string) (clientcreds.Meta, error) {
	return clientcreds.Meta{}, s.err
}
func (s stubAdmin) Purge(context.Context, string) error { return s.err }

func TestTheClientSecretsEndpointMapsErrorsToStatusesAndAuditsTheRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want int
		// reason is the audited denial; "" is not audited.
		reason string
	}{
		"overlap":        {clientcreds.ErrOverlap, http.StatusBadRequest, "bad_overlap"},
		"not generated":  {clientcreds.ErrNotGenerated, http.StatusUnprocessableEntity, "not_generated"},
		"still declared": {clientcreds.ErrStillDeclared, http.StatusUnprocessableEntity, "still_declared"},
		"no record":      {clientcreds.ErrNoRecord, http.StatusNotFound, "no_record"},
		"busy":           {clientcreds.ErrBusy, http.StatusConflict, "busy"},
		"wrapped busy":   {fmt.Errorf("take: %w", clientcreds.ErrBusy), http.StatusConflict, "busy"},
		"anything else":  {errors.New("openbao sealed leaky-value"), http.StatusInternalServerError, ""},
	} {
		for _, action := range []string{"rotate", "show", "purge"} {
			for _, overlap := range []string{"", `,"overlap_seconds":3600`} {
				if overlap != "" && action != "rotate" {
					continue
				}
				iss := newIssuer(t, &fakeDirectory{})
				trail := audittest.New(t)
				iss.UseAudit(trail)
				iss.UseClientSecrets(stubAdmin{tc.err})
				h := issuer.ClientSecretsHandlerForTest(iss, verifier())
				req := httptest.NewRequest(http.MethodPost, issuer.ClientSecretsPath+"/"+action, strings.NewReader(`{"client":"x"`+overlap+`}`))
				req.Header.Set("Authorization", "Bearer "+operatorBearer)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				what := action + " " + name
				if rec.Code != tc.want {
					t.Errorf("%s: %d, want %d", what, rec.Code, tc.want)
				}
				if strings.Contains(rec.Body.String(), "leaky-value") {
					t.Errorf("%s: an internal error's text reached the caller: %q", what, rec.Body.String())
				}
				got := trail.Records()
				if tc.reason == "" {
					if len(got) != 0 {
						t.Errorf("%s: a server failure was audited as a denial: %v", what, trail.Actions())
					}
					continue
				}
				if len(got) != 1 || got[0].GetAction() != "roster.client.secret.denied" {
					t.Errorf("%s: audited %v", what, trail.Actions())
					continue
				}
				var asked *float64
				if overlap != "" {
					v := float64(3600)
					asked = &v
				}
				assertDenied(t, what, got[0], "ada@north.example", action, tc.reason, "x", asked)
				if strings.Contains(fmt.Sprint(got[0]), "leaky-value") {
					t.Errorf("%s: the audit record holds an error's text", what)
				}
			}
		}
	}
}

func TestTheClientSecretsEndpointRealRefusals(t *testing.T) {
	t.Parallel()
	r := newSecretsRig(t)
	secretValues := []string{r.record(t, "grafana").Current, r.record(t, "gone").Current}
	f := func(v float64) *float64 { return &v }
	type denial struct {
		action, reason, client string
		overlap                *float64
	}
	var want []denial
	for name, tc := range map[string]struct {
		action, body string
		status       int
		denial       denial
	}{
		"overlap over a week": {
			"rotate", `{"client":"grafana","overlap_seconds":604801}`, http.StatusBadRequest, denial{"rotate", "bad_overlap", "grafana", f(604801)}},
		"a negative overlap":      {"rotate", `{"client":"grafana","overlap_seconds":-1}`, http.StatusBadRequest, denial{"rotate", "bad_overlap", "grafana", f(-1)}},
		"rotating a named client": {"rotate", `{"client":"argocd"}`, http.StatusUnprocessableEntity, denial{"rotate", "not_generated", "argocd", nil}},
		"rotating an orphan":      {"rotate", `{"client":"gone"}`, http.StatusUnprocessableEntity, denial{"rotate", "not_generated", "gone", nil}},
		"rotating with no record": {"rotate", `{"client":"norecord"}`, http.StatusNotFound, denial{"rotate", "no_record", "norecord", nil}},
		"purging a live client":   {"purge", `{"client":"grafana"}`, http.StatusUnprocessableEntity, denial{"purge", "still_declared", "grafana", nil}},
		"purging what is not there": {
			"purge", `{"client":"never-was"}`, http.StatusNotFound, denial{"purge", "no_record", "never-was", nil}},
	} {
		before := len(r.trail.Records())
		if code, body := r.call(t, tc.action, operatorBearer, tc.body); code != tc.status {
			t.Errorf("%s: %d %q, want %d", name, code, body, tc.status)
		}
		got := r.trail.Records()
		if len(got) != before+1 || got[before].GetAction() != "roster.client.secret.denied" {
			t.Errorf("%s: audited %v", name, r.trail.Actions())
			continue
		}
		d := tc.denial
		assertDenied(t, name, got[before], "ada@north.example", d.action, d.reason, d.client, d.overlap)
		want = append(want, d)
	}
	// No secrets store at all.
	r.manager.Store = nil
	before := len(r.trail.Records())
	if code, _ := r.call(t, "rotate", operatorBearer, `{"client":"grafana"}`); code != http.StatusNotFound {
		t.Errorf("rotate with no store: %d", code)
	}
	if code, _ := r.call(t, "purge", operatorBearer, `{"client":"gone"}`); code != http.StatusNotFound {
		t.Errorf("purge with no store: %d", code)
	}
	if code, body := r.call(t, "show", operatorBearer, `{"client":"grafana"}`); code != http.StatusOK || !strings.Contains(body, `"exists":false`) {
		t.Errorf("show with no store: %d %q", code, body)
	}
	got := r.trail.Records()
	if len(got) != before+2 {
		t.Fatalf("no-store refusals audited %v", r.trail.Actions())
	}
	assertDenied(t, "rotate with no store", got[before], "ada@north.example", "rotate", "no_record", "grafana", nil)
	assertDenied(t, "purge with no store", got[before+1], "ada@north.example", "purge", "no_record", "gone", nil)
	if len(want) != 7 {
		t.Errorf("%d cases checked", len(want))
	}
	// A denial never holds a value, and nor does an answer.
	everything := strings.Join(r.bodies, "\n") + fmt.Sprint(r.trail.Records())
	for _, v := range secretValues {
		if strings.Contains(everything, v) {
			t.Errorf("a secret value is in a response or the audit trail: %.8s...", v)
		}
	}
}

func TestAnOperatorRotatesShowsAndPurgesAndNothingReturnsASecret(t *testing.T) {
	t.Parallel()
	r := newSecretsRig(t)
	first := r.record(t, "grafana")
	gone := r.record(t, "gone")

	code, body := r.call(t, "show", operatorBearer, `{"client":"grafana"}`)
	var meta struct {
		Client, Created   string
		Exists, Generated bool
		HasPrevious       bool `json:"has_previous"`
	}
	if err := json.Unmarshal([]byte(body), &meta); err != nil || code != http.StatusOK ||
		!meta.Exists || !meta.Generated || meta.HasPrevious || meta.Client != "grafana" || meta.Created == "" {
		t.Fatalf("show = %d %s (%v)", code, body, err)
	}
	if len(r.trail.Records()) != 0 {
		t.Errorf("a show was audited: %v", r.trail.Actions())
	}

	// The default overlap is a day.
	code, body = r.call(t, "rotate", operatorBearer, `{"client":"grafana"}`)
	var rotated struct {
		Client             string
		Rotated            string
		OverlapSeconds     int64  `json:"overlap_seconds"`
		PreviousValidUntil string `json:"previous_valid_until"`
		DiscardedPrevious  bool   `json:"discarded_previous"`
	}
	if err := json.Unmarshal([]byte(body), &rotated); err != nil || code != http.StatusOK ||
		rotated.OverlapSeconds != 86400 || rotated.PreviousValidUntil == "" || rotated.DiscardedPrevious {
		t.Fatalf("rotate = %d %s (%v)", code, body, err)
	}
	second := r.record(t, "grafana")
	if second.Current == first.Current || second.Previous != first.Current {
		t.Error("the record was not rotated")
	}
	rotations := r.trail.Find("roster.client.secret.rotated")
	if len(rotations) != 1 {
		t.Fatalf("audited %v", r.trail.Actions())
	}
	if a := rotations[0].GetActor(); a.GetKind() != "person" || a.GetId() != "ada@north.example" {
		t.Errorf("actor = %v", a)
	}
	if got := rotations[0].GetTargets(); len(got) != 1 || got[0].GetId() != "grafana" {
		t.Errorf("targets = %v", got)
	}
	if got := rotations[0].GetData().GetFields()["overlap_seconds"].GetNumberValue(); got != 86400 {
		t.Errorf("overlap_seconds = %v", got)
	}
	if got := rotations[0].GetData().GetFields()["discarded_previous"].GetBoolValue(); got {
		t.Error("discarded_previous")
	}

	// A hard cut, and the second rotation cut the first one's overlap short.
	code, body = r.call(t, "rotate", operatorBearer, `{"client":"grafana","overlap_seconds":0}`)
	rotated.PreviousValidUntil = ""
	if err := json.Unmarshal([]byte(body), &rotated); err != nil || code != http.StatusOK ||
		rotated.OverlapSeconds != 0 || rotated.PreviousValidUntil != "" || !rotated.DiscardedPrevious {
		t.Fatalf("hard cut = %d %s (%v)", code, body, err)
	}
	third := r.record(t, "grafana")
	if third.Previous != "" {
		t.Error("a hard cut kept a previous")
	}
	if rs := r.trail.Find("roster.client.secret.rotated"); len(rs) != 2 ||
		!rs[1].GetData().GetFields()["discarded_previous"].GetBoolValue() {
		t.Errorf("second rotation audit = %v", r.trail.Actions())
	}

	// Purge: the live client is refused, the orphan deleted.
	if code, _ = r.call(t, "purge", operatorBearer, `{"client":"gone"}`); code != http.StatusOK {
		t.Fatalf("purge = %d", code)
	}
	if _, err := r.store.Get(context.Background(), clientcreds.Path("gone")); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("the record is still there: %v", err)
	}
	deletions := r.trail.Find("roster.client.secret.deleted")
	if len(deletions) != 1 || deletions[0].GetActor().GetId() != "ada@north.example" || deletions[0].GetActor().GetKind() != "person" ||
		deletions[0].GetTargets()[0].GetId() != "gone" {
		t.Errorf("deletion audit = %v", deletions)
	}

	// No answer, and nothing audited, holds any secret there ever was.
	everything := strings.Join(r.bodies, "\n") + fmt.Sprint(r.trail.Records())
	for _, v := range []string{first.Current, second.Current, third.Current, gone.Current} {
		if strings.Contains(everything, v) {
			t.Fatalf("a secret value is in a response or the audit trail: %.8s...", v)
		}
	}
}

func TestAShowOfAnOrphanAndOfAClientWithNoRecord(t *testing.T) {
	t.Parallel()
	r := newSecretsRig(t)
	clientcreds.ReconcileOrphans(context.Background(), []string{"grafana"}, r.store, time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), nil, clientcreds.Hooks{})
	var meta struct {
		Exists, Generated bool
		Orphaned          string
	}
	_, body := r.call(t, "show", operatorBearer, `{"client":"gone"}`)
	if err := json.Unmarshal([]byte(body), &meta); err != nil || !meta.Exists || meta.Generated || meta.Orphaned != "2026-10-07T00:00:00Z" {
		t.Errorf("orphan: %s (%v)", body, err)
	}
	_, body = r.call(t, "show", operatorBearer, `{"client":"norecord"}`)
	meta = struct {
		Exists, Generated bool
		Orphaned          string
	}{}
	if err := json.Unmarshal([]byte(body), &meta); err != nil || meta.Exists || !meta.Generated {
		t.Errorf("no record: %s (%v)", body, err)
	}
}

// time.Duration(seconds) * time.Second wraps for a very large number of
// seconds, so a value far past the week can come out as a small, valid overlap:
// 36028797018967568 s is 3600 s once it wraps.
func TestAnOverlapThatOverflowsTheDurationIsRefused(t *testing.T) {
	t.Parallel()
	r := newSecretsRig(t)
	before := r.record(t, "grafana")
	code, body := r.call(t, "rotate", operatorBearer, `{"client":"grafana","overlap_seconds":36028797018967568}`)
	if code != http.StatusBadRequest {
		t.Errorf("%d %s, want 400", code, body)
	}
	if r.record(t, "grafana") != before {
		t.Error("the record was rotated")
	}
	// The refusal is audited, as the other overlap refusals are.
	if got := r.trail.Find("roster.client.secret.denied"); len(got) != 1 {
		t.Errorf("audited %v", r.trail.Actions())
	}
}
