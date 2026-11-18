// Package subject holds what the units must agree on about a `sub`: its forms, and the SPIFFE ID of a workload.
package subject

import (
	"errors"
	"strings"
)

// Kind says which of the forms of ADR 0020 ("The sub of a token") and ADR 0022 ("SPIFFE identities") a `sub` has.
// A `sub` has one of two forms and no other: the person's own subject, or a SPIFFE ID. A pairwise subject is a form
// that v2 once had and removed before any relying party moved (decision SB1 A, 2026-11-18): it has no form now.
type Kind string

const (
	// KindPerson is the person's own subject (ADR 0019, "Assigning a subject"): 128 random bits in lower-case base32
	// without padding, 26 characters. It is the `sub` of a person at every tenant.
	KindPerson Kind = "person"
	// KindSPIFFE is the SPIFFE ID of a workload (ADR 0022): the `sub` of a JWT-SVID.
	KindSPIFFE Kind = "spiffe"
)

const (
	personLen = 26

	// The limits of the SPIFFE ID specification (SPIFFE-ID.md): a trust domain of 255 characters, an ID of 2048 bytes.
	maxTrustDomain = 255
	maxSPIFFEID    = 2048
	spiffeScheme   = "spiffe://"
)

// Classify says which form a `sub` has, and false for a string that has none: an e-mail address, a name of the
// upstream, a pairwise subject (removed, decision SB1 A), a v1 spelling (`github:org/repo`, `cluster:k8s:ns:name`,
// `aws:...`) or anything else. It reads the form
// only; it does not know whether a person or a workload has that subject. A subject of a person is checked as the bits
// it was made from, so a 26-character string whose last character carries bits that 128 random bits never set is not
// a subject.
func Classify(sub string) (Kind, bool) {
	switch {
	case strings.HasPrefix(sub, spiffeScheme):
		if validSPIFFEID(sub) {
			return KindSPIFFE, true
		}
	case len(sub) == personLen:
		if validPersonID(sub) {
			return KindPerson, true
		}
	}
	return "", false
}

// validPersonID reads 26 characters of the alphabet a-z and 2-7, whose last character has its two spare bits clear:
// 26 characters hold 130 bits, and the id is 128.
func validPersonID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	switch s[len(s)-1] {
	case 'a', 'e', 'i', 'm', 'q', 'u', 'y', '4':
		return true
	}
	return false
}

// A reason that a SPIFFE ID cannot be made. The three are the three limits of the specification a part can break.
var (
	// ErrTrustDomain: the trust domain is empty, longer than 255 characters or has a character outside a-z, 0-9, `.`,
	// `-` and `_`.
	ErrTrustDomain = errors.New("subject: not a SPIFFE trust domain")
	// ErrSegment: a part of the path is empty, is `.` or `..`, has a character outside A-Z, a-z, 0-9, `.`, `-` and `_`,
	// or a repository that is not two such parts.
	ErrSegment = errors.New("subject: not a SPIFFE path segment")
	// ErrLength: the ID is longer than 2048 bytes.
	ErrLength = errors.New("subject: SPIFFE ID too long")
)

// CIJobID is the SPIFFE ID of the CI job of a repository, `spiffe://<trust-domain>/ci/github/<org>/<repo>` (ADR 0022,
// "SPIFFE identities"), made from the `repository` claim of a verified GitHub Actions token, which is `<org>/<repo>`.
// The ID names the repository and nothing about the run: not the ref, the event or the environment, which are facts
// for policy and never part of who the subject is. Case is kept as GitHub writes it in the token.
func CIJobID(trustDomain, repository string) (string, error) {
	org, repo, ok := strings.Cut(repository, "/")
	if !ok {
		return "", ErrSegment
	}
	return spiffeID(trustDomain, "ci", "github", org, repo)
}

// ServiceAccountID is the SPIFFE ID of a Kubernetes ServiceAccount,
// `spiffe://<trust-domain>/k8s/<cluster>/ns/<namespace>/sa/<name>` (ADR 0022, "SPIFFE identities"). The cluster is
// always part of it: the same namespace and name on two clusters are two machines.
func ServiceAccountID(trustDomain, cluster, namespace, name string) (string, error) {
	return spiffeID(trustDomain, "k8s", cluster, "ns", namespace, "sa", name)
}

// spiffeID joins a trust domain and the parts of a path, and checks every limit of SPIFFE-ID.md.
func spiffeID(trustDomain string, parts ...string) (string, error) {
	if !validTrustDomain(trustDomain) {
		return "", ErrTrustDomain
	}
	for _, p := range parts {
		if !validSegment(p) {
			return "", ErrSegment
		}
	}
	id := spiffeScheme + trustDomain + "/" + strings.Join(parts, "/")
	if len(id) > maxSPIFFEID {
		return "", ErrLength
	}
	return id, nil
}

func validTrustDomain(s string) bool {
	if s == "" || len(s) > maxTrustDomain {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// validSPIFFEID reads a whole ID: the scheme, a trust domain, and a path of at least one valid segment.
func validSPIFFEID(id string) bool {
	if len(id) > maxSPIFFEID {
		return false
	}
	rest := strings.TrimPrefix(id, spiffeScheme)
	domain, path, ok := strings.Cut(rest, "/")
	if !ok || !validTrustDomain(domain) {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}
