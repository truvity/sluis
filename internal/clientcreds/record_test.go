package clientcreds

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
)

func TestGenerate(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 64 {
		s, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 43 {
			t.Errorf("len = %d, want 43 (32 bytes, base64url, no padding): %q", len(s), s)
		}
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("%q is outside the base64url alphabet or padded", s)
		}
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(raw) != 32 {
			t.Errorf("%q does not decode to 32 bytes: %d, %v", s, len(raw), err)
		}
		if seen[s] {
			t.Errorf("%q was generated twice", s)
		}
		seen[s] = true
	}
}

func TestPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, want string }{
		{"grafana", "credentials/oidc-client/grafana/secret"},
		{"Argo_CD.v2-x", "credentials/oidc-client/Argo_CD.v2-x/secret"},
		// Not a segment the layout can hold, or one that would shadow an
		// encoded id: spelled u- and the bytes in hex.
		{"", "credentials/oidc-client/u-/secret"},
		{".", "credentials/oidc-client/u-2e/secret"},
		{"..", "credentials/oidc-client/u-2e2e/secret"},
		{"has space", "credentials/oidc-client/u-686173207370616365/secret"},
		{`a\b`, "credentials/oidc-client/u-615c62/secret"},
		{"a:b", "credentials/oidc-client/u-613a62/secret"},
		{"é", "credentials/oidc-client/u-c3a9/secret"},
		{"u-ab", "credentials/oidc-client/u-752d6162/secret"},
	} {
		got := Path(tc.id)
		if got != tc.want {
			t.Errorf("Path(%q) = %q, want %q", tc.id, got, tc.want)
		}
		if err := port.CheckSecretPath(got); err != nil {
			t.Errorf("Path(%q) = %q is not a path the port accepts: %v", tc.id, got, err)
		}
	}
}

func TestPathNeverSharesAPathBetweenTwoIds(t *testing.T) {
	t.Parallel()
	// A client literally named "u-752d6162" must not land where the encoding
	// of "u-ab" lands: ids that start with u- are always encoded.
	ids := []string{"ab", "u-ab", "u-" + "752d6162", "", "u-", ".", "..", "x y", "x:y", `x\y`}
	seen := map[string]string{}
	for _, id := range ids {
		p := Path(id)
		if other, dup := seen[p]; dup {
			t.Errorf("%q and %q share %q", id, other, p)
		}
		seen[p] = id
	}
}

func TestRecordEncodeDecode(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rotated := created.Add(time.Hour)
	valid := rotated.Add(24 * time.Hour)
	in := Record{V: RecordVersion, Current: "new", Previous: "old", PreviousValidUntil: valid, Created: created, Rotated: rotated}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

func TestRecordEncodeDefaultsTheVersionAndOmitsEmptyFields(t *testing.T) {
	t.Parallel()
	b, err := Record{Current: "c", Created: time.Unix(0, 0).UTC()}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"v":1`) {
		t.Errorf("the version was not defaulted: %s", s)
	}
	for _, key := range []string{"previous", "previous_valid_until", "rotated"} {
		if strings.Contains(s, key) {
			t.Errorf("%s: an empty %s was written", s, key)
		}
	}
}

func TestRecordEncodeRefusesAnEmptyCurrent(t *testing.T) {
	t.Parallel()
	if _, err := (Record{V: RecordVersion}).Encode(); err == nil {
		t.Error("a record with no current secret was encoded")
	}
}

func TestDecodeRecordRefusals(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not json":        `current-secret-value`,
		"wrong shape":     `["x"]`,
		"no version":      `{"current":"leaky-value"}`,
		"a future":        `{"v":2,"current":"leaky-value"}`,
		"version zero":    `{"v":0,"current":"leaky-value"}`,
		"empty current":   `{"v":1,"current":""}`,
		"missing current": `{"v":1}`,
	} {
		_, err := DecodeRecord([]byte(body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "leaky-value") || strings.Contains(err.Error(), "current-secret-value") {
			t.Errorf("%s: the error holds a value: %v", name, err)
		}
	}
}
