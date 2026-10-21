package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

// Go mirror of the user-scoped fleet routes tnctl uses for teamspaces, devices and device logins
// (backstage packages/common/src/models/fleet.ts: FleetTeamspace, FleetDeviceSummary,
// FleetDeviceTunnel, FleetDeviceCredentials). The platform decides what the caller may see: the
// teamspaces of their groups, the devices of their organisation units, the login keys only for
// editors who may open a terminal.

// LastCatalogVersion is the last tnctl release that talks to the Backstage catalog instead of the
// fleet API. A platform that has no fleet API (the classic Backstage) still works with it.
const LastCatalogVersion = "v0.0.9-beta"

// UnsupportedPlatformError is returned when the platform does not have the fleet API this tnctl needs.
type UnsupportedPlatformError struct {
	// Version is this tnctl's version.
	Version string
	// Feature, when set, is what the platform cannot do yet ("add or edit local resources"): it has
	// the fleet API, but its device backend predates this route.
	Feature string
}

func (e *UnsupportedPlatformError) Error() string {
	if e.Feature != "" {
		return fmt.Sprintf("this platform cannot %s yet: its device backend predates it (the route answered 404). "+
			"Ask your platform admin to update it", e.Feature)
	}
	return fmt.Sprintf("this platform does not support tnctl %s yet (it has no fleet API for teamspaces and devices). "+
		"Use tnctl %s with this platform, or ask your platform admin to update it", e.Version, LastCatalogVersion)
}

// Access levels of a teamspace.
const (
	AccessRead  = "read"
	AccessWrite = "write"
)

// FleetTeamspace is one teamspace the caller can work with (GET /fleet/teamspaces).
type FleetTeamspace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	// Access is "write" (developer: group d-w or d-rw) or "read" (viewer: d-r).
	Access string `json:"access"`
}

// FleetDeviceTunnel says how a device can be reached; the server derives it, so every client agrees.
type FleetDeviceTunnel struct {
	// Kind is wireguard, legacy, pending or off; wireguard wins when both tunnels are available.
	Kind string `json:"kind"`
	// Label is the text of the Tunnel column: wireguard, legacy, off, or the WireGuard state
	// (for example enrolling) while pending.
	Label     string `json:"label"`
	WireGuard struct {
		// Enabled: the annotation ztna.teknoir.org/wireguard is "enabled" and the status does not say otherwise.
		Enabled    bool   `json:"enabled"`
		Annotation string `json:"annotation,omitempty"`
		State      string `json:"state,omitempty"`
		Connected  *bool  `json:"connected,omitempty"`
		// Active: enabled and the state is "active"; a client may open a WireGuard session.
		Active bool `json:"active"`
	} `json:"wireguard"`
	Legacy struct {
		// Active: the device reports its legacy SSH tunnel as open.
		Active bool `json:"active"`
		// Port is the tunnel's port at the deadend host, as text.
		Port string `json:"port,omitempty"`
	} `json:"legacy"`
}

// FleetDeviceAlert is a group of alerts of one name and state.
type FleetDeviceAlert struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Count int    `json:"count,omitempty"`
}

// FleetDeviceApp is the status of one application on a device.
type FleetDeviceApp struct {
	Running  bool `json:"running"`
	Restarts int  `json:"restarts,omitempty"`
	// Ready is "ready/total" containers, for example "1/2".
	Ready string `json:"ready,omitempty"`
	// CPU and Memory are fractions of 1.
	CPU    float64 `json:"cpu,omitempty"`
	Memory float64 `json:"memory,omitempty"`
}

// FleetDeviceState is what the device last reported. A device that reported nothing has no state.
type FleetDeviceState struct {
	// CPU, Memory and Storage are fractions of 1 (0.25 is 25 %).
	CPU     float64 `json:"cpu,omitempty"`
	Memory  float64 `json:"memory,omitempty"`
	Storage float64 `json:"storage,omitempty"`
	// NetRx and NetTx are KB/s.
	NetRx      float64                   `json:"netRx,omitempty"`
	NetTx      float64                   `json:"netTx,omitempty"`
	Alerts     []FleetDeviceAlert        `json:"alerts,omitempty"`
	AlertCount int                       `json:"alertCount,omitempty"`
	Apps       map[string]FleetDeviceApp `json:"apps,omitempty"`
	AppCount   int                       `json:"appCount,omitempty"`
}

// FleetDeviceConfig is the configuration the device runs.
type FleetDeviceConfig struct {
	Hash       string `json:"hash,omitempty"`
	LastChange string `json:"lastChange,omitempty"`
}

// FleetOrgPlacement is where a device sits in the teamspace's organisation tree.
type FleetOrgPlacement struct {
	Unit string `json:"unit"`
	Path []struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	} `json:"path"`
}

// FleetDevice is a device as the fleet API lists it (FleetDeviceSummary).
type FleetDevice struct {
	Name       string             `json:"name"`
	Namespace  string             `json:"namespace"`
	Labels     map[string]string  `json:"labels,omitempty"`
	Hardware   string             `json:"hardware,omitempty"`
	DeviceType string             `json:"deviceType,omitempty"`
	Virtual    bool               `json:"virtual,omitempty"`
	Robot      string             `json:"robot,omitempty"`
	CreatedAt  string             `json:"createdAt,omitempty"`
	State      *FleetDeviceState  `json:"state,omitempty"`
	Config     *FleetDeviceConfig `json:"config,omitempty"`
	Tunnel     FleetDeviceTunnel  `json:"tunnel"`
	// Services are the services the device publishes through its tunnel (annotation
	// ztna.teknoir.org/services); empty when there are none or when ServicesInvalid is set.
	Services        []wgapi.ServiceSpec `json:"services,omitempty"`
	ServicesInvalid bool                `json:"servicesInvalid,omitempty"`
	Org             *FleetOrgPlacement  `json:"org,omitempty"`
}

// WireGuardActive reports whether the device's WireGuard tunnel is usable.
func (d *FleetDevice) WireGuardActive() bool { return d.Tunnel.WireGuard.Active }

// LegacyTunnelActive reports whether the device's legacy SSH tunnel is open.
func (d *FleetDevice) LegacyTunnelActive() bool { return d.Tunnel.Legacy.Active }

// TunnelLabel is the value of the Tunnel column, as the server worded it ("off" when it said nothing).
func (d *FleetDevice) TunnelLabel() string {
	if d.Tunnel.Label == "" {
		return "off"
	}
	return d.Tunnel.Label
}

// StateOrZero returns the reported state, or an empty one for a device that reported nothing.
func (d *FleetDevice) StateOrZero() FleetDeviceState {
	if d.State == nil {
		return FleetDeviceState{}
	}
	return *d.State
}

// LastConfigChange is the time of the device's last configuration change, empty when unknown.
func (d *FleetDevice) LastConfigChange() string {
	if d.Config == nil {
		return ""
	}
	return d.Config.LastChange
}

// ConfigHash is the active configuration hash, empty when unknown.
func (d *FleetDevice) ConfigHash() string {
	if d.Config == nil {
		return ""
	}
	return d.Config.Hash
}

// AlertTotal is the number of alerts (the Alerts column).
func (s FleetDeviceState) AlertTotal() int {
	if s.AlertCount > 0 || len(s.Alerts) == 0 {
		return s.AlertCount
	}
	n := 0
	for _, a := range s.Alerts {
		n += a.Count
	}
	return n
}

// AppTotal is the number of applications (the Apps column).
func (s FleetDeviceState) AppTotal() int {
	if s.AppCount > 0 || len(s.Apps) == 0 {
		return s.AppCount
	}
	return len(s.Apps)
}

// FleetDeviceCredentials is the login of one device. The values are base64 text, exactly as the
// Device stores them; a field is empty when the Device has no value (a key pair is generated a few
// minutes after the Device is created). These are secrets: never log or print them.
type FleetDeviceCredentials struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	RSAPrivate   string `json:"rsaPrivate,omitempty"`
	RSAPublic    string `json:"rsaPublic,omitempty"`
	PublicSSHKey string `json:"publicSshKey,omitempty"`
}

// FleetDeviceCredentialsBulk is the answer of the bulk credentials route.
type FleetDeviceCredentialsBulk struct {
	// Devices are the devices that were found and are inside the caller's organisation units.
	Devices []FleetDeviceCredentials `json:"devices"`
	// Missing are names that do not exist or that the caller may not see (not told apart).
	Missing []string `json:"missing"`
}

// MaxBulkCredentials is how many devices one bulk credentials call takes.
const MaxBulkCredentials = 200

// MaxLabelFilters is how many label filters one device list takes.
const MaxLabelFilters = 20

// rootURL is https://<domain>/api/device/fleet, the base of the routes that name no teamspace.
func (f *Fleet) rootURL() string {
	if f.Root != "" {
		return strings.TrimSuffix(f.Root, "/")
	}
	if i := strings.LastIndex(f.Base, "/"); i >= 0 {
		return f.Base[:i]
	}
	return f.Base
}

// doRoot calls path relative to the fleet root (no teamspace in it).
func (f *Fleet) doRoot(ctx context.Context, method, path string) (json.RawMessage, error) {
	token, err := f.Token(ctx)
	if err != nil {
		return nil, err
	}
	return doRaw(ctx, method, f.rootURL()+"/"+path, token, nil)
}

func isStatus(err error, status int) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Status == status
}

// Teamspaces lists the teamspaces the caller can work with. A platform without the fleet API
// (404) answers with an *UnsupportedPlatformError.
func (f *Fleet) Teamspaces(ctx context.Context) ([]FleetTeamspace, error) {
	raw, err := f.doRoot(ctx, "GET", "teamspaces")
	if isStatus(err, http.StatusNotFound) {
		return nil, &UnsupportedPlatformError{Version: Version}
	}
	out, _, err := decode[[]FleetTeamspace](raw, err)
	return out, err
}

// platformErr turns the 404 of a device route into an *UnsupportedPlatformError when the platform
// has no fleet API at all (the teamspaces route is a 404 too), and leaves every other error alone.
func (f *Fleet) platformErr(ctx context.Context, err error) error {
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Code == "not_found" {
		return err
	}
	if _, terr := f.Teamspaces(ctx); terr != nil {
		var unsupported *UnsupportedPlatformError
		if errors.As(terr, &unsupported) {
			return terr
		}
	}
	return err
}

// Devices lists the devices of the teamspace the caller may see, optionally only those whose labels
// match every key=value in labels.
func (f *Fleet) Devices(ctx context.Context, labels map[string]string) ([]FleetDevice, error) {
	if len(labels) > MaxLabelFilters {
		return nil, fmt.Errorf("at most %d label filters are allowed, got %d", MaxLabelFilters, len(labels))
	}
	var q url.Values
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		q = url.Values{}
		for _, k := range keys {
			q.Add("label", k+"="+labels[k])
		}
	}
	out, _, err := decode[[]FleetDevice](f.Do(ctx, "GET", "devices", q, nil))
	if err != nil {
		return nil, f.platformErr(ctx, err)
	}
	return out, nil
}

// Device returns one device; a device that does not exist or lies outside the caller's organisation
// units is a 404.
func (f *Fleet) Device(ctx context.Context, name string) (*FleetDevice, error) {
	d, _, err := decode[FleetDevice](f.Do(ctx, "GET", "devices/"+url.PathEscape(name), nil, nil))
	if err != nil {
		return nil, f.platformErr(ctx, err)
	}
	if d.Name == "" {
		d.Name = name
	}
	return &d, nil
}

// DeviceNames lists the names of the devices of the teamspace.
func (f *Fleet) DeviceNames(ctx context.Context) ([]string, error) {
	devices, err := f.Devices(ctx, nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, d.Name)
	}
	return names, nil
}

// Credentials returns the login of one device. It needs the developer role in the teamspace and
// permission to open a terminal; a viewer gets a 403 whose code is no_write or no_exec.
func (f *Fleet) Credentials(ctx context.Context, name string) (*FleetDeviceCredentials, error) {
	c, _, err := decode[FleetDeviceCredentials](f.Do(ctx, "POST", "devices/"+url.PathEscape(name)+"/credentials", nil, map[string]any{}))
	if err != nil {
		return nil, f.platformErr(ctx, err)
	}
	return &c, nil
}

// CredentialsBulk returns the logins of many devices, in calls of at most MaxBulkCredentials names.
// Names the caller may not see, or that do not exist, come back in Missing.
func (f *Fleet) CredentialsBulk(ctx context.Context, names []string) (*FleetDeviceCredentialsBulk, error) {
	out := &FleetDeviceCredentialsBulk{}
	seen := map[string]bool{}
	var uniq []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}
	for len(uniq) > 0 {
		n := min(len(uniq), MaxBulkCredentials)
		chunk := uniq[:n]
		uniq = uniq[n:]
		res, _, err := decode[FleetDeviceCredentialsBulk](f.Do(ctx, "POST", "devices/credentials", nil, map[string]any{"names": chunk}))
		if err != nil {
			return nil, f.platformErr(ctx, err)
		}
		out.Devices = append(out.Devices, res.Devices...)
		out.Missing = append(out.Missing, res.Missing...)
	}
	return out, nil
}
