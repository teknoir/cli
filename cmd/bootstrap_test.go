package cmd

import (
	"testing"

	"teknoir/cli/pkg/templates"
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
