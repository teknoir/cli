package cmd

import (
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

// mkTunnel builds the tunnel facts of a device the way the platform derives them (backstage
// snapshot.ts): WireGuard is enabled by the annotation and active when its state is "active";
// "wireguard" wins over "legacy"; an enabled but not active WireGuard is "pending", labelled with its
// state ("enrolling" when none is known); anything else is "off".
func mkTunnel(annotated bool, wgState string, legacy bool, legacyPort string) api.FleetDeviceTunnel {
	var t api.FleetDeviceTunnel
	t.WireGuard.Enabled = annotated
	if annotated {
		t.WireGuard.Annotation = "enabled"
	}
	t.WireGuard.State = wgState
	t.WireGuard.Active = annotated && wgState == "active"
	t.Legacy.Active = legacy
	if legacy {
		t.Legacy.Port = legacyPort
	}
	switch {
	case t.WireGuard.Active:
		t.Kind, t.Label = "wireguard", "wireguard"
	case legacy:
		t.Kind, t.Label = "legacy", "legacy"
	case annotated:
		t.Kind, t.Label = "pending", wgState
		if wgState == "" {
			t.Label = "enrolling"
		}
	default:
		t.Kind, t.Label = "off", "off"
	}
	return t
}

func testDevice(annotated bool, wgState string, legacy bool) *api.FleetDevice {
	return &api.FleetDevice{Name: "dev1", Tunnel: mkTunnel(annotated, wgState, legacy, "40022")}
}

func TestChooseTunnel(t *testing.T) {
	tests := []struct {
		name       string
		dev        *api.FleetDevice
		force      bool
		wantKind   tunnelKind
		wantNotice bool
		wantErr    string
	}{
		{"wireguard active", testDevice(true, "active", false), false, tunnelWireGuard, false, ""},
		{"wireguard active, legacy also active", testDevice(true, "active", true), false, tunnelWireGuard, false, ""},
		{"wireguard enrolling, legacy active", testDevice(true, "enrolling", true), false, tunnelLegacy, true, ""},
		{"state active but annotation missing, legacy active", testDevice(false, "active", true), false, tunnelLegacy, true, ""},
		{"legacy only", testDevice(false, "", true), false, tunnelLegacy, true, ""},
		{"neither", testDevice(false, "", false), false, 0, false, "Tunneling is not active for dev1. It turns on by itself once the device is bootstrapped and online; if it stays off, a teamspace admin turned it off or the device is flagged. See: tnctl wg status dev1"},
		{"wireguard enrolling, nothing else", testDevice(true, "enrolling", false), false, 0, false, "Tunneling is not active for dev1."},
		{"forced, active", testDevice(true, "active", true), true, tunnelWireGuard, false, ""},
		{"forced, not active, legacy active", testDevice(true, "enrolling", true), true, 0, false, "--wireguard: dev1 has no active WireGuard tunnel (enrolling)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, notice, err := chooseTunnel(tc.dev, "dev1", tc.force)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", kind, tc.wantKind)
			}
			if (notice != "") != tc.wantNotice {
				t.Errorf("notice = %q, want notice=%v", notice, tc.wantNotice)
			}
			if tc.wantNotice && notice != "tnctl: dev1 has no active WireGuard tunnel; using the legacy SSH tunnel" {
				t.Errorf("notice = %q", notice)
			}
		})
	}
}

func TestTunnelLabel(t *testing.T) {
	tests := []struct {
		dev  *api.FleetDevice
		want string
	}{
		{testDevice(true, "active", false), "wireguard"},
		{testDevice(true, "active", true), "wireguard"},
		{testDevice(true, "enrolling", true), "legacy"},
		{testDevice(false, "", true), "legacy"},
		{testDevice(true, "enrolling", false), "enrolling"},
		{testDevice(true, "", false), "enrolling"},
		{testDevice(false, "", false), "off"},
	}
	for _, tc := range tests {
		if got := tc.dev.TunnelLabel(); got != tc.want {
			t.Errorf("TunnelLabel() = %q, want %q", got, tc.want)
		}
	}
}

func TestRefusalMessages(t *testing.T) {
	if got := errToOverWireGuard("d").Error(); got != "--to is not available over WireGuard; forward one of the device's services: tnctl port-forward d <service-id> (list them with tnctl port-forward d)" {
		t.Errorf("--to message: %s", got)
	}
	if got := errSOCKSOverWireGuard("d").Error(); got != "SOCKS is not available over WireGuard. Forward one of the device's services instead: tnctl port-forward d <service-id>" {
		t.Errorf("socks message: %s", got)
	}
}

func TestParsePortForwardArgs(t *testing.T) {
	tests := []struct {
		args       []string
		deviceFlag string
		wantDev    string
		wantMap    string
		wantErr    bool
	}{
		{nil, "", "", "", false},
		{nil, "d", "d", "", false},
		{[]string{"d"}, "", "d", "", false},
		{[]string{"d"}, "x", "x", "d", false},
		{[]string{"8080:grafana"}, "", "", "8080:grafana", false},
		{[]string{"8080:grafana"}, "d", "d", "8080:grafana", false},
		{[]string{"d", "grafana"}, "", "d", "grafana", false},
		{[]string{"d", "8080:grafana"}, "", "d", "8080:grafana", false},
		{[]string{"d", "8080:localhost:80"}, "", "d", "8080:localhost:80", false},
		{[]string{"a", "b", "c"}, "", "", "", true},
	}
	for _, tc := range tests {
		dev, m, err := parsePortForwardArgs(tc.args, tc.deviceFlag)
		if (err != nil) != tc.wantErr || dev != tc.wantDev || m != tc.wantMap {
			t.Errorf("parsePortForwardArgs(%v, %q) = %q, %q, %v; want %q, %q, err=%v", tc.args, tc.deviceFlag, dev, m, err, tc.wantDev, tc.wantMap, tc.wantErr)
		}
	}
}

func TestParseWGForwardArgGrammar(t *testing.T) {
	tests := []struct {
		in      string
		local   int
		service string
		wantErr bool
	}{
		{"grafana", 0, "grafana", false},
		{"8080:grafana", 8080, "grafana", false},
		{"8080:localhost:80", 0, "", true},
		{"0:grafana", 0, "", true},
		{"x:grafana", 0, "", true},
		{"Grafana", 0, "", true},
	}
	for _, tc := range tests {
		l, s, err := parseWGForwardArg(tc.in)
		if (err != nil) != tc.wantErr || l != tc.local || s != tc.service {
			t.Errorf("parseWGForwardArg(%q) = %d, %q, %v", tc.in, l, s, err)
		}
	}
}

func TestFormatServiceList(t *testing.T) {
	if got := formatServiceList("d", nil); !strings.Contains(got, "publishes no services") {
		t.Errorf("empty: %q", got)
	}
	got := formatServiceList("d", []wgapi.ServiceSpec{{ID: "grafana", Web: true}})
	if !strings.Contains(got, "grafana") || !strings.Contains(got, "tnctl port-forward d") {
		t.Errorf("list: %q", got)
	}
}
