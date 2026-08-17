//go:build windows

package config

import (
	"golang.org/x/sys/windows/registry"
)

// windowsMachineGUID reads the Windows MachineGuid from the registry. This
// file is only compiled on Windows targets; other platforms use the stub in
// windows_guid_other.go.
func windowsMachineGUID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()

	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	return guid, nil
}