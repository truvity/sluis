package issuer_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// limitedPolicy is the demonstration policy with the gitops CI job limited to
// its deployer role. Without the limit the same job also reaches k8s:devel,
// whose `requires` names the deployer group: the case a limit exists for, an
// identity a shared group admits to more than it should ever ask for.
const limitedPolicy = demo.Policy + `
exchange_limits:
  - match: { github: { repository: example-org/gitops } }
    clients: [aws:1111:deployer]
`

func limitedIssuer(t *testing.T, doc string) (*issuer.Issuer, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)
	return iss, trail
}

func TestAnExchangeLimitNarrowsTheAudiences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gitops := issuer.Proof{GitHub: &policy.GitHubClaims{
		Repository: "example-org/gitops", Owner: "example-org", Ref: "refs/heads/master",
	}}

	// The premise: unlimited, the job's groups admit it to both.
	open, _ := limitedIssuer(t, demo.Policy)
	for _, audience := range []string{"aws:1111:deployer", "k8s:devel"} {
		if _, err := open.Exchange(ctx, gitops, audience); err != nil {
			t.Fatalf("without a limit, gitops → %s: %v", audience, err)
		}
	}

	iss, trail := limitedIssuer(t, limitedPolicy)

	t.Run("an audience the limit names passes", func(t *testing.T) {
		grant, err := iss.Exchange(ctx, gitops, "aws:1111:deployer")
		if err != nil {
			t.Fatalf("gitops → deployer: %v", err)
		}
		if grant.Audience != "aws:1111:deployer" {
			t.Errorf("audience = %q", grant.Audience)
		}
	})

	t.Run("any other audience is refused as exchange_limited", func(t *testing.T) {
		before := len(trail.Records())
		_, err := iss.Exchange(ctx, gitops, "k8s:devel")
		if !errors.Is(err, issuer.ErrRefused) {
			t.Fatalf("gitops → k8s:devel: %v, want ErrRefused", err)
		}
		if !strings.Contains(err.Error(), "aws:1111:deployer") {
			t.Errorf("err = %v, want it to name the clients the proof is limited to", err)
		}
		records := trail.Records()[before:]
		if len(records) != 1 {
			t.Fatalf("%d records, want one", len(records))
		}
		data := records[0].GetData().AsMap()
		if data["reason_code"] != "exchange_limited" || data["audience"] != "k8s:devel" {
			t.Errorf("data = %v, want reason_code exchange_limited for k8s:devel", data)
		}
	})

	t.Run("a proof no limit matches is unaffected", func(t *testing.T) {
		webhook := issuer.Proof{ServiceAccount: &policy.ServiceAccountRef{
			Namespace: "identity-system", Name: "authorization-webhook",
		}}
		if _, err := iss.Exchange(ctx, webhook, "directory-roster"); err != nil {
			t.Errorf("workload → directory-roster: %v", err)
		}
		// Another repository of the same owner is not the limited identity.
		other := issuer.Proof{GitHub: &policy.GitHubClaims{
			Repository: "example-org/website", Owner: "example-org", Ref: "refs/heads/master",
		}}
		if _, err := iss.Exchange(ctx, other, "aws:1111:deployer"); !errors.Is(err, issuer.ErrRefused) {
			t.Errorf("website → deployer: %v, want the client's own refusal", err)
		}
	})
}

// A limit takes an audience away and never gives one: a client the limit
// names but whose `requires` the proof does not meet is still refused, under
// the client's own code.
func TestAnExchangeLimitGrantsNothing(t *testing.T) {
	t.Parallel()
	iss, trail := limitedIssuer(t, demo.Policy+`
exchange_limits:
  - match: { github: { repository: example-org/gitops } }
    clients: [aws:1111:deployer, aws:1111:power]
`)
	gitops := issuer.Proof{GitHub: &policy.GitHubClaims{
		Repository: "example-org/gitops", Owner: "example-org", Ref: "refs/heads/master",
	}}
	if _, err := iss.Exchange(context.Background(), gitops, "aws:1111:power"); !errors.Is(err, issuer.ErrRefused) {
		t.Fatalf("gitops → power: %v, want a refusal", err)
	}
	records := trail.Records()
	if got := records[len(records)-1].GetData().AsMap()["reason_code"]; got != "not_admitted" {
		t.Errorf("reason_code = %v, want not_admitted", got)
	}
}

// Over HTTP a limited audience is the same invalid_target, and the same body,
// as an audience nobody declared: which audiences exist is not the caller's to
// learn by being refused.
func TestALimitedAudienceIsRefusedLikeAnUnknownOne(t *testing.T) {
	iss, _ := limitedIssuer(t, limitedPolicy)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	const subject = "github:example-org/gitops@refs/heads/master"
	unknown := postForm(t, server, "/token", exchangeForm(subject, "aws:9999:nobody"), "local-dev", "", nil)
	limited := postForm(t, server, "/token", exchangeForm(subject, "k8s:devel"), "local-dev", "", nil)
	if unknown.body["error"] != "invalid_target" || limited.body["error"] != "invalid_target" {
		t.Fatalf("codes %v and %v, want both invalid_target", unknown.body["error"], limited.body["error"])
	}
	if unknown.status != limited.status {
		t.Errorf("status %d vs %d", unknown.status, limited.status)
	}
	a := withoutID(unknown.raw, unknown.header.Get("X-Request-Id"))
	b := withoutID(limited.raw, limited.header.Get("X-Request-Id"))
	if a != b {
		t.Errorf("the bodies differ:\n%s\n%s", a, b)
	}
	allowed := postForm(t, server, "/token", exchangeForm(subject, "aws:1111:deployer"), "local-dev", "", nil)
	if allowed.status != 200 {
		t.Errorf("gitops → deployer over HTTP = %d %s", allowed.status, allowed.raw)
	}
}
