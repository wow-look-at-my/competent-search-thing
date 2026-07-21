package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLaunchAgentPlistGolden(t *testing.T) {
	got := LaunchAgentPlist(
		"/usr/local/bin/competent-search-thing",
		"/Users/u/Library/Logs/competent-search-thing/competent-search-thing.log",
	)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.wow-look-at-my.competent-search-thing</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/competent-search-thing</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>/Users/u/Library/Logs/competent-search-thing/competent-search-thing.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/u/Library/Logs/competent-search-thing/competent-search-thing.log</string>
</dict>
</plist>
`
	require.Equal(t, want, got)
}

// TestLaunchAgentPlistCrashOnlyKeepAlive pins the load-bearing
// KeepAlive form: the SuccessfulExit=false dictionary (restart on
// crash only). The bare `<true/>` KeepAlive variant respawns a
// clean-exit copy forever -- with the single-instance IPC handoff
// that is a visible summon-the-bar loop every ~10 seconds.
func TestLaunchAgentPlistCrashOnlyKeepAlive(t *testing.T) {
	got := LaunchAgentPlist("/bin/app", "/tmp/app.log")
	require.Contains(t, got,
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"KeepAlive must be the crash-only SuccessfulExit=false dict")
	require.NotContains(t, got, "<key>KeepAlive</key>\n\t<true/>",
		"the unconditional KeepAlive true form respawn-loops the exit-0 handoff")
	require.Contains(t, got, "<key>RunAtLoad</key>\n\t<true/>", "the agent starts at login")
}

func TestLaunchAgentPlistEscapesXML(t *testing.T) {
	got := LaunchAgentPlist(`/opt/a & b/<app>`, `/logs/"quoted" & 'log'.log`)
	require.Contains(t, got, "<string>/opt/a &amp; b/&lt;app&gt;</string>")
	require.Contains(t, got, "<string>/logs/&quot;quoted&quot; &amp; &apos;log&apos;.log</string>")
	require.NotContains(t, got, "a & b", "raw ampersands must never reach the XML")
}

func TestLaunchAgentPlistUsesAquaSession(t *testing.T) {
	got := LaunchAgentPlist("/bin/app", "/tmp/app.log")
	require.Contains(t, got, "<key>LimitLoadToSessionType</key>\n\t<string>Aqua</string>",
		"a GUI app loads only in the graphical login session")
}
