package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/atoom/common/refusal"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	keyv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/key/v2alpha1"
	"github.com/truvity/sluis/v2/gen/go/sluis/key/v2alpha1/keyv2alpha1connect"
	"github.com/truvity/sluis/v2/kits/go/rpc"
)

var fixedNow = time.UnixMilli(1793750400000) // 2026-11-04T00:00:00Z

// fakeTransport records what the emitter sent and answers with err.
type fakeTransport struct {
	transportv1alpha1connect.TransportServiceClient
	mu   sync.Mutex
	reqs []*transportv1alpha1.EmitRequest
	err  error
}

func (f *fakeTransport) Emit(_ context.Context, r *connect.Request[transportv1alpha1.EmitRequest]) (*connect.Response[transportv1alpha1.EmitResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Msg)
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&transportv1alpha1.EmitResponse{}), nil
}

// fakeKeys answers Mac with a fixed value and records the request.
type fakeKeys struct {
	keyv2alpha1connect.KeyServiceClient
	mu   sync.Mutex
	reqs []*keyv2alpha1.MacRequest
	mac  []byte
	err  error
}

func (f *fakeKeys) Mac(_ context.Context, r *connect.Request[keyv2alpha1.MacRequest]) (*connect.Response[keyv2alpha1.MacResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Msg)
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&keyv2alpha1.MacResponse{Mac: f.mac}), nil
}

// hmacKeys is a key@ that holds one key and answers Mac with the HMAC-SHA384 of the input under it, as key@ does under
// a tenant's pseudonym key (REQ-AUDIT-127 proves the real one), so the pseudonym depends on the key and the input.
type hmacKeys struct {
	keyv2alpha1connect.KeyServiceClient
	key []byte
}

func (f hmacKeys) Mac(_ context.Context, r *connect.Request[keyv2alpha1.MacRequest]) (*connect.Response[keyv2alpha1.MacResponse], error) {
	h := hmac.New(sha512.New384, f.key)
	h.Write(r.Msg.GetInput())
	return connect.NewResponse(&keyv2alpha1.MacResponse{Mac: h.Sum(nil)}), nil
}

func testMAC() []byte {
	mac := make([]byte, 48)
	for i := range mac {
		mac[i] = byte(i)
	}
	return mac
}

func newTestEmitter(t *testing.T, tr transportv1alpha1connect.TransportServiceClient, keys keyv2alpha1connect.KeyServiceClient) *Emitter {
	t.Helper()
	e, err := New(Config{Actor: "key", Boundary: "dev", Release: "0.0.0-test", Transport: tr, Keys: keys, Table: openTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var signEvent = Event{Action: Action{Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}, Status: StatusSuccess, Caller: "signer", Operation: "Sign"}

// verifies: REQ-AUDIT-128
func TestPseudonymComesFromTheTenantsPseudonymKey(t *testing.T) {
	keys := &fakeKeys{mac: testMAC()}
	e := newTestEmitter(t, &fakeTransport{}, keys)
	ev := signEvent
	ev.Tenant = "acme"
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "person-123"}
	rec, uid, err := e.Record(rpc.WithCorrelationID(context.Background(), "req-1"), ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.reqs) != 1 {
		t.Fatalf("Mac calls = %d", len(keys.reqs))
	}
	m := keys.reqs[0]
	wantInput := pseudonymInput("dev", Subject{Tenant: "acme", Kind: "person", Value: "person-123"})
	if m.GetPurpose() != "pseudonym" || m.GetTenant() != "acme" || m.GetLabel() != "sluis-pseudonym-v1" || !bytes.Equal(m.GetInput(), wantInput) || m.GetEpoch() != "202611" {
		t.Errorf("Mac request = %v", m)
	}
	var got struct {
		Resources []struct{ UID string } `json:"resources"`
		Metadata  struct {
			UID            string `json:"uid"`
			CorrelationUID string `json:"correlation_uid"`
		} `json:"metadata"`
		Time int64 `json:"time"`
	}
	if err := json.Unmarshal(rec, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 1 || got.Resources[0].UID != "p1.202611.AAECAwQFBgcICQoLDA0ODw" {
		t.Errorf("resources = %v: an API Activity has no user, the subject is its resource", got.Resources)
	}
	if got.Time != fixedNow.UnixMilli() {
		t.Errorf("time = %d", got.Time)
	}
	if got.Metadata.UID != uid || got.Metadata.CorrelationUID != "req-1" {
		t.Errorf("metadata = %+v, uid %s", got.Metadata, uid)
	}
	// The raw subject is nowhere in the record.
	if bytes.Contains(rec, []byte("person-123")) {
		t.Error("the raw subject is in the record")
	}
}

var pseudonymRE = regexp.MustCompile(`^p1\.202611\.[A-Za-z0-9_-]{22}$`)

// An external actor (a person, a workload, a client) is recorded under a pseudonym that key@ computes with a key, and
// under nothing else: the raw identifier is in neither the record nor the message handed to the transport, the
// pseudonym changes with the key, the tenant, the kind and the identifier, and an event with no subject records none.
//
// verifies: REQ-AUDIT-023
func TestAnExternalActorIsRecordedUnderAKeyedPseudonym(t *testing.T) {
	keyA, keyB := []byte("tenant key A"), []byte("tenant key B")
	subjects := []Subject{
		{Tenant: "acme", Kind: "person", Value: "person-123"},
		{Tenant: "acme", Kind: "person", Value: "ada.lovelace@example.org"}, // an e-mail address is a fine subject: it is hashed
		{Tenant: "acme", Kind: "workload", Value: "spiffe://example.org/ns/pay/sa/api"},
		{Tenant: "acme", Kind: "client", Value: "client-abc"},
	}
	emitWith := func(t *testing.T, key []byte, s Subject) (rec []byte, msg []byte) {
		t.Helper()
		tr := &fakeTransport{}
		e := newTestEmitter(t, tr, hmacKeys{key: key})
		ev := signEvent
		ev.Subject = &s
		if err := e.Emit(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		if len(tr.reqs) != 1 {
			t.Fatalf("transport got %d messages", len(tr.reqs))
		}
		wire, err := proto.Marshal(tr.reqs[0])
		if err != nil {
			t.Fatal(err)
		}
		return tr.reqs[0].GetMessage().GetRecord(), wire
	}
	pseudonymOf := func(t *testing.T, rec []byte) string {
		t.Helper()
		var got struct {
			Resources []struct{ UID string } `json:"resources"`
		}
		if err := json.Unmarshal(rec, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Resources) != 1 {
			t.Fatalf("resources = %v, want the subject", got.Resources)
		}
		return got.Resources[0].UID
	}

	seen := map[string]Subject{}
	for _, s := range subjects {
		t.Run(s.Kind+"/"+s.Value, func(t *testing.T) {
			rec, wire := emitWith(t, keyA, s)
			got := pseudonymOf(t, rec)
			if !pseudonymRE.MatchString(got) {
				t.Fatalf("user.uid = %q, want p1.<YYYYMM>.<128 bits, base64url>", got)
			}
			// Computed here from the key and the 0029 input: p1.<epoch>.<first 128 bits of HMAC-SHA384>.
			h := hmac.New(sha512.New384, keyA)
			h.Write(pseudonymInput("dev", s))
			if want := "p1.202611." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16]); got != want {
				t.Errorf("user.uid = %q, want %q", got, want)
			}
			// Nothing of the raw identifier, in the record or in the whole message.
			for _, raw := range []string{s.Value, strings.SplitN(s.Value, "@", 2)[0]} {
				if bytes.Contains(rec, []byte(raw)) || bytes.Contains(wire, []byte(raw)) {
					t.Errorf("%q is in what was sent", raw)
				}
			}
			if other, dup := seen[got]; dup {
				t.Errorf("%+v and %+v share the pseudonym %s", s, other, got)
			}
			seen[got] = s
			// The same key and subject give the same pseudonym; another key gives another one: it is keyed.
			again, _ := emitWith(t, keyA, s)
			if pseudonymOf(t, again) != got {
				t.Error("the same key and subject gave two pseudonyms")
			}
			other, _ := emitWith(t, keyB, s)
			if pseudonymOf(t, other) == got {
				t.Error("another key gave the same pseudonym: it is not keyed")
			}
		})
	}

	t.Run("tenant and kind", func(t *testing.T) {
		base := Subject{Tenant: "acme", Kind: "person", Value: "x"}
		a, _ := emitWith(t, keyA, base)
		for name, s := range map[string]Subject{
			"tenant": {Tenant: "globex", Kind: "person", Value: "x"},
			"kind":   {Tenant: "acme", Kind: "client", Value: "x"},
		} {
			b, _ := emitWith(t, keyA, s)
			if pseudonymOf(t, a) == pseudonymOf(t, b) {
				t.Errorf("another %s gave the same pseudonym", name)
			}
		}
	})

	t.Run("no subject records no user", func(t *testing.T) {
		keys := &fakeKeys{mac: testMAC()}
		tr := &fakeTransport{}
		e := newTestEmitter(t, tr, keys)
		if err := e.Emit(context.Background(), signEvent); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(tr.reqs[0].GetMessage().GetRecord(), &got); err != nil {
			t.Fatal(err)
		}
		if _, has := got["user"]; has || len(keys.reqs) != 0 {
			t.Errorf("user = %v, Mac calls = %d", got["user"], len(keys.reqs))
		}
	})
}

// verifies: REQ-AUDIT-023
func TestSubjectWithoutKeysIsAnErrorAndNothingIsSent(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, nil)
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "person-123"}
	for name, call := range map[string]func() error{
		"Record":       func() error { _, _, err := e.Record(context.Background(), ev); return err },
		"Emit":         func() error { return e.Emit(context.Background(), ev) },
		"EmitBlocking": func() error { return e.EmitBlocking(context.Background(), ev) },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: a subject without Keys was accepted", name)
		}
		if reason, _ := refusal.Reason(err); reason != "keys" {
			t.Errorf("%s: reason = %q (%v)", name, reason, err)
		}
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent")
	}
}

func TestKeyFailureStopsTheEmit(t *testing.T) {
	keys := &fakeKeys{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, keys)
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "p"}
	err := e.Emit(context.Background(), ev)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err = %v", err)
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent without a pseudonym")
	}
}

// verifies: REQ-AUDIT-188
//
// A refusal that is worth more than the name of its subject is written when key@ cannot make the pseudonym: the record goes
// without a subject, and an Authentication says `unknown` in the user, where without the flag the emit fails and nothing is
// sent. The fact is not lost, so nothing is counted.
func TestARecordWhoseSubjectIsBestEffortIsWrittenWhenKeyCannotNameIt(t *testing.T) {
	keys := &fakeKeys{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	tr := &fakeTransport{}
	e, err := New(Config{Actor: "session", Boundary: "dev", Release: "0.0.0-test", Transport: tr, Keys: keys, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{Action: Action{Name: "session.signin", ClassUID: 3002, ActivityID: 1}, Status: StatusFailure, StatusCode: "client", Tenant: "acme"}
	ev.Subject = &Subject{Tenant: "acme", Kind: "client", Value: "c"}
	if err := e.Emit(context.Background(), ev); err == nil {
		t.Fatal("the emit of a subject that must be named did not fail")
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent without a pseudonym")
	}
	ev.SubjectBestEffort = true
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("a best-effort subject: %v", err)
	}
	if len(tr.reqs) != 1 {
		t.Fatalf("requests = %d, want the record", len(tr.reqs))
	}
	var rec struct {
		User struct {
			UID    string `json:"uid"`
			TypeID int    `json:"type_id"`
		} `json:"user"`
		Unmapped *struct{} `json:"unmapped"`
	}
	if err := json.Unmarshal(tr.reqs[0].GetMessage().GetRecord(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.User.UID != "unknown" || rec.User.TypeID != 0 {
		t.Errorf("user = %+v, want unknown with type_id 0", rec.User)
	}
	// Only the first, failed emit is a lost fact; the second was written.
	if got := e.Lost(); got != 1 {
		t.Errorf("lost = %d, want 1 (the emit that failed)", got)
	}
}

// verifies: REQ-AUDIT-188
//
// A subject is optional only on a refusal: the flag on a success event is refused, and nothing is sent, because a success that
// went without the name of its subject would say less than it should and nobody would be told.
func TestASuccessCannotLeaveItsSubjectBestEffort(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, &fakeKeys{mac: testMAC()})
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "p"}
	ev.SubjectBestEffort = true
	err := e.Emit(context.Background(), ev)
	reason, _ := refusal.Reason(err)
	path, _ := refusal.FieldPath(err)
	if err == nil || reason != "schema" || path != "subject_best_effort" {
		t.Fatalf("err = %v (reason %q, path %q), want schema at subject_best_effort", err, reason, path)
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent")
	}
	ev.Status, ev.StatusCode = StatusFailure, "grant"
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("a failure with the flag: %v", err)
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	ok := Config{Actor: "key", Boundary: "dev", Release: "r", Transport: &fakeTransport{}, Table: openTable(t)}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"no actor": func(c *Config) { c.Actor = "" }, "no boundary": func(c *Config) { c.Boundary = "" },
		"no release": func(c *Config) { c.Release = "" }, "no transport": func(c *Config) { c.Transport = nil },
	} {
		c := ok
		mod(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// pseudonymInput is the Mac input of a pseudonym as audit's record 0011 ("Pseudonyms") lays it out: the boundary (audit's
// scope), the tenant, the kind and the identifier, each with a 4-byte big-endian length in front. The mac_inputs vectors
// hold it to the bytes key@ is asked for.
func pseudonymInput(boundary string, s Subject) []byte {
	var b []byte
	for _, part := range []string{boundary, s.Tenant, s.Kind, s.Value} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(part))) //nolint:gosec // every part is at most StringMax bytes
		b = append(b, part...)
	}
	return b
}
