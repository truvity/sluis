package githubtokens_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
)

// releasedDir holds, per release, one file per method: the envelope a caller of
// that release wrote and the answer it got, for the current protocol version and
// for the one before it. A later release replays every file, so a change to the
// wire that an older caller cannot read fails here instead of in a deployment
// where the two sides differ by one version. A file is never edited after its
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
	out, err := json.Marshal(server(&fake{}).Dispatch(modcall.WithCaller(context.Background(), caller), req))
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
	seen := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g golden
		if err = json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if g.Module != githubtokens.Module {
			continue
		}
		seen++
		sameJSON(t, f+" (v2)", dispatch(t, g.Request, g.Caller), g.Response)
		sameJSON(t, f+" (v1)", dispatch(t, g.RequestV1, g.Caller), g.ResponseV1)
	}
	// A sweep that finds nothing proves nothing.
	if seen == 0 {
		t.Errorf("no released golden for %s.%s under %s", githubtokens.Module, githubtokens.MethodMintInstallation, releasedDir)
	}
}

// TestWriteMissingGoldens writes the file of the version in SLUIS_GOLDEN_DIR
// if it does not exist yet. It never overwrites one: a released file is history.
func TestWriteMissingGoldens(t *testing.T) {
	dir := os.Getenv("SLUIS_GOLDEN_DIR")
	if dir == "" {
		t.Skip("set SLUIS_GOLDEN_DIR to write the goldens of a new release")
	}
	path := filepath.Join(dir, githubtokens.Module+"."+githubtokens.MethodMintInstallation+".json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	payload, _ := json.Marshal(request)
	env := func(v int) json.RawMessage {
		r := modcall.Request{V: v, Kind: modcall.Kind, Module: githubtokens.Module, Method: githubtokens.MethodMintInstallation, Payload: payload}
		if v >= 2 {
			r.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
			r.Deadline = 4102444800000
		}
		b, _ := json.Marshal(r)
		return b
	}
	g := golden{Module: githubtokens.Module, Method: githubtokens.MethodMintInstallation, Caller: githubtokens.CallerIssuer,
		Request: env(2), RequestV1: env(0)}
	g.Response, g.ResponseV1 = dispatch(t, g.Request, g.Caller), dispatch(t, g.RequestV1, g.Caller)
	out, _ := json.MarshalIndent(g, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
