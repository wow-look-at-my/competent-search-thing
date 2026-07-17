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

func TestCommandExecutable(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
		ok      bool
	}{
		{"bare", "/usr/bin/cst toggle", "/usr/bin/cst", true},
		{"bare no args", "/usr/bin/cst", "/usr/bin/cst", true},
		{"leading whitespace", " \t/usr/bin/cst toggle", "/usr/bin/cst", true},
		{"tab separator", "/usr/bin/cst\ttoggle", "/usr/bin/cst", true},
		{"double quoted spaces", `"/opt/My Apps/cst" toggle`, "/opt/My Apps/cst", true},
		{"escaped quote", `"/odd\"name/cst" toggle`, `/odd"name/cst`, true},
		{"escaped backslash", `"C:\\odd\\cst" toggle`, `C:\odd\cst`, true},
		{"backslash kept before other chars", `"/a\b/cst" toggle`, `/a\b/cst`, true},
		{"single quoted", "'/it is/cst' toggle", "/it is/cst", true},
		{"adjacent segments", `"/opt/My Apps"/cst toggle`, "/opt/My Apps/cst", true},
		{"empty", "", "", false},
		{"only spaces", "   ", "", false},
		{"unterminated double quote", `"/opt/cst toggle`, "", false},
		{"unterminated single quote", "'/opt/cst toggle", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := commandExecutable(tc.command)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCommandExecutableUndoesToggleCommand(t *testing.T) {
	// The parser and the quoter agree: whatever exe ToggleCommand
	// writes, commandExecutable reads back verbatim.
	for _, exe := range []string{
		"/usr/bin/competent-search-thing",
		"/opt/My Apps/cst",
		`/odd"name/cst`,
		`C:\odd\cst`,
		"/it's/cst",
		"/tab\there/cst",
	} {
		got, ok := commandExecutable(ToggleCommand(exe))
		require.True(t, ok, "exe %q", exe)
		require.Equal(t, exe, got)
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
