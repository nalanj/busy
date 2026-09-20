package agent

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/invopop/jsonschema"
)

// jsonUnmarshalStrict unmarshals raw JSON into v, rejecting unknown fields
// so typos in model-emitted args don't get silently ignored.
func jsonUnmarshalStrict(data string, v any) error {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// schemaAsMap reflects a struct into a sorus-compatible Parameters map.
// We marshal the invopop/jsonschema Schema to JSON and unmarshal back into
// map[string]any so sorus sees the wire shape it expects.
func schemaAsMap(v any) map[string]any {
	s := jsonschema.Reflect(v)
	data, err := json.Marshal(s)
	if err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return out
}

// errEOF is re-exported here to satisfy io.EOF without exposing io.
var errEOF = io.EOF
