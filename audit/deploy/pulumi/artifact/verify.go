package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The identity a release's checksums.txt is signed with, the one
// hack/release-verify.sh pins for `cosign verify-blob`: the release workflow of
// the repository at a v* tag, issued by GitHub's OIDC issuer. Nothing else
// that holds a Fulcio certificate (another repository, another workflow, a
// branch) can sign for a release.
const (
	// OIDCIssuer is the issuer of the signing certificate.
	OIDCIssuer = "https://token.actions.githubusercontent.com"
	// DefaultSignerRepository is the repository whose release workflow signs.
	DefaultSignerRepository = "truvity/sluis"
	// ChecksumsBundle is the asset that holds the signature of checksums.txt.
	ChecksumsBundle = "checksums.txt.sigstore.json"
)

var repositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// SignerIdentity is the certificate identity (the subject alternative name) a
// release of version is signed with: the release workflow of repo at exactly the
// tag of that version. The tag is pinned and not a v* pattern, so that the
// signed checksums.txt of one release cannot stand for another.
func SignerIdentity(repo, version string) (string, error) {
	if repo == "" {
		repo = DefaultSignerRepository
	}
	if !repositoryRE.MatchString(repo) {
		return "", fmt.Errorf("the signer repository %q is not owner/name", repo)
	}
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" || v == "(devel)" || strings.ContainsAny(v, " \t\n/@") {
		return "", fmt.Errorf("the release %q is not a version: the signature is checked against the tag of the release", version)
	}
	return "https://github.com/" + repo + "/.github/workflows/release.yaml@refs/tags/v" + v, nil
}

// TrustedRoot is where the verification gets Sigstore's trusted material (the
// Fulcio and Rekor roots and the certificate transparency logs). The default
// reads the public-good instance through TUF, from the root embedded in
// sigstore-go, and caches it under the user's cache directory; a test replaces
// it.
var TrustedRoot = func() (root.TrustedMaterial, error) {
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("fetching Sigstore's trusted root: %w", err)
	}
	return tr, nil
}

// VerifyChecksums holds checksums.txt to the Sigstore bundle published beside
// it (checksums.txt.sigstore.json): the signature must verify over exactly
// these bytes, with a certificate that Fulcio issued to the release workflow of
// repo at the tag of version, by GitHub's OIDC issuer, and the signature must
// be in Rekor's transparency log with a signed certificate timestamp.
//
// trustedRootJSON is a Sigstore trusted root (the JSON `cosign trusted-root create`
// or the public-good instance's `trusted_root.json` is), for an estate that is
// mirrored or air-gapped and cannot reach the TUF repository; empty fetches the
// public-good root through TUF.
func VerifyChecksums(checksums, bundleJSON []byte, repo, version, trustedRootJSON string) error {
	b, err := parseBundle(bundleJSON)
	if err != nil {
		return err
	}
	tm, err := trustedMaterial(trustedRootJSON)
	if err != nil {
		return err
	}
	return verifySigned(tm, b, checksums, repo, version, strictOptions...)
}

// trustedMaterial is the supplied trusted root, or the public-good one.
func trustedMaterial(trustedRootJSON string) (root.TrustedMaterial, error) {
	if strings.TrimSpace(trustedRootJSON) == "" {
		return TrustedRoot()
	}
	tm, err := root.NewTrustedRootFromJSON([]byte(trustedRootJSON))
	if err != nil {
		return nil, fmt.Errorf("Release.TrustedRootJSON is not a Sigstore trusted root: %w", err)
	}
	return tm, nil
}

func parseBundle(bundleJSON []byte) (*bundle.Bundle, error) {
	if len(bundleJSON) == 0 {
		return nil, errors.New("the signature bundle is empty")
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("the signature bundle is not a Sigstore bundle: %w", err)
	}
	return &b, nil
}

// strictOptions are what a release signature must have: the certificate carries a
// signed certificate timestamp of a log in the trusted root, the signature is in
// the transparency log, and the log's time is the observed time of the signing.
var strictOptions = []verify.VerifierOption{
	verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1),
}

// verifySigned is the check on an entity already read.
func verifySigned(tm root.TrustedMaterial, entity verify.SignedEntity, checksums []byte, repo, version string,
	opts ...verify.VerifierOption) error {
	san, err := SignerIdentity(repo, version)
	if err != nil {
		return err
	}
	id, err := verify.NewShortCertificateIdentity(OIDCIssuer, "", san, "")
	if err != nil {
		return err
	}
	v, err := verify.NewVerifier(tm, opts...)
	if err != nil {
		return err
	}
	if _, err := v.Verify(entity, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(id))); err != nil {
		return fmt.Errorf("checksums.txt does not verify against its Sigstore bundle (identity %s, issuer %s): %w", san, OIDCIssuer, err)
	}
	return nil
}
