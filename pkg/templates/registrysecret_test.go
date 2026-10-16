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

// runRegistrySecret runs the block with a fake k3s that prints the .dockerconfigjson of the
// named secret from secrets (and fails for a secret that is not there).
func runRegistrySecret(t *testing.T, ds string, secrets map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	fake := "#!/bin/sh\ncase \"$6\" in\n"
	for name, v := range secrets {
		fake += name + ") printf '%s' '" + v + "' ;;\n"
	}
	fake += "*) exit 1 ;;\nesac\n"
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
	const empty = "eyJhdXRocyI6e319"
	for _, tt := range []struct {
		name, ds string
		secrets  map[string]string
		want     string
	}{
		{"given", "Z2l2ZW4=", map[string]string{"teknoir-registry-secret": "ZXhpc3Rpbmc="}, "SECRET=Z2l2ZW4=\n"},
		{"re-run keeps the device's", "", map[string]string{"teknoir-registry-secret": "ZXhpc3Rpbmc="}, "SECRET=ZXhpc3Rpbmc=\n"},
		{"re-run takes the legacy secret", "", map[string]string{"artifact-registry-secret": "bGVnYWN5"}, "SECRET=bGVnYWN5\n"},
		{"an empty config does not hide the legacy secret", "", map[string]string{"teknoir-registry-secret": empty, "artifact-registry-secret": "bGVnYWN5"}, "SECRET=bGVnYWN5\n"},
		{"first run gets a valid empty config", "", nil, "SECRET=" + empty + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if out := runRegistrySecret(t, tt.ds, tt.secrets); !strings.Contains(out, tt.want) {
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
