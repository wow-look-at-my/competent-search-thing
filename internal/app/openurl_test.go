package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenExternalURLOpensWithoutHiding(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	var opened []string
	a.plat.open = func(path string, _ []string) error {
		opened = append(opened, path)
		return nil
	}
	require.NoError(t, a.OpenExternalURL("https://kagi.com/api/keys"))
	require.Equal(t, []string{"https://kagi.com/api/keys"}, opened)
	// No Hide() on this path by construction -- the editor stays up
	// (Open() is the hide-on-success activation path; this is not it).
}

func TestOpenExternalURLRejectsNonHTTP(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	var opened []string
	a.plat.open = func(path string, _ []string) error {
		opened = append(opened, path)
		return nil
	}
	for _, raw := range []string{
		"",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.com/x",
		"https://", // no host
		"/etc/passwd",
	} {
		require.Error(t, a.OpenExternalURL(raw), "raw=%q", raw)
	}
	require.Empty(t, opened, "rejected URLs never reach the open seam")
}
