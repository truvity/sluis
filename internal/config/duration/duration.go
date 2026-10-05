// Package duration is the time span a configuration document spells as a Go
// duration string. It is a package of its own, below internal/config, so that
// the packages a document's sections are checked by (internal/exportspec) can
// name it without importing the configuration they are part of.
package duration

import (
	"encoding/json"
	"fmt"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Duration is a time span as a document spells it: a Go duration string such
// as "30s", "2m" or "168h".
type Duration time.Duration

// D returns the span as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) parse(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// UnmarshalJSON reads a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is a string such as \"30s\": %w", err)
	}
	return d.parse(s)
}

// MarshalJSON writes a duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalYAML reads a duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("a duration is a string such as \"30s\": %w", err)
	}
	return d.parse(s)
}

// MarshalYAML writes a duration string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
