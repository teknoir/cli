package cmd

import (
	"fmt"
	"io"

	"github.com/teknoir/cli/pkg/api"
)

// deviceDisabledError is returned instead of a transport when the device is disabled: no tunnel
// can reach it until a teamspace admin enables it again.
type deviceDisabledError struct {
	device   string
	sentence string // " by <by> on <date>: <reason>", empty when the platform sent none
}

func (e *deviceDisabledError) Error() string {
	return endSentence("device "+e.device+" is disabled"+e.sentence) +
		" It cannot be reached until a teamspace admin enables it: tnctl device enable " + e.device
}

// errDeviceDisabled is the error to return for a device that is disabled or being disabled, nil when
// it is enabled (also while it is being enabled again: that only needs a minute).
func errDeviceDisabled(d *api.FleetDevice, deviceName string) error {
	if d == nil || d.Disabled == nil {
		return nil
	}
	switch d.Disabled.State {
	case api.DisabledStateDisabled, api.DisabledStateDisabling:
		return &deviceDisabledError{device: deviceName, sentence: d.Disabled.Detail()}
	}
	return nil
}

// errDeviceEnabling is returned when a device is being enabled again and has no tunnel yet.
func errDeviceEnabling(deviceName string) error {
	return fmt.Errorf("device %s is being enabled again; Tunneling comes back within a few minutes. See: tnctl device %s", deviceName, deviceName)
}

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
	return fmt.Errorf("Tunneling is not active for %s. It turns on by itself once the device is bootstrapped and online; if it stays off, a teamspace admin turned it off or the device is flagged. See: tnctl wg status %s", deviceName, deviceName)
}

// legacyFallbackNotice is printed once on stderr when the legacy tunnel is used.
func legacyFallbackNotice(deviceName string) string {
	return fmt.Sprintf("tnctl: %s has no active WireGuard tunnel; using the legacy SSH tunnel", deviceName)
}

// chooseTunnel picks the transport for a device: WireGuard when it is active,
// otherwise the legacy SSH tunnel when that is active, otherwise an error.
// forceWG (the hidden --wireguard flag) never falls back. notice is a line for
// stderr, empty when there is nothing to say.
func chooseTunnel(d *api.FleetDevice, deviceName string, forceWG bool) (kind tunnelKind, notice string, err error) {
	if err := errDeviceDisabled(d, deviceName); err != nil {
		return 0, "", err
	}
	enabling := d.Disabled != nil && d.Disabled.State == api.DisabledStateEnabling
	if deviceWireGuardActive(d) {
		return tunnelWireGuard, "", nil
	}
	if forceWG {
		return 0, "", fmt.Errorf("--wireguard: %s has no active WireGuard tunnel (%s)", deviceName, wireGuardStateLabel(d))
	}
	if d.LegacyTunnelActive() {
		return tunnelLegacy, legacyFallbackNotice(deviceName), nil
	}
	if enabling {
		return 0, "", errDeviceEnabling(deviceName)
	}
	return 0, "", errTunnelingOff(deviceName)
}

// selectTunnel is chooseTunnel with the notice written to stderr.
func selectTunnel(d *api.FleetDevice, deviceName string, forceWG bool, stderr io.Writer) (tunnelKind, error) {
	kind, notice, err := chooseTunnel(d, deviceName, forceWG)
	if notice != "" {
		fmt.Fprintln(stderr, notice)
	}
	return kind, err
}

// wireGuardStateLabel describes the WireGuard state of a device in one word,
// for errors: "active", the reported state (for example "enrolling") when
// enabled but not active, or "off".
func wireGuardStateLabel(d *api.FleetDevice) string {
	if d.WireGuardActive() {
		return "active"
	}
	if wg := d.Tunnel.WireGuard; wg.Enabled {
		if wg.State != "" {
			return wg.State
		}
		return "enrolling"
	}
	return "off"
}
