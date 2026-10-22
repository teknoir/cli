package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

var (
	realm          string
	clientID       string
	noBrowser      bool
	loginCheckOnly bool
)

// loginCmd represents the login command
var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to the Teknoir platform",
	Long: `Authenticates the user with the Teknoir platform using the Device Authorization Flow.
This command will attempt to open your default browser to complete the login sequence.
If it cannot open the browser, it will provide a URL and a code for you to enter manually.`,
	RunE: runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&realm, "realm", "master", "Keycloak realm")
	loginCmd.Flags().StringVar(&clientID, "client-id", config.ClientID, "Keycloak client ID")
	loginCmd.Flags().BoolVar(&loginCheckOnly, "check", false, "Do not log in: check that the stored login works (exit 0), or exit 6 when it has expired; prints the user and expiry, never a token")
	loginCmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not try to open a browser; just print the login URL and code (the default over SSH or without a display)")
}

func runLogin(cmd *cobra.Command, args []string) error {
	if loginCheckOnly {
		return runLoginCheck(cmd)
	}
	ctx := cmd.Context()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	domain := cfg.Domain
	authDomain := "auth." + domain

	// Teknoir's Keycloak instance is hosted on the /auth subpath
	issuerURL := fmt.Sprintf("https://%s/auth/realms/%s", authDomain, realm)

	if config.Debug {
		fmt.Printf("DEBUG: Issuer URL: %s\n", issuerURL)
		fmt.Printf("DEBUG: Client ID: %s\n", clientID)
	}

	conf := &oauth2.Config{
		ClientID: clientID,
		// teknoir-ztna (the audience of the WireGuard hub) only when the realm offers it, so
		// production and airgapped realms get exactly the request they always got.
		Scopes: auth.LoginScopes(ctx, issuerURL),
		Endpoint: oauth2.Endpoint{
			AuthURL:       issuerURL + "/protocol/openid-connect/auth",
			DeviceAuthURL: issuerURL + "/protocol/openid-connect/auth/device",
			TokenURL:      issuerURL + "/protocol/openid-connect/token",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}

	token, err := loginWithFallback(ctx, conf, domain)
	if err != nil {
		return err
	}

	if w := missingRefreshTokenWarning(token, clientID); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}

	// The stored access token is the main token (Backstage, fleet API): never the hub-audience one
	// the device flow returns when it was granted the hub scope.
	token, err = auth.MainLoginToken(ctx, domain, realm, clientID, token)
	if err != nil {
		return err
	}

	storeLogin(cfg, domain, token, realm, clientID)

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save tokens to config: %w", err)
	}

	fmt.Println("Successfully logged in! Tokens saved to configuration.")
	return nil
}

// loginWithFallback runs the device flow. When the realm lists teknoir-ztna but the client lost it
// (a rolled-back realm change: invalid_scope), it runs the flow once more without the scope, with a
// warning. The device code is spent, so the user confirms a new one. The login then works for
// everything but the hub audience (a hub in enforce mode refuses it).
func loginWithFallback(ctx context.Context, conf *oauth2.Config, domain string) (*oauth2.Token, error) {
	token, err := deviceLogin(ctx, conf, domain)
	var scopeErr *hubScopeMissingError
	if errors.As(err, &scopeErr) && dropZTNAScope(conf) {
		fmt.Fprintln(os.Stderr, "Warning: "+auth.HubScopeMissingMessage+" Logging in again without it; WireGuard access (ssh, exec, port-forward) needs the scope on a hub that enforces the audience.")
		return deviceLogin(ctx, conf, domain)
	}
	return token, err
}

// deviceLogin runs one device authorization flow with conf and returns the token.
func deviceLogin(ctx context.Context, conf *oauth2.Config, domain string) (*oauth2.Token, error) {
	verifier := oauth2.GenerateVerifier()

	fmt.Printf("Starting device authorization for %s...\n", domain)
	device, err := conf.DeviceAuth(
		ctx,
		oauth2.S256ChallengeOption(verifier),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start device authorization: %w", loginError(err))
	}

	// Always print the URL and the code first: opening a browser is only a convenience,
	// and on a headless host or over SSH there is none (xdg-open may even "succeed"
	// without showing anything), so the user must never depend on it.
	loginURL := device.VerificationURIComplete
	if loginURL == "" {
		loginURL = device.VerificationURI
	}
	fmt.Printf("To log in, open this URL in a browser (on any machine):\n\n  %s\n\n", loginURL)
	fmt.Printf("and confirm the code:\n\n  %s\n\n", device.UserCode)
	if noBrowser || !canOpenBrowser() {
		fmt.Println("(Not opening a browser here: no display, SSH session or --no-browser.)")
	} else if err := openBrowser(loginURL); err != nil {
		fmt.Printf("(Could not open a browser automatically: %v)\n", err)
	}

	fmt.Println("Waiting for authorization...")
	token, err := conf.DeviceAccessToken(
		ctx,
		device,
		oauth2.VerifierOption(verifier),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to complete device authorization: %w", loginError(err))
	}
	return token, nil
}

// hubScopeMissingError is the invalid_scope answer to a login that asked for teknoir-ztna: the
// realm lists the scope but the client lacks it. Its text is the one clear line for the user.
type hubScopeMissingError struct{ cause error }

func (e *hubScopeMissingError) Error() string { return auth.HubScopeMissingMessage }
func (e *hubScopeMissingError) Unwrap() error { return e.cause }

// loginError replaces an invalid_scope answer (the realm lists the hub scope but the client lacks
// it) by one clear line; every other error is returned as it is.
func loginError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorCode == "invalid_scope" {
		return &hubScopeMissingError{cause: err}
	}
	return err
}

// dropZTNAScope removes teknoir-ztna from conf.Scopes and reports whether it was there.
func dropZTNAScope(conf *oauth2.Config) bool {
	kept := conf.Scopes[:0:0]
	for _, sc := range conf.Scopes {
		if sc != auth.ZTNAScope {
			kept = append(kept, sc)
		}
	}
	dropped := len(kept) != len(conf.Scopes)
	conf.Scopes = kept
	return dropped
}

// storeLogin records a completed login in cfg (the caller saves it). The refresh token is replaced
// even when the response has none: the old one belongs to the previous login and is most likely
// dead, so keeping it would only produce a confusing invalid_grant later.
func storeLogin(cfg *config.Config, domain string, token *oauth2.Token, realm, clientID string) {
	auth := cfg.Auths[config.SanitizeDomain(domain)]
	auth.AccessToken = token.AccessToken
	auth.RefreshToken = token.RefreshToken
	auth.Expiry = token.Expiry.Format(time.RFC3339)
	auth.Realm = realm
	auth.ClientID = clientID
	cfg.Auths[config.SanitizeDomain(domain)] = auth
}

// canOpenBrowser reports whether this session can show a browser: never over SSH, and on
// Linux/BSD only with an X11 or Wayland display.
func canOpenBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	default:
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default: // linux, freebsd, etc.
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
