package policy

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// ClientSecret is how a confidential client comes by its secret. It is one
// of two shapes, and never both:
//
//   - a string, `secret: grafana`, the NAME of an input the installation
//     delivers (`clients/<id>/secret`): the value is made elsewhere and the
//     issuer only reads it;
//   - an object, `secret: {generate: true}`: the issuer makes the value
//     itself, once, and keeps it with its credentials. Nothing in the
//     document names where it is; the client's id does.
//
// An older binary refuses the object form (its field is a string).
type ClientSecret struct {
	// Name is the input's name; empty when Generate is set.
	Name string
	// Generate says the issuer makes the secret.
	Generate bool
}

// IsZero reports that no secret is declared. yaml.v3's `omitempty` uses it.
func (s ClientSecret) IsZero() bool { return s.Name == "" && !s.Generate }

// UnmarshalYAML implements yaml.Unmarshaler: a scalar names an input, a
// mapping is `{generate: true}`.
func (s *ClientSecret) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var name string
		if err := node.Decode(&name); err != nil {
			return fmt.Errorf("secret: %w", err)
		}
		*s = ClientSecret{Name: name}
		return nil
	case yaml.MappingNode:
		var raw map[string]any
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("secret: %w", err)
		}
		return s.fromObject(raw)
	default:
		return errors.New("secret must be a name or {generate: true}")
	}
}

// MarshalYAML implements yaml.Marshaler, the inverse of UnmarshalYAML.
func (s ClientSecret) MarshalYAML() (any, error) {
	if s.Generate {
		return map[string]bool{"generate": true}, nil
	}
	return s.Name, nil
}

// UnmarshalJSON implements json.Unmarshaler, with the same two shapes.
func (s *ClientSecret) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case nil:
		*s = ClientSecret{}
		return nil
	case string:
		*s = ClientSecret{Name: v}
		return nil
	case map[string]any:
		return s.fromObject(v)
	default:
		return errors.New("secret must be a name or {generate: true}")
	}
}

// MarshalJSON implements json.Marshaler.
func (s ClientSecret) MarshalJSON() ([]byte, error) {
	if s.Generate {
		return []byte(`{"generate":true}`), nil
	}
	return json.Marshal(s.Name)
}

func (s *ClientSecret) fromObject(raw map[string]any) error {
	for k := range raw {
		if k != "generate" {
			return fmt.Errorf("secret: unknown key %q: the object form is {generate: true}", k)
		}
	}
	gen, ok := raw["generate"].(bool)
	if !ok {
		return errors.New("secret: the object form is {generate: true}")
	}
	if !gen {
		return errors.New("secret: generate: false is refused; to name an input write `secret: <name>`")
	}
	*s = ClientSecret{Generate: true}
	return nil
}
