package gsettings

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToggleCommand(t *testing.T) {
	cases := []struct {
		exe  string
		want string
	}{
		{"/usr/bin/competent-search-thing", "/usr/bin/competent-search-thing toggle"},
		{"cst", "cst toggle"},
		{"/opt/My Apps/cst", `"/opt/My Apps/cst" toggle`},
		{`/odd"name/cst`, `"/odd\"name/cst" toggle`},
		{`C:\odd\cst`, `"C:\\odd\\cst" toggle`},
		{"/it's/cst", `"/it's/cst" toggle`},
		{"/tab\there/cst", "\"/tab\there/cst\" toggle"},
	}
	for _, tc := range cases {
		t.Run(tc.exe, func(t *testing.T) {
			require.Equal(t, tc.want, ToggleCommand(tc.exe))
		})
	}
}

// TestRunSmoke exercises the real gsettings binary when it is
// installed (it is in CI's ubuntu-24.04 image); the schema-independent
// paths are enough -- EnsureBinding logic is fully tested against the
// scripted Runner.
func TestRunSmoke(t *testing.T) {
	if _, err := exec.LookPath("gsettings"); err != nil {
		t.Skip("gsettings not installed")
	}

	out, err := Run(context.Background(), "--version")
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// Errors fold stderr into the message.
	_, err = Run(context.Background(), "get", "no.such.schema.anywhere", "key")
	require.Error(t, err)
	require.Contains(t, err.Error(), "No such schema")
}
