package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"text/template"

	"teknoir/cli/pkg/api"
	"teknoir/cli/pkg/config"
	"teknoir/cli/pkg/templates"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap [device-name]",
	Short: "Generate bootstrap scripts for a device",
	RunE: func(cmd *cobra.Command, args []string) error {
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
		} else if cmd.Flags().Changed("device") {
			deviceName = cfg.GetDevice()
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

		data := templates.TemplateData{
			Domain:       domain,
			Namespace:    namespace,
			DeviceID:     deviceName,
			RSAPrivate:   rsaPrivate,
			RSAPublic:    rsaPublic,
			Username:     username,
			Password:     password,
			PublicSSHKey: publicSSHKey,
			K3SToken:     k3sToken,
			DockerSecret: dockerSecret,
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
	bootstrapCmd.Flags().String("docker-secret", "", "The contents of the .dockerconfigjson")
	rootCmd.AddCommand(bootstrapCmd)
}
