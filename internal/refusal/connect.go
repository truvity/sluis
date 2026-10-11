package refusal

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
)

// ConnectOptions installs [Interceptor] on a Connect handler.
func ConnectOptions(log *slog.Logger) []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(Interceptor(log))}
}

// Interceptor is how a Connect service says no.
//
// The handlers behind it name their reasons freely -- "this needs the
// operator role over acme's directory", "that is somebody else's session",
// the text of an error from a store -- because a reason written where the
// refusal is made is a reason a maintainer can read. This is the one place
// they are kept from the caller:
//
//   - PermissionDenied is "not authorised (request <id>)", and the message
//     the handler wrote is logged under the id.
//   - Internal, and the Unknown a plain error becomes, is "server error
//     (request <id>)", and the error is logged under the id.
//   - Unauthenticated is "sign in first": the caller is nobody yet, and
//     what was wrong with their credential is not theirs to be told.
//
// Every other code is the handler's own answer to the question asked --
// not found, invalid argument, a failed precondition -- and passes
// untouched.
//
// log is where the originals go; nil is the default logger at the time of
// the call.
func Interceptor(log *slog.Logger) connect.Interceptor { return interceptor{log} }

type interceptor struct{ log *slog.Logger }

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, req)
		return response, i.mask(ctx, req.Spec().Procedure, err)
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return i.mask(ctx, conn.Spec().Procedure, next(ctx, conn))
	}
}

func (i interceptor) mask(ctx context.Context, procedure string, err error) error {
	if err == nil {
		return nil
	}
	code := connect.CodeOf(err)
	var message string
	var fixed error
	level := slog.LevelWarn
	switch code {
	case connect.CodePermissionDenied:
		fixed = errors.New(Text(ctx, NotAuthorised))
	case connect.CodeInternal, connect.CodeUnknown:
		fixed = errors.New(Text(ctx, ServerError))
		level = slog.LevelError
	case connect.CodeUnauthenticated:
		fixed = errors.New(SignInFirst)
	default:
		return err
	}
	message = err.Error()
	var coded *connect.Error
	if errors.As(err, &coded) {
		message = coded.Message()
	}
	log := i.log
	if log == nil {
		log = slog.Default()
	}
	attrs := []any{slog.String("procedure", procedure), slog.String("code", code.String()), slog.String("message", message)}
	if id := ID(ctx); id != "" {
		attrs = append([]any{slog.String(LogKey, id)}, attrs...)
	}
	log.Log(ctx, level, "refused", attrs...)
	return connect.NewError(code, fixed)
}
