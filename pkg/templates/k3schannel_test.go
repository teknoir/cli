package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A channel name is not a release: the k3s installer downloads INSTALL_K3S_VERSION as an exact tag
// (stable would be a 404), and resolves INSTALL_K3S_CHANNEL itself. The library moves a channel that
// arrives as the version to INSTALL_K3S_CHANNEL, and keeps all the other checks.

func TestChannelAsVersionBecomesInstallK3sChannel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed string
		env       []string
		wantRC    int
		want      string
	}{
		{"stable, fresh", "", []string{"INSTALL_K3S_VERSION=stable"}, 0, "version=unset channel=stable action=install effective="},
		{"latest, fresh", "", []string{"INSTALL_K3S_VERSION=latest"}, 0, "version=unset channel=latest action=install effective="},
		{"testing, fresh", "", []string{"INSTALL_K3S_VERSION=testing"}, 0, "version=unset channel=testing action=install effective="},
		{"v1.34, fresh", "", []string{"INSTALL_K3S_VERSION=v1.34"}, 0, "version=unset channel=v1.34 action=install effective="},
		{"exact stays exact", "", []string{"INSTALL_K3S_VERSION=v1.34.11+k3s1"}, 0, "version=v1.34.11+k3s1 channel=unset action=install effective=v1.34.11+k3s1"},
		{"exact without revision stays exact", "", []string{"INSTALL_K3S_VERSION=v1.34.11"}, 0, "version=v1.34.11 channel=unset action=install effective=v1.34.11"},
		{"a channel in INSTALL_K3S_CHANNEL is left alone", "", []string{"INSTALL_K3S_CHANNEL=stable"}, 0, "version=unset channel=stable action=install effective="},
		{"installed k3s is kept by default", "v1.35.9+k3s1", []string{"INSTALL_K3S_VERSION=stable"}, 0, "version=v1.35.9+k3s1 channel=unset action=keep effective=v1.35.9+k3s1"},
		{"upgrade with a channel is allowed, with a warning", "v1.35.9+k3s1", []string{"INSTALL_K3S_VERSION=stable", "TN_K3S_UPGRADE=true"}, 0, "version=unset channel=stable action=upgrade effective="},
		{"v1.34 channel, upgrade", "v1.33.2+k3s1", []string{"INSTALL_K3S_VERSION=v1.34", "TN_K3S_UPGRADE=true"}, 0, "version=unset channel=v1.34 action=upgrade effective="},
		{"exact downgrade is still refused", "v1.36.5+k3s1", []string{"INSTALL_K3S_VERSION=v1.34.11+k3s1", "TN_K3S_UPGRADE=true"}, 1, ""},
		{"exact multi-minor is still refused", "v1.33.2+k3s1", []string{"INSTALL_K3S_VERSION=v1.36.5+k3s1", "TN_K3S_UPGRADE=true"}, 1, ""},
		{"junk that looks like a channel is left to the installer", "", []string{"INSTALL_K3S_VERSION=v1.34.x"}, 0, "version=v1.34.x channel=unset action=install effective=v1.34.x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInst(t)
			i.installed(tc.installed)
			out, err := i.run("tn_inst_prepare server; "+resultLine, append([]string{"TN_CGROUP_FSTYPE=cgroup2fs"}, tc.env...)...)
			if rc := exitCode(err); rc != tc.wantRC {
				t.Fatalf("rc=%d, want %d\n%s", rc, tc.wantRC, out)
			}
			if tc.wantRC == 0 {
				if got := result(t, out); got != tc.want {
					t.Errorf("got  %s\nwant %s\n%s", got, tc.want, out)
				}
			}
		})
	}
}

func TestChannelAgentIsNotComparedWithTheServer(t *testing.T) {
	i := newInst(t)
	i.installed("")
	out, err := i.run("tn_inst_prepare agent; "+resultLine, "TN_CGROUP_FSTYPE=cgroup2fs", "K3S_URL="+agentURL, "TN_TEST_PUBLISHED=v1.35.9+k3s1",
		"INSTALL_K3S_VERSION=stable", "TN_K3S_PIN_SOURCE=env")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// the agent runs the release of its server, and the installer is not given the channel
	if got := result(t, out); !strings.HasPrefix(got, "version=v1.35.9+k3s1 channel=unset action=install") {
		t.Errorf("got %s\n%s", got, out)
	}
	if !strings.Contains(out, "not an exact release") {
		t.Errorf("no note that the channel cannot be compared:\n%s", out)
	}
}

// A fake k3s installer: it records what it receives, and fails like the real one when
// INSTALL_K3S_VERSION is not an exact release.
func TestInstallerReceivesChannelOrVersionNeverBoth(t *testing.T) {
	for _, tc := range []struct {
		env         []string
		wantVersion string
		wantChannel string
	}{
		{[]string{"INSTALL_K3S_VERSION=stable"}, "", "stable"},
		{[]string{"INSTALL_K3S_VERSION=v1.34"}, "", "v1.34"},
		{[]string{"INSTALL_K3S_VERSION=latest", "INSTALL_K3S_CHANNEL=stable"}, "", "latest"},
		{[]string{"INSTALL_K3S_VERSION=v1.36.5+k3s1"}, "v1.36.5+k3s1", ""},
	} {
		i := newInst(t)
		i.installed("")
		rec := filepath.Join(i.dir, "installer.rec")
		inst := filepath.Join(i.dir, "k3s_installer.sh")
		write(t, inst, "#!/bin/sh\necho \"version=${INSTALL_K3S_VERSION-unset} channel=${INSTALL_K3S_CHANNEL-unset}\" > '"+rec+"'\n"+
			"case ${INSTALL_K3S_VERSION-} in stable|latest|testing|v[0-9]*.[0-9]) echo 'fake installer: not a release' >&2; exit 22 ;; esac\n")
		if err := os.Chmod(inst, 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := i.run("tn_inst_prepare server; "+inst, append([]string{"TN_CGROUP_FSTYPE=cgroup2fs"}, tc.env...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.env, err, out)
		}
		got := strings.TrimSpace(string(mustRead(t, rec)))
		v, c := "unset", "unset"
		if tc.wantVersion != "" {
			v = tc.wantVersion
		}
		if tc.wantChannel != "" {
			c = tc.wantChannel
		}
		if want := "version=" + v + " channel=" + c; got != want {
			t.Errorf("%v: installer got %q, want %q", tc.env, got, want)
		}
	}
}

func TestChannelCodeRunsUnderDash(t *testing.T) {
	needTool(t, "dash")
	i := newInst(t)
	i.installed("")
	cmd := exec.Command("dash", "-c", "set -e; . "+installLibPath+"; INSTALL_K3S_VERSION=stable; export INSTALL_K3S_VERSION; tn_inst_normalize_pin; "+resultLine)
	cmd.Env = append(os.Environ(), "TN_K3S_CONFIG_DIR="+i.k3sCfg)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RESULT version=unset channel=stable") {
		t.Errorf("%v\n%s", err, out)
	}
}

// k3s reads config.yaml.d/*.yaml and *.yml, the suffix without regard to case, in lexical order, and
// the last file that sets a key wins (pkg/configfilearg: HasSuffixI).
func TestDataDirFromDropinsOfEverySuffix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string // name -> data-dir
		want  string            // file that wins, "" = default
	}{
		{"a .yml drop-in", map[string]string{"50-x.yml": "A"}, "50-x.yml"},
		{"a .YAML drop-in", map[string]string{"50-x.YAML": "A"}, "50-x.YAML"},
		{"a .Yml drop-in", map[string]string{"50-x.Yml": "A"}, "50-x.Yml"},
		{"the last in lexical order wins, across suffixes", map[string]string{"10-a.yaml": "A", "20-b.yml": "B", "30-c.YAML": "C"}, "30-c.YAML"},
		{"a later .yaml beats an earlier .yml", map[string]string{"10-a.yml": "A", "20-b.yaml": "B"}, "20-b.yaml"},
		{"other suffixes are ignored", map[string]string{"10-a.yaml": "A", "20-b.txt": "B", "30-c.yaml.bak": "C"}, "10-a.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			write(t, p.unit, unitText())
			dirs := map[string]string{}
			for f, v := range tc.files {
				dirs[f] = filepath.Join(p.dir, "dd-"+v)
				write(t, filepath.Join(p.k3sCfg, "config.yaml.d", f), "data-dir: "+dirs[f]+"\n")
			}
			out, err := p.sh(`tn_ca_resolve_datadir; echo "DD=$DATA_DIR SRC=$TN_CA_DATADIR_SRC"; if tn_ca_datadir_consistent; then echo CONSISTENT; else echo "INCONSISTENT $TN_CA_CFG_DATADIR_SRC"; fi`, "DATA_DIR=")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wantDir := dirs[tc.want]
			if want := "DD=" + wantDir + " SRC=" + filepath.Join(p.k3sCfg, "config.yaml.d", tc.want); !strings.Contains(out, want) {
				t.Errorf("want %q in\n%s", want, out)
			}
			if !strings.Contains("\n"+out, "\nCONSISTENT") {
				t.Errorf("the resolved data dir disagrees with the files:\n%s", out)
			}
			// a different data dir, from outside the files, is inconsistent with them
			out, _ = p.sh(`DATA_DIR=/elsewhere; tn_ca_datadir_consistent && echo CONSISTENT || echo "INCONSISTENT $TN_CA_CFG_DATADIR_SRC"`, "DATA_DIR=")
			if !strings.Contains(out, "INCONSISTENT "+filepath.Join(p.k3sCfg, "config.yaml.d", tc.want)) {
				t.Errorf("%s", out)
			}
		})
	}
}
