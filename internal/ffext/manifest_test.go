package ffext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSocketPath(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	require.Equal(t, "/custom/s.sock",
		SocketPath(env(map[string]string{EnvSocket: "/custom/s.sock"})))
	require.Equal(t, filepath.Join("/run/user/1000", "competent-search-thing-ffext.sock"),
		SocketPath(env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"})))
	p := SocketPath(env(nil))
	require.Contains(t, p, "competent-search-thing-ffext-")
	require.Contains(t, p, ".sock")
}

func TestHostNameShape(t *testing.T) {
	// Firefox's documented host-name rule (MDN Native_manifests).
	require.Regexp(t, regexp.MustCompile(`^\w+(\.\w+)*$`), HostName)
}

func TestManifestPathPerOS(t *testing.T) {
	p, ok := ManifestPath("linux", "/home/u", "/cfg")
	require.True(t, ok)
	require.Equal(t, "/home/u/.mozilla/native-messaging-hosts/competent_search_thing.json", p)

	p, ok = ManifestPath("darwin", "/Users/u", "/cfg")
	require.True(t, ok)
	require.Equal(t, "/Users/u/Library/Application Support/Mozilla/NativeMessagingHosts/competent_search_thing.json", p)

	p, ok = ManifestPath("windows", "", `C:\cfg`)
	require.True(t, ok)
	require.Equal(t, filepath.Join(`C:\cfg`, "competent_search_thing.json"), p)

	// Other unix-likes take the linux shape.
	p, ok = ManifestPath("freebsd", "/home/u", "")
	require.True(t, ok)
	require.Equal(t, "/home/u/.mozilla/native-messaging-hosts/competent_search_thing.json", p)

	_, ok = ManifestPath("linux", "", "/cfg")
	require.False(t, ok, "no home = no manifest location")
	_, ok = ManifestPath("windows", "/home/u", "")
	require.False(t, ok, "no configDir = no windows manifest location")
}

func TestWrapperPathAndContent(t *testing.T) {
	require.Equal(t, filepath.Join("/cfg", "firefox-host.sh"), WrapperPath("linux", "/cfg"))
	require.Equal(t, filepath.Join("/cfg", "firefox-host.bat"), WrapperPath("windows", "/cfg"))

	sh := string(WrapperContent("linux", "/usr/local/bin/app"))
	require.Contains(t, sh, "#!/bin/sh\n")
	require.Contains(t, sh, `exec '/usr/local/bin/app' firefox-host "$@"`)

	// A path holding a single quote survives the sh quoting.
	quoted := string(WrapperContent("linux", "/opt/o'brien/app"))
	require.Contains(t, quoted, `exec '/opt/o'\''brien/app' firefox-host "$@"`)
	require.Equal(t, "/opt/o'brien/app", wrapperExe([]byte(quoted)))

	bat := string(WrapperContent("windows", `C:\Apps\cst.exe`))
	require.Contains(t, bat, "@echo off\r\n")
	require.Contains(t, bat, `"C:\Apps\cst.exe" firefox-host %*`)
	require.Equal(t, `C:\Apps\cst.exe`, wrapperExe([]byte(bat)))

	require.Empty(t, wrapperExe([]byte("#!/bin/sh\necho not ours\n")))
}

func TestManifestContent(t *testing.T) {
	raw := ManifestContent("/cfg/firefox-host.sh")
	var m nativeManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, HostName, m.Name)
	require.Equal(t, "stdio", m.Type)
	require.Equal(t, "/cfg/firefox-host.sh", m.Path)
	require.Equal(t, []string{ExtensionID}, m.AllowedExtensions)
	require.NotEmpty(t, m.Description)
	require.Equal(t, byte('\n'), raw[len(raw)-1], "trailing newline (the config.Encode convention)")
}

func TestInstallHostFreshAndIdempotent(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()

	res, err := InstallHost("linux", home, cfg, "/usr/bin/app")
	require.NoError(t, err)
	require.True(t, res.WroteWrapper)
	require.True(t, res.WroteManifest)
	require.Empty(t, res.PreviousExe)
	require.Equal(t, filepath.Join(cfg, "firefox-host.sh"), res.WrapperPath)
	require.Equal(t, filepath.Join(home, ".mozilla", "native-messaging-hosts", HostName+".json"), res.ManifestPath)

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(res.WrapperPath)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "wrapper is owner-executable")
	}
	var m nativeManifest
	raw, err := os.ReadFile(res.ManifestPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, res.WrapperPath, m.Path)

	// Second run: steady state, zero writes.
	res2, err := InstallHost("linux", home, cfg, "/usr/bin/app")
	require.NoError(t, err)
	require.False(t, res2.WroteWrapper)
	require.False(t, res2.WroteManifest)
	require.Empty(t, res2.PreviousExe)
}

func TestInstallHostSelfHealsExeChange(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	_, err := InstallHost("linux", home, cfg, "/old/place/app")
	require.NoError(t, err)

	res, err := InstallHost("linux", home, cfg, "/new/place/app")
	require.NoError(t, err)
	require.True(t, res.WroteWrapper, "exe change rewrites the wrapper")
	require.Equal(t, "/old/place/app", res.PreviousExe, "the repair log's old half")
	require.False(t, res.WroteManifest, "the manifest still points at the same wrapper path")

	content, err := os.ReadFile(res.WrapperPath)
	require.NoError(t, err)
	require.Equal(t, "/new/place/app", wrapperExe(content))
}

func TestInstallHostHealsForeignWrapperAndManifest(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	// A hand-edited (or corrupt) wrapper and manifest are rewritten,
	// with no PreviousExe when the old content is not parseable.
	require.NoError(t, os.WriteFile(WrapperPath("linux", cfg), []byte("#!/bin/sh\necho hi\n"), 0o700))
	mpath, ok := ManifestPath("linux", home, cfg)
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(filepath.Dir(mpath), 0o755))
	require.NoError(t, os.WriteFile(mpath, []byte("{}"), 0o644))

	res, err := InstallHost("linux", home, cfg, "/usr/bin/app")
	require.NoError(t, err)
	require.True(t, res.WroteWrapper)
	require.True(t, res.WroteManifest)
	require.Empty(t, res.PreviousExe)
}

func TestInstallHostErrors(t *testing.T) {
	_, err := InstallHost("linux", "", t.TempDir(), "/usr/bin/app")
	require.Error(t, err, "no home")
	_, err = InstallHost("linux", t.TempDir(), "", "/usr/bin/app")
	require.Error(t, err, "no config dir")
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.txt")
	require.NoError(t, writeFileAtomic(p, []byte("one"), 0o600))
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "one", string(got))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	require.NoError(t, writeFileAtomic(p, []byte("two"), 0o600))
	got, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "two", string(got))
	// No stray temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
