package kube_test

import (
	"context"
	"errors"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/truvity/sluis/internal/kube"
)

// reviewing is a client whose API server answers every TokenReview with status.
func reviewing(status authnv1.TokenReviewStatus) *kube.Client {
	api := fake.NewClientset()
	api.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		review.Status = status
		return true, review, nil
	})
	return kube.NewClient(api, namespace, "directory-roster")
}

// A mounted ServiceAccount token authenticates against the API server's own
// audience. Asked for another, a server that is audience aware refuses it or
// answers with no audience in common; one that is not answers authenticated
// and reports none. Either way the token was not minted for this purpose,
// and only an audience common to the review and the token admits it.
func TestReviewTokenAsksTheAudienceItWasGiven(t *testing.T) {
	t.Parallel()

	user := authnv1.UserInfo{Username: "system:serviceaccount:sluis:recovery"}
	for name, tc := range map[string]struct {
		status authnv1.TokenReviewStatus
		admit  bool
	}{
		"the audience is common":       {authnv1.TokenReviewStatus{Authenticated: true, User: user, Audiences: []string{"sluis-recovery"}}, true},
		"one of several is common":     {authnv1.TokenReviewStatus{Authenticated: true, User: user, Audiences: []string{"other", "sluis-recovery"}}, true},
		"authenticated, none reported": {authnv1.TokenReviewStatus{Authenticated: true, User: user}, false},
		"another audience reported":    {authnv1.TokenReviewStatus{Authenticated: true, User: user, Audiences: []string{"https://kubernetes.default.svc"}}, false},
		"not authenticated":            {authnv1.TokenReviewStatus{User: user, Audiences: []string{"sluis-recovery"}}, false},
		"no username":                  {authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{"sluis-recovery"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := reviewing(tc.status).ReviewToken(context.Background(), "token", []string{"sluis-recovery"})
			switch {
			case tc.admit && (err != nil || got != user.Username):
				t.Errorf("= %q, %v; want %q", got, err, user.Username)
			case !tc.admit && !errors.Is(err, kube.ErrTokenRejected):
				t.Errorf("= %q, %v; want ErrTokenRejected", got, err)
			}
		})
	}
}
