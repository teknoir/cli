package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// jwt builds an unsigned token with the claims (only the payload is ever read).
func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + "." + enc([]byte("sig"))
}

// fakeIDP serves the token endpoint with a fixed answer and counts the requests.
func fakeIDP(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := tokenURL
	tokenURL = func(string, string) string { return srv.URL + "/token" }
	t.Cleanup(func() { tokenURL = old })
	return srv, &n
}

func expiredAccess() config.AuthConfig {
	return config.AuthConfig{
		AccessToken: "at", RefreshToken: "rt", Realm: "master",
		Expiry: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}
}

func TestRefreshRejectedMapsToSessionExpired(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"invalid_grant":       {400, `{"error":"invalid_grant","error_description":"Token is not active"}`},
		"invalid_token":       {401, `{"error":"invalid_token","error_description":"gone"}`},
		"unauthorized_client": {400, `{"error":"unauthorized_client"}`},
		"session not active":  {400, `{"error":"invalid_grant","error_description":"Session not active"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeIDP(t, c.status, c.body)
			_, err := GetValidToken(context.Background(), "teknoir.cloud", expiredAccess())
			if !errors.Is(err, ErrSessionExpired) {
				t.Fatalf("want ErrSessionExpired, got %v", err)
			}
			if err.Error() != SessionExpiredMessage {
				t.Errorf("message %q", err.Error())
			}
			var se *SessionExpiredError
			if !errors.As(err, &se) || !strings.Contains(se.Detail(), "invalid") && !strings.Contains(se.Detail(), "unauthorized") {
				t.Errorf("the cause must be kept for --debug: %v", se)
			}
			if errors.Is(err, ErrNotLoggedIn) {
				t.Error("an expired session is not 'not logged in'")
			}
		})
	}
}

func TestRefreshFailuresThatSayNothingAboutTheLoginAreNotExpiry(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"server error":            {500, `{"error":"server_error"}`},
		"bad gateway html":        {502, `<html>bad gateway</html>`},
		"service unavailable":     {503, ``},
		"invalid_request":         {400, `{"error":"invalid_request"}`},
		"temporarily unavailable": {400, `{"error":"temporarily_unavailable"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeIDP(t, c.status, c.body)
			_, err := GetValidToken(context.Background(), "teknoir.cloud", expiredAccess())
			if err == nil || errors.Is(err, ErrSessionExpired) {
				t.Fatalf("must be a plain, retryable error: %v", err)
			}
			if !strings.Contains(err.Error(), "failed to refresh token") {
				t.Errorf("message %q", err.Error())
			}
		})
	}
}

func TestRefreshNetworkErrorIsTransient(t *testing.T) {
	srv, _ := fakeIDP(t, 200, `{}`)
	srv.Close() // connection refused
	_, err := GetValidToken(context.Background(), "teknoir.cloud", expiredAccess())
	if err == nil || errors.Is(err, ErrSessionExpired) {
		t.Fatalf("a network error must not be reported as an expired login: %v", err)
	}
}

func TestLocallyExpiredRefreshTokenSkipsTheRequest(t *testing.T) {
	_, n := fakeIDP(t, 200, `{"access_token":"x","token_type":"bearer","expires_in":300}`)
	a := expiredAccess()
	a.RefreshToken = jwt(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(-time.Hour).Unix()})
	_, err := GetValidToken(context.Background(), "teknoir.cloud", a)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
	if n.Load() != 0 {
		t.Error("the token endpoint was called although the refresh token is known to be expired")
	}
}

func TestOfflineAndLiveRefreshTokensAreRedeemed(t *testing.T) {
	_, n := fakeIDP(t, 200, `{"access_token":"new","refresh_token":"rt2","token_type":"bearer","expires_in":300}`)
	for name, rt := range map[string]string{
		"offline, no exp": jwt(t, map[string]any{"typ": "Offline"}),
		"offline, exp 0":  jwt(t, map[string]any{"typ": "Offline", "exp": 0}),
		"future exp":      jwt(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(time.Hour).Unix()}),
		"opaque":          "not-a-jwt",
	} {
		a := expiredAccess()
		a.RefreshToken = rt
		tok, err := GetValidToken(context.Background(), "teknoir.cloud", a)
		if err != nil || tok.AccessToken != "new" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n.Load() != 4 {
		t.Errorf("requests: %d", n.Load())
	}
}

func TestMissingRefreshTokenMeansExpiredOnceTheAccessTokenEnds(t *testing.T) {
	_, n := fakeIDP(t, 200, `{}`)
	a := expiredAccess()
	a.RefreshToken = ""
	if _, err := GetValidToken(context.Background(), "teknoir.cloud", a); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
	if n.Load() != 0 {
		t.Error("no request without a refresh token")
	}
	// while the access token lasts it still works
	a.Expiry = time.Now().Add(time.Hour).Format(time.RFC3339)
	if tok, err := GetValidToken(context.Background(), "teknoir.cloud", a); err != nil || tok.AccessToken != "at" {
		t.Fatalf("%v", err)
	}
}

func TestFreshAccessTokenWithDeadRefreshToken(t *testing.T) {
	fakeIDP(t, 400, `{"error":"invalid_grant"}`)
	cfg := cfgWith("teknoir.cloud", config.AuthConfig{
		AccessToken: "at", RefreshToken: "rt", Realm: "master",
		Expiry: time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	saved := false
	d := defaultTokenDeps
	d.load = func() (*config.Config, error) { return cfg, nil }
	d.save = func(*config.Config) error { saved = true; return nil }
	d.lock = nil
	refresh := d.refresh
	d.refresh = func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
		a.Expiry = ""
		return refresh(ctx, domain, a)
	}
	_, err := accessToken(context.Background(), "teknoir.cloud", d)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
	if saved {
		t.Error("nothing may be saved when the refresh failed")
	}
}

func TestRefreshMargin(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name  string
		token string
		want  time.Duration
	}{
		{"60 s token: half of it", jwt(t, map[string]any{"iat": now, "exp": now + 60}), 30 * time.Second},
		{"300 s token: 60 s", jwt(t, map[string]any{"iat": now, "exp": now + 300}), 60 * time.Second},
		{"10 s token", jwt(t, map[string]any{"iat": now, "exp": now + 10}), 5 * time.Second},
		{"no iat", jwt(t, map[string]any{"exp": now + 300}), 60 * time.Second},
		{"opaque", "abc", 60 * time.Second},
		{"empty", "", 60 * time.Second},
	}
	for _, c := range cases {
		if got := refreshMargin(c.token); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestCachedTokenUsedUntilTheMargin(t *testing.T) {
	_, n := fakeIDP(t, 400, `{"error":"invalid_grant"}`)
	now := time.Now()
	at := jwt(t, map[string]any{"iat": now.Unix(), "exp": now.Add(60 * time.Second).Unix()})
	// 60 s token with 40 s left: margin is 30 s, so it is still used (the old fixed 1 min margin refreshed every call)
	a := config.AuthConfig{AccessToken: at, RefreshToken: "rt", Realm: "master", Expiry: now.Add(40 * time.Second).Format(time.RFC3339)}
	if tok, err := GetValidToken(context.Background(), "teknoir.cloud", a); err != nil || tok.AccessToken != at {
		t.Fatalf("%v", err)
	}
	// with 20 s left it refreshes
	a.Expiry = now.Add(20 * time.Second).Format(time.RFC3339)
	if _, err := GetValidToken(context.Background(), "teknoir.cloud", a); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests %d", n.Load())
	}
}

func TestCheck(t *testing.T) {
	now := time.Now()
	at := jwt(t, map[string]any{"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "preferred_username": "anders", "sub": "u1"})
	t.Run("valid access token still redeems the refresh token", func(t *testing.T) {
		newRT := jwt(t, map[string]any{"typ": "Offline"})
		_, n := fakeIDP(t, 200, `{"access_token":"`+at+`","refresh_token":"`+newRT+`","token_type":"bearer","expires_in":300}`)
		cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: at, RefreshToken: jwt(t, map[string]any{"typ": "Offline"}), Realm: "master", Expiry: now.Add(5 * time.Minute).Format(time.RFC3339)})
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }, refresh: GetValidToken, save: func(*config.Config) error { return nil }}
		st, err := check(context.Background(), "teknoir.cloud", d)
		if err != nil {
			t.Fatal(err)
		}
		if st.User != "anders" || !st.Offline || st.RefreshExpires != nil || n.Load() != 1 {
			t.Errorf("%+v, %d requests", st, n.Load())
		}
	})
	t.Run("valid access token but revoked session is caught", func(t *testing.T) {
		fakeIDP(t, 400, `{"error":"invalid_grant"}`)
		cfg := cfgWith("teknoir.cloud", config.AuthConfig{AccessToken: at, RefreshToken: jwt(t, map[string]any{"typ": "Offline"}), Realm: "master", Expiry: now.Add(5 * time.Minute).Format(time.RFC3339)})
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }, refresh: GetValidToken, save: func(*config.Config) error { return nil }}
		if _, err := check(context.Background(), "teknoir.cloud", d); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("a revoked session passed --check on a cached access token: %v", err)
		}
	})
	t.Run("refresh works", func(t *testing.T) {
		newAT := jwt(t, map[string]any{"preferred_username": "anders", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})
		rtExp := now.Add(48 * time.Hour).Unix()
		newRT := jwt(t, map[string]any{"typ": "Refresh", "exp": rtExp})
		fakeIDP(t, 200, `{"access_token":"`+newAT+`","refresh_token":"`+newRT+`","token_type":"bearer","expires_in":300}`)
		cfg := cfgWith("teknoir.cloud", expiredAccess())
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }, refresh: GetValidToken, save: func(c *config.Config) error { return nil }}
		st, err := check(context.Background(), "teknoir.cloud", d)
		if err != nil {
			t.Fatal(err)
		}
		if st.User != "anders" || st.RefreshExpires == nil || st.RefreshExpires.Unix() != rtExp {
			t.Errorf("%+v", st)
		}
		b, _ := json.Marshal(st)
		if strings.Contains(string(b), newAT) || strings.Contains(string(b), newRT) {
			t.Error("a token leaked into the status")
		}
	})
	t.Run("dead refresh token", func(t *testing.T) {
		fakeIDP(t, 400, `{"error":"invalid_grant"}`)
		cfg := cfgWith("teknoir.cloud", expiredAccess())
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }, refresh: GetValidToken, save: func(*config.Config) error { return nil }}
		if _, err := check(context.Background(), "teknoir.cloud", d); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("network error is not expiry", func(t *testing.T) {
		srv, _ := fakeIDP(t, 200, `{}`)
		srv.Close()
		cfg := cfgWith("teknoir.cloud", expiredAccess())
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }, refresh: GetValidToken, save: func(*config.Config) error { return nil }}
		if _, err := check(context.Background(), "teknoir.cloud", d); err == nil || errors.Is(err, ErrSessionExpired) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("not logged in", func(t *testing.T) {
		cfg := cfgWith("other.example", config.AuthConfig{AccessToken: "x"})
		d := tokenDeps{load: func() (*config.Config, error) { return cfg, nil }}
		if _, err := check(context.Background(), "teknoir.cloud", d); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRefreshTokenJustPastExpIsLeftToKeycloak(t *testing.T) {
	// a host whose clock runs ahead must not declare a still-valid login dead
	_, n := fakeIDP(t, 200, `{"access_token":"new","refresh_token":"rt2","token_type":"bearer","expires_in":300}`)
	a := expiredAccess()
	a.RefreshToken = jwt(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := GetValidToken(context.Background(), "teknoir.cloud", a); err != nil || n.Load() != 1 {
		t.Fatalf("err %v, %d requests", err, n.Load())
	}
	// ... but one that is clearly past its exp is dead without a request
	a.RefreshToken = jwt(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(-refreshExpirySkew - time.Minute).Unix()})
	if _, err := GetValidToken(context.Background(), "teknoir.cloud", a); !errors.Is(err, ErrSessionExpired) || n.Load() != 1 {
		t.Fatalf("err %v, %d requests", err, n.Load())
	}
}
