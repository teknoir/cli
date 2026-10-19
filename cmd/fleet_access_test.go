package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

// The commands that open a session to a device read the device (no secrets) first and ask for its
// login only when it can be reached; the login route answers developers who may open a terminal
// and says why to everybody else.

const secretMarkers = "PRIVATE-KEY-SENTINEL SUDO-PASSWORD"

// noSecrets fails when any of the secrets of credsJSON shows in s.
func noSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, m := range strings.Fields(secretMarkers) {
		if strings.Contains(s, m) || strings.Contains(s, b64s(m)) {
			t.Errorf("%s shows a secret (%s): %s", what, m, s)
		}
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func credentialRequests(p *fakePlatform) int {
	n := 0
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reqs {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/credentials") {
			n++
		}
	}
	return n
}

// ---------- ssh ----------

func TestSSHOverWireGuardAsksForTheLoginAfterTheDevice(t *testing.T) {
	out := fakeSSH(t)
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	_ = captureStdout(t, func() {
		if _, _, err := runGlobal(t, e, "ssh", "vision-01"); err != nil {
			t.Fatal(err)
		}
	})
	p.mu.Lock()
	var order []string
	for _, r := range p.reqs {
		order = append(order, r.Method+" "+r.Path)
	}
	p.mu.Unlock()
	if strings.Join(order, ",") != "GET /devices/vision-01,POST /devices/vision-01/credentials" {
		t.Errorf("requests %v", order)
	}
	args := strings.Join(readLines(t, out), "\x00")
	for _, want := range []string{"teknoir@vision-01", "HostKeyAlias=vision-01.teknoir-demo.ztna.teknoir"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args lack %q: %q", want, args)
		}
	}
	noSecrets(t, "ssh arguments", args)
}

func TestSSHOverLegacyTunnel(t *testing.T) {
	out := fakeSSH(t)
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	stdout := captureStdout(t, func() {
		if _, errs, err := runGlobal(t, e, "ssh", "legacy-02"); err != nil {
			t.Fatal(err, errs)
		}
	})
	args := readLines(t, out)
	join := strings.Join(args, "\x00")
	for _, want := range []string{"-p\x0031001", "teknoir@127.0.0.1", "deadend-teknoir-demo.teknoir.cloud"} {
		if !strings.Contains(join, want) {
			t.Errorf("ssh args lack %q: %q", want, args)
		}
	}
	noSecrets(t, "banner", stdout)
}

func TestSSHBannerKeepsThePasswordOutOfTheScrollback(t *testing.T) {
	banner := func(show bool) string {
		old := sshShowPassword
		sshShowPassword = show
		defer func() { sshShowPassword = old }()
		return captureStdout(t, func() { printRemoteShellBanner("dev-1", "SUDO-PASSWORD") })
	}
	if got := banner(false); strings.Contains(got, "SUDO-PASSWORD") || !strings.Contains(got, "--show-password") {
		t.Errorf("default banner: %q", got)
	}
	if got := banner(true); !strings.Contains(got, "SSH Password: SUDO-PASSWORD") {
		t.Errorf("--show-password banner: %q", got)
	}
	if got := captureStdout(t, func() { printRemoteShellBanner("dev-1", "") }); strings.Contains(got, "password") {
		t.Errorf("no password, no hint: %q", got)
	}
}

func TestSSHViewerIsToldToAskForWriteAccess(t *testing.T) {
	out := fakeSSH(t)
	p := fleetPlatform(t)
	p.on("POST", "/devices/vision-01/credentials", 403, noWrite)
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "ssh", "vision-01")
	if err == nil || exitCodeOf(err) != codeAuth {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	for _, want := range []string{"not allowed for this account or teamspace", "need the developer role in teamspace teknoir-demo", "Ask a teamspace admin for write access"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "tnctl login") {
		t.Errorf("a permission error must not say 'log in': %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("ssh ran although the login was refused")
	}
}

func TestSSHWithoutAnActiveTunnelNeverAsksForTheLogin(t *testing.T) {
	fakeSSH(t)
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "ssh", "off-04")
	if err == nil || !strings.Contains(err.Error(), "Tunneling is not active for off-04") {
		t.Fatalf("err %v", err)
	}
	if n := credentialRequests(p); n != 0 {
		t.Errorf("%d credential requests for a device that cannot be reached", n)
	}
}

func TestSSHFlagsOfWireGuardOnlyRefuseBeforeTheLogin(t *testing.T) {
	fakeSSH(t)
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	t.Cleanup(func() { sshNoP2P = false })
	_, _, err := runGlobal(t, e, "ssh", "legacy-02", "--no-p2p")
	if err == nil || !strings.Contains(err.Error(), "only apply to a WireGuard tunnel") || credentialRequests(p) != 0 {
		t.Errorf("err %v, credential requests %d", err, credentialRequests(p))
	}
}

func TestSSHOnAnOldPlatform(t *testing.T) {
	fakeSSH(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "ssh", "vision-01")
	var up *api.UnsupportedPlatformError
	if !errors.As(err, &up) {
		t.Errorf("%T %v", err, err)
	}
}

// ---------- exec ----------

func TestExecChannelNeedsAnActiveTunnelAndTheLogin(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	fl, _, _, _ := e.fleet()
	ctx := context.Background()

	// legacy-only device: no channel, no login asked
	if _, err := newWGExecWith(ctx, fl, "teknoir.cloud", "teknoir-demo", "legacy-02"); err == nil || !strings.Contains(err.Error(), "has no active WireGuard tunnel") {
		t.Errorf("legacy: %v", err)
	}
	if _, err := newWGExecWith(ctx, fl, "teknoir.cloud", "teknoir-demo", "off-04"); err == nil {
		t.Error("off device accepted")
	}
	if n := credentialRequests(p); n != 0 {
		t.Errorf("%d credential requests before a usable tunnel", n)
	}

	// unknown device: the platform's 404
	p.on("GET", "/devices/ghost", 404, `{"error":{"name":"NotFoundError","message":"No device called ghost."}}`)
	if _, err := newWGExecWith(ctx, fl, "teknoir.cloud", "teknoir-demo", "ghost"); exitCodeOf(err) != codeNotFound {
		t.Errorf("ghost: exit %d %v", exitCodeOf(err), err)
	}

	// viewer
	p.on("POST", "/devices/vision-01/credentials", 403, noWrite)
	_, err := newWGExecWith(ctx, fl, "teknoir.cloud", "teknoir-demo", "vision-01")
	if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "developer role") {
		t.Errorf("viewer: exit %d %v", exitCodeOf(err), err)
	}

	// developer: a channel, with the key in a private directory
	p.on("POST", "/devices/vision-01/credentials", 200, credsJSON("vision-01"))
	ex, err := newWGExecWith(ctx, fl, "teknoir.cloud", "teknoir-demo", "vision-01")
	if err != nil {
		t.Fatal(err)
	}
	w := ex.(*wgExec)
	key, err := os.ReadFile(filepath.Join(w.sess.tmpDir, "id"))
	if err != nil || !strings.Contains(string(key), "PRIVATE-KEY-SENTINEL") {
		t.Errorf("key file: %q %v", key, err)
	}
	ex.Close()
	if _, err := os.Stat(w.sess.tmpDir); !os.IsNotExist(err) {
		t.Error("key directory not removed")
	}
}

// ---------- port-forward, socks-proxy ----------

func TestPortForwardListsServicesWithoutTheLogin(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	var runErr error
	stdout := captureStdout(t, func() { _, _, runErr = runGlobal(t, e, "port-forward", "vision-01") })
	if runErr != nil {
		t.Fatal(runErr)
	}
	for _, want := range []string{"Services on vision-01", "grafana", "ssh"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("service list lacks %q:\n%s", want, stdout)
		}
	}
	// WireGuard needs no login from the platform: a viewer may list and forward services
	if n := credentialRequests(p); n != 0 {
		t.Errorf("%d credential requests to list services", n)
	}
}

func TestPortForwardInvalidServicesAnnotation(t *testing.T) {
	p := fleetPlatform(t)
	p.on("GET", "/devices/vision-01", 200, strings.Replace(devVision, `"services":[`, `"servicesInvalid":true,"x":[`, 1))
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "port-forward", "vision-01")
	if err == nil || !strings.Contains(err.Error(), "services annotation of the device is not valid") {
		t.Errorf("err %v", err)
	}
}

func TestPortForwardOverLegacyTunnel(t *testing.T) {
	out := fakeSSH(t)
	t.Cleanup(func() { localPort, targetAddr = 0, "" })
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	stdout := captureStdout(t, func() {
		if _, _, err := runGlobal(t, e, "port-forward", "legacy-02", "8080:localhost:80"); err != nil {
			t.Fatal(err)
		}
	})
	join := strings.Join(readLines(t, out), "\x00")
	for _, want := range []string{"-p\x0031001", "-L\x008080:localhost:80", "teknoir@127.0.0.1"} {
		if !strings.Contains(join, want) {
			t.Errorf("ssh args lack %q: %q", want, join)
		}
	}
	noSecrets(t, "output", stdout+join)
}

func TestPortForwardOverLegacyTunnelNeedsTheLogin(t *testing.T) {
	out := fakeSSH(t)
	t.Cleanup(func() { localPort, targetAddr = 0, "" })
	p := fleetPlatform(t)
	p.on("POST", "/devices/legacy-02/credentials", 403, noWrite)
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "port-forward", "legacy-02", "8080:localhost:80")
	if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "developer role") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("ssh ran although the login was refused")
	}
}

func TestSocksProxy(t *testing.T) {
	out := fakeSSH(t)
	t.Cleanup(func() { socksPort = 0 })
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	_ = captureStdout(t, func() {
		if _, _, err := runGlobal(t, e, "socks-proxy", "legacy-02", "1080"); err != nil {
			t.Fatal(err)
		}
	})
	join := strings.Join(readLines(t, out), "\x00")
	for _, want := range []string{"-D\x001080", "-p\x0031001", "teknoir@127.0.0.1"} {
		if !strings.Contains(join, want) {
			t.Errorf("ssh args lack %q: %q", want, join)
		}
	}
	// a WireGuard device offers no SOCKS and no login is asked for
	before := credentialRequests(p)
	_, _, err := runGlobal(t, e, "socks-proxy", "vision-01", "1081")
	if err == nil || !strings.Contains(err.Error(), "SOCKS is not available over WireGuard") || credentialRequests(p) != before {
		t.Errorf("err %v", err)
	}
}

// ---------- bootstrap ----------

func TestBootstrapUsesTheLoginRoute(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	dir := t.TempDir()
	t.Chdir(dir)
	var runErr error
	_ = captureStdout(t, func() { _, _, runErr = runGlobal(t, e, "bootstrap", "vision-01") })
	if runErr != nil {
		t.Fatal(runErr)
	}
	script, err := os.ReadFile(filepath.Join(dir, "bootstrap_agent_vision-01.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "PRIVATE-KEY-SENTINEL") || !strings.Contains(string(script), "teknoir-demo") {
		t.Errorf("the script lacks the device login:\n%.400s", script)
	}
	if n := len(p.seen("POST", "/devices/vision-01/credentials")); n != 1 {
		t.Errorf("%d credential requests", n)
	}
}

func TestBootstrapKeyPairNotThereYet(t *testing.T) {
	p := fleetPlatform(t)
	p.on("POST", "/devices/vision-01/credentials", 200, `{"name":"vision-01","namespace":"teknoir-demo","username":"`+b64s("teknoir")+`"}`)
	e, _ := testEnv(t, p, nil)
	dir := t.TempDir()
	t.Chdir(dir)
	_, _, err := runGlobal(t, e, "bootstrap", "vision-01")
	if err == nil || !strings.Contains(err.Error(), "has no key pair yet") || !strings.Contains(err.Error(), "run the command again shortly") {
		t.Errorf("err %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "bootstrap_*")); len(files) != 0 {
		t.Errorf("scripts written without a key pair: %v", files)
	}
}

func TestBootstrapViewer(t *testing.T) {
	p := fleetPlatform(t)
	p.on("POST", "/devices/vision-01/credentials", 403, noWrite)
	e, _ := testEnv(t, p, nil)
	dir := t.TempDir()
	t.Chdir(dir)
	_, _, err := runGlobal(t, e, "bootstrap", "vision-01")
	if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "developer role") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "bootstrap_*")); len(files) != 0 {
		t.Errorf("scripts written: %v", files)
	}
}

// ---------- wg status: services ----------

func TestFleetServices(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	fl, _, _, _ := e.fleet()
	svcs, err := fleetServices(context.Background(), fl, "vision-01")
	if err != nil || len(svcs) != 2 || svcs[0].ID != "grafana" || !svcs[0].Web {
		t.Fatalf("%v %v", svcs, err)
	}
	if svcs, err := fleetServices(context.Background(), fl, "new-03"); err != nil || len(svcs) != 0 {
		t.Errorf("none published: %v %v", svcs, err)
	}
	p.on("GET", "/devices/ghost", 404, `{"error":{"name":"NotFoundError","message":"No device called ghost."}}`)
	if _, err := fleetServices(context.Background(), fl, "ghost"); exitCodeOf(err) != codeNotFound {
		t.Errorf("ghost: exit %d %v", exitCodeOf(err), err)
	}
}

// ---------- ansible inventory ----------

func bulkCredentials(t *testing.T, p *fakePlatform) {
	p.onFunc("POST", "/devices/credentials", func(r rec) (int, string) {
		var req struct{ Names []string }
		_ = json.Unmarshal([]byte(r.Body), &req)
		var devs []json.RawMessage
		var missing []string
		for _, n := range req.Names {
			switch n {
			case "vision-01", "legacy-02", "new-03", "off-04":
				devs = append(devs, json.RawMessage(credsJSON(n)))
			default:
				missing = append(missing, n)
			}
		}
		return 200, jsonOf(t, map[string]any{"devices": devs, "missing": missing})
	})
}

func TestAnsibleInventoryAsksForTheLoginsOfReachableDevicesInOneCall(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(envAnsibleTransport, "")
	t.Setenv(envWGHub, "")
	t.Cleanup(func() { ansibleList, ansibleHost = false, "" })
	p := fleetPlatform(t)
	bulkCredentials(t, p)
	e, _ := testEnv(t, p, nil)
	out, errs, err := runGlobal(t, e, "ansible-inventory", "--list")
	if err != nil {
		t.Fatal(err, errs)
	}
	var inv struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(inv.Meta.Hostvars) != 2 || inv.Meta.Hostvars["vision-01"]["ansible_teknoir_transport"] != "wireguard" ||
		inv.Meta.Hostvars["legacy-02"]["ansible_teknoir_transport"] != "tunnel" {
		t.Errorf("hostvars %v", inv.Meta.Hostvars)
	}
	reqs := p.seen("POST", "/devices/credentials")
	if len(reqs) != 1 {
		t.Fatalf("%d bulk requests", len(reqs))
	}
	var body struct{ Names []string }
	_ = json.Unmarshal([]byte(reqs[0].Body), &body)
	if strings.Join(body.Names, ",") != "vision-01,legacy-02" {
		t.Errorf("logins asked for %v: only devices that can be reached", body.Names)
	}
	_ = errs // the warnings about unreachable devices go to the process stderr (TestBuildInventoryTransports)
	// the secrets go to the key files only
	if strings.Contains(out, "PRIVATE-KEY-SENTINEL") {
		t.Error("the inventory prints a private key")
	}
}

func TestAnsibleInventoryViewer(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(envAnsibleTransport, "")
	t.Cleanup(func() { ansibleList, ansibleHost = false, "" })
	p := fleetPlatform(t)
	p.on("POST", "/devices/credentials", 403, noWrite)
	e, _ := testEnv(t, p, nil)
	_, _, err := runGlobal(t, e, "ansible-inventory", "--list")
	if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "developer role") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestAnsibleInventoryWithNothingToReachAsksForNoLogin(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(envAnsibleTransport, "")
	t.Cleanup(func() { ansibleList, ansibleHost = false, "" })
	p := fleetPlatform(t)
	p.on("GET", "/devices", 200, "["+devNew+","+devOff+"]")
	e, _ := testEnv(t, p, nil)
	out, _, err := runGlobal(t, e, "ansible-inventory", "--list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"hostvars": {}`) || credentialRequests(p) != 0 {
		t.Errorf("out %s, credential requests %d", out, credentialRequests(p))
	}
}
