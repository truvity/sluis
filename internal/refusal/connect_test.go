package refusal_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logtest"
)

// The interceptor is exercised through a real Connect handler, so what it
// guarantees is what a client reads off the wire.
func call(t *testing.T, handlerErr error) (*connect.Error, *logtest.Handler) {
	t.Helper()
	logger, records := logtest.Logger()
	handler := connect.NewUnaryHandler("/test.v1.Service/Do",
		func(context.Context, *connect.Request[struct{}]) (*connect.Response[struct{}], error) {
			return nil, handlerErr
		}, connect.WithInterceptors(refusal.Interceptor(logger)),
		connect.WithCodec(jsonCodec{}))
	mux := http.NewServeMux()
	mux.Handle("/test.v1.Service/Do", refusal.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := connect.NewClient[struct{}, struct{}](server.Client(), server.URL+"/test.v1.Service/Do",
		connect.WithCodec(jsonCodec{}))
	request := connect.NewRequest(&struct{}{})
	_, err := client.CallUnary(t.Context(), request)
	var coded *connect.Error
	if !errors.As(err, &coded) {
		t.Fatalf("the call returned %v, want a Connect error", err)
	}
	return coded, records
}

func TestPermissionDeniedIsTheFixedWordsAndTheReasonIsLogged(t *testing.T) {
	t.Parallel()
	denied, records := call(t, connect.NewError(connect.CodePermissionDenied,
		errors.New("this needs the operator role over acme's directory")))
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
	if !strings.Contains(record.String("message"), "acme's directory") || record.String("procedure") != "/test.v1.Service/Do" {
		t.Errorf("the log does not carry the original: %v", record)
	}
}

func TestInternalAndUnknownAreServerErrorAndTheErrorIsLogged(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		handlerErr error
		logged     string
	}{
		"internal": {connect.NewError(connect.CodeInternal, errors.New("pq: password authentication failed for user sluis")), "password authentication failed"},
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
		failed, _ := call(t, connect.NewError(connect.CodeUnauthenticated, errors.New(said)))
		if failed.Code() != connect.CodeUnauthenticated || failed.Message() != "sign in first" {
			t.Errorf("%q became %v %q, want unauthenticated \"sign in first\"", said, failed.Code(), failed.Message())
		}
	}
}

func TestOtherCodesPassUntouched(t *testing.T) {
	t.Parallel()
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeUnavailable} {
		failed, records := call(t, connect.NewError(code, errors.New("the handler's own answer")))
		if failed.Code() != code || failed.Message() != "the handler's own answer" {
			t.Errorf("%v became %v %q, want it passed through", code, failed.Code(), failed.Message())
		}
		if len(records.Records()) != 0 {
			t.Errorf("%v: a pass-through was logged as a refusal", code)
		}
	}
}
