package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/policy"
)

// MarshalOptions are the caller's choices for [MarshalInstallation].
type MarshalOptions struct {
	// Header is a comment block written before the document. Each line is
	// written as a YAML comment (`# ` is added to a line that does not start
	// with `#`), and the block ends with a line break. The caller owns its
	// text; it is not part of the document and the loader ignores it.
	Header string
}

// MarshalInstallation writes an installation as its canonical YAML document:
// the form a person reviews and `sluisctl render` reads.
//
// The form is fixed. `apiVersion` is the first key; every other key, at every
// level, is sorted by name. The indent is two spaces, the style is block, and a
// duration is written as Go spells it with the zero trailing units dropped
// (`1h`, `15m`, `336h`, `1h30m`), never `1h0m0s`. A field that is unset, or
// zero, is left out; so is an empty list or map. A section that is set but
// empty (`controllers.github: {}` is a controller that is on) is kept, because
// leaving it out would turn it off.
//
// The installation is refused when the loader would refuse the document, so a
// writer cannot produce a document that sluis will not start with: the
// document is held to the installation schema ([ValidateInstallation]), to the
// checks [Render] makes before it renders, and is decoded back into the types.
// An empty APIVersion is written as [InstallationVersion].
//
// The output is a function of the installation alone, and loading it gives an
// installation that writes the same bytes again.
func MarshalInstallation(in *Installation, opts MarshalOptions) ([]byte, error) {
	if in == nil {
		return nil, errors.New("marshal installation: no installation")
	}
	doc := *in
	if doc.APIVersion == "" {
		doc.APIVersion = InstallationVersion
	}
	if err := doc.check(); err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	raw, err := json.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	canonicalAccess(generic)
	if err := ValidateInstallation(generic); err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	var back Installation
	if err := json.Unmarshal(raw, &back); err != nil {
		return nil, fmt.Errorf("marshal installation: the document does not read back: %w", err)
	}
	body, err := encode(generic)
	if err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	// Whatever the YAML encoder did to the values, the loader must read the
	// same document the schema was shown.
	var reread any
	if err := yaml.Unmarshal(body, &reread); err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	if err := ValidateInstallation(reread); err != nil {
		return nil, fmt.Errorf("marshal installation: %w", err)
	}
	return append(headerComment(opts.Header), body...), nil
}

// canonicalAccess rewrites the access tables' durations (the policy's
// Duration fields, found by type, not by how a string looks) from Go's full
// form (`4h0m0s`, `15m0s`) to the form every other section uses (`4h`, `15m`),
// so the document spells a span one way. Both read back as the same span. A
// string in any other field is left as it is.
func canonicalAccess(doc any) {
	m, ok := doc.(map[string]any)
	if !ok || m["access"] == nil {
		return
	}
	m["access"] = canonicalDurations(m["access"], reflect.TypeFor[policy.Policy]())
}

var durationType = reflect.TypeFor[policy.Duration]()

func canonicalDurations(v any, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch val := v.(type) {
	case string:
		if t == durationType {
			if d, err := time.ParseDuration(val); err == nil {
				return Duration(d).String()
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i, e := range val {
				val[i] = canonicalDurations(e, t.Elem())
			}
		}
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for k, e := range val {
				val[k] = canonicalDurations(e, t.Elem())
			}
		case reflect.Struct:
			canonicalFields(val, t)
		}
	}
	return v
}

// canonicalFields visits the fields of a struct by the names its yaml tags
// give, descending into inlined structs.
func canonicalFields(val map[string]any, t reflect.Type) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if strings.Contains(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				canonicalFields(val, ft)
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == "-" || !f.IsExported() {
			continue
		}
		if e, ok := val[name]; ok {
			val[name] = canonicalDurations(e, f.Type)
		}
	}
}

func headerComment(h string) []byte {
	h = strings.TrimRight(strings.ReplaceAll(h, "\r\n", "\n"), "\n")
	if h == "" {
		return nil
	}
	var b bytes.Buffer
	for _, line := range strings.Split(h, "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			b.WriteString(strings.TrimRight(line, " "))
		case strings.TrimSpace(line) == "":
			b.WriteString("#")
		default:
			b.WriteString("# " + strings.TrimRight(line, " "))
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// CheckInstallation reports whether existing is what [MarshalInstallation]
// writes for in with opts. When it is not, diff is a line diff from existing
// (`-`) to the written document (`+`) with three lines of context. It returns
// an error, and no verdict, when the installation cannot be written.
//
// It compares bytes, so a header the caller changed, or a hand edit that the
// loader reads the same, is a difference: that is what a `--check` mode is for.
func CheckInstallation(existing []byte, in *Installation, opts MarshalOptions) (equal bool, diff string, err error) {
	want, err := MarshalInstallation(in, opts)
	if err != nil {
		return false, "", err
	}
	if bytes.Equal(existing, want) {
		return true, "", nil
	}
	return false, lineDiff(string(existing), string(want)), nil
}

// lineDiff is a unified-style diff of two texts, by longest common subsequence.
func lineDiff(a, b string) string {
	x, y := splitLines(a), splitLines(b)
	// lcs[i][j] is the common length of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		mark byte
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, op{' ', x[i]})
			i++
			j++
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, op{'+', y[j]})
			j++
		default:
			ops = append(ops, op{'-', x[i]})
			i++
		}
	}
	const context = 3
	keep := make([]bool, len(ops))
	for k, o := range ops {
		if o.mark == ' ' {
			continue
		}
		for n := max(0, k-context); n <= min(len(ops)-1, k+context); n++ {
			keep[n] = true
		}
	}
	var out strings.Builder
	skipped := false
	for k, o := range ops {
		if !keep[k] {
			skipped = true
			continue
		}
		if skipped {
			out.WriteString("...\n")
			skipped = false
		}
		out.WriteByte(o.mark)
		out.WriteString(o.text)
		out.WriteByte('\n')
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
