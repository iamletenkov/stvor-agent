// Package version holds the build version of stvor-agent.
package version

import "strings"

// Version is set at build time:
//
//	go build -ldflags "-X github.com/iamletenkov/stvor-agent/internal/version.Version=4.0.0"
//
// Version and Commit are the only package-level variables allowed to be
// mutable, and only via ldflags.
var Version = "dev"

// Commit is the VCS commit of the build, set like Version
// (-X …/internal/version.Commit=<sha>); "" — a build without VCS: `version`
// prints "unknown".
var Commit = ""

// Short returns the bare X.Y.Z form of the raw version: surrounding spaces and
// a leading "v" are dropped, an empty value becomes "dev".
func Short(raw string) string {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if v == "" {
		v = "dev"
	}
	return v
}
