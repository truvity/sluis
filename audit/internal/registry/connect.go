package registry

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/sluis/audit/internal/rpcmount"
	"github.com/truvity/sluis/audit/internal/telemetry"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
	"github.com/truvity/sluis/audit/wire"
)

// Handler serves the registry over Connect.
type Handler struct {
	auditv1connect.UnimplementedRegistryServiceHandler
	Registry *Registry
}

// NewHandler returns the path and handler to mount. opts are the HTTP
// handler's, after this repository's own.
func NewHandler(r *Registry, opts ...connecthttp.Option) (string, http.Handler) {
	rpc := rpcmount.NewServer(telemetry.ConnectServerInterceptor())
	auditv1connect.RegisterRegistryServiceHandler(rpc, &Handler{Registry: r})
	return rpcmount.Handler(rpc, append(wire.HandlerOptions(), opts...)...)
}

// RegisterCatalogue implements the service.
//
// Validation problems come back as a response rather than an error: they are
// the application's to fix and it wants the whole list, and a transport error
// would make a client retry something that will never succeed.
func (h *Handler) RegisterCatalogue(
	ctx context.Context, req *auditv1.RegisterCatalogueRequest,
) (*auditv1.RegisterCatalogueResponse, error) {
	c := req.GetCatalogue()
	if c == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "no catalogue")
	}
	problems, err := h.Registry.Register(ctx, Entry{
		Source: c.GetSource(), Version: c.GetVersion(),
		Document: c.GetDocument(), Schemas: c.GetSchemas(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &auditv1.RegisterCatalogueResponse{Problems: problems}, nil
}

// GetCatalogue implements the service.
func (h *Handler) GetCatalogue(
	ctx context.Context, req *auditv1.GetCatalogueRequest,
) (*auditv1.GetCatalogueResponse, error) {
	e, err := h.Registry.Entry(ctx, req.GetSource(), req.GetVersion())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &auditv1.GetCatalogueResponse{Catalogue: asProto(e)}, nil
}

// ListCatalogues implements the service.
func (h *Handler) ListCatalogues(
	ctx context.Context, _ *auditv1.ListCataloguesRequest,
) (*auditv1.ListCataloguesResponse, error) {
	entries, err := h.Registry.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out := make([]*auditv1.Catalogue, 0, len(entries))
	for _, e := range entries {
		out = append(out, asProto(e))
	}
	return &auditv1.ListCataloguesResponse{Catalogues: out}, nil
}

func asProto(e Entry) *auditv1.Catalogue {
	return &auditv1.Catalogue{
		Source: e.Source, Version: e.Version,
		Document: e.Document, Schemas: e.Schemas,
		RegisteredAt: timestamppb.New(e.RegisteredAt),
	}
}
