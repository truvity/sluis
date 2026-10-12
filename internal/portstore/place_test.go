package portstore_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/portstore"
)

func TestAnAppsPlaceIsTheAddressItsKeyIsWrittenUnder(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, stores := v5base(t, e)
		if _, _, ok := b.GitHubAppPlace("app-1"); ok {
			t.Error("a place was named before the adapter was")
		}
		b.WithPlace("ssm", "/sluis/prod/")
		adapter, location, ok := b.GitHubAppPlace("app-1")
		if !ok || adapter != "ssm" {
			t.Fatalf("GitHubAppPlace = %q, %q, %v", adapter, location, ok)
		}
		if _, err := stores.GitHub().AppCredential("app-1", "r1").Put(ctx, []byte("k"), ""); err != nil {
			t.Fatal(err)
		}
		written := l.written()
		if len(written) != 1 || "/sluis/prod/"+written[0] != location+"r1" {
			t.Errorf("the key is at %v, the place says %q", written, location)
		}
		if _, location, _ = b.GitHubAppPlace(""); location != "/sluis/prod/" {
			t.Errorf("an App with no id of its own is at the root, got %q", location)
		}
	})
}

func TestLayoutV4NamesTheRootOnly(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b := portstore.New(e.open(t)).WithPlace("openbao", "secret/sluis")
		adapter, location, ok := b.GitHubAppPlace("app-1")
		if !ok || adapter != "openbao" || location != "secret/sluis/" || strings.Contains(location, "app-1") {
			t.Errorf("GitHubAppPlace = %q, %q, %v", adapter, location, ok)
		}
	})
}
