# Keycloak Configuration for teknoir-cli

To enable authentication for the `tnctl` CLI, you must configure a client in your Keycloak realm.

## Client Settings

1. **Client ID**: `teknoir-cli`
2. **Client Authentication**: `Off` (for public clients)
3. **Standard Flow**: `Off`
4. **Direct Access Grants**: `On`
5. **OAuth 2.0 Device Authorization Grant**: `On`

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
  * Default

## Optional client scope `teknoir-ztna` (WireGuard tunneling)

The WireGuard hub (`wg.<domain>`) accepts only tokens whose audience contains `teknoir-ztna`, so the
token `tnctl` sends to Backstage and the fleet API cannot open tunnel sessions. Create the scope and
add it to the client as an **optional** scope (never default: a default scope cannot be left out of a
token):

* Client scope `teknoir-ztna`, Type: Optional, Include in token scope: ON, Display on consent screen: OFF
* Mapper "By configuration", type Audience, Name `audience-teknoir-ztna`,
  Included Custom Audience `teknoir-ztna`, Add to access token: ON, Add to ID token: OFF
* Client `teknoir-cli` -> Client Scopes -> Add client scope -> `teknoir-ztna` -> Optional
  (the client keeps `offline_access` as optional too)

How `tnctl` uses it:

* `tnctl login` adds the scope to the device login only when the realm lists it in `scopes_supported`
  (the discovery document). Realms without it (production, airgapped installs) get the same request as
  before. If the realm lists it but the client does not have it (`invalid_scope`, e.g. after the client
  was reverted to `offline_access` only), `tnctl` warns and runs the login once more without the scope
  (you confirm a second code); WireGuard access then needs a hub that does not enforce the audience.
  A hub refresh that gets `invalid_scope` is retried downscoped once, with one warning per process.
* The access token that `tnctl` caches and sends to Backstage is always renewed **without** the scope
  (a downscoped refresh), so it has no `teknoir-ztna` audience.
* For every hub call (session create and each renewal) `tnctl` redeems the refresh token again **with**
  the scope and sends that fresh token only to `wg.<domain>`. It is never cached; only the rotated
  refresh token is saved.
* A login made before the scope existed cannot gain it on refresh (Keycloak leaves it out silently):
  run `tnctl login` once more. `tnctl login --check` shows `Tunneling: relogin` for such a login.