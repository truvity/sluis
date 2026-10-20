// Package wire is how this repository's services speak JSON.
//
// The reference promises snake_case JSON — `occurred_at`, `tenant_id` — and the
// archive has always been written that way. The services were not: connect-go's
// default JSON codec writes lowerCamelCase, so a response said `occurredAt`
// where the reference, the archive and every exported file say `occurred_at`.
// It went unnoticed because reading is lenient — protojson accepts either
// spelling on the way in — and because Go clients speak binary protobuf, where
// names do not exist. Only a JSON reader saw it: a browser, the viewer, curl.
//
// HandlerOptions is the fix, and every handler in this repository applies it
// before the caller's own options, so that none of them is the one that forgot
// and a deployment can still override it deliberately.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// HandlerOptions make a handler write snake_case JSON, under both names a
// Connect client may ask for JSON by, and refuse a message over
// [MaxRequestBytes]. connect-go v2's codec option replaces the whole list, so
// binary protobuf, which Go clients speak, is listed with them.
func HandlerOptions() []connecthttp.Option {
	return []connecthttp.Option{
		connecthttp.WithCodecs(
			connectproto.NewBinaryCodec(),
			Codec{name: connect.CodecNameJSON},
			Codec{name: connect.CodecNameJSON + "; charset=utf-8"},
		),
		connecthttp.WithReadMaxBytes(MaxRequestBytes),
	}
}

// MaxRequestBytes is the largest message a handler reads, as decoded: a sink
// batch of at most ten records, or a catalogue being registered. The
// listener caps the body as sent; this caps what a compressed body expands
// to.
const MaxRequestBytes = 4 << 20

// Codec is protojson with the proto's own field names on output.
//
// Input is unchanged from connect-go's own codec: either spelling is accepted
// and unknown fields are discarded, so that a client one schema version ahead
// is not refused. Only what this side writes is fixed.
type Codec struct{ name string }

var (
	_ connect.StableCodec = Codec{}

	marshal   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// Name implements connect.Codec.
func (c Codec) Name() string {
	if c.name == "" {
		return connect.CodecNameJSON
	}
	return c.name
}

// Marshal is the JSON of message, with the proto's own field names.
func (Codec) Marshal(message any) ([]byte, error) {
	m, ok := message.(proto.Message)
	if !ok {
		return nil, notProto(message)
	}
	return marshal.Marshal(m)
}

// MarshalWrite implements connect.Codec.
func (c Codec) MarshalWrite(_ context.Context, dst io.Writer, message any) error {
	out, err := c.Marshal(message)
	if err != nil {
		return err
	}
	_, err = dst.Write(out)
	return err
}

// MarshalWriteStable implements connect.StableCodec, which GET requests use as
// a cache key. protojson's whitespace is not stable, so it is compacted.
func (c Codec) MarshalWriteStable(_ context.Context, dst io.Writer, message any) error {
	out, err := c.Marshal(message)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		return err
	}
	_, err = dst.Write(compact.Bytes())
	return err
}

// IsBinary implements connect.StableCodec.
func (Codec) IsBinary() bool { return false }

// Unmarshal reads JSON into message, either spelling of a field name.
func (Codec) Unmarshal(data []byte, message any) error {
	m, ok := message.(proto.Message)
	if !ok {
		return notProto(message)
	}
	if len(data) == 0 {
		return errors.New("wire: an empty body is not a JSON object")
	}
	if err := unmarshal.Unmarshal(data, m); err != nil {
		return fmt.Errorf("wire: unmarshal into %T: %w", message, err)
	}
	return nil
}

// UnmarshalRead implements connect.Codec.
func (c Codec) UnmarshalRead(_ context.Context, src io.Reader, message any) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return c.Unmarshal(data, message)
}

func notProto(message any) error {
	return fmt.Errorf("wire: %T is not a protobuf message", message)
}
