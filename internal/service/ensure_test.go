package service

// The auto-registration decision matrix (Ensure), pinned per OS over
// the scripted runner: foreign owner -> opt-out -> current -> stale
// repair -> fresh registration, and the headless degrades. The
// load-bearing negative: Ensure NEVER starts anything (no bootstrap,
// no start, no restart) -- the exact-argv scripted runner fails the
// test on any such call.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// ensureManager is testManager plus a resolvable opt-out path.
func ensureManager(t *testing.T, goos string) (*Manager, *scriptedRunner) {
	t.Helper()
	m, r := testManager(t, goos)
	m.OptOutFile = OptOutPath(filepath.Join(m.Home, "cfg"))
	return m, r
}

func TestEnsureUnsupportedOS(t *testing.T) {
	m, r := ensureManager(t, "windows")
	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureUnsupported, res.Action)
	require.Zero(t, r.callCount())
}

// --- darwin matrix ----------------------------------------------------

func TestEnsureDarwinYieldsToBrewPlist(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.brewAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.brewAgentPath(), []byte("brew"), 0o644))

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureYielded, res.Action)
	require.Contains(t, res.Owner, "brew services")
	require.Contains(t, res.Owner, m.brewAgentPath())
	require.False(t, res.OursToo)
	require.Zero(t, r.callCount(), "the plist alone is evidence")
	require.NoFileExists(t, m.launchAgentPath(), "yield writes nothing")
}

func TestEnsureDarwinYieldNamesOurLeftoverAgent(t *testing.T) {
	m, _ := ensureManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.brewAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.brewAgentPath(), []byte("brew"), 0o644))
	require.NoError(t, os.WriteFile(m.launchAgentPath(), []byte("ours"), 0o644))

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureYielded, res.Action)
	require.True(t, res.OursToo, "the caller can hint at 'service uninstall'")
}

func TestEnsureDarwinOptedOut(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	scriptNoBrewAgent(r)
	require.NoError(t, m.WriteOptOut())

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureOptedOut, res.Action)
	require.Equal(t, m.OptOutFile, res.Note, "the log names the marker")
	require.NoFileExists(t, m.launchAgentPath())
}

func TestEnsureDarwinFreshRegistersWithoutStarting(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.on("", "launchctl", "enable", guiSvc)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRegistered, res.Action)
	require.Equal(t, m.launchAgentPath(), res.ServicePath)
	require.Equal(t, testExe, res.Exe)

	data, err := os.ReadFile(res.ServicePath)
	require.NoError(t, err)
	require.Equal(t, LaunchAgentPlist(testExe, m.darwinLogPath()), string(data))
	require.DirExists(t, filepath.Dir(m.darwinLogPath()))
	// The one call is enable; NO bootstrap -- bootstrapping a
	// RunAtLoad agent starts a copy that would summon the bar.
	require.Equal(t, [][]string{
		{"launchctl", "print", guiBrewSvc},
		{"launchctl", "enable", guiSvc},
	}, r.allCalls())
}

func TestEnsureDarwinCurrentIsANoOp(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.on("", "launchctl", "enable", guiSvc)
	_, err := m.Ensure(context.Background())
	require.NoError(t, err)

	r2 := newScriptedRunner(t)
	m.Run = r2.run
	scriptNoBrewAgent(r2)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureCurrent, res.Action)
	require.Equal(t, [][]string{{"launchctl", "print", guiBrewSvc}}, r2.allCalls(),
		"a current registration runs only the ownership probe")
}

func TestEnsureDarwinStaleRepairs(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.launchAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.launchAgentPath(),
		[]byte(LaunchAgentPlist("/old/Cellar/binary", m.darwinLogPath())), 0o644))
	scriptNoBrewAgent(r)
	r.on("", "launchctl", "enable", guiSvc)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRepaired, res.Action)
	require.Equal(t, "/old/Cellar/binary", res.PreviousExe, "the repair log's old side")
	require.Equal(t, testExe, res.Exe)

	data, err := os.ReadFile(m.launchAgentPath())
	require.NoError(t, err)
	require.Contains(t, string(data), testExe)
	for _, call := range r.allCalls() {
		require.NotEqual(t, "bootout", call[1], "repair never unloads the possibly-running job")
		require.NotEqual(t, "bootstrap", call[1], "repair never starts anything")
	}
}

func TestEnsureDarwinFreshHeadlessRollsBack(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "enable", guiSvc)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err, "headless is a degrade, not an error")
	require.Equal(t, EnsureUnavailable, res.Action)
	require.Contains(t, res.Note, "GUI domain")
	require.NoFileExists(t, m.launchAgentPath(),
		"a fresh write rolls back so the state stays 'not registered'")
}

func TestEnsureDarwinRepairKeepsContentWhenEnableFails(t *testing.T) {
	m, r := ensureManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.launchAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.launchAgentPath(),
		[]byte(LaunchAgentPlist("/old/binary", m.darwinLogPath())), 0o644))
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "enable", guiSvc)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRepaired, res.Action)
	require.Contains(t, res.Note, "enable failed")
	data, err := os.ReadFile(m.launchAgentPath())
	require.NoError(t, err)
	require.Contains(t, string(data), testExe, "the repaired content is kept")
}

// --- linux matrix -----------------------------------------------------

func TestEnsureLinuxYieldsToDebUnit(t *testing.T) {
	m, r := ensureManager(t, "linux")
	sys := t.TempDir()
	m.SystemUnitDirs = []string{filepath.Join(sys, "missing"), sys}
	debUnit := filepath.Join(sys, UnitName)
	require.NoError(t, os.WriteFile(debUnit, []byte("deb"), 0o644))

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureYielded, res.Action)
	require.Equal(t, "the deb-installed unit "+debUnit, res.Owner)
	require.Empty(t, res.Hint)
	require.False(t, res.OursToo)
	require.Zero(t, r.callCount())
	require.NoFileExists(t, m.unitPath())
}

func TestEnsureLinuxYieldsToBrewUnitWithStopOnceHint(t *testing.T) {
	m, r := ensureManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.brewUnitPath(), []byte("brew"), 0o644))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte("ours"), 0o644))

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureYielded, res.Action)
	require.Contains(t, res.Owner, "brew services")
	require.Contains(t, res.Owner, m.brewUnitPath())
	require.Contains(t, res.Hint, "brew services stop pazer/build/competent-search-thing",
		"the polite handover: stop brew's unit once and self-registration owns it")
	require.True(t, res.OursToo)
	require.Zero(t, r.callCount())
}

func TestEnsureLinuxOptedOut(t *testing.T) {
	m, r := ensureManager(t, "linux")
	require.NoError(t, m.WriteOptOut())

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureOptedOut, res.Action)
	require.Equal(t, m.OptOutFile, res.Note)
	require.Zero(t, r.callCount())
	require.NoFileExists(t, m.unitPath())
}

func TestEnsureLinuxFreshRegistersWithoutStarting(t *testing.T) {
	m, r := ensureManager(t, "linux")
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRegistered, res.Action)
	require.Equal(t, m.unitPath(), res.ServicePath)

	data, err := os.ReadFile(m.unitPath())
	require.NoError(t, err)
	require.Equal(t, SystemdUnit(testExe), string(data))
	// daemon-reload + enable and NOTHING else: no is-active probes,
	// no start -- the app is already running, possibly AS the unit.
	require.Equal(t, [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", UnitName},
	}, r.allCalls())
}

func TestEnsureLinuxCurrentIsZeroExec(t *testing.T) {
	m, r := ensureManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte(SystemdUnit(testExe)), 0o644))

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureCurrent, res.Action)
	require.Zero(t, r.callCount(), "the every-boot check costs stats and one read, zero execs")
}

func TestEnsureLinuxStaleRepairs(t *testing.T) {
	m, r := ensureManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte(SystemdUnit("/old/Cellar/binary")), 0o644))
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRepaired, res.Action)
	require.Equal(t, "/old/Cellar/binary", res.PreviousExe)

	data, err := os.ReadFile(m.unitPath())
	require.NoError(t, err)
	require.Equal(t, SystemdUnit(testExe), string(data))
	for _, call := range r.allCalls() {
		require.NotContains(t, call, "restart", "repair never restarts the possibly-us unit")
		require.NotContains(t, call, "start")
	}
}

func TestEnsureLinuxFreshNoBusRollsBack(t *testing.T) {
	m, r := ensureManager(t, "linux")
	r.fail(errNotFound, "systemctl", "--user", "daemon-reload")

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureUnavailable, res.Action)
	require.Contains(t, res.Note, "systemd user manager unavailable")
	require.NoFileExists(t, m.unitPath())
}

func TestEnsureLinuxFreshEnableFailureRollsBack(t *testing.T) {
	m, r := ensureManager(t, "linux")
	r.on("", "systemctl", "--user", "daemon-reload")
	r.fail(errNotFound, "systemctl", "--user", "enable", UnitName)

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureUnavailable, res.Action)
	require.Contains(t, res.Note, "enable failed")
	require.NoFileExists(t, m.unitPath())
}

func TestEnsureLinuxRepairKeepsContentWhenBusGone(t *testing.T) {
	m, r := ensureManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte(SystemdUnit("/old/binary")), 0o644))
	r.fail(errNotFound, "systemctl", "--user", "daemon-reload")

	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRepaired, res.Action)
	require.Contains(t, res.Note, "unavailable")
	data, err := os.ReadFile(m.unitPath())
	require.NoError(t, err)
	require.Equal(t, SystemdUnit(testExe), string(data), "the repaired content is kept")
}

// --- the extraction helpers -------------------------------------------

func TestUnitExecStartExtraction(t *testing.T) {
	require.Equal(t, "/old/binary", unitExecStart([]byte(SystemdUnit("/old/binary"))))
	require.Equal(t, `"/opt/my tools/app"`, unitExecStart([]byte(SystemdUnit("/opt/my tools/app"))),
		"quoted spellings surface verbatim -- a log detail, not a parse")
	require.Empty(t, unitExecStart([]byte("no exec line here\n")))
}

func TestPlistProgramExtraction(t *testing.T) {
	require.Equal(t, "/old/binary",
		plistProgram([]byte(LaunchAgentPlist("/old/binary", "/tmp/l.log"))))
	require.Equal(t, `/opt/a & b/<app>`,
		plistProgram([]byte(LaunchAgentPlist(`/opt/a & b/<app>`, "/tmp/l.log"))),
		"escaped paths round-trip through xmlUnescape")
	require.Empty(t, plistProgram([]byte("not a plist")))
	require.Empty(t, plistProgram([]byte("<key>ProgramArguments</key> no strings")))
	require.Empty(t, plistProgram([]byte("<key>ProgramArguments</key><string>torn")))
}

// TestEnsureUsesConfigDirMarker pins the end-to-end marker contract
// the CLI + app share: NewManager resolves <configDir>/service.optout,
// uninstall's WriteOptOut makes Ensure answer EnsureOptedOut, and
// install's ClearOptOut re-arms registration.
func TestEnsureUsesConfigDirMarker(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	prod, err := NewManager()
	require.NoError(t, err)

	m, r := testManager(t, "linux")
	m.OptOutFile = prod.OptOutFile
	require.NoError(t, m.WriteOptOut())
	res, err := m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureOptedOut, res.Action)
	require.FileExists(t, filepath.Join(cfgDir, "service.optout"))

	require.NoError(t, m.ClearOptOut())
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	res, err = m.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, EnsureRegistered, res.Action)
}
