//go:build !linux

package watchsetup

import "errors"

// prodReExec is never reached off Linux (Ensure only re-execs after a
// Linux-only capability grant); the stub keeps the package building on
// every target without pulling in syscall.Exec, which Windows lacks.
func prodReExec(exe string, argv, env []string) error {
	return errors.New("re-exec is only supported on Linux")
}
