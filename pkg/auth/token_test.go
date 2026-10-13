package auth

import (
	"context"
	"errors"
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
