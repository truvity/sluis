package iameval_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/deploy/pulumi/internal/iameval"
)

const (
	role = "arn:aws:iam::111122223333:role/acme-fn"
	root = "arn:aws:iam::111122223333:root"
	key  = "arn:aws:kms:eu-west-1:111122223333:key/k1"
)

func eval(t *testing.T, req iameval.Request, identity, keyPolicy string) iameval.Decision {
	t.Helper()
	var kp iameval.Policy
	if keyPolicy != "" {
		kp = iameval.MustParse(keyPolicy)
	}
	d, err := iameval.Evaluate(req, iameval.MustParse(identity), kp)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGlobs(t *testing.T) {
	for _, c := range []struct {
		pat, s string
		want   bool
	}{
		{"a*", "abc", true}, {"*c", "abc", true}, {"a*c", "ac", true}, {"a?c", "abc", true}, {"a?c", "ac", false},
		{"*", "", true}, {"a*b*c", "aXbYbZc", true}, {"a*b*c", "aXbYbZ", false}, {"abc", "abcd", false},
	} {
		if got := iameval.Glob(c.pat, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v", c.pat, c.s, got)
		}
	}
	// An ARN glob does not cross a colon in the early sections.
	if iameval.ArnGlob("arn:aws:iam::*:role/x", "arn:aws:iam::1:2:role/x") {
		t.Error("a * crossed a colon")
	}
	if !iameval.ArnGlob("arn:aws:iam::*:role/x*", "arn:aws:iam::1:role/xyz") {
		t.Error("an ARN glob did not match")
	}
}

func TestAllowDenyAndGlobs(t *testing.T) {
	id := `[
	  {"Sid":"A","Effect":"Allow","Action":"ssm:Get*","Resource":"arn:aws:ssm:r:1:parameter/p/*"},
	  {"Sid":"D","Effect":"Deny","Action":["ssm:GetParameterHistory"],"Resource":"*"}]`
	req := iameval.Request{Principal: role, Action: "ssm:GetParameter", Resource: "arn:aws:ssm:r:1:parameter/p/x"}
	if d := eval(t, req, id, ""); !d.Allowed {
		t.Errorf("get denied: %s", d.Reason)
	}
	req.Action = "SSM:getparameters"
	if d := eval(t, req, id, ""); !d.Allowed {
		t.Errorf("an action compares without case: %s", d.Reason)
	}
	req.Action = "ssm:GetParameterHistory"
	if d := eval(t, req, id, ""); d.Allowed || !strings.Contains(d.Reason, "explicit deny by identity D") {
		t.Errorf("the deny lost: %+v", d)
	}
	req.Action, req.Resource = "ssm:GetParameter", "arn:aws:ssm:r:1:parameter/q/x"
	if d := eval(t, req, id, ""); d.Allowed {
		t.Error("another path allowed")
	}
	req.Action, req.Resource = "ssm:PutParameter", "arn:aws:ssm:r:1:parameter/p/x"
	if d := eval(t, req, id, ""); d.Allowed {
		t.Error("an action outside the glob allowed")
	}
}

func TestNotActionAndNotResource(t *testing.T) {
	id := `[{"Sid":"A","Effect":"Allow","NotAction":"s3:Delete*","NotResource":"arn:aws:s3:::b/secret/*"}]`
	r := iameval.Request{Principal: role, Action: "s3:GetObject", Resource: "arn:aws:s3:::b/x"}
	if !eval(t, r, id, "").Allowed {
		t.Error("NotAction/NotResource denied a plain get")
	}
	r.Action = "s3:DeleteObject"
	if eval(t, r, id, "").Allowed {
		t.Error("NotAction allowed a delete")
	}
	r.Action, r.Resource = "s3:GetObject", "arn:aws:s3:::b/secret/k"
	if eval(t, r, id, "").Allowed {
		t.Error("NotResource allowed the excluded resource")
	}
}

func TestConditionOperators(t *testing.T) {
	base := iameval.Request{Principal: role, Action: "x:Do", Resource: "r"}
	for _, c := range []struct {
		name string
		cond string
		req  iameval.Request
		want bool
	}{
		{"StringEquals hit", `{"StringEquals":{"k:a":"v"}}`, base.WithContext("k:a", "v"), true},
		{"StringEquals miss", `{"StringEquals":{"k:a":"v"}}`, base.WithContext("k:a", "w"), false},
		{"StringEquals absent", `{"StringEquals":{"k:a":"v"}}`, base, false},
		{"StringEquals key case", `{"StringEquals":{"K:A":"v"}}`, base.WithContext("k:a", "v"), true},
		{"StringNotEquals absent passes", `{"StringNotEquals":{"k:a":"v"}}`, base, true},
		{"StringNotEquals hit fails", `{"StringNotEquals":{"k:a":"v"}}`, base.WithContext("k:a", "v"), false},
		{"StringLike", `{"StringLike":{"k:a":"ssm.*.amazonaws.com"}}`, base.WithContext("k:a", "ssm.r.amazonaws.com"), true},
		{"StringLike miss", `{"StringLike":{"k:a":"ssm.*.amazonaws.com"}}`, base.WithContext("k:a", "dynamodb.r.amazonaws.com"), false},
		{"StringNotLike", `{"StringNotLike":{"k:a":"p/*"}}`, base.WithContext("k:a", "q/x"), true},
		{"ArnLike", `{"ArnLike":{"k:a":"arn:aws:iam::*:role/fn-*"}}`, base.WithContext("k:a", "arn:aws:iam::1:role/fn-x"), true},
		{"ArnLike miss", `{"ArnLike":{"k:a":"arn:aws:iam::*:role/fn-*"}}`, base.WithContext("k:a", "arn:aws:iam::1:role/other"), false},
		{"ArnEquals principal", `{"ArnEquals":{"aws:PrincipalArn":["` + role + `"]}}`, base, true},
		{"ArnNotEquals principal", `{"ArnNotEquals":{"aws:PrincipalArn":["` + role + `"]}}`, base, false},
		{"Null absent", `{"Null":{"k:a":"true"}}`, base, true},
		{"Null present", `{"Null":{"k:a":"true"}}`, base.WithContext("k:a", "v"), false},
		{"Null false present", `{"Null":{"k:a":"false"}}`, base.WithContext("k:a", "v"), true},
		{"Bool", `{"Bool":{"k:a":"true"}}`, base.WithContext("k:a", "true"), true},
		{"IfExists absent", `{"StringEqualsIfExists":{"k:a":"v"}}`, base, true},
		{"IfExists wrong", `{"StringEqualsIfExists":{"k:a":"v"}}`, base.WithContext("k:a", "w"), false},
		{"ForAnyValue hit", `{"ForAnyValue:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`,
			base.WithContext("dynamodb:LeadingKeys", "a", "maintenance"), true},
		{"ForAnyValue miss", `{"ForAnyValue:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`,
			base.WithContext("dynamodb:LeadingKeys", "a", "b"), false},
		{"ForAnyValue absent", `{"ForAnyValue:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`, base, false},
		{"ForAllValues one stray", `{"ForAllValues:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`,
			base.WithContext("dynamodb:LeadingKeys", "maintenance", "b"), false},
		{"ForAllValues all", `{"ForAllValues:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`,
			base.WithContext("dynamodb:LeadingKeys", "maintenance"), true},
		{"ForAllValues absent passes", `{"ForAllValues:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}`, base, true},
		{"ForAnyValue:StringNotEquals", `{"ForAnyValue:StringNotEquals":{"kms:EncryptionContextKeys":["instance","purpose"]}}`,
			base.WithEncryptionContext(map[string]string{"instance": "i", "extra": "x"}), true},
		{"kms:EncryptionContext key", `{"StringEquals":{"kms:EncryptionContext:purpose":"sign"}}`,
			base.WithEncryptionContext(map[string]string{"purpose": "sign"}), true},
	} {
		id := `[{"Sid":"S","Effect":"Allow","Action":"x:Do","Resource":"r","Condition":` + c.cond + `}]`
		if got := eval(t, c.req, id, "").Allowed; got != c.want {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestADenyWithAConditionHoldsOnlyWhenTheConditionDoes(t *testing.T) {
	id := `[{"Sid":"A","Effect":"Allow","Action":"dynamodb:*","Resource":"*"},
	  {"Sid":"D","Effect":"Deny","Action":"dynamodb:PutItem","Resource":"*",
	   "Condition":{"ForAnyValue:StringEquals":{"dynamodb:LeadingKeys":["maintenance"]}}}]`
	r := iameval.Request{Principal: role, Action: "dynamodb:PutItem", Resource: "t"}
	if !eval(t, r.WithContext("dynamodb:LeadingKeys", "session"), id, "").Allowed {
		t.Error("a write outside the partition was denied")
	}
	if eval(t, r.WithContext("dynamodb:LeadingKeys", "session", "maintenance"), id, "").Allowed {
		t.Error("a mixed write reached the partition")
	}
}

const (
	rootKeyPolicy = `[{"Sid":"R","Effect":"Allow","Principal":{"AWS":"` + root + `"},"Action":"kms:*","Resource":"*",
	  "Condition":{"ArnEquals":{"aws:PrincipalArn":"` + role + `"},"StringEquals":{"kms:ViaService":"ssm.r.amazonaws.com"}}}]`
	allowKms = `[{"Sid":"I","Effect":"Allow","Action":"kms:Decrypt","Resource":"` + key + `"}]`
)

func TestKeyPolicyAndIdentityPolicyTogether(t *testing.T) {
	r := iameval.Request{Principal: role, Action: "kms:Decrypt", Resource: key}.WithContext("kms:ViaService", "ssm.r.amazonaws.com")
	if d := eval(t, r, allowKms, rootKeyPolicy); !d.Allowed {
		t.Errorf("both allow, denied: %s", d.Reason)
	}
	// The key policy is a ceiling: an identity Allow alone is not enough...
	direct := iameval.Request{Principal: role, Action: "kms:Decrypt", Resource: key}
	if d := eval(t, direct, allowKms, rootKeyPolicy); d.Allowed {
		t.Error("the key policy's ViaService condition was bypassed")
	}
	// ...and a root statement alone is not a grant.
	if d := eval(t, r, `[]`, rootKeyPolicy); d.Allowed {
		t.Error("the key policy's root statement granted without an identity Allow")
	}
	// A statement that names the principal grants by itself.
	own := `[{"Sid":"O","Effect":"Allow","Principal":{"AWS":"` + role + `"},"Action":"kms:Decrypt","Resource":"*"}]`
	if d := eval(t, direct, `[]`, own); !d.Allowed {
		t.Errorf("a key policy naming the principal did not allow: %s", d.Reason)
	}
	// Another account's root does not delegate.
	other := strings.ReplaceAll(rootKeyPolicy, "111122223333:root", "999999999999:root")
	if d := eval(t, r, allowKms, other); d.Allowed {
		t.Error("another account's root delegated")
	}
	// An explicit deny in the key policy beats both Allows.
	deny := `[{"Sid":"X","Effect":"Deny","Principal":{"AWS":"*"},"Action":"kms:Decrypt","Resource":"*"}]`
	if d, err := iameval.Evaluate(r, iameval.MustParse(allowKms), iameval.MustParse(deny)); err != nil || d.Allowed {
		t.Errorf("a key policy deny lost: %+v %v", d, err)
	}
}

func TestWhatItCannotEvaluateIsAnError(t *testing.T) {
	r := iameval.Request{Principal: role, Action: "x:Do", Resource: "r"}
	for name, doc := range map[string]string{
		"operator":        `[{"Effect":"Allow","Action":"x:Do","Resource":"r","Condition":{"NumericEquals":{"k":"1"}}}]`,
		"variable":        `[{"Effect":"Allow","Action":"x:Do","Resource":"r","Condition":{"StringEquals":{"k":"${aws:username}"}}}]`,
		"resource var":    `[{"Effect":"Allow","Action":"x:Do","Resource":"r/${aws:username}"}]`,
		"identity princ.": `[{"Effect":"Allow","Principal":"*","Action":"x:Do","Resource":"r"}]`,
		"notprincipal":    `[{"Effect":"Allow","NotPrincipal":"*","Action":"x:Do","Resource":"r"}]`,
		"both actions":    `[{"Effect":"Allow","Action":"x:Do","NotAction":"y:*","Resource":"r"}]`,
		"no effect":       `[{"Action":"x:Do","Resource":"r"}]`,
	} {
		p, err := iameval.Parse(doc)
		if err == nil {
			_, err = iameval.Evaluate(r, p, iameval.Policy{})
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	multi := iameval.MustParse(`[{"Effect":"Allow","Action":"x:Do","Resource":"r","Condition":{"StringEquals":{"k":"a"}}}]`)
	if _, err := iameval.Evaluate(r.WithContext("k", "a", "b"), multi, iameval.Policy{}); err == nil {
		t.Error("a multi-valued key under a plain operator was not refused")
	}
	if _, err := iameval.Evaluate(iameval.Request{}, multi, iameval.Policy{}); err == nil {
		t.Error("an empty request was not refused")
	}
}

func TestParseTakesADocumentAMapAndAList(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"x:Do","Resource":"r"}}`
	if p := iameval.MustParse(doc); p.Len() != 1 {
		t.Errorf("one statement object: %d", p.Len())
	}
	if p := iameval.MustParse([]map[string]any{{"Effect": "Allow", "Action": []string{"x:Do"}, "Resource": "r"}}); p.Len() != 1 {
		t.Errorf("a list of maps: %d", p.Len())
	}
	if _, err := iameval.Parse("not json"); err == nil {
		t.Error("garbage parsed")
	}
}
