package auth

import (
	"context"
	"fmt"
	"time"

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
}

var defaultTokenDeps = tokenDeps{
	load:    config.Load,
	refresh: GetValidToken,
	save:    func(c *config.Config) error { return c.Save() },
}

// AccessToken loads the tnctl configuration, returns a valid access token for
// the domain (refreshing it when it is about to expire) and persists a
// refreshed token back to the configuration. It re-reads the configuration on
// every call, so long-running processes always get the current token.
func AccessToken(ctx context.Context, domain string) (string, error) {
	return accessToken(ctx, domain, defaultTokenDeps)
}

func accessToken(ctx context.Context, domain string, d tokenDeps) (string, error) {
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
