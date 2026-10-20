package rpcmount_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/truvity/sluis/audit/internal/rpcmount"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
)

type failing struct {
	auditv1connect.UnimplementedRegistryServiceHandler
	err error
}

func (f failing) RegisterCatalogue(context.Context, *auditv1.RegisterCatalogueRequest) (*auditv1.RegisterCatalogueResponse, error) {
	return nil, f.err
}

func call(t *testing.T, err error) error {
	t.Helper()
	rpc := rpcmount.NewServer()
	auditv1connect.RegisterRegistryServiceHandler(rpc, failing{err: err})
	path, handler := rpcmount.Handler(rpc)
	if path != "/"+auditv1connect.RegistryServiceName+"/" {
		t.Fatalf("path = %q", path)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := auditv1connect.NewRegistryServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL)))
	_, got := client.RegisterCatalogue(context.Background(), &auditv1.RegisterCatalogueRequest{})
	return got
}

// The client is told what connect-go v1 told it.
func TestAClientSeesWhatV1Sent(t *testing.T) {
	remote := connect.NewError(connect.CodeNotFound, "no such catalogue").WithRemote()
	for name, tc := range map[string]struct {
		err     error
		code    connect.Code
		message string
	}{
		"a coded error":                    {connect.NewError(connect.CodeInvalidArgument, "no catalogue"), connect.CodeInvalidArgument, "no catalogue"},
		"a wrapped coded error":            {fmt.Errorf("register: %w", connect.NewError(connect.CodeAlreadyExists, "taken")), connect.CodeAlreadyExists, "taken"},
		"a plain error":                    {errors.New("the store is down"), connect.CodeUnknown, "the store is down"},
		"a cancelled context":              {fmt.Errorf("read: %w", context.Canceled), connect.CodeCanceled, "read: context canceled"},
		"a deadline":                       {context.DeadlineExceeded, connect.CodeDeadlineExceeded, "context deadline exceeded"},
		"another service's error":          {remote, connect.CodeNotFound, "no such catalogue"},
		"another service's error, wrapped": {fmt.Errorf("upstream: %w", remote), connect.CodeNotFound, "no such catalogue"},
	} {
		t.Run(name, func(t *testing.T) {
			got := call(t, tc.err)
			coded, ok := errors.AsType[*connect.Error](got)
			if !ok || coded.Code() != tc.code || coded.Message() != tc.message {
				t.Fatalf("got %v; want %v: %s", got, tc.code, tc.message)
			}
		})
	}
}

func TestHandlerRefusesTwoServices(t *testing.T) {
	rpc := connect.NewServer()
	auditv1connect.RegisterRegistryServiceHandler(rpc, failing{})
	auditv1connect.RegisterSinkServiceHandler(rpc, auditv1connect.UnimplementedSinkServiceHandler{})
	defer func() {
		if recover() == nil {
			t.Fatal("two services on one handler did not panic")
		}
	}()
	rpcmount.Handler(rpc)
}
