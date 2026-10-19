package cmd

import (
	"encoding/base64"
	"github.com/teknoir/cli/pkg/api"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestQuoteArg(t *testing.T) {
	cases := []struct{ in, goos, want string }{
		{"/usr/bin/tnctl", "linux", "/usr/bin/tnctl"},
		{"/tmp/a b/tnctl", "linux", "'/tmp/a b/tnctl'"},
		{"it's", "linux", `'it'\''s'`},
		{"", "linux", "''"},
		{"a;b", "darwin", "'a;b'"},
		{`C:\Program Files\tnctl.exe`, "windows", `"C:\Program Files\tnctl.exe"`},
		{"plain-name.ns", "windows", "plain-name.ns"},
	}
	for _, c := range cases {
		if got := quoteArg(c.in, c.goos); got != c.want {
			t.Errorf("quoteArg(%q,%s) = %s, want %s", c.in, c.goos, got, c.want)
		}
	}
}

func TestBuildProxyCommand(t *testing.T) {
	a := proxyArgs{
		Exe: "/opt/tn ctl/tnctl", Domain: "teknoir.cloud", Namespace: "ns1", Device: "dev-1", Port: 22,
		KnownHostsFile: "/tmp/tnctl-wg-1/known_hosts", HubURL: "https://wg.teknoir.cloud",
	}
	got := buildProxyCommand(a, "linux")
	want := `'/opt/tn ctl/tnctl' wg-proxy --domain teknoir.cloud --namespace ns1 --device dev-1 --port 22 ` +
		`--known-hosts-file /tmp/tnctl-wg-1/known_hosts --hub-url https://wg.teknoir.cloud`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	a.TURNServer = "10.0.0.5:3478"
	a.ConfigFile = "/home/u/my conf.yaml"
	got = buildProxyCommand(a, "linux")
	for _, frag := range []string{"--config '/home/u/my conf.yaml' --domain", "--turn-server 10.0.0.5:3478 --hub-url"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in %s", frag, got)
		}
	}
}

func TestBuildProxyCommandEscapesPercent(t *testing.T) {
	a := proxyArgs{Exe: "/opt/100%/tnctl", Domain: "d", Namespace: "n", Device: "x", Port: 22, KnownHostsFile: "/tmp/%h/kh", HubURL: "https://wg.d"}
	got := buildProxyCommand(a, "linux")
	if strings.Contains(strings.ReplaceAll(got, "%%", ""), "%") {
		t.Fatalf("unescaped %% in %s", got)
	}
	if !strings.Contains(got, "/opt/100%%/tnctl") || !strings.Contains(got, "/tmp/%%h/kh") {
		t.Fatalf("percent not doubled: %s", got)
	}
}

// What ssh does with the option: expand %% to %, then hand it to sh -c. The
// shell must see exactly the words we meant, whatever the paths contain.
func TestBuildProxyCommandShellRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	a := proxyArgs{
		Exe: "/opt/we ird/it's 100%/tnctl", ConfigFile: `/c/$HOME "x" y.yaml`, Domain: "teknoir.cloud", Namespace: "ns1", Device: "dev-1", Port: 22,
		KnownHostsFile: "/tmp/a b/kh;rm -rf x", TURNServer: "h:1", HubURL: "https://wg.teknoir.cloud",
	}
	cmdline := strings.ReplaceAll(buildProxyCommand(a, "linux"), "%%", "%")
	out, err := exec.Command(sh, "-c", `printf '%s\n' `+strings.TrimPrefix(cmdline, "")).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := []string{
		a.Exe, "wg-proxy", "--config", a.ConfigFile, "--domain", "teknoir.cloud", "--namespace", "ns1", "--device", "dev-1",
		"--port", "22", "--known-hosts-file", a.KnownHostsFile, "--turn-server", "h:1", "--hub-url", "https://wg.teknoir.cloud",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shell saw\n%q\nwant\n%q", got, want)
	}
}

func TestBuildSSHArgs(t *testing.T) {
	got := buildSSHArgs("PROXY", "/t/known_hosts", "dev-1.ns1.ztna.teknoir", "/t/id", "teknoir", "dev-1")
	want := []string{
		"-o", "ProxyCommand=PROXY",
		"-o", "UserKnownHostsFile=/t/known_hosts",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlias=dev-1.ns1.ztna.teknoir",
		"-o", "ServerAliveInterval=30",
		"-o", "IdentitiesOnly=yes",
		"-i", "/t/id",
		"teknoir@dev-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestValidLoginName(t *testing.T) {
	for _, ok := range []string{"teknoir", "pi", "a.b_c-d"} {
		if err := validLoginName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "a b", "a@b", "a\nb"} {
		if err := validLoginName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestResolveWGOptionPrecedence(t *testing.T) {
	const env = "TNCTL_WG_TEST_OPT"
	t.Setenv(env, "")
	if got := resolveWGOption("", env, "def"); got != "def" {
		t.Errorf("default: %q", got)
	}
	t.Setenv(env, "fromenv")
	if got := resolveWGOption("", env, "def"); got != "fromenv" {
		t.Errorf("env over default: %q", got)
	}
	if got := resolveWGOption("fromflag", env, "def"); got != "fromflag" {
		t.Errorf("flag over env: %q", got)
	}
}

func TestDefaultHubURL(t *testing.T) {
	if got := defaultHubURL("teknoir.cloud"); got != "https://wg.teknoir.cloud" {
		t.Fatal(got)
	}
}

func TestValidateHubURL(t *testing.T) {
	good := []string{"https://wg.teknoir.cloud", "https://wg.example:8443", "http://127.0.0.1:8080", "http://localhost:80", "http://[::1]:8080"}
	for _, u := range good {
		if err := validateHubURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{"", "wg.teknoir.cloud", "http://wg.teknoir.cloud", "ftp://x", "https://u:p@wg.x", "https://wg.x?a=b", "https://"}
	for _, u := range bad {
		if err := validateHubURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestSSHWireGuardFlags(t *testing.T) {
	f := sshCmd.Flags().Lookup("wireguard")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--wireguard missing or default not false: %+v", f)
	}
	for _, name := range []string{"turn-server", "wg-hub-url"} {
		if sshCmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s missing on ssh", name)
		}
	}
	// local to ssh only
	for _, c := range rootCmd.Commands() {
		if c == sshCmd || c == portForwardCmd || c.Name() == "wg-proxy" {
			continue
		}
		if c.Flags().Lookup("wireguard") != nil {
			t.Errorf("--wireguard leaked into %s", c.Name())
		}
	}
	if rootCmd.PersistentFlags().Lookup("wireguard") != nil {
		t.Error("--wireguard is persistent")
	}
}

// runSSHWireGuard with a fake `ssh` on PATH: checks the argument list, the key
// file mode while ssh runs, and that the temp directory is gone afterwards.
func TestRunSSHWireGuardWithFakeSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + out + ".args\n" +
		"for last; do :; done\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -i ]; then key=$2; fi; shift; done\n" +
		"stat -c '%a' \"$key\" > " + out + ".mode 2>/dev/null || stat -f '%Lp' \"$key\" > " + out + ".mode\n" +
		"cat \"$key\" > " + out + ".key\n" +
		"echo \"$key\" > " + out + ".keypath\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(envWGHub, "")
	t.Setenv(envWGTurnServer, "")

	dev := &api.FleetDeviceCredentials{}
	dev.Username = base64.StdEncoding.EncodeToString([]byte("teknoir"))
	dev.RSAPrivate = base64.StdEncoding.EncodeToString([]byte("PRIVATE-KEY-DATA"))

	oldTurn, oldHub := sshTURNServer, sshWGHubURL
	sshTURNServer, sshWGHubURL = "10.1.2.3:3478", ""
	defer func() { sshTURNServer, sshWGHubURL = oldTurn, oldHub }()

	if err := runSSHWireGuard("teknoir.cloud", "ns1", "dev-1", dev); err != nil {
		t.Fatal(err)
	}

	argsRaw, err := os.ReadFile(out + ".args")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(argsRaw), "\n"), "\n")
	join := strings.Join(args, "\x00")
	for _, want := range []string{
		"StrictHostKeyChecking=yes", "HostKeyAlias=dev-1.ns1.ztna.teknoir", "ServerAliveInterval=30", "teknoir@dev-1",
	} {
		if !strings.Contains(join, want) {
			t.Errorf("ssh args lack %q: %q", want, args)
		}
	}
	var proxy, kh string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "ProxyCommand="); ok {
			proxy = v
		}
		if v, ok := strings.CutPrefix(a, "UserKnownHostsFile="); ok {
			kh = v
		}
	}
	if !strings.Contains(proxy, "wg-proxy --domain teknoir.cloud --namespace ns1 --device dev-1 --port 22 --known-hosts-file") ||
		!strings.Contains(proxy, "--turn-server 10.1.2.3:3478 --hub-url https://wg.teknoir.cloud") {
		t.Errorf("proxy command %q", proxy)
	}
	if mode := strings.TrimSpace(readFile(t, out+".mode")); mode != "600" {
		t.Errorf("key mode %s", mode)
	}
	if readFile(t, out+".key") != "PRIVATE-KEY-DATA" {
		t.Error("key content")
	}
	keyPath := strings.TrimSpace(readFile(t, out+".keypath"))
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("temp key not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(kh)); !os.IsNotExist(err) {
		t.Errorf("temp dir not removed: %v", err)
	}
}

func TestRunSSHWireGuardRejectsBadInput(t *testing.T) {
	dev := &api.FleetDeviceCredentials{}
	dev.Username = base64.StdEncoding.EncodeToString([]byte("-oProxyCommand=x"))
	dev.RSAPrivate = base64.StdEncoding.EncodeToString([]byte("k"))
	if err := runSSHWireGuard("teknoir.cloud", "ns1", "dev-1", dev); err == nil {
		t.Error("option-like login accepted")
	}
	dev.Username = base64.StdEncoding.EncodeToString([]byte("ok"))
	if err := runSSHWireGuard("teknoir.cloud", "ns1", "-bad", dev); err == nil {
		t.Error("bad device name accepted")
	}
	t.Setenv(envWGHub, "http://wg.teknoir.cloud")
	if err := runSSHWireGuard("teknoir.cloud", "ns1", "dev-1", dev); err == nil {
		t.Error("plain http hub accepted")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestValidateHubHost(t *testing.T) {
	ok := []string{"https://wg.teknoir.cloud", "https://teknoir.cloud", "https://WG.Teknoir.Cloud.:8443", "http://127.0.0.1:1", "http://localhost", "http://[::1]:9"}
	for _, u := range ok {
		if err := validateHubHost(u, "teknoir.cloud"); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{"https://wg.evil.example", "https://evilteknoir.cloud", "https://teknoir.cloud.evil.example", "https://wg.teknoir.online"}
	for _, u := range bad {
		if err := validateHubHost(u, "teknoir.cloud"); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	if err := validateHubHost("https://wg.teknoir.cloud", ""); err == nil {
		t.Error("empty domain accepted")
	}
}

func TestRunSSHWireGuardRefusesForeignHub(t *testing.T) {
	dev := &api.FleetDeviceCredentials{}
	dev.Username = base64.StdEncoding.EncodeToString([]byte("teknoir"))
	dev.RSAPrivate = base64.StdEncoding.EncodeToString([]byte("k"))
	old := sshWGHubURL
	sshWGHubURL = "https://wg.evil.example"
	defer func() { sshWGHubURL = old }()
	if err := runSSHWireGuard("teknoir.cloud", "ns1", "dev-1", dev); err == nil || !strings.Contains(err.Error(), "outside the logged-in domain") {
		t.Fatalf("err = %v", err)
	}
}

// SIGTERM to tnctl while ssh runs: ssh gets it, and the key directory is removed.
func TestRunSSHWireGuardCleansUpOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -i ]; then key=$2; fi; shift; done\n" +
		"echo \"$key\" > " + out + ".keypath\n" +
		"trap 'exit 7' TERM\n" +
		"while :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(envWGHub, "")
	dev := &api.FleetDeviceCredentials{}
	dev.Username = base64.StdEncoding.EncodeToString([]byte("teknoir"))
	dev.RSAPrivate = base64.StdEncoding.EncodeToString([]byte("PRIVATE"))
	old := sshWGHubURL
	sshWGHubURL = ""
	defer func() { sshWGHubURL = old }()

	done := make(chan error, 1)
	go func() { done <- runSSHWireGuard("teknoir.cloud", "ns1", "dev-1", dev) }()
	var keyPath string
	for i := 0; i < 200 && keyPath == ""; i++ {
		time.Sleep(25 * time.Millisecond)
		if b, err := os.ReadFile(out + ".keypath"); err == nil {
			keyPath = strings.TrimSpace(string(b))
		}
	}
	if keyPath == "" {
		t.Fatal("fake ssh never started")
	}
	time.Sleep(100 * time.Millisecond) // let the trap install
	if err := selfSignal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected ssh exit status 7")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runSSHWireGuard did not return")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("key not removed after SIGTERM: %v", err)
	}
}

func selfSignal(sig os.Signal) error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
