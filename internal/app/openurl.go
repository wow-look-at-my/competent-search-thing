package app

import (
	"fmt"
	"log"
)

// OpenExternalURL opens an http(s) URL with the operating system's
// default browser WITHOUT hiding the bar -- the config editor's
// clickable documentation links (get-an-API-key pages) route here so
// the webview never navigates away from the app and the editor stays
// up. Validation matches the open_url plugin action exactly
// (validHTTPURL: absolute http(s) with a host; defense in depth --
// the frontend merely echoes schema description URLs). Deliberately
// NOT Open(): that path hides the bar and records frecency, both
// wrong for a documentation link clicked mid-edit.
func (a *App) OpenExternalURL(raw string) error {
	if !validHTTPURL(raw) {
		return fmt.Errorf("open url: %q is not an http(s) URL", raw)
	}
	log.Printf("config: opening %s", raw)
	return a.openTarget(raw)
}
