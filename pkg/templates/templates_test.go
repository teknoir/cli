package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// ---------------------------------------------------------------- the k3s pin

func renderK3S(t *testing.T, tpl *template.Template, version string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, TemplateData{Domain: "test.cloud", DeviceID: "dev-1", K3SToken: "tok", K3SVersion: version}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// installer lines of the script: the exports before ./k3s_installer.sh
func exportsOf(out, name string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "export "+name+"=") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestK3SPinDefaultsForServerAndAgent(t *testing.T) {
	if DefaultK3SVersion != "v1.36.5+k3s1" {
		t.Errorf("default pin %s", DefaultK3SVersion)
	}
	for name, tpl := range map[string]*template.Template{"server": ServerTemplate, "agent": AgentTemplate} {
		for _, v := range []string{"", DefaultK3SVersion} {
			out := renderK3S(t, tpl, v)
			want := `export INSTALL_K3S_VERSION="${INSTALL_K3S_VERSION:-v1.36.5+k3s1}"`
			if got := exportsOf(out, "INSTALL_K3S_VERSION"); len(got) != 1 || got[0] != want {
				t.Errorf("%s (version %q): %q", name, v, got)
			}
			// the pin is set before the installer runs
			if strings.Index(out, "INSTALL_K3S_VERSION=") > strings.Index(out, "\n$SUDO ./k3s_installer.sh") {
				t.Errorf("%s: the pin comes after the installer", name)
			}
		}
	}
}

func TestK3SPinOverridesAndChannels(t *testing.T) {
	for name, tpl := range map[string]*template.Template{"server": ServerTemplate, "agent": AgentTemplate} {
		out := renderK3S(t, tpl, "v1.34.11+k3s1")
		if got := exportsOf(out, "INSTALL_K3S_VERSION"); len(got) != 1 || !strings.Contains(got[0], "v1.34.11+k3s1") || strings.Contains(strings.Join(exportsOf(out, "INSTALL_K3S_VERSION"), ""), "v1.36.5") {
			t.Errorf("%s override: %q", name, got)
		}
		// stable keeps the old behaviour: nothing is exported
		out = renderK3S(t, tpl, "stable")
		if len(exportsOf(out, "INSTALL_K3S_VERSION"))+len(exportsOf(out, "INSTALL_K3S_CHANNEL")) != 0 {
			t.Errorf("%s stable exports a version or channel", name)
		}
		if !strings.Contains(out, "stable") {
			t.Errorf("%s stable: no note", name)
		}
		out = renderK3S(t, tpl, "latest")
		if got := exportsOf(out, "INSTALL_K3S_CHANNEL"); len(got) != 1 || !strings.Contains(got[0], "latest") || len(exportsOf(out, "INSTALL_K3S_VERSION")) != 0 {
			t.Errorf("%s latest: %q", name, got)
		}
	}
}

// server and agent of one cluster get the same version from one TemplateData
func TestK3SPinIsTheSameForServerAndAgent(t *testing.T) {
	for _, v := range []string{"", "v1.35.9+k3s1", "stable", "latest"} {
		s := exportsOf(renderK3S(t, ServerTemplate, v), "INSTALL_K3S_VERSION")
		a := exportsOf(renderK3S(t, AgentTemplate, v), "INSTALL_K3S_VERSION")
		if strings.Join(s, "|") != strings.Join(a, "|") {
			t.Errorf("version %q: server %q, agent %q", v, s, a)
		}
	}
}

func TestValidateK3SVersion(t *testing.T) {
	for _, ok := range []string{"", "stable", "latest", "v1.36.5+k3s1", "v1.34.11+k3s2", "v1.27.0+k3s10"} {
		if err := ValidateK3SVersion(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"1.36.5+k3s1", "v1.36.5", "v1.36", "v1.36.5-rc1+k3s1", "testing", "v1.36.5+k3s1; rm -rf /", "$(id)", "v1.36.5+k3s1\n", "`x`", "\"", " stable"} {
		if err := ValidateK3SVersion(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// the rendered scripts parse, with every kind of pin
func TestRenderedScriptsParseWithEveryPin(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not installed")
	}
	for name, tpl := range map[string]*template.Template{"server": ServerTemplate, "agent": AgentTemplate} {
		for _, v := range []string{"", "v1.34.11+k3s1", "stable", "latest"} {
			f := filepath.Join(t.TempDir(), "s.sh")
			if err := os.WriteFile(f, []byte(renderK3S(t, tpl, v)), 0o600); err != nil {
				t.Fatal(err)
			}
			if o, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s %q: sh -n: %v\n%s", name, v, err, o)
			}
		}
	}
}

// The CA is made in the data dir that setup resolved; the manifests and the config written
// afterwards must use the same one.
func TestServerScriptUsesTheResolvedDataDir(t *testing.T) {
	out := renderK3S(t, ServerTemplate, "")
	for _, want := range []string{"${DATA_DIR}/server/manifests", `tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml data-dir "data-dir: ${DATA_DIR}"`} {
		if !strings.Contains(out, want) {
			t.Errorf("server script lacks %q", want)
		}
	}
	if strings.Contains(out, "/var/lib/rancher/k3s/server/manifests") {
		t.Error("manifests are still written to a hard-coded data dir")
	}
}
