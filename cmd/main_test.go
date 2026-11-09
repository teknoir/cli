package cmd

import (
	"os"
	"strings"
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
	// Nor may the developer's shell choose a test's domain, teamspace, device, hub or bot: root.go
	// reads every TNCTL_ variable (AutomaticEnv), and the bot and agent commands read the others.
	// A test that needs one sets it with t.Setenv.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TNCTL_") || strings.HasPrefix(k, "TEKNOIRBOT_") {
			_ = os.Unsetenv(k)
		}
	}
	for _, k := range []string{"TEKNOIR_DOMAIN", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
