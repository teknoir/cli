package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the follow-up review: an agent follows the k3s release of its server, the cgroup flag
// is in place before the CA step restarts k3s, the restart and rotate-ca wait for a slow k3s, a k3s
// whose version cannot be read is not touched, the flag goes in from v1.31 (atomically), a refusal
// says what this run changed, and the data dir must agree with config.yaml.

const agentURL = "https://srv:6443"

// ---------------------------------------------------------------- the agent follows its server

func TestAgentFollowsItsServer(t *testing.T) {
	const (
		v134 = "v1.34.11+k3s1"
		v135 = "v1.35.9+k3s1"
		v136 = "v1.36.5+k3s1"
	)
	for _, tc := range []struct {
		name      string
		installed string
		pin       string // INSTALL_K3S_VERSION, "" = unset
		source    string // TN_K3S_PIN_SOURCE
		server    string // the release the server script published ("" = nothing published: 404)
		anon      string // the release an anonymous /version gives ("" = 401/404)
		raw       string // a raw content of the published file instead
		anonBody  string // a raw answer of /version instead
		env       []string
		noURL     bool
		wantRC    int
		want      string // the RESULT line
		msgs      []string
		notMsgs   []string
		curl      int // calls of curl, -1 = not checked
	}{
		// --- a new agent: the release of the server, never the pin of the script
		{name: "server older than the default pin", pin: v136, source: "default", server: v134,
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1,
			msgs: []string{"runs " + v134, "the script's default " + v136 + " is not used", "the agent gets the release of its server"}},
		{name: "server newer than the default pin", pin: v134, source: "default", server: v136,
			want: "version=" + v136 + " channel=unset action=install effective=" + v136, curl: 1},
		{name: "the default pin equals the server", pin: v136, source: "default", server: v136,
			want: "version=" + v136 + " channel=unset action=install effective=" + v136, curl: 1, notMsgs: []string{"is not used"}},
		{name: "no pin at all (stable)", source: "default", server: v135,
			want: "version=" + v135 + " channel=unset action=install effective=" + v135, curl: 1},
		{name: "the latest channel is not followed either", source: "default", server: v135, env: []string{"INSTALL_K3S_CHANNEL=latest"},
			want: "version=" + v135 + " channel=unset action=install effective=" + v135, curl: 1},
		{name: "an anonymous /version, one line of JSON", pin: v136, source: "default", anonBody: `{"major":"1","minor":"33","gitVersion":"v1.33.4+k3s1","goVersion":"go1.24"}`,
			want: "version=v1.33.4+k3s1 channel=unset action=install effective=v1.33.4+k3s1", curl: 2, msgs: []string{"read from " + agentURL + "/version"}},
		{name: "an anonymous /version", pin: v136, source: "default", anon: v135,
			want: "version=" + v135 + " channel=unset action=install effective=" + v135, curl: 2},
		{name: "the published file wins over /version", pin: v136, source: "default", server: v134, anon: v135,
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1, msgs: []string{"read from " + agentURL + "/static/teknoir/k3s-version"}},
		{name: "the published file with blanks and a CRLF", pin: v136, source: "default", raw: " v1.34.11+k3s1 \r",
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1},
		{name: "a trailing slash in K3S_URL", pin: v136, source: "default", server: v134, env: []string{"K3S_URL=" + agentURL + "/"},
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1},
		{name: "a server that fails twice and then answers", pin: v136, source: "default", server: v134, env: []string{"TN_TEST_FAIL_N=2"},
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 3},

		// --- a release that was asked for
		{name: "asked for: the same as the server", pin: v134, source: "flag", server: v134,
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1, msgs: []string{"is the release of the server"}},
		{name: "asked for: older than the server is installed with a warning", pin: v134, source: "env", server: v136,
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 1, msgs: []string{"OLDER than the release of the server", "installed as asked"}},
		{name: "asked for: newer than the server is refused", pin: v136, source: "flag", server: v134, wantRC: 1, curl: 1,
			msgs: []string{"refusing to install k3s " + v136 + " on this agent", "runs the older " + v134, "must not be newer than the apiserver", "TN_K3S_FORCE=true", "Nothing was changed"}},
		{name: "asked for: newer than the server, forced", pin: v136, source: "env", server: v134, env: []string{"TN_K3S_FORCE=true"},
			want: "version=" + v136 + " channel=unset action=install effective=" + v136, curl: 1, msgs: []string{"NEWER than the release of the server", "TN_K3S_FORCE=true"}},
		{name: "asked for: a channel cannot be compared, the server wins", source: "flag", server: v135, env: []string{"INSTALL_K3S_CHANNEL=latest"},
			want: "version=" + v135 + " channel=unset action=install effective=" + v135, curl: 1, msgs: []string{"not an exact release", "the release of the server is installed instead"}},

		// --- the server cannot be asked
		{name: "server down, default pin: refused", pin: v136, source: "default", wantRC: 1, curl: 2, env: []string{"TN_TEST_SERVER_DOWN=1", "TN_K3S_SERVER_VERSION_TRIES=2"},
			msgs: []string{"cannot read the k3s release of the server at " + agentURL, "does not guess", "Is the server up", "INSTALL_K3S_VERSION=v1.xx.y+k3sN", "TN_K3S_FORCE=true", "Nothing was changed"}},
		{name: "server down, default pin, three tries by default", pin: v136, source: "default", wantRC: 1, curl: 3, env: []string{"TN_TEST_SERVER_DOWN=1"}},
		{name: "server down, asked for an exact release: installed with a warning", pin: v134, source: "flag", env: []string{"TN_TEST_SERVER_DOWN=1"},
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 3, msgs: []string{"cannot read the k3s release of the server", "installed as asked"}},
		{name: "server down, forced", pin: v136, source: "default", env: []string{"TN_TEST_SERVER_DOWN=1", "TN_K3S_FORCE=true"},
			want: "version=" + v136 + " channel=unset action=install effective=" + v136, curl: 3, msgs: []string{"TN_K3S_FORCE=true", "installed anyway"}},
		{name: "server down, no pin, forced: the installer decides", source: "default", env: []string{"TN_TEST_SERVER_DOWN=1", "TN_K3S_FORCE=true"},
			want: "version=unset channel=unset action=install effective=", curl: 3},
		{name: "a server that does not publish its release (bootstrapped by an older script)", pin: v136, source: "default", wantRC: 1, curl: 2,
			msgs:    []string{"cannot read the k3s release of the server", "does not publish its release", "run the server script again", "does not allow an anonymous /version", "INSTALL_K3S_VERSION=v1.xx.y+k3sN", "Nothing was changed"},
			notMsgs: []string{"Is the server up"}},
		{name: "an anonymous /version that is not k3s (rke2)", pin: v136, source: "default", anonBody: `{"gitVersion":"v1.34.11+rke2r1"}`, wantRC: 1, curl: 2},
		{name: "a published file without a release", pin: v136, source: "default", raw: `<html>nope</html>`, wantRC: 1, curl: 2},
		{name: "a blank published file", pin: v136, source: "default", raw: ` `, wantRC: 1, curl: 2},
		{name: "a release without +k3s is no k3s release", pin: v136, source: "default", raw: `v1.34.11`, wantRC: 1, curl: 2},
		{name: "a server that does not publish, asked for an exact release", pin: v134, source: "env",
			want: "version=" + v134 + " channel=unset action=install effective=" + v134, curl: 2, msgs: []string{"installed as asked"}},
		{name: "K3S_URL is not set", pin: v136, source: "default", noURL: true, wantRC: 1, curl: 0, msgs: []string{"K3S_URL is not set"}},
		{name: "an OS build installs the pin without asking", pin: v136, source: "default", env: []string{"OS_BUILD=true"},
			want: "version=" + v136 + " channel=unset action=install effective=" + v136, curl: 0, msgs: []string{"OS build"}},

		// --- an agent that is installed
		{name: "installed: kept, the server is not asked", installed: v134, pin: v136, source: "default", server: v136,
			want: "version=" + v134 + " channel=unset action=keep effective=" + v134, curl: 0},
		{name: "installed, server down: kept all the same", installed: v134, pin: v136, source: "default",
			env: []string{"TN_TEST_SERVER_DOWN=1"}, want: "version=" + v134 + " channel=unset action=keep effective=" + v134, curl: 0},
		{name: "installed, upgrade: to the release of the server", installed: v134, pin: v136, source: "default", server: v135, env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + v135 + " channel=unset action=upgrade effective=" + v135, curl: 1},
		{name: "installed, upgrade, already there", installed: v135, pin: v136, source: "default", server: v135, env: []string{"TN_K3S_UPGRADE=true"},
			want: "version=" + v135 + " channel=unset action=same effective=" + v135, curl: 1},
		{name: "installed, upgrade over two minors is still refused", installed: v134, pin: v136, source: "default", server: v136, env: []string{"TN_K3S_UPGRADE=true"},
			wantRC: 1, curl: 1, msgs: []string{"more than one minor version"}},
		{name: "installed newer than the server: no downgrade", installed: v136, pin: v136, source: "default", server: v134, env: []string{"TN_K3S_UPGRADE=true"},
			wantRC: 1, curl: 1, msgs: []string{"refusing to downgrade"}},
		{name: "installed, upgrade, server down: refused", installed: v134, pin: v136, source: "default", env: []string{"TN_K3S_UPGRADE=true", "TN_TEST_SERVER_DOWN=1"},
			wantRC: 1, curl: 3, msgs: []string{"cannot read the k3s release of the server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed(tc.installed)
			env := append([]string{"TN_CGROUP_FSTYPE=cgroup2fs", "TN_K3S_PIN_SOURCE=" + tc.source}, tc.env...)
			if !tc.noURL && !hasPrefix(env, "K3S_URL=") {
				env = append(env, "K3S_URL="+agentURL)
			}
			if tc.pin != "" {
				env = append(env, "INSTALL_K3S_VERSION="+tc.pin)
			}
			if tc.server != "" {
				env = append(env, "TN_TEST_PUBLISHED="+tc.server)
			}
			if tc.raw != "" {
				env = append(env, "TN_TEST_PUBLISHED="+tc.raw)
			}
			if tc.anon != "" {
				env = append(env, "TN_TEST_SERVER_VERSION="+tc.anon)
			}
			if tc.anonBody != "" {
				env = append(env, "TN_TEST_SERVER_BODY="+tc.anonBody)
			}
			out, err := i.run("tn_inst_prepare agent; "+resultLine, env...)
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
			calls := i.curlCalls()
			if tc.curl >= 0 {
				if n := strings.Count(calls, "\n"); n != tc.curl {
					t.Errorf("curl was called %d times, want %d:\n%s", n, tc.curl, calls)
				}
			}
			if tc.curl > 0 {
				want := agentURL + "/static/teknoir/k3s-version"
				if !strings.Contains(calls, want+"\n") {
					t.Errorf("curl did not ask %s:\n%s", want, calls)
				}
				// fails on HTTP errors, no certificate check, bounded
				if !strings.Contains(calls, "-fsk --max-time 10 ") {
					t.Errorf("curl flags: %s", calls)
				}
			}
		})
	}
}

func hasPrefix(list []string, p string) bool {
	for _, e := range list {
		if strings.HasPrefix(e, p) {
			return true
		}
	}
	return false
}

// A server never asks anybody: it installs the pin, and the cgroup logic follows the release
// that is installed, which for an agent is the one of the server.
func TestServerRoleNeverAsksAndAgentCgroupFollowsTheInstalledRelease(t *testing.T) {
	i := newInst(t)
	i.installed("")
	out, err := i.run("tn_inst_prepare server; "+resultLine, "TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL="+agentURL, "TN_K3S_PIN_SOURCE=default",
		"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_TEST_PUBLISHED=v1.34.11+k3s1")
	if err != nil || result(t, out) != "version=v1.36.5+k3s1 channel=unset action=install effective=v1.36.5+k3s1" {
		t.Fatalf("%v\n%s", err, out)
	}
	if i.curlCalls() != "" {
		t.Errorf("a server script asked a server: %s", i.curlCalls())
	}

	// cgroup v1 agent: the flag follows the release that will run, not the pin of the script
	for _, tc := range []struct {
		server string
		file   bool
	}{
		{"v1.30.14+k3s1", false}, // the kubelet has no such flag: none written, although the default pin is v1.36.5
		{"v1.31.0+k3s1", true},
		{"v1.34.11+k3s1", true},
		{"v1.36.5+k3s1", true},
	} {
		i := newInst(t)
		i.installed("")
		out, err := i.run("tn_inst_prepare agent", "TN_CGROUP_FSTYPE=tmpfs", "K3S_URL="+agentURL, "TN_K3S_PIN_SOURCE=default",
			"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_TEST_PUBLISHED="+tc.server)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.server, err, out)
		}
		if _, serr := os.Stat(i.dropin()); (serr == nil) != tc.file {
			t.Errorf("agent on %s: drop-in exists=%v, want %v\n%s", tc.server, serr == nil, tc.file, out)
		}
	}
}

// wget is used when there is no curl
func TestServerVersionWithWgetOnly(t *testing.T) {
	i := newInst(t)
	i.installed("")
	// a PATH with the usual tools, but no curl (and wget only in the first one)
	mkbin := func(name string) string {
		bin := filepath.Join(i.dir, name)
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"sh", "sed", "tr", "cat", "find", "rm", "mv", "mkdir", "sleep", "awk", "stat", "grep", "tail", "dirname", "cut", "date", "env", "id", "head", "sort"} {
			if p, err := exec.LookPath(tool); err == nil {
				if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
		}
		return bin
	}
	bin := mkbin("wgetonly")
	wlog := filepath.Join(i.dir, "wget.log")
	write(t, filepath.Join(bin, "wget"), "#!/bin/sh\necho \"$*\" >> '"+wlog+"'\nfor a in \"$@\"; do url=$a; done\ncase \"$url\" in */static/teknoir/k3s-version) echo v1.35.9+k3s1; exit 0 ;; esac\nexit 8\n")
	if err := os.Chmod(filepath.Join(bin, "wget"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := i.run("tn_inst_prepare agent; "+resultLine, "PATH="+bin, "TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL="+agentURL,
		"TN_K3S_PIN_SOURCE=default", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	if err != nil || result(t, out) != "version=v1.35.9+k3s1 channel=unset action=install effective=v1.35.9+k3s1" {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(readLog(wlog), agentURL+"/static/teknoir/k3s-version") || !strings.Contains(readLog(wlog), "--no-check-certificate") {
		t.Errorf("wget: %s", readLog(wlog))
	}
	// and neither: refused, not guessed
	out, err = i.run("tn_inst_prepare agent; "+resultLine, "PATH="+mkbin("neither"), "TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL="+agentURL,
		"TN_K3S_PIN_SOURCE=default", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	if exitCode(err) != 1 || strings.Contains(out, "RESULT") || !strings.Contains(out, "cannot read the k3s release of the server") {
		t.Errorf("went on without curl and wget:\n%s", out)
	}
}

// The generated agent script: the template says who chose the release, and the policy does the rest.
func TestRenderedAgentPinSource(t *testing.T) {
	needTool(t, "sh")
	for _, tc := range []struct {
		name   string
		data   TemplateData
		env    []string
		server string
		want   string
	}{
		{"default: the server wins", TemplateData{}, nil, "v1.34.11+k3s1", "version=v1.34.11+k3s1 channel=unset action=install"},
		{"default, server newer", TemplateData{}, nil, "v1.37.1+k3s1", "version=v1.37.1+k3s1 channel=unset action=install"},
		{"tnctl --k3s-version newer than the server: refused", TemplateData{K3SVersion: "v1.36.5+k3s1", K3SVersionExplicit: true}, nil, "v1.34.11+k3s1", ""},
		{"tnctl --k3s-version equal", TemplateData{K3SVersion: "v1.34.11+k3s1", K3SVersionExplicit: true}, nil, "v1.34.11+k3s1", "version=v1.34.11+k3s1 channel=unset action=install"},
		{"INSTALL_K3S_VERSION in the environment, newer: refused", TemplateData{}, []string{"INSTALL_K3S_VERSION=v1.37.1+k3s1"}, "v1.34.11+k3s1", ""},
		{"INSTALL_K3S_VERSION in the environment, older", TemplateData{}, []string{"INSTALL_K3S_VERSION=v1.33.4+k3s1"}, "v1.34.11+k3s1", "version=v1.33.4+k3s1 channel=unset action=install"},
		{"--k3s-version latest: the server wins", TemplateData{K3SVersion: "latest", K3SVersionExplicit: true}, nil, "v1.34.11+k3s1", "version=v1.34.11+k3s1 channel=unset action=install"},
		{"--k3s-version stable (no pin): the server wins", TemplateData{K3SVersion: "stable", K3SVersionExplicit: true}, nil, "v1.34.11+k3s1", "version=v1.34.11+k3s1 channel=unset action=install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var part bytes.Buffer
			if err := AgentTemplate.ExecuteTemplate(&part, "k3s_version.sh.tmpl", tc.data); err != nil {
				t.Fatal(err)
			}
			i := newInst(t)
			i.installed("")
			out, err := i.run(part.String()+"\ntn_inst_prepare agent; "+resultLine, append([]string{"TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL=" + agentURL, "TN_TEST_PUBLISHED=" + tc.server}, tc.env...)...)
			if tc.want == "" {
				if exitCode(err) != 1 || !strings.Contains(out, "refusing to install") {
					t.Fatalf("not refused: %v\n%s", err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := result(t, out); !strings.HasPrefix(got, tc.want+" ") {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// the source that the template records
	for _, tc := range []struct {
		data TemplateData
		env  []string
		want string
	}{
		{TemplateData{}, nil, "default"},
		{TemplateData{K3SVersionExplicit: true}, nil, "flag"},
		{TemplateData{}, []string{"INSTALL_K3S_VERSION=v1.34.11+k3s1"}, "env"},
		{TemplateData{K3SVersionExplicit: true}, []string{"INSTALL_K3S_CHANNEL=latest"}, "env"},
	} {
		var part bytes.Buffer
		if err := AgentTemplate.ExecuteTemplate(&part, "k3s_version.sh.tmpl", tc.data); err != nil {
			t.Fatal(err)
		}
		i := newInst(t)
		out, err := i.run(part.String()+"\necho \"SOURCE=$TN_K3S_PIN_SOURCE\"", tc.env...)
		if err != nil || !strings.Contains(out, "SOURCE="+tc.want+"\n") {
			t.Errorf("%+v %v: %v\n%s", tc.data, tc.env, err, out)
		}
	}
}

// ---------------------------------------------------------------- a k3s whose version cannot be read

func TestUnreadableK3sVersionIsNotInstalledOver(t *testing.T) {
	const pin = "INSTALL_K3S_VERSION=v1.36.5+k3s1"
	garbage := func(i *inst) {
		i.bin = filepath.Join(i.dir, "garbage-k3s")
		write(i.t, i.bin, "#!/bin/sh\necho 'this is not k3s'\nexit 0\n")
		_ = os.Chmod(i.bin, 0o755)
	}
	failing := func(i *inst) {
		i.bin = filepath.Join(i.dir, "failing-k3s")
		write(i.t, i.bin, "#!/bin/sh\necho 'Segmentation fault' >&2\nexit 139\n")
		_ = os.Chmod(i.bin, 0o755)
	}
	datastore := func(i *inst) {
		if err := os.MkdirAll(filepath.Join(i.dir, "data", "server", "db"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(*inst)
		role  string
	}{
		{"binary that prints something else", garbage, "server"},
		{"binary that crashes", failing, "server"},
		{"datastore without a binary", datastore, "server"},
		{"agent: binary that prints something else", garbage, "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed("")
			tc.setup(i)
			env := []string{"TN_CGROUP_FSTYPE=cgroup2fs", "TN_K3S_PIN_SOURCE=flag", "K3S_URL=" + agentURL, "TN_TEST_PUBLISHED=v1.36.5+k3s1", pin}
			out, err := i.run("tn_inst_prepare "+tc.role+"; "+resultLine, env...)
			if exitCode(err) != 1 || strings.Contains(out, "RESULT") {
				t.Fatalf("not refused (rc=%d):\n%s", exitCode(err), out)
			}
			for _, m := range []string{"version cannot be read", "does not install anything", "TN_K3S_FORCE=true", "Nothing was changed"} {
				if !strings.Contains(out, m) {
					t.Errorf("output lacks %q:\n%s", m, out)
				}
			}
			if _, serr := os.Stat(i.dropin()); serr == nil {
				t.Error("a drop-in was written although the run was refused")
			}
			// forced: the pin is installed, with a warning
			out, err = i.run("tn_inst_prepare "+tc.role+"; "+resultLine, append(env, "TN_K3S_FORCE=true")...)
			if err != nil || !strings.Contains(out, "TN_K3S_FORCE=true: going on as if k3s were not installed") ||
				result(t, out) != "version=v1.36.5+k3s1 channel=unset action=install effective=v1.36.5+k3s1" {
				t.Errorf("forced: %v\n%s", err, out)
			}
		})
	}
	// no binary, no datastore: a new device
	i := newInst(t)
	i.installed("")
	if out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=cgroup2fs", pin); err != nil || !strings.Contains(out, "k3s is not installed") {
		t.Errorf("fresh device: %v\n%s", err, out)
	}
	// a readable binary with a datastore is the normal case
	i = newInst(t)
	i.installed("v1.34.11+k3s1")
	datastore(i)
	if out, err := i.run("tn_inst_prepare server; "+resultLine, "TN_CGROUP_FSTYPE=cgroup2fs", pin); err != nil || !strings.Contains(result(t, out), "action=keep") {
		t.Errorf("installed: %v\n%s", err, out)
	}
	// a datastore in the data dir that was named
	i = newInst(t)
	i.installed("")
	other := filepath.Join(i.dir, "elsewhere")
	if err := os.MkdirAll(filepath.Join(other, "server", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=cgroup2fs", pin, "DATA_DIR="+other); exitCode(err) != 1 || !strings.Contains(out, other+"/server/db") {
		t.Errorf("datastore of DATA_DIR: %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------- cgroup flag: atomic, idempotent

func TestCgroupDropinIsWrittenAtomically(t *testing.T) {
	i := newInst(t)
	i.installed("")
	env := []string{"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.34.11+k3s1"}
	dir := filepath.Dir(i.dropin())
	// a leftover of a run that was killed in the middle
	write(t, filepath.Join(dir, ".99-teknoir-cgroupv1.yaml.tmp.4242"), "half")
	out, err := i.run("tn_inst_prepare server", env...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "99-teknoir-cgroupv1.yaml" {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("config.yaml.d holds %v", names)
	}
	// idempotent: the file is not touched when it is right (same inode, same mtime)
	st1, _ := os.Stat(i.dropin())
	time.Sleep(1100 * time.Millisecond)
	out, err = i.run("tn_inst_prepare server", env...)
	st2, _ := os.Stat(i.dropin())
	if err != nil || !strings.Contains(out, "already in place") || !os.SameFile(st1, st2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("not idempotent: %v\n%s", err, out)
	}
	// a changed file is replaced in one step
	write(t, i.dropin(), "kubelet-arg+:\n  - \"fail-cgroupv1=true\"\n")
	if out, err = i.run("tn_inst_prepare server", env...); err != nil || !strings.Contains(string(mustRead(t, i.dropin())), "fail-cgroupv1=false") {
		t.Errorf("not rewritten: %v\n%s", err, out)
	}
	// the temporary file is named like a hidden file that does not end in .yaml, and is moved into place
	b := string(mustRead(t, filepath.Join("scripts", "tn-k3s-install.sh")))
	if !strings.Contains(b, `_tn_tmp=$_tn_cdir/.$TN_INST_CGROUP_FILE.tmp.$$`) || !strings.Contains(b, `mv -f "$_tn_tmp" "$_tn_cfile"`) {
		t.Error("the drop-in is not written through a temporary file")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCgroupDropinWriteFailureIsFatalAndLeavesNothingBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	i := newInst(t)
	i.installed("")
	dir := filepath.Dir(i.dropin())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	out, err := i.run("tn_inst_prepare server", "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	if exitCode(err) != 1 || !strings.Contains(out, "cannot write") {
		t.Fatalf("rc=%d\n%s", exitCode(err), out)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("left behind: %v", ents)
	}
}

// ---------------------------------------------------------------- a refusal says what changed

func TestRefusalsSayWhatThisRunChanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"downgrade", []string{"INSTALL_K3S_VERSION=v1.34.11+k3s1", "TN_K3S_UPGRADE=true", "TN_K3S_FORCE=true"}},
		{"minor jump", []string{"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_K3S_UPGRADE=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// installed v1.35.9 for the jump (v1.35 -> v1.37 pin); for the downgrade the pin is older
			installed := "v1.35.9+k3s1"
			env := append([]string{"TN_CGROUP_FSTYPE=cgroup2fs"}, tc.env...)
			if tc.name == "minor jump" {
				installed = "v1.34.11+k3s1"
			}
			i := newInst(t)
			i.installed(installed)
			out, err := i.run("tn_inst_prepare server", env...)
			if exitCode(err) != 1 || !strings.Contains(out, "Nothing was changed") {
				t.Fatalf("without a change: rc=%d\n%s", exitCode(err), out)
			}
			changed := "the CA was rotated in the datastore of k3s (k3s certificate rotate-ca); the CA files in /x/server/tls were replaced; k3s was restarted"
			i = newInst(t)
			i.installed(installed)
			out, err = i.run("tn_inst_prepare server", append(env, "TN_K3S_CA_CHANGED="+changed)...)
			if exitCode(err) != 1 || strings.Contains(out, "Nothing was changed") ||
				!strings.Contains(out, "The k3s release was not changed, but earlier in this run: "+changed) {
				t.Fatalf("after a change: rc=%d\n%s", exitCode(err), out)
			}
		})
	}
	// the agent refusal too
	i := newInst(t)
	i.installed("")
	out, err := i.run("tn_inst_prepare agent", "TN_CGROUP_FSTYPE=cgroup2fs", "TN_K3S_PIN_SOURCE=flag", "INSTALL_K3S_VERSION=v1.36.5+k3s1", "K3S_URL="+agentURL,
		"TN_TEST_PUBLISHED=v1.34.11+k3s1", "TN_K3S_CA_CHANGED=k3s was restarted")
	if exitCode(err) != 1 || strings.Contains(out, "Nothing was changed") || !strings.Contains(out, "earlier in this run: k3s was restarted") {
		t.Fatalf("agent: rc=%d\n%s", exitCode(err), out)
	}
}

// the CA library records what it changes
func TestTheCAStepRecordsWhatItChanged(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	out := p.mustSh("tn_k3s_ca_setup; echo \"CHANGED=$TN_K3S_CA_CHANGED\"", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true")
	for _, want := range []string{"CHANGED=the CA was rotated in the datastore of k3s", "the CA files in " + p.data + "/server/tls were replaced", "k3s was restarted"} {
		if !strings.Contains(out, want) {
			t.Errorf("TN_K3S_CA_CHANGED lacks %q:\n%s", want, out)
		}
	}
	// a fresh device: the PKI was generated; a second run changes nothing
	q := newPKI(t)
	q.legacyCert()
	out = q.mustSh("tn_k3s_ca_setup; echo \"CHANGED=$TN_K3S_CA_CHANGED\"")
	if !strings.Contains(out, "CHANGED=the k3s PKI in "+q.data+"/server/tls was generated") || strings.Contains(out, "restarted") {
		t.Errorf("fresh setup:\n%s", out)
	}
	out = q.mustSh("tn_k3s_ca_setup; echo \"CHANGED=[$TN_K3S_CA_CHANGED]\"")
	if !strings.Contains(out, "CHANGED=[]") {
		t.Errorf("a second setup changed something:\n%s", out)
	}
}

// ---------------------------------------------------------------- the order: flag before the CA step

func TestServerScriptOrder(t *testing.T) {
	out := renderK3S(t, ServerTemplate, "")
	idx := func(s string) int { return strings.Index(out, s) }
	pin := idx("\nexport INSTALL_K3S_VERSION=")
	resolve := idx("\ntn_ca_resolve_datadir\n")
	prep := idx("\ntn_inst_prepare server\n")
	ca := idx("\ntn_k3s_ca_setup\n")
	inst := idx("\n$SUDO ./k3s_installer.sh")
	if !(pin > 0 && pin < resolve && resolve < prep && prep < ca && ca < inst) {
		t.Errorf("order: pin %d, resolve datadir %d, tn_inst_prepare %d, tn_k3s_ca_setup %d, installer %d", pin, resolve, prep, ca, inst)
	}
	if strings.Count(out, "\ntn_inst_prepare server\n") != 1 {
		t.Error("tn_inst_prepare is called more than once")
	}
	// the library functions are defined before they are called
	for _, fn := range []string{"tn_ca_resolve_datadir()", "tn_inst_prepare()", "tn_k3s_ca_setup()"} {
		if idx(fn) < 0 || idx(fn) > resolve {
			t.Errorf("%s is not defined before its first call", fn)
		}
	}
	// the agent has no CA step, and K3S_URL (to ask the server) is set before tn_inst_prepare
	ag := renderK3S(t, AgentTemplate, "")
	if strings.Contains(ag, "\ntn_k3s_ca_setup\n") {
		t.Error("the agent script runs the CA step")
	}
	if !(strings.Index(ag, `export K3S_URL=${K3S_URL}`) > 0 && strings.Index(ag, `export K3S_URL=${K3S_URL}`) < strings.Index(ag, "\ntn_inst_prepare agent\n")) {
		t.Error("the agent script must have K3S_URL before tn_inst_prepare agent")
	}
}

// What the order buys on a device that an old script left hung: cgroup v1, a k3s >= v1.35 that exits
// (and restarts itself in a loop), and a CA from before the fix. The kubelet flag is in place when
// rotate-ca runs and when k3s is restarted.
func TestTheKubeletFlagIsInPlaceBeforeTheCAStepTouchesK3s(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
		want  string
	}{
		{"flag first, the server script", "tn_inst_prepare server", "dropin=yes"},
		{"CA first, the old order", "tn_k3s_ca_setup", "dropin=no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			p.legacyPKI(true)
			dropin := filepath.Join(p.k3sCfg, "config.yaml.d", "99-teknoir-cgroupv1.yaml")
			calls := filepath.Join(p.dir, "calls.log")
			bin := filepath.Join(p.dir, "fake-k3s")
			write(t, bin, "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'k3s version v1.36.5+k3s1 (0123abcd)'; echo 'go version go1.24.1'; exit 0; fi\n"+
				"if [ -f '"+dropin+"' ]; then echo \"rotate dropin=yes\" >> '"+calls+"'; else echo \"rotate dropin=no\" >> '"+calls+"'; fi\nexit 0\n")
			if err := os.Chmod(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			restart := "if [ -f '" + dropin + "' ]; then echo 'restart dropin=yes' >> '" + calls + "'; else echo 'restart dropin=no' >> '" + calls + "'; fi"
			second := "tn_k3s_ca_setup"
			if tc.first == "tn_k3s_ca_setup" {
				second = "tn_inst_prepare server"
			}
			script := ". " + installLibPath + "; tn_ca_resolve_datadir; " + tc.first + "; " + second + `; echo "CHANGED=$TN_K3S_CA_CHANGED"`
			out := p.mustSh(script, "K3S_BIN="+bin, "TN_K3S_RESTART_CMD="+restart, "TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
			got := string(mustRead(t, calls))
			for _, line := range []string{"rotate " + tc.want, "restart " + tc.want} {
				if !strings.Contains(got, line) {
					t.Errorf("%q not in the calls:\n%s\n%s", line, got, out)
				}
			}
			if _, err := os.Stat(dropin); err != nil {
				t.Errorf("the drop-in was never written:\n%s", out)
			}
		})
	}
}

// ---------------------------------------------------------------- slow k3s: restart and rotate-ca

func TestServiceTimeoutDefaultsToTenMinutes(t *testing.T) {
	p := newPKI(t)
	if out := p.mustSh(`tn_ca_svc_timeout`, "TN_K3S_SERVICE_TIMEOUT="); !strings.Contains(out, "600") {
		t.Errorf("default timeout: %s", out)
	}
	if out := p.mustSh(`tn_ca_svc_timeout`, "TN_K3S_SERVICE_TIMEOUT=33"); !strings.Contains(out, "33") {
		t.Errorf("explicit timeout: %s", out)
	}
	for _, f := range []string{"scripts/tn-k3s-ca.sh"} {
		b := mustRead(t, f)
		if strings.Contains(string(b), "(120)") && strings.Contains(string(b), "SERVICE_TIMEOUT  seconds that a stop, start or restart of k3s may take (120)") {
			t.Errorf("%s still documents 120 s", f)
		}
	}
}

// fakeSlowSystemctl: `restart` takes long; the unit is `activating` for becomeActive seconds after
// it started and `active` before and after. Every call is logged.
func fakeSlowSystemctl(t *testing.T, becomeActive int) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "systemctl.log")
	write(t, filepath.Join(dir, "systemctl"), `#!/bin/sh
echo "$*" >> '`+log+`'
case "$1" in
  is-active)
    if [ -f '`+dir+`/restarted' ]; then
      now=$(date +%s); then_=$(cat '`+dir+`/restarted')
      if [ $((now - then_)) -lt `+itoa(becomeActive)+` ]; then echo activating; exit 3; fi
    fi
    echo active ;;
  restart) date +%s > '`+dir+`/restarted'; sleep 20 ;;
  cat) exit 0 ;;
esac
`)
	if err := os.Chmod(filepath.Join(dir, "systemctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func TestARestartThatIsSlowButGetsThereIsASuccess(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	write(t, p.unit, unitText())
	fakeSlowSystemctl(t, 3)
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	start := time.Now()
	out, err := p.sh("tn_ca_remediate all; echo \"CHANGED=$TN_K3S_CA_CHANGED\"", "K3S_BIN="+bin, "TN_K3S_SERVICE_TIMEOUT=1", "TN_K3S_READY_GRACE=20", "TN_K3S_READY_POLL=1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("took %s", d)
	}
	for _, want := range []string{"did not finish within 1s: watching the unit", "k3s is active now", "k3s was restarted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "could not be restarted") {
		t.Errorf("reported as a failure:\n%s", out)
	}
}

func TestARestartThatNeverGetsThereIsStillReportedAfterTheGrace(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	write(t, p.unit, unitText())
	fakeSlowSystemctl(t, 1000)
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	start := time.Now()
	out, err := p.sh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_SERVICE_TIMEOUT=1", "TN_K3S_READY_GRACE=3", "TN_K3S_READY_POLL=1")
	if exitCode(err) != 5 {
		t.Fatalf("rc=%d\n%s", exitCode(err), out)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("took %s", d)
	}
	for _, want := range []string{"did not finish within 1s", "the unit was not active 3s later either", "datastore of k3s holds the new CA", "could not be restarted"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
}

// a k3s server that is still starting does not answer rotate-ca: it is tried again, but only when the
// unit is there and starting or running
func TestRotateCaIsTriedAgainWhileTheUnitStarts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     string
		failFirst int
		wantRC    int
		wantCalls int
	}{
		{"unit activating: third try works", "activating", 2, 0, 3},
		{"unit active: second try works", "active", 1, 0, 2},
		{"unit activating, never answers", "activating", 100, 1, 3},
		{"unit inactive: one try", "inactive", 100, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			p.legacyPKI(true)
			write(t, p.unit, unitText())
			fakeSystemctl(t, tc.state, 0)
			calls := filepath.Join(p.dir, "rotate.calls")
			bin := filepath.Join(p.dir, "fake-k3s")
			write(t, bin, "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'k3s version v1.34.11+k3s1 (0123abcd)'; exit 0; fi\n"+
				"echo x >> '"+calls+"'\nn=$(wc -l < '"+calls+"')\nif [ \"$n\" -le "+itoa(tc.failFirst)+" ]; then echo 'connection refused' >&2; exit 1; fi\nexit 0\n")
			if err := os.Chmod(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			out, err := p.sh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true", "TN_K3S_ROTATE_TRIES=3", "TN_K3S_ROTATE_WAIT=1")
			if exitCode(err) != tc.wantRC {
				t.Fatalf("rc=%d, want %d\n%s", exitCode(err), tc.wantRC, out)
			}
			if n := strings.Count(string(readLog(calls)), "x\n"); n != tc.wantCalls {
				t.Errorf("rotate-ca was called %d times, want %d\n%s", n, tc.wantCalls, out)
			}
			if d := time.Since(start); d > 12*time.Second {
				t.Errorf("took %s", d)
			}
			if tc.wantRC == 1 && !strings.Contains(out, "were not changed") {
				t.Errorf("a failed rotation does not say that the CA was not changed:\n%s", out)
			}
			if tc.wantRC == 0 && tc.wantCalls > 1 && !strings.Contains(out, "trying again in 1s") {
				t.Errorf("no word about the retry:\n%s", out)
			}
		})
	}
}

// ---------------------------------------------------------------- the data dir must agree with config.yaml

func TestDataDirThatDisagreesWithConfigYamlIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		unit   func(dd string) string
		file   string // relative to the k3s config dir
		cfg    func(other string) string
		useEnv bool // the data dir is given as DATA_DIR
		wantOK bool
		msgs   func(dd, other string) []string
	}{
		{name: "ExecStart --data-dir and another data-dir in config.yaml", unit: func(dd string) string { return unitText("--data-dir", dd) },
			file: "config.yaml", cfg: func(o string) string { return "data-dir: " + o + "\n" },
			msgs: func(dd, o string) []string {
				return []string{"k3s runs from " + dd + " (ExecStart of k3s.service)", "config.yaml sets data-dir: " + o, "without --data-dir", "The CA and k3s were not changed"}
			}},
		{name: "ExecStart --data-dir=... and another data-dir in config.yaml.d", unit: func(dd string) string { return unitText("--data-dir=" + dd) },
			file: "config.yaml.d/50-x.yaml", cfg: func(o string) string { return "data-dir: \"" + o + "\"\n" },
			msgs: func(dd, o string) []string { return []string{"k3s runs from " + dd, "50-x.yaml sets data-dir: " + o} }},
		{name: "an explicit DATA_DIR and another data-dir in config.yaml", unit: func(string) string { return unitText() }, useEnv: true,
			file: "config.yaml", cfg: func(o string) string { return "data-dir: " + o + "\n" },
			msgs: func(dd, o string) []string { return []string{"k3s runs from " + dd + " (--data-dir / DATA_DIR)"} }},
		{name: "the same data dir in both, with a trailing slash", unit: func(dd string) string { return unitText("--data-dir", dd) },
			file: "config.yaml", cfg: func(o string) string { return "data-dir: DD/\n" }, wantOK: true},
		{name: "config.yaml is the source", unit: func(string) string { return unitText() },
			file: "config.yaml", cfg: func(o string) string { return "data-dir: DD\n" }, wantOK: true},
		{name: "no data-dir in config.yaml", unit: func(dd string) string { return unitText("--data-dir", dd) },
			file: "config.yaml", cfg: func(o string) string { return "node-name: x\n" }, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			dd := filepath.Join(p.dir, "srv-k3s")
			other := filepath.Join(p.dir, "var-other")
			write(t, p.unit, tc.unit(dd))
			write(t, filepath.Join(p.k3sCfg, tc.file), strings.ReplaceAll(tc.cfg(other), "DD", dd))
			env := []string{"DATA_DIR="}
			if tc.useEnv {
				env = []string{"DATA_DIR=" + dd}
			}
			out, err := p.sh("tn_ca_resolve_datadir; if tn_ca_datadir_consistent; then echo CONSISTENT; else echo INCONSISTENT; fi", env...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := strings.Contains("\n"+out, "\nCONSISTENT"); got != tc.wantOK {
				t.Errorf("consistent=%v, want %v\n%s", got, tc.wantOK, out)
			}
			if tc.wantOK {
				return
			}
			// the setup stops, before it creates anything
			setup, serr := p.sh("tn_k3s_ca_setup", env...)
			if exitCode(serr) != 1 {
				t.Fatalf("setup went on (rc=%d):\n%s", exitCode(serr), setup)
			}
			for _, m := range tc.msgs(dd, other) {
				if !strings.Contains(setup, m) {
					t.Errorf("message lacks %q:\n%s", m, setup)
				}
			}
			for _, d := range []string{dd, other} {
				if _, err := os.Stat(filepath.Join(d, "server")); err == nil {
					t.Errorf("%s/server was created", d)
				}
			}
		})
	}
}

// the dash/bash differences of the new code are covered by the plain-shell test of the libraries;
// this one runs the new install code under dash as well, as a device with /bin/sh = dash does
func TestNewInstallCodeRunsUnderDash(t *testing.T) {
	if _, err := exec.LookPath("dash"); err != nil {
		t.Skip("no dash")
	}
	i := newInst(t)
	i.installed("")
	cmd := exec.Command("dash", "-c", "set -e; set -o noglob; umask 027; . "+installLibPath+"; tn_inst_prepare agent; "+resultLine)
	cmd.Env = append(os.Environ(), "TN_K3S_CONFIG_DIR="+i.k3sCfg, "K3S_BIN="+i.bin, "DATA_DIR="+filepath.Join(i.dir, "data"), "TN_CGROUP_FSTYPE=tmpfs",
		"K3S_URL="+agentURL, "TN_K3S_PIN_SOURCE=default", "INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_TEST_PUBLISHED=v1.35.9+k3s1")
	out, err := cmd.CombinedOutput()
	if err != nil || result(t, string(out)) != "version=v1.35.9+k3s1 channel=unset action=install effective=v1.35.9+k3s1" {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(i.dropin()); err != nil {
		t.Error("no drop-in on cgroup v1")
	}
}

// ---------------------------------------------------------------- the server publishes its release

func (i *inst) published() string {
	return filepath.Join(i.dir, "data", "server", "static", "teknoir", "k3s-version")
}

func TestServerPublishesItsRelease(t *testing.T) {
	i := newInst(t)
	i.installed("v1.34.11+k3s1")
	out, err := i.run("tn_inst_publish_release")
	if err != nil || !strings.Contains(out, "Published the k3s release v1.34.11+k3s1 for the agents") {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(mustRead(t, i.published())); got != "v1.34.11+k3s1\n" {
		t.Errorf("file: %q", got)
	}
	// the library of the agent reads exactly that file back
	ent, _ := os.ReadDir(filepath.Dir(i.published()))
	if len(ent) != 1 {
		t.Errorf("temporary files left: %v", ent)
	}
	// unchanged when right: same inode
	st1, _ := os.Stat(i.published())
	out, err = i.run("tn_inst_publish_release")
	st2, _ := os.Stat(i.published())
	if err != nil || !strings.Contains(out, "already published") || !os.SameFile(st1, st2) {
		t.Errorf("not idempotent: %v\n%s", err, out)
	}
	// k3s was upgraded: the file follows
	i.installed("v1.35.9+k3s1")
	out, err = i.run("tn_inst_publish_release")
	if err != nil || string(mustRead(t, i.published())) != "v1.35.9+k3s1\n" {
		t.Errorf("not updated: %v\n%s", err, out)
	}
	// a leftover temporary file of a killed run does not matter
	write(t, filepath.Join(filepath.Dir(i.published()), ".k3s-version.tmp.1"), "x")
	i.installed("v1.36.5+k3s1")
	if out, err = i.run("tn_inst_publish_release"); err != nil || string(mustRead(t, i.published())) != "v1.36.5+k3s1\n" {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestPublishNeverFailsTheScript(t *testing.T) {
	// the version of k3s cannot be read
	i := newInst(t)
	i.installed("")
	out, err := i.run("tn_inst_publish_release; echo AFTER")
	if err != nil || !strings.Contains(out, "AFTER") || !strings.Contains(out, "cannot read the release of the installed k3s") {
		t.Errorf("%v\n%s", err, out)
	}
	if _, serr := os.Stat(i.published()); serr == nil {
		t.Error("something was published")
	}
	// the directory is not writable
	if os.Geteuid() != 0 {
		j := newInst(t)
		j.installed("v1.34.11+k3s1")
		dd := filepath.Join(j.dir, "data")
		if err := os.MkdirAll(dd, 0o555); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dd, 0o755)
		out, err = j.run("tn_inst_publish_release; echo AFTER")
		if err != nil || !strings.Contains(out, "AFTER") || !strings.Contains(out, "cannot publish the k3s release") {
			t.Errorf("%v\n%s", err, out)
		}
	}
}

// what the server publishes is what an agent reads and follows
func TestAgentReadsWhatTheServerPublishes(t *testing.T) {
	srv := newInst(t)
	srv.installed("v1.34.11+k3s1")
	srv.mustRun("tn_inst_publish_release")
	agent := newInst(t)
	agent.installed("")
	out, err := agent.run("tn_inst_prepare agent; "+resultLine, "TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL="+agentURL, "TN_K3S_PIN_SOURCE=default",
		"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_TEST_PUBLISHED="+strings.TrimSpace(string(mustRead(t, srv.published()))))
	if err != nil || result(t, out) != "version=v1.34.11+k3s1 channel=unset action=install effective=v1.34.11+k3s1" {
		t.Fatalf("%v\n%s", err, out)
	}
}

func (i *inst) mustRun(script string, env ...string) string {
	i.t.Helper()
	out, err := i.run(script, env...)
	if err != nil {
		i.t.Fatalf("%v\n%s", err, out)
	}
	return out
}

func TestServerScriptPublishesAfterTheInstallerAndTheAgentDoesNot(t *testing.T) {
	srv := renderK3S(t, ServerTemplate, "")
	inst := strings.Index(srv, "\n$SUDO ./k3s_installer.sh")
	pub := strings.Index(srv, "\ntn_inst_publish_release\n")
	manifests := strings.Index(srv, `info "Install Teknoir Manifests"`)
	if !(inst > 0 && inst < pub && pub < manifests) {
		t.Errorf("installer %d, publish %d, manifests %d", inst, pub, manifests)
	}
	if ag := renderK3S(t, AgentTemplate, ""); strings.Contains(ag, "\ntn_inst_publish_release\n") {
		t.Error("the agent script publishes a release")
	}
}
