// Package logtest is a capturing slog.Handler for tests: it keeps each
// record with its attributes and groups, so a test asserts on the record
// (find it by message, read an attribute by key) instead of matching the
// text a handler happened to print.
//
// The handler is held to the slog.Handler contract by slogtest (see
// logtest_test.go). Any custom handler added to this repository (an OTel
// log bridge, a redacting handler) gets the same conformance test.
package logtest

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// Record is one captured log record. Attrs are flat and keyed by the dotted
// path of their groups ("req.id" for attribute id in group req), resolved
// (a LogValuer is replaced by its value).
type Record struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   []slog.Attr
	attrMap map[string]slog.Value
}

// Attr returns the value under key (dotted for groups) and whether it exists.
func (r Record) Attr(key string) (slog.Value, bool) {
	v, ok := r.attrMap[key]
	return v, ok
}

// String is the attribute under key as text, "" if absent.
func (r Record) String(key string) string {
	v, ok := r.attrMap[key]
	if !ok {
		return ""
	}
	return v.String()
}

// store is shared by a Handler and every handler derived from it.
type store struct {
	mu      sync.Mutex
	records []Record
	level   slog.Leveler
}

// Handler captures records. The zero value is not usable; use New.
type Handler struct {
	s      *store
	groups []string    // open groups, outermost first
	pre    []slog.Attr // attributes from WithAttrs, already qualified by groups
}

// New returns a Handler that captures every record at or above level (Debug
// when nil).
func New(level slog.Leveler) *Handler {
	if level == nil {
		level = slog.LevelDebug
	}
	return &Handler{s: &store{level: level}}
}

// Logger is a *slog.Logger over a new capturing Handler, and the Handler.
func Logger() (*slog.Logger, *Handler) {
	h := New(nil)
	return slog.New(h), h
}

// Enabled reports whether level is captured.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.s.level.Level()
}

// Handle captures r.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level, Message: r.Message, attrMap: map[string]slog.Value{}}
	for _, a := range h.pre {
		rec.add(a)
	}
	prefix := strings.Join(h.groups, ".")
	r.Attrs(func(a slog.Attr) bool {
		rec.add(qualify(prefix, a))
		return true
	})
	h.s.mu.Lock()
	h.s.records = append(h.s.records, rec)
	h.s.mu.Unlock()
	return nil
}

// WithAttrs returns a Handler whose records carry attrs.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := h.clone()
	prefix := strings.Join(h.groups, ".")
	for _, a := range attrs {
		n.pre = append(n.pre, qualify(prefix, a))
	}
	return n
}

// WithGroup returns a Handler that nests later attributes under name.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := h.clone()
	n.groups = append(n.groups, name)
	return n
}

func (h *Handler) clone() *Handler {
	return &Handler{
		s:      h.s,
		groups: append([]string(nil), h.groups...),
		pre:    append([]slog.Attr(nil), h.pre...),
	}
}

// qualify prefixes a's key with the group path. A group attribute keeps its
// own key as the next path element; add flattens it.
func qualify(prefix string, a slog.Attr) slog.Attr {
	if prefix == "" {
		return a
	}
	if a.Equal(slog.Attr{}) {
		return a
	}
	if a.Value.Kind() == slog.KindGroup && a.Key == "" {
		// An inline group: its members sit directly under the prefix.
		members := a.Value.Group()
		out := make([]slog.Attr, len(members))
		for i, m := range members {
			out[i] = qualify(prefix, m)
		}
		return slog.Attr{Key: "", Value: slog.GroupValue(out...)}
	}
	a.Key = prefix + "." + a.Key
	return a
}

// add flattens a into the record, dropping empty attributes and
// resolving LogValuers.
func (r *Record) add(a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return
		}
		for _, m := range members {
			if a.Key != "" {
				m.Key = a.Key + "." + m.Key
			}
			r.add(m)
		}
		return
	}
	r.Attrs = append(r.Attrs, a)
	r.attrMap[a.Key] = a.Value
}

// Records returns a copy of everything captured so far.
func (h *Handler) Records() []Record {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return append([]Record(nil), h.s.records...)
}

// Find returns the first record with exactly this message.
func (h *Handler) Find(message string) (Record, bool) {
	for _, r := range h.Records() { //nolint:gocritic // test support, copies are fine
		if r.Message == message {
			return r, true
		}
	}
	return Record{}, false
}

// MustFind is Find that fails the test when there is no such record.
func (h *Handler) MustFind(t testing.TB, message string) Record {
	t.Helper()
	r, ok := h.Find(message)
	if !ok {
		t.Fatalf("no log record with message %q; captured: %q", message, h.Messages())
	}
	return r
}

// Messages lists the captured messages in order.
func (h *Handler) Messages() []string {
	var out []string
	for _, r := range h.Records() { //nolint:gocritic // test support, copies are fine
		out = append(out, r.Message)
	}
	return out
}

// Count is how many records carry message.
func (h *Handler) Count(message string) int {
	n := 0
	for _, r := range h.Records() { //nolint:gocritic // test support, copies are fine
		if r.Message == message {
			n++
		}
	}
	return n
}

// CountLevel is how many records are at exactly level.
func (h *Handler) CountLevel(level slog.Level) int {
	n := 0
	for _, r := range h.Records() { //nolint:gocritic // test support, copies are fine
		if r.Level == level {
			n++
		}
	}
	return n
}

// Mentions reports whether text appears in any captured message, attribute
// key or attribute value: the check for "this secret was never logged".
func (h *Handler) Mentions(text string) bool {
	for _, r := range h.Records() { //nolint:gocritic // test support, copies are fine
		if strings.Contains(r.Message, text) {
			return true
		}
		for _, a := range r.Attrs {
			if strings.Contains(a.Key, text) || strings.Contains(a.Value.String(), text) {
				return true
			}
		}
	}
	return false
}
