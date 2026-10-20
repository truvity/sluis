package refusal_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	directoryv1 "github.com/truvity/sluis/gen/directory/v1"
	"github.com/truvity/sluis/gen/directory/v1/directoryv1connect"
	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logtest"
)

// failing answers every Describe with the error it holds.
type failing struct {
	directoryv1connect.UnimplementedDirectoryServiceHandler
	err error
}

func (f failing) Describe(context.Context, *directoryv1.DescribeRequest) (*directoryv1.DescribeResponse, error) {
	return nil, f.err
}

// procedure is the RPC the tests call.
const procedure = directoryv1connect.DirectoryServiceDescribeProcedure

// The interceptor is exercised through a real Connect handler, so what it
// guarantees is what a client reads off the wire.
func call(t *testing.T, handlerErr error) (*connect.Error, *logtest.Handler) {
	t.Helper()
	logger, records := logtest.Logger()
	rpc := connect.NewServer(refusal.Interceptor(logger))
	directoryv1connect.RegisterDirectoryServiceHandler(rpc, failing{err: handlerErr})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	server := httptest.NewServer(refusal.Middleware(mux))
	t.Cleanup(server.Close)

	client := directoryv1connect.NewDirectoryServiceClient(connect.NewClient(
		connecthttp.NewTransport(server.Client(), server.URL)))
	_, err := client.Describe(t.Context(), &directoryv1.DescribeRequest{})
	var coded *connect.Error
	if !errors.As(err, &coded) {
		t.Fatalf("the call returned %v, want a Connect error", err)
	}
	return coded, records
}

func TestPermissionDeniedIsTheFixedWordsAndTheReasonIsLogged(t *testing.T) {
	t.Parallel()
	denied, records := call(t, connect.NewError(connect.CodePermissionDenied,
		"this needs the operator role over acme's directory"))
	if denied.Code() != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want permission_denied", denied.Code())
	}
	message := denied.Message()
	if !strings.HasPrefix(message, "not authorised (request ") || !strings.HasSuffix(message, ")") {
		t.Fatalf("message = %q, want the fixed words and the request id", message)
	}
	if strings.Contains(message, "acme") || strings.Contains(message, "operator") {
		t.Errorf("message = %q gives the reason away", message)
	}
	record := records.MustFind(t, "refused")
	id := strings.TrimSuffix(strings.TrimPrefix(message, "not authorised (request "), ")")
	if record.String(refusal.LogKey) != id {
		t.Errorf("the log names request %q, the caller was told %q", record.String(refusal.LogKey), id)
	}
	if !strings.Contains(record.String("message"), "acme's directory") || record.String("procedure") != procedure {
		t.Errorf("the log does not carry the original: %v", record)
	}
}

func TestInternalAndUnknownAreServerErrorAndTheErrorIsLogged(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		handlerErr error
		logged     string
	}{
		"internal": {connect.NewError(connect.CodeInternal, "pq: password authentication failed for user sluis"), "password authentication failed"},
		"unknown":  {errors.New("dial tcp 10.1.2.3:5432: connection refused"), "connection refused"},
	} {
		failed, records := call(t, tc.handlerErr)
		if !strings.HasPrefix(failed.Message(), "server error (request ") {
			t.Errorf("%s: message = %q, want the fixed words and the request id", name, failed.Message())
		}
		if strings.Contains(failed.Message(), "10.1.2.3") || strings.Contains(failed.Message(), "pq") {
			t.Errorf("%s: message = %q gives the error away", name, failed.Message())
		}
		if got := records.MustFind(t, "refused").String("message"); !strings.Contains(got, tc.logged) {
			t.Errorf("%s: the log carries %q, want the error (%q)", name, got, tc.logged)
		}
	}
}

func TestUnauthenticatedStaysSignInFirstWhateverTheHandlerSaid(t *testing.T) {
	t.Parallel()
	for _, said := range []string{"sign in first", "that token was not accepted: it expired at 12:00"} {
		failed, _ := call(t, connect.NewError(connect.CodeUnauthenticated, said))
		if failed.Code() != connect.CodeUnauthenticated || failed.Message() != "sign in first" {
			t.Errorf("%q became %v %q, want unauthenticated \"sign in first\"", said, failed.Code(), failed.Message())
		}
	}
}

func TestOtherCodesPassUntouched(t *testing.T) {
	t.Parallel()
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeUnavailable} {
		failed, records := call(t, connect.NewError(code, "the handler's own answer"))
		if failed.Code() != code || failed.Message() != "the handler's own answer" {
			t.Errorf("%v became %v %q, want it passed through", code, failed.Code(), failed.Message())
		}
		if len(records.Records()) != 0 {
			t.Errorf("%v: a pass-through was logged as a refusal", code)
		}
	}
}
