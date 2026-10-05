package docsgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// schemaRow is one key of a JSON schema.
type schemaRow struct {
	path     string
	typ      string
	def      string
	required bool
	desc     string
}

// loadSchema reads a JSON schema file.
func loadSchema(root, file string) (map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return s, nil
}

type walker struct {
	root map[string]any
	// stop lists the paths whose children are documented elsewhere.
	stop map[string]bool
	rows []schemaRow
}

// schemaRows lists every key of the schema, parents before children and
// siblings in name order. A path in stop is listed but not descended into.
func schemaRows(schema map[string]any, stop ...string) []schemaRow {
	w := &walker{root: schema, stop: map[string]bool{}}
	for _, s := range stop {
		w.stop[s] = true
	}
	w.props(schema, "", map[string]bool{})
	return w.rows
}

// resolve follows a local $ref. The referencing node's own keys win, so a
// description written at the use site is the one shown.
func (w *walker) resolve(n map[string]any, seen map[string]bool) (map[string]any, string, map[string]bool) {
	ref, _ := n["$ref"].(string)
	if ref == "" {
		return n, "", seen
	}
	if !strings.HasPrefix(ref, "#/") {
		return n, ref, seen
	}
	if seen[ref] {
		return n, "", seen
	}
	var cur any = w.root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, _ := cur.(map[string]any)
		cur = m[part]
	}
	target, _ := cur.(map[string]any)
	if target == nil {
		return n, "", seen
	}
	next := map[string]bool{ref: true}
	for k := range seen {
		next[k] = true
	}
	merged := map[string]any{}
	for k, v := range target {
		merged[k] = v
	}
	for k, v := range n {
		if k != "$ref" {
			merged[k] = v
		}
	}
	return merged, "", next
}

// collectProps gathers the properties a node declares, directly and through
// allOf, anyOf and oneOf branches (a key in several branches is listed once).
func collectProps(n map[string]any) (map[string]map[string]any, map[string]bool) {
	props := map[string]map[string]any{}
	req := map[string]bool{}
	var visit func(m map[string]any, top bool)
	visit = func(m map[string]any, top bool) {
		if p, ok := m["properties"].(map[string]any); ok {
			for k, v := range p {
				if vm, ok := v.(map[string]any); ok {
					if _, dup := props[k]; !dup {
						props[k] = vm
					}
				}
			}
		}
		if top {
			if r, ok := m["required"].([]any); ok {
				for _, k := range r {
					if s, ok := k.(string); ok {
						req[s] = true
					}
				}
			}
		}
		for _, comb := range []string{"allOf", "anyOf", "oneOf"} {
			if l, ok := m[comb].([]any); ok {
				for _, b := range l {
					if bm, ok := b.(map[string]any); ok {
						visit(bm, false)
					}
				}
			}
		}
	}
	visit(n, true)
	return props, req
}

func (w *walker) props(n map[string]any, path string, seen map[string]bool) {
	n, _, seen = w.resolve(n, seen)
	props, req := collectProps(n)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		w.node(props[k], join(path, k), req[k], seen)
	}
	if ap, ok := n["additionalProperties"].(map[string]any); ok {
		w.node(ap, join(path, "<name>"), false, seen)
	}
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func (w *walker) node(n map[string]any, path string, required bool, seen map[string]bool) {
	n, ext, seen := w.resolve(n, seen)
	row := schemaRow{path: path, required: required, typ: typeOf(n, ext)}
	row.desc, _ = n["description"].(string)
	if d, ok := n["default"]; ok {
		b, _ := json.Marshal(d)
		row.def = string(b)
	}
	// An array of objects is listed as key[] with the element's keys below.
	items, _ := n["items"].(map[string]any)
	if items != nil {
		items, _, _ = w.resolve(items, seen)
	}
	if items != nil && row.desc == "" {
		row.desc, _ = items["description"].(string)
	}
	w.rows = append(w.rows, row)
	if w.stop[path] {
		return
	}
	if items != nil {
		w.props(items, path+"[]", seen)
		return
	}
	w.props(n, path, seen)
}

func typeOf(n map[string]any, ext string) string {
	if ext != "" {
		return "fragment: " + ext[strings.LastIndex(ext, "/")+1:]
	}
	var t string
	switch v := n["type"].(type) {
	case string:
		t = v
	case []any:
		parts := make([]string, 0, len(v))
		for _, p := range v {
			parts = append(parts, fmt.Sprint(p))
		}
		t = strings.Join(parts, " or ")
	}
	if e, ok := n["enum"].([]any); ok {
		vals := make([]string, 0, len(e))
		for _, v := range e {
			vals = append(vals, fmt.Sprint(v))
		}
		return "one of " + strings.Join(vals, ", ")
	}
	if t == "" {
		if _, ok := n["properties"]; ok {
			return "object"
		}
		return "any"
	}
	if f, ok := n["format"].(string); ok {
		t += " (" + f + ")"
	}
	return t
}

// renderRows writes the rows as a table: Key, Type, Default, Meaning.
func renderRows(source string, rows []schemaRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source: `%s`. Generated by `just docs-generate`; keys are listed with their parents first.\n\n", source)
	b.WriteString("| Key | Type | Default | Meaning |\n|---|---|---|---|\n")
	for _, r := range rows {
		def := "—"
		switch {
		case r.def != "":
			def = "`" + strings.ReplaceAll(r.def, "|", `\|`) + "`"
		case r.required:
			def = "**required**"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", r.path, cell(r.typ), def, cell(r.desc))
	}
	return b.String()
}

func underController(p string) bool {
	return p == "controllers" || strings.HasPrefix(p, "controllers.")
}

func configRows(root string, controllers bool) (string, error) {
	const src = "schemas/config/sluis.schema.json"
	s, err := loadSchema(root, src)
	if err != nil {
		return "", err
	}
	var rows []schemaRow
	for _, r := range schemaRows(s) {
		if underController(r.path) == controllers {
			rows = append(rows, r)
		}
	}
	return renderRows(src, rows), nil
}

func configKeys(root string) (string, error)            { return configRows(root, false) }
func configKeysControllers(root string) (string, error) { return configRows(root, true) }

// chartValues lists the chart's values. `config` and `policy` embed the
// service and policy documents, which have their own pages, so they are listed
// as one row each.
func chartValues(root string) (string, error) {
	const src = "charts/sluis/values.schema.json"
	s, err := loadSchema(root, src)
	if err != nil {
		return "", err
	}
	return renderRows(src, schemaRows(s, "config", "policy")), nil
}

func policyKeys(root string) (string, error) {
	const src = "schemas/config/policy.schema.json"
	s, err := loadSchema(root, src)
	if err != nil {
		return "", err
	}
	return renderRows(src, schemaRows(s)), nil
}
