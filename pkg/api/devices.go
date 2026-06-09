package api

import (
	"context"
	"fmt"
	"time"

	"teknoir/cli/pkg/auth"
	"teknoir/cli/pkg/config"
)

type BackstageDevice struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Settings struct {
			Username     string `json:"username"`
			Password     string `json:"userpassword"`
			RSAPrivate   string `json:"rsa_private"`
			RSAPublic    string `json:"rsa_public"`
			PublicSSHKey string `json:"publicsshkey"`
		} `json:"settings"`
		Subresources struct {
			Status struct {
				RemoteAccess struct {
					Active bool   `json:"active"`
					Port   string `json:"port"`
				} `json:"remote_access"`
			} `json:"status"`
		} `json:"subresources"`
	} `json:"spec"`
}

type backstageResponse struct {
	Items []BackstageDevice `json:"items"`
}

func FetchDeviceDetails(ctx context.Context, domain, namespace, deviceName string) (*BackstageDevice, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	sanitizedDomain := config.SanitizeDomain(domain)
	authData, exists := cfg.Auths[sanitizedDomain]
	if !exists || authData.AccessToken == "" {
		return nil, fmt.Errorf("no credentials found for domain %s. Please log in first", domain)
	}

	token, err := auth.GetValidToken(ctx, domain, authData)
	if err != nil {
		return nil, err
	}

	// Update config with new token if refreshed
	if token.AccessToken != authData.AccessToken || token.RefreshToken != authData.RefreshToken {
		authData.AccessToken = token.AccessToken
		authData.RefreshToken = token.RefreshToken
		authData.Expiry = token.Expiry.Format(time.RFC3339)
		cfg.Auths[sanitizedDomain] = authData
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	url := fmt.Sprintf("https://%s/api/catalog/entities/by-refs", domain)
	payload := map[string][]string{
		"entityRefs": {fmt.Sprintf("resource:%s/%s", namespace, deviceName)},
	}

	var resp backstageResponse
	if err := RequestWithBody(ctx, "POST", url, token.AccessToken, payload, &resp); err != nil {
		return nil, err
	}

	if len(resp.Items) == 0 {
		return nil, fmt.Errorf("device %s/%s not found", namespace, deviceName)
	}

	return &resp.Items[0], nil
}

func FetchDevices(ctx context.Context, domain, namespace string) ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	sanitizedDomain := config.SanitizeDomain(domain)
	authData, exists := cfg.Auths[sanitizedDomain]
	if !exists || authData.AccessToken == "" {
		return nil, fmt.Errorf("no credentials found for domain %s. Please log in first", domain)
	}

	token, err := auth.GetValidToken(ctx, domain, authData)
	if err != nil {
		return nil, err
	}

	// Update config with new token if refreshed
	if token.AccessToken != authData.AccessToken || token.RefreshToken != authData.RefreshToken {
		authData.AccessToken = token.AccessToken
		authData.RefreshToken = token.RefreshToken
		authData.Expiry = token.Expiry.Format(time.RFC3339)
		cfg.Auths[sanitizedDomain] = authData
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	url := fmt.Sprintf("https://%s/api/catalog/entities?filter=kind%%3Dresource%%2Cspec.type%%3Ddevice%%2Cmetadata.namespace%%3D%s&order=asc%%3Ametadata.name", domain, namespace)

	var items []BackstageDevice
	if err := Request(ctx, "GET", url, token.AccessToken, &items); err != nil {
		return nil, err
	}

	var devices []string
	for _, item := range items {
		devices = append(devices, item.Metadata.Name)
	}

	return devices, nil
}
