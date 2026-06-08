package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"time"

	"teknoir/cli/pkg/api"
	authPkg "teknoir/cli/pkg/auth"
	"teknoir/cli/pkg/config"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var sshCmd = &cobra.Command{
	Use:   "ssh [device-name]",
	Short: "SSH into a device",
	RunE: func(cmd *cobra.Command, args []string) error {
		domain := viper.GetString("domain")
		namespace := viper.GetString("namespace")

		if domain == "" || namespace == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}

		var deviceName string
		if len(args) > 0 {
			deviceName = args[0]
		} else if cmd.Flags().Changed("device") {
			deviceName = viper.GetString("device")
		}

		if deviceName == "" {
			// Fetch and select device via fuzzy finder
			devices, err := fetchDevices(cmd.Context(), domain, namespace)
			if err != nil {
				return err
			}

			if len(devices) == 0 {
				return fmt.Errorf("no devices found in namespace %s", namespace)
			}

			idx, err := fuzzyfinder.Find(devices, func(i int) string {
				return devices[i]
			})
			if err != nil {
				return err
			}
			deviceName = devices[idx]
		}

		// Fetch device details from Backstage API
		device, err := fetchDeviceDetails(cmd.Context(), domain, namespace, deviceName)
		if err != nil {
			return err
		}

		if !device.Spec.Subresources.Status.RemoteAccess.Active {
			return fmt.Errorf("remote access is not enabled for device %s. Please enable it in the Teknoir Console", deviceName)
		}

		// Decode base64 username and private key
		usernameBytes, err := base64.StdEncoding.DecodeString(device.Spec.Settings.Username)
		if err != nil {
			return fmt.Errorf("failed to decode username: %w", err)
		}
		username := string(usernameBytes)

		privateKey, err := base64.StdEncoding.DecodeString(device.Spec.Settings.RSAPrivate)
		if err != nil {
			return fmt.Errorf("failed to decode RSA private key: %w", err)
		}

		// Create secure temp file for private key
		tmpFile, err := os.CreateTemp("", "tnctl-ssh-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if err := os.WriteFile(tmpFile.Name(), privateKey, 0600); err != nil {
			return fmt.Errorf("failed to write private key to temporary file: %w", err)
		}

		deadendHost := fmt.Sprintf("deadend-%s.%s", namespace, domain)
		remoteAccessPort := device.Spec.Subresources.Status.RemoteAccess.Port

		// Construct ProxyCommand as specified in the requirements
		// Replicating the exact proxy chaining structure found in legacy ssh_device.sh
		proxyCommand := fmt.Sprintf("ssh -o ProxyCommand='ncat --ssl %s 2222' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i %s -N -W %%h:%%p teknoir@%s -p 2222",
			deadendHost, tmpFile.Name(), deadendHost)

		// Final SSH execution command using os/exec
		sshExec := exec.Command("ssh",
			"-p", remoteAccessPort,
			"-i", tmpFile.Name(),
			"-o", "ProxyCommand="+proxyCommand,
			fmt.Sprintf("%s@127.0.0.1", username),
		)

		// Wire standard streams for interactive terminal session
		sshExec.Stdin = os.Stdin
		sshExec.Stdout = os.Stdout
		sshExec.Stderr = os.Stderr

		return sshExec.Run()
	},
}

type backstageDevice struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Settings struct {
			Username   string `json:"username"`
			RSAPrivate string `json:"rsa_private"`
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
	Items []backstageDevice `json:"items"`
}

func fetchDeviceDetails(ctx context.Context, domain, namespace, deviceName string) (*backstageDevice, error) {
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

	url := fmt.Sprintf("https://%s/api/catalog/entities/by-refs", domain)
	payload := map[string][]string{
		"entityRefs": {fmt.Sprintf("resource:%s/%s", namespace, deviceName)},
	}

	var resp backstageResponse
	if err := api.RequestWithBody(ctx, "POST", url, token.AccessToken, payload, &resp); err != nil {
		return nil, err
	}

	if len(resp.Items) == 0 {
		return nil, fmt.Errorf("device %s/%s not found", namespace, deviceName)
	}

	return &resp.Items[0], nil
}

func fetchDevices(ctx context.Context, domain, namespace string) ([]string, error) {
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

	url := fmt.Sprintf("https://%s/api/catalog/entities?filter=kind%%3Dresource%%2Cspec.type%%3Ddevice%%2Cmetadata.namespace%%3D%s&order=asc%%3Ametadata.name", domain, namespace)

	var items []backstageDevice
	if err := api.Request(ctx, "GET", url, token.AccessToken, &items); err != nil {
		return nil, err
	}

	var devices []string
	for _, item := range items {
		devices = append(devices, item.Metadata.Name)
	}

	return devices, nil
}

func init() {
	rootCmd.AddCommand(sshCmd)
}
