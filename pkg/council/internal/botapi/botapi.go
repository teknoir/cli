// Package botapi holds the rules that the bot API clients of the council share with pkg/api: which
// base URLs may carry a TeknoirBot's key, and how that key is read.
package botapi

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// ErrKey is what Key returns when the bot's key file is missing or empty: this computer predates
// the agent, and only a person can re-create the bot. The error names the file, never its content.
var ErrKey = errors.New("the bot key is missing or empty")

// ErrInsecure is what Allowed refuses: the bot's key must not travel in clear text, so only https,
// or http on a loopback address, may carry it.
var ErrInsecure = errors.New("the bot API base must be https (or http on a loopback address)")

// TrimBase is an API base without surrounding space and without a trailing slash.
func TrimBase(apiBase string) string {
	return strings.TrimRight(strings.TrimSpace(apiBase), "/")
}

// Allowed reports whether apiBase may carry the bot's key: https anywhere, http only on a loopback
// address.
func Allowed(apiBase string) bool {
	u, err := url.Parse(TrimBase(apiBase))
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			return ip.IsLoopback()
		}
		return strings.EqualFold(host, "localhost")
	}
	return false
}

// Key is the content of the bot's key file, trimmed. The file is read on every request so a
// replaced key is picked up without a restart; its path may be shown, its content never.
func Key(keyFile string) (string, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("%w (%s): this computer predates the agent; re-create the bot", ErrKey, keyFile)
	}
	return strings.TrimSpace(string(b)), nil
}
