package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

func cfgWith(domain string, a config.AuthConfig) *config.Config {
	return &config.Config{Domain: domain, Auths: map[string]config.AuthConfig{config.SanitizeDomain(domain): a}}
}

func TestAccessTokenNotLoggedIn(t *testing.T) {
	cases := map[string]*config.Config{
		"no entry for the domain": cfgWith("other.example", config.AuthConfig{AccessToken: "x"}),
		"entry without token":     cfgWith("teknoir.cloud", config.AuthConfig{RefreshToken: "r"}),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }}
			_, err := accessToken(context.Background(), "teknoir.cloud", d)
			if !errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("want ErrNotLoggedIn, got %v", err)
			}
			if want := "no credentials found for domain teknoir.cloud. Please log in first"; err.Error() != want {
				t.Fatalf("message changed: %q", err.Error())
			}
		})
	}
}

func TestAccessTokenUnchangedDoesNotSave(t *testing.T) {
	cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "at", RefreshToken: "rt"})
	saved := false
	d := tokenDeps{
		load: func() (*config.Config, error) { return cfg, nil },
		refresh: func(_ context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
			if domain != "teknoir.cloud" {
				t.Errorf("domain %q", domain)
			}
			return &oauth2.Token{AccessToken: a.AccessToken, RefreshToken: a.RefreshToken}, nil
		},
		save: func(*config.Config) error { saved = true; return nil },
	}
	tok, err := accessToken(context.Background(), "teknoir.cloud", d)
	if err != nil || tok != "at" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if saved {
		t.Fatal("config saved although the token did not change")
	}
}

func TestAccessTokenRefreshSaves(t *testing.T) {
	cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "old", RefreshToken: "rt-old", Realm: "teknoir", Namespace: "ns1"})
	exp := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var saved *config.Config
	d := tokenDeps{
		load: func() (*config.Config, error) { return cfg, nil },
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "new", RefreshToken: "rt-new", Expiry: exp}, nil
		},
		save: func(c *config.Config) error { saved = c; return nil },
	}
	tok, err := accessToken(context.Background(), "teknoir.cloud", d)
	if err != nil || tok != "new" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	got := saved.Auths["teknoir_cloud"]
	if got.AccessToken != "new" || got.RefreshToken != "rt-new" || got.Expiry != exp.Format(time.RFC3339) {
		t.Fatalf("saved auth = %+v", got)
	}
	if got.Realm != "teknoir" || got.Namespace != "ns1" {
		t.Fatalf("other fields lost: %+v", got)
	}
}

func TestAccessTokenErrors(t *testing.T) {
	boom := errors.New("boom")
	cfg := cfgWith("d.example", config.AuthConfig{AccessToken: "a"})
	load := func() (*config.Config, error) { return cfg, nil }

	if _, err := accessToken(context.Background(), "d.example", tokenDeps{load: func() (*config.Config, error) { return nil, boom }}); !errors.Is(err, boom) {
		t.Fatalf("load error lost: %v", err)
	}
	if _, err := accessToken(context.Background(), "d.example", tokenDeps{load: load,
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) { return nil, boom }}); !errors.Is(err, boom) {
		t.Fatalf("refresh error lost: %v", err)
	}
	_, err := accessToken(context.Background(), "d.example", tokenDeps{load: load,
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "n"}, nil
		},
		save: func(*config.Config) error { return boom }})
	if !errors.Is(err, boom) || err.Error() != "failed to save refreshed token: boom" {
		t.Fatalf("save error: %v", err)
	}
}

// With an unexpired token the real GetValidToken returns without any network access.
func TestAccessTokenUnexpiredNoNetwork(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "live", RefreshToken: "r", Expiry: exp})
	d := tokenDeps{
		load:    func() (*config.Config, error) { return cfg, nil },
		refresh: GetValidToken,
		save:    func(*config.Config) error { t.Fatal("unexpected save"); return nil },
	}
	tok, err := accessToken(context.Background(), "teknoir.cloud", d)
	if err != nil || tok != "live" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

// Concurrent AccessToken calls must not interleave the refresh cycle: each
// refresh sees the token the previous one stored.
func TestAccessTokenSerialisesRefresh(t *testing.T) {
	cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "t0", RefreshToken: "r0"})
	var inRefresh, maxIn int32
	var mu sync.Mutex
	n := 0
	d := tokenDeps{
		load: func() (*config.Config, error) {
			mu.Lock()
			defer mu.Unlock()
			c := *cfg
			c.Auths = map[string]config.AuthConfig{}
			for k, v := range cfg.Auths {
				c.Auths[k] = v
			}
			return &c, nil
		},
		refresh: func(_ context.Context, _ string, a config.AuthConfig) (*oauth2.Token, error) {
			if v := atomic.AddInt32(&inRefresh, 1); v > atomic.LoadInt32(&maxIn) {
				atomic.StoreInt32(&maxIn, v)
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&inRefresh, -1)
			mu.Lock()
			n++
			nn := n
			mu.Unlock()
			return &oauth2.Token{AccessToken: a.AccessToken + "+", RefreshToken: fmt.Sprintf("r%d", nn)}, nil
		},
		save: func(c *config.Config) error {
			mu.Lock()
			cfg = c
			mu.Unlock()
			return nil
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = accessToken(context.Background(), "teknoir.cloud", d) }()
	}
	wg.Wait()
	if maxIn != 1 {
		t.Fatalf("refresh ran %d at a time", maxIn)
	}
	if got := cfg.Auths[config.SanitizeDomain("teknoir.cloud")].AccessToken; got != "t0++++++++" {
		t.Fatalf("lost update: %q", got)
	}
}

func TestFileLockExcludesAndRecovers(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".tnctl.yaml")
	if err := os.WriteFile(f, []byte("domain: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(f)
	defer viper.Reset()
	un, err := fileLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := fileLock(ctx); err == nil {
		t.Fatal("second lock acquired while held")
	}
	un()
	un2, err := fileLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	un2()
	// stale lock is taken over
	_ = os.WriteFile(f+".lock", nil, 0o600)
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(f+".lock", old, old)
	un3, err := fileLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	un3()
}

func TestFreshAccessTokenForcesRefresh(t *testing.T) {
	var sawExpiry []string
	d := tokenDeps{
		load: func() (*config.Config, error) {
			return &config.Config{Auths: map[string]config.AuthConfig{
				config.SanitizeDomain("example.com"): {AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).Format(time.RFC3339)},
			}}, nil
		},
		refresh: func(_ context.Context, _ string, a config.AuthConfig) (*oauth2.Token, error) {
			sawExpiry = append(sawExpiry, a.Expiry)
			return &oauth2.Token{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(5 * time.Minute)}, nil
		},
		save: func(*config.Config) error { return nil },
	}
	refresh := d.refresh
	d.refresh = func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
		a.Expiry = ""
		return refresh(ctx, domain, a)
	}
	tok, err := accessToken(context.Background(), "example.com", d)
	if err != nil || tok != "new" {
		t.Fatalf("got %q, %v", tok, err)
	}
	if len(sawExpiry) != 1 || sawExpiry[0] != "" {
		t.Fatalf("refresh did not see a cleared expiry: %v", sawExpiry)
	}
}

const fileLogin = "domain: teknoir.cloud\nauths:\n  teknoir_cloud:\n    access_token: at-old\n    refresh_token: rt-old\n    namespace: teknoir-demo\n"

// useFileLogin makes the global viper read a config file holding fileLogin, as tnctl does at start,
// and returns its path.
func useFileLogin(t *testing.T) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), ".tnctl.yaml")
	if err := os.WriteFile(path, []byte(fileLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	return path
}

// The config file is removed while a long-lived tnctl (tnctl mcp) runs, the reset the troubleshooting
// docs give: after the next Reload the login is gone, no refresh is made and the file stays removed.
func TestRefreshAfterTheConfigWasRemovedIsNotLoggedIn(t *testing.T) {
	path := useFileLogin(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	config.Reload() // the next tool call
	refreshed := false
	d := tokenDeps{
		load: config.LoadFresh,
		refresh: func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
			refreshed = true
			return &oauth2.Token{AccessToken: "at-new", RefreshToken: "rt-new", Expiry: time.Now().Add(time.Hour)}, nil
		},
		save: func(c *config.Config) error { return c.Save() },
	}
	_, err := accessToken(context.Background(), "teknoir.cloud", d)
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("after the config was removed: %v, want not logged in", err)
	}
	if refreshed {
		t.Error("the removed login's refresh token was redeemed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the removed config file is back (%v)", err)
	}
}

// Another tnctl (tnctl login, which takes no file lock) replaces the login while this process
// refreshes the old one: the new login stays, and the rotated token of the old one is not saved.
func TestRefreshKeepsAConcurrentLogin(t *testing.T) {
	path := useFileLogin(t)
	saved := 0
	d := tokenDeps{
		load: config.LoadFresh,
		lock: fileLock,
		refresh: func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
			// the other process writes while the refresh is on the network
			other := strings.NewReplacer("rt-old", "rt-newlogin", "at-old", "at-newlogin", "teknoir-demo", "acme").Replace(fileLogin)
			if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
				t.Error(err)
			}
			return &oauth2.Token{AccessToken: "at-rot", RefreshToken: "rt-rot", Expiry: time.Now().Add(time.Hour)}, nil
		},
		save: func(c *config.Config) error { saved++; return c.Save() },
	}
	if _, err := accessToken(context.Background(), "teknoir.cloud", d); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "acme") || !strings.Contains(s, "rt-newlogin") || strings.Contains(s, "rt-rot") || saved != 0 {
		t.Errorf("the concurrent login was overwritten: acme=%v rt-newlogin=%v rt-rot=%v, %d saves",
			strings.Contains(s, "acme"), strings.Contains(s, "rt-newlogin"), strings.Contains(s, "rt-rot"), saved)
	}
}

// A tnctl ns acme lands while this process refreshes (AccessToken) or redeems a hub token
// (HubAccessToken): both the teamspace and the rotated refresh token stay in the file.
func TestRefreshKeepsAConcurrentNsAndSavesTheToken(t *testing.T) {
	for _, hub := range []bool{false, true} {
		path := useFileLogin(t)
		other := func() {
			if err := os.WriteFile(path, []byte(strings.Replace(fileLogin, "teknoir-demo", "acme", 1)), 0o600); err != nil {
				t.Error(err)
			}
		}
		rot := &oauth2.Token{AccessToken: "at-rot", RefreshToken: "rt-rot", Expiry: time.Now().Add(time.Hour)}
		d := tokenDeps{
			load: config.LoadFresh,
			lock: fileLock,
			refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
				other()
				return rot, nil
			},
			hub: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
				other()
				return rot, nil
			},
			save: func(c *config.Config) error { return c.Save() },
		}
		var err error
		if hub {
			_, _, err = hubAccessToken(context.Background(), "teknoir.cloud", d)
		} else {
			_, err = accessToken(context.Background(), "teknoir.cloud", d)
		}
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := config.LoadFresh()
		if err != nil {
			t.Fatal(err)
		}
		a := fresh.Auths["teknoir_cloud"]
		if a.Namespace != "acme" || a.RefreshToken != "rt-rot" {
			t.Errorf("hub=%v: the file holds namespace %q and refresh token %q, want acme and rt-rot", hub, a.Namespace, a.RefreshToken)
		}
		if wantAT := map[bool]string{false: "at-rot", true: "at-old"}[hub]; a.AccessToken != wantAT {
			t.Errorf("hub=%v: stored access token %q, want %q (a hub token never replaces the main token)", hub, a.AccessToken, wantAT)
		}
	}
}

// saveRefreshed with faked deps: the login whose refresh token was redeemed gets the new tokens, a
// replaced or removed login is left alone, and a load error is returned.
func TestSaveRefreshed(t *testing.T) {
	set := func(a *config.AuthConfig) { a.RefreshToken = "rt-new" }
	for name, c := range map[string]struct {
		stored  *config.Config
		wantRT  string
		wantSav bool
	}{
		"same login":       {cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "a", RefreshToken: "rt-old", Namespace: "ns"}), "rt-new", true},
		"replaced login":   {cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "a", RefreshToken: "rt-other"}), "rt-other", false},
		"removed login":    {cfgWith("other.example", config.AuthConfig{AccessToken: "a", RefreshToken: "rt-old"}), "", false},
		"no refresh token": {cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: "a"}), "", false},
	} {
		t.Run(name, func(t *testing.T) {
			var saved *config.Config
			d := tokenDeps{
				load: func() (*config.Config, error) { return c.stored, nil },
				save: func(cfg *config.Config) error { saved = cfg; return nil },
			}
			if err := saveRefreshed(d, "teknoir_cloud", "rt-old", set); err != nil {
				t.Fatal(err)
			}
			if (saved != nil) != c.wantSav {
				t.Fatalf("saved %v, want %v", saved != nil, c.wantSav)
			}
			if saved != nil {
				if a := saved.Auths["teknoir_cloud"]; a.RefreshToken != c.wantRT || a.Namespace != "ns" {
					t.Errorf("saved refresh token %q and namespace %q, want %q and ns", a.RefreshToken, a.Namespace, c.wantRT)
				}
			}
		})
	}
	boom := errors.New("boom")
	d := tokenDeps{load: func() (*config.Config, error) { return nil, boom }}
	if err := saveRefreshed(d, "teknoir_cloud", "rt-old", set); !errors.Is(err, boom) {
		t.Errorf("load error: %v", err)
	}
}

// The config file is removed while a refresh is on the network, after this tool call's Reload saw
// it: the refresh saves nothing, the file stays removed, and the next call is not logged in.
func TestRefreshDuringWhichTheConfigIsRemovedSavesNothing(t *testing.T) {
	for _, hub := range []bool{false, true} {
		path := useFileLogin(t)
		config.Reload()
		rot := &oauth2.Token{AccessToken: "at-rot", RefreshToken: "rt-rot", Expiry: time.Now().Add(time.Hour)}
		remove := func() {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}
		saves := 0
		d := tokenDeps{
			load: config.LoadFresh,
			lock: fileLock,
			refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
				remove()
				return rot, nil
			},
			hub: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
				remove()
				return rot, nil
			},
			save: func(c *config.Config) error { saves++; return c.Save() },
		}
		var err error
		if hub {
			_, _, err = hubAccessToken(context.Background(), "teknoir.cloud", d)
		} else {
			_, err = accessToken(context.Background(), "teknoir.cloud", d)
		}
		if err != nil {
			t.Errorf("hub=%v: %v, want the refreshed token", hub, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || saves != 0 {
			t.Errorf("hub=%v: the removed config file is back (%v), %d saves", hub, err, saves)
		}
		config.Reload() // the next tool call
		d.refresh = func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) { return rot, nil }
		if _, err := accessToken(context.Background(), "teknoir.cloud", d); !errors.Is(err, ErrNotLoggedIn) {
			t.Errorf("hub=%v: the next call after the remove: %v, want not logged in", hub, err)
		}
	}
}

// A long-lived tnctl that never reloads the config (wg-proxy) renews after the config file was
// removed: not logged in, the removed login's refresh token is not redeemed and the file stays gone.
func TestRenewalAfterTheConfigWasRemovedWithoutReload(t *testing.T) {
	path := useFileLogin(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	refreshed := false
	d := forceRefresh(tokenDeps{
		load: config.LoadFresh,
		lock: fileLock,
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
			refreshed = true
			return &oauth2.Token{AccessToken: "at-rot", RefreshToken: "rt-rot", Expiry: time.Now().Add(time.Hour)}, nil
		},
		save: func(c *config.Config) error { return c.Save() },
	})
	if _, err := accessToken(context.Background(), "teknoir.cloud", d); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("renewal after the config was removed: %v, want not logged in", err)
	}
	if refreshed {
		t.Error("the removed login's refresh token was redeemed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the removed config file is back (%v)", err)
	}
}

// A file an older tnctl saved during `tnctl -n acme <command>` (its Save wrote the bound flag):
// every later run targets acme. A token refresh, with no -n and no user action, must not switch it.
func TestRefreshKeepsTheActiveTeamspace(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), ".tnctl.yaml")
	body := "domain: teknoir.cloud\nflag_device: \"\"\nflag_namespace: acme\nauths:\n  teknoir_cloud:\n    access_token: at-old\n    refresh_token: rt-old\n    namespace: teknoir-demo\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	ns := func() string {
		config.Reload() // what tnctl mcp does before each call; a new process reads the same
		c, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		return c.GetNamespace()
	}
	before := ns()
	d := tokenDeps{
		load: config.LoadFresh,
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "at-new", RefreshToken: "rt-new", Expiry: time.Now().Add(time.Hour)}, nil
		},
		save: func(c *config.Config) error { return c.Save() },
	}
	if _, err := accessToken(context.Background(), "teknoir.cloud", d); err != nil {
		t.Fatal(err)
	}
	after := ns()
	t.Logf("active teamspace before the refresh %q, after %q", before, after)
	if before != after {
		t.Errorf("a token refresh switched the active teamspace from %q to %q", before, after)
	}
}

// A tnctl login --domain other.example lands while this process refreshes teknoir.cloud: the
// refresh saves its rotated token and keeps the other logins and the domain the other process chose.
func TestRefreshKeepsAConcurrentLoginToAnotherDomain(t *testing.T) {
	path := useFileLogin(t)
	const other = "domain: other.example\nauths:\n  teknoir_cloud:\n    access_token: at-old\n    refresh_token: rt-old\n    namespace: teknoir-demo\n  other_example:\n    access_token: at-x\n    refresh_token: rt-x\n    namespace: x\n"
	d := tokenDeps{
		load: config.LoadFresh,
		lock: fileLock,
		refresh: func(context.Context, string, config.AuthConfig) (*oauth2.Token, error) {
			if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
				t.Error(err)
			}
			return &oauth2.Token{AccessToken: "at-rot", RefreshToken: "rt-rot", Expiry: time.Now().Add(time.Hour)}, nil
		},
		save: func(c *config.Config) error { return c.Save() },
	}
	if _, err := accessToken(context.Background(), "teknoir.cloud", d); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadFresh()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Domain != "other.example" {
		t.Errorf("domain %q, want other.example", fresh.Domain)
	}
	if a := fresh.Auths["other_example"]; a.RefreshToken != "rt-x" {
		t.Errorf("the other domain's login was lost: %v", a.RefreshToken == "")
	}
	if a := fresh.Auths["teknoir_cloud"]; a.RefreshToken != "rt-rot" {
		t.Errorf("rotated token not saved")
	}
}
