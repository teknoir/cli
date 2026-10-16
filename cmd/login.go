package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

var (
	realm     string
	clientID  string
	noBrowser bool
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
	loginCmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not try to open a browser; just print the login URL and code (the default over SSH or without a display)")
}

func runLogin(cmd *cobra.Command, args []string) error {
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
		Scopes: []string{
			"openid",
			"profile",
			"email",
			"offline_access",
		},
		Endpoint: oauth2.Endpoint{
			AuthURL:       issuerURL + "/protocol/openid-connect/auth",
			DeviceAuthURL: issuerURL + "/protocol/openid-connect/auth/device",
			TokenURL:      issuerURL + "/protocol/openid-connect/token",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}

	verifier := oauth2.GenerateVerifier()

	fmt.Printf("Starting device authorization for %s...\n", domain)
	device, err := conf.DeviceAuth(
		ctx,
		oauth2.S256ChallengeOption(verifier),
	)
	if err != nil {
		return fmt.Errorf("failed to start device authorization: %w", err)
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
		return fmt.Errorf("failed to complete device authorization: %w", err)
	}

	// Save tokens
	auth := cfg.Auths[config.SanitizeDomain(domain)]
	auth.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		auth.RefreshToken = token.RefreshToken
	}
	auth.Expiry = token.Expiry.Format(time.RFC3339)
	auth.Realm = realm
	auth.ClientID = clientID
	cfg.Auths[config.SanitizeDomain(domain)] = auth

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save tokens to config: %w", err)
	}

	fmt.Println("Successfully logged in! Tokens saved to configuration.")
	return nil
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
