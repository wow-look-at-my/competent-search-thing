package service

// The auto-registration opt-out marker: a tiny file (NOT a config.json
// field -- registration is machinery, not configuration) that `service
// uninstall` writes and Ensure respects, so the app never re-registers
// against the user's explicit wish; `service install` clears it.

import (
	"errors"
	"os"
	"path/filepath"
)

// optOutName is the marker file name inside the config directory.
const optOutName = "service.optout"

// OptOutPath returns the login-service auto-registration opt-out
// marker location for the given config directory.
func OptOutPath(configDir string) string {
	return filepath.Join(configDir, optOutName)
}

// WriteOptOut records the user's opt-out (called by `service
// uninstall` after removing the service file).
func (m *Manager) WriteOptOut() error {
	if m.OptOutFile == "" {
		return errors.New("no config directory resolved; cannot record the auto-registration opt-out")
	}
	if err := os.MkdirAll(filepath.Dir(m.OptOutFile), 0o755); err != nil {
		return err
	}
	content := "login-service auto-registration is disabled.\n" +
		"Written by '" + appName + " service uninstall'; delete this file or run\n" +
		"'" + appName + " service install' to re-enable it.\n"
	return os.WriteFile(m.OptOutFile, []byte(content), 0o644)
}

// ClearOptOut removes the opt-out marker (called by `service
// install`); a missing marker or unresolved config dir is a no-op.
func (m *Manager) ClearOptOut() error {
	if m.OptOutFile == "" {
		return nil
	}
	if err := os.Remove(m.OptOutFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
