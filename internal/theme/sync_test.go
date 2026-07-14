package theme

import (
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStyleCSSRootBlockMatchesDarkBuiltin is the sync guard between
// the dark builtin and the frontend: frontend/src/style.css declares
// every token as --sb-<name> in its :root block (the pre-runtime
// fallback values), and those declarations must stay IDENTICAL to
// builtin/dark.json, name-for-name and value-for-value. If this test
// fails, someone edited one side without the other.
func TestStyleCSSRootBlockMatchesDarkBuiltin(t *testing.T) {
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	cssPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "frontend", "src", "style.css")
	data, err := os.ReadFile(cssPath)
	require.NoError(t, err, "style.css must sit at frontend/src relative to this package")

	rootRe := regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
	m := rootRe.FindStringSubmatch(string(data))
	require.NotNil(t, m, "style.css must declare a :root block")

	declRe := regexp.MustCompile(`--sb-([a-z0-9-]+)\s*:\s*([^;]+);`)
	got := map[string]string{}
	for _, d := range declRe.FindAllStringSubmatch(m[1], -1) {
		got[d[1]] = strings.TrimSpace(d[2])
	}

	want, err := Resolve(DefaultName, "")
	require.NoError(t, err)
	require.Equal(t, want, got,
		"style.css :root --sb-* declarations must equal builtin/dark.json token-for-token")
}
