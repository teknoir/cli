package cmd

import "fmt"

// deviceHostKeyOpts are the ssh options for the device hop (the device's sshd, reached at
// 127.0.0.1:<tunnel port> through deadend). The host key is stored under a per-device alias
// instead of [127.0.0.1]:<port>: tunnel ports are random and reused across devices and
// re-registrations, so keying on the port mixed devices up ("host identification changed", or
// one device's key accepted for another). accept-new trusts a device on first use, also without
// a terminal, and still refuses a device whose key later changes.
func deviceHostKeyOpts(deviceName, namespace, domain string) []string {
	return []string{
		"-o", fmt.Sprintf("HostKeyAlias=%s.%s.%s", deviceName, namespace, domain),
		"-o", "StrictHostKeyChecking=accept-new",
	}
}
