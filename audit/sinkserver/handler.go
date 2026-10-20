// Package sinkserver is the receiving side of the sink: the SinkService handler
// the writer mounts, and the Receiver that checks a caller and a batch before
// handing it to a Sink.
//
// The client side, and the Sink interface both sides share, are in the SDK's
// sink package, which carries none of the server's dependencies.
package sinkserver

import (
	"context"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/audit/internal/rpcmount"
	"github.com/truvity/sluis/audit/internal/telemetry"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"github.com/truvity/sluis/audit/sdk/sink"
	"github.com/truvity/sluis/audit/wire"
)

// Handler serves SinkService from a Sink, which is how the writer is reached
// by anything that cannot call it in process.
type Handler struct {
	auditv1connect.UnimplementedSinkServiceHandler
	sink sink.Sink
}

// NewHandler returns the path and handler to mount. opts are the HTTP
// handler's, after this repository's own.
func NewHandler(s sink.Sink, opts ...connecthttp.Option) (string, http.Handler) {
	rpc := rpcmount.NewServer(telemetry.ConnectServerInterceptor())
	auditv1connect.RegisterSinkServiceHandler(rpc, &Handler{sink: s})
	return rpcmount.Handler(rpc, append(wire.HandlerOptions(), opts...)...)
}

// Write implements the service.
func (h *Handler) Write(
	ctx context.Context, req *auditv1.WriteRequest,
) (*auditv1.WriteResponse, error) {
	in := &sink.Request{
		Records:  req.GetRecords(),
		Delivery: req.GetDelivery(),
	}
	ctx, done := sink.Observe(ctx, sink.TransportConnectServer, trace.SpanKindInternal, in)
	res, err := h.sink.Write(ctx, in)
	done(res, err)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	return toProto(res), nil
}

func toProto(r *sink.Result) *auditv1.WriteResponse {
	if r == nil {
		return &auditv1.WriteResponse{}
	}
	out := &auditv1.WriteResponse{Accepted: int32(r.Accepted), Durability: r.Durability}
	for _, x := range r.Rejected {
		out.Rejected = append(out.Rejected, &auditv1.Rejection{Id: x.ID, Reason: x.Reason})
	}
	return out
}
