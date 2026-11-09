package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestConfig_Save_NoDebug(t *testing.T) {
	// Setup viper
	viper.Reset()
	tmpFile := ".tnctl.test.yaml"
	viper.SetConfigFile(tmpFile)
	defer os.Remove(tmpFile)

	// Set debug in viper manually to simulate it being in the config file
	viper.Set("debug", true)

	cfg := DefaultConfig()
	cfg.Domain = "test.domain"

	// Save
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Read it back in a fresh viper instance
	v2 := viper.New()
	v2.SetConfigFile(tmpFile)
	if err := v2.ReadInConfig(); err != nil {
		t.Fatalf("failed to read config back: %v", err)
	}

	if v2.IsSet("debug") {
		t.Errorf("expected 'debug' to NOT be set in saved config, but it was found")
	}
}

// A config file viper names but that is not there (a HOME where the default config could not be
// written) reads as "not logged in": an empty config, not an error.
func TestLoadFreshWithAMissingFileIsNotLoggedIn(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))

	cfg, err := LoadFresh()
	if err != nil {
		t.Fatalf("LoadFresh: %v", err)
	}
	if len(cfg.Auths) != 0 {
		t.Errorf("auths = %v, want none", cfg.Auths)
	}
}
