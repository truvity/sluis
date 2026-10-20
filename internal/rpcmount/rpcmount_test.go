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

	directoryv1 "github.com/truvity/sluis/gen/directory/v1"
	"github.com/truvity/sluis/gen/directory/v1/directoryv1connect"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/rpcmount"
)

type failing struct {
	directoryv1connect.UnimplementedDirectoryServiceHandler
	err error
}

func (f failing) Describe(context.Context, *directoryv1.DescribeRequest) (*directoryv1.DescribeResponse, error) {
	return nil, f.err
}

func call(t *testing.T, err error) error {
	t.Helper()
	rpc := rpcmount.NewServer()
	directoryv1connect.RegisterDirectoryServiceHandler(rpc, failing{err: err})
	path, handler := rpcmount.Handler(rpc)
	if path != "/"+directoryv1connect.DirectoryServiceName+"/" {
		t.Fatalf("path = %q", path)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := directoryv1connect.NewDirectoryServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL)))
	_, got := client.Describe(context.Background(), &directoryv1.DescribeRequest{})
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
	directoryv1connect.RegisterDirectoryServiceHandler(rpc, failing{})
	sluisv1connect.RegisterSessionServiceHandler(rpc, sluisv1connect.UnimplementedSessionServiceHandler{})
	defer func() {
		if recover() == nil {
			t.Fatal("two services on one handler did not panic")
		}
	}()
	rpcmount.Handler(rpc)
}

func TestServerContextCarriesTheCallInfo(t *testing.T) {
	ctx, info := rpcmount.ServerContext(context.Background())
	got, ok := connect.CallInfoForServerContext(ctx)
	if !ok || got != info {
		t.Fatalf("CallInfoForServerContext = %p, %v; want %p", got, ok, info)
	}
	info.RequestHeader().Set("Cookie", "a=b")
	if rpcmount.HTTPHeader(got.RequestHeader()).Get("Cookie") != "a=b" {
		t.Fatal("the header set on the info is not the call's")
	}
}
