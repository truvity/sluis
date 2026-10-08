package auditpulumi

import "runtime/debug"

// SetLibraryVersion makes the guards read the library's release as v, and returns
// what puts it back. A test binary of this module has no release of its own.
func SetLibraryVersion(v string) func() {
	was := libraryVersion
	libraryVersion = func() string { return v }
	return func() { libraryVersion = was }
}

// Absent and Denied expose the guard's reading of an error to the tests.
func Absent(err error) bool { return absent(err) }
func Denied(err error) bool { return denied(err) }

// PodIdentityTrust exposes the Pod Identity trust statement's refusals.
func PodIdentityTrust(clusterArn, ns, sa string) (map[string]any, error) {
	return podIdentityTrustStatement(clusterArn, ns, sa)
}

// SetBuildInfo makes the library read its own release from bi (nil: no build
// information), and returns what puts it back.
func SetBuildInfo(bi *debug.BuildInfo) func() {
	was := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, bi != nil }
	return func() { readBuildInfo = was }
}
