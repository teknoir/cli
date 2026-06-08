# Keycloak Configuration for teknoir-cli

To enable authentication for the `tnctl` CLI, you must configure a client in your Keycloak realm.

## Client Settings

1. **Client ID**: `teknoir-cli`
2. **Client Authentication**: `Off` (for public clients)
3. **Standard Flow Enabled**: `ON` (Required for the authorization code flow)

Add the following to **Valid Redirect URIs**:
- `http://127.0.0.1:*`

## Advanced Settings

1. **Proof Key for Code Exchange (PKCE) Challenge Method**: `S256` (Recommended)

## Client Scopes
Then go to Client scopes menu:
* Add (or create) a scope: teknoir-cli
* Type: Default

Configure a new mapper for the scope:
* "By configuration"
* Mapper type: Audience
* Name: teknoir-cli
* Included Client Audience: teknoir-cli
* Add to access token: ON

Add Client Scope to the client:
* Client: teknoir-cli
* Client Scopes -> Add client scope
  * teknoir-cli