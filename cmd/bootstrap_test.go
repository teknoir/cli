package cmd

import (
	"testing"

	"github.com/teknoir/cli/pkg/templates"
)

func TestBootstrapK3SVersionFlag(t *testing.T) {
	f := bootstrapCmd.Flags().Lookup("k3s-version")
	if f == nil {
		t.Fatal("no --k3s-version flag")
	}
	if f.DefValue != templates.DefaultK3SVersion {
		t.Errorf("default %q", f.DefValue)
	}
}

func TestBootstrapK3SUpgradeFlags(t *testing.T) {
	for _, name := range []string{"k3s-upgrade", "force"} {
		f := bootstrapCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("no --%s flag", name)
		}
		// a re-run keeps the installed k3s unless the operator asks otherwise
		if f.DefValue != "false" {
			t.Errorf("--%s defaults to %q", name, f.DefValue)
		}
	}
}
