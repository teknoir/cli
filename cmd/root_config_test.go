package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teknoir/cli/pkg/config"
)

// A --config dotfile without extension (.tnctlrc: filepath.Ext calls its whole name the
// extension) is YAML too: login saves into it and the next tnctl reads it.
func TestDotfileConfigIsYAML(t *testing.T) {
	for _, name := range []string{".tnctlrc", ".tnctl"} {
		t.Run(name, func(t *testing.T) {
			home := useFreshHome(t, name)
			if err := os.WriteFile(filepath.Join(home, name), []byte("domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    namespace: ns1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			freshProcess()
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.GetNamespace() != "ns1" {
				t.Errorf("initConfig did not read %s: teamspace %q", name, cfg.GetNamespace())
			}
			cfg.Auths["teknoir_cloud"] = config.AuthConfig{AccessToken: "at", RefreshToken: "rt", Namespace: "acme"}
			if err := cfg.Save(); err != nil {
				t.Fatalf("Save (tnctl login) into %s: %v", name, err)
			}
			freshProcess()
			fresh, err := config.LoadFresh()
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Auths["teknoir_cloud"].RefreshToken != "rt" || fresh.Auths["teknoir_cloud"].Namespace != "acme" {
				t.Error("the next tnctl is not logged in")
			}
		})
	}
}

// The default config a first run creates is the --config file this run reads and Save
// writes, never ~/.tnctl.yaml.
func TestDefaultConfigIsTheConfigFlag(t *testing.T) {
	home := useFreshHome(t, "sub/new.yaml")
	freshProcess()
	path := filepath.Join(home, "sub", "new.yaml")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("--config file after the first run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tnctl.yaml")); !os.IsNotExist(err) {
		t.Errorf("the first run with --config created ~/.tnctl.yaml (%v)", err)
	}
}
