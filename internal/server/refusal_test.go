package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logtest"
)

// refusalLog keeps what the servers under test log, so that a test can ask
// what the log said about the request a person was given a reference for.
var refusalLog = logtest.New(nil)

// capturedLogger is the logger a server under test is given when the test
// is about what a refusal leaves in the log.
func capturedLogger() *slog.Logger { return slog.New(refusal.LogHandler(refusalLog)) }

// loggedFor is everything logged under a request id, as one text.
func loggedFor(id string) string {
	var out strings.Builder
	for _, record := range refusalLog.Records() {
		if record.String(refusal.LogKey) != id {
			continue
		}
		out.WriteString(record.Message)
		for _, attr := range record.Attrs {
			out.WriteString(" " + attr.Key + "=" + attr.Value.String())
		}
		out.WriteString("\n")
	}
	return out.String()
}

var reference = regexp.MustCompile(`Reference: ([A-Za-z0-9._:-]+)`)

// wantRefused fails unless a response says only "<phrase>. Reference: <id>",
// with the response's own request id, none of the given words, and a log
// that holds each reason under that id.
func wantRefused(t *testing.T, response *httptest.ResponseRecorder, phrase string, absent []string, reasons ...string) {
	t.Helper()
	body := response.Body.String()
	found := reference.FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("the response carries no reference: %q", body)
	}
	id := response.Header().Get(refusal.Header)
	if found[1] != id {
		t.Errorf("the page quotes %q, the response is %q", found[1], id)
	}
	if !strings.Contains(body, phrase+". Reference: "+id) {
		t.Errorf("the response does not say %q:\n%s", phrase+". Reference: "+id, body)
	}
	for _, word := range absent {
		if strings.Contains(body, word) {
			t.Errorf("the response gives away %q:\n%s", word, body)
		}
	}
	logged := loggedFor(id)
	for _, reason := range reasons {
		if !strings.Contains(logged, reason) {
			t.Errorf("the log under %s does not carry %q:\n%s", id, reason, logged)
		}
	}
}

// servedWithID serves a handler as every listener does, with a request id.
func servedWithID(h http.HandlerFunc) http.Handler { return refusal.Middleware(h) }
