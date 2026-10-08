package sluispulumi

import "runtime/debug"

// SetBuildInfo makes the library read its own release from bi (nil: no build
// information), and returns what puts it back.
func SetBuildInfo(bi *debug.BuildInfo) func() {
	was := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, bi != nil }
	return func() { readBuildInfo = was }
}
