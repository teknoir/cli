package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/teknoir/wg-tunnel/pkg/api"
	"github.com/teknoir/wg-tunnel/pkg/knownhosts"
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
