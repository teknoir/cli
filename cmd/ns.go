package cmd

import (
	"context"
	"fmt"
	"time"

	"teknoir/cli/pkg/api"
	authPkg "teknoir/cli/pkg/auth"
	"teknoir/cli/pkg/config"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var nsCmd = &cobra.Command{
	Use:   "ns [namespace]",
	Short: "Select active namespace",
	RunE: func(cmd *cobra.Command, args []string) error {
		domain := viper.GetString("domain")

		if len(args) > 0 {
			viper.Set("namespace", args[0])
			return viper.WriteConfig()
		}

		// Fetch real namespaces
		namespaces, err := fetchNamespaces(cmd.Context(), domain)
		if err != nil {
			return err
		}

		if len(namespaces) == 0 {
			return fmt.Errorf("no namespaces found")
		}

		idx, err := fuzzyfinder.Find(namespaces, func(i int) string {
			return namespaces[i]
		})
		if err != nil {
			return err
		}

		selected := namespaces[idx]
		viper.Set("namespace", selected)
		if err := viper.WriteConfig(); err != nil {
			return err
		}
		fmt.Printf("Switched to namespace: %s\n", selected)
		return nil
	},
}

type APIResponse struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"items"`
}

func fetchNamespaces(ctx context.Context, domain string) ([]string, error) {
	// Load config to get access token
	var cfg config.Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	auth, exists := cfg.Auths[config.SanitizeDomain(domain)]
	if !exists || auth.AccessToken == "" {
		return nil, fmt.Errorf("no credentials found for domain %s. Please log in first", domain)
	}

	token, err := authPkg.GetValidToken(ctx, domain, auth)
	if err != nil {
		return nil, err
	}

	// Update config with new token if refreshed
	if token.AccessToken != auth.AccessToken || token.RefreshToken != auth.RefreshToken {
		auth.AccessToken = token.AccessToken
		auth.RefreshToken = token.RefreshToken
		auth.Expiry = token.Expiry.Format(time.RFC3339)
		cfg.Auths[config.SanitizeDomain(domain)] = auth
		viper.Set("auths", cfg.Auths)
		if err := viper.WriteConfig(); err != nil {
			return nil, fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	url := fmt.Sprintf("https://%s/api/catalog/entities/by-query?fields=metadata.name&filter=kind%%3Dsystem%%2Cmetadata.annotations.backstage.io%%2Fmanaged-by-location%%3Dprofile-provider%%3Ak8s", domain)

	var apiResp APIResponse
	if err := api.Request(ctx, "GET", url, token.AccessToken, &apiResp); err != nil {
		return nil, err
	}

	var namespaces []string
	for _, item := range apiResp.Items {
		namespaces = append(namespaces, item.Metadata.Name)
	}

	return namespaces, nil
}

func init() {
	rootCmd.AddCommand(nsCmd)
}
