package edgecloudflare_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	edge "github.com/truvity/sluis/deploy/pulumi/edge/cloudflare"
)

// The default rule caches the three public documents of the domain, for five
// minutes, for GET and HEAD without an Origin header, and nothing else.
func TestPublicCacheRuleDefaults(t *testing.T) {
	r, err := edge.PublicCacheRule(domainName, edge.PublicCacheArgs{})
	if err != nil {
		t.Fatal(err)
	}
	want := `(http.host eq "access.example.test" and http.request.method in {"GET" "HEAD"} and ` +
		`http.request.uri.path in {"/.well-known/oauth-authorization-server" "/.well-known/openid-configuration" "/keys"} ` +
		`and not any(lower(http.request.headers.names[*])[*] eq "origin"))`
	if r.Expression != want {
		t.Errorf("expression\n got %s\nwant %s", r.Expression, want)
	}
	if r.EdgeTTLSeconds != 300 {
		t.Errorf("edge TTL = %d, want 300", r.EdgeTTLSeconds)
	}
	if r.Host != domainName || len(r.Paths) != len(edge.DefaultPublicCachePaths) {
		t.Errorf("host and paths: %+v", r)
	}
	if !strings.Contains(r.Description, domainName) || !strings.Contains(r.Description, "Origin") {
		t.Errorf("description %q should name the host and the Origin exclusion", r.Description)
	}
}

// A browser request carries an Origin, which the service echoes in
// Access-Control-Allow-Origin: the rule must leave it uncached.
func TestPublicCacheRuleNeverCachesARequestWithAnOrigin(t *testing.T) {
	r, err := edge.PublicCacheRule(domainName, edge.PublicCacheArgs{Paths: []string{"/keys"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.Expression, `and not any(lower(http.request.headers.names[*])[*] eq "origin"))`) {
		t.Errorf("the Origin exclusion is not the rule's last conjunct: %s", r.Expression)
	}
}

func TestPublicCacheRuleOptions(t *testing.T) {
	r, err := edge.PublicCacheRule(domainName, edge.PublicCacheArgs{TTL: 30 * time.Second, Paths: []string{"/keys"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.EdgeTTLSeconds != 30 || !slices.Equal(r.Paths, []string{"/keys"}) || !strings.Contains(r.Expression, `{"/keys"}`) {
		t.Errorf("rule: %+v", r)
	}

	// The caller's slice is not reordered.
	in := []string{"/keys", "/.well-known/openid-configuration"}
	if _, err := edge.PublicCacheRule(domainName, edge.PublicCacheArgs{Paths: in}); err != nil {
		t.Fatal(err)
	}
	if in[0] != "/keys" {
		t.Errorf("PublicCacheRule sorted the caller's paths: %v", in)
	}
}

func TestPublicCacheRuleRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		host string
		args edge.PublicCacheArgs
		want string
	}{
		"quote in host":      {`a".example.test`, edge.PublicCacheArgs{}, "not a domain name"},
		"bare host":          {"localhost", edge.PublicCacheArgs{}, "not a domain name"},
		"TTL too long":       {domainName, edge.PublicCacheArgs{TTL: 2 * time.Hour}, "between 1s and"},
		"TTL under a second": {domainName, edge.PublicCacheArgs{TTL: time.Millisecond}, "between 1s and"},
		"TTL not whole":      {domainName, edge.PublicCacheArgs{TTL: 1500 * time.Millisecond}, "whole seconds"},
		"relative path":      {domainName, edge.PublicCacheArgs{Paths: []string{"keys"}}, "must start with /"},
		"quote in path":      {domainName, edge.PublicCacheArgs{Paths: []string{`/keys" or true`}}, "URL path characters"},
		"query in path":      {domainName, edge.PublicCacheArgs{Paths: []string{"/keys?x=1"}}, "URL path characters"},
		"duplicate path":     {domainName, edge.PublicCacheArgs{Paths: []string{"/keys", "/keys"}}, "listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := edge.PublicCacheRule(tc.host, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// With Args.PublicCache, NewEdge hands the rule to the estate's callback once
// and exposes it.
func TestEdgeHandsThePublicCacheRuleToTheEstate(t *testing.T) {
	var got []edge.CacheRule
	var exposed *edge.CacheRule
	_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		e, err := edge.NewEdge(ctx, "staging", &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore,
			TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
			PublicCache: &edge.PublicCacheArgs{CreateCacheRule: func(_ *pulumi.Context, r edge.CacheRule) error {
				got = append(got, r)
				return nil
			}},
		})
		if err != nil {
			return err
		}
		exposed = e.PublicCacheRule
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := edge.PublicCacheRule(domainName, edge.PublicCacheArgs{})
	if len(got) != 1 || got[0].Expression != want.Expression || got[0].EdgeTTLSeconds != want.EdgeTTLSeconds {
		t.Fatalf("callback got %+v, want once %+v", got, want)
	}
	if exposed == nil || exposed.Expression != want.Expression {
		t.Errorf("Edge.PublicCacheRule = %+v", exposed)
	}
}

// Without Args.PublicCache there is no rule; a PublicCache without its
// callback, or with a bad TTL, is refused before anything is created.
func TestEdgePublicCacheIsOptional(t *testing.T) {
	var exposed *edge.CacheRule
	_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		e, err := edge.NewEdge(ctx, "staging", &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore,
			TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
		})
		if err != nil {
			return err
		}
		exposed = e.PublicCacheRule
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if exposed != nil {
		t.Errorf("a rule without PublicCache: %+v", exposed)
	}

	for name, tc := range map[string]struct {
		cache *edge.PublicCacheArgs
		want  string
	}{
		"no callback": {&edge.PublicCacheArgs{}, "CreateCacheRule is required"},
		"bad TTL": {&edge.PublicCacheArgs{TTL: 2 * time.Hour, CreateCacheRule: func(*pulumi.Context, edge.CacheRule) error { return nil }},
			"between 1s and"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
				_, err := edge.NewEdge(ctx, "staging", &edge.Args{
					FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore,
					TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
					PublicCache:      tc.cache,
				})
				return err
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
			if n := rec.count("aws:apigatewayv2/domainName:DomainName"); n != 0 {
				t.Errorf("%d domains created before the refusal", n)
			}
		})
	}
}
