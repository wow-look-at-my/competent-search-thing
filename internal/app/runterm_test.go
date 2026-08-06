package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalRunnerWiring(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	assert.Nil(t, a.terminalRunner(), "no PATH lookup seam means no capability")

	a.plat.lookPath = func(name string) (string, error) {
		if name == "xterm" {
			return "/usr/bin/xterm", nil
		}
		return "", errors.New("not found")
	}
	term := a.terminalRunner()
	require.NotNil(t, term)
	assert.Equal(t, "xterm", term.Name)
	require.NotNil(t, term.LookPath)
	require.NotNil(t, term.Command)
	assert.Equal(t, []string{"/usr/bin/xterm", "-e", "/usr/bin/htop"}, term.Command([]string{"/usr/bin/htop"}))
}

func TestTerminalRunnerNilWithoutATerminal(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	assert.Nil(t, a.terminalRunner())
}
