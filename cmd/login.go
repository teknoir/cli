package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	"teknoir/cli/pkg/config"
)

var (
	realm    string
	clientID string
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

	if viper.GetBool("debug") {
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

	if device.VerificationURIComplete != "" {
		fmt.Printf("Opening browser for login...\n")
		if err := openBrowser(device.VerificationURIComplete); err != nil {
			fmt.Printf("Could not open browser automatically: %v\n", err)
			fmt.Printf("Please open this URL manually:\n\n%s\n\n", device.VerificationURIComplete)
		}
	} else {
		fmt.Printf("Opening browser for login...\n")
		if err := openBrowser(device.VerificationURI); err != nil {
			fmt.Printf("Could not open browser automatically: %v\n", err)
			fmt.Printf("Please open this URL manually:\n\n%s\n\n", device.VerificationURI)
		}
		fmt.Printf("Enter this code:\n\n%s\n\n", device.UserCode)
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
