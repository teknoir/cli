package api

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"teknoir/cli/pkg/auth"
	"teknoir/cli/pkg/config"
)

type DeviceAlert struct {
	Count int    `json:"count"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type AppStatus struct {
	CpuUsage        float64 `json:"cpu_usage"`
	CpuUsagePct     float64 `json:"cpu_usage_pct"`
	MemoryUsage     float64 `json:"memory_usage"`
	MemoryUsagePct  float64 `json:"memory_usage_pct"`
	ReadyContainers int     `json:"ready_containers"`
	Running         bool    `json:"running"`
	TotalContainers int     `json:"total_containers"`
}

type DeviceState struct {
	Alerts       []DeviceAlert        `json:"alerts"`
	Apps         map[string]AppStatus `json:"apps"`
	CpuUsage     float64              `json:"cpu_usage"`
	MemoryUsage  float64              `json:"memory_usage"`
	NetRxRate    float64              `json:"net_rx_rate"`
	NetTxRate    float64              `json:"net_tx_rate"`
	StorageUsage float64              `json:"storage_usage"`
}

type DeviceConfig struct {
	ActiveConfigHash string `json:"active_config_hash"`
	LastConfigChange string `json:"last_config_change"`
}

type DeviceStatus struct {
	Config       DeviceConfig `json:"config"`
	RemoteAccess struct {
		Active bool   `json:"active"`
		Port   string `json:"port"`
	} `json:"remote_access"`
	State DeviceState `json:"state"`
}

type BackstageDevice struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
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
			Status DeviceStatus `json:"status"`
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
	devices, err := FetchDeviceDetailsList(ctx, domain, namespace, nil)
	if err != nil {
		return nil, err
	}
	var deviceNames []string
	for _, d := range devices {
		deviceNames = append(deviceNames, d.Metadata.Name)
	}
	return deviceNames, nil
}

func FetchDeviceDetailsList(ctx context.Context, domain, namespace string, labels map[string]string) ([]BackstageDevice, error) {
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

	filterStr := fmt.Sprintf("kind=resource,spec.type=device,metadata.namespace=%s", namespace)
	for k, v := range labels {
		filterStr += fmt.Sprintf(",metadata.labels.%s=%s", k, v)
	}

	reqURL := fmt.Sprintf("https://%s/api/catalog/entities?filter=%s&order=asc%%3Ametadata.name", domain, url.QueryEscape(filterStr))

	var items []BackstageDevice
	if err := Request(ctx, "GET", reqURL, token.AccessToken, &items); err != nil {
		return nil, err
	}

	return items, nil
}
