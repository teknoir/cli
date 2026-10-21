package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/wg-tunnel/pkg/api"
	"github.com/teknoir/wg-tunnel/pkg/knownhosts"
	"github.com/teknoir/wg-tunnel/pkg/wgclient"
)

const (
	envWGTurnServer = "TNCTL_WG_TURN_SERVER"
	envWGHub        = "TNCTL_WG_HUB"
)

// nameRE is a DNS-style name (namespace, device).
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// resolveWGOption returns the first non-empty of: the flag value, the
// environment variable, the default.
func resolveWGOption(flagValue, envName, def string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	return def
}

// defaultHubURL is where the hub serves the user session API.
func defaultHubURL(domain string) string {
	return "https://" + api.UserHostPrefix + "." + domain
}

// validateHubURL requires https (the bearer token travels in the request)
// except for a loopback host, which is for local tests.
func validateHubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("the hub URL is not a valid URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the hub URL must not contain credentials, a query or a fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("the hub URL must use https (plain http is only allowed for localhost)")
	}
	return errors.New("the hub URL must use https")
}

// validateWGTarget checks the (namespace, device, port) triple.
func validateWGTarget(namespace, device string, port int) error {
	switch {
	case !nameRE.MatchString(namespace):
		return errors.New("--namespace is required and must be a DNS-style name")
	case !nameRE.MatchString(device):
		return errors.New("--device is required and must be a DNS-style name")
	case port < 1 || port > 65535:
		return errors.New("--port must be between 1 and 65535")
	}
	if err := knownhosts.ValidAlias(api.KnownHostsAlias(namespace, device)); err != nil {
		return fmt.Errorf("namespace/device: %w", err)
	}
	return nil
}

// validateHubHost refuses a hub outside the logged-in domain: the Keycloak
// access token for the domain must not be sent to an arbitrary host (a
// poisoned TNCTL_WG_HUB or a pasted command line). Loopback is allowed for tests.
func validateHubHost(raw, domain string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("the hub URL is not a valid URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	d := strings.ToLower(strings.Trim(domain, "."))
	if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
		return nil
	}
	return fmt.Errorf("the hub host %q is outside the logged-in domain %q; refusing to send your login token there", host, domain)
}

// newWGCredential returns the wgclient credential func: the token is read from
// ~/.tnctl.yaml (and refreshed) on every call, never from argv or the
// environment. Session creation may use the cached token; every renewal must
// present a newly issued one (the hub refuses a token it has already
// accepted), so renewals force a refresh.
func newWGCredential(domain string) func(ctx context.Context, wgPublicKey string) (string, error) {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context, _ string) (string, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		var tok string
		var err error
		if first {
			tok, err = auth.AccessToken(ctx, domain)
		} else {
			tok, err = auth.FreshAccessToken(ctx, domain)
		}
		if errors.Is(err, auth.ErrSessionExpired) {
			return "", &wgLoginExpired{err}
		}
		return tok, err
	}
}

// wgLoginExpired is the credential error of a dead login. The wgclient session retries a failing
// credential until the hub session runs out, which would leave an unusable tunnel open for
// minutes. It therefore presents itself as a definitive hub denial (errors.As to *wgclient.Error:
// 401 unauthenticated), which ends the session at once; errors.Is still matches
// auth.ErrSessionExpired, and describeWGError prints the login message.
type wgLoginExpired struct{ err error }

func (e *wgLoginExpired) Error() string { return e.err.Error() }
func (e *wgLoginExpired) Unwrap() error { return e.err }

func (e *wgLoginExpired) As(target any) bool {
	if t, ok := target.(**wgclient.Error); ok {
		*t = &wgclient.Error{Status: http.StatusUnauthorized, Reason: api.ReasonUnauthenticated}
		return true
	}
	return false
}

// wgPosture describes this tnctl to the hub's posture check.
func wgPosture() api.Posture {
	host, _ := os.Hostname()
	return api.Posture{Software: "tnctl", Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host}
}
