package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The server script builds INSTALL_K3S_EXEC BEFORE tn_inst_prepare, because the library reads it
// (tn_inst_cgroup_configured_elsewhere): a fail-cgroupv1 that the caller put there means "the device
// sets it itself", and the drop-in is removed. If the script then rebuilt the variable without the
// caller's value, the installer would get no flag and a k3s >= v1.35 would not start on cgroup v1.

// serverPrologue is the real text of the rendered server script from "# BEGIN install-k3s-exec" to the
// call of tn_inst_prepare: the arguments of the installer, the data dir, the guard, the release step.
func serverPrologue(t *testing.T) string {
	t.Helper()
	out := renderK3S(t, ServerTemplate, "")
	begin := strings.Index(out, "# BEGIN install-k3s-exec\n")
	end := strings.Index(out, "\ntn_inst_prepare server\n")
	if begin < 0 || end < begin {
		t.Fatalf("the server script has no install-k3s-exec block before tn_inst_prepare (begin %d, end %d)", begin, end)
	}
	return out[begin : end+len("\ntn_inst_prepare server\n")]
}

func (i *inst) runPrologue(t *testing.T, tail string, env ...string) (string, error) {
	t.Helper()
	script := "set -e; set -o noglob; umask 027\n" +
		"info() { echo '[INFO] ' \"$@\"; }; warn() { echo '[WARN] ' \"$@\" >&2; }; fatal() { echo '[ERROR] ' \"$@\" >&2; exit 1; }\n" +
		"TN_K3S_CA_NO_MAIN=1; . " + libPath + "; . " + installLibPath + "\n" +
		serverPrologue(t) + tail
	cmd := exec.Command("sh", "-c", script)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "INSTALL_K3S_") || strings.HasPrefix(e, "TN_K3S_") || strings.HasPrefix(e, "TN_CGROUP") || strings.HasPrefix(e, "OS_BUILD") || strings.HasPrefix(e, "K3S_BIN") || strings.HasPrefix(e, "DATA_DIR") || strings.HasPrefix(e, "USE_DOCKER") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "TN_K3S_CONFIG_DIR="+i.k3sCfg, "K3S_BIN="+i.bin, "DATA_DIR="+filepath.Join(i.dir, "data"),
		"TN_K3S_UNIT_FILE="+filepath.Join(i.dir, "no-such.service"), "PRODUCT=k3s",
		"_K3S_TOKEN=tok", "_DEVICE_ID=dev-1", "_DOMAIN=test.cloud", "TN_K3S_PIN_SOURCE=default", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

const baseExec = "server --token tok --tls-san dev-1.test.cloud --tls-san 10.0.0.10"

func execOf(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "EXEC=") {
			return strings.TrimPrefix(l, "EXEC=")
		}
	}
	t.Fatalf("no EXEC line:\n%s", out)
	return ""
}

// The reviewer's scenario: the caller gives the flag in INSTALL_K3S_EXEC, the host is cgroup v1, k3s
// >= v1.35 is installed. The installer must still get the flag.
func TestACallersFailCgroupV1ReachesTheInstaller(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"`,
		"TN_CGROUP_FSTYPE=tmpfs", "INSTALL_K3S_EXEC=--kubelet-arg=fail-cgroupv1=false")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := execOf(t, out)
	if !strings.Contains(got, "fail-cgroupv1=false") {
		t.Errorf("the flag does not reach the installer: INSTALL_K3S_EXEC=%q\n%s", got, out)
	}
	if !strings.HasPrefix(got, baseExec) {
		t.Errorf("INSTALL_K3S_EXEC lost the arguments of the script: %q", got)
	}
	// the library took the flag as the device's own (it saw it in INSTALL_K3S_EXEC): that is only safe
	// because the installer gets it, which was asserted above
	if !strings.Contains(out, "set by the device itself (INSTALL_K3S_EXEC)") {
		if _, err := os.Stat(i.dropin()); err != nil {
			t.Errorf("neither the library took the caller's flag nor the drop-in is there:\n%s", out)
		}
	}
}

// Without a flag from the caller the drop-in is written and the arguments are exactly ours.
func TestInstallExecIsOursWhenTheCallerGivesNone(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"`, "TN_CGROUP_FSTYPE=tmpfs")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := execOf(t, out); got != baseExec {
		t.Errorf("INSTALL_K3S_EXEC=%q, want %q (no stray space)", got, baseExec)
	}
	if _, err := os.Stat(i.dropin()); err != nil {
		t.Errorf("no drop-in on cgroup v1 with k3s 1.36:\n%s", out)
	}
}

func TestInstallExecKeepsTheCallersOtherArgumentsAndAddsDocker(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"`, "TN_CGROUP_FSTYPE=cgroup2fs",
		"INSTALL_K3S_EXEC=--node-label=a=b", "USE_DOCKER=true")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := execOf(t, out), baseExec+" --node-label=a=b --docker"; got != want {
		t.Errorf("INSTALL_K3S_EXEC=%q, want %q", got, want)
	}
}

// A data dir that disagrees with config.yaml is refused before tn_inst_prepare changes anything.
func TestDataDirMismatchIsRefusedBeforeTheReleaseStepChangesAnything(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	write(t, filepath.Join(i.k3sCfg, "config.yaml"), "data-dir: "+filepath.Join(i.dir, "other")+"\n")
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"`, "TN_CGROUP_FSTYPE=tmpfs")
	if err == nil {
		t.Fatalf("the script went on:\n%s", out)
	}
	if !strings.Contains(out, "sets data-dir:") || !strings.Contains(out, "The CA and k3s were not changed") {
		t.Errorf("no refusal text:\n%s", out)
	}
	if _, err := os.Stat(i.dropin()); err == nil {
		t.Errorf("the drop-in was written before the refusal:\n%s", out)
	}
	if strings.Contains(out, "Checking the k3s release") {
		t.Errorf("tn_inst_prepare ran before the refusal:\n%s", out)
	}
}
