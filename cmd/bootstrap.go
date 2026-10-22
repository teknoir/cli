package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"text/template"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/templates"

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
		registryMirror, _ := cmd.Flags().GetString("registry-mirror")
		registryCredFile, _ := cmd.Flags().GetString("registry-credential-file")
		// Fail on a bad mirror or an unusable credential file before the platform is asked for anything.
		if err := checkRegistryFlags(registryMirror, registryCredFile); err != nil {
			return err
		}
		nodeOpts, err := nodeOptionsFromFlags(cmd)
		if err != nil {
			return err
		}
		nodes, err := nodeOpts.resolve()
		if err != nil {
			return err
		}
		fl, domain, namespace, err := defaultEnv.targetClient()
		if err != nil {
			return err
		}

		var deviceName string
		if len(args) > 0 {
			deviceName = args[0]
		} else if device != "" {
			deviceName = device
		}

		deviceName, err = pickDevice(cmd.Context(), fl, namespace, deviceName)
		if err != nil {
			return err
		}

		bl, err := loadBootstrapLogin(cmd.Context(), fl, namespace, deviceName)
		if err != nil {
			return err
		}
		rsaPrivate, rsaPublic, username, password, publicSSHKey := bl.rsaPrivate, bl.rsaPublic, bl.username, bl.password, bl.publicSSHKey

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

		registry, err := registryFromFlags(registryMirror, registryCredFile, bl.registry)
		if err != nil {
			return err
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
			RegistryMirror:     registry.Mirror,
			RegistryUsername:   registry.Username,
			RegistryPassword:   registry.Password,
		}

		if err := templates.ValidateTemplateData(data); err != nil {
			return fmt.Errorf("device %s: %w", deviceName, err)
		}

		serverData, agentData := nodes.apply(data)

		generate := writeBootstrapScript

		agentFile := fmt.Sprintf("bootstrap_agent_%s.sh", deviceName)
		serverFile := fmt.Sprintf("bootstrap_server_%s.sh", deviceName)
		dockerServerFile := fmt.Sprintf("bootstrap_docker_server_%s.sh", deviceName)

		if err := generate(templates.AgentTemplate, agentFile, agentData); err != nil {
			return err
		}
		if err := generate(templates.ServerTemplate, serverFile, serverData); err != nil {
			return err
		}
		if err := generate(templates.DockerServerTemplate, dockerServerFile, data); err != nil {
			return err
		}

		fmt.Printf("Successfully generated bootstrap scripts:\n")
		fmt.Printf("- %s\n", agentFile)
		fmt.Printf("- %s\n", serverFile)
		fmt.Printf("- %s\n", dockerServerFile)
		fmt.Print(nodes.describe(agentFile, serverFile))
		switch registry.Mirror {
		case "":
		case templates.RegistryMirrorNone:
			fmt.Printf("%s and %s remove a registries.yaml that an earlier script wrote (registry mirror none)\n", agentFile, serverFile)
		default:
			fmt.Printf("%s and %s configure the registry mirror %s (/etc/rancher/k3s/registries.yaml); they contain its credential, keep them private\n", agentFile, serverFile, registry.Mirror)
		}

		skipUpload, _ := cmd.Flags().GetBool("skip-upload")
		if !skipUpload {
			fmt.Println("TODO: Implement script upload to secure bucket")
		}

		return nil
	},
}

// bootstrapLogin is the decoded login of a device that the bootstrap scripts install.
type bootstrapLogin struct {
	rsaPrivate, rsaPublic, username, password, publicSSHKey string
	// registry is the registry mirror the platform offers the device, nil when it sends none.
	registry *api.FleetRegistry
}

// loadBootstrapLogin asks the platform for the login of a device and decodes it. The platform gives
// it only to developers who may open a terminal; a viewer gets its 403 explanation.
func loadBootstrapLogin(ctx context.Context, fl *api.Fleet, namespace, deviceName string) (*bootstrapLogin, error) {
	c, err := loadLogin(ctx, fl, deviceName)
	if err != nil {
		return nil, err
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
	var bl bootstrapLogin
	if bl.rsaPrivate, err = decode("rsa_private", c.RSAPrivate); err != nil {
		return nil, err
	}
	if bl.rsaPublic, err = decode("rsa_public", c.RSAPublic); err != nil {
		return nil, err
	}
	// The device-controller generates the key pair after the Device is created. Scripts without it
	// install empty keys and fail at the k3s CA step on the device.
	if bl.rsaPrivate == "" || bl.rsaPublic == "" {
		return nil, errNoKeyPair(namespace, deviceName)
	}
	if bl.username, err = decode("username", c.Username); err != nil {
		return nil, err
	}
	if bl.password, err = decode("userpassword", c.Password); err != nil {
		return nil, err
	}
	if bl.publicSSHKey, err = decode("publicsshkey", c.PublicSSHKey); err != nil {
		return nil, err
	}
	bl.registry = c.Registry
	return &bl, nil
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
	addNodeFlags(bootstrapCmd)
	bootstrapCmd.Flags().String("docker-secret", "", "The contents of the .dockerconfigjson")
	bootstrapCmd.Flags().String("registry-mirror", "",
		"pull images through this Harbor host (pull-through cache projects dockerhub, ghcr, gcr, quay, k8s, nvcr), with the upstream registry as the fallback: the scripts write /etc/rancher/k3s/registries.yaml before the k3s installer. \"none\" removes a file the scripts wrote. Without it the file is left alone (or follows the registry that the platform sends with the device credentials, when it sends one)")
	bootstrapCmd.Flags().String("registry-credential-file", "",
		"file with the Harbor credential for --registry-mirror, one line username:password (a robot account of the teamspace), readable by you only. The password goes into the generated scripts and is never printed")
	rootCmd.AddCommand(bootstrapCmd)
}

// writeBootstrapScript renders a bootstrap script to filename with mode 0600: the scripts hold the device
// password, the k3s token and the teamspace's Harbor robot password. A file that already exists (written with
// 0644 by an earlier tnctl) is chmodded too, because OpenFile's mode applies to new files only.
func writeBootstrapScript(tmpl *template.Template, filename string, data templates.TemplateData) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	return tmpl.Execute(f, data)
}
