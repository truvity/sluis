package console

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubapp"
)

func TestASealedLinkStateOpensOnlyWithTheSecretWithinTheWindow(t *testing.T) {
	now := time.Now()
	st := linkState{
		Expires: now.Add(linkContinueWindow), Tokens: githubapp.UserTokens{AccessToken: "tok"}, UserID: 7, Login: "ada-gh",
		Verified: []string{"ada@globex.example"},
	}
	sealed, err := sealLinkState("secret", st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openLinkState("secret", sealed, now)
	if err != nil || got.UserID != 7 || got.Tokens.AccessToken != "tok" || len(got.Verified) != 1 {
		t.Fatalf("open = %+v, %v", got, err)
	}
	if _, err = openLinkState("other", sealed, now); err == nil {
		t.Error("a state opened under another secret")
	}
	if _, err = openLinkState("secret", sealed, now.Add(linkContinueWindow+time.Second)); err == nil {
		t.Error("an expired state opened")
	}
	if _, err = openLinkState("secret", sealed[:len(sealed)-2]+"AA", now); err == nil {
		t.Error("a tampered state opened")
	}
}
