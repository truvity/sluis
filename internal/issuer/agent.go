package issuer

import (
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/policy"
)

// SessionClass is who holds a refresh chain: a person at a browser, or a
// background host such as an MCP client that keeps its refresh token in a
// credential store of its own. It is decided once, when an authorization
// completes ([Storage.Complete]), recorded on the authorization request and
// then on the session at code redemption ([Sessions.Record]), and never
// re-derived. See docs/decisions/0040-agent-class-sessions.md.
type SessionClass string

// The session classes.
const (
	// ClassInteractive is held to the installation's `lifetimes`. A session
	// recorded with no class, which is every one recorded before classes
	// existed, is one.
	ClassInteractive SessionClass = policy.SessionInteractive
	// ClassAgent is held to `lifetimes.agent`: a longer idle and absolute
	// limit, and a mandatory short cap on its access and ID tokens.
	ClassAgent SessionClass = policy.SessionAgent
)

// AgentLifetimes are the agent class's own lifetimes: the service
// configuration's `lifetimes.agent`.
type AgentLifetimes struct {
	// Refresh is the idle limit: how long an agent chain lives without
	// being refreshed.
	Refresh time.Duration
	// Absolute is how long an agent chain lives from `auth_time`, however
	// often it is refreshed. A resource's `absolute_cap` may only shorten
	// it.
	Absolute time.Duration
	// Access caps every access and ID token an agent chain is issued. It
	// is mandatory, so the bound on revocation latency holds for an agent
	// client that names no resource as well.
	Access time.Duration
}

// The agent class's defaults and ceilings.
const (
	// DefaultAgentRefresh is 14 days: half the absolute limit, so a host
	// used once a fortnight lives its full 30 days and one left longer does
	// not.
	DefaultAgentRefresh = 14 * 24 * time.Hour
	// DefaultAgentAbsolute is 30 days from auth_time.
	DefaultAgentAbsolute = 30 * 24 * time.Hour
	// DefaultAgentAccess is 30 minutes.
	DefaultAgentAccess = 30 * time.Minute
	// MaxAgentAbsolute is 90 days: a ceiling so that a typo cannot turn the
	// class into "never signs in again".
	MaxAgentAbsolute = 90 * 24 * time.Hour
	// MaxAgentAccess is one hour.
	MaxAgentAccess = time.Hour
)

// withDefaults fills what is unset (zero) with the defaults. A negative
// value is left for [CheckAgentLifetimes] to refuse.
func (a AgentLifetimes) withDefaults() AgentLifetimes {
	if a.Refresh == 0 {
		a.Refresh = DefaultAgentRefresh
	}
	if a.Absolute == 0 {
		a.Absolute = DefaultAgentAbsolute
	}
	if a.Access == 0 {
		a.Access = DefaultAgentAccess
	}
	return a
}

// CheckAgentLifetimes refuses `lifetimes.agent` values that could never be
// meant, in the style of [policy.Resource.CheckAbsoluteCap]: an absolute
// limit that is not positive or is past [MaxAgentAbsolute], an idle limit
// that is not positive or is longer than the absolute one, and an
// access-token cap that is not positive or is past [MaxAgentAccess].
// Shortening is always allowed. [issuerapp.FromConfig] calls it at load, so
// the service stops rather than runs with a class nobody meant.
func CheckAgentLifetimes(a AgentLifetimes) error {
	var errs []error
	if a.Absolute <= 0 || a.Absolute > MaxAgentAbsolute {
		errs = append(errs, fmt.Errorf(
			"lifetimes.agent.absolute (%s) must be positive and at most %s: an agent chain still has to end",
			a.Absolute, MaxAgentAbsolute))
	}
	if a.Refresh <= 0 {
		errs = append(errs, fmt.Errorf("lifetimes.agent.refresh (%s) must be positive", a.Refresh))
	} else if a.Refresh > a.Absolute {
		errs = append(errs, fmt.Errorf(
			"lifetimes.agent.refresh (%s) must be at most lifetimes.agent.absolute (%s): "+
				"an idle limit past the absolute one never applies", a.Refresh, a.Absolute))
	}
	if a.Access <= 0 || a.Access > MaxAgentAccess {
		errs = append(errs, fmt.Errorf(
			"lifetimes.agent.access (%s) must be positive and at most %s: it is the bound on how long "+
				"an agent client keeps access after its person is removed", a.Access, MaxAgentAccess))
	}
	return errors.Join(errs...)
}
