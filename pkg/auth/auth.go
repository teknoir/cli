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
	if exp, ok := refreshTokenExpiry(auth.RefreshToken); ok && !exp.After(time.Now()) {
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
