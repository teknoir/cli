package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// idp is a fake realm: a token endpoint that records every form and answers like Keycloak (the
// audience follows the requested scope, the refresh token rotates and keeps the granted scope), and
// a discovery document.
type idp struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	forms      []url.Values
	n          int
	refuseZTNA bool   // token endpoint answers invalid_scope to a request with teknoir-ztna (a rolled-back realm)
	status     int    // token endpoint status (0: 200)
	errBody    string // token endpoint body for a non-200 status
	scopes     string // scopes_supported of the discovery document ("" none)
	discHits   int
	discCode   int // discovery status (0: 200)
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{t: t}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/realms/master/.well-known/openid-configuration":
			p.discHits++
			if p.discCode != 0 {
				w.WriteHeader(p.discCode)
				return
			}
			_, _ = fmt.Fprintf(w, `{"issuer":"x","scopes_supported":[%s]}`, p.scopes)
		case "/realms/master/protocol/openid-connect/token":
			_ = r.ParseForm()
			p.forms = append(p.forms, r.PostForm)
			if p.refuseZTNA && strings.Contains(r.PostForm.Get("scope"), "teknoir-ztna") {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_scope","error_description":"Invalid scopes: teknoir-ztna"}`))
				return
			}
			if p.status != 0 {
				w.WriteHeader(p.status)
				_, _ = w.Write([]byte(p.errBody))
				return
			}
			p.n++
			aud := `["teknoir-cli","account"]`
			if strings.Contains(r.PostForm.Get("scope"), "teknoir-ztna") {
				aud = `["teknoir-ztna","teknoir-cli","account"]`
			}
			now := time.Now()
			access := jwtRaw(t, fmt.Sprintf(`{"aud":%s,"iat":%d,"exp":%d,"jti":"at-%d","email":"alice@example.com"}`, aud, now.Unix(), now.Add(5*time.Minute).Unix(), p.n))
			refresh := jwtRaw(t, fmt.Sprintf(`{"typ":"Offline","scope":%q,"jti":"rt-%d"}`, grantedByTest(r.PostForm.Get("refresh_token")), p.n))
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":300}`, access, refresh)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	old := tokenURL
	tokenURL = func(string, string) string { return p.srv.URL + "/realms/master/protocol/openid-connect/token" }
	t.Cleanup(func() { tokenURL = old })
	return p
}

func (p *idp) issuer() string { return p.srv.URL + "/realms/master" }

func (p *idp) tokenForms() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.forms...)
}

// grantedByTest returns the scope claim of the presented refresh token, as Keycloak keeps it.
func grantedByTest(refreshToken string) string { return jwtClaims(refreshToken).Scope }

func jwtRaw(t *testing.T, payload string) string {
	t.Helper()
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payload)) + "." + enc([]byte("sig"))
}

// store is an in-memory config behind tokenDeps; saves counts the writes.
type store struct {
	mu    sync.Mutex
	cfg   *config.Config
	saves int
}

func newStore(a config.AuthConfig) *store {
	return &store{cfg: cfgWith("teknoir.cloud", a)}
}

func (s *store) deps() tokenDeps {
	return tokenDeps{
		load: func() (*config.Config, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			c := *s.cfg
			c.Auths = map[string]config.AuthConfig{}
			for k, v := range s.cfg.Auths {
				c.Auths[k] = v
			}
			return &c, nil
		},
		refresh: GetValidToken,
		save: func(c *config.Config) error {
			s.mu.Lock()
			s.cfg, s.saves = c, s.saves+1
			s.mu.Unlock()
			return nil
		},
	}
}

func (s *store) auth() config.AuthConfig { return s.cfg.Auths[config.SanitizeDomain("teknoir.cloud")] }

const fullScope = "openid profile email offline_access teknoir-ztna"

// zLogin is what a login made with the hub scope stores: an access token that HAS the hub audience
// (still valid) and an offline refresh token granted the scope.
func zLogin(t *testing.T) config.AuthConfig {
	now := time.Now()
	return config.AuthConfig{
		AccessToken:  jwtRaw(t, fmt.Sprintf(`{"aud":["teknoir-ztna","teknoir-cli"],"iat":%d,"exp":%d,"jti":"login"}`, now.Unix(), now.Add(5*time.Minute).Unix())),
		RefreshToken: jwtRaw(t, fmt.Sprintf(`{"typ":"Offline","scope":%q,"jti":"rt-login"}`, fullScope)),
		Realm:        "master",
		Expiry:       now.Add(5 * time.Minute).Format(time.RFC3339),
	}
}

// oldLogin is a login without the hub scope (production, airgapped, made before the scope existed).
func oldLogin(t *testing.T) config.AuthConfig {
	now := time.Now()
	return config.AuthConfig{
		AccessToken:  jwtRaw(t, fmt.Sprintf(`{"aud":["teknoir-cli"],"iat":%d,"exp":%d}`, now.Unix(), now.Add(-time.Minute).Unix())),
		RefreshToken: jwtRaw(t, `{"typ":"Offline","scope":"openid profile email offline_access"}`),
		Realm:        "master",
		Expiry:       now.Add(-time.Minute).Format(time.RFC3339),
	}
}

func TestMainTokenIsDownscopedAndTheZTNATokenFromLoginIsNotReused(t *testing.T) {
	p := newIDP(t)
	s := newStore(zLogin(t))
	loginAT := s.auth().AccessToken
	tok, err := accessToken(context.Background(), "teknoir.cloud", s.deps())
	if err != nil {
		t.Fatal(err)
	}
	forms := p.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("%d token requests; the cached ztna token must be replaced by a refresh", len(forms))
	}
	if got := forms[0].Get("scope"); got != "openid profile email offline_access" {
		t.Fatalf("main token scope %q", got)
	}
	if forms[0].Get("grant_type") != "refresh_token" || forms[0].Get("client_id") == "" || forms[0].Get("refresh_token") == "" {
		t.Fatalf("form %v", forms[0])
	}
	if tok == loginAT || hasAudience(tok, "teknoir-ztna") || !hasAudience(tok, "teknoir-cli") {
		t.Fatalf("main token aud wrong: %v", jwtClaims(tok).Aud)
	}
	if got := s.auth().AccessToken; got != tok {
		t.Fatal("the downscoped token was not saved")
	}
	// A second call uses the (now downscoped) cached token without a request.
	if _, err := accessToken(context.Background(), "teknoir.cloud", s.deps()); err != nil || len(p.tokenForms()) != 1 {
		t.Fatalf("err=%v, %d requests", err, len(p.tokenForms()))
	}
	// A forced refresh (FreshAccessToken, Check) is downscoped too.
	if _, err := accessToken(context.Background(), "teknoir.cloud", forceRefresh(s.deps())); err != nil {
		t.Fatal(err)
	}
	if f := p.tokenForms(); len(f) != 2 || f[1].Get("scope") != "openid profile email offline_access" {
		t.Fatalf("forms %v", f)
	}
}

func TestHubTokenCarriesTheScopeAndLeavesTheMainTokenAlone(t *testing.T) {
	p := newIDP(t)
	a := zLogin(t)
	a.AccessToken = jwtRaw(t, fmt.Sprintf(`{"aud":["teknoir-cli"],"iat":%d,"exp":%d,"jti":"main"}`, time.Now().Unix(), time.Now().Add(5*time.Minute).Unix()))
	s := newStore(a)
	before := s.auth()
	for i := 1; i <= 2; i++ { // never cached: every call is a request
		tok, ztna, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps())
		if err != nil {
			t.Fatal(err)
		}
		if !ztna || !hasAudience(tok, "teknoir-ztna") || !hasAudience(tok, "teknoir-cli") {
			t.Fatalf("hub token aud %v, ztna=%v", jwtClaims(tok).Aud, ztna)
		}
		forms := p.tokenForms()
		if len(forms) != i || forms[i-1].Get("scope") != fullScope {
			t.Fatalf("request %d: %v", i, forms)
		}
		got := s.auth()
		if got.AccessToken != before.AccessToken || got.Expiry != before.Expiry {
			t.Fatal("the stored access token or its expiry was replaced by the hub token")
		}
		if got.RefreshToken == before.RefreshToken || got.RefreshToken == "" {
			t.Fatal("the rotated refresh token was not saved")
		}
		before.RefreshToken = got.RefreshToken
	}
	if s.saves != 2 {
		t.Fatalf("%d saves", s.saves)
	}
	// The rotated refresh token keeps the granted scope, so the next downscoped main refresh works.
	if !hasScope(s.auth().RefreshToken, "teknoir-ztna") {
		t.Fatal("rotated refresh token lost the hub scope")
	}
}

func TestOldLoginSendsNoScopeParameter(t *testing.T) {
	p := newIDP(t)
	s := newStore(oldLogin(t))
	if _, err := accessToken(context.Background(), "teknoir.cloud", s.deps()); err != nil {
		t.Fatal(err)
	}
	tok, ztna, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps())
	if err != nil {
		t.Fatal(err)
	}
	if ztna || hasAudience(tok, "teknoir-ztna") {
		t.Fatal("an old login must not get the hub audience")
	}
	if _, err := accessToken(context.Background(), "teknoir.cloud", forceRefresh(s.deps())); err != nil {
		t.Fatal(err)
	}
	forms := p.tokenForms()
	if len(forms) != 3 {
		t.Fatalf("%d requests", len(forms))
	}
	for i, f := range forms {
		if _, has := f["scope"]; has {
			t.Fatalf("request %d sent a scope parameter: %v", i, f)
		}
	}
}

func TestRefreshTokenWithoutAnyScopeClaimSendsNoScope(t *testing.T) {
	p := newIDP(t)
	a := oldLogin(t)
	a.RefreshToken = "opaque-refresh-token" // not a JWT at all
	s := newStore(a)
	if _, _, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps()); err != nil {
		t.Fatal(err)
	}
	if f := p.tokenForms(); len(f) != 1 || f[0].Has("scope") {
		t.Fatalf("%v", f)
	}
}

func TestInvalidGrantStillMapsToSessionExpired(t *testing.T) {
	p := newIDP(t)
	p.status, p.errBody = 400, `{"error":"invalid_grant","error_description":"Token is not active"}`
	s := newStore(zLogin(t))
	if _, err := accessToken(context.Background(), "teknoir.cloud", s.deps()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("main: %v", err)
	}
	_, _, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps())
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("hub: %v", err)
	}
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) || re.ErrorCode != "invalid_grant" {
		t.Fatalf("the cause is not a RetrieveError: %v", err)
	}
	if s.saves != 0 {
		t.Fatal("config saved after a failed refresh")
	}
}

func TestInvalidScopeOnRefreshIsNotSessionExpiry(t *testing.T) {
	p := newIDP(t)
	p.status, p.errBody = 400, `{"error":"invalid_scope"}`
	s := newStore(zLogin(t))
	_, _, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps())
	if err == nil || errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
}

// A rolled-back realm change: teknoir-cli lost the optional scope but the stored refresh token still
// lists it. The hub refresh retries once downscoped, with one warning, and the login keeps working.
func TestHubTokenFallsBackWithoutTheScopeWhenTheRealmRefusesIt(t *testing.T) {
	p := newIDP(t)
	p.refuseZTNA = true
	var warned bytes.Buffer
	oldW := warnWriter
	warnWriter = &warned
	warnedHubScope.Store(false)
	t.Cleanup(func() { warnWriter = oldW; warnedHubScope.Store(false) })

	s := newStore(zLogin(t))
	for i := 0; i < 2; i++ { // two calls, like a create and a renewal: one warning
		tok, ztna, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps())
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if ztna || hasAudience(tok, ztnaAudience) {
			t.Fatalf("call %d: the fallback token has the hub audience", i)
		}
	}
	scopes := []string{}
	for _, f := range p.tokenForms() {
		scopes = append(scopes, f.Get("scope"))
	}
	want := []string{fullScope, mainScope, fullScope, mainScope}
	if fmt.Sprint(scopes) != fmt.Sprint(want) {
		t.Fatalf("scopes sent %q, want %q", scopes, want)
	}
	if n := strings.Count(warned.String(), "Warning:"); n != 1 || !strings.Contains(warned.String(), "teknoir-ztna") {
		t.Fatalf("warnings: %q", warned.String())
	}
	if s.saves == 0 {
		t.Fatal("the rotated refresh token was not saved")
	}
}

func TestMainTokenStillRefreshesWhenTheRealmRefusesTheHubScope(t *testing.T) {
	p := newIDP(t)
	p.refuseZTNA = true
	a := zLogin(t)
	a.Expiry = "" // force a refresh
	s := newStore(a)
	tok, err := accessToken(context.Background(), "teknoir.cloud", s.deps())
	if err != nil || hasAudience(tok, ztnaAudience) {
		t.Fatalf("main token: %v", err)
	}
	if f := p.tokenForms(); len(f) != 1 || f[0].Get("scope") != mainScope {
		t.Fatalf("forms: %v", f)
	}
}

func TestHubTokenNotLoggedInAndDeadRefreshToken(t *testing.T) {
	newIDP(t)
	s := &store{cfg: cfgWith("other.example", config.AuthConfig{AccessToken: "x"})}
	if _, _, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("got %v", err)
	}
	a := zLogin(t)
	a.RefreshToken = jwtRaw(t, fmt.Sprintf(`{"typ":"Refresh","exp":%d,"scope":%q}`, time.Now().Add(-time.Hour).Unix(), fullScope))
	s = newStore(a)
	if _, _, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestLoginScopesFollowDiscovery(t *testing.T) {
	p := newIDP(t)
	base := "openid profile email offline_access"
	for name, tc := range map[string]struct {
		scopes string
		code   int
		want   string
	}{
		"realm offers the scope":  {`"openid","teknoir-ztna","offline_access"`, 0, base + " teknoir-ztna"},
		"realm without the scope": {`"openid","profile","email","offline_access"`, 0, base},
		"no scopes_supported":     {``, 0, base},
		"similar scope only":      {`"teknoir-ztna-status"`, 0, base},
		"discovery fails (500)":   {`"teknoir-ztna"`, 500, base},
		"discovery not found":     {`"teknoir-ztna"`, 404, base},
	} {
		t.Run(name, func(t *testing.T) {
			p.mu.Lock()
			p.scopes, p.discCode = tc.scopes, tc.code
			p.mu.Unlock()
			if got := strings.Join(LoginScopes(context.Background(), p.issuer()), " "); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	// An unreachable realm adds nothing and does not hang.
	p.srv.Close()
	start := time.Now()
	if got := strings.Join(LoginScopes(context.Background(), p.issuer()), " "); got != base {
		t.Fatalf("got %q", got)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("discovery held the login up")
	}
}

func TestStatusTunneling(t *testing.T) {
	now := time.Now()
	valid := func(a config.AuthConfig) config.AuthConfig {
		a.AccessToken = jwtRaw(t, fmt.Sprintf(`{"aud":["teknoir-cli"],"iat":%d,"exp":%d,"preferred_username":"anders"}`, now.Unix(), now.Add(5*time.Minute).Unix()))
		a.Expiry = now.Add(5 * time.Minute).Format(time.RFC3339)
		return a
	}
	for name, tc := range map[string]struct {
		login  config.AuthConfig
		scopes string
		want   string
	}{
		"login has the scope":                    {valid(zLogin(t)), `"teknoir-ztna"`, TunnelingOK},
		"login has the scope, realm lost it":     {valid(zLogin(t)), ``, TunnelingOK},
		"realm offers it, login lacks it":        {valid(oldLogin(t)), `"teknoir-ztna"`, TunnelingRelogin},
		"realm does not offer it (prod, airgap)": {valid(oldLogin(t)), `"openid"`, TunnelingNA},
	} {
		t.Run(name, func(t *testing.T) {
			p := newIDP(t)
			p.scopes = tc.scopes
			s := newStore(tc.login)
			st, err := check(context.Background(), "teknoir.cloud", s.deps())
			if err != nil {
				t.Fatal(err)
			}
			if st.Tunneling != tc.want {
				t.Fatalf("tunneling %q, want %q", st.Tunneling, tc.want)
			}
		})
	}
}

func TestHasAudienceAndScopeParsing(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"aud":"teknoir-ztna"}`, true},
		{`{"aud":["a","teknoir-ztna"]}`, true},
		{`{"aud":["teknoir-ztna-status"]}`, false},
		{`{"aud":42}`, false},
		{`{"aud":null}`, false},
		{`{}`, false},
	} {
		if got := hasAudience(jwtRaw(t, tc.payload), "teknoir-ztna"); got != tc.want {
			t.Errorf("%s: got %v", tc.payload, got)
		}
	}
	if hasAudience("not-a-jwt", "teknoir-ztna") || hasScope("not-a-jwt", "teknoir-ztna") {
		t.Error("garbage matched")
	}
	if !hasScope(jwtRaw(t, `{"scope":"openid  teknoir-ztna"}`), "teknoir-ztna") || hasScope(jwtRaw(t, `{"scope":"teknoir-ztna-status"}`), "teknoir-ztna") {
		t.Error("scope parsing")
	}
}

// A login granted the hub scope but not offline_access (a user without that role): Keycloak answers
// invalid_scope to a refresh that asks for more than the refresh token holds, so both tokens are
// requested with the scopes of the refresh token itself.
func TestScopesFollowTheRefreshTokenWithoutOfflineAccess(t *testing.T) {
	const granted = "openid profile email teknoir-ztna"
	login := func() config.AuthConfig {
		a := zLogin(t)
		a.RefreshToken = jwtRaw(t, fmt.Sprintf(`{"typ":"Refresh","scope":%q,"jti":"rt-login"}`, granted))
		return a
	}
	p := newIDP(t)
	if _, err := accessToken(context.Background(), "teknoir.cloud", forceRefresh(newStore(login()).deps())); err != nil {
		t.Fatal(err)
	}
	s := newStore(login())
	if _, ztna, err := hubAccessToken(context.Background(), "teknoir.cloud", s.deps()); err != nil || !ztna {
		t.Fatalf("hub: ztna=%v err=%v", ztna, err)
	}
	forms := p.tokenForms()
	if len(forms) != 2 || forms[0].Get("scope") != "openid profile email" || forms[1].Get("scope") != granted {
		t.Fatalf("scopes sent: %v", forms)
	}

	// The realm-refused fallback also stays within the granted scopes.
	p.refuseZTNA = true
	warnedHubScope.Store(true) // keep the test output quiet
	t.Cleanup(func() { warnedHubScope.Store(false) })
	if _, ztna, err := hubAccessToken(context.Background(), "teknoir.cloud", newStore(login()).deps()); err != nil || ztna {
		t.Fatalf("fallback: ztna=%v err=%v", ztna, err)
	}
	forms = p.tokenForms()
	if got := forms[len(forms)-1].Get("scope"); got != "openid profile email" {
		t.Fatalf("fallback scope %q", got)
	}
}

func TestScopesForTokenWithoutScopeClaim(t *testing.T) {
	m, h := scopesFor("opaque")
	if m != mainScope || h != mainScope+" teknoir-ztna" {
		t.Fatalf("%q %q", m, h)
	}
}
