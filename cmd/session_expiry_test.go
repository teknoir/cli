package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/wg-tunnel/pkg/wgclient"
	"golang.org/x/oauth2"
)

var expiredErr = &auth.SessionExpiredError{Cause: errors.New(`oauth2: "invalid_grant" "Token is not active"`)}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + "." + enc([]byte("sig"))
}

func TestSessionExpiredExitCodeAndMessage(t *testing.T) {
	for name, err := range map[string]error{
		"typed":                  expiredErr,
		"sentinel":               auth.ErrSessionExpired,
		"wrapped":                fmt.Errorf("listing robots: %w", expiredErr),
		"wrapped twice":          fmt.Errorf("a: %w", fmt.Errorf("b: %w", expiredErr)),
		"credential denial type": &wgLoginExpired{expiredErr},
	} {
		if got := exitCodeOf(err); got != codeAuth {
			t.Errorf("%s: exit %d, want %d (the 'not logged in' code)", name, got, codeAuth)
		}
		var ce *cliError
		if !errors.As(classify(err), &ce) || ce.Error() != "Your tnctl login has expired. Run: tnctl login" {
			t.Errorf("%s: message %q", name, classify(err))
		}
		var b bytes.Buffer
		writeError(&b, err, "table")
		if b.String() != "Error: Your tnctl login has expired. Run: tnctl login\n" {
			t.Errorf("%s: text output %q", name, b.String())
		}
	}
	// the raw oauth2 text is not what the user sees
	var b bytes.Buffer
	writeError(&b, fmt.Errorf("failed: %w", expiredErr), "table")
	if strings.Contains(b.String(), "invalid_grant") {
		t.Errorf("raw error leaked: %q", b.String())
	}
}

func TestSessionExpiredJSON(t *testing.T) {
	var b bytes.Buffer
	writeError(&b, fmt.Errorf("x: %w", expiredErr), "json")
	var v struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, b.String())
	}
	if v.Error.Code != "session_expired" || v.Error.Message != auth.SessionExpiredMessage {
		t.Errorf("%+v", v)
	}
	// plain "not logged in" and a rejected token keep the code "auth"
	for _, err := range []error{auth.ErrNotLoggedIn, &api.Error{Status: 401}} {
		b.Reset()
		writeError(&b, err, "json")
		if !strings.Contains(b.String(), `"code":"auth"`) {
			t.Errorf("%v: %s", err, b.String())
		}
	}
}

func TestTransientFailEndsOnExpiredSessionButRetriesNetworkErrors(t *testing.T) {
	var tr transient
	if err := tr.fail(fmt.Errorf("poll: %w", expiredErr)); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("an expired session must end the wait at once, got %v", err)
	}
	// a refresh that failed on the network is retried (5 times), like any other poll
	netErr := fmt.Errorf("failed to refresh token: %w", errors.New("dial tcp: connection refused"))
	for i := 0; i < 5; i++ {
		if err := tr.fail(netErr); err != nil {
			t.Fatalf("attempt %d: network error must be transient, got %v", i+1, err)
		}
	}
	if err := tr.fail(netErr); err == nil {
		t.Fatal("the sixth failure must end the wait")
	}
}

// tokenEnv is testEnv whose fleet token function is fn.
func tokenEnv(t *testing.T, p *fakePlatform, fn func(context.Context) (string, error)) *cliEnv {
	e, _ := testEnv(t, p, nil)
	fleet, domain, ns, _ := e.fleet()
	fleet.Token = fn
	e.fleet = func() (*api.Fleet, string, string, error) { return fleet, domain, ns, nil }
	return e
}

// countedPolls serves the same view for every poll of the target and counts them.
func countedPolls(t *testing.T, p *fakePlatform, v *api.ConfigTargetView) *atomic.Int32 {
	var n atomic.Int32
	p.onFunc("GET", "/config/vision-station-01", func(rec) (int, string) {
		n.Add(1)
		return 200, jsonOf(t, v)
	})
	return &n
}

func TestWaitEndsWhenTheSessionExpires(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	polls := countedPolls(t, p, viewAt("r1", "active", ""))
	// the offline session is revoked once the first poll has been answered
	e := tokenEnv(t, p, func(context.Context) (string, error) {
		if polls.Load() >= 1 {
			return "", fmt.Errorf("get token: %w", expiredErr)
		}
		return "tok", nil
	})
	_, errs, err := setWait(t, e, "--wait=published")
	if err == nil {
		t.Fatalf("the wait must fail\n%s", errs)
	}
	if exitCodeOf(err) != codeAuth || classify(err).Error() != auth.SessionExpiredMessage {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if n := polls.Load(); n != 1 {
		t.Errorf("the wait kept polling after the session expired: %d polls", n)
	}
	if strings.Contains(errs, "retrying") {
		t.Errorf("an expired session must not be retried:\n%s", errs)
	}
}

func TestWaitRetriesWhenTheRefreshFailsOnTheNetwork(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	polls := countedPolls(t, p, viewAt("r1", "active", ""))
	var failed atomic.Int32
	e := tokenEnv(t, p, func(context.Context) (string, error) {
		// after the first poll, the next two token refreshes fail on the network, then it works again
		if polls.Load() >= 1 && failed.Add(1) <= 2 {
			return "", fmt.Errorf("failed to refresh token: %w", errors.New("dial tcp: i/o timeout"))
		}
		return "tok", nil
	})
	_, errs, err := setWait(t, e, "--wait=published", "--timeout=30s")
	// the view never reaches r2, so the wait runs into its timeout: what matters is that it got
	// past the network errors instead of ending with them
	if exitCodeOf(err) != codeTimeout {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if !strings.Contains(errs, "retrying after failed to refresh token") || polls.Load() < 3 {
		t.Errorf("%d polls\n%s", polls.Load(), errs)
	}
}

func TestDescribeWGErrorSessionExpired(t *testing.T) {
	o := &wgProxyOptions{domain: "teknoir.cloud", namespace: "ns1", device: "dev-1"}
	for name, err := range map[string]error{
		"plain":      expiredErr,
		"at create":  fmt.Errorf("wgclient: obtaining credential: %w", expiredErr),
		"credential": &wgLoginExpired{expiredErr},
		"renewal":    fmt.Errorf("obtaining credential: %w", &wgLoginExpired{expiredErr}),
	} {
		if got := describeWGError(err, o); got != auth.SessionExpiredMessage {
			t.Errorf("%s: %q", name, got)
		}
	}
	// the hub session running out is a different message than the login running out
	got := describeWGError(fmt.Errorf("%w: renew failed", wgclient.ErrExpired), o)
	if strings.Contains(got, "tnctl login") || !strings.Contains(got, "hub could not be reached") {
		t.Errorf("hub expiry: %q", got)
	}
}

func TestWGLoginExpiredEndsTheSessionAtOnce(t *testing.T) {
	// wgclient retries a failing renewal until the hub session expires, unless the error is a
	// definitive denial: the dead login presents itself as one.
	err := fmt.Errorf("obtaining credential: %w", &wgLoginExpired{expiredErr})
	var he *wgclient.Error
	if !errors.As(err, &he) || !he.Denial() {
		t.Fatalf("not a denial: %+v", he)
	}
	if !errors.Is(err, auth.ErrSessionExpired) {
		t.Error("must still match ErrSessionExpired")
	}
	var other *wgclient.Error
	if errors.As(&wgLoginExpired{errors.New("x")}, &other) && other == nil {
		t.Error("As must not hand out a nil error")
	}
}

func TestRunWGProxyExpiredLogin(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	cred := func(context.Context, string) (string, error) { return "", &wgLoginExpired{expiredErr} }
	var stdout, stderr bytes.Buffer
	code := runWGProxy(context.Background(), goodOpts(srv.URL, t.TempDir()), cred, nil, strings.NewReader(""), &stdout, &stderr)
	if code != wgExitFail || stderr.String() != "tnctl: wireguard: Your tnctl login has expired. Run: tnctl login\n" || stdout.Len() != 0 {
		t.Errorf("code %d, stderr %q", code, stderr.String())
	}
	if calls != 0 {
		t.Error("the hub must not be called without a credential")
	}
}

func TestNewWGCredentialMapsExpiredLogin(t *testing.T) {
	useConfig(t, "teknoir.cloud", map[string]any{
		"access_token": "at", "refresh_token": testJWT(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(-time.Hour).Unix()}),
		"expiry": time.Now().Add(-time.Minute).Format(time.RFC3339), "realm": "master",
	})
	cred := newWGCredential("teknoir.cloud")
	for i := 0; i < 2; i++ { // session creation, then a renewal
		_, err := cred(context.Background(), "")
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("call %d: %v", i+1, err)
		}
		var he *wgclient.Error
		if !errors.As(err, &he) || !he.Denial() {
			t.Errorf("call %d: must end the session", i+1)
		}
	}
}

func TestMissingRefreshTokenWarning(t *testing.T) {
	tok := &oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(5 * time.Minute)}
	w := missingRefreshTokenWarning(tok, "teknoir-cli")
	if !strings.Contains(w, "no refresh token") || !strings.Contains(w, "offline_access") || !strings.Contains(w, "teknoir-cli") {
		t.Errorf("%q", w)
	}
	tok.RefreshToken = "r"
	if w := missingRefreshTokenWarning(tok, "teknoir-cli"); w != "" {
		t.Errorf("warned although there is a refresh token: %q", w)
	}
}

// useConfig points viper at a temporary config file with one login.
func useConfig(t *testing.T, domain string, a map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tnctl.yaml")
	b, _ := json.Marshal(map[string]any{"domain": domain, "auths": map[string]any{strings.ReplaceAll(domain, ".", "_"): a}})
	if err := os.WriteFile(path, b, 0o600); err != nil { // JSON is YAML
		t.Fatal(err)
	}
	// never the real ~/.tnctl.yaml: Execute runs initConfig, which reads cfgFile (else $HOME)
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	cfgFile = path
	viper.Reset()
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cfgFile = oldCfg; viper.Reset() })
	return path
}

func runLoginCheckCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	loginCheckOnly = false
	t.Cleanup(func() { loginCheckOnly = false })
	e, _ := testEnv(t, newFakePlatform(t), nil)
	return runCLIWith(t, e, []*cobra.Command{loginCmd}, append([]string{"login"}, args...)...)
}

func TestLoginCheckValidLogin(t *testing.T) {
	now := time.Now()
	at := testJWT(t, map[string]any{"preferred_username": "anders", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})
	rt := testJWT(t, map[string]any{"typ": "Offline"})
	useConfig(t, "teknoir.cloud", map[string]any{"access_token": at, "refresh_token": rt, "expiry": now.Add(5 * time.Minute).Format(time.RFC3339), "realm": "master"})

	out, errs, err := runLoginCheckCLI(t, "--check")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "teknoir.cloud") || !strings.Contains(out, "anders") || !strings.Contains(out, "offline session") {
		t.Errorf("stdout:\n%s", out)
	}
	if strings.Contains(out+errs, at) || strings.Contains(out+errs, rt) {
		t.Error("a token was printed")
	}

	out, _, err = runLoginCheckCLI(t, "--check", "-o", "json")
	outputFlag = ""
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Domain, User                  string
		Offline, Refreshed            bool
		AccessExpires, RefreshExpires any
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.User != "anders" || st.Domain != "teknoir.cloud" || !st.Offline || st.Refreshed || st.RefreshExpires != nil {
		t.Errorf("%v: %s", err, out)
	}
	if strings.Contains(out, at) || strings.Contains(out, rt) {
		t.Error("a token was printed")
	}
}

func TestLoginCheckExpiredLoginExits6(t *testing.T) {
	rt := testJWT(t, map[string]any{"typ": "Refresh", "exp": time.Now().Add(-time.Hour).Unix()})
	useConfig(t, "teknoir.cloud", map[string]any{"access_token": "at", "refresh_token": rt, "expiry": time.Now().Add(-time.Minute).Format(time.RFC3339), "realm": "master"})
	_, _, err := runLoginCheckCLI(t, "--check")
	if err == nil || exitCodeOf(err) != codeAuth || classify(err).Error() != auth.SessionExpiredMessage {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestLoginCheckNotLoggedInExits6(t *testing.T) {
	useConfig(t, "teknoir.cloud", map[string]any{})
	_, _, err := runLoginCheckCLI(t, "--check")
	if exitCodeOf(err) != codeAuth || !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
}
