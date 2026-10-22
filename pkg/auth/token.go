package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// ErrNotLoggedIn is matched (errors.Is) by the error AccessToken returns when
// ~/.tnctl.yaml holds no credentials for the domain.
var ErrNotLoggedIn = fmt.Errorf("not logged in")

// NotLoggedInError reports that there are no stored credentials for a domain.
type NotLoggedInError struct{ Domain string }

func (e *NotLoggedInError) Error() string {
	return fmt.Sprintf("no credentials found for domain %s. Please log in first", e.Domain)
}

// Is makes errors.Is(err, ErrNotLoggedIn) true.
func (e *NotLoggedInError) Is(target error) bool { return target == ErrNotLoggedIn }

// tokenDeps are the side effects AccessToken needs, so they can be faked.
type tokenDeps struct {
	load    func() (*config.Config, error)
	refresh func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error)
	save    func(*config.Config) error
	// hub redeems the refresh token for a hub token (nil: redeemHubToken).
	hub func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error)
	// lock serialises the read-refresh-save cycle across processes; nil = none.
	lock func(ctx context.Context) (unlock func(), err error)
}

// tokenMu serialises the cycle inside one process.
var tokenMu sync.Mutex

// fileLock takes an exclusive lock file next to the config file, so two tnctl
// processes (a long-lived wg-proxy and another command) never run the
// rotating-refresh-token cycle at the same time. A lock older than 30s is
// considered stale (crashed holder) and is taken over.
func fileLock(ctx context.Context) (func(), error) {
	path := viper.ConfigFileUsed()
	if path == "" {
		return func() {}, nil
	}
	lock := path + ".lock"
	deadline := time.Now().Add(15 * time.Second)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		if !os.IsExist(err) {
			return func() {}, nil // cannot lock (read-only dir): proceed unlocked
		}
		if st, serr := os.Stat(lock); serr == nil && time.Since(st.ModTime()) > 30*time.Second {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for the tnctl config lock %s", lock)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

var defaultTokenDeps = tokenDeps{
	// LoadFresh re-reads the file, so a refresh token rotated by another
	// process is never overwritten with a stale one.
	load:    config.LoadFresh,
	refresh: GetValidToken,
	save:    func(c *config.Config) error { return c.Save() },
	lock:    fileLock,
}

// AccessToken loads the tnctl configuration, returns a valid access token for
// the domain (the main token: it never has the hub audience, see HubAccessToken) (refreshing it when it is about to expire) and persists a
// refreshed token back to the configuration. It re-reads the configuration on
// every call, so long-running processes always get the current token.
func AccessToken(ctx context.Context, domain string) (string, error) {
	return accessToken(ctx, domain, defaultTokenDeps)
}

// FreshAccessToken is like AccessToken but always redeems the refresh token,
// so the returned access token is newly issued by Keycloak. The WireGuard hub
// requires a newly issued token on every session renewal; a user who was
// disabled or logged out in Keycloak therefore loses the tunnel at the next
// renewal instead of when the old access token expires.
func FreshAccessToken(ctx context.Context, domain string) (string, error) {
	return accessToken(ctx, domain, forceRefresh(defaultTokenDeps))
}

// forceRefresh makes d always redeem the refresh token, even while the access token is valid.
func forceRefresh(d tokenDeps) tokenDeps {
	refresh := d.refresh
	d.refresh = func(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
		a.Expiry = "" // unparsable expiry makes GetValidToken redeem the refresh token
		return refresh(ctx, domain, a)
	}
	return d
}

// HubAccessToken returns a newly issued access token for the WireGuard hub: it always redeems the
// refresh token, with the scope teknoir-ztna when the login was granted it, so the token carries the
// hub audience (ztna reports whether it does). It is never cached and never used for anything but
// wg.<domain>: only the rotated refresh token is saved, the stored access token (the main token,
// which Backstage and the fleet API see) and its expiry stay untouched. A login without the scope
// (production, airgapped, a login made before the realm had it) is redeemed without a scope, as
// before: the token then has no hub audience, which a hub in enforce mode refuses.
func HubAccessToken(ctx context.Context, domain string) (token string, ztna bool, err error) {
	return hubAccessToken(ctx, domain, defaultTokenDeps)
}

func hubAccessToken(ctx context.Context, domain string, d tokenDeps) (string, bool, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if d.lock != nil {
		unlock, err := d.lock(ctx)
		if err != nil {
			return "", false, err
		}
		defer unlock()
	}
	cfg, err := d.load()
	if err != nil {
		return "", false, err
	}
	key := config.SanitizeDomain(domain)
	authData, exists := cfg.Auths[key]
	if !exists || authData.AccessToken == "" {
		return "", false, &NotLoggedInError{Domain: domain}
	}
	if err := usableRefreshToken(authData.RefreshToken); err != nil {
		return "", false, err
	}
	redeem := d.hub
	if redeem == nil {
		redeem = redeemHubToken
	}
	token, err := redeem(ctx, domain, authData)
	if err != nil {
		return "", false, err
	}
	if token.RefreshToken != authData.RefreshToken {
		authData.RefreshToken = token.RefreshToken
		cfg.Auths[key] = authData
		if err := d.save(cfg); err != nil {
			return "", false, fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}
	return token.AccessToken, hasAudience(token.AccessToken, ztnaAudience), nil
}

// redeemHubToken redeems the refresh token with the hub scope when the login was granted it. When
// the realm refuses the scope (invalid_scope: the realm change was rolled back and teknoir-cli lost
// the optional scope), it redeems once more downscoped, with a warning: the token then has no hub
// audience, which a hub in transition mode still accepts and one in enforce mode refuses (a clear
// "run tnctl login" / "ask an admin" then, not an unusable tnctl).
func redeemHubToken(ctx context.Context, domain string, a config.AuthConfig) (*oauth2.Token, error) {
	if !hasScope(a.RefreshToken, ztnaScope) {
		return redeemRefreshToken(ctx, domain, a, "")
	}
	token, err := redeemRefreshToken(ctx, domain, a, hubScope)
	if !isInvalidScope(err) {
		return token, err
	}
	warnHubScopeRefused()
	return redeemRefreshToken(ctx, domain, a, mainScope)
}

// isInvalidScope reports whether err is the token endpoint's invalid_scope answer.
func isInvalidScope(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && re.ErrorCode == "invalid_scope"
}

// warnWriter and warnedHubScope serve warnHubScopeRefused: one warning per process, however many
// renewals a long-lived wg-proxy does.
var (
	warnWriter     io.Writer = os.Stderr
	warnedHubScope atomic.Bool
)

func warnHubScopeRefused() {
	if warnedHubScope.Swap(true) {
		return
	}
	_, _ = fmt.Fprintln(warnWriter, "Warning: the realm refused the scope teknoir-ztna for this client (invalid_scope); continuing without the hub audience. A WireGuard hub that enforces the audience will refuse this login: ask a platform admin to add teknoir-ztna to the optional scopes of the teknoir-cli client.")
}

func accessToken(ctx context.Context, domain string, d tokenDeps) (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if d.lock != nil {
		unlock, err := d.lock(ctx)
		if err != nil {
			return "", err
		}
		defer unlock()
	}
	cfg, err := d.load()
	if err != nil {
		return "", err
	}

	key := config.SanitizeDomain(domain)
	authData, exists := cfg.Auths[key]
	if !exists || authData.AccessToken == "" {
		return "", &NotLoggedInError{Domain: domain}
	}

	token, err := d.refresh(ctx, domain, authData)
	if err != nil {
		return "", err
	}

	// Update config with new token if refreshed
	if token.AccessToken != authData.AccessToken || token.RefreshToken != authData.RefreshToken {
		authData.AccessToken = token.AccessToken
		authData.RefreshToken = token.RefreshToken
		authData.Expiry = token.Expiry.Format(time.RFC3339)
		cfg.Auths[key] = authData
		if err := d.save(cfg); err != nil {
			return "", fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}
	return token.AccessToken, nil
}
