package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"text/template"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
	"github.com/teknoir/cli/pkg/templates"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap [device-name]",
	Short: "Generate bootstrap scripts for a device",
	RunE: func(cmd *cobra.Command, args []string) error {
		k3sVersion, _ := cmd.Flags().GetString("k3s-version")
		if err := templates.ValidateK3SVersion(k3sVersion); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		domain := cfg.Domain
		namespace := cfg.GetNamespace()

		if domain == "" || namespace == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}

		var deviceName string
		if len(args) > 0 {
			deviceName = args[0]
		} else if device != "" {
			deviceName = device
		}

		if deviceName == "" {
			devices, err := api.FetchDevices(cmd.Context(), domain, namespace)
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

		device, err := api.FetchDeviceDetails(cmd.Context(), domain, namespace, deviceName)
		if err != nil {
			return err
		}

		decode := func(name, s string) (string, error) {
			if s == "" {
				return "", nil
			}
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return "", fmt.Errorf("failed to decode %s: %w", name, err)
			}
			return string(b), nil
		}

		rsaPrivate, err := decode("rsa_private", device.Spec.Settings.RSAPrivate)
		if err != nil {
			return err
		}
		rsaPublic, err := decode("rsa_public", device.Spec.Settings.RSAPublic)
		if err != nil {
			return err
		}
		username, err := decode("username", device.Spec.Settings.Username)
		if err != nil {
			return err
		}
		password, err := decode("userpassword", device.Spec.Settings.Password)
		if err != nil {
			return err
		}
		publicSSHKey, err := decode("publicsshkey", device.Spec.Settings.PublicSSHKey)
		if err != nil {
			return err
		}

		k3sToken, err := generateK3SToken()
		if err != nil {
			return fmt.Errorf("failed to generate K3S token: %w", err)
		}

		dockerSecret, _ := cmd.Flags().GetString("docker-secret")
		k3sUpgrade, _ := cmd.Flags().GetBool("k3s-upgrade")
		force, _ := cmd.Flags().GetBool("force")
		if force && !k3sUpgrade {
			return fmt.Errorf("--force only applies together with --k3s-upgrade")
		}

		data := templates.TemplateData{
			Domain:             domain,
			Namespace:          namespace,
			DeviceID:           deviceName,
			RSAPrivate:         rsaPrivate,
			RSAPublic:          rsaPublic,
			Username:           username,
			Password:           password,
			PublicSSHKey:       publicSSHKey,
			K3SToken:           k3sToken,
			DockerSecret:       dockerSecret,
			K3SVersion:         k3sVersion,
			K3SVersionExplicit: cmd.Flags().Changed("k3s-version"),
			K3SUpgrade:         k3sUpgrade,
			K3SForce:           force,
		}

		generate := func(tmpl *template.Template, filename string) error {
			f, err := os.Create(filename)
			if err != nil {
				return err
			}
			defer f.Close()
			return tmpl.Execute(f, data)
		}

		agentFile := fmt.Sprintf("bootstrap_agent_%s.sh", deviceName)
		serverFile := fmt.Sprintf("bootstrap_server_%s.sh", deviceName)
		dockerServerFile := fmt.Sprintf("bootstrap_docker_server_%s.sh", deviceName)

		if err := generate(templates.AgentTemplate, agentFile); err != nil {
			return err
		}
		if err := generate(templates.ServerTemplate, serverFile); err != nil {
			return err
		}
		if err := generate(templates.DockerServerTemplate, dockerServerFile); err != nil {
			return err
		}

		fmt.Printf("Successfully generated bootstrap scripts:\n")
		fmt.Printf("- %s\n", agentFile)
		fmt.Printf("- %s\n", serverFile)
		fmt.Printf("- %s\n", dockerServerFile)

		skipUpload, _ := cmd.Flags().GetBool("skip-upload")
		if !skipUpload {
			fmt.Println("TODO: Implement script upload to secure bucket")
		}

		return nil
	},
}

func generateK3SToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func init() {
	bootstrapCmd.Flags().Bool("skip-upload", false, "Skip uploading the generated scripts")
	bootstrapCmd.Flags().String("k3s-version", templates.DefaultK3SVersion,
		"k3s release the scripts install (INSTALL_K3S_VERSION): an exact release like v1.36.5+k3s1, or a channel (stable, latest, testing, v1.34, passed as INSTALL_K3S_CHANNEL). Server and agent scripts use the same value")
	bootstrapCmd.Flags().Bool("k3s-upgrade", false,
		"let the scripts replace an installed k3s with --k3s-version (TN_K3S_UPGRADE=true is the default in the scripts). Without it a re-run keeps the installed k3s; a downgrade is always refused")
	bootstrapCmd.Flags().Bool("force", false,
		"with --k3s-upgrade: allow an upgrade over more than one minor version (TN_K3S_FORCE=true is the default in the scripts)")
	bootstrapCmd.Flags().String("docker-secret", "", "The contents of the .dockerconfigjson")
	rootCmd.AddCommand(bootstrapCmd)
}
