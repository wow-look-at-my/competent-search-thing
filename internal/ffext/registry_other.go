//go:build !windows

package ffext

// registerHostManifest is windows-only registry glue; on every other
// OS Firefox finds the manifest by its filesystem location and there
// is nothing to register.
func registerHostManifest(string) error { return nil }
