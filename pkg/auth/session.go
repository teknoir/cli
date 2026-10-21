package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// SessionExpiredMessage is what the user sees when the stored login can no longer be renewed.
const SessionExpiredMessage = "Your tnctl login has expired. Run: tnctl login"

// ErrSessionExpired is matched (errors.Is) by every error that means "the refresh token is dead":
// Keycloak rejected it (revoked, user disabled, offline session idle for too long, realm reset)
// or it is known locally to have expired. Network and server errors never match it: they say
// nothing about the login and are worth retrying.
var ErrSessionExpired = errors.New("session expired")

// SessionExpiredError reports that the login cannot be renewed and `tnctl login` is needed.
type SessionExpiredError struct {
	// Cause is the underlying reason (the token endpoint's answer), for --debug; may be nil.
	Cause error
}

func (e *SessionExpiredError) Error() string { return SessionExpiredMessage }
func (e *SessionExpiredError) Unwrap() error { return e.Cause }

// Is makes errors.Is(err, ErrSessionExpired) true.
func (e *SessionExpiredError) Is(target error) bool { return target == ErrSessionExpired }

// Detail is the cause in one line, or "" when there is none.
func (e *SessionExpiredError) Detail() string {
	if e.Cause == nil {
		return ""
	}
	return e.Cause.Error()
}

// mapRefreshError turns a failed refresh into ErrSessionExpired when the token endpoint refused
// the refresh token, and into a plain (retryable) error otherwise.
func mapRefreshError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		switch re.ErrorCode {
		case "invalid_grant", "invalid_token", "unauthorized_client":
			return &SessionExpiredError{Cause: err}
		}
	}
	return fmt.Errorf("failed to refresh token: %w", err)
}

// maxRefreshMargin is how long before its expiry an access token is renewed.
const maxRefreshMargin = 60 * time.Second

// refreshMargin is min(60s, lifetime/2): a token that lives 60 s is renewed in its second half, one
// that lives 300 s a minute before it ends. The lifetime is read from the token's iat and exp
// claims; for a token that is not a readable JWT the margin is 60 s.
func refreshMargin(accessToken string) time.Duration {
	c := jwtClaims(accessToken)
	if c.Iat > 0 && c.Exp > c.Iat {
		if half := time.Duration(c.Exp-c.Iat) * time.Second / 2; half < maxRefreshMargin {
			return half
		}
	}
	return maxRefreshMargin
}

// claims are the JWT claims tnctl looks at. They are read without verifying the signature: the
// token is the user's own and the values only decide when to refresh and what to display.
type claims struct {
	Exp               int64  `json:"exp"`
	Iat               int64  `json:"iat"`
	Typ               string `json:"typ"`
	Sub               string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
}

func jwtClaims(token string) claims {
	var c claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

// refreshTokenExpiry is the expiry of the refresh token when it carries one. Keycloak's offline
// tokens have none (ok is false): they end when revoked or after the offline idle timeout.
func refreshTokenExpiry(refreshToken string) (time.Time, bool) {
	c := jwtClaims(refreshToken)
	if c.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}

// isOfflineToken reports whether the refresh token is a Keycloak offline token.
func isOfflineToken(refreshToken string) bool {
	return strings.EqualFold(jwtClaims(refreshToken).Typ, "Offline")
}

// Status describes a stored login, for `tnctl login --check`. It never holds a token.
type Status struct {
	Domain string `json:"domain"`
	// User is the account the access token was issued to (preferred_username, email or subject).
	User string `json:"user,omitempty"`
	// AccessExpires is when the current access token ends.
	AccessExpires time.Time `json:"accessExpires"`
	// Refreshed is true when the check had to redeem the refresh token (so the login is alive).
	Refreshed bool `json:"refreshed"`
	// Offline is true when the refresh token is an offline token.
	Offline bool `json:"offline"`
	// RefreshExpires is when the refresh token ends; nil when it has no fixed expiry (an offline
	// session ends when revoked or after the realm's offline idle timeout, 30 days by default).
	RefreshExpires *time.Time `json:"refreshExpires,omitempty"`
}

// Check verifies that the stored login for the domain works: it returns a Status when the access
// token is still valid or a refresh succeeds (the refreshed tokens are saved), and an error
// matching ErrSessionExpired when the login is dead, ErrNotLoggedIn when there is none. Other
// errors (network, server) are returned as they are: they do not say the login is dead.
func Check(ctx context.Context, domain string) (*Status, error) {
	return check(ctx, domain, defaultTokenDeps)
}

func check(ctx context.Context, domain string, d tokenDeps) (*Status, error) {
	before, err := d.load()
	if err != nil {
		return nil, err
	}
	old := before.Auths[config.SanitizeDomain(domain)]
	if _, err := accessToken(ctx, domain, d); err != nil {
		return nil, err
	}
	cfg, err := d.load()
	if err != nil {
		return nil, err
	}
	a := cfg.Auths[config.SanitizeDomain(domain)]
	st := &Status{Domain: domain, Offline: isOfflineToken(a.RefreshToken)}
	st.Refreshed = a.AccessToken != old.AccessToken
	if exp, err := time.Parse(time.RFC3339, a.Expiry); err == nil {
		st.AccessExpires = exp
	}
	c := jwtClaims(a.AccessToken)
	switch {
	case c.PreferredUsername != "":
		st.User = c.PreferredUsername
	case c.Email != "":
		st.User = c.Email
	default:
		st.User = c.Sub
	}
	if exp, ok := refreshTokenExpiry(a.RefreshToken); ok {
		st.RefreshExpires = &exp
	}
	return st, nil
}
