# Teknoir CLI Authentication Guide

This document explains the proper authentication flow for the `tnctl` CLI and provides a step-by-step guide to configuring your infrastructure (Keycloak, OAuth2-Proxy, and Istio) to support it.

## The Problem

When a user visits a protected endpoint (e.g., `https://teknoir.cloud/api/...`) in a browser, `oauth2-proxy` (or an equivalent session manager) intercepts the request. If the user doesn't have a valid session cookie, `oauth2-proxy` redirects them to Keycloak to log in, returning a 302 redirect to an HTML login page.

When the CLI makes the same request, it includes an `Authorization: Bearer <token>` header instead of a session cookie. By default, `oauth2-proxy` or the Istio `ext_authz` filter ignores this header, assuming the user is unauthenticated, and attempts to redirect the CLI to Keycloak. This results in the CLI receiving an HTML response instead of the expected JSON data, causing the `invalid character '<' looking for beginning of value` error.

## Proper CLI Auth Flow (OAuth2 / OIDC)

For a CLI, the standard flow is **OAuth2 Authorization Code with PKCE** (which is already implemented in `tnctl login`).
1. **CLI** opens a browser for the user to log in.
2. **Keycloak** issues an Access Token (JWT) and a Refresh Token to the CLI.
3. **CLI** attaches the Access Token as an `Authorization: Bearer <token>` header to API requests.
4. **Infrastructure** validates the JWT token and allows the request through to the backend service *without* requiring a cookie.

## Architecture Options

There are three main approaches to enabling programmatic (Bearer token) access in an environment protected by session-based authentication.

### Option 1: Configure OAuth2-Proxy to Accept JWTs (Shared Domain)
If you want to keep everything on `teknoir.cloud` and you use `oauth2-proxy`, you can configure it to validate JWTs in addition to cookies.

### Option 2: Use Istio to Bypass OAuth2-Proxy for JWTs (Recommended for Istio Environments)
You can configure Istio's native `AuthorizationPolicy` to validate the JWT directly using `RequestAuthentication` and skip the `ext_authz` (OAuth2-Proxy) check entirely for requests with a valid Bearer token.

### Option 3: Separate API Domain (e.g., `api.teknoir.cloud`)
Create a separate domain strictly for programmatic access. This domain does not use `oauth2-proxy` at all. It relies solely on Istio's native `RequestAuthentication` and `AuthorizationPolicy` to validate the Keycloak JWT.

---

## Step-by-Step Implementation Guide

### Phase 1: Keycloak Configuration

Ensure your Keycloak client is configured correctly for the CLI.

1. **Client ID**: Ensure the `tnctl` (or your chosen CLI client ID) exists.
2. **Access Type**: `public` (since a CLI cannot securely store a static client secret) OR `confidential` if you distribute a secret with the CLI.
3. **Valid Redirect URIs**: `http://127.0.0.1:*` (The CLI spins up a local server on a random port for the callback).
4. **PKCE**: Enabled (Proof Key for Code Exchange is standard for native/CLI apps).
5. **Audience (Optional but recommended)**: Ensure the token issued contains an audience claim that your API expects. You may need to create a Client Scope with an Audience mapper and assign it to the CLI client.

---

### Phase 2: Infrastructure Configuration

Choose **ONE** of the following options based on your preference. Option 2 is usually the cleanest if you use Istio heavily.

#### Option 1: Update OAuth2-Proxy

If all traffic flows through `oauth2-proxy`, configure it to accept Bearer tokens.

Add the following flags to your `oauth2-proxy` deployment arguments:

```yaml
args:
  # Other args...
  - --skip-jwt-bearer-tokens=true
  - --extra-jwt-issuers=https://auth.teknoir.cloud/auth/realms/master=teknoir-cli
```
*Note: Replace `master` with your realm and `audience-name` with the expected audience (if configured, otherwise omit `=audience-name`). `oauth2-proxy` will validate the token against Keycloak's JWKS endpoint.*

#### Option 2: Istio Native Validation (Shared Domain)

Instead of passing the token to `oauth2-proxy`, let Istio validate it and skip `oauth2-proxy` for API traffic.

**Step A. Create a `RequestAuthentication`:**
This tells Istio how to validate the JWT from Keycloak.

```yaml
apiVersion: security.istio.io/v1beta1
kind: RequestAuthentication
metadata:
  name: jwt-auth
  namespace: istio-system # or your gateway namespace
spec:
  selector:
    matchLabels:
      app: istio-ingressgateway # Update to match your gateway labels
  jwtRules:
  - issuer: "https://auth.teknoir.cloud/auth/realms/master"
    jwksUri: "https://auth.teknoir.cloud/auth/realms/master/protocol/openid-connect/certs"
    forwardOriginalToken: true
```

**Step B. Update your `AuthorizationPolicy` to bypass `ext_authz`:**

Assuming you have an `AuthorizationPolicy` that sends traffic to your `ext_authz` (OAuth2-Proxy), you can exclude requests that have a valid JWT.

```yaml
apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: require-ext-authz
  namespace: istio-system
spec:
  selector:
    matchLabels:
      app: istio-ingressgateway
  action: CUSTOM
  provider:
    name: oauth2-proxy
  rules:
  - to:
    - operation:
        paths: ["/*"]
    # DO NOT send to oauth2-proxy if the request has a valid JWT
    when:
    - key: request.auth.claims[iss]
      notValues: ["https://auth.teknoir.cloud/auth/realms/master"]
```
*This ensures that if `request.auth.claims[iss]` is populated (meaning the JWT was successfully validated by the `RequestAuthentication` resource), Istio will NOT trigger the `oauth2-proxy` check.*

**Step C. Add an `AuthorizationPolicy` to explicitly allow the JWT:**

```yaml
apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: allow-jwt
  namespace: istio-system
spec:
  selector:
    matchLabels:
      app: istio-ingressgateway
  action: ALLOW
  rules:
  - from:
    - source:
        requestPrincipals: ["https://auth.teknoir.cloud/auth/realms/master/*"]
```

#### Option 3: Dedicated API Domain (`api.teknoir.cloud`)

If you prefer to completely separate browser UI traffic from programmatic traffic:

1. **DNS**: Create a DNS record for `api.teknoir.cloud`.
2. **Routing**: Create an Istio `VirtualService` for `api.teknoir.cloud` that routes directly to your backend services (e.g., Backstage catalog).
3. **No OAuth2-Proxy**: DO NOT attach the `oauth2-proxy` `EnvoyFilter` or `ext_authz` `AuthorizationPolicy` to this VirtualService/Gateway.
4. **Secure with JWT**: Secure it exclusively using the `RequestAuthentication` and `AuthorizationPolicy` (ALLOW action with `requestPrincipals`) shown in Option 2.
5. **CLI Update**: In your CLI configuration or flags, use `--domain api.teknoir.cloud` for operations, and ensure authentication is still configured to use `auth.teknoir.cloud`.

## Conclusion

To solve the HTML redirect issue, your infrastructure must recognize the `Authorization: Bearer` header. Implementing **Option 2** (Istio RequestAuthentication bypassing the ext_authz provider) provides the cleanest, most performant solution without requiring a separate domain.
