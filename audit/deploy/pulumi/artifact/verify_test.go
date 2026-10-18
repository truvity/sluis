package artifact

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The virtual Sigstore issues no signed certificate timestamp, so the tests
// check everything else the strict options do; TestStrictOptionsRequireTheCertificateTimestamp
// keeps the SCT in the options a release is checked with.
var testOptions = []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}

const checksums = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  sluis-issuer_1.77.0_linux_arm64.zip\n"

func TestSignerIdentityPinsTheTagOfTheRelease(t *testing.T) {
	got, err := SignerIdentity("", "v1.77.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v1.77.0-rc.1"
	if got != want {
		t.Errorf("identity = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "(devel)", "1.0/0", "1.0@x", "1 0"} {
		if _, err := SignerIdentity("", bad); err == nil {
			t.Errorf("the version %q gave an identity", bad)
		}
	}
	if _, err := SignerIdentity("not a repo", "1.0.0"); err == nil {
		t.Error("a repository that is not owner/name gave an identity")
	}
}

func TestChecksumsVerifyAgainstTheReleaseWorkflowOnly(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	ident := func(repo, ref string) string {
		return "https://github.com/" + repo + "/.github/workflows/release.yaml@refs/" + ref
	}
	for name, c := range map[string]struct {
		identity, issuer, body, version string
		want                            string // "" verifies; else a part of the error
	}{
		"the release workflow at the tag":  {ident("truvity/sluis", "tags/v1.77.0"), OIDCIssuer, checksums, "1.77.0", ""},
		"the version with a v":             {ident("truvity/sluis", "tags/v1.77.0"), OIDCIssuer, checksums, "v1.77.0", ""},
		"another repository":               {ident("evil/sluis", "tags/v1.77.0"), OIDCIssuer, checksums, "1.77.0", "does not verify"},
		"another tag (a replayed release)": {ident("truvity/sluis", "tags/v1.76.0"), OIDCIssuer, checksums, "1.77.0", "does not verify"},
		"a branch":                         {ident("truvity/sluis", "heads/master"), OIDCIssuer, checksums, "1.77.0", "does not verify"},
		"another workflow": {
			"https://github.com/truvity/sluis/.github/workflows/ci.yaml@refs/tags/v1.77.0", OIDCIssuer, checksums, "1.77.0", "does not verify"},
		"another issuer":        {ident("truvity/sluis", "tags/v1.77.0"), "https://accounts.example.test", checksums, "1.77.0", "does not verify"},
		"changed checksums.txt": {ident("truvity/sluis", "tags/v1.77.0"), OIDCIssuer, checksums + "ff  extra\n", "1.77.0", "does not verify"},
		"no version to pin":     {ident("truvity/sluis", "tags/v1.77.0"), OIDCIssuer, checksums, "", "not a version"},
	} {
		t.Run(name, func(t *testing.T) {
			signed := checksums
			if c.body != checksums {
				signed = checksums // signed bytes are always the original; the body is what is checked
			}
			entity, err := vs.Sign(c.identity, c.issuer, []byte(signed))
			if err != nil {
				t.Fatal(err)
			}
			err = verifySigned(vs, entity, []byte(c.body), "", c.version, testOptions...)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("verification failed: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestAMalformedBundleIsRefused(t *testing.T) {
	for name, b := range map[string]string{"empty": "", "not json": "x", "not a bundle": `{"a":1}`} {
		if _, err := parseBundle([]byte(b)); err == nil {
			t.Errorf("%s: a bundle that is none verified", name)
		}
	}
}

func TestStrictOptionsRequireTheCertificateTimestamp(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	entity, err := vs.Sign("https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v1.77.0", OIDCIssuer, []byte(checksums))
	if err != nil {
		t.Fatal(err)
	}
	// The same entity passes without the SCT and is refused with it: the release is checked with it.
	if err := verifySigned(vs, entity, []byte(checksums), "", "1.77.0", testOptions...); err != nil {
		t.Fatalf("control: %v", err)
	}
	if err := verifySigned(vs, entity, []byte(checksums), "", "1.77.0", strictOptions...); err == nil {
		t.Error("an entity with no signed certificate timestamp verified under the options a release is checked with")
	}
}

// ResolveSHA256 reads the digest from a signed checksums.txt: it fetches the
// bundle beside it, hands both to the verifier, and refuses what the verifier
// refuses. Only UnverifiedChecksums skips it.
func TestResolveSHA256VerifiesTheChecksumsFirst(t *testing.T) {
	const asset = "sluis-issuer_1.77.0_linux_arm64.zip"
	files := map[string]string{"/v1.77.0/checksums.txt": checksums, "/v1.77.0/" + ChecksumsBundle: "{}"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	rel := &Release{BaseURL: srv.URL}
	want := strings.Fields(checksums)[0]

	var gotRepo, gotVersion string
	var gotSums, gotBundle string
	verdict := error(nil)
	defer func(old func([]byte, []byte, string, string, string) error) { verifier = old }(verifier)
	var gotRoot string
	verifier = func(sums, bundle []byte, repo, version, trustedRoot string) error {
		gotSums, gotBundle, gotRepo, gotVersion, gotRoot = string(sums), string(bundle), repo, version, trustedRoot
		return verdict
	}

	got, err := ResolveSHA256(rel, "v1.77.0", asset)
	if err != nil || got != want {
		t.Fatalf("ResolveSHA256 = %q, %v; want %q", got, err, want)
	}
	if gotSums != checksums || gotBundle != "{}" || gotVersion != "1.77.0" {
		t.Errorf("the verifier was given %q, %q, %q", gotSums, gotBundle, gotVersion)
	}

	rel.TrustedRootJSON = `{"mediaType":"root"}`
	if _, err := ResolveSHA256(rel, "1.77.0", asset); err != nil || gotRoot != rel.TrustedRootJSON {
		t.Errorf("the trusted root reached the verifier as %q (%v)", gotRoot, err)
	}
	rel.SignerRepository = "acme/fork"
	if _, err := ResolveSHA256(rel, "1.77.0", asset); err != nil || gotRepo != "acme/fork" {
		t.Errorf("the signer repository reached the verifier as %q (%v)", gotRepo, err)
	}

	verdict = errors.New("signature is not from the release workflow")
	if _, err := ResolveSHA256(rel, "1.77.0", asset); err == nil || !strings.Contains(err.Error(), "release workflow") {
		t.Errorf("a refused signature gave %v", err)
	}
	rel.UnverifiedChecksums = true
	if got, err := ResolveSHA256(rel, "1.77.0", asset); err != nil || got != want {
		t.Errorf("UnverifiedChecksums: %q, %v", got, err)
	}

	rel.UnverifiedChecksums = false
	delete(files, "/v1.77.0/"+ChecksumsBundle)
	verdict = nil
	if _, err := ResolveSHA256(rel, "1.77.0", asset); err == nil || !strings.Contains(err.Error(), ChecksumsBundle) {
		t.Errorf("a release with no bundle gave %v: the digest was read from an unsigned file", err)
	}
}

func TestCodeSigningArgsAreHeldToSignerProfiles(t *testing.T) {
	good := "arn:aws:signer:eu-west-1:111122223333:/signing-profiles/sluis_release/AbCdEfGhIj"
	for name, c := range map[string]struct {
		arns []string
		ok   bool
	}{
		"a profile version":     {[]string{good}, true},
		"none":                  {nil, false},
		"a profile, no version": {[]string{"arn:aws:signer:eu-west-1:111122223333:/signing-profiles/sluis_release"}, false},
		"a role":                {[]string{"arn:aws:iam::111122223333:role/x"}, false},
	} {
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			_, err := NewCodeSigningConfig(ctx, "csc", CodeSigningArgs{SigningProfileVersionArns: c.arns})
			return err
		}, pulumi.WithMocks("p", "s", noMocks{}))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type noMocks struct{}

func (noMocks) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return a.Name + "_id", a.Inputs, nil
}
func (noMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// A supplied trusted root replaces the TUF fetch, and one that is none is refused
// by name; with none supplied the default is read.
func TestASuppliedTrustedRootReplacesTheFetchAndMustBeOne(t *testing.T) {
	defer func(old func() (root.TrustedMaterial, error)) { TrustedRoot = old }(TrustedRoot)
	fetched := 0
	TrustedRoot = func() (root.TrustedMaterial, error) {
		fetched++
		return ca.NewVirtualSigstore()
	}
	if _, err := trustedMaterial(""); err != nil || fetched != 1 {
		t.Errorf("no root supplied: fetched %d times (%v)", fetched, err)
	}
	if _, err := trustedMaterial(`{"mediaType":"not a trusted root"}`); err == nil || !strings.Contains(err.Error(), "Release.TrustedRootJSON") {
		t.Errorf("a root that is none: %v", err)
	}
	if _, err := trustedMaterial("not json"); err == nil || fetched != 1 {
		t.Errorf("garbage: %v, fetched %d times (a supplied root is never replaced by the fetch)", err, fetched)
	}
}
