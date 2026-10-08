package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clientsIssuer is an issuer's operator endpoint: it keeps what it was asked
// and answers with what the test set.
type clientsIssuer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	reply  string
	paths  []string
	auth   []string
	types  []string
	bodies []map[string]any
}

func newClientsIssuer(t *testing.T, status int, reply string) *clientsIssuer {
	t.Helper()
	c := &clientsIssuer{status: status, reply: reply}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the body is not JSON: %q", raw)
		}
		c.mu.Lock()
		c.paths = append(c.paths, r.Method+" "+r.URL.Path)
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.types = append(c.types, r.Header.Get("Content-Type"))
		c.bodies = append(c.bodies, body)
		status, reply := c.status, c.reply
		c.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, reply, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(c.Close)
	signedInWith(t, c.URL, Session{RefreshToken: "a-refresh", AccessToken: "the-bearer", AccessExpires: time.Now().Add(time.Hour)})
	return c
}

const rotatedReply = `{"client":"grafana","rotated":"2026-10-07T10:00:00Z","overlap_seconds":3600,` +
	`"previous_valid_until":"2026-10-07T11:00:00Z","discarded_previous":false}`

func TestClientsRotateTakesTheIdBeforeOrAfterTheFlags(t *testing.T) {
	for name, args := range map[string]func(url string) []string{
		"id first": func(u string) []string {
			return []string{"clients", "rotate", "grafana", "--issuer", u, "--overlap", "1h"}
		},
		"id last": func(u string) []string {
			return []string{"clients", "rotate", "--issuer", u, "--overlap", "1h", "grafana"}
		},
	} {
		issuer := newClientsIssuer(t, http.StatusOK, rotatedReply)
		out, err := captureStdoutErr(t, func() error { return run(args(issuer.URL)) })
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(issuer.paths) != 1 || issuer.paths[0] != "POST /.access/client-secrets/rotate" {
			t.Errorf("%s: requests %v", name, issuer.paths)
			continue
		}
		if issuer.auth[0] != "Bearer the-bearer" || issuer.types[0] != "application/json" {
			t.Errorf("%s: auth %q type %q", name, issuer.auth[0], issuer.types[0])
		}
		if issuer.bodies[0]["client"] != "grafana" || issuer.bodies[0]["overlap_seconds"] != float64(3600) || len(issuer.bodies[0]) != 2 {
			t.Errorf("%s: body %v", name, issuer.bodies[0])
		}
		if !strings.Contains(out, "rotated the secret of grafana at 2026-10-07T10:00:00Z") ||
			!strings.Contains(out, "the old secret still works until 2026-10-07T11:00:00Z") {
			t.Errorf("%s: output %q", name, out)
		}
	}
}

// `rotate --issuer URL grafana --overlap 0`: the flag package stops at the first
// word that is not a flag, so what follows the id is left over. A hard cut asked
// for after a leak must not be sent as the default 24h overlap, without a word.
func TestClientsRotateDoesNotDropFlagsAfterTheId(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, rotatedReply)
	_, err := captureStdoutErr(t, func() error {
		return run([]string{"clients", "rotate", "--issuer", issuer.URL, "grafana", "--overlap", "0"})
	})
	if err != nil {
		if codeFor(err) != exitUsage {
			t.Errorf("err = %v", err)
		}
		return
	}
	if got, ok := issuer.bodies[0]["overlap_seconds"]; !ok || got != float64(0) {
		t.Errorf("the overlap asked for was dropped: body %v", issuer.bodies[0])
	}
}

func TestClientsRotateOverlapBounds(t *testing.T) {
	for name, tc := range map[string]struct {
		overlap string
		sent    any // what overlap_seconds is in the body; nil is absent
		usage   bool
	}{
		"absent is the issuer's default": {"", nil, false},
		"zero is a hard cut":             {"0", float64(0), false},
		"zero seconds":                   {"0s", float64(0), false},
		"a day":                          {"24h", float64(86400), false},
		"the maximum":                    {"168h", float64(604800), false},
		"a second over":                  {"168h1s", nil, true},
		"over a week":                    {"169h", nil, true},
		"negative":                       {"-1h", nil, true},
		"not a duration":                 {"soon", nil, true},
		"a bare number of seconds":       {"3600", nil, true},
	} {
		issuer := newClientsIssuer(t, http.StatusOK, rotatedReply)
		args := []string{"clients", "rotate", "grafana", "--issuer", issuer.URL}
		if tc.overlap != "" {
			args = append(args, "--overlap", tc.overlap)
		}
		_, err := captureStdoutErr(t, func() error { return run(args) })
		if tc.usage {
			if err == nil || codeFor(err) != exitUsage {
				t.Errorf("%s: err %v code %d, want a usage error", name, err, codeFor(err))
			}
			if len(issuer.paths) != 0 {
				t.Errorf("%s: the issuer was asked: %v", name, issuer.paths)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got, present := issuer.bodies[0]["overlap_seconds"]
		if (tc.sent == nil) == present || (present && got != tc.sent) {
			t.Errorf("%s: overlap_seconds = %v (present %v), want %v", name, got, present, tc.sent)
		}
	}
}

func TestClientsUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":         {"clients"},
		"an unknown subcommand": {"clients", "frobnicate", "grafana"},
		"no id":                 {"clients", "rotate", "--overlap", "1h"},
		"a blank id":            {"clients", "show", " "},
		"two ids":               {"clients", "show", "grafana", "metabase"},
		"overlap on show":       {"clients", "show", "grafana", "--overlap", "1h"},
		"overlap on purge":      {"clients", "purge", "grafana", "--overlap", "1h"},
		"an unknown flag":       {"clients", "rotate", "grafana", "--force"},
	} {
		issuer := newClientsIssuer(t, http.StatusOK, `{}`)
		_, err := captureStdoutErr(t, func() error { return run(append(args, "--issuer", issuer.URL)) })
		if err == nil || codeFor(err) != exitUsage {
			t.Errorf("%s: err %v code %d", name, err, codeFor(err))
		}
		if len(issuer.paths) != 0 {
			t.Errorf("%s: the issuer was asked: %v", name, issuer.paths)
		}
	}
}

func TestClientsExitCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		reply  string
		code   int
		text   string
	}{
		"not signed in":     {http.StatusUnauthorized, "a bearer token is required", exitNotSignedIn, "not signed in"},
		"not an operator":   {http.StatusForbidden, "managing client secrets is an operator's", exitNotGranted, "operator"},
		"not generated":     {http.StatusUnprocessableEntity, "that client does not have `secret: {generate: true}`", 1, "generate: true"},
		"still declared":    {http.StatusUnprocessableEntity, "that client is still a generated client of the policy", 1, "still a generated client"},
		"no stored secret":  {http.StatusNotFound, "that client has no stored secret", 1, "no stored secret"},
		"busy":              {http.StatusConflict, "the client's secret is being changed; try again", 1, "try again"},
		"a bad request":     {http.StatusBadRequest, "the overlap is between 0 and 7 days", exitUsage, "overlap"},
		"the issuer failed": {http.StatusInternalServerError, "the secret could not be changed", 1, "500"},
	} {
		for _, sub := range []string{"rotate", "show", "purge"} {
			issuer := newClientsIssuer(t, tc.status, tc.reply)
			_, err := captureStdoutErr(t, func() error { return run([]string{"clients", sub, "grafana", "--issuer", issuer.URL}) })
			if err == nil {
				t.Errorf("%s %s: no error", sub, name)
				continue
			}
			if codeFor(err) != tc.code {
				t.Errorf("%s %s: code %d, want %d (%v)", sub, name, codeFor(err), tc.code, err)
			}
			if !strings.Contains(err.Error(), tc.text) {
				t.Errorf("%s %s: error %q lacks %q", sub, name, err, tc.text)
			}
		}
	}
}

func TestClientsAnIssuerThatCannotBeReachedIsRetryable(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, `{}`)
	url := issuer.URL
	issuer.Close()
	_, err := captureStdoutErr(t, func() error { return run([]string{"clients", "show", "grafana", "--issuer", url}) })
	if err == nil || codeFor(err) != exitUnreachable {
		t.Errorf("err %v code %d, want unreachable", err, codeFor(err))
	}
}

func TestClientsShowPrintsMetadataAndNoSecret(t *testing.T) {
	// An issuer that, wrongly, sent a value must still not have it printed.
	issuer := newClientsIssuer(t, http.StatusOK, `{"client":"grafana","exists":true,"generated":true,`+
		`"created":"2026-10-01T09:00:00Z","rotated":"2026-10-06T09:00:00Z","has_previous":true,`+
		`"previous_valid_until":"2026-10-07T09:00:00Z","previous_active":true,`+
		`"current":"leaky-current","previous":"leaky-previous","secret":"leaky-secret"}`)
	out, err := captureStdoutErr(t, func() error { return run([]string{"clients", "show", "grafana", "--issuer", issuer.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "leaky") {
		t.Errorf("a secret value was printed: %q", out)
	}
	for _, want := range []string{"grafana", "2026-10-01T09:00:00Z", "2026-10-06T09:00:00Z", "2026-10-07T09:00:00Z", "previous still valid"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if issuer.bodies[0]["client"] != "grafana" || len(issuer.bodies[0]) != 1 || issuer.paths[0] != "POST /.access/client-secrets/show" {
		t.Errorf("request %v %v", issuer.paths, issuer.bodies)
	}
}

func TestClientsShowOfAClientWithNoRecordAndOfAnOrphan(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, `{"client":"fresh","exists":false,"generated":true,"has_previous":false,"previous_active":false}`)
	out, err := captureStdoutErr(t, func() error { return run([]string{"clients", "show", "fresh", "--issuer", issuer.URL}) })
	if err != nil || !strings.Contains(out, "fresh has no stored secret (generated in the policy: true)") {
		t.Errorf("%q, %v", out, err)
	}
	issuer.mu.Lock()
	issuer.reply = `{"client":"gone","exists":true,"generated":false,"created":"2026-10-01T09:00:00Z","orphaned":"2026-10-05T09:00:00Z"}`
	issuer.mu.Unlock()
	out, err = captureStdoutErr(t, func() error { return run([]string{"clients", "show", "gone", "--issuer", issuer.URL}) })
	if err != nil || !strings.Contains(out, "orphaned since") || !strings.Contains(out, "2026-10-05T09:00:00Z") {
		t.Errorf("%q, %v", out, err)
	}
}

func TestClientsRotateSaysWhatItDid(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, `{"client":"grafana","rotated":"2026-10-07T10:00:00Z","overlap_seconds":0,"discarded_previous":true}`)
	out, err := captureStdoutErr(t, func() error {
		return run([]string{"clients", "rotate", "grafana", "--overlap", "0", "--issuer", issuer.URL})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the old secret stopped working at once") || !strings.Contains(out, "warning: an earlier rotation's overlap was still open") {
		t.Errorf("output %q", out)
	}
	if strings.Contains(out, "still works until") {
		t.Errorf("a hard cut said the old secret still works: %q", out)
	}
}

func TestClientsPurge(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, `{"client":"gone","deleted":true}`)
	out, err := captureStdoutErr(t, func() error { return run([]string{"clients", "purge", "gone", "--issuer", issuer.URL}) })
	if err != nil || !strings.Contains(out, "deleted the stored secret of gone") {
		t.Errorf("%q, %v", out, err)
	}
	// What a purge does not do is said: the input is the operator's.
	if !strings.Contains(out, "clients/gone/secret") {
		t.Errorf("the purge does not say what is left to do: %q", out)
	}
	if issuer.paths[0] != "POST /.access/client-secrets/purge" || issuer.bodies[0]["client"] != "gone" {
		t.Errorf("request %v %v", issuer.paths, issuer.bodies)
	}
}
