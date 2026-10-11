package iameval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Request is one call to evaluate.
type Request struct {
	// Principal is the caller's role ARN; it is also the value of aws:PrincipalArn.
	Principal string
	// Action is the API action, for example `ssm:GetParameter`.
	Action string
	// Resource is the ARN the call is made on.
	Resource string
	// Context is the request's condition keys and their values. A key not
	// present is absent: `Null` reads it as such.
	Context map[string][]string
}

// WithContext returns a copy of the request with the key set to the values.
func (r Request) WithContext(key string, values ...string) Request {
	out := r.clone()
	out.Context[strings.ToLower(key)] = values
	return out
}

// WithEncryptionContext returns a copy of the request carrying a KMS
// encryption context: one kms:EncryptionContext:<k> key per entry, and
// kms:EncryptionContextKeys with all the keys.
func (r Request) WithEncryptionContext(ec map[string]string) Request {
	out := r.clone()
	var keys []string
	for k, v := range ec {
		out.Context["kms:encryptioncontext:"+strings.ToLower(k)] = []string{v}
		keys = append(keys, k)
	}
	out.Context["kms:encryptioncontextkeys"] = keys
	return out
}

func (r Request) clone() Request {
	out := r
	out.Context = map[string][]string{}
	for k, v := range r.Context {
		out.Context[k] = v
	}
	return out
}

// Decision is the result of an evaluation.
type Decision struct {
	Allowed bool
	// Reason says which statement decided, or why nothing allowed.
	Reason string
}

// Policy is a parsed policy document.
type Policy struct {
	statements []statement
}

// Len is the number of statements.
func (p Policy) Len() int { return len(p.statements) }

type statement struct {
	sid        string
	allow      bool
	principals *principals
	actions    []string
	notActions []string
	resources  []string
	notRes     []string
	conditions []condition
}

type principals struct {
	any  bool
	arns []string
}

type condition struct {
	op     string
	values map[string][]string
}

type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one any
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	var add func(v any) error
	add = func(v any) error {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				if err := add(e); err != nil {
					return err
				}
			}
		case string, bool, float64:
			*l = append(*l, fmt.Sprint(x))
		default:
			return fmt.Errorf("a value of type %T where a string or a list of strings is expected", v)
		}
		return nil
	}
	return add(one)
}

type rawStatement struct {
	Sid          string                           `json:"Sid"`
	Effect       string                           `json:"Effect"`
	Principal    json.RawMessage                  `json:"Principal"`
	NotPrincipal json.RawMessage                  `json:"NotPrincipal"`
	Action       *stringList                      `json:"Action"`
	NotAction    *stringList                      `json:"NotAction"`
	Resource     *stringList                      `json:"Resource"`
	NotResource  *stringList                      `json:"NotResource"`
	Condition    map[string]map[string]stringList `json:"Condition"`
}

// Parse reads a policy. The input is a JSON document (string or []byte), a
// list of statements, or a whole document as a map; anything else that
// encoding/json can marshal into one of those shapes works too.
func Parse(in any) (Policy, error) {
	var raw []byte
	switch x := in.(type) {
	case string:
		raw = []byte(x)
	case []byte:
		raw = x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return Policy{}, fmt.Errorf("iameval: %w", err)
		}
		raw = b
	}
	var doc struct {
		Statement json.RawMessage `json:"Statement"`
	}
	list := raw
	if err := json.Unmarshal(raw, &doc); err == nil && doc.Statement != nil {
		list = doc.Statement
	}
	var many []rawStatement
	if err := json.Unmarshal(list, &many); err != nil {
		var one rawStatement
		if err2 := json.Unmarshal(list, &one); err2 != nil {
			return Policy{}, fmt.Errorf("iameval: not a policy: %w", err)
		}
		many = []rawStatement{one}
	}
	var p Policy
	for i, r := range many {
		s, err := convert(r)
		if err != nil {
			return Policy{}, fmt.Errorf("iameval: statement %d (%q): %w", i, r.Sid, err)
		}
		p.statements = append(p.statements, s)
	}
	return p, nil
}

// MustParse is Parse that panics; for test setup.
func MustParse(in any) Policy {
	p, err := Parse(in)
	if err != nil {
		panic(err)
	}
	return p
}

func convert(r rawStatement) (statement, error) {
	s := statement{sid: r.Sid}
	switch r.Effect {
	case "Allow":
		s.allow = true
	case "Deny":
	default:
		return s, fmt.Errorf("effect %q is neither Allow nor Deny", r.Effect)
	}
	if len(r.NotPrincipal) > 0 {
		return s, errors.New("NotPrincipal is not evaluated")
	}
	if len(r.Principal) > 0 {
		pr, err := parsePrincipals(r.Principal)
		if err != nil {
			return s, err
		}
		s.principals = pr
	}
	if (r.Action == nil) == (r.NotAction == nil) {
		return s, errors.New("exactly one of Action and NotAction is required")
	}
	if r.Action != nil {
		s.actions = *r.Action
	} else {
		s.notActions = *r.NotAction
	}
	if (r.Resource == nil) == (r.NotResource == nil) {
		return s, errors.New("exactly one of Resource and NotResource is required")
	}
	if r.Resource != nil {
		s.resources = *r.Resource
	} else {
		s.notRes = *r.NotResource
	}
	for _, list := range [][]string{s.resources, s.notRes} {
		for _, v := range list {
			if strings.Contains(v, "${") {
				return s, fmt.Errorf("policy variable in Resource %q is not evaluated", v)
			}
		}
	}
	for op, keys := range r.Condition {
		if _, _, _, err := splitOperator(op); err != nil {
			return s, err
		}
		c := condition{op: op, values: map[string][]string{}}
		for k, vals := range keys {
			for _, v := range vals {
				if strings.Contains(v, "${") {
					return s, fmt.Errorf("policy variable in condition %s/%s %q is not evaluated", op, k, v)
				}
			}
			c.values[strings.ToLower(k)] = vals
		}
		s.conditions = append(s.conditions, c)
	}
	return s, nil
}

func parsePrincipals(raw json.RawMessage) (*principals, error) {
	var star string
	if err := json.Unmarshal(raw, &star); err == nil {
		if star != "*" {
			return nil, fmt.Errorf("principal %q is not *", star)
		}
		return &principals{any: true}, nil
	}
	var obj map[string]stringList
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("principal: %w", err)
	}
	p := &principals{}
	for kind, vals := range obj {
		switch kind {
		case "AWS":
			for _, v := range vals {
				if v == "*" {
					p.any = true
				}
				p.arns = append(p.arns, v)
			}
		case "Service", "Federated", "CanonicalUser":
			// Never an IAM role calling with its own credentials.
		default:
			return nil, fmt.Errorf("principal type %q is not evaluated", kind)
		}
	}
	return p, nil
}

// Evaluate decides a request against an identity policy and, when the
// resource is a KMS key, the key policy (pass the zero Policy for none).
func Evaluate(req Request, identity, key Policy) (Decision, error) {
	if req.Principal == "" || req.Action == "" || req.Resource == "" {
		return Decision{}, errors.New("iameval: a request needs a principal, an action and a resource")
	}
	ctx := map[string][]string{}
	for k, v := range req.Context {
		ctx[strings.ToLower(k)] = v
	}
	if _, ok := ctx["aws:principalarn"]; !ok {
		ctx["aws:principalarn"] = []string{req.Principal}
	}
	var identityAllow, keyDirect, keyDelegated string
	for _, side := range []struct {
		name string
		pol  Policy
		key  bool
	}{{"identity", identity, false}, {"key", key, true}} {
		for i := range side.pol.statements {
			s := side.pol.statements[i]
			if (s.principals != nil) == !side.key {
				return Decision{}, fmt.Errorf("iameval: %s policy statement %q: a Principal belongs in a key policy only", side.name, s.sid)
			}
			ok, delegated, err := s.matches(req, ctx, side.key)
			if err != nil {
				return Decision{}, fmt.Errorf("iameval: %s policy statement %q: %w", side.name, s.sid, err)
			}
			if !ok {
				continue
			}
			label := side.name + " " + s.sid
			switch {
			case !s.allow:
				return Decision{Reason: "explicit deny by " + label}, nil
			case !side.key:
				identityAllow = label
			case delegated:
				keyDelegated = label
			default:
				keyDirect = label
			}
		}
	}
	switch {
	case keyDirect != "":
		return Decision{Allowed: true, Reason: "allowed by " + keyDirect}, nil
	case key.Len() > 0 && keyDelegated == "":
		return Decision{Reason: "no key policy statement allows it"}, nil
	case identityAllow == "":
		return Decision{Reason: "no identity policy statement allows it"}, nil
	case key.Len() > 0:
		return Decision{Allowed: true, Reason: "allowed by " + identityAllow + " under " + keyDelegated}, nil
	}
	return Decision{Allowed: true, Reason: "allowed by " + identityAllow}, nil
}

// matches reports whether the statement applies to the request; delegated is
// true when a key policy statement applies through the account root.
func (s statement) matches(req Request, ctx map[string][]string, isKey bool) (ok, delegated bool, err error) {
	if isKey {
		if s.principals == nil {
			return false, false, errors.New("a key policy statement needs a Principal")
		}
		var direct bool
		direct, delegated = s.principals.matches(req.Principal)
		if !direct && !delegated {
			return false, false, nil
		}
		delegated = delegated && !direct
	}
	if !matchAny(s.actions, s.notActions, req.Action, true) || !matchAny(s.resources, s.notRes, req.Resource, false) {
		return false, false, nil
	}
	for _, c := range s.conditions {
		held, err := c.holds(ctx)
		if err != nil {
			return false, false, err
		}
		if !held {
			return false, false, nil
		}
	}
	return true, delegated, nil
}

func matchAny(in, not []string, v string, fold bool) bool {
	hit := func(list []string) bool {
		for _, p := range list {
			if fold {
				if Glob(strings.ToLower(p), strings.ToLower(v)) {
					return true
				}
			} else if Glob(p, v) {
				return true
			}
		}
		return false
	}
	if in != nil {
		return hit(in)
	}
	return !hit(not)
}

// matches says whether the statement's principals name the caller (direct, or
// `*`) or the root of the caller's account (root, which delegates to IAM).
func (p *principals) matches(caller string) (direct, root bool) {
	if p.any {
		return true, false
	}
	for _, a := range p.arns {
		switch {
		case a == caller:
			direct = true
		case isRootOf(a, accountOf(caller)):
			root = true
		}
	}
	return direct, root
}

// isRootOf reports whether p is the root principal of the account: the account
// id itself, or `arn:<partition>:iam::<account>:root`.
func isRootOf(p, account string) bool {
	if account == "" {
		return false
	}
	parts := strings.SplitN(p, ":", 6)
	return p == account || (len(parts) == 6 && parts[0] == "arn" && parts[2] == "iam" && parts[4] == account && parts[5] == "root")
}

func accountOf(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

// Glob matches `*` (any run) and `?` (one character) against the whole of s.
func Glob(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]) && p[pi] != '*':
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// ArnGlob matches an ARN pattern section by section: `*` and `?` never cross a colon.
func ArnGlob(pattern, arn string) bool {
	ps, as := strings.SplitN(pattern, ":", 6), strings.SplitN(arn, ":", 6)
	if len(ps) != len(as) {
		return false
	}
	for i := range ps {
		// The last section (the resource) may hold colons, and a `*` there spans them.
		if !Glob(ps[i], as[i]) {
			return false
		}
	}
	return true
}

func splitOperator(op string) (set, base string, ifExists bool, err error) {
	base = op
	for _, pfx := range []string{"ForAnyValue:", "ForAllValues:"} {
		if strings.HasPrefix(base, pfx) {
			set, base = strings.TrimSuffix(pfx, ":"), strings.TrimPrefix(base, pfx)
		}
	}
	if strings.HasSuffix(base, "IfExists") {
		ifExists, base = true, strings.TrimSuffix(base, "IfExists")
	}
	switch base {
	case "StringEquals", "StringNotEquals", "StringLike", "StringNotLike",
		"ArnEquals", "ArnLike", "ArnNotEquals", "ArnNotLike", "Bool":
	case "Null":
		if set != "" || ifExists {
			return "", "", false, fmt.Errorf("operator %q: Null takes no set prefix or IfExists", op)
		}
	default:
		return "", "", false, fmt.Errorf("condition operator %q is not evaluated", op)
	}
	return set, base, ifExists, nil
}

func (c condition) holds(ctx map[string][]string) (bool, error) {
	set, base, ifExists, err := splitOperator(c.op)
	if err != nil {
		return false, err
	}
	for key, want := range c.values {
		have, present := ctx[key]
		if base == "Null" {
			ok := false
			for _, w := range want {
				if (w == "true") == !present {
					ok = true
				}
			}
			if !ok {
				return false, nil
			}
			continue
		}
		negated := strings.Contains(base, "Not")
		if !present || len(have) == 0 {
			// A missing key fails a positive operator and passes a negated one;
			// ForAllValues passes an empty set, ForAnyValue fails it.
			var ok bool
			switch {
			case ifExists:
				ok = true
			case set == "ForAllValues":
				ok = true
			case set == "ForAnyValue":
				ok = false
			default:
				ok = negated
			}
			if !ok {
				return false, nil
			}
			continue
		}
		if set == "" && len(have) > 1 {
			return false, fmt.Errorf("key %s has %d values and %s has no set prefix (ForAnyValue or ForAllValues)", key, len(have), c.op)
		}
		count := 0
		for _, h := range have {
			if satisfies(base, h, want) {
				count++
			}
		}
		switch set {
		case "ForAnyValue":
			if count == 0 {
				return false, nil
			}
		default: // single value, or ForAllValues
			if count != len(have) {
				return false, nil
			}
		}
	}
	return true, nil
}

// satisfies is the operator on one context value against the policy's values.
func satisfies(base, have string, want []string) bool {
	positive := strings.ReplaceAll(base, "Not", "")
	matched := false
	for _, w := range want {
		var hit bool
		switch positive {
		case "StringEquals":
			hit = have == w
		case "StringLike":
			hit = Glob(w, have)
		case "ArnEquals", "ArnLike":
			hit = ArnGlob(w, have)
		case "Bool":
			hit = strings.EqualFold(have, w)
		}
		if hit {
			matched = true
			break
		}
	}
	if base != positive {
		return !matched
	}
	return matched
}
