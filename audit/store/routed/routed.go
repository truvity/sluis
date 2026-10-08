// Package routed is the archive of an installation with several install
// presets, each in a store of its own.
//
// An installation configures the presets it uses and each preset has its own
// bucket (deployment.presets). A profile is kept under one preset, so everything
// addressed to a profile -- its records, its seals, its recorded compositions --
// lives in that preset's store. The things no profile owns live in two places:
//
//   - the catalogues and extension schemas that describe the records are in every
//     store, so that each bucket is readable without the others (a locked object
//     outlives the installation that wrote it);
//   - the rest (legal holds, sealed identities, dead letters, the notary's
//     delegations and revocations) is in the home store, which is the strongest
//     preset's: the one whose bucket is the most carefully kept.
//
// Routed implements store.Store, so the writer, the notary, the indexer and the
// query service use it as the one archive they always had.
package routed

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/audit/store"
)

// Store is a set of stores addressed as one.
type Store struct {
	home    store.Store
	all     []store.Store
	routeOf map[string]store.Store
}

// New makes the routed store. home receives what no profile owns; byProfile
// says which store each profile lives in. Every store in byProfile and home
// is one of the set, and a store that is the value of several keys is one member.
func New(home store.Store, byProfile map[string]store.Store) (*Store, error) {
	if home == nil {
		return nil, errors.New("routed: a home store is required")
	}
	s := &Store{home: home, routeOf: map[string]store.Store{}}
	s.all = append(s.all, home)
	seen := map[store.Store]bool{home: true}
	names := make([]string, 0, len(byProfile))
	for name := range byProfile {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		st := byProfile[name]
		if st == nil {
			return nil, fmt.Errorf("routed: profile %s has no store", name)
		}
		s.routeOf[name] = st
		if !seen[st] {
			seen[st] = true
			s.all = append(s.all, st)
		}
	}
	return s, nil
}

// Stores lists the distinct stores, the home first.
func (s *Store) Stores() []store.Store { return append([]store.Store(nil), s.all...) }

// Home is the store of what no profile owns.
func (s *Store) Home() store.Store { return s.home }

// For is the store a profile lives in; the home for one that is not routed.
func (s *Store) For(profile string) store.Store {
	if st, ok := s.routeOf[profile]; ok {
		return st
	}
	return s.home
}

// profileOf is the profile a key or prefix is addressed to, if it names one.
func profileOf(key string) (string, bool) {
	for _, p := range [...]string{store.RecordsPrefix, store.SealsPrefix, "schema/profile/"} {
		if rest, ok := strings.CutPrefix(key, p); ok {
			name, _, found := strings.Cut(rest, "/")
			return name, found && name != ""
		}
	}
	return "", false
}

// everywhere is whether a key is a description of records, kept beside them in
// every store.
func everywhere(key string) bool {
	if strings.HasPrefix(key, "schema/profile/") {
		return false
	}
	return strings.HasPrefix(key, store.CataloguePrefix) || strings.HasPrefix(key, "schema/")
}

func (s *Store) route(key string) store.Store {
	if p, ok := profileOf(key); ok {
		return s.For(p)
	}
	return s.home
}

// Put implements store.Store. A description of the records goes to every
// store; it is ErrExists only when every store already had it.
func (s *Store) Put(ctx context.Context, o store.Object) error {
	if !everywhere(o.Key) {
		return s.route(o.Key).Put(ctx, o)
	}
	exists := 0
	for _, st := range s.all {
		switch err := st.Put(ctx, o); {
		case err == nil:
		case errors.Is(err, store.ErrExists):
			exists++
		default:
			return err
		}
	}
	if exists == len(s.all) {
		return fmt.Errorf("%w: %s", store.ErrExists, o.Key)
	}
	return nil
}

// Get implements store.Store.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if !everywhere(key) {
		return s.route(key).Get(ctx, key)
	}
	return firstOf(s.all, func(st store.Store) ([]byte, error) { return st.Get(ctx, key) })
}

// Head implements store.Store.
func (s *Store) Head(ctx context.Context, key string) (store.Entry, error) {
	if !everywhere(key) {
		return s.route(key).Head(ctx, key)
	}
	return firstOf(s.all, func(st store.Store) (store.Entry, error) { return st.Head(ctx, key) })
}

func firstOf[T any](all []store.Store, f func(store.Store) (T, error)) (T, error) {
	var zero T
	for _, st := range all {
		v, err := f(st)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return zero, err
		}
	}
	return zero, store.ErrNotFound
}

// stores are the stores a prefix can be in.
func (s *Store) stores(prefix string) []store.Store {
	if p, ok := profileOf(prefix); ok {
		return []store.Store{s.For(p)}
	}
	if strings.HasPrefix(prefix, "schema/profile/") {
		// Names no profile yet: the compositions of every one.
		return s.all
	}
	if everywhere(prefix) {
		return s.all[:1:1]
	}
	// A prefix under records/ or seals/, or a head of either (a listing of "rec").
	underOrHead := func(dir string) bool { return strings.HasPrefix(prefix, dir) || strings.HasPrefix(dir, prefix) }
	if prefix == "" || underOrHead(store.RecordsPrefix) || underOrHead(store.SealsPrefix) {
		return s.all
	}
	return []store.Store{s.home}
}

// List implements store.Store. A listing that spans profiles is the merge of
// the stores', in key order.
func (s *Store) List(ctx context.Context, prefix, after string, limit int) ([]store.Entry, error) {
	targets := s.stores(prefix)
	if len(targets) == 1 {
		return targets[0].List(ctx, prefix, after, limit)
	}
	var out []store.Entry
	for _, st := range targets {
		es, err := st.List(ctx, prefix, after, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SetLegalHold implements store.Store.
func (s *Store) SetLegalHold(ctx context.Context, key string, on bool) error {
	return s.route(key).SetLegalHold(ctx, key, on)
}

// ExtendRetention implements store.Store.
func (s *Store) ExtendRetention(ctx context.Context, key string, until time.Time) error {
	return s.route(key).ExtendRetention(ctx, key, until)
}

// Prefixes implements store.Store.
func (s *Store) Prefixes(ctx context.Context, prefix, delimiter string) ([]string, error) {
	targets := s.stores(prefix)
	if len(targets) == 1 {
		return targets[0].Prefixes(ctx, prefix, delimiter)
	}
	seen := map[string]bool{}
	var out []string
	for _, st := range targets {
		ps, err := st.Prefixes(ctx, prefix, delimiter)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

var _ store.Store = (*Store)(nil)
