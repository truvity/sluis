// Package agentgate keeps agent-class sessions (docs/decisions/0040-agent-class-sessions.md)
// refused at load until the release that adds their consent page.
//
// The machinery ships first: the class recorded on a chain, its lifetimes,
// its token caps. What makes it safe to use -- an authorization that never
// completes silently, and a browser sign-out that spares agent sessions --
// ships in a later release. Until then a policy that says `session: agent`
// is refused when it is loaded ([policy.Policy.Validate]), so that a
// release cut in between cannot hand out a 30-day chain nobody consented
// to. That release removes this package.
//
// There is no switch an installation can turn. The gate opens only for a
// test binary, from its TestMain, so that the machinery stays tested.
package agentgate

import "sync/atomic"

var open atomic.Bool

// Open reports whether a policy may declare `session: agent`. False in
// every build that serves anybody.
func Open() bool { return open.Load() }

// OpenForTests opens the gate for one test binary. Call it from TestMain,
// before m.Run; it takes the *testing.M so that nothing outside a test has
// a value to call it with.
func OpenForTests(m interface{ Run() int }) {
	if m != nil {
		open.Store(true)
	}
}
