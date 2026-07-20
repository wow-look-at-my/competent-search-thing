//go:build !darwin

package sysstats

// newDarwinReaders on a non-darwin build binds nothing: the only way
// here is a caller asking for GOOS "darwin" without injecting the
// Options seam (a fixture test, or misconfiguration), and New
// degrades that to the placeholders row via darwinReaders.ok.
func newDarwinReaders() *darwinReaders { return &darwinReaders{} }
