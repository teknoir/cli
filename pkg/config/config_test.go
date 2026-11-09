package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/pflag"
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

// Reload is guarded by viperMu like Save: a reload while a token refresh saves, or another call reads,
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

// Reload drops what Save set in memory: after a token refresh saved the config, a tnctl ns made by
// another process still counts on the next Reload.
func TestReloadDropsWhatSaveSet(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "tnctl.yaml")
	write := func(ns string) {
		t.Helper()
		content := "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: tok\n    namespace: " + ns + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("teknoir-demo")
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFresh()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil { // a token refresh: viper's overrides now hold teknoir-demo
		t.Fatal(err)
	}
	write("acme") // tnctl ns acme in another process
	Reload()
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if ns := cfg.GetNamespace(); ns != "acme" {
		t.Errorf("namespace %q after Reload, want acme: what Save set in memory still wins", ns)
	}
}

// A token refresh saves the config while -n, TNCTL_NAMESPACE and TNCTL_DOMAIN are in effect: none of
// them may reach the file, and afterwards the flag and the environment still decide for this run.
// Keys an older tnctl wrote (flag_namespace) are cleaned; the file's other keys stay, and a
// group-readable file becomes the user's alone.
func TestSaveWritesNoFlagOrEnvironment(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), ".tnctl.yaml")
	content := "domain: teknoir.online\nflag_namespace: stale\ncustom: kept\nauths:\n  teknoir_online:\n    access_token: a\n    refresh_token: r1\n    namespace: prod-ns\n  teknoir_cloud:\n    access_token: b\n    refresh_token: r2\n    namespace: teknoir-demo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil { // group-readable, as an older tnctl or the user made it
		t.Fatal(err)
	}
	fl := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fl.StringP("namespace", "n", "", "")
	fl.String("device", "", "")
	if err := fl.Set("namespace", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := fl.Set("device", "cam1"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TNCTL_NAMESPACE", "acme")
	t.Setenv("TNCTL_DOMAIN", "teknoir.cloud")
	t.Setenv("TNCTL_DEBUG", "true")
	_ = viper.BindPFlag("flag_namespace", fl.Lookup("namespace"))
	_ = viper.BindPFlag("flag_device", fl.Lookup("device"))
	_ = viper.BindEnv("flag_namespace", "TNCTL_NAMESPACE")
	viper.AutomaticEnv()
	viper.SetEnvPrefix("TNCTL")
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFresh() // what auth.accessToken saves after a refresh
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Auths["teknoir_cloud"]
	a.RefreshToken = "r3"
	cfg.Auths["teknoir_cloud"] = a
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	fv := viper.New()
	fv.SetConfigFile(path)
	if err := fv.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"flag_namespace", "namespace", "flag_device", "device", "debug"} {
		if fv.IsSet(k) {
			t.Errorf("the file holds %s: %v", k, fv.Get(k))
		}
	}
	if got := fv.GetString("domain"); got != "teknoir.online" {
		t.Errorf("file domain %q", got)
	}
	if got := fv.GetString("custom"); got != "kept" {
		t.Errorf("the file's own key custom is %q, want kept", got)
	}
	if got := fv.GetString("auths.teknoir_cloud.refresh_token"); got != "r3" {
		t.Errorf("refresh token %q", got)
	}
	if got := fv.GetString("auths.teknoir_online.namespace"); got != "prod-ns" {
		t.Errorf("other domain's namespace %q", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600: the file holds tokens", fi.Mode().Perm())
	}
	// This run keeps its flag and environment.
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "teknoir.cloud" {
		t.Errorf("after Save the domain is %q, want TNCTL_DOMAIN's teknoir.cloud", got.Domain)
	}
	if ns := got.GetNamespace(); ns != "acme" {
		t.Errorf("after Save the namespace is %q, want acme", ns)
	}
	if got.Auths["teknoir_cloud"].RefreshToken != "r3" {
		t.Error("after Save the global viper does not hold the saved login")
	}
}

// The teamspace chosen with tnctl ns reaches the file through auths (SetNamespace), while the -n of
// the same run does not.
func TestSaveKeepsTheChosenNamespace(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), ".tnctl.yaml")
	if err := os.WriteFile(path, []byte("domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: a\n    namespace: teknoir-demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fl := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fl.StringP("namespace", "n", "", "")
	if err := fl.Set("namespace", "acme"); err != nil { // tnctl -n acme ns other
		t.Fatal(err)
	}
	_ = viper.BindPFlag("flag_namespace", fl.Lookup("namespace"))
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetNamespace("other")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	fv := viper.New()
	fv.SetConfigFile(path)
	if err := fv.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if got := fv.GetString("auths.teknoir_cloud.namespace"); got != "other" {
		t.Errorf("saved teamspace %q, want other", got)
	}
	if fv.IsSet("flag_namespace") || fv.IsSet("namespace") {
		t.Errorf("the file holds the -n of the run: flag_namespace %q, namespace %q", fv.GetString("flag_namespace"), fv.GetString("namespace"))
	}
}

// The config file was removed while a long-lived tnctl runs: Reload makes that "not logged in", also
// for a JSON config file. A file that cannot be parsed keeps what viper has.
func TestReloadAfterTheFileWasRemoved(t *testing.T) {
	for _, c := range []struct{ name, login string }{
		{".tnctl.yaml", "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: a\n    refresh_token: r\n"},
		{"tnctl.json", `{"domain":"teknoir.cloud","auths":{"teknoir_cloud":{"access_token":"a","refresh_token":"r"}}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			path := filepath.Join(t.TempDir(), c.name)
			if err := os.WriteFile(path, []byte(c.login), 0o600); err != nil {
				t.Fatal(err)
			}
			viper.SetConfigFile(path)
			if err := viper.ReadInConfig(); err != nil {
				t.Fatal(err)
			}
			if cfg, _ := Load(); len(cfg.Auths) != 1 {
				t.Fatalf("logins before the remove: %v, want teknoir_cloud", authKeys(cfg))
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			Reload()
			for name, load := range map[string]func() (*Config, error){"Load": Load, "LoadFresh": LoadFresh} {
				cfg, err := load()
				if err != nil {
					t.Fatal(err)
				}
				if len(cfg.Auths) != 0 {
					t.Errorf("%s after the file was removed: logins %v, want none", name, authKeys(cfg))
				}
			}
		})
	}
	t.Run("broken file", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		path := filepath.Join(t.TempDir(), ".tnctl.yaml")
		if err := os.WriteFile(path, []byte("domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		viper.SetConfigFile(path)
		Reload()
		if err := os.WriteFile(path, []byte("domain: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		Reload()
		if cfg, _ := Load(); len(cfg.Auths) != 1 {
			t.Errorf("a broken file dropped the login: logins %v", authKeys(cfg))
		}
	})
}

// authKeys are the domains cfg holds a login for: test messages name them, never the tokens.
func authKeys(cfg *Config) []string {
	var ks []string
	for k := range cfg.Auths {
		ks = append(ks, k)
	}
	return ks
}

// useConfig makes the global viper read a config file name holding content, as tnctl does at
// start (initConfig sets the type yaml for a file without extension), and returns its path.
func useConfig(t *testing.T, name, content string) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if filepath.Ext(path) == "" {
		viper.SetConfigType("yaml")
	}
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	return path
}

// refreshSaveRT is what auth.accessToken does after a refresh: LoadFresh, change the token, Save.
func refreshSaveRT(t *testing.T, rt string) {
	t.Helper()
	cfg, err := LoadFresh()
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Auths["teknoir_cloud"]
	a.RefreshToken = rt
	cfg.Auths["teknoir_cloud"] = a
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

// A login survives Save in every config format viper writes: the next process (LoadFresh) and this
// one (Load) still have it, and the file's own keys stay.
func TestSaveKeepsTheLoginInEveryFormat(t *testing.T) {
	for name, content := range map[string]string{
		"tnctl.json": `{"domain":"teknoir.cloud","custom":"keep","auths":{"teknoir_cloud":{"access_token":"at","refresh_token":"rt","namespace":"ns1"}}}`,
		"tnctl.yaml": "domain: teknoir.cloud\ncustom: keep\nauths:\n  teknoir_cloud:\n    access_token: at\n    refresh_token: rt\n    namespace: ns1\n",
		"tnctl.toml": "domain = \"teknoir.cloud\"\ncustom = \"keep\"\n[auths.teknoir_cloud]\naccess_token = \"at\"\nrefresh_token = \"rt\"\nnamespace = \"ns1\"\n",
		"tnctlconf":  "domain: teknoir.cloud\ncustom: keep\nauths:\n  teknoir_cloud:\n    access_token: at\n    refresh_token: rt\n    namespace: ns1\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := useConfig(t, name, content)
			for i, rt := range []string{"rt2", "rt3"} {
				refreshSaveRT(t, rt)
				fresh, err := LoadFresh()
				if err != nil {
					t.Fatal(err)
				}
				if a := fresh.Auths["teknoir_cloud"]; a.RefreshToken != rt || a.AccessToken != "at" || a.Namespace != "ns1" {
					t.Errorf("save %d: the file has refresh token set=%v, access token set=%v, namespace %q", i, a.RefreshToken == rt, a.AccessToken == "at", a.Namespace)
				}
				same, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				if same.Auths["teknoir_cloud"].RefreshToken != rt || same.GetNamespace() != "ns1" {
					t.Errorf("save %d: this process has refresh token set=%v, namespace %q", i, same.Auths["teknoir_cloud"].RefreshToken == rt, same.GetNamespace())
				}
			}
			if b, _ := os.ReadFile(path); !strings.Contains(string(b), "keep") {
				t.Error("the file's own key custom was lost")
			}
		})
	}
}

// A top-level namespace that tnctl v0.0.1-beta and v0.0.2-beta wrote (tnctl ns) is the teamspace
// of a login without one of its own: a token refresh keeps it. One that an older Save copied from
// TNCTL_NAMESPACE (it then also wrote that value as flag_namespace) is removed.
func TestSaveKeepsALegacyTopLevelNamespace(t *testing.T) {
	const login = "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: at\n    refresh_token: rt\n"
	for name, c := range map[string]struct {
		top, want string
	}{
		"v0.0.2 file":                 {"namespace: legacy-ns\n", "legacy-ns"},
		"saved by an older tnctl":     {"namespace: legacy-ns\nflag_namespace: \"\"\n", "legacy-ns"},
		"an older -n of another name": {"namespace: legacy-ns\nflag_namespace: acme\n", "legacy-ns"},
		"TNCTL_NAMESPACE written":     {"namespace: acme\nflag_namespace: acme\n", "default"},
	} {
		t.Run(name, func(t *testing.T) {
			path := useConfig(t, ".tnctl.yaml", login+c.top)
			refreshSaveRT(t, "rt2")
			same, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			fv := viper.New()
			fv.SetConfigFile(path)
			if err := fv.ReadInConfig(); err != nil {
				t.Fatal(err)
			}
			if ns := same.GetNamespace(); ns != c.want {
				t.Errorf("this process targets %q after the refresh, want %q", ns, c.want)
			}
			if got := fv.GetString("namespace"); got != map[bool]string{true: "", false: c.want}[c.want == "default"] {
				t.Errorf("the file's namespace is %q", got)
			}
			if fv.IsSet("flag_namespace") {
				t.Errorf("the file still holds flag_namespace %q", fv.GetString("flag_namespace"))
			}
		})
	}
}

// The token file is the user's alone: Save makes an existing group- or world-readable config 0600.
func TestSaveMakesTheConfigPrivate(t *testing.T) {
	path := useConfig(t, ".tnctl.yaml", "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: at\n    refresh_token: rt\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshSaveRT(t, "rt2")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode after Save: %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

// A config file that is a symlink (a dotfiles repo) stays one: Save writes the file it points to.
func TestSaveFollowsASymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "tnctl.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: at\n    refresh_token: rt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".tnctl.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(link)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	refreshSaveRT(t, "rt2")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the config is no longer a symlink: %v (%v)", fi.Mode(), err)
	}
	tv := viper.New()
	tv.SetConfigFile(target)
	if err := tv.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if tv.GetString("auths.teknoir_cloud.refresh_token") != "rt2" {
		t.Error("the symlink's target does not hold the saved token")
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("target mode %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("Save left files next to the link: %d entries", len(entries))
	}
}
