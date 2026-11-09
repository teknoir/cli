package config

import (
	"os"
	"path/filepath"
	"sync"
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

// Tool calls of `tnctl mcp` run concurrently: a token refresh in one saves the config while another
// reads it. Without viperMu that is a concurrent map read and write, which kills the process.
func TestConfigConcurrentLoadAndSave(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "tnctl.yaml")
	content := "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: tok\n    namespace: teknoir-demo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cfg, err := LoadFresh()
			if err != nil {
				t.Errorf("LoadFresh: %v", err)
				return
			}
			if err := cfg.Save(); err != nil {
				t.Errorf("Save: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cfg, err := Load()
			if err != nil {
				t.Errorf("Load: %v", err)
				return
			}
			if ns := cfg.GetNamespace(); ns != "teknoir-demo" {
				t.Errorf("namespace %q", ns)
				return
			}
		}
	}()
	wg.Wait()
}

// Run is guarded by viperMu like Save: a reload while a token refresh saves, or another call reads,
// is no concurrent map read and write either.
func TestConfigConcurrentReload(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "tnctl.yaml")
	content := "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: tok\n    namespace: teknoir-demo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	base, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			Reload()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := Load(); err != nil {
				t.Errorf("Load: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if ns := base.GetNamespace(); ns != "teknoir-demo" {
				t.Errorf("namespace %q", ns)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			cfg, err := LoadFresh()
			if err != nil {
				t.Errorf("LoadFresh: %v", err)
				return
			}
			if err := cfg.Save(); err != nil {
				t.Errorf("Save: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
