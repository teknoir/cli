package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// discoveryTimeout bounds the discovery request: it only decides an optional scope, so a slow or
// failing realm must never hold up a login.
const discoveryTimeout = 5 * time.Second

// issuerOfTokenURL is the realm issuer URL of a token endpoint URL.
func issuerOfTokenURL(u string) string {
	return strings.TrimSuffix(u, "/protocol/openid-connect/token")
}

// RealmOffersHubScope reports whether the realm at issuerURL lists the scope teknoir-ztna in
// scopes_supported of its discovery document. A failed or unreadable discovery means no: tnctl
// then sends exactly the requests it always sent (production and airgapped realms lack the scope).
func RealmOffersHubScope(ctx context.Context, issuerURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuerURL, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var doc struct {
		ScopesSupported []string `json:"scopes_supported"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxTokenIO)).Decode(&doc) != nil {
		return false
	}
	return slices.Contains(doc.ScopesSupported, ztnaScope)
}

// LoginScopes is the scope list for a device login at issuerURL: the usual four, plus teknoir-ztna
// when the realm offers it.
func LoginScopes(ctx context.Context, issuerURL string) []string {
	scopes := strings.Fields(mainScope)
	if RealmOffersHubScope(ctx, issuerURL) {
		scopes = append(scopes, ztnaScope)
	}
	return scopes
}

// HubScopeMissingMessage is shown when the realm lists the scope but refuses it for the client.
const HubScopeMissingMessage = "The login service lists the scope teknoir-ztna but does not allow it for this client (invalid_scope). Ask a platform admin to add teknoir-ztna to the optional scopes of the teknoir-cli client."
