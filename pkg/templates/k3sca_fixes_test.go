package templates

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the "final fixes": data dir detection, regeneration race, --force conditions, messages,
// backups, old k3s. They use the helpers of k3sca_test.go.

// ---------------------------------------------------------------- data dir detection

// unitText is a k3s.service as the k3s installer writes it, with the given ExecStart arguments.
func unitText(args ...string) string {
	var b strings.Builder
	b.WriteString(`[Unit]
Description=Lightweight Kubernetes
[Service]
Type=notify
EnvironmentFile=-/etc/default/%N
EnvironmentFile=-/etc/systemd/system/k3s.service.env
KillMode=process
ExecStartPre=/bin/sh -xc '! /usr/bin/systemctl is-enabled --quiet nm-cloud-setup.service 2>/dev/null'
ExecStartPre=-/sbin/modprobe br_netfilter
ExecStart=/usr/local/bin/k3s \
    server \
`)
	for i, a := range args {
		b.WriteString("\t'" + a + "'")
		if i < len(args)-1 {
			b.WriteString(" \\")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolve runs tn_ca_resolve_datadir without an explicit DATA_DIR and returns "dir|source"
func (p *pki) resolve(env ...string) string {
	p.t.Helper()
	out := p.mustSh(`tn_ca_resolve_datadir; echo "RESOLVED:$DATA_DIR|$TN_CA_DATADIR_SRC"`, append([]string{"DATA_DIR="}, env...)...)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "RESOLVED:") {
			return strings.TrimPrefix(l, "RESOLVED:")
		}
	}
	p.t.Fatalf("no RESOLVED line:\n%s", out)
	return ""
}

func TestDataDirResolution(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		p := newPKI(t)
		if got := p.resolve(); got != "/var/lib/rancher/k3s|default" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("explicit beats everything", func(t *testing.T) {
		p := newPKI(t)
		write(t, p.unit, unitText("--data-dir", "/from/unit"))
		write(t, filepath.Join(p.k3sCfg, "config.yaml"), "data-dir: /from/config\n")
		if got := p.resolve("DATA_DIR=/explicit"); got != "/explicit|--data-dir / DATA_DIR" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("config.yaml", func(t *testing.T) {
		for name, tc := range map[string]struct{ yaml, want string }{
			"plain":            {"data-dir: /opt/k3s\n", "/opt/k3s"},
			"no space":         {"data-dir:/opt/k3s\n", "/opt/k3s"},
			"double quoted":    {"disable:\n  - traefik\ndata-dir: \"/opt/k 3s\" # c\nnode-name: x\n", "/opt/k 3s"},
			"single quoted":    {"data-dir: '/opt/k3s'\n", "/opt/k3s"},
			"comment":          {"data-dir: /opt/k3s   # where\n", "/opt/k3s"},
			"crlf":             {"data-dir: /opt/k3s\r\nnode-name: x\r\n", "/opt/k3s"},
			"last one wins":    {"data-dir: /first\ndata-dir: /second\n", "/second"},
			"indented ignored": {"x:\n  data-dir: /nested\n", "/var/lib/rancher/k3s"},
			"commented out":    {"# data-dir: /opt/k3s\n", "/var/lib/rancher/k3s"},
			"empty":            {"data-dir:\n", "/var/lib/rancher/k3s"},
		} {
			t.Run(name, func(t *testing.T) {
				p := newPKI(t)
				write(t, filepath.Join(p.k3sCfg, "config.yaml"), tc.yaml)
				got := p.resolve()
				dir, src, _ := strings.Cut(got, "|")
				if dir != tc.want {
					t.Errorf("dir %q, want %q", dir, tc.want)
				}
				if tc.want != "/var/lib/rancher/k3s" && src != filepath.Join(p.k3sCfg, "config.yaml") {
					t.Errorf("source %q", src)
				}
			})
		}
	})
	t.Run("config.yaml.d beats config.yaml, last file in lexical order wins", func(t *testing.T) {
		p := newPKI(t)
		write(t, filepath.Join(p.k3sCfg, "config.yaml"), "data-dir: /from/config\n")
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "10-a.yaml"), "data-dir: /from/a\n")
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "20-b.yaml"), "data-dir: /from/b\n")
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "05-c.yaml"), "data-dir: /from/c\n")
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "30-nodir.yaml"), "node-name: x\n")
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "40-other.txt"), "data-dir: /ignored\n")
		got := p.resolve()
		if want := "/from/b|" + filepath.Join(p.k3sCfg, "config.yaml.d", "20-b.yaml"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("config.yaml.d only", func(t *testing.T) {
		p := newPKI(t)
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "a.yaml"), "data-dir: /from/a\n")
		if got := p.resolve(); !strings.HasPrefix(got, "/from/a|") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("ExecStart of the unit beats the config files", func(t *testing.T) {
		for name, tc := range map[string]struct {
			args []string
			want string
		}{
			"--data-dir DIR":    {[]string{"--disable=traefik", "--data-dir", "/from/unit", "--token", "x"}, "/from/unit"},
			"--data-dir=DIR":    {[]string{"--data-dir=/from/unit", "--token", "x"}, "/from/unit"},
			"-d DIR":            {[]string{"--token", "x", "-d", "/from/unit"}, "/from/unit"},
			"-d=DIR":            {[]string{"-d=/from/unit"}, "/from/unit"},
			"the last one":      {[]string{"--data-dir", "/one", "--data-dir=/two"}, "/two"},
			"none in ExecStart": {[]string{"--disable=traefik", "--token", "x"}, "/from/conf.d"},
		} {
			t.Run(name, func(t *testing.T) {
				p := newPKI(t)
				write(t, p.unit, unitText(tc.args...))
				write(t, filepath.Join(p.k3sCfg, "config.yaml"), "data-dir: /from/config\n")
				write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "a.yaml"), "data-dir: /from/conf.d\n")
				got := p.resolve()
				if dir, _, _ := strings.Cut(got, "|"); dir != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			})
		}
	})
	t.Run("ExecStart on one line and a drop-in reset", func(t *testing.T) {
		p := newPKI(t)
		write(t, p.unit, "[Service]\nExecStart=/usr/local/bin/k3s server --data-dir /old\nExecStart=\nExecStart=/usr/local/bin/k3s server -d /new\n")
		if got := p.resolve(); !strings.HasPrefix(got, "/new|ExecStart of k3s.service") {
			t.Errorf("got %q", got)
		}
		write(t, p.unit, "[Service]\nExecStartPre=/bin/true --data-dir /pre\nExecStart=/usr/local/bin/k3s server --token x\n")
		if got := p.resolve(); got != "/var/lib/rancher/k3s|default" {
			t.Errorf("ExecStartPre must not count: %q", got)
		}
	})
	t.Run("K3S_DATA_DIR of the unit", func(t *testing.T) {
		p := newPKI(t)
		envf := filepath.Join(p.dir, "k3s.service.env")
		write(t, envf, "K3S_URL=x\nK3S_DATA_DIR='/from/envfile'\n")
		write(t, p.unit, unitText("--token", "x")+"EnvironmentFile=-"+envf+"\n")
		if got := p.resolve(); !strings.HasPrefix(got, "/from/envfile|K3S_DATA_DIR") {
			t.Errorf("got %q", got)
		}
		// config.yaml (a command line flag to k3s) beats the environment
		write(t, filepath.Join(p.k3sCfg, "config.yaml"), "data-dir: /from/config\n")
		if got := p.resolve(); !strings.HasPrefix(got, "/from/config|") {
			t.Errorf("got %q", got)
		}
		q := newPKI(t)
		write(t, q.unit, "[Service]\nEnvironment=\"FOO=1\" \"K3S_DATA_DIR=/from/env\"\nExecStart=/usr/local/bin/k3s server\n")
		if got := q.resolve(); !strings.HasPrefix(got, "/from/env|K3S_DATA_DIR") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("no globbing, the bootstrap runs with noglob", func(t *testing.T) {
		p := newPKI(t)
		write(t, filepath.Join(p.k3sCfg, "config.yaml.d", "a.yaml"), "data-dir: /from/a\n")
		// p.sh already runs with `set -o noglob`; this is the same assertion under dash -u as well
		if got := p.resolve(); !strings.HasPrefix(got, "/from/a|") {
			t.Errorf("got %q", got)
		}
	})
}

// check, remediate and setup all use the resolved dir when there is no explicit one
func TestCheckRemediateSetupUseTheResolvedDataDir(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	other := filepath.Join(p.dir, "opt-k3s")
	p.data = other
	p.legacyPKI(true)
	write(t, filepath.Join(p.k3sCfg, "config.yaml"), "data-dir: "+other+"\n")

	out, err := p.sh("tn_ca_check", "DATA_DIR=")
	if rc := exitCode(err); rc != 2 || !strings.Contains(out, "needs-rotation") || !strings.Contains(out, "config.yaml") {
		t.Fatalf("check rc=%d\n%s", rc, out)
	}
	// stand-alone, no --data-dir
	cmd := exec.Command("sh", libPath, "check", "--config-dir", p.cfg)
	o, err := cmd.CombinedOutput()
	if rc := exitCode(err); rc != 2 || !strings.Contains(string(o), "needs-rotation") {
		t.Errorf("sh tn-k3s-ca.sh check: rc=%d\n%s", rc, o)
	}

	bin, calls := fakeK3s(t, t.TempDir(), true)
	p.mustSh("tn_ca_remediate all", "DATA_DIR=", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true")
	if c, _ := os.ReadFile(calls); !strings.Contains(string(c), "--data-dir "+other+" ") {
		t.Errorf("rotate-ca was not called with the resolved data dir: %s", c)
	}
	p.assertGoodPKI(orig)

	// setup on a never started device with the data dir in a conf.d file
	q := newPKI(t)
	q.legacyCert()
	qdir := filepath.Join(q.dir, "conf-d-dir")
	write(t, filepath.Join(q.k3sCfg, "config.yaml.d", "50-data.yaml"), "data-dir: "+qdir+"\n")
	out = q.mustSh("tn_k3s_ca_setup", "DATA_DIR=")
	if !strings.Contains(out, "k3s data dir: "+qdir) {
		t.Errorf("setup did not say where it works:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(qdir, "server", "tls", "server-ca.crt")); err != nil {
		t.Errorf("setup did not create the PKI in the resolved dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(q.data, "server")); err == nil {
		t.Error("setup used the default data dir")
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// A k3s server unit exists, but no data dir with a CA was found: never ok.
func TestCheckIsNotOkWhenAServerUnitExistsButNoServerDataFound(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	// no unit, no data: an agent or a fresh machine, ok
	if rc, o := p.check(); rc != 0 || !strings.Contains(o, "not-a-server") {
		t.Fatalf("no unit: rc=%d %s", rc, o)
	}
	// a server unit, data dir (the default or a wrong explicit one) without server/tls
	write(t, p.unit, unitText("--data-dir", "/somewhere/else", "--token", "x"))
	out, err := p.sh("tn_ca_check", "DATA_DIR=")
	rc := exitCode(err)
	if rc != 4 || !strings.Contains(out, "no-server-data") || !strings.Contains(out, "/somewhere/else") || !strings.Contains(out, "--data-dir") {
		t.Errorf("unit with a missing data dir: rc=%d\n%s", rc, out)
	}
	if rc, o := p.check(); rc != 4 || !strings.Contains(o, "no-server-data") {
		t.Errorf("explicit data dir without server/tls: rc=%d %s", rc, o)
	}
	// remediate cannot act on it either, and says why
	if o, err := p.sh("tn_ca_remediate all"); exitCode(err) != 4 || !strings.Contains(o, "no-server-data") && !strings.Contains(o, "server unit") {
		t.Errorf("remediate: rc=%d\n%s", exitCode(err), o)
	}
	// pointing at the real dir: ok again
	p.mustSh("tn_k3s_ca_setup")
	if rc, o := p.check(); rc != 0 {
		t.Errorf("check after setup: rc=%d %s", rc, o)
	}
	// ... and setup itself is not blocked by a unit without a data dir (OS builds install the
	// unit without starting it; the first bootstrap creates the PKI before the installer)
	q := newPKI(t)
	q.legacyCert()
	write(t, q.unit, unitText("--token", "x"))
	q.mustSh("tn_k3s_ca_setup")
	if _, err := os.Stat(q.tls("server-ca.crt")); err != nil {
		t.Errorf("setup with a unit but no data: %v", err)
	}
}

// ---------------------------------------------------------------- regeneration race

func TestRegenerationStopsK3sAndReplacesTheDirSafely(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(false) // hung first start
	oldSum := sum(t, p.tls("root-ca.pem"))
	log := filepath.Join(p.dir, "svc.log")
	// the stop command records what server/tls looked like at that moment
	stop := "echo stop root=$(sha256sum " + p.tls("root-ca.pem") + " | cut -d' ' -f1) >> " + log
	start := "echo start root=$(sha256sum " + p.tls("root-ca.pem") + " | cut -d' ' -f1) >> " + log
	p.mustSh("tn_ca_remediate prepare", "TN_K3S_STOP_CMD="+stop, "TN_K3S_START_CMD="+start)
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0] != "stop root="+oldSum || !strings.HasPrefix(lines[1], "start root=") || lines[1] == "start root="+oldSum {
		t.Errorf("service log:\n%s", b)
	}
	p.assertGoodPKI(orig)
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-tls-*")); len(m) != 0 {
		t.Errorf("work dirs left behind: %v", m)
	}

	// `remediate all` on such a device: stop, replace, start, then the restart of the tool
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(false)
	qlog := filepath.Join(q.dir, "svc.log")
	q.mustSh("tn_ca_remediate all", "TN_K3S_STOP_CMD=echo stop >> "+qlog, "TN_K3S_START_CMD=echo start >> "+qlog, "TN_K3S_RESTART_CMD=echo restart >> "+qlog)
	if b, _ := os.ReadFile(qlog); string(b) != "stop\nstart\nrestart\n" {
		t.Errorf("service log: %q", b)
	}

	// a fresh bootstrap has no k3s: nothing is stopped or started (STOP_CMD unset, no active unit)
	r := newPKI(t)
	r.legacyCert()
	out := r.mustSh("tn_k3s_ca_setup")
	if strings.Contains(out, "Stopping k3s") {
		t.Errorf("setup stopped k3s:\n%s", out)
	}
}

func TestRegenerationRestoresTheOldDirOnAnyFailure(t *testing.T) {
	t.Run("installed result does not validate", testRestoreWhenInstalledPKIFails)
	t.Run("k3s cannot be stopped", testNothingReplacedWhenK3sCannotBeStopped)
}

// the installed result does not validate: the old dir comes back and k3s is started again
func testRestoreWhenInstalledPKIFails(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(false)
	oldSum := sum(t, p.tls("root-ca.pem")) + sum(t, p.tls("server-ca.crt"))
	log := filepath.Join(p.dir, "svc.log")
	failingOpenssl(t, "verify", 7) // the 6 checks of the generated dir pass, those of the installed dir fail
	out, err := p.sh("tn_ca_remediate prepare", "TN_K3S_STOP_CMD=echo stop >> "+log, "TN_K3S_START_CMD=echo start >> "+log)
	if err == nil {
		t.Fatalf("prepare succeeded although the installed PKI does not validate:\n%s", out)
	}
	if !strings.Contains(out, "does not validate") || !strings.Contains(out, "was restored") {
		t.Errorf("messages:\n%s", out)
	}
	if got := sum(t, p.tls("root-ca.pem")) + sum(t, p.tls("server-ca.crt")); got != oldSum {
		t.Error("the old server/tls was not restored")
	}
	if b, _ := os.ReadFile(log); string(b) != "stop\nstart\n" {
		t.Errorf("service log: %q (k3s must be started again after a failure)", b)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-tls-*")); len(m) != 0 {
		t.Errorf("work dirs left behind: %v", m)
	}

}

// k3s cannot be stopped: nothing is replaced, nothing is started
func testNothingReplacedWhenK3sCannotBeStopped(t *testing.T) {
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(false)
	before := sum(t, q.tls("server-ca.crt"))
	qlog := filepath.Join(q.dir, "svc.log")
	out, err := q.sh("tn_ca_remediate prepare", "TN_K3S_STOP_CMD=false", "TN_K3S_START_CMD=echo start >> "+qlog)
	if err == nil || !strings.Contains(out, "cannot stop k3s") {
		t.Errorf("stop failure: err=%v\n%s", err, out)
	}
	if sum(t, q.tls("server-ca.crt")) != before {
		t.Error("server/tls changed although k3s could not be stopped")
	}
	if _, err := os.Stat(qlog); err == nil {
		t.Error("k3s was started although it was never stopped")
	}
	if m, _ := filepath.Glob(filepath.Join(q.data, "server", "tls-backup-tn-*.tar")); len(m) != 0 {
		t.Errorf("backup made although nothing was replaced: %v", m)
	}
}

// ---------------------------------------------------------------- --force conditions

func (p *pki) stagedRotateCmd() string {
	p.t.Helper()
	stages, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*"))
	if len(stages) != 1 {
		p.t.Fatalf("staging dirs: %v", stages)
	}
	b, err := os.ReadFile(filepath.Join(stages[0], "ROTATE_CMD"))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(b)
}

func TestRotateCmdForcesWhenTheOldRootCannotVouch(t *testing.T) {
	noBC := filepath.Join(t.TempDir(), "no-ext.cnf")
	write(t, noBC, "[req]\ndistinguished_name=dn\nprompt=no\nx509_extensions=ext\n[dn]\nCN=teknoir.ai\n[ext]\nsubjectKeyIdentifier=hash\n")
	future := time.Now().Add(48 * time.Hour).UTC().Format("20060102150405Z")
	for _, tc := range []struct {
		name string
		args []string
		why  string
	}{
		{"no basicConstraints", []string{"-config", noBC}, "no basicConstraints"},
		{"CA:FALSE", []string{"-addext", "basicConstraints=critical,CA:FALSE"}, "CA:FALSE"},
		{"not yet valid", []string{"-not_before", future, "-not_after", "20700101000000Z"}, "cannot verify the old chain"},
		{"pathlen 0, openssl cannot verify the old chain", []string{"-addext", "basicConstraints=critical,CA:TRUE,pathlen:0"}, "cannot verify the old chain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCertArgs(tc.args...)
			p.legacyPKI(true)
			out := p.mustSh("tn_ca_openssl; tn_ca_remediate prepare")
			if !strings.Contains(out, "--force") || !strings.Contains(out, tc.why) {
				t.Errorf("no word about --force (%q):\n%s", tc.why, out)
			}
			if rc := p.stagedRotateCmd(); !strings.Contains(rc, "--force && touch") {
				t.Errorf("ROTATE_CMD: %s", rc)
			}
		})
	}
	// a good old root (CA:TRUE, no keyUsage, valid) needs no --force: covered by
	// TestRotateCmdForcesOnlyWhenTheOldRootCannotVouch
}

// ---------------------------------------------------------------- messages and cleanup

func TestRestartFailureAfterARotationSaysTheCAWasChanged(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(true)
	bin, _ := fakeK3s(t, t.TempDir(), true)
	out, err := p.sh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=false")
	if exitCode(err) != 5 {
		t.Fatalf("rc=%d\n%s", exitCode(err), out)
	}
	for _, want := range []string{"datastore of k3s holds the new CA", "systemctl restart k3s", "journalctl", "Do not roll back"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not changed") {
		t.Errorf("a changed device is described as not changed:\n%s", out)
	}
	p.assertGoodPKI(orig) // the files were finalized before the restart

	// the same through the bootstrap setup
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(true)
	out, err = q.sh("tn_k3s_ca_setup", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=false")
	if err == nil {
		t.Fatal("setup continued after a failed restart")
	}
	if strings.Contains(out, "was not changed") || !strings.Contains(out, "WAS changed") || !strings.Contains(out, "holds the new CA") {
		t.Errorf("setup message:\n%s", out)
	}

	// and a failed rotate-ca still says that nothing was changed
	r := newPKI(t)
	r.legacyCert()
	r.legacyPKI(true)
	badBin, _ := fakeK3s(t, t.TempDir(), false)
	out, err = r.sh("tn_k3s_ca_setup", "K3S_BIN="+badBin, "TN_K3S_RESTART_CMD=true")
	if err == nil || !strings.Contains(out, "was not changed beyond the saved backup") {
		t.Errorf("failed rotation: err=%v\n%s", err, out)
	}
}

// rotate-ca succeeded, the files were not copied: a second run finalizes, it does not stage again
func TestRemediateFinalizesACompletedRotationInsteadOfStagingAgain(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(true)
	bin, calls := fakeK3s(t, t.TempDir(), true)
	p.mustSh("tn_ca_remediate prepare", "K3S_BIN="+bin)
	p.mustSh("tn_ca_remediate rotate", "K3S_BIN="+bin)
	// (finalize "failed": the files are still the old ones)
	restarted := filepath.Join(p.dir, "restarted")
	out := p.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=touch "+restarted)
	if !strings.Contains(out, "finalizing it") {
		t.Errorf("message:\n%s", out)
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "rotate-ca") != 1 {
		t.Errorf("rotate-ca was run again:\n%s", b)
	}
	if _, err := os.Stat(restarted); err != nil {
		t.Error("k3s was not restarted")
	}
	p.assertGoodPKI(orig)
}

func TestLeftoverRotatedStagingIsCleanedWhenTheCAIsOk(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.mustSh("tn_k3s_ca_setup")
	if err := os.WriteFile(filepath.Join(p.data, "server", "token"), []byte("K10x::server:y"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, script string }{
		{"remediate all", "tn_ca_remediate all"},
		{"remediate prepare", "tn_ca_remediate prepare"},
		{"setup", "tn_k3s_ca_setup"},
	} {
		stage := filepath.Join(p.data, "server", ".tn-ca-rotate-20200101T000000")
		write(t, filepath.Join(stage, "ROTATED"), "")
		write(t, filepath.Join(stage, "server", "tls", "root-ca.key"), "not a key")
		out := p.mustSh(tc.script)
		if _, err := os.Stat(stage); err == nil {
			t.Errorf("%s: the staging dir was not removed", tc.name)
		}
		if !strings.Contains(out, "Removed the leftover staging dir") {
			t.Errorf("%s: no message:\n%s", tc.name, out)
		}
	}
	// a read-only check leaves it alone
	stage := filepath.Join(p.data, "server", ".tn-ca-rotate-20200101T000000")
	write(t, filepath.Join(stage, "ROTATED"), "")
	p.check()
	if _, err := os.Stat(stage); err != nil {
		t.Error("check removed the staging dir")
	}
}

func TestBackupsKeepTheNewestThree(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(false)
	for _, ts := range []string{"20200101T000000", "20200102T000000", "20200103T000000", "20200104T000000"} {
		write(t, filepath.Join(p.data, "server", "tls-backup-tn-"+ts+".tar"), "old")
	}
	p.mustSh("tn_ca_remediate prepare")
	m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar"))
	if len(m) != 3 {
		t.Fatalf("backups: %v", m)
	}
	for _, gone := range []string{"20200101T000000", "20200102T000000"} {
		if _, err := os.Stat(filepath.Join(p.data, "server", "tls-backup-tn-"+gone+".tar")); err == nil {
			t.Errorf("%s was kept", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(p.data, "server", "tls-backup-tn-20200104T000000.tar")); err != nil {
		t.Error("an older one than the newest 3 survived instead")
	}
}

func TestProgramModeOptionWithoutValue(t *testing.T) {
	for _, args := range [][]string{
		{"remediate", "--phase"},
		{"check", "--data-dir"},
		{"check", "--config-dir"},
		{"remediate", "--phase", ""},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, "sh", append([]string{libPath}, args...)...)
		out, err := cmd.CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		if timedOut {
			t.Errorf("%v hangs", args)
			continue
		}
		if exitCode(err) != 2 || !strings.Contains(string(out), "needs a value") || !strings.Contains(string(out), "usage:") {
			t.Errorf("%v: rc=%d\n%s", args, exitCode(err), out)
		}
	}
}

// ---------------------------------------------------------------- k3s too old for rotate-ca

func fakeK3sVersion(t *testing.T, dir, version string, ok bool) (bin, log string) {
	t.Helper()
	log = filepath.Join(dir, "k3s-calls")
	bin = filepath.Join(dir, "fake-k3s")
	rc := "0"
	if !ok {
		rc = "1"
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'k3s version " + version + " (0123abcd)'; echo 'go version go1.22.1'; exit 0; fi\necho \"$*\" >> '" + log + "'\nexit " + rc + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestRotateCaSupportedVersions(t *testing.T) {
	p := newPKI(t)
	for _, tc := range []struct {
		v    string
		want int
	}{
		{"v1.23.17+k3s1", 1}, {"v1.24.11+k3s1", 1}, {"v1.24.12+k3s1", 0},
		{"v1.25.7+k3s1", 1}, {"v1.25.8+k3s1", 0}, {"v1.26.2+k3s1", 1}, {"v1.26.3+k3s1", 0},
		{"v1.27.0+k3s1", 0}, {"v1.34.11+k3s1", 0}, {"v1.36.5+k3s1", 0}, {"v1.19.5+k3s2", 1},
	} {
		bin, _ := fakeK3sVersion(t, t.TempDir(), tc.v, true)
		_, err := p.sh("tn_ca_rotate_supported", "K3S_BIN="+bin)
		if exitCode(err) != tc.want {
			t.Errorf("%s: rc=%d, want %d", tc.v, exitCode(err), tc.want)
		}
	}
	// a version that cannot be told (no k3s binary, odd output) does not block
	for _, bin := range []string{"/nonexistent/k3s", "echo hello"} {
		if out, err := p.sh("tn_ca_rotate_supported", "K3S_BIN="+bin); err != nil {
			t.Errorf("K3S_BIN=%q blocks: %v\n%s", bin, err, out)
		}
	}
	// a K3S_BIN with arguments, like the harness' "docker exec srv k3s", works
	old, _ := fakeK3sVersion(t, t.TempDir(), "v1.20.1+k3s1", true)
	if _, err := p.sh("tn_ca_rotate_supported", "K3S_BIN=env FAKE=1 "+old); exitCode(err) != 1 {
		t.Errorf("K3S_BIN with arguments is not used")
	}
}

func TestOldK3sFailsSafeBeforeAnythingIsChanged(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	before := sum(t, p.tls("server-ca.crt")) + sum(t, p.tls("root-ca.pem"))
	bin, calls := fakeK3sVersion(t, t.TempDir(), "v1.24.5+k3s1", true)
	for _, script := range []string{"tn_ca_remediate prepare", "tn_ca_remediate all", "tn_k3s_ca_setup"} {
		out, err := p.sh(script, "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=touch "+filepath.Join(p.dir, "restarted"))
		if err == nil {
			t.Fatalf("%s succeeded on k3s v1.24.5:\n%s", script, out)
		}
		for _, want := range []string{"v1.24.5", "v1.24.12", "v1.26.3", "one minor version at a time", "The CA and k3s were not changed"} {
			if !strings.Contains(out, want) && script != "tn_k3s_ca_setup" {
				t.Errorf("%s: advice lacks %q:\n%s", script, want, out)
			}
		}
	}
	if sum(t, p.tls("server-ca.crt"))+sum(t, p.tls("root-ca.pem")) != before {
		t.Error("server/tls changed")
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*")); len(m) != 0 {
		t.Errorf("staging left behind: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); len(m) != 0 {
		t.Errorf("backup made: %v", m)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("rotate-ca was called")
	}
	if _, err := os.Stat(filepath.Join(p.dir, "restarted")); err == nil {
		t.Error("k3s was restarted")
	}
	// the phase `rotate` refuses too (a staging made by a newer k3s binary, then a downgrade)
	good, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	p.mustSh("tn_ca_remediate prepare", "K3S_BIN="+good)
	if out, err := p.sh("tn_ca_remediate rotate", "K3S_BIN="+bin); err == nil || !strings.Contains(out, "v1.24.12") {
		t.Errorf("rotate on an old k3s: err=%v\n%s", err, out)
	}
	// a k3s with rotate-ca goes on
	p.mustSh("tn_ca_remediate all", "K3S_BIN="+good, "TN_K3S_RESTART_CMD=true")
}
