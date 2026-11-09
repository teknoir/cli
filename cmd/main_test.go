package cmd

import (
	"os"
	"testing"
)

// TestMain points HOME at a directory of this run's own, so no test can read or write the config
// file of the developer running it (or of another test): initConfig falls back to $HOME.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp(os.TempDir(), "tnctl-cmd-home-")
	if err != nil {
		os.Exit(1)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
