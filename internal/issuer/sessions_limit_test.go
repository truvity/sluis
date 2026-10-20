package issuer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/httpsec"
)

// A call to the session service is a few fields. A message over the limit
// is refused before it is handed to the service, whichever name it is sent
// under.
func TestTheSessionServiceRefusesAnOversizeMessage(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mountSessions(mux, namedSessions{}, "")
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := sluisv1connect.NewSessionServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL)))

	_, err := client.ListSessions(t.Context(),
		&sluisv1.ListSessionsRequest{Identity: strings.Repeat("x", httpsec.MaxRequestBody+1)})
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Errorf("an oversize message: %v (%v), want resource_exhausted", got, err)
	}

	if _, err = client.ListSessions(t.Context(), &sluisv1.ListSessionsRequest{Identity: "ada"}); err != nil {
		t.Errorf("a small message: %v", err)
	}
}
