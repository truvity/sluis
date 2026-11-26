package hub

import (
	"maps"
	"strings"

	"github.com/truvity/sluis/internal/emailaddr"
)

// GoneAfterPasses is how many complete reads in a row an address must be
// missing from before a hub that only reads snapshots answers it as gone,
// authoritatively. A hub that reads its directory never needs it: a miss
// is read live there, and the live answer is the fact.
//
// One complete read missing an address is not enough. A reader cannot ask
// the directory whether the account was created since the read, and a
// removal acted on such an absence takes away access somebody still has.
// Three in a row, at the refresh interval, is an account that the
// directory has not had for that long.
const GoneAfterPasses = 3

// passesKept caps the per-domain count. Only "at least GoneAfterPasses"
// matters; the cap keeps a stored snapshot the same size pass after pass.
const passesKept = GoneAfterPasses

// carryAbsences fills in, on a snapshot just taken by a complete read, the
// counts that say for how many complete reads in a row an address has been
// missing, from prev, the snapshot this read replaces (nil when there was
// none or it could not be read: the counting then starts again, which
// holds a removal longer and never shortens it).
//
// covered is the domains this read answered for. A domain's count is the
// complete reads in a row that covered it; an address the directory once
// had is counted on its own from the first complete read it was missing
// from, and an address it never had is as old as its domain's count. A
// failed or partial read writes no snapshot, so it is no pass: it counts
// nothing and resets nothing.
func (s *Snapshot) carryAbsences(prev *Snapshot, covered []string) {
	s.Passes = make(map[string]int, len(covered))
	for _, d := range covered {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		n := 0
		if prev != nil {
			n = prev.Passes[d]
		}
		s.Passes[d] = min(n+1, passesKept)
	}
	s.Absent = map[string]int{}
	if prev == nil {
		return
	}
	count := func(address string, before int) {
		if _, held := s.PrimaryOf(address); held {
			return // back, or never gone: nothing to count
		}
		domain, ok := emailaddr.Domain(address)
		if !ok {
			return
		}
		if _, counted := s.Passes[domain]; !counted {
			// Not read on this pass, so not seen missing from it. Its
			// domain's count starts again when it is read, and the
			// address's absence is no older than that.
			return
		}
		// At GoneAfterPasses the address is dropped: its domain's count,
		// which is at least as high, answers for it from then on.
		if next := before + 1; next < GoneAfterPasses {
			s.Absent[address] = next
		}
	}
	for address, n := range prev.Absent {
		count(address, n)
	}
	for address := range prev.Accounts {
		if _, counting := prev.Absent[address]; !counting {
			count(address, 0)
		}
	}
	for address := range prev.aliases {
		if _, counting := prev.Absent[address]; !counting {
			count(address, 0)
		}
	}
}

// AbsentPasses is how many complete reads in a row an address has been
// missing from: 0 for an address the snapshot holds, as an account or an
// alias of one. A snapshot written before the counts were kept counts 0
// for everybody until its successors have counted again.
func (s *Snapshot) AbsentPasses(email string) int {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, held := s.PrimaryOf(email); held {
		return 0
	}
	domain, ok := emailaddr.Domain(email)
	if !ok {
		return 0
	}
	n := s.Passes[domain]
	if c, counting := s.Absent[email]; counting && c < n {
		n = c
	}
	return n
}

// gone reports whether an address has been missing from GoneAfterPasses
// complete reads in a row.
func (s *Snapshot) gone(email string) bool {
	return s.AbsentPasses(email) >= GoneAfterPasses
}

// keepAbsences carries the counts over to a narrowed copy, for the domains
// it still serves only: a domain narrowed away and served again has not
// been read in between, and its accounts are missing from the copy for
// that reason alone.
func (s *Snapshot) keepAbsences(from *Snapshot, serve []string) {
	kept := make(map[string]bool, len(serve))
	for _, d := range serve {
		kept[strings.ToLower(strings.TrimSpace(d))] = true
	}
	s.Passes = map[string]int{}
	for d, n := range from.Passes {
		if kept[d] {
			s.Passes[d] = n
		}
	}
	s.Absent = map[string]int{}
	for address, n := range from.Absent {
		if domain, ok := emailaddr.Domain(address); ok && kept[domain] {
			s.Absent[address] = n
		}
	}
}

// startAbsence marks an address a live read did not find: it is counted
// from the next complete read on, like an address that read drops, rather
// than as old as its domain's count.
func (s *Snapshot) startAbsence(address string) {
	if s.Absent == nil {
		s.Absent = map[string]int{}
	}
	s.Absent[address] = 0
}

// cloneAbsences copies the counts for [Snapshot.clone].
func (s *Snapshot) cloneAbsences(out *Snapshot) {
	out.Passes = maps.Clone(s.Passes)
	out.Absent = maps.Clone(s.Absent)
}
