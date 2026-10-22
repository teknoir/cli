package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/auth"
	"golang.org/x/oauth2"
)

// ztnaIDP is a fake realm for the two-token scheme: the token endpoint records the forms and puts
// the hub audience into a token only when the scope was requested; discovery lists the scope when
// offered is set.
type ztnaIDP struct {
	srv     *httptest.Server
	offered bool

	mu    sync.Mutex
	forms []url.Values
}

func newZTNAIDP(t *testing.T, offered bool) *ztnaIDP {
	t.Helper()
	p := &ztnaIDP{offered: offered}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			if p.offered {
				_, _ = w.Write([]byte(`{"scopes_supported":["openid","teknoir-ztna"]}`))
			} else {
				_, _ = w.Write([]byte(`{"scopes_supported":["openid","profile"]}`))
			}
			return
		}
		_ = r.ParseForm()
		p.mu.Lock()
		p.forms = append(p.forms, r.PostForm)
		n := len(p.forms)
		p.mu.Unlock()
		aud := []string{"teknoir-cli"}
		if strings.Contains(r.PostForm.Get("scope"), "teknoir-ztna") {
			aud = append(aud, "teknoir-ztna")
		}
		now := time.Now()
		at := testJWT(t, map[string]any{"aud": aud, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "jti": fmt.Sprint("at-", n), "preferred_username": "anders"})
		rt := testJWT(t, map[string]any{"typ": "Offline", "scope": scopeOfRefresh(r.PostForm.Get("refresh_token")), "jti": fmt.Sprint("rt-", n)})
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "refresh_token": rt, "token_type": "Bearer", "expires_in": 300})
	}))
	t.Cleanup(p.srv.Close)
	restore := auth.SetTokenURLForTest(func(string, string) string { return p.srv.URL + "/protocol/openid-connect/token" })
	t.Cleanup(restore)
	return p
}

func scopeOfRefresh(rt string) string {
	parts := strings.Split(rt, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := decodeSeg(parts[1])
	if err != nil {
		return ""
	}
	var c struct{ Scope string }
	_ = json.Unmarshal(b, &c)
	return c.Scope
}

func (p *ztnaIDP) scopes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.forms))
	for i, f := range p.forms {
		out[i] = f.Get("scope")
		if !f.Has("scope") {
			out[i] = "<none>"
		}
	}
	return out
}

func zScopedConfig(t *testing.T, scope string) map[string]any {
	now := time.Now()
	return map[string]any{
		// what a login with the hub scope stores: a (valid) access token with the hub audience
		"access_token":  testJWT(t, map[string]any{"aud": []string{"teknoir-cli", "teknoir-ztna"}, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}),
		"refresh_token": testJWT(t, map[string]any{"typ": "Offline", "scope": scope}),
		"expiry":        now.Add(5 * time.Minute).Format(time.RFC3339),
		"realm":         "master",
	}
}

const (
	zFull = "openid profile email offline_access teknoir-ztna"
	zMain = "openid profile email offline_access"
)

// Every call of the wgclient credential (session create and each renewal) mints a hub token with
// the hub scope; none of them is the cached main token, and the main token is never replaced.
func TestNewWGCredentialAlwaysMintsAHubToken(t *testing.T) {
	p := newZTNAIDP(t, true)
	cfg := zScopedConfig(t, zFull)
	mainAT := cfg["access_token"]
	path := useConfig(t, "teknoir.cloud", cfg)
	cred := newWGCredential("teknoir.cloud")
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		tok, err := cred(context.Background(), "wgkey")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if tok == mainAT || seen[tok] {
			t.Fatalf("call %d reused a token", i+1)
		}
		seen[tok] = true
		if !strings.Contains(tokenAud(t, tok), "teknoir-ztna") {
			t.Fatalf("call %d: hub token without the audience: %s", i+1, tokenAud(t, tok))
		}
	}
	got := p.scopes()
	if len(got) != 3 {
		t.Fatalf("%d token requests, want one per call: %v", len(got), got)
	}
	for _, s := range got {
		if s != zFull {
			t.Fatalf("scopes %v", got)
		}
	}
	// the stored access token is still the login's one (only the refresh token rotated)
	if b := readFile(t, path); !strings.Contains(b, fmt.Sprint(mainAT)) {
		t.Fatal("the stored main access token was replaced by a hub token")
	}
}

// An old login (production, airgapped) sends no scope parameter on the hub path.
func TestNewWGCredentialOldLoginSendsNoScope(t *testing.T) {
	p := newZTNAIDP(t, false)
	cfg := zScopedConfig(t, zMain)
	cfg["access_token"] = testJWT(t, map[string]any{"aud": []string{"teknoir-cli"}, "iat": time.Now().Unix(), "exp": time.Now().Add(-time.Minute).Unix()})
	cfg["expiry"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
	useConfig(t, "teknoir.cloud", cfg)
	cred := newWGCredential("teknoir.cloud")
	for i := 0; i < 2; i++ {
		tok, err := cred(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tokenAud(t, tok), "teknoir-ztna") {
			t.Fatal("old login got the hub audience")
		}
	}
	for _, s := range p.scopes() {
		if s != "<none>" {
			t.Fatalf("scope parameter sent: %v", p.scopes())
		}
	}
}

func TestLoginCheckTunnelingStates(t *testing.T) {
	for name, tc := range map[string]struct {
		offered   bool
		scope     string
		want      string
		wantLine  string
		forbidden string
	}{
		"ok":      {true, zFull, "ok", "Tunneling: ok", "once more"},
		"relogin": {true, zMain, "relogin", "run 'tnctl login' once more", ""},
		"n/a":     {false, zMain, "n/a", "Tunneling: n/a", "once more"},
	} {
		t.Run(name, func(t *testing.T) {
			newZTNAIDP(t, tc.offered)
			cfg := zScopedConfig(t, tc.scope)
			useConfig(t, "teknoir.cloud", cfg)
			out, errs, err := runLoginCheckCLI(t, "--check")
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if !strings.Contains(out, tc.wantLine) || (tc.forbidden != "" && strings.Contains(out, tc.forbidden)) {
				t.Errorf("stdout:\n%s", out)
			}
			out, _, err = runLoginCheckCLI(t, "--check", "-o", "json")
			outputFlag = ""
			if err != nil {
				t.Fatal(err)
			}
			var st struct{ Tunneling string }
			if err := json.Unmarshal([]byte(out), &st); err != nil || st.Tunneling != tc.want {
				t.Errorf("%v: %s", err, out)
			}
			if strings.Contains(out, "eyJ") {
				t.Error("a token was printed")
			}
		})
	}
}

func TestLoginErrorExplainsInvalidScope(t *testing.T) {
	err := loginError(&oauth2.RetrieveError{ErrorCode: "invalid_scope", ErrorDescription: "Invalid scopes: teknoir-ztna"})
	if err == nil || !strings.Contains(err.Error(), "teknoir-ztna") || !strings.Contains(err.Error(), "platform admin") || strings.Count(err.Error(), "\n") != 0 {
		t.Fatalf("%v", err)
	}
	other := &oauth2.RetrieveError{ErrorCode: "access_denied"}
	if got := loginError(other); !errors.Is(got, other) {
		t.Fatalf("another error was changed: %v", got)
	}
	if got := loginError(errors.New("boom")); got.Error() != "boom" {
		t.Fatal(got)
	}
}

func decodeSeg(s string) ([]byte, error) { return base64RawURL.DecodeString(strings.TrimRight(s, "=")) }

func tokenAud(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal("not a jwt")
	}
	b, err := decodeSeg(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Aud []string }
	_ = json.Unmarshal(b, &c)
	return strings.Join(c.Aud, ",")
}

var base64RawURL = base64.RawURLEncoding
