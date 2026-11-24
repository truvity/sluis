package audit

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
)

// The vector files are shared with kits/rust/sluis-audit (kits/conformance/audit): both suites must pass on them.
const vectorDir = "../../conformance/audit/"

func readVectors(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(vectorDir + name) //nolint:gosec // a fixed directory of the repository
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

// convert turns decoded JSON into the types Canonical and Event.Sluis take: an integer is int64, a number that is
// not an integer (or does not fit int64) is float64, which the library must refuse, and with strict a list of
// strings, or an empty list, is []string.
func convert(v any, strict bool) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return n
		}
		f, _ := strconv.ParseFloat(string(x), 64)
		if math.IsInf(f, 0) {
			f = math.MaxFloat64
		}
		return f
	case []any:
		out := make([]any, len(x))
		allStr := len(x) > 0
		for i, e := range x {
			out[i] = convert(e, strict)
			if _, ok := out[i].(string); !ok {
				allStr = false
			}
		}
		if strict && (allStr || len(x) == 0) {
			ss := make([]string, len(out))
			for i := range out {
				ss[i] = out[i].(string)
			}
			return ss
		}
		return out
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = convert(e, strict)
		}
		return m
	default:
		return v
	}
}

// sluisFrom converts the sluis member of a vector into Event.Sluis. {"$repeat": "a", "times": n} is a string of n
// copies, so a vector can hold a long string without being long.
func sluisFrom(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return expandRepeat(convert(v, true)).(map[string]any)
}

func expandRepeat(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x["$repeat"].(string); ok {
			n, _ := x["times"].(int64)
			return strings.Repeat(s, int(n))
		}
		for k, e := range x {
			x[k] = expandRepeat(e)
		}
	case []string, []any:
	}
	return v
}

// repeatable is a string, or {"$repeat": "a", "times": n} for n copies, so a vector can hold a long string.
type repeatable string

func (r *repeatable) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = repeatable(s)
		return nil
	}
	var m struct {
		Repeat string `json:"$repeat"`
		Times  int    `json:"times"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*r = repeatable(strings.Repeat(m.Repeat, m.Times))
	return nil
}

type eventJSON struct {
	Action struct {
		Name         string     `json:"name"`
		ClassUID     int        `json:"class_uid"`
		ActivityID   int        `json:"activity_id"`
		ActivityName repeatable `json:"activity_name"`
	} `json:"action"`
	Subject *struct {
		Tenant string     `json:"tenant"`
		Kind   repeatable `json:"kind"`
		Value  repeatable `json:"value"`
	} `json:"subject"`
	Status     int             `json:"status"`
	StatusCode string          `json:"status_code"`
	Tenant     string          `json:"tenant"`
	Sluis      json.RawMessage `json:"sluis"`
	Policy     *struct {
		DecisionID string   `json:"decision_id"`
		Digest     string   `json:"digest"`
		PolicyIDs  []string `json:"policy_ids"`
		Decision   int      `json:"decision"`
	} `json:"policy"`
	Caller    string `json:"caller"`
	Operation string `json:"operation"`
	Entity    *struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		UID     string `json:"uid"`
		Version string `json:"version"`
	} `json:"entity"`
}

func (e eventJSON) event(t *testing.T) Event {
	t.Helper()
	ev := Event{
		Action: Action{Name: e.Action.Name, ClassUID: e.Action.ClassUID, ActivityID: e.Action.ActivityID, ActivityName: string(e.Action.ActivityName)},
		Status: Status(e.Status), StatusCode: e.StatusCode, Tenant: e.Tenant, ProductObject: sluisFrom(t, e.Sluis),
	}
	if e.Policy != nil {
		ev.Policy = &PolicyRef{DecisionID: e.Policy.DecisionID, Digest: e.Policy.Digest, PolicyIDs: e.Policy.PolicyIDs, Decision: Decision(e.Policy.Decision)}
	}
	ev.Caller, ev.Operation = e.Caller, e.Operation
	if e.Entity != nil {
		ev.Entity = &Entity{Type: e.Entity.Type, Name: e.Entity.Name, UID: e.Entity.UID, Version: e.Entity.Version}
	}
	if e.Subject != nil {
		ev.Subject = &Subject{Tenant: e.Subject.Tenant, Kind: string(e.Subject.Kind), Value: string(e.Subject.Value)}
	}
	return ev
}

// recordCase is one record vector: the resolved inputs of a record and the bytes and hash it must come to. Lost, when
// present, is the count of lost facts the emitter carries (0030 "The sluis fields").
type recordCase struct {
	Name        string    `json:"name"`
	Actor       string    `json:"actor"`
	Boundary    string    `json:"boundary"`
	Release     string    `json:"release"`
	UID         string    `json:"uid"`
	TimeMS      int64     `json:"time_ms"`
	Correlation string    `json:"correlation"`
	Pseudonym   string    `json:"pseudonym"`
	Event       eventJSON `json:"event"`
	Lost        *struct {
		Process string `json:"process"`
		Total   int64  `json:"total"`
		Since   int64  `json:"since"`
	} `json:"lost"`
	Expected string `json:"expected"`
	SHA384   string `json:"sha384"`
}

func (c recordCase) input(t *testing.T) RecordInput {
	t.Helper()
	return RecordInput{
		Component: c.Actor, Scope: c.Boundary, Release: c.Release, UID: c.UID, TimeMS: c.TimeMS,
		Correlation: c.Correlation, Pseudonym: c.Pseudonym, Event: c.Event.event(t),
	}
}

// build makes the record of the case. A case without a note is audit's pure serialiser. A case with a note of lost facts is
// made by an emitter of sluis, as it happens in a process: one emit of the event that stands for Lost.Total facts fails at
// Lost.Since, and the next emit, at TimeMS, carries the note. The emitter's own process and metadata.uid are random, so
// they are put back to the case's before the bytes are compared.
func (c recordCase) build(t *testing.T) []byte {
	t.Helper()
	if c.Lost == nil {
		got, err := BuildRecord(c.input(t))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if c.Correlation != "" || c.Pseudonym != "" {
		t.Fatal("a case with a note names no correlation and no pseudonym")
	}
	ev := c.Event.event(t)
	now := time.UnixMilli(c.Lost.Since)
	tr := &fakeTransport{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	// The cases probe the layout of the note, not sluis's catalogue, so the table is the open one of the tests.
	e, err := New(Config{Actor: c.Actor, Boundary: c.Boundary, Release: c.Release, Transport: tr, Table: openTable(t), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	lost := ev
	lost.Facts = c.Lost.Total
	if err := e.Emit(context.Background(), lost); err == nil {
		t.Fatal("the emit that loses the facts did not fail")
	}
	tr.err, now = nil, time.UnixMilli(c.TimeMS)
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	msg := tr.reqs[len(tr.reqs)-1].GetMessage()
	rec := strings.Replace(string(msg.GetRecord()), `"uid":"`+msg.GetUid()+`"`, `"uid":"`+c.UID+`"`, 1)
	rec = processRE.ReplaceAllString(rec, `"process":"`+c.Lost.Process+`"`)
	return []byte(rec)
}

var processRE = regexp.MustCompile(`"process":"[0-9a-f]{16}"`)

// check is what audit's emitter says of an event before it lays out a record: BuildRecord refuses the event first.
func check(ev Event) error {
	_, err := BuildRecord(RecordInput{Component: "key", Scope: "dev", Release: "0.0.0-test", UID: "01950000-0000-7000-8000-000000000001", Event: ev})
	return err
}

type recordFile struct {
	Cases      []recordCase `json:"cases"`
	Pseudonyms []struct {
		TimeMS   int64  `json:"time_ms"`
		MacHex   string `json:"mac_hex"`
		Epoch    string `json:"epoch"`
		Expected string `json:"expected"`
	} `json:"pseudonyms"`
	UUIDs []struct {
		MS       uint64 `json:"ms"`
		Counter  uint16 `json:"counter"`
		RandBHex string `json:"rand_b_hex"`
		Expected string `json:"expected"`
	} `json:"uuids"`
	Refusals []struct {
		Name   string          `json:"name"`
		Sluis  json.RawMessage `json:"sluis"`
		Reason string          `json:"reason"`
		Path   string          `json:"path"`
	} `json:"refusals"`
	Accepted []struct {
		Name  string          `json:"name"`
		Sluis json.RawMessage `json:"sluis"`
	} `json:"accepted"`
	EventRefusals []struct {
		Name   string    `json:"name"`
		Event  eventJSON `json:"event"`
		Reason string    `json:"reason"`
		Path   string    `json:"path"`
	} `json:"event_refusals"`
	EventAccepted []struct {
		Name  string    `json:"name"`
		Event eventJSON `json:"event"`
	} `json:"event_accepted"`
	MacLabel  string `json:"mac_label"`
	MacInputs []struct {
		Boundary, Tenant, Kind, Value string
		InputHex                      string `json:"input_hex"`
	} `json:"mac_inputs"`
	Email struct {
		Match   []string `json:"match"`
		NoMatch []string `json:"no_match"`
	} `json:"email"`
	RecordMax int `json:"record_max_bytes"`
}

// verifies: REQ-AUDIT-088, REQ-AUDIT-090, REQ-AUDIT-116, REQ-AUDIT-117, REQ-AUDIT-120, REQ-AUDIT-186, REQ-AUDIT-188
func TestRecordVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.Cases) == 0 {
		t.Fatal("the vector file is empty")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := c.build(t)
			if string(got) != c.Expected {
				t.Fatalf("got  %s\nwant %s", got, c.Expected)
			}
		})
	}
}

// The hash of a record is the SHA-384 of its RFC 8785 bytes: the known answer of every record vector, the same
// one the indexer's row carries (actors/indexer).
//
// verifies: REQ-AUDIT-004
func TestRecordHashVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.Cases) == 0 {
		t.Fatal("the vector file is empty")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := c.build(t)
			sum := sha512.Sum384(got)
			if hex.EncodeToString(sum[:]) != c.SHA384 {
				t.Fatalf("SHA-384 of %s = %x, want %s", got, sum, c.SHA384)
			}
		})
	}
}

// captionKeys are the members OCSF gives to the text of an enum id: the caption of severity_id, status_id, a type id, a
// category, a class, an activity or an action, and the words for them. A stored record holds the ids.
var captionKeys = []string{"caption", "severity", "status", "type", "category", "class", "activity", "action", "disposition"}

// leanViolations lists what a stored record must not hold outside unmapped.sluis: observables, enrichments, and enum
// captions. Three members read like captions and are not: activity_name, which describes activity 99 (Other) and is
// required then, entity.type, which names the kind of an entity of type_id 99 (Other) and which the toolkit requires
// beside it, resources[].type, which is a plain string in OCSF, and authorizations[].decision, the one caption OCSF requires
// of an authorization.
func leanViolations(rec map[string]any) []string {
	var bad []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				p := path + "/" + k
				switch {
				case p == "/unmapped":
					continue // the sluis facts are the emitter's own and bounded elsewhere
				case p == "/entity/type" || p == "/resources[]/type":
					// The word that names the entity's kind, beside type_id 99 (Other).
				case k == "observables" || k == "enrichments" || slices.Contains(captionKeys, k):
					bad = append(bad, p)
				case strings.HasSuffix(k, "_name") && p != "/activity_name" && p != "/metadata/log_name" && p != "/metadata/product/name":
					bad = append(bad, p)
				case k == "decision" && p != "/authorizations[]/decision":
					bad = append(bad, p)
				}
				walk(p, e)
			}
		case []any:
			for _, e := range x {
				walk(path+"[]", e)
			}
		}
	}
	walk("", rec)
	return bad
}

// A stored record holds enum ids, not captions, and no observables or enrichments. The records checked are the ones
// the emitter builds from the inputs of the record vectors, so a change to the emitter is caught, and the check is run
// on a record that has each of them, so it is not vacuous.
//
// verifies: REQ-AUDIT-091
func TestRecordVectorsAreLean(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.Cases) == 0 {
		t.Fatal("the vector file is empty")
	}
	for _, c := range f.Cases {
		built := c.build(t)
		var rec map[string]any
		if err := json.Unmarshal(built, &rec); err != nil {
			t.Fatal(err)
		}
		if bad := leanViolations(rec); len(bad) != 0 {
			t.Errorf("%s: not lean: %v", c.Name, bad)
		}
	}
	dirty := map[string]any{
		"class_name": "API Activity", "observables": []any{}, "enrichments": []any{},
		"severity": "Informational", "status": "Success", "caption": "x",
		"actor":          map[string]any{"user": map[string]any{"type": "System"}, "user_name": "x"},
		"metadata":       map[string]any{"product": map[string]any{"vendor_name": "v"}},
		"authorizations": []any{map[string]any{"decision": "Allowed"}},
		"policy":         map[string]any{"decision": "Allowed"},
	}
	want := []string{"/class_name", "/observables", "/enrichments", "/severity", "/status", "/caption", "/actor/user/type", "/actor/user_name", "/metadata/product/vendor_name", "/policy/decision"}
	got := leanViolations(dirty)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

// delegationUses lists where a record has a delegation object, and the profiles it declares other than security_control
// and host (Application Lifecycle takes `actor` from it).
func delegationUses(v any, path string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := path + "/" + k
			if k == "delegation" {
				out = append(out, p)
			}
			if p == "/metadata/profiles" {
				if list, ok := e.([]any); ok {
					for _, pr := range list {
						if pr != "security_control" && pr != "host" {
							out = append(out, p+"="+pr.(string))
						}
					}
				}
			}
			out = append(out, delegationUses(e, p)...)
		}
	case []any:
		for _, e := range x {
			out = append(out, delegationUses(e, path+"[]")...)
		}
	}
	return out
}

// No record uses the delegation object or declares the ai_operation profile: the records the emitter builds from the
// inputs of the record vectors declare security_control, or host for a lifecycle fact, and no other. The check is run on a record that has both, so it is not
// vacuous.
//
// verifies: REQ-AUDIT-124
func TestRecordVectorsUseNoDelegationAndNoAIProfile(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.Cases) == 0 {
		t.Fatal("the vector file is empty")
	}
	for _, c := range f.Cases {
		built := c.build(t)
		var rec any
		if err := json.Unmarshal(built, &rec); err != nil {
			t.Fatal(err)
		}
		if bad := delegationUses(rec, ""); len(bad) != 0 {
			t.Errorf("%s: %v", c.Name, bad)
		}
	}
	var dirty any
	if err := json.Unmarshal([]byte(`{"delegation":{},"metadata":{"profiles":["security_control","ai_operation"]}}`), &dirty); err != nil {
		t.Fatal(err)
	}
	got := delegationUses(dirty, "")
	slices.Sort(got)
	if want := []string{"/delegation", "/metadata/profiles=ai_operation"}; !slices.Equal(got, want) {
		t.Errorf("flagged %v, want %v", got, want)
	}
}

// verifies: REQ-AUDIT-046, REQ-AUDIT-107
func TestRefusalVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	base := Event{Action: Action{Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}, Status: StatusSuccess, Caller: "signer", Operation: "Sign"}
	if len(f.Refusals) == 0 {
		t.Fatal("no refusal vectors")
	}
	for _, r := range f.Refusals {
		t.Run(r.Name, func(t *testing.T) {
			ev := base
			ev.ProductObject = sluisFrom(t, r.Sluis)
			err := check(ev)
			if err == nil {
				t.Fatal("accepted")
			}
			if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
				t.Errorf("code = %v", code)
			}
			if reason, _ := refusal.Reason(err); reason != r.Reason {
				t.Errorf("reason = %q, want %q", reason, r.Reason)
			}
			if path, _ := refusal.FieldPath(err); path != r.Path {
				t.Errorf("path = %q, want %q", path, r.Path)
			}
			// An error never shows a value.
			if strings.Contains(err.Error(), "example") {
				t.Errorf("the error shows a value: %v", err)
			}
		})
	}
	for _, a := range f.Accepted {
		ev := base
		ev.ProductObject = sluisFrom(t, a.Sluis)
		if err := check(ev); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
}

func TestRecordIsRefusedAboveItsBound(t *testing.T) {
	// 20 strings of 1,024 bytes are each within bounds and together above 16 KiB.
	sluis := map[string]any{}
	for i := range 20 {
		sluis["k"+strconv.Itoa(i)] = strings.Repeat("a", StringMax)
	}
	ev := Event{Action: Action{Name: "x", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}, Status: StatusSuccess, ProductObject: sluis, Caller: "signer", Operation: "Sign"}
	_, err := BuildRecord(RecordInput{Component: "a", Scope: "dev", Release: "r", UID: "u", Event: ev})
	if reason, _ := refusal.Reason(err); reason != "bound" {
		t.Fatalf("err = %v", err)
	}
	if path, _ := refusal.FieldPath(err); path != "record" {
		t.Fatalf("path = %q", path)
	}
}

func TestEventRefusalVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.EventRefusals) == 0 || len(f.EventAccepted) == 0 {
		t.Fatal("no event vectors")
	}
	for _, r := range f.EventRefusals {
		t.Run(r.Name, func(t *testing.T) {
			err := check(r.Event.event(t))
			if err == nil {
				t.Fatal("accepted")
			}
			if reason, _ := refusal.Reason(err); reason != r.Reason {
				t.Errorf("reason = %q, want %q", reason, r.Reason)
			}
			if path, _ := refusal.FieldPath(err); path != r.Path {
				t.Errorf("path = %q, want %q", path, r.Path)
			}
		})
	}
	for _, a := range f.EventAccepted {
		if err := check(a.Event.event(t)); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
}

func TestPseudonymInputVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.MacInputs) == 0 || f.MacLabel != PseudonymLabel {
		t.Fatalf("label %q, %d inputs", f.MacLabel, len(f.MacInputs))
	}
	seen := map[string]string{}
	for _, v := range f.MacInputs {
		got := hex.EncodeToString(pseudonymInput(v.Boundary, Subject{Tenant: v.Tenant, Kind: v.Kind, Value: v.Value}))
		if got != v.InputHex {
			t.Errorf("input of %+v = %s, want %s", v, got, v.InputHex)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%+v and %s give the same input", v, other)
		}
		seen[got] = v.Boundary + "/" + v.Tenant
	}
}
