package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A re-run on a device whose k3s has started before keeps the cluster token of that k3s: tnctl
// generates a new token for every set of scripts, and k3s refuses to start with another token
// than the one its datastore is encrypted with.
func TestReRunKeepsTheExistingK3sToken(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	srv := filepath.Join(i.dir, "data", "server")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "token"), []byte("K10abcdef::server:existingpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"; echo "TOKEN=$_K3S_TOKEN"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := execOf(t, out), "server --token existingpass --tls-san dev-1.test.cloud --tls-san 10.0.0.10"; got != want {
		t.Fatalf("INSTALL_K3S_EXEC = %q, want %q\n%s", got, want, out)
	}
	if !strings.Contains(out, "TOKEN=existingpass\n") {
		t.Fatalf("_K3S_TOKEN not replaced:\n%s", out)
	}
	if !strings.Contains(out, "keeping its cluster token") || strings.Count(out, "existingpass") != 2 {
		t.Fatalf("the script must say it keeps the token without printing it:\n%s", out)
	}
}

func TestFirstRunUsesTheGeneratedK3sToken(t *testing.T) {
	i := newInst(t)
	out, err := i.runPrologue(t, `echo "EXEC=$INSTALL_K3S_EXEC"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := execOf(t, out); got != baseExec {
		t.Fatalf("INSTALL_K3S_EXEC = %q, want %q\n%s", got, baseExec, out)
	}
	if strings.Contains(out, "keeping its cluster token") {
		t.Fatalf("a first run has no token to keep:\n%s", out)
	}
}

// A re-run with the node flags (--server, --tls-san, --role ...): the extra TLS SANs stay in the
// arguments of the installer and only the token is replaced by the one of the existing k3s.
func TestReRunKeepsTheExistingK3sTokenWithNodeFlags(t *testing.T) {
	i := newInst(t)
	i.installed("v1.36.5+k3s1")
	srv := filepath.Join(i.dir, "data", "server")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "token"), []byte("K10abcdef::server:existingpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := serverRolesData()
	d.K3SToken = "tok"
	var buf bytes.Buffer
	if err := ServerTemplate.Execute(&buf, d); err != nil {
		t.Fatal(err)
	}
	full := buf.String()
	begin := strings.Index(full, "# BEGIN install-k3s-exec\n")
	end := strings.Index(full, "\ntn_inst_prepare server\n")
	if begin < 0 || end < begin {
		t.Fatalf("no install-k3s-exec block (begin %d, end %d)", begin, end)
	}
	prologue := full[begin : end+len("\ntn_inst_prepare server\n")]
	script := "set -e; set -o noglob; umask 027\n" +
		"info() { echo '[INFO] ' \"$@\"; }; warn() { echo '[WARN] ' \"$@\" >&2; }; fatal() { echo '[ERROR] ' \"$@\" >&2; exit 1; }\n" +
		"TN_K3S_CA_NO_MAIN=1; . " + libPath + "; . " + installLibPath + "\n" + prologue + `echo "EXEC=$INSTALL_K3S_EXEC"; echo "TOKEN=$_K3S_TOKEN"` + "\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "TN_K3S_CONFIG_DIR="+i.k3sCfg, "K3S_BIN="+i.bin, "DATA_DIR="+filepath.Join(i.dir, "data"),
		"TN_K3S_UNIT_FILE="+filepath.Join(i.dir, "no-such.service"), "PRODUCT=k3s",
		"_K3S_TOKEN=tok", "_DEVICE_ID=dev-1", "_DOMAIN=test.cloud", "TN_K3S_PIN_SOURCE=default", "INSTALL_K3S_VERSION=v1.36.5+k3s1")
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "server --token existingpass --tls-san dev-1.test.cloud --tls-san 10.0.0.10 --tls-san orin.example.test --tls-san 10.0.0.5"
	if got := execOf(t, out); got != want {
		t.Fatalf("INSTALL_K3S_EXEC = %q, want %q\n%s", got, want, out)
	}
	if !strings.Contains(out, "TOKEN=existingpass\n") {
		t.Fatalf("_K3S_TOKEN not replaced:\n%s", out)
	}
}
