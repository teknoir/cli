package api

import (
	"context"
	"fmt"
	"net/url"

	"github.com/teknoir/cli/pkg/auth"
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
		// Annotations include ztna.teknoir.org/services (JSON list of service specs).
		Annotations map[string]string `json:"annotations,omitempty"`
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
	accessToken, err := auth.AccessToken(ctx, domain)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/api/catalog/entities/by-refs", domain)
	payload := map[string][]string{
		"entityRefs": {fmt.Sprintf("resource:%s/%s", namespace, deviceName)},
	}

	var resp backstageResponse
	if err := RequestWithBody(ctx, "POST", url, accessToken, payload, &resp); err != nil {
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
	accessToken, err := auth.AccessToken(ctx, domain)
	if err != nil {
		return nil, err
	}

	filterStr := fmt.Sprintf("kind=resource,spec.type=device,metadata.namespace=%s", namespace)
	for k, v := range labels {
		filterStr += fmt.Sprintf(",metadata.labels.%s=%s", k, v)
	}

	reqURL := fmt.Sprintf("https://%s/api/catalog/entities?filter=%s&order=asc%%3Ametadata.name", domain, url.QueryEscape(filterStr))

	var items []BackstageDevice
	if err := Request(ctx, "GET", reqURL, accessToken, &items); err != nil {
		return nil, err
	}

	return items, nil
}
