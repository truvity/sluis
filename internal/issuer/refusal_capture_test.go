package issuer_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logtest"
)

// refusalLog keeps every record this test binary logs, so that a test can
// ask what the log said about the request a caller was given an id for.
// Installed as the default logger because the OpenID library logs through
// it, and so does the package's refusal helper.
var refusalLog = logtest.New(nil)

func init() {
	slog.SetDefault(slog.New(refusal.LogHandler(refusalLog)))
}

// loggedFor is everything logged under a request id, as one text: the
// message and every attribute value of each record.
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

// requestIDOf is the id a response was given, failing the test when it has
// none: every answer from this service names its request.
func requestIDOf(t *testing.T, header http.Header) string {
	t.Helper()
	id := header.Get(refusal.Header)
	if !refusal.WellFormed(id) {
		t.Fatalf("X-Request-Id = %q, want a well-formed request id", id)
	}
	return id
}

// wantRefusedAs fails unless the description is the fixed words for the
// response's request id, and the log holds each wanted piece of the real
// reason under that same id.
func wantRefusedAs(t *testing.T, header http.Header, description, phrase string, reasons ...string) {
	t.Helper()
	id := requestIDOf(t, header)
	if want := phrase + " (request " + id + ")"; description != want {
		t.Errorf("error_description = %q, want exactly %q", description, want)
	}
	logged := loggedFor(id)
	for _, reason := range reasons {
		if !strings.Contains(logged, reason) {
			t.Errorf("the log under request %s does not carry the reason %q:\n%s", id, reason, logged)
		}
	}
}

// withoutID is a response body with its request id taken out, for comparing
// two answers that must be the same apart from it.
func withoutID(body, id string) string { return strings.ReplaceAll(body, id, "<id>") }

// withoutRequest is a decoded response body as text with the request id
// taken out of its description.
func withoutRequest(body map[string]any) string {
	copied := map[string]any{}
	for key, value := range body {
		copied[key] = value
	}
	if description, ok := copied["error_description"].(string); ok {
		copied["error_description"] = requestInText.ReplaceAllString(description, "(request <id>)")
	}
	return fmt.Sprint(copied)
}

var requestInText = regexp.MustCompile(`\(request ([A-Za-z0-9._:-]+)\)`)

// wantRefusedBody is [wantRefusedAs] for a test helper that returns the
// body and not the headers: the id is the one the description quotes, which
// is the point of quoting it.
func wantRefusedBody(t *testing.T, body map[string]any, phrase string, reasons ...string) {
	t.Helper()
	description, _ := body["error_description"].(string)
	found := requestInText.FindStringSubmatch(description)
	if found == nil {
		t.Fatalf("error_description = %q, want %q and a request id", description, phrase)
	}
	header := http.Header{}
	header.Set(refusal.Header, found[1])
	wantRefusedAs(t, header, description, phrase, reasons...)
}

var referenceInPage = regexp.MustCompile(`Reference: ([A-Za-z0-9._:-]+)`)

// wantRefusedPage fails unless a page says only the fixed words and a
// reference, and the log under that reference holds each wanted reason.
func wantRefusedPage(t *testing.T, page, phrase string, reasons ...string) {
	t.Helper()
	found := referenceInPage.FindStringSubmatch(page)
	if found == nil {
		t.Fatalf("the page carries no reference:\n%s", page)
	}
	if want := strings.ToUpper(phrase[:1]) + phrase[1:] + ". Reference: " + found[1]; !strings.Contains(page, want) {
		t.Errorf("the page does not say %q", want)
	}
	logged := loggedFor(found[1])
	for _, reason := range reasons {
		if !strings.Contains(logged, reason) {
			t.Errorf("the log under reference %s does not carry %q:\n%s", found[1], reason, logged)
		}
	}
}
