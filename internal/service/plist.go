package service

import "strings"

// brewLabel is the launchd label `brew services` would use if the
// Homebrew formula ever gains a service block (homebrew.mxcl.<name>
// is brew's fixed convention). Exactly one unit may own startup:
// under brew's keep_alive true, a second unit's copy exiting 0
// through the single-instance IPC handoff is respawned every ~10s,
// each respawn re-summoning the bar. Install detects this label and
// refuses -- the brew unit wins (its opt_bin path tracks upgrades,
// and brew users expect `brew services` to own what it manages).
const brewLabel = "homebrew.mxcl." + appName

// LaunchAgentPlist renders the launchd LaunchAgent property list for
// the given binary and log file path.
//
// Key choices, verified against launchd.plist(5):
//   - RunAtLoad true: start at login when the agent is bootstrapped.
//   - KeepAlive {SuccessfulExit: false}: restart ONLY on a non-zero
//     exit (a crash); a clean exit 0 -- the single-instance handoff
//     path when another copy already runs -- must NOT respawn-loop.
//     (KeepAlive true is the looping variant; never use it here.)
//     launchd's default ThrottleInterval additionally spaces genuine
//     crash respawns ~10s apart.
//   - LimitLoadToSessionType Aqua: a GUI app; load only in the
//     graphical login session.
//   - ProcessType Interactive: user-facing, no background resource
//     throttling.
//   - StandardOutPath/StandardErrorPath: both append to the one log
//     file (launchd opens them O_APPEND).
func LaunchAgentPlist(exe, logPath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(Label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
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
	<string>` + xmlEscape(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(logPath) + `</string>
</dict>
</plist>
`
}

// xmlEscaper escapes the five XML special characters for text nodes.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// xmlEscape escapes s for use inside a plist <string> element.
func xmlEscape(s string) string { return xmlEscaper.Replace(s) }
