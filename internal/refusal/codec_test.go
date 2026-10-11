package refusal_test

import "encoding/json"

// jsonCodec carries an empty message, so the tests need no generated type.
type jsonCodec struct{}

func (jsonCodec) Name() string                    { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (jsonCodec) Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
