// Package version holds the Wok release version, set at link time.
package version

// Version is overridden by release builds with
// -ldflags "-X github.com/alg0rix/wok/internal/version.Version=v1.2.3".
var Version = "dev"
