// Package version carries the build's identity, so that an operator
// looking at a console can say which build they are looking at without
// asking a cluster.
package version

// Version is the release this binary was built from. The release
// workflow stamps it from the git tag; a development build says so.
var Version = "dev"

// Module is the module a Lambda zip is built for, set by the release with
// `-ldflags -X` and empty in a development build. A zip whose main is another
// module's refuses to start (internal/lambdaapp.StartModule), so the pin makes
// a zip that cannot be run as a module it was not built for.
var Module = ""

// String returns the version.
func String() string { return Version }
