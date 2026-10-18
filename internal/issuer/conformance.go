package issuer

import (
	"context"
	"errors"
	"fmt"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/policy"
)

// The conformance account (docs/decisions/0076) is the test subject of the
// OpenID conformance harness: a username and password, proved by SRP, with no
// privilege. It signs in only while the installation turns it on, only to
// clients the policy marks `harness`, and only while the recovery switch of
// docs/decisions/0075 has not been thrown. This file is the rules; the
// password door (password.go) is the way in.

// errConformanceOff is the refusal of everything the account opens while it is
// off.
var errConformanceOff = errors.New("issuer: the conformance account is turned off")

// ErrConformanceOff is returned when a sign-in of the conformance account is
// asked to complete after the installation turned the account off, or an
// operator threw the recovery switch.
var ErrConformanceOff = errConformanceOff

// UseConformance turns the conformance account on or off for this issuer
// (`conformance.enabled`). Call it before the issuer serves.
func (i *Issuer) UseConformance(on bool) { i.conformance = on }

// ConformanceOn reports whether the installation turned the account on.
func (i *Issuer) ConformanceOn() bool { return i != nil && i.conformance }

// conformanceCheck is what the account's sign-ins are asked, at every place
// they are read: the flag first, then the recovery switch. The account is shut
// by the same button that shuts recovery, so that no password door is open that
// an operator believes is closed.
func (i *Issuer) conformanceCheck(ctx context.Context) recoveryVerdict {
	if !i.ConformanceOn() {
		return recoveryEnded
	}
	return i.recoveryCheck(ctx)
}

// isHarness reports whether the policy marks the client a harness's.
func (i *Issuer) isHarness(clientID string) bool {
	client, ok := i.Policy().Client(clientID)
	return ok && client.Harness
}

// conformanceEntitled is the entitlement of the conformance account to a
// client. It is the client's `harness` mark and nothing else: the client's
// `requires` is not consulted, because the account is held to no group, and it
// never opens a resource.
func (s *Storage) conformanceEntitled(ctx context.Context, clientID, resource string) error {
	switch s.iss.conformanceCheck(ctx) {
	case recoveryEnded:
		return ErrConformanceOff
	case recoveryUnknown:
		return fmt.Errorf("%w: %w", ErrConformanceOff, errRecoveryUnknown)
	case recoveryOn:
	}
	if resource != "" {
		return fmt.Errorf("%w: the conformance account opens no resource", ErrNotEntitled)
	}
	if !s.iss.isHarness(clientID) {
		return fmt.Errorf("%w: %s is not a harness client", ErrNotEntitled, clientID)
	}
	return nil
}

// conformanceToken refuses a token for the account's sign-in once the account
// is off, and one for a client that is not a harness's.
func (s *Storage) conformanceToken(ctx context.Context, clientID string) error {
	switch s.iss.conformanceCheck(ctx) {
	case recoveryEnded:
		return deny(ctx, oidc.ErrInvalidGrant, errConformanceOff)
	case recoveryUnknown:
		return fault(ctx, errRecoveryUnknown)
	case recoveryOn:
	}
	if !s.iss.isHarness(clientID) {
		return deny(ctx, oidc.ErrInvalidGrant, errNotAdmitted)
	}
	return nil
}

// ConformanceSubject checks that the policy does not make the account an
// operator. The account is a ServiceAccount to the policy, so a matcher for it
// in an operator group would make a test subject an operator: the issuer
// refuses to start with one.
func ConformanceSubject(set *policy.Set, subject string) error {
	account, ok := policy.ParseServiceAccountSubject(subject)
	if !ok {
		return fmt.Errorf("conformance subject %q is not a ServiceAccount", subject)
	}
	for _, group := range set.Evaluate(policy.Input{ServiceAccount: &account}).Groups {
		if policy.IsOperators(group) {
			return fmt.Errorf("the policy puts the conformance account (%s) in the operator group %q: "+
				"it is a non-privileged test subject and the service will not start with it", subject, group)
		}
	}
	return nil
}
