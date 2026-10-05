package docsgen

import (
	"strings"
	"testing"
)

func TestReplaceRegionRewritesOnlyTheInside(t *testing.T) {
	t.Parallel()
	in := "before\n<!-- generated: a -->\n\nold\n<!-- /generated -->\nafter\n<!-- generated: b -->\nkeep\n<!-- /generated -->\n"
	got, err := replaceRegion(in, "a", "new\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "before\n<!-- generated: a -->\n\nnew\n<!-- /generated -->\nafter\n<!-- generated: b -->\nkeep\n<!-- /generated -->\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	again, _ := replaceRegion(got, "a", "new\n")
	if again != got {
		t.Fatal("rewriting a region twice changed it")
	}
}

func TestReplaceRegionRefusesBrokenMarkers(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"missing":    "no region here\n",
		"unclosed":   "<!-- generated: a -->\nx\n",
		"nested":     "<!-- generated: a -->\n<!-- generated: b -->\n<!-- /generated -->\n",
		"duplicated": "<!-- generated: a -->\n<!-- /generated -->\n<!-- generated: a -->\n<!-- /generated -->\n",
	} {
		if _, err := replaceRegion(in, "a", "x"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSchemaRowsFollowRefsArraysAndStops(t *testing.T) {
	t.Parallel()
	s := map[string]any{
		"$defs":    map[string]any{"dur": map[string]any{"type": "string", "description": "a duration"}},
		"required": []any{"b"},
		"properties": map[string]any{
			"b": map[string]any{"type": "string", "default": "x", "description": "bee"},
			"a": map[string]any{"$ref": "#/$defs/dur", "default": "5m"},
			"list": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}}},
			"embedded": map[string]any{"type": "object", "properties": map[string]any{"deep": map[string]any{"type": "string"}}},
		},
	}
	var got []string
	for _, r := range schemaRows(s, "embedded") {
		got = append(got, r.path+":"+r.typ+":"+r.def)
	}
	want := `a:string:"5m" b:string:"x" embedded:object: list:array: list[].n:integer:`
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
}

// The committed docs are what the generators write. Fix with `just docs-generate`.
func TestTheDocsAreNotStale(t *testing.T) {
	stale, err := Run("../..", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		t.Fatalf("stale regions %v: run `just docs-generate` and commit the result", stale)
	}
}
