package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Offline tests of scripts/tn-k3s-install.sh: the k3s release policy of a re-run, and the kubelet
// flag for cgroup v1 hosts. The docker harness in test/k3s-ca runs the same library with real k3s.

const installLibPath = "scripts/tn-k3s-install.sh"

type inst struct {
	t       *testing.T
	dir     string
	k3sCfg  string // TN_K3S_CONFIG_DIR
	bin     string // K3S_BIN (a fake k3s)
	curlLog string // the calls of the fake curl
}

func newInst(t *testing.T) *inst {
	t.Helper()
	needTool(t, "sh")
	i := &inst{t: t, dir: t.TempDir()}
	i.k3sCfg = filepath.Join(i.dir, "rancher-k3s")
	// the GNU info reader must never be called by the library
	fake := filepath.Join(i.dir, "bin")
	write(t, filepath.Join(fake, "info"), "#!/bin/sh\necho GNU-INFO-WAS-CALLED \"$*\" >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(fake, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the server of an agent: `curl` answers like the supervisor of k3s. /static/teknoir/k3s-version is
	// the release that the Teknoir server script published (TN_TEST_PUBLISHED, anything is served
	// as it is); /version is the anonymous answer of a server that allows one (TN_TEST_SERVER_VERSION or
	// TN_TEST_SERVER_BODY). Everything else is a 404 (curl -f: exit 22). TN_TEST_SERVER_DOWN: nothing
	// answers (exit 7); TN_TEST_FAIL_N: the first N calls fail like that. Every call is logged.
	i.curlLog = filepath.Join(i.dir, "curl.log")
	write(t, filepath.Join(fake, "curl"), `#!/bin/sh
echo "$*" >> '`+i.curlLog+`'
for a in "$@"; do url=$a; done
if [ -n "$TN_TEST_SERVER_DOWN" ]; then exit 7; fi
n=$(wc -l < '`+i.curlLog+`'); if [ "$n" -le "${TN_TEST_FAIL_N:-0}" ]; then exit 7; fi
case "$url" in
  */static/teknoir/k3s-version)
    if [ -n "$TN_TEST_PUBLISHED" ]; then printf '%s\n' "$TN_TEST_PUBLISHED"; exit 0; fi
    exit 22 ;;
  */version)
    if [ -n "$TN_TEST_SERVER_BODY" ]; then printf '%s' "$TN_TEST_SERVER_BODY"; exit 0; fi
    if [ -n "$TN_TEST_SERVER_VERSION" ]; then
      printf '{\n  "major": "1",\n  "minor": "34",\n  "gitVersion": "%s",\n  "gitCommit": "0123abcd",\n  "goVersion": "go1.24.1"\n}' "$TN_TEST_SERVER_VERSION"; exit 0
    fi
    exit 22 ;;
esac
exit 22
`)
	if err := os.Chmod(filepath.Join(fake, "curl"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+":"+os.Getenv("PATH"))
	return i
}

// curlCalls is what the library asked the (fake) server for
func (i *inst) curlCalls() string { return readLog(i.curlLog) }

// installed makes `k3s --version` answer like the real one; "" = no k3s.
func (i *inst) installed(version string) {
	i.t.Helper()
	i.bin = filepath.Join(i.dir, "fake-k3s")
	if version == "" {
		i.bin = filepath.Join(i.dir, "no-such-k3s")
		return
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'k3s version " + version + " (0123abcd)'; echo 'go version go1.24.1'; exit 0; fi\nexit 1\n"
	write(i.t, i.bin, script)
	if err := os.Chmod(i.bin, 0o755); err != nil {
		i.t.Fatal(err)
	}
}

// run sources the library under the bootstrap script shell options and runs script. Environment
// of the machine that starts with INSTALL_K3S_, TN_K3S_ or TN_CGROUP is dropped.
func (i *inst) run(script string, env ...string) (string, error) {
	i.t.Helper()
	full := "set -e; set -o noglob; umask 027; . " + installLibPath + "; " + script
	cmd := exec.Command("sh", "-c", full)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "INSTALL_K3S_") || strings.HasPrefix(e, "TN_K3S_") || strings.HasPrefix(e, "TN_CGROUP") || strings.HasPrefix(e, "OS_BUILD") || strings.HasPrefix(e, "K3S_BIN") || strings.HasPrefix(e, "K3S_URL") || strings.HasPrefix(e, "DATA_DIR") || strings.HasPrefix(e, "TN_TEST_") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "TN_K3S_CONFIG_DIR="+i.k3sCfg, "K3S_BIN="+i.bin, "DATA_DIR="+filepath.Join(i.dir, "data"), "TN_K3S_SERVER_VERSION_WAIT=0")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte("GNU-INFO-WAS-CALLED")) {
		i.t.Errorf("the info binary was called:\n%s", out)
	}
	return string(out), err
}

const resultLine = `echo "RESULT version=${INSTALL_K3S_VERSION-unset} channel=${INSTALL_K3S_CHANNEL-unset} action=$TN_INST_ACTION effective=$TN_INST_EFFECTIVE"`

func result(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "RESULT ") {
			return strings.TrimPrefix(l, "RESULT ")
		}
	}
	t.Fatalf("no RESULT line:\n%s", out)
	return ""
}

func (i *inst) dropin() string {
	return filepath.Join(i.k3sCfg, "config.yaml.d", "99-teknoir-cgroupv1.yaml")
}

// ---------------------------------------------------------------- the library itself

func TestK3sInstallLibraryIsPlainShell(t *testing.T) {
	b, err := os.ReadFile(installLibPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("{{")) {
		t.Fatal("the library is embedded as a template: it must not contain '{{'")
	}
	for _, sh := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(sh); err != nil {
			continue
		}
		if out, err := exec.Command(sh, "-n", installLibPath).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
	if string(b) != TnK3sInstallScript {
		t.Error("embedded TnK3sInstallScript differs from the file")
	}
	for _, bad := range []string{"\ntrap ", "\n  trap ", "set +o noglob", "set -x", "local "} {
		if strings.Contains(string(b), bad) {
			t.Errorf("library must not contain %q", bad)
		}
	}
}

func TestVersionParseAndCompare(t *testing.T) {
	i := newInst(t)
	for _, tc := range []struct {
		a, b string
		want string // -1, 0, 1, or "err"
	}{
		{"v1.34.11+k3s1", "v1.36.5+k3s1", "-1"},
		{"v1.36.5+k3s1", "v1.36.5+k3s1", "0"},
		{"v1.36.5+k3s2", "v1.36.5+k3s1", "1"},
		{"v1.36.10+k3s1", "v1.36.9+k3s3", "1"}, // numeric, not lexical
		{"v1.9.0+k3s1", "v1.10.0+k3s1", "-1"},
		{"v1.36.5", "v1.36.5+k3s1", "-1"}, // no revision is revision 0
		{"v1.36.5+k3s1", "v1.36.4+k3s9", "1"},
		{"v1.35.0", "v1.35.0", "0"},
		{"stable", "v1.36.5+k3s1", "err"},
		{"", "v1.36.5+k3s1", "err"},
		{"v1.36.5+k3s1", "latest", "err"},
		{"v1.36+k3s1", "v1.36.5+k3s1", "err"},
		{"1.36.5+k3s1", "v1.36.5+k3s1", "err"},
		{"v1.36.5-rc1+k3s1", "v1.36.5+k3s1", "err"},
	} {
		want := tc.want
		out, err := i.run(`A='` + tc.a + `'; B='` + tc.b + `'; if tn_inst_cmp "$A" "$B"; then echo "CMP $TN_INST_CMP"; else echo "CMP err"; fi`)
		if err != nil {
			t.Fatalf("%q vs %q: %v\n%s", tc.a, tc.b, err, out)
		}
		if !strings.Contains(out, "CMP "+want+"\n") {
			t.Errorf("%q vs %q: want %s, got\n%s", tc.a, tc.b, want, out)
		}
	}
}

func TestInstalledVersionIsReadFromTheRealOutput(t *testing.T) {
	i := newInst(t)
	for _, v := range []string{"v1.34.11+k3s1", "v1.36.5+k3s1", "v1.27.0+k3s2", "v1.30.2"} {
		i.installed(v)
		out, err := i.run(`tn_inst_installed_version; echo "INSTALLED=$TN_INST_INSTALLED"`)
		if err != nil || !strings.Contains(out, "INSTALLED="+v+"\n") {
			t.Errorf("%s: %v\n%s", v, err, out)
		}
	}
	i.installed("")
	out, err := i.run(`tn_inst_installed_version; echo "INSTALLED=$TN_INST_INSTALLED"`)
	if err != nil || !strings.Contains(out, "INSTALLED=\n") {
		t.Errorf("no k3s: %v\n%s", err, out)
	}
	// a K3S_BIN with arguments, like the harness' "docker exec srv k3s"
	i.installed("v1.36.5+k3s1")
	out, err = i.run(`tn_inst_installed_version; echo "INSTALLED=$TN_INST_INSTALLED"`, "K3S_BIN=env FAKE=1 "+i.bin)
	if err != nil || !strings.Contains(out, "INSTALLED=v1.36.5+k3s1\n") {
		t.Errorf("K3S_BIN with arguments: %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------- the release policy

func TestReleasePolicy(t *testing.T) {
	const pin = "v1.36.5+k3s1"
	for _, tc := range []struct {
		name      string
		installed string
		env       []string // besides INSTALL_K3S_VERSION=pin
		pin       string   // default: pin
		wantRC    int
		want      string   // RESULT
		msgs      []string // in the output
		notMsgs   []string
	}{
		{name: "fresh device installs the pin", installed: "", want: "version=" + pin + " channel=unset action=install effective=" + pin},
		{name: "re-run on the same release", installed: pin, want: "version=" + pin + " channel=unset action=same effective=" + pin, msgs: []string{"installed again, nothing is upgraded"}},
		{name: "older k3s is KEPT, not upgraded", installed: "v1.34.11+k3s1",
			want: "version=v1.34.11+k3s1 channel=unset action=keep effective=v1.34.11+k3s1",
			msgs: []string{"k3s v1.34.11+k3s1 is kept", "TN_K3S_UPGRADE=true", "NOT installed"}},
		{name: "newer k3s is kept, no downgrade", installed: "v1.37.0+k3s1",
			want: "version=v1.37.0+k3s1 channel=unset action=keep effective=v1.37.0+k3s1",
			msgs: []string{"OLDER than the installed", "cannot be downgraded"}},
		{name: "a newer revision of the same patch is kept", installed: "v1.36.5+k3s2",
			want: "version=v1.36.5+k3s2 channel=unset action=keep effective=v1.36.5+k3s2"},
		{name: "channel pin and installed k3s: the installed release", installed: "v1.35.9+k3s1", pin: "-", env: []string{"INSTALL_K3S_CHANNEL=latest"},
			want: "version=v1.35.9+k3s1 channel=unset action=keep effective=v1.35.9+k3s1"},
		{name: "no pin at all (stable) and installed k3s", installed: "v1.35.9+k3s1", pin: "-",
			want: "version=v1.35.9+k3s1 channel=unset action=keep effective=v1.35.9+k3s1"},
		{name: "no pin at all and no k3s: the installer decides", installed: "", pin: "-",
			want: "version=unset channel=unset action=install effective="},

		// explicit opt-in
		{name: "upgrade one minor", installed: "v1.35.9+k3s1", env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + pin + " channel=unset action=upgrade effective=" + pin, msgs: []string{"upgrading k3s from v1.35.9+k3s1 to " + pin}},
		{name: "upgrade within the minor", installed: "v1.36.2+k3s1", env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + pin + " channel=unset action=upgrade effective=" + pin},
		{name: "upgrade, same release", installed: pin, env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + pin + " channel=unset action=same effective=" + pin},
		{name: "upgrade is refused over two minors", installed: "v1.34.11+k3s1", env: []string{"TN_K3S_UPGRADE=true"}, wantRC: 1,
			msgs: []string{"more than one minor version", "v1.34 -> v1.36", "v1.35.x first", "TN_K3S_FORCE=true", "Nothing was changed"}},
		{name: "upgrade over two minors with force warns", installed: "v1.34.11+k3s1", env: []string{"TN_K3S_UPGRADE=true", "TN_K3S_FORCE=true"},
			want: "version=" + pin + " channel=unset action=upgrade effective=" + pin, msgs: []string{"skips minor versions", "backup"}},
		{name: "force alone does not upgrade", installed: "v1.34.11+k3s1", env: []string{"TN_K3S_FORCE=true"},
			want: "version=v1.34.11+k3s1 channel=unset action=keep effective=v1.34.11+k3s1"},
		{name: "downgrade is refused with upgrade", installed: "v1.37.1+k3s1", env: []string{"TN_K3S_UPGRADE=true"}, wantRC: 1,
			msgs: []string{"refusing to downgrade k3s from v1.37.1+k3s1 to " + pin, "Nothing was changed"}},
		{name: "downgrade is refused with force too", installed: "v1.37.1+k3s1", env: []string{"TN_K3S_UPGRADE=true", "TN_K3S_FORCE=true"}, wantRC: 1,
			msgs: []string{"refusing to downgrade"}},
		{name: "a lower patch is a downgrade", installed: "v1.36.6+k3s1", env: []string{"TN_K3S_UPGRADE=true"}, wantRC: 1, msgs: []string{"refusing to downgrade"}},
		{name: "a lower k3s revision is a downgrade", installed: "v1.36.5+k3s2", env: []string{"TN_K3S_UPGRADE=true"}, wantRC: 1, msgs: []string{"refusing to downgrade"}},
		{name: "upgrade to a channel cannot be checked", installed: "v1.34.11+k3s1", pin: "-", env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=unset channel=unset action=upgrade effective=", msgs: []string{"not an exact release", "cannot be checked"}},
		{name: "upgrade of a fresh device is just an install", installed: "", env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + pin + " channel=unset action=install effective=" + pin},
		{name: "TN_K3S_UPGRADE=false is a keep", installed: "v1.34.11+k3s1", env: []string{"TN_K3S_UPGRADE=false"},
			want: "version=v1.34.11+k3s1 channel=unset action=keep effective=v1.34.11+k3s1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed(tc.installed)
			env := append([]string{}, tc.env...)
			p := tc.pin
			if p == "" {
				p = pin
			}
			script := ""
			if p != "-" {
				env = append(env, "INSTALL_K3S_VERSION="+p)
			}
			// cgroup v2: this test is about the release
			env = append(env, "TN_CGROUP_FSTYPE=cgroup2fs")
			script += "tn_inst_plan; " + resultLine
			out, err := i.run(script, env...)
			if rc := exitCode(err); rc != tc.wantRC {
				t.Fatalf("rc=%d, want %d\n%s", rc, tc.wantRC, out)
			}
			if tc.wantRC == 0 {
				if got := result(t, out); got != tc.want {
					t.Errorf("result\n got  %s\n want %s\n%s", got, tc.want, out)
				}
			} else if strings.Contains(out, "RESULT") {
				t.Errorf("the script went on after the refusal:\n%s", out)
			}
			for _, m := range tc.msgs {
				if !strings.Contains(out, m) {
					t.Errorf("output lacks %q:\n%s", m, out)
				}
			}
			for _, m := range tc.notMsgs {
				if strings.Contains(out, m) {
					t.Errorf("output has %q:\n%s", m, out)
				}
			}
		})
	}
}

// ---------------------------------------------------------------- cgroup v1

func TestCgroupV1WritesTheKubeletFlag(t *testing.T) {
	const want = "kubelet-arg+:\n  - \"fail-cgroupv1=false\"\n"
	for _, tc := range []struct {
		name      string
		installed string
		version   string // INSTALL_K3S_VERSION
		env       []string
		file      bool
		msgs      []string
		notMsgs   []string
	}{
		{name: "cgroup v1 and k3s v1.36.5", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true,
			msgs: []string{"cgroup v1 is deprecated", "Set the kubelet argument fail-cgroupv1=false"}},
		{name: "the hybrid layout is v1", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true},
		{name: "exactly v1.35.0", version: "v1.35.0+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true},
		{name: "cgroup v1 and k3s v1.34.11: set for a later upgrade", version: "v1.34.11+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true,
			msgs: []string{"cgroup v1 is deprecated", "starts on cgroup v1 without help", "an upgrade to v1.35 or newer"}},
		{name: "exactly v1.31.0", version: "v1.31.0+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true},
		{name: "cgroup v1 and k3s v1.30.14: the kubelet has no such flag", version: "v1.30.14+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: false,
			msgs: []string{"cgroup v1 is deprecated", "older than v1.31"}},
		{name: "cgroup v2 needs nothing", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=cgroup2fs"}, file: false, notMsgs: []string{"deprecated"}},
		{name: "cgroup v1 and a channel: k3s is new", version: "", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true},
		{name: "an installed k3s that is kept decides", installed: "v1.34.11+k3s1", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: true,
			msgs: []string{"without help"}},
		{name: "an old installed k3s that is kept gets no flag", installed: "v1.30.14+k3s1", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs"}, file: false,
			msgs: []string{"older than v1.31"}},
		{name: "upgrading to v1.36 needs it", installed: "v1.35.9+k3s1", version: "v1.36.5+k3s1", env: []string{"TN_CGROUP_FSTYPE=tmpfs", "TN_K3S_UPGRADE=true"}, file: true},
		{name: "an OS build cannot tell: set it", version: "v1.36.5+k3s1", env: []string{"OS_BUILD=true", "TN_CGROUP_FSTYPE=cgroup2fs"}, file: true,
			msgs: []string{"cannot tell whether the device uses cgroup v1"}},
		{name: "TN_K3S_CGROUPV1=true forces", version: "v1.36.5+k3s1", env: []string{"TN_K3S_CGROUPV1=true", "TN_CGROUP_FSTYPE=cgroup2fs"}, file: true},
		{name: "TN_K3S_CGROUPV1=false forces", version: "v1.36.5+k3s1", env: []string{"TN_K3S_CGROUPV1=false", "TN_CGROUP_FSTYPE=tmpfs"}, file: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed(tc.installed)
			env := append([]string{}, tc.env...)
			if tc.version != "" {
				env = append(env, "INSTALL_K3S_VERSION="+tc.version)
			}
			out, err := i.run("tn_inst_prepare server", env...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			b, rerr := os.ReadFile(i.dropin())
			if tc.file {
				if rerr != nil || string(b[strings.Index(string(b), "\nkubelet-arg+:")+1:]) != want {
					t.Errorf("drop-in: %v\n%s", rerr, b)
				}
				if !strings.Contains(string(b), "cgroup v2") {
					t.Errorf("the drop-in does not say what to do about it:\n%s", b)
				}
			} else if rerr == nil {
				t.Errorf("a drop-in was written:\n%s", b)
			}
			for _, m := range tc.msgs {
				if !strings.Contains(out, m) {
					t.Errorf("output lacks %q:\n%s", m, out)
				}
			}
			for _, m := range tc.notMsgs {
				if strings.Contains(out, m) {
					t.Errorf("output has %q:\n%s", m, out)
				}
			}
		})
	}
}

func TestCgroupV1IsIdempotentAndKeepsTheUsersKubeletArgs(t *testing.T) {
	i := newInst(t)
	i.installed("")
	userCfg := "kubelet-arg:\n  - max-pods=77\n  - \"eviction-hard=memory.available<100Mi\"\nnode-name: mine\n"
	write(t, filepath.Join(i.k3sCfg, "config.yaml"), userCfg)
	write(t, filepath.Join(i.k3sCfg, "config.yaml.d", "10-user.yaml"), "kubelet-arg+:\n  - v=2\n")
	env := []string{"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1"}
	for n := 0; n < 3; n++ {
		out, err := i.run("tn_inst_prepare server", env...)
		if err != nil {
			t.Fatalf("run %d: %v\n%s", n, err, out)
		}
		if n > 0 && !strings.Contains(out, "already in place") {
			t.Errorf("run %d did not notice the file:\n%s", n, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(i.k3sCfg, "config.yaml")); string(b) != userCfg {
		t.Errorf("config.yaml of the user was changed:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(i.k3sCfg, "config.yaml.d", "10-user.yaml")); string(b) != "kubelet-arg+:\n  - v=2\n" {
		t.Errorf("the user's drop-in was changed:\n%s", b)
	}
	m, _ := filepath.Glob(filepath.Join(i.k3sCfg, "config.yaml.d", "*"))
	if len(m) != 2 {
		t.Errorf("files: %v", m)
	}
	// ours is the last one in lexical order, so `+` appends to whatever came before
	if filepath.Base(m[len(m)-1]) != "99-teknoir-cgroupv1.yaml" {
		t.Errorf("order: %v", m)
	}
}

func TestCgroupV1DropinGoesWhenNotNeeded(t *testing.T) {
	i := newInst(t)
	i.installed("")
	env := []string{"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1"}
	i.run("tn_inst_prepare server", env...)
	if _, err := os.Stat(i.dropin()); err != nil {
		t.Fatal("not written")
	}
	// the device moved to cgroup v2
	out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=cgroup2fs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	if err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(i.dropin()); err == nil {
		t.Error("the drop-in is still there on cgroup v2")
	}
}

func TestCgroupV1RespectsTheDevicesOwnSetting(t *testing.T) {
	// the device sets the flag in a file that the running k3s reads: our drop-in goes, theirs wins
	for name, tc := range map[string]struct{ file, content string }{
		"config.yaml":       {"config.yaml", "kubelet-arg:\n  - fail-cgroupv1=false\n"},
		"config.yaml.d":     {"config.yaml.d/10-user.yaml", "kubelet-arg+:\n  - fail-cgroupv1=false\n"},
		"config.yaml.d yml": {"config.yaml.d/10-user.yml", "kubelet-arg+: [\"fail-cgroupv1=false\"]\n"},
		"trailing comment":  {"config.yaml", "kubelet-arg:\n  - 'fail-cgroupv1=false' # on purpose\n"},
	} {
		t.Run(name, func(t *testing.T) {
			i := newInst(t)
			i.installed("")
			write(t, filepath.Join(i.k3sCfg, tc.file), tc.content)
			write(t, i.dropin(), "kubelet-arg+:\n  - \"fail-cgroupv1=false\"\n")
			out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
			if err != nil || !strings.Contains(out, "set by the device itself") {
				t.Fatalf("%v\n%s", err, out)
			}
			if _, err := os.Stat(i.dropin()); err == nil {
				t.Error("our drop-in overrides the device's own setting")
			}
		})
	}
}

// A comment, and fail-cgroupv1=true, are not "the device sets it itself": our drop-in is written, and an
// existing one stays. (A device that sets fail-cgroupv1=true on cgroup v1 on purpose is not supported.)
func TestCgroupV1DeviceSettingThatDoesNotCount(t *testing.T) {
	for name, tc := range map[string]struct{ file, content string }{
		"commented in config.yaml":         {"config.yaml", "kubelet-arg:\n  # - fail-cgroupv1=false\n  - v=2\n"},
		"commented in config.yaml.d":       {"config.yaml.d/10-user.yaml", "#kubelet-arg+: [fail-cgroupv1=false]\n"},
		"commented in a yml":               {"config.yaml.d/10-user.yml", "    # fail-cgroupv1=false\n"},
		"true in config.yaml":              {"config.yaml", "kubelet-arg:\n  - fail-cgroupv1=true\n"},
		"true in config.yaml.d":            {"config.yaml.d/10-user.yaml", "kubelet-arg+:\n  - fail-cgroupv1=true\n"},
		"false only in a trailing comment": {"config.yaml", "kubelet-arg:\n  - v=2 # fail-cgroupv1=false\n"},
		"a longer name":                    {"config.yaml", "kubelet-arg:\n  - fail-cgroupv1=falsey\n"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, withDropin := range []bool{false, true} {
				i := newInst(t)
				i.installed("")
				write(t, filepath.Join(i.k3sCfg, tc.file), tc.content)
				if withDropin {
					write(t, i.dropin(), "kubelet-arg+:\n  - \"fail-cgroupv1=false\"\n")
				}
				out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
				if err != nil || strings.Contains(out, "set by the device itself") || strings.Contains(out, "Removed") {
					t.Fatalf("withDropin=%v: %v\n%s", withDropin, err, out)
				}
				if !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
					t.Errorf("withDropin=%v: no drop-in\n%s", withDropin, out)
				}
			}
		})
	}
	// the same in INSTALL_K3S_EXEC
	for _, exec := range []string{"server --kubelet-arg=fail-cgroupv1=true", "server --kubelet-arg=fail-cgroupv1=falsey"} {
		i := newInst(t)
		i.installed("")
		out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1", "INSTALL_K3S_EXEC="+exec)
		if err != nil || strings.Contains(out, "set by the device itself") || !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
			t.Errorf("%q: %v\n%s", exec, err, out)
		}
	}
}

// A fail-cgroupv1 that only the INSTALL_K3S_EXEC of the caller carries reaches the unit when the
// installer has run. Until then a k3s that restarts (the CA step restarts it) or a device that reboots
// reads no flag, so the drop-in must stay if it is there, and be written if it is not.
func TestCgroupV1DropinStaysWhenTheOnlyOtherSettingIsInstallK3sExec(t *testing.T) {
	exec := "INSTALL_K3S_EXEC=server --kubelet-arg=fail-cgroupv1=false"
	env := []string{"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1", exec}
	// present: it stays, byte for byte
	i := newInst(t)
	i.installed("")
	i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	before := string(mustRead(t, i.dropin()))
	out, err := i.run("tn_inst_prepare server", env...)
	if err != nil || !strings.Contains(out, "set by the device itself (INSTALL_K3S_EXEC)") || !strings.Contains(out, "stays until the installer") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(i.dropin()); err != nil || string(mustRead(t, i.dropin())) != before {
		t.Errorf("the drop-in did not survive tn_inst_prepare: %v", err)
	}
	if strings.Contains(out, "Removed") {
		t.Errorf("something was removed:\n%s", out)
	}
	// absent: it is written
	i = newInst(t)
	i.installed("")
	if out, err = i.run("tn_inst_prepare server", env...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
		t.Error("no drop-in although only INSTALL_K3S_EXEC has the flag")
	}
	// the flag is in a persistent file AND in INSTALL_K3S_EXEC: the file counts, ours goes
	i = newInst(t)
	i.installed("")
	write(t, filepath.Join(i.k3sCfg, "config.yaml.d", "10-user.yaml"), "kubelet-arg+:\n  - fail-cgroupv1=false\n")
	write(t, i.dropin(), "kubelet-arg+:\n  - \"fail-cgroupv1=false\"\n")
	if out, err = i.run("tn_inst_prepare server", env...); err != nil || !strings.Contains(out, "10-user.yaml") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(i.dropin()); err == nil {
		t.Error("our drop-in stays although a persistent file has the flag")
	}
}

// With no flag from the device at all the drop-in is written.
func TestCgroupV1DropinIsWrittenWithoutAFlagFromTheDevice(t *testing.T) {
	i := newInst(t)
	i.installed("")
	out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1", "INSTALL_K3S_EXEC=server --node-label=a=b")
	if err != nil || !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// An installed k3s older than v1.31 that is being upgraded cannot start with the flag: it is not
// written (and an existing one is removed) before the installer, and tn_inst_finish writes it after.
func TestCgroupV1FlagWaitsForTheInstallerWhenTheInstalledK3sIsTooOld(t *testing.T) {
	for _, withDropin := range []bool{false, true} {
		i := newInst(t)
		i.installed("v1.30.14+k3s1")
		if withDropin {
			write(t, i.dropin(), "kubelet-arg+:\n  - \"fail-cgroupv1=false\"\n")
		}
		env := []string{"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.31.4+k3s1", "TN_K3S_UPGRADE=true", "TN_K3S_PIN_SOURCE=flag"}
		out, err := i.run("tn_inst_prepare server", env...)
		if err != nil || !strings.Contains(out, "after the installer has upgraded it") {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, err := os.Stat(i.dropin()); err == nil {
			t.Errorf("withDropin=%v: the old kubelet would read the flag before the installer ran\n%s", withDropin, out)
		}
		out, err = i.run("tn_inst_prepare server; tn_inst_finish", env...)
		if err != nil || !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
			t.Fatalf("withDropin=%v: not written after the installer: %v\n%s", withDropin, err, out)
		}
		// finish without a deferred write does nothing, and prepare resets it
		i2 := newInst(t)
		i2.installed("")
		if out, err = i2.run("tn_inst_prepare server; tn_inst_finish", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1"); err != nil || strings.Contains(out, "held back") {
			t.Errorf("%v\n%s", err, out)
		}
	}
}

// The installer starts the NEW release as soon as it has rewritten the unit, and v1.35 and newer do not
// start on cgroup v1 without the flag. So when the drop-in has to wait (the installed k3s is older than
// v1.31) and the release is not known to be older than v1.35, the flag is on INSTALL_K3S_EXEC from the first
// start; tn_inst_finish writes the drop-in after the installer.
func TestCgroupV1FlagIsOnTheInstallerCommandWhenTheDropinHasToWait(t *testing.T) {
	const flag = "--kubelet-arg=fail-cgroupv1=false"
	show := `; echo "EXEC=[${INSTALL_K3S_EXEC-unset}]"; if [ -e "$TN_K3S_CONFIG_DIR/config.yaml.d/99-teknoir-cgroupv1.yaml" ]; then echo DROPIN=yes; else echo DROPIN=no; fi`
	for _, tc := range []struct {
		name     string
		env      []string
		wantExec string // the whole INSTALL_K3S_EXEC after tn_inst_prepare
		msg      string
	}{
		{name: "channel upgrade", env: []string{"INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server --tls-san a"},
			wantExec: "server --tls-san a " + flag, msg: "may need the flag on its first start"},
		{name: "channel upgrade, no exec at all", env: []string{"INSTALL_K3S_CHANNEL=latest", "TN_K3S_UPGRADE=true"},
			wantExec: flag},
		{name: "a forced jump to v1.36.5", env: []string{"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_K3S_UPGRADE=true", "TN_K3S_FORCE=true", "INSTALL_K3S_EXEC=agent"},
			wantExec: "agent " + flag},
		{name: "exact v1.35.0 (a jump too, forced)", env: []string{"INSTALL_K3S_VERSION=v1.35.0+k3s1", "TN_K3S_UPGRADE=true", "TN_K3S_FORCE=true", "INSTALL_K3S_EXEC=server"},
			wantExec: "server " + flag},
		{name: "exact v1.31.14 does not need it on its first start", env: []string{"INSTALL_K3S_VERSION=v1.31.14+k3s1", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server"},
			wantExec: "server"},
		{name: "a channel named v1.31 does not need it either", env: []string{"INSTALL_K3S_CHANNEL=v1.31", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server"},
			wantExec: "server"},
		{name: "already on the command of the caller: not twice", env: []string{"INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server " + flag},
			wantExec: "server " + flag, msg: "already has fail-cgroupv1=false"},
		{name: "already on the command of the caller, spelled with a space", env: []string{"INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server --kubelet-arg fail-cgroupv1=false"},
			wantExec: "server --kubelet-arg fail-cgroupv1=false"},
		{name: "true on the command of the caller is not the flag", env: []string{"INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server --kubelet-arg=fail-cgroupv1=true"},
			wantExec: "server --kubelet-arg=fail-cgroupv1=true " + flag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed("v1.30.14+k3s1")
			env := append([]string{"TN_CGROUP_FSTYPE=tmpfs", "TN_K3S_PIN_SOURCE=flag"}, tc.env...)
			out, err := i.run("tn_inst_prepare server"+show, env...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(out, "EXEC=["+tc.wantExec+"]\n") {
				t.Errorf("want INSTALL_K3S_EXEC %q:\n%s", tc.wantExec, out)
			}
			if strings.Count(out, "fail-cgroupv1=false") > 0 && strings.Count(strings.SplitN(strings.SplitN(out, "EXEC=[", 2)[1], "]", 2)[0], "fail-cgroupv1=false") > 1 {
				t.Errorf("the flag is on the command twice:\n%s", out)
			}
			if !strings.Contains(out, "DROPIN=no") {
				t.Errorf("the drop-in must wait for the installer:\n%s", out)
			}
			if tc.msg != "" && !strings.Contains(out, tc.msg) {
				t.Errorf("output lacks %q:\n%s", tc.msg, out)
			}
			// after the installer: the drop-in
			out, err = i.run("tn_inst_prepare server; tn_inst_finish"+show, env...)
			if err != nil || !strings.Contains(out, "DROPIN=yes") || !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
				t.Errorf("not written after the installer: %v\n%s", err, out)
			}
		})
	}
	// the exec of a re-run, when the installed k3s is new enough, is left alone
	i := newInst(t)
	i.installed("v1.34.11+k3s1")
	out, err := i.run("tn_inst_prepare server"+show, "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_EXEC=server")
	if err != nil || !strings.Contains(out, "EXEC=[server]\n") {
		t.Errorf("%v\n%s", err, out)
	}
	// cgroup v2: nothing at all
	i = newInst(t)
	i.installed("v1.30.14+k3s1")
	out, err = i.run("tn_inst_prepare server"+show, "TN_CGROUP_FSTYPE=cgroup2fs", "INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server")
	if err != nil || !strings.Contains(out, "EXEC=[server]\n") || !strings.Contains(out, "DROPIN=no") {
		t.Errorf("%v\n%s", err, out)
	}
	// the device already has the flag in a file k3s reads: no command line flag either
	i = newInst(t)
	i.installed("v1.30.14+k3s1")
	write(t, filepath.Join(i.k3sCfg, "config.yaml.d", "10-user.yaml"), "kubelet-arg+:\n  - fail-cgroupv1=false\n")
	out, err = i.run("tn_inst_prepare server"+show, "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_CHANNEL=stable", "TN_K3S_UPGRADE=true", "INSTALL_K3S_EXEC=server")
	if err != nil || !strings.Contains(out, "EXEC=[server]\n") {
		t.Errorf("%v\n%s", err, out)
	}
}

// the kubelet flag is only ever added for the agent and the server script, through the same call
func TestAgentRoleUsesTheSameCgroupLogic(t *testing.T) {
	i := newInst(t)
	i.installed("")
	if out, err := i.run("tn_inst_prepare agent", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_K3S_PIN_SOURCE=env", "K3S_URL=https://srv:6443", "TN_TEST_SERVER_VERSION=v1.36.5+k3s1"); err != nil || !strings.Contains(out, "(agent)") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(i.dropin()); err != nil {
		t.Error("not written for the agent")
	}
}

func TestRealCgroupDetectionDoesNotFail(t *testing.T) {
	// no override: the host decides, and whatever it is the call must succeed
	i := newInst(t)
	i.installed("")
	if out, err := i.run("tn_inst_prepare server", "INSTALL_K3S_VERSION=v1.36.5+k3s1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// ---------------------------------------------------------------- config.yaml

func TestConfigEnsure(t *testing.T) {
	i := newInst(t)
	f := filepath.Join(i.dir, "etc", "config.yaml")
	run := func(key, text string) string {
		t.Helper()
		out, err := i.run(`tn_inst_config_ensure "$F" "$K" "$T"`, "F="+f, "K="+key, "T="+text)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return out
	}
	read := func() string { b, _ := os.ReadFile(f); return string(b) }

	run("disable", "disable:\n  - traefik")
	run("node-name", "node-name: teknoir-master")
	if got := read(); got != "disable:\n  - traefik\nnode-name: teknoir-master\n" {
		t.Fatalf("fresh file:\n%s", got)
	}
	// again: nothing changes
	if out := run("disable", "disable:\n  - traefik"); !strings.Contains(out, "already sets") {
		t.Errorf("no message:\n%s", out)
	}
	run("node-name", "node-name: other")
	if got := read(); got != "disable:\n  - traefik\nnode-name: teknoir-master\n" {
		t.Fatalf("second run:\n%s", got)
	}

	// a file of the device owner: kept as it is, a missing key is appended on its own line
	write(t, f, "# mine\nkubelet-arg:\n  - max-pods=77\nnode-name: custom\ndisable+:\n  - servicelb") // no trailing newline
	run("disable", "disable:\n  - traefik")
	run("node-name", "node-name: teknoir-master")
	run("data-dir", "data-dir: /opt/k3s")
	want := "# mine\nkubelet-arg:\n  - max-pods=77\nnode-name: custom\ndisable+:\n  - servicelb\ndata-dir: /opt/k3s\n"
	if got := read(); got != want {
		t.Errorf("owner's file:\n%s\nwant:\n%s", got, want)
	}

	// only a TOP-LEVEL key counts; CRLF is fine
	write(t, f, "x:\n  node-name: nested\r\nother: 1\r\n")
	run("node-name", "node-name: teknoir-master")
	if got := read(); !strings.HasSuffix(got, "node-name: teknoir-master\n") {
		t.Errorf("a nested key was taken for the top-level one:\n%s", got)
	}
	write(t, f, "node-name: crlf\r\n")
	if out := run("node-name", "node-name: teknoir-master"); !strings.Contains(out, "already sets") {
		t.Errorf("crlf key not found:\n%s", out)
	}
	// a comment that mentions the key is not the key
	write(t, f, "# node-name: old\n")
	run("node-name", "node-name: teknoir-master")
	if got := read(); got != "# node-name: old\nnode-name: teknoir-master\n" {
		t.Errorf("comment:\n%s", got)
	}
}

// ---------------------------------------------------------------- the generated scripts

func TestScriptsEmbedTheInstallLibraryAndCallItBeforeTheInstaller(t *testing.T) {
	for name, tpl := range map[string]*struct{ out string }{
		"server": {renderK3S(t, ServerTemplate, "")},
		"agent":  {renderK3S(t, AgentTemplate, "")},
	} {
		out := tpl.out
		call := "\ntn_inst_prepare " + name + "\n"
		for _, want := range []string{"tn_inst_prepare()", "tn_inst_plan()", "tn_inst_cgroup()", call} {
			if !strings.Contains(out, want) {
				t.Errorf("%s script lacks %q", name, want)
			}
		}
		iPrep := strings.Index(out, call)
		iPin := strings.Index(out, "\nexport INSTALL_K3S_VERSION=")
		iInst := strings.Index(out, "\n$SUDO ./k3s_installer.sh")
		if !(iPin >= 0 && iPin < iPrep && iPrep < iInst) {
			t.Errorf("%s: the order is pin %d, tn_inst_prepare %d, installer %d", name, iPin, iPrep, iInst)
		}
		// the library comes before its first use
		if strings.Index(out, "tn_inst_prepare()") > iPrep {
			t.Errorf("%s: library after the call", name)
		}
	}
	// the docker server variant installs no k3s at all
	if strings.Contains(DockerServerTemplateStr, "tn-k3s-install") {
		t.Error("the docker variant embeds the install library")
	}
	// the server writes config.yaml without clobbering it
	srv := renderK3S(t, ServerTemplate, "")
	if strings.Contains(srv, "tee ${K3S_CONFIG_PATH}/config.yaml") {
		t.Error("the server script still overwrites config.yaml")
	}
	for _, want := range []string{`tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml disable`, `tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml node-name "node-name: teknoir-master"`} {
		if !strings.Contains(srv, want) {
			t.Errorf("server script lacks %q", want)
		}
	}
}

func renderData(t *testing.T, d TemplateData) (server, agent string) {
	t.Helper()
	var s, a bytes.Buffer
	if err := ServerTemplate.Execute(&s, d); err != nil {
		t.Fatal(err)
	}
	if err := AgentTemplate.Execute(&a, d); err != nil {
		t.Fatal(err)
	}
	return s.String(), a.String()
}

func TestUpgradeAndForceDefaultsInTheScripts(t *testing.T) {
	base := TemplateData{Domain: "test.cloud", DeviceID: "dev-1", K3SToken: "tok"}
	s, a := renderData(t, base)
	for name, out := range map[string]string{"server": s, "agent": a} {
		if len(exportsOf(out, "TN_K3S_UPGRADE"))+len(exportsOf(out, "TN_K3S_FORCE")) != 0 {
			t.Errorf("%s: upgrade/force are on by default", name)
		}
	}
	up := base
	up.K3SUpgrade = true
	s, a = renderData(t, up)
	for name, out := range map[string]string{"server": s, "agent": a} {
		if got := exportsOf(out, "TN_K3S_UPGRADE"); len(got) != 1 || got[0] != `export TN_K3S_UPGRADE="${TN_K3S_UPGRADE:-true}"` {
			t.Errorf("%s upgrade: %q", name, got)
		}
		if len(exportsOf(out, "TN_K3S_FORCE")) != 0 {
			t.Errorf("%s: force without --force", name)
		}
	}
	fo := up
	fo.K3SForce = true
	s, a = renderData(t, fo)
	for name, out := range map[string]string{"server": s, "agent": a} {
		if got := exportsOf(out, "TN_K3S_FORCE"); len(got) != 1 || got[0] != `export TN_K3S_FORCE="${TN_K3S_FORCE:-true}"` {
			t.Errorf("%s force: %q", name, got)
		}
		// set before tn_inst_prepare runs
		if strings.Index(out, "export TN_K3S_FORCE=") > strings.Index(out, "\ntn_inst_prepare ") {
			t.Errorf("%s: force is set after tn_inst_prepare", name)
		}
	}
	// every combination of pins parses
	for _, d := range []TemplateData{up, fo, {K3SForce: true}} {
		for _, v := range []string{"", "stable", "latest", "v1.34.11+k3s1"} {
			d.K3SVersion = v
			s, a := renderData(t, d)
			for name, out := range map[string]string{"server": s, "agent": a} {
				f := filepath.Join(t.TempDir(), "s.sh")
				if err := os.WriteFile(f, []byte(out), 0o600); err != nil {
					t.Fatal(err)
				}
				for _, sh := range []string{"sh", "dash"} {
					if _, err := exec.LookPath(sh); err != nil {
						continue
					}
					if o, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
						t.Errorf("%s %s %q: %v\n%s", sh, name, v, err, o)
					}
				}
			}
		}
	}
}

// What the generated script does between the pin and the installer, run for real with the
// rendered pin lines: the defaults come from the template, the environment overrides them.
func TestRenderedPinThroughThePolicy(t *testing.T) {
	needTool(t, "sh")
	for _, tc := range []struct {
		name      string
		data      TemplateData
		installed string
		env       []string
		wantRC    int
		want      string
	}{
		{"fresh device, default", TemplateData{}, "", nil, 0, "version=v1.36.5+k3s1 channel=unset action=install"},
		{"re-run keeps", TemplateData{}, "v1.34.11+k3s1", nil, 0, "version=v1.34.11+k3s1 channel=unset action=keep"},
		{"--k3s-upgrade, one minor", TemplateData{K3SUpgrade: true}, "v1.35.9+k3s1", nil, 0, "version=v1.36.5+k3s1 channel=unset action=upgrade"},
		{"--k3s-upgrade, two minors", TemplateData{K3SUpgrade: true}, "v1.34.11+k3s1", nil, 1, ""},
		{"--k3s-upgrade --force, two minors", TemplateData{K3SUpgrade: true, K3SForce: true}, "v1.34.11+k3s1", nil, 0, "version=v1.36.5+k3s1 channel=unset action=upgrade"},
		{"--k3s-upgrade, env says no", TemplateData{K3SUpgrade: true}, "v1.35.9+k3s1", []string{"TN_K3S_UPGRADE=false"}, 0, "version=v1.35.9+k3s1 channel=unset action=keep"},
		{"env opts in", TemplateData{}, "v1.35.9+k3s1", []string{"TN_K3S_UPGRADE=true"}, 0, "version=v1.36.5+k3s1 channel=unset action=upgrade"},
		{"env pin beats the template pin on a new device", TemplateData{}, "", []string{"INSTALL_K3S_VERSION=v1.34.11+k3s1"}, 0, "version=v1.34.11+k3s1 channel=unset action=install"},
		{"latest channel, installed", TemplateData{K3SVersion: "latest"}, "v1.35.9+k3s1", nil, 0, "version=v1.35.9+k3s1 channel=unset action=keep"},
		{"stable, fresh", TemplateData{K3SVersion: "stable"}, "", nil, 0, "version=unset channel=unset action=install"},
		{"latest, fresh", TemplateData{K3SVersion: "latest"}, "", nil, 0, "version=unset channel=latest action=install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var part bytes.Buffer
			if err := ServerTemplate.ExecuteTemplate(&part, "k3s_version.sh.tmpl", tc.data); err != nil {
				t.Fatal(err)
			}
			i := newInst(t)
			i.installed(tc.installed)
			out, err := i.run(part.String()+"\ntn_inst_prepare server; "+resultLine, append([]string{"TN_CGROUP_FSTYPE=cgroup2fs"}, tc.env...)...)
			if rc := exitCode(err); rc != tc.wantRC {
				t.Fatalf("rc=%d\n%s", rc, out)
			}
			if tc.wantRC == 0 {
				if got := result(t, out); !strings.HasPrefix(got, tc.want+" ") && got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
