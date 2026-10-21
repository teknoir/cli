package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// tokenURL is the token endpoint of a domain's realm. A variable so tests can point it at a fake.
var tokenURL = func(domain, realm string) string {
	return fmt.Sprintf("https://auth.%s/auth/realms/%s/protocol/openid-connect/token", domain, realm)
}

// SetTokenURLForTest points the token endpoint at a fake (for tests in other packages) and returns
// the function that restores it.
func SetTokenURLForTest(f func(domain, realm string) string) (restore func()) {
	old := tokenURL
	tokenURL = f
	return func() { tokenURL = old }
}

// refreshExpirySkew is how far past its exp a refresh token must be before tnctl gives up on it
// without asking Keycloak, so a fast local clock cannot kill a login that is still valid.
const refreshExpirySkew = 5 * time.Minute

// GetValidToken returns a valid access token, refreshing it if necessary. A refresh token that
// Keycloak rejects (or that is known to be expired) gives an error matching ErrSessionExpired.
func GetValidToken(ctx context.Context, domain string, auth config.AuthConfig) (*oauth2.Token, error) {
	if auth.AccessToken == "" {
		return nil, fmt.Errorf("no access token")
	}

	// Use the cached token while it has more than the refresh margin left.
	expiry, err := time.Parse(time.RFC3339, auth.Expiry)
	if err == nil && time.Until(expiry) > refreshMargin(auth.AccessToken) {
		return &oauth2.Token{
			AccessToken:  auth.AccessToken,
			RefreshToken: auth.RefreshToken,
			Expiry:       expiry,
		}, nil
	}

	// A refresh token that is missing or known to be expired cannot be redeemed: skip the request.
	if auth.RefreshToken == "" {
		return nil, &SessionExpiredError{Cause: fmt.Errorf("no refresh token stored")}
	}
	// Keycloak has the last word on a token that expired only moments ago: this host's clock may
	// run ahead of it, so only a token that is clearly past its exp is declared dead locally.
	if exp, ok := refreshTokenExpiry(auth.RefreshToken); ok && time.Since(exp) > refreshExpirySkew {
		return nil, &SessionExpiredError{Cause: fmt.Errorf("refresh token expired at %s", exp.UTC().Format(time.RFC3339))}
	}

	clientID := auth.ClientID
	if clientID == "" {
		clientID = config.ClientID
	}

	conf := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			TokenURL:  tokenURL(domain, auth.Realm),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	token := &oauth2.Token{
		RefreshToken: auth.RefreshToken,
	}

	newToken, err := conf.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, mapRefreshError(err)
	}

	return newToken, nil
}
