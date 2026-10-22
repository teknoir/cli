package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

	// Use the cached token while it has more than the refresh margin left, unless it carries the
	// hub audience: the main token (fleet API, Backstage) must never have it, so such a token (the
	// one a login stores) is replaced by a downscoped refresh.
	expiry, err := time.Parse(time.RFC3339, auth.Expiry)
	if err == nil && time.Until(expiry) > refreshMargin(auth.AccessToken) && !hasAudience(auth.AccessToken, ztnaAudience) {
		return &oauth2.Token{
			AccessToken:  auth.AccessToken,
			RefreshToken: auth.RefreshToken,
			Expiry:       expiry,
		}, nil
	}

	if err := usableRefreshToken(auth.RefreshToken); err != nil {
		return nil, err
	}

	// A refresh token that was granted the hub scope is redeemed downscoped, so this token never
	// has the hub audience. A login without it (production, airgapped, old logins) sends no scope,
	// exactly as before.
	scope := ""
	if hasScope(auth.RefreshToken, ztnaScope) {
		scope, _ = scopesFor(auth.RefreshToken)
	}
	return redeemRefreshToken(ctx, domain, auth, scope)
}

// MainLoginToken turns the token a device login returned into the main token to store. The login
// asks for the hub scope so that one browser confirmation yields a refresh token that may mint hub
// tokens, which also puts the hub audience into that first access token. The stored access token is
// the one Backstage and the fleet API see and must never have it, so such a token is replaced at
// once by a downscoped redemption of the refresh token (the rotated refresh token comes with it).
// A token without the hub audience, or without a refresh token to redeem, is returned as it is.
func MainLoginToken(ctx context.Context, domain, realm, clientID string, token *oauth2.Token) (*oauth2.Token, error) {
	if token == nil || token.RefreshToken == "" || !hasAudience(token.AccessToken, ztnaAudience) {
		return token, nil
	}
	mainSc, _ := scopesFor(token.RefreshToken)
	main, err := redeemRefreshToken(ctx, domain, config.AuthConfig{Realm: realm, ClientID: clientID, RefreshToken: token.RefreshToken}, mainSc)
	if err != nil {
		return nil, fmt.Errorf("could not obtain the main token after login: %w", err)
	}
	if hasAudience(main.AccessToken, ztnaAudience) {
		return nil, fmt.Errorf("the realm still puts the %s audience into the main token (scope %q); not storing it", ztnaAudience, mainSc)
	}
	return main, nil
}

// usableRefreshToken is a SessionExpiredError for a refresh token that is missing or known to be
// expired (it cannot be redeemed: skip the request), nil otherwise. Keycloak has the last word on a
// token that expired only moments ago: this host's clock may run ahead of it, so only a token that
// is clearly past its exp is declared dead locally.
func usableRefreshToken(refreshToken string) error {
	if refreshToken == "" {
		return &SessionExpiredError{Cause: fmt.Errorf("no refresh token stored")}
	}
	if exp, ok := refreshTokenExpiry(refreshToken); ok && time.Since(exp) > refreshExpirySkew {
		return &SessionExpiredError{Cause: fmt.Errorf("refresh token expired at %s", exp.UTC().Format(time.RFC3339))}
	}
	return nil
}

// Scopes and audience of the two tokens one login yields (see token.go).
const (
	// ztnaScope is the optional Keycloak client scope that adds the audience ztnaAudience.
	ztnaScope = "teknoir-ztna"
	// ZTNAScope is ztnaScope for callers outside the package (the login command).
	ZTNAScope = ztnaScope
	// ztnaAudience is the audience the WireGuard hub requires (the aud of a hub token).
	ztnaAudience = "teknoir-ztna"
	// mainScope is what a login requests for the main token: no hub audience.
	mainScope  = "openid profile email offline_access"
	maxTokenIO = 1 << 20
)

// scopesFor derives the scopes to redeem a refresh token with from the scope claim of that token
// itself: Keycloak answers invalid_scope to a refresh that asks for more than the token was
// granted, so a hard-coded list breaks a login that lacks one of them (a user without the
// offline_access role). main is the claim without the hub scope, hub is the claim with it. A token
// without a scope claim yields mainScope and mainScope plus the hub scope.
func scopesFor(refreshToken string) (main, hub string) {
	var keep []string
	for _, sc := range strings.Fields(jwtClaims(refreshToken).Scope) {
		if sc != ztnaScope {
			keep = append(keep, sc)
		}
	}
	if len(keep) == 0 {
		keep = strings.Fields(mainScope)
	}
	main = strings.Join(keep, " ")
	return main, main + " " + ztnaScope
}

// redeemRefreshToken redeems the refresh token at the realm's token endpoint. scope is sent only
// when not empty (a refresh without scope returns the originally granted scopes). Errors from the
// token endpoint are *oauth2.RetrieveError, so mapRefreshError applies. The refresh token of the
// answer is the rotated one, or the old one when the answer carries none.
func redeemRefreshToken(ctx context.Context, domain string, auth config.AuthConfig, scope string) (*oauth2.Token, error) {
	clientID := auth.ClientID
	if clientID == "" {
		clientID = config.ClientID
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {auth.RefreshToken},
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL(domain, auth.Realm), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, mapRefreshError(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := http.DefaultClient
	if c, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && c != nil {
		hc = c
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, mapRefreshError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenIO))
	if err != nil {
		return nil, mapRefreshError(fmt.Errorf("reading the token response: %w", err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		re := &oauth2.RetrieveError{Response: resp, Body: body}
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			URI         string `json:"error_uri"`
		}
		if json.Unmarshal(body, &e) == nil {
			re.ErrorCode, re.ErrorDescription, re.ErrorURI = e.Error, e.Description, e.URI
		}
		return nil, mapRefreshError(re)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, mapRefreshError(fmt.Errorf("decoding the token response: %w", err))
	}
	if tr.AccessToken == "" {
		return nil, mapRefreshError(fmt.Errorf("server response missing access_token"))
	}
	tok := &oauth2.Token{AccessToken: tr.AccessToken, TokenType: tr.TokenType, RefreshToken: tr.RefreshToken}
	if tok.RefreshToken == "" {
		tok.RefreshToken = auth.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tok, nil
}
