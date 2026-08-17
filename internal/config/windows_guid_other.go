//go:build !windows

package config

// windowsMachineGUID is a no-op stub on non-Windows platforms. The Windows
// implementation lives in windows_guid_windows.go and reads the MachineGuid
// from the registry.
func windowsMachineGUID() (string, error) {
	return "", nil
}