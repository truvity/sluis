//nolint:lll // messages and fixtures are prose and one-line tables
package cloudflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ConditionIPKey is the key of the client-IP restriction in a token's condition.
// Settled against a live account (2026-10-08): the API accepts `request.ip` and
// `request_ip` alike but stores and returns `request_ip`, so that is what a
// clone sends and what is compared. A prototype's condition spelled the other
// way is renamed to it.
const ConditionIPKey = "request_ip"

// conditionIPAliases are the spellings a prototype's condition may carry.
var conditionIPAliases = []string{"request.ip", "request_ip"}

// The reasons a prototype is refused. They are the `reason` of the audit
// record (roster.cloudflare.token.refused).
const (
	ReasonPrototypeActive    = "prototype_active"
	ReasonPrototypeForbidden = "prototype_forbidden"
	ReasonPrototypeMissing   = "prototype_missing"
)

// PrototypeError is a prototype that must not be cloned.
type PrototypeError struct {
	// Reason is one of the Reason* constants.
	Reason string
	// Detail says what the check found: names of permission groups, never a
	// value.
	Detail string
}

func (e *PrototypeError) Error() string { return "cloudflare prototype refused: " + e.Detail }

// Forbidden permission groups. A prototype that grants any of them is refused,
// because a token holding one could widen itself or reach the account's money,
// membership or identity providers:
//
//	Account API Tokens Edit   a token that could make tokens (also "Write")
//	Billing                   the account's payment (Read or Write)
//	Account Settings          the account itself (Read or Write)
//	Memberships               who is in the account (Read or Write)
//	Access: Organizations, Identity Providers, and Groups   (Read or Write)
//
// The match is on the permission group's name as Cloudflare lists it, case
// folded; ids are resolved to names through the account's permission groups.
var forbiddenName = []*regexp.Regexp{
	regexp.MustCompile(`^(account )?api tokens (edit|write)$`),
	regexp.MustCompile(`^billing\b`),
	regexp.MustCompile(`^account settings\b`),
	regexp.MustCompile(`^memberships\b`),
	regexp.MustCompile(`identity providers`),
}

// ForbiddenGroup reports whether a permission group of that name may never be
// cloned.
func ForbiddenGroup(name string) bool {
	n := strings.ToLower(strings.Join(strings.Fields(name), " "))
	for _, re := range forbiddenName {
		if re.MatchString(n) {
			return true
		}
	}
	return false
}

// policy is the part of a token's policy that is cloned.
type policy struct {
	Effect           json.RawMessage   `json:"effect"`
	Resources        json.RawMessage   `json:"resources"`
	PermissionGroups []permissionGroup `json:"permission_groups"`
}

type permissionGroup struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func decodePolicies(raw json.RawMessage) ([]policy, error) {
	var ps []policy
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("the policies are not a list: %w", err)
	}
	return ps, nil
}

// GroupIDs are the ids of the permission groups a policy list names.
func GroupIDs(policies json.RawMessage) ([]string, error) {
	ps, err := decodePolicies(policies)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for _, p := range ps {
		for _, g := range p.PermissionGroups {
			if !seen[g.ID] {
				seen[g.ID] = true
				ids = append(ids, g.ID)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// CheckPrototype holds a prototype token to the rules before it is cloned: it
// must exist, be DISABLED (an active one is a usable token that never expires),
// have at least one policy, and name no forbidden permission group. names maps a
// permission group id to its name, from the account's permission groups; a group
// it does not know is judged by the name the policy itself carries, and refused
// when there is none, since what cannot be named cannot be vetted.
func CheckPrototype(tok Token, names map[string]string) error {
	if tok.ID == "" {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: "the prototype token does not exist"}
	}
	if tok.Status != StatusDisabled {
		return &PrototypeError{Reason: ReasonPrototypeActive, Detail: fmt.Sprintf("the prototype %s is %q; it must be disabled, or it would be a usable token that never expires", tok.ID, tok.Status)}
	}
	ps, err := decodePolicies(tok.Policies)
	if err != nil {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: err.Error()}
	}
	if len(ps) == 0 {
		return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: fmt.Sprintf("the prototype %s has no policies", tok.ID)}
	}
	var forbidden, unknown []string
	for _, p := range ps {
		if len(p.PermissionGroups) == 0 {
			return &PrototypeError{Reason: ReasonPrototypeMissing, Detail: fmt.Sprintf("a policy of the prototype %s names no permission group", tok.ID)}
		}
		for _, g := range p.PermissionGroups {
			name, ok := names[g.ID]
			if !ok {
				name = g.Name
			}
			switch {
			case name == "":
				unknown = append(unknown, g.ID)
			case ForbiddenGroup(name):
				forbidden = append(forbidden, name)
			}
		}
	}
	if len(forbidden) > 0 {
		sort.Strings(forbidden)
		return &PrototypeError{Reason: ReasonPrototypeForbidden, Detail: "the prototype grants " + strings.Join(dedupe(forbidden), ", ") + ", which a minted token may never hold"}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &PrototypeError{Reason: ReasonPrototypeForbidden, Detail: "the prototype names permission groups that cannot be resolved to a name: " + strings.Join(dedupe(unknown), ", ")}
	}
	return nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// IsPrototypeError reports whether err is a refused prototype, and returns it.
func IsPrototypeError(err error) (*PrototypeError, bool) {
	var pe *PrototypeError
	return pe, errors.As(err, &pe)
}

// ClonePolicies is the policies a clone is created with: each policy's effect,
// resources and permission groups exactly as the prototype has them. What
// Cloudflare generated for the prototype (policy ids, a permission group's name
// and meta) is left behind, because create takes only the permission group's id.
func ClonePolicies(policies json.RawMessage) (json.RawMessage, error) {
	ps, err := decodePolicies(policies)
	if err != nil {
		return nil, err
	}
	out := make([]policy, len(ps))
	for i, p := range ps {
		out[i] = policy{Effect: p.Effect, Resources: p.Resources}
		for _, g := range p.PermissionGroups {
			out[i].PermissionGroups = append(out[i].PermissionGroups, permissionGroup{ID: g.ID})
		}
	}
	return marshalNoEscape(out)
}

// CloneCondition is the condition a clone is created with: the prototype's,
// with the client-IP key spelled as [ConditionIPKey]. An absent, null or empty
// condition is none.
func CloneCondition(condition json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(condition)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("the condition is not an object: %w", err)
	}
	for _, alias := range conditionIPAliases {
		if v, ok := m[alias]; ok && alias != ConditionIPKey {
			delete(m, alias)
			if _, set := m[ConditionIPKey]; !set {
				m[ConditionIPKey] = v
			}
		}
	}
	// An empty restriction restricts nothing; sending it only invites a refusal.
	for k, v := range m {
		if t := bytes.TrimSpace(v); bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte("{}")) {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil, nil
	}
	return marshalNoEscape(m)
}

func marshalNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}
