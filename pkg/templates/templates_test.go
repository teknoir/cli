package templates

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func TestTemplates_Execution(t *testing.T) {
	data := TemplateData{
		Domain:       "test.cloud",
		Namespace:    "test-ns",
		DeviceID:     "test-device",
		RSAPrivate:   "private-key",
		RSAPublic:    "public-key",
		Username:     "test-user",
		Password:     "test-pass",
		PublicSSHKey: "ssh-key",
		K3SToken:     "k3s-token",
		DockerSecret: "docker-secret",
	}

	tests := []struct {
		name     string
		template *template.Template
	}{
		{"Agent", AgentTemplate},
		{"Server", ServerTemplate},
		{"DockerServer", DockerServerTemplate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := tt.template.Execute(&buf, data)
			if err != nil {
				t.Fatalf("failed to execute template %s: %v", tt.name, err)
			}

			output := buf.String()

			// Basic sanity checks
			if !strings.HasPrefix(output, "#!/bin/sh") {
				t.Errorf("template %s: missing shebang", tt.name)
			}
			if !strings.Contains(output, "test.cloud") {
				t.Errorf("template %s: missing domain", tt.name)
			}
			if !strings.Contains(output, "test-device") {
				t.Errorf("template %s: missing device ID", tt.name)
			}
			if !strings.Contains(output, "Install device specific keys") {
				t.Errorf("template %s: missing keys section", tt.name)
			}
			if !strings.Contains(output, "setup_user()") {
				t.Errorf("template %s: missing setup_user function", tt.name)
			}
			if !strings.Contains(output, "Device bootstrapped successfully!") {
				t.Errorf("template %s: missing success message", tt.name)
			}
		})
	}
}
