// Package version holds the build version, set with
// -ldflags "-X github.com/NordTempo/platform/internal/version.Version=v1.2.3".
package version

// Version of this binary. The CLI and daemon ship together, so this is also
// the API compatibility marker.
var Version = "dev"
