package auth

import (
	"github.com/teknoir/cli/pkg/config"
)

// Identity is who the stored login belongs to, read from the config file without asking the
// platform (it proves nothing: the platform decides whether the login still works).
type Identity struct {
	Domain string
	// Email is the account's email claim, or its preferred username or subject when it has none.
	Email string
}

// StoredIdentity returns the identity of the login stored for domain. Without one it answers an
// error matching ErrNotLoggedIn.
func StoredIdentity(domain string) (*Identity, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a, ok := cfg.Auths[config.SanitizeDomain(domain)]
	if !ok || a.AccessToken == "" {
		return nil, &NotLoggedInError{Domain: domain}
	}
	c := jwtClaims(a.AccessToken)
	email := c.Email
	if email == "" {
		email = c.PreferredUsername
	}
	if email == "" {
		email = c.Sub
	}
	return &Identity{Domain: domain, Email: email}, nil
}
