package keys

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Context key names of the default encryption context. They are
// product-neutral: "instance" is the deployment (Options.Instance) and
// "purpose" is the short purpose, so a ciphertext made for one deployment's
// seal key does not open as another's conceal, even under the same KMS key.
const (
	ContextInstance = "instance"
	ContextPurpose  = "purpose"
)

// ContextMode says which encryption context a purpose uses.
type ContextMode string

const (
	// ContextDefault sends {"instance": ..., "purpose": ...}.
	ContextDefault ContextMode = "default"
	// ContextOff sends no context. Use it only when the key policy cannot
	// condition on a context (see the package documentation).
	ContextOff ContextMode = "off"
	// ContextMap sends exactly the configured map.
	ContextMap ContextMode = "map"
)

// ContextSpec is the `context` of a key entry: "default", "off" or an object
// of string pairs sent as is. The zero value is the default.
type ContextSpec struct {
	Mode ContextMode
	Map  map[string]string
}

func (c ContextSpec) resolve(p Purpose, instance string) (map[string]string, error) {
	switch c.Mode {
	case "", ContextDefault:
		if instance == "" {
			return nil, errors.New("the default context binds the instance name, and Options.Instance is empty; " +
				`set it, or configure context: off or an explicit map`)
		}
		return map[string]string{ContextInstance: instance, ContextPurpose: string(p)}, nil
	case ContextOff:
		return nil, nil
	case ContextMap:
		return cloneMap(c.Map), nil
	}
	return nil, fmt.Errorf("unknown context mode %q", c.Mode)
}

// UnmarshalJSON accepts "default", "off" or an object of strings.
func (c *ContextSpec) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		switch s {
		case "default":
			*c = ContextSpec{Mode: ContextDefault}
		case "off":
			*c = ContextSpec{Mode: ContextOff}
		default:
			return fmt.Errorf(`context %q: use "default", "off" or an object of strings`, s)
		}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf(`context: use "default", "off" or an object of strings: %w`, err)
	}
	if len(m) == 0 {
		return errors.New(`context: an empty object sends no context; write "off" to say so`)
	}
	*c = ContextSpec{Mode: ContextMap, Map: m}
	return nil
}

// MarshalJSON writes the shortest form.
func (c ContextSpec) MarshalJSON() ([]byte, error) {
	switch c.Mode {
	case "", ContextDefault:
		return []byte(`"default"`), nil
	case ContextOff:
		return []byte(`"off"`), nil
	}
	return json.Marshal(c.Map)
}

// Entry is one purpose's key: an alias (or transit key name) and its context.
// In JSON it is a string (the key, default context) or {"key", "context"}.
type Entry struct {
	Key     string
	Context ContextSpec
}

// UnmarshalJSON accepts the short and the long form.
func (e *Entry) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*e = Entry{Key: s}
		return nil
	}
	var long struct {
		Key     string       `json:"key"`
		Context *ContextSpec `json:"context"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&long); err != nil {
		return fmt.Errorf("a key is a string or {key, context}: %w", err)
	}
	e.Key = long.Key
	if long.Context != nil {
		e.Context = *long.Context
	}
	return nil
}

// MarshalJSON writes the short form when the context is the default.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Context.Mode == "" || e.Context.Mode == ContextDefault {
		return json.Marshal(e.Key)
	}
	return json.Marshal(struct {
		Key     string      `json:"key"`
		Context ContextSpec `json:"context"`
	}{e.Key, e.Context})
}

// Config is the `keys:` block both products share:
//
//	keys:
//	  adapter: kms
//	  sign: alias/sluis-signing
//	  seal: {key: alias/audit-seal, context: off}
type Config struct {
	Adapter string
	Keys    map[Purpose]Entry
}

// UnmarshalJSON reads `adapter` and one entry per purpose; any other member
// is an error that names it.
func (c *Config) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("keys: %w", err)
	}
	out := Config{Keys: map[Purpose]Entry{}}
	for name, v := range raw {
		if name == "adapter" {
			if err := json.Unmarshal(v, &out.Adapter); err != nil {
				return fmt.Errorf("keys.adapter: %w", err)
			}
			continue
		}
		p := Purpose(name)
		if !p.valid() {
			return fmt.Errorf("keys.%s: unknown purpose (known: %v)", name, Purposes())
		}
		var e Entry
		if err := json.Unmarshal(v, &e); err != nil {
			return fmt.Errorf("keys.%s: %w", name, err)
		}
		out.Keys[p] = e
	}
	*c = out
	return nil
}

// MarshalJSON is the inverse of UnmarshalJSON.
func (c Config) MarshalJSON() ([]byte, error) {
	m := map[string]any{"adapter": c.Adapter}
	for p, e := range c.Keys {
		m[string(p)] = e
	}
	return json.Marshal(m)
}

var (
	uuidRe  = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	mrkIDRe = regexp.MustCompile(`(?i)^mrk-[0-9a-f]{32}$`)
)

// Validate checks what does not depend on the adapter: a known adapter name,
// at least one key, and aliases or names only. The adapter's own rules
// (Backend.ValidateName) run in Open.
func (c Config) Validate() error {
	switch c.Adapter {
	case "":
		return errors.New("keys.adapter is required (kms, local or transit)")
	case "kms", "local", "transit":
	default:
		return fmt.Errorf("keys.adapter %q is not one of kms, local, transit", c.Adapter)
	}
	if len(c.Keys) == 0 {
		return errors.New("keys: no purpose is configured")
	}
	for p, e := range c.Keys {
		if !p.valid() {
			return fmt.Errorf("keys.%s: unknown purpose", p)
		}
		if err := checkName(e.Key); err != nil {
			return fmt.Errorf("keys.%s: %w", p, err)
		}
		if e.Context.Mode == ContextMap && len(e.Context.Map) == 0 {
			return fmt.Errorf("keys.%s.context: an empty map sends no context; use \"off\"", p)
		}
	}
	return nil
}

// checkName refuses ARNs and raw key ids: a configuration names a key by
// alias so a key can be replaced without editing every deployment, and an
// ARN would carry an account and region into a file that is shared.
func checkName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("key is empty")
	case strings.HasPrefix(name, "arn:"):
		return errors.New("an ARN is not accepted: name the key by alias (alias/<name>), so the key can be replaced and the file carries no account or region")
	case uuidRe.MatchString(name) || mrkIDRe.MatchString(name):
		return errors.New("a key id is not accepted: name the key by alias (alias/<name>)")
	case strings.ContainsAny(name, " \t\r\n"):
		return errors.New("a key name has no whitespace")
	}
	return nil
}
