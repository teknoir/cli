package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"text/template"

	"github.com/teknoir/cli/pkg/templates"
)

// The bootstrap scripts hold the teamspace's Harbor robot password: new files are 0600, and a file an earlier
// tnctl wrote with 0644 is tightened and truncated, not appended to.
func TestWriteBootstrapScriptIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	tmpl := template.Must(template.New("t").Parse("password={{.RegistryPassword}}\n"))
	data := templates.TemplateData{RegistryPassword: "s3cret"}
	dir := t.TempDir()

	fresh := filepath.Join(dir, "fresh.sh")
	if err := writeBootstrapScript(tmpl, fresh, data); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("fresh file: %v %v, want 0600", fi.Mode().Perm(), err)
	}

	old := filepath.Join(dir, "old.sh")
	if err := os.WriteFile(old, []byte("an older, much longer script body that must not survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o644); err != nil { // umask may have cleared bits
		t.Fatal(err)
	}
	if err := writeBootstrapScript(tmpl, old, data); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(old)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("overwritten file: %v %v, want 0600", fi.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(old); string(b) != "password=s3cret\n" {
		t.Errorf("content %q", b)
	}
}
