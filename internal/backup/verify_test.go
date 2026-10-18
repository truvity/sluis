package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/backup"
)

func TestFingerprintsNameTheSameRecipientsInAnyOrder(t *testing.T) {
	a, b := backup.Fingerprints([]string{"age1b", " age1a ", "age1b"}), backup.Fingerprints([]string{"age1a", "age1b"})
	if len(a) != 2 || a[0] != b[0] || a[1] != b[1] {
		t.Errorf("%v %v", a, b)
	}
	if fp := backup.Fingerprint("age1a"); len(fp) != 64 || strings.Contains(fp, "age1") {
		t.Errorf("fingerprint %q", fp)
	}
}

func TestEveryRefusalHasAWordAndOnlyTheseDo(t *testing.T) {
	for err, want := range map[error]string{
		backup.ErrNotAttested: backup.ReasonUnattested, backup.ErrMismatch: backup.ReasonMismatch,
		backup.ErrNotSealed: backup.ReasonUnsealed, backup.ErrAttestUnavailable: backup.ReasonUnavailable,
		backup.ErrRecipients: "", backup.ErrIntegrity: "",
	} {
		if got := backup.ReasonOf(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
		if is := errors.Is(err, backup.ErrUnverified); is != (want != "") {
			t.Errorf("%v: unverified %v", err, is)
		}
	}
}

func TestAnAttestorThatFailsInItsOwnWordsIsUnavailable(t *testing.T) {
	v := backup.Verifier{Attestor: backup.AttestorFunc(func(context.Context, string, string) ([]backup.Claim, error) {
		return nil, errors.New("boom")
	})}
	_, err := v.Check(context.Background(), &backup.Fetched{Sidecar: &backup.Sidecar{Instance: "i", ID: "b"}})
	if !errors.Is(err, backup.ErrAttestUnavailable) {
		t.Fatal(err)
	}
}
