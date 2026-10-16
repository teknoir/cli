package templates

import (
	"os"
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
