package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/modcall"
)

// releasedDir holds, per release, one file per method: the envelope a caller of
// that release wrote and the answer it got, for the current version and for the
// one before it. A later release replays every file, so a change to the wire
// that an older caller cannot read fails here instead of in a deployment where
// the two sides differ by one version. The pattern is that of
// internal/audit/catalogue/testdata/released: a file is never edited after its
// release.
const releasedDir = "../../modcall/testdata/released"

type golden struct {
	Module     string          `json:"module"`
	Method     string          `json:"method"`
	Caller     string          `json:"caller"`
	Request    json.RawMessage `json:"request"`
	RequestV1  json.RawMessage `json:"request_v1"`
	Response   json.RawMessage `json:"response"`
	ResponseV1 json.RawMessage `json:"response_v1"`
}

func dispatch(t *testing.T, raw json.RawMessage, caller string) json.RawMessage {
	t.Helper()
	var req modcall.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, &fake{})
	out, err := json.Marshal(s.Dispatch(modcall.WithCaller(context.Background(), caller), req))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameJSON(t *testing.T, name string, got, want json.RawMessage) {
	t.Helper()
	var a, b bytes.Buffer
	if err := json.Indent(&a, got, "", " "); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := json.Indent(&b, want, "", " "); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if a.String() != b.String() {
		t.Errorf("%s: the answer changed\n got: %s\nwant: %s", name, got, want)
	}
}

// TestEveryReleasedEnvelopeStillDecodesAndIsAnsweredAsBefore replays the files.
func TestEveryReleasedEnvelopeStillDecodesAndIsAnsweredAsBefore(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(releasedDir, "v*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g golden
		if err = json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if g.Module != rpc.Module {
			continue
		}
		seen[g.Method]++
		sameJSON(t, f+" (v2)", dispatch(t, g.Request, g.Caller), g.Response)
		sameJSON(t, f+" (v1)", dispatch(t, g.RequestV1, g.Caller), g.ResponseV1)
	}
	// A sweep that finds nothing proves nothing.
	for _, m := range []string{rpc.MethodMint, rpc.MethodGranted} {
		if seen[m] == 0 {
			t.Errorf("no released golden for %s.%s under %s", rpc.Module, m, releasedDir)
		}
	}
}

// TestWriteMissingGoldens writes the files of the version in SLUIS_GOLDEN_DIR
// that do not exist yet. It never overwrites one: a released file is history.
func TestWriteMissingGoldens(t *testing.T) {
	dir := os.Getenv("SLUIS_GOLDEN_DIR")
	if dir == "" {
		t.Skip("set SLUIS_GOLDEN_DIR to write the goldens of a new release")
	}
	caller := rpc.Caller{Actor: audit.Person("a@example.com"), Groups: []string{"ops"}}
	mint, _ := json.Marshal(struct {
		Preset   string        `json:"preset"`
		Caller   rpc.Caller    `json:"caller"`
		Lifetime time.Duration `json:"lifetime"`
	}{"r2", caller, 10 * time.Minute})
	granted, _ := json.Marshal(struct {
		Caller rpc.Caller `json:"caller"`
	}{caller})
	for _, c := range []struct {
		method, class string
		payload       []byte
	}{{rpc.MethodMint, rpc.CallerIssuer, mint}, {rpc.MethodGranted, rpc.CallerConsole, granted}} {
		path := filepath.Join(dir, rpc.Module+"."+c.method+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		env := func(v int) json.RawMessage {
			r := modcall.Request{V: v, Kind: modcall.Kind, Module: rpc.Module, Method: c.method, Payload: c.payload}
			if v >= 2 {
				r.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
				r.Deadline = 4102444800000
			}
			b, _ := json.Marshal(r)
			return b
		}
		g := golden{Module: rpc.Module, Method: c.method, Caller: c.class, Request: env(2), RequestV1: env(0)}
		g.Response, g.ResponseV1 = dispatch(t, g.Request, c.class), dispatch(t, g.RequestV1, c.class)
		out, _ := json.MarshalIndent(g, "", "  ")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
