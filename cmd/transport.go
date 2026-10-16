package cmd

import (
	"fmt"
	"io"

	"github.com/teknoir/cli/pkg/api"
)

// tunnelKind is the transport a device is reached through.
type tunnelKind int

const (
	// tunnelWireGuard is the default tunnel.
	tunnelWireGuard tunnelKind = iota
	// tunnelLegacy is the SSH tunnel (deadend, ncat --ssl, reverse tunnel).
	// It is only an automatic fallback and goes away once production has moved
	// to WireGuard.
	tunnelLegacy
)

// errTunnelingOff is the one message every command prints when a device has no
// usable tunnel.
func errTunnelingOff(deviceName string) error {
	return fmt.Errorf("Tunneling is not active for %s. Turn it on under Tunneling on the device page in the Teknoir console.", deviceName)
}

// legacyFallbackNotice is printed once on stderr when the legacy tunnel is used.
func legacyFallbackNotice(deviceName string) string {
	return fmt.Sprintf("tnctl: %s has no active WireGuard tunnel; using the legacy SSH tunnel", deviceName)
}

// chooseTunnel picks the transport for a device: WireGuard when it is active,
// otherwise the legacy SSH tunnel when that is active, otherwise an error.
// forceWG (the hidden --wireguard flag) never falls back. notice is a line for
// stderr, empty when there is nothing to say.
func chooseTunnel(d *api.BackstageDevice, deviceName string, forceWG bool) (kind tunnelKind, notice string, err error) {
	if deviceWireGuardActive(d) {
		return tunnelWireGuard, "", nil
	}
	if forceWG {
		return 0, "", fmt.Errorf("--wireguard: %s has no active WireGuard tunnel (%s)", deviceName, wireGuardStateLabel(d))
	}
	if d.Spec.Subresources.Status.RemoteAccess.Active {
		return tunnelLegacy, legacyFallbackNotice(deviceName), nil
	}
	return 0, "", errTunnelingOff(deviceName)
}

// selectTunnel is chooseTunnel with the notice written to stderr.
func selectTunnel(d *api.BackstageDevice, deviceName string, forceWG bool, stderr io.Writer) (tunnelKind, error) {
	kind, notice, err := chooseTunnel(d, deviceName, forceWG)
	if notice != "" {
		fmt.Fprintln(stderr, notice)
	}
	return kind, err
}

// wireGuardStateLabel describes the WireGuard state of a device in one word,
// for errors: "active", the reported state (for example "enrolling") when
// enabled but not active, or "off".
func wireGuardStateLabel(d *api.BackstageDevice) string {
	if d.WireGuardActive() {
		return "active"
	}
	wg := d.Spec.Subresources.Status.WireGuard
	if d.Metadata.Annotations[api.WireGuardAnnotation] == "enabled" {
		if wg.State != "" {
			return wg.State
		}
		return "enrolling"
	}
	return "off"
}
