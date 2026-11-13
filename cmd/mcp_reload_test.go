package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"
	"github.com/teknoir/cli/pkg/config"
)

// freshProcess makes the global viper what a new tnctl process would have.
func freshProcess() {
	viper.Reset()
	bindViper()
	initConfig()
}

// setNamespaceFlag is -n v on this run's command line.
func setNamespaceFlag(t *testing.T, v string) {
	t.Helper()
	f := rootCmd.PersistentFlags().Lookup("namespace")
	if err := f.Value.Set(v); err != nil {
		t.Fatal(err)
	}
	f.Changed = true
	t.Cleanup(clearNamespaceFlag)
}

func clearNamespaceFlag() {
	f := rootCmd.PersistentFlags().Lookup("namespace")
	_ = f.Value.Set("")
	f.Changed = false
}

var refreshSeq atomic.Int64

// refreshSave is what auth.accessToken does after a refresh: LoadFresh, change the token, Save.
func refreshSave(t *testing.T, key string) {
	cfg, err := config.LoadFresh()
	if err != nil {
		t.Error(err)
		return
	}
	a := cfg.Auths[key]
	a.RefreshToken = "rt-" + strconv.FormatInt(refreshSeq.Add(1), 10)
	cfg.Auths[key] = a
	if err := cfg.Save(); err != nil {
		t.Error(err)
	}
}

// fileSettings reads the config file alone, without flags or environment.
func fileSettings(t *testing.T) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigFile(cfgFile)
	if err := v.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	return v
}

// A token refresh during `tnctl -n acme <command>` saves the config: the -n stays out of the file, so
// the next tnctl without -n targets the stored teamspace, and a tnctl ns after it still counts.
func TestSaveKeepsTheNamespaceFlagOutOfTheFile(t *testing.T) {
	noBotMode(t)
	t.Setenv("TNCTL_NAMESPACE", "")
	t.Setenv("TNCTL_DOMAIN", "")
	t.Cleanup(func() { viper.Reset(); bindViper() })
	useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
	freshProcess()
	setNamespaceFlag(t, "acme")
	cfg, _ := config.Load()
	if ns := cfg.GetNamespace(); ns != "acme" {
		t.Fatalf("with -n acme: %q", ns)
	}
	refreshSave(t, "teknoir_cloud")
	if v := fileSettings(t); v.IsSet("flag_namespace") || v.IsSet("namespace") {
		t.Errorf("the file holds the -n of the run: flag_namespace %q, namespace %q", v.GetString("flag_namespace"), v.GetString("namespace"))
	}
	if cfg, _ := config.Load(); cfg.GetNamespace() != "acme" {
		t.Errorf("after the refresh this run targets %q, want its -n acme", cfg.GetNamespace())
	}

	clearNamespaceFlag()
	freshProcess() // the next tnctl, without -n
	cfg, _ = config.Load()
	if ns := cfg.GetNamespace(); ns != "teknoir-demo" {
		t.Errorf("next tnctl without -n targets %q, want teknoir-demo", ns)
	}
	cfg.SetNamespace("other") // tnctl ns other
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	freshProcess()
	cfg, _ = config.Load()
	if ns := cfg.GetNamespace(); ns != "other" {
		t.Errorf("after tnctl ns other: %q", ns)
	}
}

// The same with TNCTL_NAMESPACE (an mcp server started with it): the environment of one process
// stays out of the file.
func TestSaveKeepsTNCTLNamespaceOutOfTheFile(t *testing.T) {
	noBotMode(t)
	t.Setenv("TNCTL_DOMAIN", "")
	t.Cleanup(func() { viper.Reset(); bindViper() })
	useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
	t.Setenv("TNCTL_NAMESPACE", "acme")
	freshProcess()
	refreshSave(t, "teknoir_cloud")
	if v := fileSettings(t); v.IsSet("flag_namespace") || v.IsSet("namespace") {
		t.Errorf("the file holds TNCTL_NAMESPACE: flag_namespace %q, namespace %q", v.GetString("flag_namespace"), v.GetString("namespace"))
	}

	t.Setenv("TNCTL_NAMESPACE", "")
	freshProcess()
	cfg, _ := config.Load()
	if ns := cfg.GetNamespace(); ns != "teknoir-demo" {
		t.Errorf("next tnctl without TNCTL_NAMESPACE targets %q, want teknoir-demo", ns)
	}
	cfg.SetNamespace("other")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	freshProcess()
	cfg, _ = config.Load()
	if ns := cfg.GetNamespace(); ns != "other" {
		t.Errorf("after tnctl ns other: %q", ns)
	}
}

const twoDomainLogins = `domain: teknoir.online
auths:
  teknoir_online:
    access_token: x
    refresh_token: r1
    namespace: prod-ns
  teknoir_cloud:
    access_token: y
    refresh_token: r2
    namespace: teknoir-demo
`

// tnctl mcp runs with TNCTL_DOMAIN, another domain than the file's: a token refresh that saves the
// config between one call's Reload and its Load leaves TNCTL_DOMAIN in charge.
func TestMCPSaveBetweenReloadAndLoadKeepsTheEnvDomain(t *testing.T) {
	noBotMode(t)
	t.Setenv("TNCTL_NAMESPACE", "")
	t.Setenv("TNCTL_DOMAIN", "teknoir.cloud")
	t.Cleanup(func() { viper.Reset(); bindViper() })
	useConfigFile(t, twoDomainLogins)
	freshProcess()
	fl, err := mcpFleet(defaultEnv, "")
	if err != nil || !strings.Contains(fl.Base, "teknoir.cloud/api/device/fleet/teknoir-demo") {
		t.Fatalf("start: %v %v", fl, err)
	}
	config.Reload()
	refreshSave(t, "teknoir_cloud") // another call's token refresh lands here
	fl2, _, _, err := defaultEnv.fleet()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fl2.Base, "teknoir.cloud/") {
		t.Errorf("after a Save between Reload and Load: %s", fl2.Base)
	}
	fl3, err := mcpFleet(defaultEnv, "") // and the next call
	if err != nil || !strings.Contains(fl3.Base, "teknoir.cloud/") {
		t.Errorf("next call: %v %v", fl3, err)
	}
}

// The same with calls and refreshes running concurrently, as tnctl mcp's tool calls do.
func TestMCPConcurrentSavesKeepTheEnvDomain(t *testing.T) {
	noBotMode(t)
	t.Setenv("TNCTL_NAMESPACE", "")
	t.Setenv("TNCTL_DOMAIN", "teknoir.cloud")
	t.Cleanup(func() { viper.Reset(); bindViper() })
	useConfigFile(t, twoDomainLogins)
	freshProcess()
	var stop atomic.Bool
	var saves atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			refreshSave(t, "teknoir_cloud")
			saves.Add(1)
		}
	}()
	var wrong, total atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				fl, err := mcpFleet(defaultEnv, "")
				if err != nil {
					t.Error(err)
					return
				}
				total.Add(1)
				if !strings.Contains(fl.Base, "teknoir.cloud/") {
					wrong.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	stop.Store(true)
	<-done
	if wrong.Load() > 0 {
		t.Errorf("%d of %d calls used the file's domain, not TNCTL_DOMAIN (%d saves)", wrong.Load(), total.Load(), saves.Load())
	}
}

// useFreshHome is a HOME with no config yet; cfg names the --config file ("" for the default).
func useFreshHome(t *testing.T, cfg string) string {
	t.Helper()
	noBotMode(t)
	t.Setenv("TNCTL_NAMESPACE", "")
	t.Setenv("TNCTL_DOMAIN", "")
	t.Cleanup(func() { viper.Reset(); bindViper() })
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := cfgFile
	cfgFile = ""
	if cfg != "" {
		cfgFile = filepath.Join(home, cfg)
	}
	t.Cleanup(func() { cfgFile = old })
	return home
}

// The default config a fresh HOME gets is the user's alone, also after the login saved its tokens
// in it, and it holds none of the first run's flags or environment.
func TestDefaultConfigIsPrivate(t *testing.T) {
	home := useFreshHome(t, "")
	t.Setenv("TNCTL_NAMESPACE", "acme")
	setNamespaceFlag(t, "acme")
	freshProcess() // creates ~/.tnctl.yaml
	path := filepath.Join(home, ".tnctl.yaml")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("default config mode %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	cfgFile = path
	if v := fileSettings(t); v.IsSet("flag_namespace") || v.IsSet("flag_device") || v.GetString("domain") != "teknoir.cloud" {
		t.Errorf("default config: flag_namespace %q, flag_device %q, domain %q", v.GetString("flag_namespace"), v.GetString("flag_device"), v.GetString("domain"))
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auths["teknoir_cloud"] = config.AuthConfig{AccessToken: "at", RefreshToken: "rt"}
	if err := cfg.Save(); err != nil { // tnctl login
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of the config holding the tokens %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

// tnctl --config <file without extension> login: the next tnctl with that --config is logged in.
func TestConfigFileWithoutExtension(t *testing.T) {
	useFreshHome(t, "tnctlconf")
	freshProcess()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auths["teknoir_cloud"] = config.AuthConfig{AccessToken: "at", RefreshToken: "rt", Namespace: "ns1"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	freshProcess() // the next tnctl --config <same file>
	next, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if next.Auths["teknoir_cloud"].RefreshToken != "rt" || next.GetNamespace() != "ns1" {
		t.Errorf("next tnctl: logged in=%v, teamspace %q", next.Auths["teknoir_cloud"].RefreshToken == "rt", next.GetNamespace())
	}
	fresh, err := config.LoadFresh()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Auths["teknoir_cloud"].RefreshToken != "rt" {
		t.Error("LoadFresh: not logged in")
	}
}
