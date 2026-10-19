package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// ---------- fixtures: what the fleet API answers ----------

// The same three devices as the golden file testdata/devices_output.golden, which the catalog
// based tnctl v0.0.9-beta produced for them: vision-01 (WireGuard and legacy tunnel up, alerts,
// apps), legacy-02 (legacy tunnel only) and new-03 (WireGuard enrolling, nothing reported yet).
const (
	devVision = `{"name":"vision-01","namespace":"teknoir-demo","labels":{"site":"karlstad","env":"prod"},
"tunnel":{"kind":"wireguard","label":"wireguard","wireguard":{"enabled":true,"annotation":"enabled","state":"active","connected":true,"active":true},"legacy":{"active":true,"port":"30022"}},
"services":[{"id":"grafana","kind":"internal","target":"grafana.monitoring","port":3000,"protocol":"http","web":true},{"id":"ssh","kind":"external","protocol":"tcp"}],
"state":{"cpu":0.25,"memory":0.5,"storage":0.75,"netRx":1.5,"netTx":2.5,
 "alerts":[{"name":"HighCPU","state":"firing","count":2},{"name":"DiskFull","state":"pending","count":1}],"alertCount":3,
 "apps":{"grafana":{"running":true,"restarts":0,"ready":"1/2","cpu":0.1,"memory":0.2},"alpha":{"running":false,"restarts":3,"ready":"0/1","cpu":0,"memory":0.05}},"appCount":2},
"config":{"hash":"abc123","lastChange":"2026-10-19T08:00:00Z"}}`
	devLegacy = `{"name":"legacy-02","namespace":"teknoir-demo","labels":{"site":"stockholm"},
"tunnel":{"kind":"legacy","label":"legacy","wireguard":{"enabled":false,"active":false},"legacy":{"active":true,"port":"31001"}},
"state":{"cpu":0.0123}}`
	devNew = `{"name":"new-03","namespace":"teknoir-demo",
"tunnel":{"kind":"pending","label":"enrolling","wireguard":{"enabled":true,"annotation":"enabled","state":"enrolling","active":false},"legacy":{"active":false}}}`
	devOff = `{"name":"off-04","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}}}`
)

func b64s(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func credsJSON(name string) string {
	return fmt.Sprintf(`{"name":%q,"namespace":"teknoir-demo","username":%q,"password":%q,"rsaPrivate":%q,"rsaPublic":%q,"publicSshKey":%q}`,
		name, b64s("teknoir"), b64s("SUDO-PASSWORD"), b64s("-----BEGIN PRIVATE KEY-----\nPRIVATE-KEY-SENTINEL\n-----END PRIVATE KEY-----\n"),
		b64s("ssh-rsa PUBLIC"), b64s("ssh-rsa AAAA tnctl"))
}

const noWrite = `{"code":"no_write","message":"The login keys of devices need the developer role in teamspace teknoir-demo. You can view devices but not use their keys. Ask a teamspace admin for write access.","error":{"name":"NotAllowedError","message":"x"}}`

// fleetPlatform serves the devices and teamspaces of a platform with the new fleet routes.
func fleetPlatform(t *testing.T) *fakePlatform {
	t.Helper()
	p := newFakePlatform(t)
	p.on("GET", "/api/device/fleet/teamspaces", 200, `[{"name":"acme","access":"read"},{"name":"teknoir-demo","displayName":"Teknoir Demo","access":"write"}]`)
	p.on("GET", "/devices", 200, "["+devVision+","+devLegacy+","+devNew+","+devOff+"]")
	p.on("GET", "/devices/vision-01", 200, devVision)
	p.on("GET", "/devices/legacy-02", 200, devLegacy)
	p.on("GET", "/devices/new-03", 200, devNew)
	p.on("GET", "/devices/off-04", 200, devOff)
	for _, n := range []string{"vision-01", "legacy-02", "new-03", "off-04"} {
		p.on("POST", "/devices/"+n+"/credentials", 200, credsJSON(n))
	}
	return p
}

// useDefaultEnv makes the commands that are not built from an env (ssh, port-forward, ...) use e.
func useDefaultEnv(t *testing.T, e *cliEnv) {
	t.Helper()
	old := *defaultEnv
	defaultEnv.fleet, defaultEnv.domainFleet = e.fleet, e.domainFleet
	t.Cleanup(func() { *defaultEnv = old })
}

// runGlobal runs a command that is a package level variable (ssh, bootstrap, ...) against e.
func runGlobal(t *testing.T, e *cliEnv, args ...string) (string, string, error) {
	t.Helper()
	useDefaultEnv(t, e)
	return runCLIWith(t, e, []*cobra.Command{sshCmd, portForwardCmd, socksProxyCmd, bootstrapCmd, ansibleInventoryCmd}, args...)
}

// capturePterm collects what the table commands print (pterm writes to its own output).
func capturePterm(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	pterm.SetDefaultOutput(&buf)
	pterm.DisableStyling()
	t.Cleanup(func() {
		pterm.SetDefaultOutput(os.Stdout)
		pterm.EnableStyling()
	})
	return &buf
}

// fakeSSH puts an `ssh` on PATH that records its arguments and, for -i, the key it was given.
// It returns the file the arguments are written to (one per line).
func fakeSSH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "ssh.args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + out + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(envWGHub, "")
	t.Setenv(envWGTurnServer, "")
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// ---------- ns ----------

func TestNsListShowsTheTeamspacesOfTheAccount(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "ns", "--list")
	if err != nil {
		t.Fatal(err)
	}
	want := "NAME          ACCESS  DISPLAY NAME\nacme          read    \nteknoir-demo  write   Teknoir Demo\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
	if got := p.seen("GET", "/api/device/fleet/teamspaces"); len(got) != 1 {
		t.Errorf("teamspace requests: %v", got)
	}
}

func TestNsNoTeamspaces(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", "/api/device/fleet/teamspaces", 200, `[]`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "ns", "--list")
	if err == nil || !strings.Contains(err.Error(), "no namespaces found") {
		t.Errorf("err %v", err)
	}
}

// ---------- devices, device: golden output ----------

func TestDevicesAndDeviceOutputIsUnchanged(t *testing.T) {
	p := fleetPlatform(t)
	p.on("GET", "/devices", 200, "["+devVision+","+devLegacy+","+devNew+"]")
	e, _ := testEnv(t, p, nil)
	buf := capturePterm(t)
	for _, args := range [][]string{{"devices"}, {"device", "vision-01"}, {"device", "new-03"}} {
		if _, _, err := runCLI(t, e, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// testdata/devices_output.golden is what tnctl v0.0.9-beta printed for the same devices from the
	// catalog (applications are sorted by name now; they came in random order).
	want, err := os.ReadFile("testdata/devices_output.golden")
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("output differs from the golden file.\ngot:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestDevicesLabelFilterIsAskedOfThePlatform(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	capturePterm(t)
	if _, _, err := runCLI(t, e, "devices", "site=karlstad", "env=prod"); err != nil {
		t.Fatal(err)
	}
	got := p.seen("GET", "/devices")
	if len(got) != 1 || got[0].Query != "label=env%3Dprod&label=site%3Dkarlstad" {
		t.Errorf("requests %+v", got)
	}
	if _, _, err := runCLI(t, e, "devices", "site"); err == nil || !strings.Contains(err.Error(), "Expected key=value") {
		t.Errorf("bad label: %v", err)
	}
	p.on("GET", "/devices", 200, `[]`)
	_, _, err := runCLI(t, e, "devices", "site=nowhere")
	if err == nil || !strings.Contains(err.Error(), "no devices found in namespace teknoir-demo with the specified labels") {
		t.Errorf("empty: %v", err)
	}
	_, _, err = runCLI(t, e, "devices")
	if err == nil || !strings.Contains(err.Error(), "no devices found in namespace teknoir-demo") || strings.Contains(err.Error(), "labels") {
		t.Errorf("empty, no labels: %v", err)
	}
}

func TestDeviceNotFound(t *testing.T) {
	p := fleetPlatform(t)
	p.on("GET", "/devices/ghost", 404, `{"error":{"name":"NotFoundError","message":"No device called ghost in teamspace teknoir-demo."}}`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "device", "ghost")
	if err == nil || exitCodeOf(err) != codeNotFound || !strings.Contains(err.Error(), "No device called ghost") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// ---------- auth errors: 302, 401, 403 on every command that reads the platform ----------

func TestAuthErrorsOfTheMigratedCommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		// reads says which routes the command calls
		route string
	}{
		{"ns", []string{"ns", "--list"}, "/api/device/fleet/teamspaces"},
		{"devices", []string{"devices"}, "/devices"},
		{"device", []string{"device", "vision-01"}, "/devices/vision-01"},
	}
	for _, c := range cases {
		t.Run(c.name+"/302", func(t *testing.T) {
			p := fleetPlatform(t)
			e, _ := testEnv(t, p, nil)
			// a token the gateway does not accept is a redirect to the login
			e.fleet = wrongToken(p, e.fleet)
			e.domainFleet = wrongTokenRoot(p, e.domainFleet)
			_, _, err := runCLI(t, e, c.args...)
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Status != 302 || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "run tnctl login") {
				t.Errorf("exit %d: %v", exitCodeOf(err), err)
			}
		})
		t.Run(c.name+"/401", func(t *testing.T) {
			p := fleetPlatform(t)
			p.on("GET", c.route, 401, `{"error":{"name":"AuthenticationError","message":"Token expired"}}`)
			e, _ := testEnv(t, p, nil)
			_, _, err := runCLI(t, e, c.args...)
			if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "not logged in or token expired: run tnctl login") || !strings.Contains(err.Error(), "Token expired") {
				t.Errorf("exit %d: %v", exitCodeOf(err), err)
			}
		})
		t.Run(c.name+"/403", func(t *testing.T) {
			p := fleetPlatform(t)
			p.on("GET", c.route, 403, `{"error":{"name":"NotAllowedError","message":"You do not have access to devices in teamspace teknoir-demo."}}`)
			e, _ := testEnv(t, p, nil)
			_, _, err := runCLI(t, e, c.args...)
			if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "not allowed for this account or teamspace: You do not have access to devices in teamspace teknoir-demo.") {
				t.Errorf("exit %d: %v", exitCodeOf(err), err)
			}
			if strings.Contains(err.Error(), "tnctl login") {
				t.Errorf("logging in again does not help a 403: %v", err)
			}
		})
	}
}

func wrongToken(_ *fakePlatform, f func() (*api.Fleet, string, string, error)) func() (*api.Fleet, string, string, error) {
	return func() (*api.Fleet, string, string, error) {
		fl, d, ns, err := f()
		if fl != nil {
			fl.Token = func(context.Context) (string, error) { return "expired", nil }
		}
		return fl, d, ns, err
	}
}

func wrongTokenRoot(_ *fakePlatform, f func() (*api.Fleet, string, error)) func() (*api.Fleet, string, error) {
	return func() (*api.Fleet, string, error) {
		fl, d, err := f()
		if fl != nil {
			fl.Token = func(context.Context) (string, error) { return "expired", nil }
		}
		return fl, d, err
	}
}

// ---------- a platform without the fleet API ----------

func TestOldPlatformSaysWhichTnctlToUse(t *testing.T) {
	p := newFakePlatform(t) // knows no routes: the classic Backstage
	e, _ := testEnv(t, p, nil)
	for _, args := range [][]string{{"ns", "--list"}, {"devices"}, {"device", "vision-01"}} {
		_, _, err := runCLI(t, e, args...)
		var up *api.UnsupportedPlatformError
		if !errors.As(err, &up) {
			t.Errorf("%v: %T %v", args, err, err)
			continue
		}
		if exitCodeOf(err) != codeUsage {
			t.Errorf("%v: exit code %d", args, exitCodeOf(err))
		}
		for _, want := range []string{"does not support tnctl", "use tnctl v0.0.9-beta"} {
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
				t.Errorf("%v: message lacks %q: %v", args, want, err)
			}
		}
	}
}

// ---------- User-Agent ----------

func TestEveryRequestCarriesTheUserAgent(t *testing.T) {
	p := fleetPlatform(t)
	e, _ := testEnv(t, p, nil)
	capturePterm(t)
	for _, args := range [][]string{{"ns", "--list"}, {"devices"}, {"device", "vision-01"}} {
		if _, _, err := runCLI(t, e, args...); err != nil {
			t.Fatal(err)
		}
	}
	want := fmt.Sprintf("tnctl/%s (%s/%s)", api.Version, runtime.GOOS, runtime.GOARCH)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) < 3 {
		t.Fatalf("requests: %v", p.reqs)
	}
	for _, r := range p.reqs {
		if r.UA != want {
			t.Errorf("%s %s: User-Agent %q, want %q", r.Method, r.Full, r.UA, want)
		}
	}
}
