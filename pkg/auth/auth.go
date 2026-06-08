package auth

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"teknoir/cli/pkg/config"
)

// GetValidToken returns a valid access token, refreshing it if necessary.
func GetValidToken(ctx context.Context, domain string, auth config.AuthConfig) (*oauth2.Token, error) {
	if auth.AccessToken == "" {
		return nil, fmt.Errorf("no access token")
	}

	// Check if token is expired
	expiry, err := time.Parse(time.RFC3339, auth.Expiry)
	if err == nil && time.Until(expiry) > 1*time.Minute {
		return &oauth2.Token{
			AccessToken:  auth.AccessToken,
			RefreshToken: auth.RefreshToken,
			Expiry:       expiry,
		}, nil
	}

	// Refresh token
	authDomain := "auth." + domain
	issuerURL := fmt.Sprintf("https://%s/auth/realms/%s", authDomain, auth.Realm)
	tokenURL := issuerURL + "/protocol/openid-connect/token"

	clientID := auth.ClientID
	if clientID == "" {
		clientID = config.ClientID
	}

	conf := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	token := &oauth2.Token{
		RefreshToken: auth.RefreshToken,
	}

	newToken, err := conf.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	return newToken, nil
}
