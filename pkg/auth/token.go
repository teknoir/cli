package auth

import (
	"context"
	"fmt"
	"os"
	"sync"
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
// the domain (refreshing it when it is about to expire) and persists a
// refreshed token back to the configuration. It re-reads the configuration on
// every call, so long-running processes always get the current token.
func AccessToken(ctx context.Context, domain string) (string, error) {
	return accessToken(ctx, domain, defaultTokenDeps)
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
