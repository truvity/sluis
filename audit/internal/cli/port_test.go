package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/state/memory"
)

// The store's credentials are a JSON object at an address of the installation's
// state store. A pair that is incomplete, or carries anything else, is refused
// by field name and never echoed.
func TestTheArchiveCredentialsAreReadFromTheStateStore(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		value string
		id    string
		fail  string
	}{
		"a pair":          {value: `{"accessKeyID":"id-1","secretAccessKey":"s3cret-1"}`, id: "id-1"},
		"no secret":       {value: `{"accessKeyID":"id-1"}`, fail: "must hold both"},
		"an extra field":  {value: `{"accessKeyID":"a","secretAccessKey":"s3cret-2","token":"t"}`, fail: "token"},
		"an empty object": {value: `{}`, fail: "must hold both"},
	} {
		t.Run(name, func(t *testing.T) {
			st := memory.New()
			if _, err := st.Put(ctx, "internal/archive", []byte(tc.value), ""); err != nil {
				t.Fatal(err)
			}
			id, secret, err := readCredentials(ctx, st, "internal/archive")
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("err = %v, want it to say %q", err, tc.fail)
				}
				if strings.Contains(err.Error(), "s3cret") {
					t.Fatalf("the error echoes a secret: %v", err)
				}
				return
			}
			if err != nil || id != tc.id || secret == "" {
				t.Fatalf("got %q, %q, %v", id, secret, err)
			}
		})
	}
	if _, _, err := readCredentials(ctx, memory.New(), "internal/archive"); err == nil || !strings.Contains(err.Error(), "internal/archive") {
		t.Fatalf("a missing credential: %v", err)
	}
}
