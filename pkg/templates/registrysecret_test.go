package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// registrySecretBlock renders the registry_secret partial for docker secret ds.
func registrySecretBlock(t *testing.T, ds string) string {
	t.Helper()
	d := goldenData()
	d.DockerSecret = ds
	var b strings.Builder
	if err := ServerTemplate.ExecuteTemplate(&b, "registry_secret.sh.tmpl", d); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// runRegistrySecret runs the block with a fake k3s that prints existing (or fails when empty).
func runRegistrySecret(t *testing.T, ds, existing string) string {
	t.Helper()
	dir := t.TempDir()
	fake := "#!/bin/sh\n[ -n \"" + existing + "\" ] || exit 1\nprintf '%s' '" + existing + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "k3s"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "set -e\ninfo() { echo \"[INFO] $*\"; }\nSUDO=\n" + registrySecretBlock(t, ds) + "echo \"SECRET=${_TN_DOCKER_SECRET}\"\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func TestRegistrySecret(t *testing.T) {
	for _, tt := range []struct{ name, ds, existing, want string }{
		{"given", "Z2l2ZW4=", "ZXhpc3Rpbmc=", "SECRET=Z2l2ZW4=\n"},
		{"re-run keeps the device's", "", "ZXhpc3Rpbmc=", "SECRET=ZXhpc3Rpbmc=\n"},
		{"first run gets a valid empty config", "", "", "SECRET=eyJhdXRocyI6e319\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if out := runRegistrySecret(t, tt.ds, tt.existing); !strings.Contains(out, tt.want) {
				t.Fatalf("want %q in:\n%s", tt.want, out)
			}
		})
	}
}

func TestManifestsUseTheRegistrySecretVariable(t *testing.T) {
	out := renderK3S(t, ServerTemplate, "")
	if strings.Count(out, `.dockerconfigjson: "${_TN_DOCKER_SECRET}"`) != 2 {
		t.Fatal("both teknoir-registry-secret objects must take ${_TN_DOCKER_SECRET}")
	}
	if strings.Index(out, "_TN_DOCKER_SECRET=\"") > strings.Index(out, "teknoir.yaml > /dev/null << EOL") {
		t.Fatal("_TN_DOCKER_SECRET must be set before the manifests are written")
	}
}
