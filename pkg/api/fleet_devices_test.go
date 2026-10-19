package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/config"
)

// call is one request a fleetStub saw.
type call struct {
	Method, Path, Query, Body, UA string
}

// fleetStub is a fleet API: routes are "METHOD /path" below /api/device/fleet; each answers
// (status, body). Unknown routes are the 404 of a platform without them.
type fleetStub struct {
	mu     sync.Mutex
	routes map[string]func(call) (int, string)
	calls  []call
	srv    *httptest.Server
}

func newFleetStub(t *testing.T) (*fleetStub, *Fleet) {
	t.Helper()
	s := &fleetStub{routes: map[string]func(call) (int, string){}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/device/fleet")
		c := call{r.Method, path, r.URL.RawQuery, string(b), r.Header.Get("User-Agent")}
		s.mu.Lock()
		s.calls = append(s.calls, c)
		h := s.routes[r.Method+" "+path]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if h == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"name":"NotFoundError","message":"Route not found"}}`))
			return
		}
		status, body := h(c)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.srv.Close)
	f := &Fleet{
		Base:  s.srv.URL + "/api/device/fleet/teknoir-demo",
		Token: func(context.Context) (string, error) { return "tok", nil },
	}
	return s, f
}

func (s *fleetStub) on(route string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = func(call) (int, string) { return status, body }
}

func (s *fleetStub) seen() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call{}, s.calls...)
}

func TestTeamspaces(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("GET /teamspaces", 200, `[{"name":"teknoir-demo","displayName":"Teknoir Demo","access":"write"},{"name":"acme","access":"read"}]`)
	got, err := f.Teamspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "teknoir-demo" || got[0].DisplayName != "Teknoir Demo" || got[0].Access != AccessWrite || got[1].Access != AccessRead {
		t.Errorf("got %+v", got)
	}
	c := s.seen()[0]
	if c.Method != "GET" || c.Path != "/teamspaces" {
		t.Errorf("call %+v", c)
	}
}

func TestTeamspacesRootFromBase(t *testing.T) {
	f := &Fleet{Base: "https://teknoir.cloud/api/device/fleet/teknoir-demo"}
	if f.rootURL() != "https://teknoir.cloud/api/device/fleet" {
		t.Errorf("root %s", f.rootURL())
	}
	n := NewFleet("teknoir.cloud", "my ns")
	if n.Root != "https://teknoir.cloud/api/device/fleet" || n.Base != "https://teknoir.cloud/api/device/fleet/my%20ns" {
		t.Errorf("%+v", n)
	}
}

func TestTeamspaces404MeansUnsupportedPlatform(t *testing.T) {
	_, f := newFleetStub(t)
	Version = "v1.2.3"
	defer func() { Version = "dev" }()
	_, err := f.Teamspaces(context.Background())
	var up *UnsupportedPlatformError
	if !errors.As(err, &up) {
		t.Fatalf("want *UnsupportedPlatformError, got %T %v", err, err)
	}
	for _, want := range []string{"does not support tnctl v1.2.3 yet", "Use tnctl " + LastCatalogVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %s", want, err)
		}
	}
}

const deviceJSON = `{"name":"vision-01","namespace":"teknoir-demo","labels":{"site":"karlstad"},"virtual":false,
"tunnel":{"kind":"wireguard","label":"wireguard","wireguard":{"enabled":true,"annotation":"enabled","state":"active","connected":true,"active":true},"legacy":{"active":true,"port":"30022"}},
"services":[{"id":"grafana","kind":"internal","target":"grafana.monitoring","port":3000,"protocol":"http","web":true}],
"state":{"cpu":0.25,"memory":0.5,"storage":0.75,"netRx":1.5,"netTx":2.5,"alerts":[{"name":"HighCPU","state":"firing","count":2},{"name":"DiskFull","state":"pending","count":1}],"alertCount":3,
"apps":{"grafana":{"running":true,"restarts":0,"ready":"1/2","cpu":0.1,"memory":0.2}},"appCount":1},
"config":{"hash":"abc123","lastChange":"2026-10-19T08:00:00Z"},"org":{"unit":"lab","path":[{"id":"se","name":"Sweden"},{"id":"lab","name":"Karlstad lab"}]}}`

func TestDeviceAndDevices(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("GET /teknoir-demo/devices/vision-01", 200, deviceJSON)
	s.on("GET /teknoir-demo/devices", 200, "["+deviceJSON+"]")
	d, err := f.Device(context.Background(), "vision-01")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "vision-01" || !d.WireGuardActive() || !d.LegacyTunnelActive() || d.Tunnel.Legacy.Port != "30022" || d.TunnelLabel() != "wireguard" {
		t.Errorf("tunnel: %+v", d.Tunnel)
	}
	if len(d.Services) != 1 || d.Services[0].ID != "grafana" || !d.Services[0].Web || d.Services[0].Port != 3000 {
		t.Errorf("services: %+v", d.Services)
	}
	st := d.StateOrZero()
	if st.CPU != 0.25 || st.NetRx != 1.5 || st.AlertTotal() != 3 || st.AppTotal() != 1 || st.Apps["grafana"].Ready != "1/2" {
		t.Errorf("state: %+v", st)
	}
	if d.ConfigHash() != "abc123" || d.LastConfigChange() != "2026-10-19T08:00:00Z" || d.Org.Unit != "lab" {
		t.Errorf("config/org: %+v %+v", d.Config, d.Org)
	}
	all, err := f.Devices(context.Background(), nil)
	if err != nil || len(all) != 1 {
		t.Fatalf("%v %v", all, err)
	}
	names, _ := f.DeviceNames(context.Background())
	if len(names) != 1 || names[0] != "vision-01" {
		t.Errorf("names %v", names)
	}
	for _, c := range s.seen() {
		if c.Query != "" {
			t.Errorf("no filter, no query: %+v", c)
		}
	}
}

func TestDeviceWithoutStateOrTunnel(t *testing.T) {
	var d FleetDevice
	if err := json.Unmarshal([]byte(`{"name":"x","namespace":"n"}`), &d); err != nil {
		t.Fatal(err)
	}
	st := d.StateOrZero()
	if st.CPU != 0 || st.AlertTotal() != 0 || st.AppTotal() != 0 || d.ConfigHash() != "" || d.LastConfigChange() != "" || d.TunnelLabel() != "off" || d.WireGuardActive() {
		t.Errorf("zero value: %+v %+v", st, d)
	}
	// counts fall back to the lists when the server did not send them
	st = FleetDeviceState{Alerts: []FleetDeviceAlert{{Count: 2}, {Count: 1}}, Apps: map[string]FleetDeviceApp{"a": {}, "b": {}}}
	if st.AlertTotal() != 3 || st.AppTotal() != 2 {
		t.Errorf("fallback counts: %d %d", st.AlertTotal(), st.AppTotal())
	}
}

func TestDevicesLabelFilter(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("GET /teknoir-demo/devices", 200, "[]")
	if _, err := f.Devices(context.Background(), map[string]string{"site": "karlstad", "env": "a&b=c"}); err != nil {
		t.Fatal(err)
	}
	// sorted by key, values escaped
	if q := s.seen()[0].Query; q != "label=env%3Da%26b%3Dc&label=site%3Dkarlstad" {
		t.Errorf("query %q", q)
	}
	many := map[string]string{}
	for i := 0; i < MaxLabelFilters+1; i++ {
		many[fmt.Sprintf("k%d", i)] = "v"
	}
	if _, err := f.Devices(context.Background(), many); err == nil || !strings.Contains(err.Error(), "at most 20 label filters") {
		t.Errorf("too many labels accepted: %v", err)
	}
}

func TestDeviceNotFoundIsNot404OfPlatform(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("GET /teamspaces", 200, "[]")
	s.on("GET /teknoir-demo/devices/ghost", 404, `{"error":{"name":"NotFoundError","message":"No device called ghost"}}`)
	_, err := f.Device(context.Background(), "ghost")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 404 || !strings.Contains(ae.Message, "ghost") {
		t.Fatalf("want the device's 404, got %T %v", err, err)
	}
	// the platform has the fleet API (teamspaces answers): one probe, no upgrade hint
	var up *UnsupportedPlatformError
	if errors.As(err, &up) {
		t.Error("a missing device must not look like an old platform")
	}
}

func TestOldPlatformGetsUpgradeHintFromEveryDeviceRoute(t *testing.T) {
	_, f := newFleetStub(t) // no routes at all: the classic Backstage
	ctx := context.Background()
	_, e1 := f.Devices(ctx, nil)
	_, e2 := f.Device(ctx, "d")
	_, e3 := f.Credentials(ctx, "d")
	_, e4 := f.CredentialsBulk(ctx, []string{"d"})
	for i, err := range []error{e1, e2, e3, e4} {
		var up *UnsupportedPlatformError
		if !errors.As(err, &up) {
			t.Errorf("route %d: %T %v", i, err, err)
		}
	}
}

func TestCredentialsSingle(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/vision-01/credentials", 200,
		`{"name":"vision-01","namespace":"teknoir-demo","username":"dGVrbm9pcg==","password":"cHc=","rsaPrivate":"UFJJVkFURQ==","rsaPublic":"UFVC","publicSshKey":"c3NoLXJzYQ=="}`)
	c, err := f.Credentials(context.Background(), "vision-01")
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "dGVrbm9pcg==" || c.Password != "cHc=" || c.RSAPrivate != "UFJJVkFURQ==" || c.RSAPublic != "UFVC" || c.PublicSSHKey != "c3NoLXJzYQ==" {
		t.Errorf("got %+v", c)
	}
	call := s.seen()[0]
	if call.Method != "POST" || call.Body != "{}" {
		t.Errorf("call %+v", call)
	}
	// a device with no key pair yet: the fields are absent
	s.on("POST /teknoir-demo/devices/new/credentials", 200, `{"name":"new","namespace":"teknoir-demo","username":"dQ=="}`)
	c, err = f.Credentials(context.Background(), "new")
	if err != nil || c.RSAPrivate != "" || c.Username != "dQ==" {
		t.Errorf("%+v %v", c, err)
	}
}

func TestCredentialsErrorsCarryTheReason(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/a/credentials", 403, `{"code":"no_write","message":"The login keys of devices need the developer role in teamspace teknoir-demo.","error":{"name":"NotAllowedError","message":"x"}}`)
	s.on("POST /teknoir-demo/devices/b/credentials", 429, `{"code":"rate_limited","message":"slow down"}`)
	_, err := f.Credentials(context.Background(), "a")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "no_write" || !ae.IsAuth() {
		t.Fatalf("%+v", err)
	}
	if !strings.Contains(ae.Error(), "not allowed for this account or teamspace: The login keys of devices need the developer role") {
		t.Errorf("message %q", ae.Error())
	}
	if strings.Contains(ae.Error(), "tnctl login") {
		t.Errorf("a permission error must not tell the user to log in again: %q", ae.Error())
	}
	s.mu.Lock()
	s.routes["POST /teknoir-demo/devices/b/credentials"] = func(call) (int, string) { return 429, `{"code":"rate_limited","message":"slow down"}` }
	s.mu.Unlock()
	_, err = f.Credentials(context.Background(), "b")
	if !errors.As(err, &ae) || ae.Status != 429 || ae.Code != "rate_limited" {
		t.Errorf("%+v", err)
	}
}

func TestCredentialsBulkChunksAndDeduplicates(t *testing.T) {
	s, f := newFleetStub(t)
	var sizes []int
	s.mu.Lock()
	s.routes["POST /teknoir-demo/devices/credentials"] = func(c call) (int, string) {
		var req struct{ Names []string }
		_ = json.Unmarshal([]byte(c.Body), &req)
		sizes = append(sizes, len(req.Names))
		var out FleetDeviceCredentialsBulk
		for _, n := range req.Names {
			if n == "d0" {
				out.Missing = append(out.Missing, n)
				continue
			}
			out.Devices = append(out.Devices, FleetDeviceCredentials{Name: n, Namespace: "teknoir-demo", Username: "dQ=="})
		}
		b, _ := json.Marshal(out)
		return 200, string(b)
	}
	s.mu.Unlock()
	var names []string
	for i := 0; i < 450; i++ {
		names = append(names, fmt.Sprintf("d%d", i))
	}
	names = append(names, "d1", "d2") // duplicates are sent once
	res, err := f.CredentialsBulk(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[200 200 50]" {
		t.Errorf("chunks %v", sizes)
	}
	if len(res.Devices) != 449 || len(res.Missing) != 1 || res.Missing[0] != "d0" {
		t.Errorf("%d devices, missing %v", len(res.Devices), res.Missing)
	}
	for _, c := range s.seen() {
		if c.Method != "POST" || c.Path != "/teknoir-demo/devices/credentials" {
			t.Errorf("call %+v", c)
		}
	}
}

func TestCredentialsNeverAppearInDebugOutput(t *testing.T) {
	s, f := newFleetStub(t)
	const secret = "U0VDUkVULVBSSVZBVEUtS0VZ"
	s.on("POST /teknoir-demo/devices/vision-01/credentials", 200, `{"name":"vision-01","namespace":"teknoir-demo","rsaPrivate":"`+secret+`"}`)
	s.on("GET /teknoir-demo/devices/vision-01", 200, deviceJSON)

	config.Debug = true
	defer func() { config.Debug = false }()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	_, err1 := f.Credentials(context.Background(), "vision-01")
	_, err2 := f.Device(context.Background(), "vision-01")
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Error("the debug output shows the device login")
	}
	if !strings.Contains(out, "not shown: device logins") {
		t.Errorf("debug output should say the body is withheld:\n%s", out)
	}
	if !strings.Contains(out, `"name":"vision-01"`) {
		t.Errorf("other bodies are still shown in debug output:\n%s", out)
	}
}

// tnctl reads teamspaces, devices and device logins from the fleet API only: the Backstage catalog
// is closed to the Keycloak token (access restrictions of the platform).
func TestNoCatalogCallsInTheSource(t *testing.T) {
	root := ".." + string(os.PathSeparator) + ".."
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "/api/"+"catalog") {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Errorf("code that calls the catalog: %v", hits)
	}
}
