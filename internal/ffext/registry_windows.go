//go:build windows

package ffext

import "golang.org/x/sys/windows/registry"

// registerHostManifest points the per-user Firefox native-messaging
// registry key at manifestPath: the key's DEFAULT value under
// HKCU\Software\Mozilla\NativeMessagingHosts\<HostName> names the
// manifest JSON (MDN Native_manifests, Windows). Idempotent; HKCU
// needs no elevation.
func registerHostManifest(manifestPath string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Mozilla\NativeMessagingHosts\`+HostName, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("", manifestPath)
}
