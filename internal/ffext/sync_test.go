package ffext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// The webextension/ drift guard (the internal/theme sync_test.go
// precedent): the shipped companion extension and this package speak
// one contract -- the host name, the pinned extension id, the
// protocol version, the message types, and the permission set the
// native manifest's install story promises. Edit both sides together
// or the build fails.

func webextFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "webextension", name))
	require.NoError(t, err, "webextension/%s must ship with the repo", name)
	return raw
}

func TestWebextensionManifestLockstep(t *testing.T) {
	var m struct {
		ManifestVersion int    `json:"manifest_version"`
		Name            string `json:"name"`
		Background      struct {
			Page       string `json:"page"`
			Persistent *bool  `json:"persistent"`
		} `json:"background"`
		Permissions []string `json:"permissions"`
		BSS         struct {
			Gecko struct {
				ID string `json:"id"`
			} `json:"gecko"`
		} `json:"browser_specific_settings"`
	}
	require.NoError(t, json.Unmarshal(webextFile(t, "manifest.json"), &m))

	// The pinned id IS the native manifest's allowed_extensions entry;
	// a drift breaks connectNative silently.
	require.Equal(t, ExtensionID, m.BSS.Gecko.ID)
	// Exactly the two documented permissions: nativeMessaging for the
	// port, tabs for url/title/lastAccessed on tabs.query results.
	require.Equal(t, []string{"nativeMessaging", "tabs"}, m.Permissions)
	// MV2 persistent background page: the design holds ONE native port
	// open for the whole browser session (an MV3 event page idles out
	// and cannot be woken by the host -- connections are always
	// extension-initiated).
	require.Equal(t, 2, m.ManifestVersion)
	require.Equal(t, "background.html", m.Background.Page)
	require.NotNil(t, m.Background.Persistent, "persistent must be explicit")
	require.True(t, *m.Background.Persistent)
	require.NotEmpty(t, m.Name)
}

// mjsConst extracts one `export const NAME = <value>;` from logic.mjs.
func mjsConst(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^export const ` + regexp.QuoteMeta(name) + ` = (.+);$`)
	m := re.FindStringSubmatch(src)
	require.NotNil(t, m, "logic.mjs must export const %s", name)
	return m[1]
}

func mjsString(t *testing.T, src, name string) string {
	t.Helper()
	v := mjsConst(t, src, name)
	s, err := strconv.Unquote(v)
	require.NoError(t, err, "%s must be a plain string literal, got %s", name, v)
	return s
}

func mjsInt(t *testing.T, src, name string) int {
	t.Helper()
	v := mjsConst(t, src, name)
	n, err := strconv.Atoi(v)
	require.NoError(t, err, "%s must be an integer literal, got %s", name, v)
	return n
}

func TestWebextensionLogicLockstep(t *testing.T) {
	src := string(webextFile(t, "logic.mjs"))

	require.Equal(t, HostName, mjsString(t, src, "HOST_NAME"),
		"runtime.connectNative's host name must match the native manifest's name")
	require.Equal(t, ProtocolVersion, mjsInt(t, src, "PROTOCOL_VERSION"))
	require.Equal(t, MsgListTabs, mjsString(t, src, "MSG_LIST_TABS"))
	require.Equal(t, MsgActivate, mjsString(t, src, "MSG_ACTIVATE"))
	require.Equal(t, MsgTabsChanged, mjsString(t, src, "MSG_TABS_CHANGED"))

	// tabRow must keep emitting favIconUrl -- the wire field rides the
	// tolerance contract (no protocol bump; a missing field parses as
	// ""), so only this pin keeps the Go side from silently losing the
	// live favicon hints if the projection drops it.
	require.Regexp(t, `favIconUrl:\s*tab\.favIconUrl`, src,
		"logic.mjs tabRow must project tab.favIconUrl onto the wire")
}

func TestWebextensionBackgroundLoadsLogic(t *testing.T) {
	// The thin entry chain: background.html loads background.js as a
	// module, which imports logic.mjs -- the split that keeps the
	// logic importable by the vitest suite.
	html := string(webextFile(t, "background.html"))
	require.Contains(t, html, `<script type="module" src="background.js">`)
	js := string(webextFile(t, "background.js"))
	require.Contains(t, js, `from "./logic.mjs"`)
}

func TestWrapperNamesTheFirefoxHostSubcommand(t *testing.T) {
	// The generated wrapper execs "<binary> firefox-host" -- the cobra
	// subcommand internal/cli registers. Pin the literal so a renamed
	// subcommand cannot silently orphan installed wrappers.
	for _, goos := range []string{"linux", "darwin", "windows"} {
		require.Contains(t, string(WrapperContent(goos, "/x/app")), " firefox-host ",
			fmt.Sprintf("goos %s", goos))
	}
}
