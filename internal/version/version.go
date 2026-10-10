// Package version carries the build's identity, so that an operator
// looking at a console can say which build they are looking at without
// asking a cluster.
package version

import "strings"

// Version is the release this binary was built from. The release
// workflow stamps it from the git tag; a development build says so.
var Version = "dev"

// ModulePrefix starts the value of [Module]. The marker makes the pin findable
// in the shipped binary (`grep -a sluis-module=issuer bootstrap`), which a bare
// module name in a string table is not: release-verify reads it from each zip.
const ModulePrefix = "sluis-module="

// Module is the module a Lambda zip is built for, as the release sets it with
// `-ldflags -X`: ModulePrefix and the module name (`sluis-module=issuer`). It is
// empty in a development build. A zip whose main is another module's refuses to
// start (internal/lambdaapp.StartModule), so a zip cannot be run as a module it
// was not built for.
var Module = ""

// Pinned is the module name [Module] pins: empty for an unpinned build.
func Pinned() string { return strings.TrimPrefix(Module, ModulePrefix) }

// String returns the version.
func String() string { return Version }
