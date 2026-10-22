package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/templates"
)

const testRegPassword = "S3cretRobotPassw0rdFORtest"

func credFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBootstrapRegistryFlagsExist(t *testing.T) {
	for _, name := range []string{"registry-mirror", "registry-credential-file"} {
		f := bootstrapCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("no --%s flag", name)
		}
		if f.DefValue != "" {
			t.Errorf("--%s defaults to %q: without it registries.yaml must be left alone", name, f.DefValue)
		}
	}
}

func TestRegistryFromFlags_NothingGivenChangesNothing(t *testing.T) {
	s, err := registryFromFlags("", "", nil)
	if err != nil || s != (registrySettings{}) {
		t.Fatalf("%+v %v", s, err)
	}
	// a platform without a registry field, or with an empty one, changes nothing either
	for _, p := range []*api.FleetRegistry{nil, {}} {
		if s, err := registryFromFlags("", "", p); err != nil || s != (registrySettings{}) {
			t.Fatalf("%+v %v", s, err)
		}
	}
}

func TestRegistryFromFlags_HostWithCredentialFile(t *testing.T) {
	f := credFile(t, "robot$nodes:"+testRegPassword+"\n", 0o600)
	s, err := registryFromFlags("harbor.teknoir.cloud", f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s != (registrySettings{Mirror: "harbor.teknoir.cloud", Username: "robot$nodes", Password: testRegPassword}) {
		t.Errorf("%+v", s)
	}
	// CRLF files and a colon inside the password
	f = credFile(t, "u:pa:ss\r\n", 0o600)
	s, err = registryFromFlags("harbor.teknoir.cloud", f, nil)
	if err != nil || s.Username != "u" || s.Password != "pa:ss" {
		t.Errorf("%+v %v", s, err)
	}
}

func TestRegistryFromFlags_None(t *testing.T) {
	s, err := registryFromFlags("none", "", nil)
	if err != nil || s != (registrySettings{Mirror: "none"}) {
		t.Fatalf("%+v %v", s, err)
	}
	// none carries no credential, even when the platform sends one
	s, err = registryFromFlags("none", "", &api.FleetRegistry{Host: "h.example", Username: "u", Password: "p"})
	if err != nil || s != (registrySettings{Mirror: "none"}) {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestRegistryFromFlags_PlatformDefault(t *testing.T) {
	p := &api.FleetRegistry{Host: "harbor.example.test", Username: "robot$ts-demo", Password: testRegPassword}
	s, err := registryFromFlags("", "", p)
	if err != nil || s != (registrySettings{Mirror: "harbor.example.test", Username: "robot$ts-demo", Password: testRegPassword}) {
		t.Fatalf("%+v %v", s, err)
	}
	// the same host given explicitly takes the platform's credential
	s, err = registryFromFlags("harbor.example.test", "", p)
	if err != nil || s.Password != testRegPassword {
		t.Fatalf("%+v %v", s, err)
	}
	// a file wins over the platform's credential
	f := credFile(t, "mine:filepw\n", 0o600)
	s, err = registryFromFlags("", f, p)
	if err != nil || s.Username != "mine" || s.Mirror != "harbor.example.test" {
		t.Fatalf("%+v %v", s, err)
	}
	// another host explicitly: the platform's credential is for its own host only
	if _, err = registryFromFlags("other.example.test", "", p); err == nil {
		t.Fatal("the platform's credential must not be sent to another host")
	}
	// the platform's mirror without a credential still needs one
	if _, err = registryFromFlags("", "", &api.FleetRegistry{Host: "harbor.example.test"}); err == nil || !strings.Contains(err.Error(), "that the platform sent") {
		t.Fatalf("%v", err)
	}
	// an unusable host from the platform is refused, not rendered
	if _, err = registryFromFlags("", "", &api.FleetRegistry{Host: "bad host", Username: "u", Password: "p"}); err == nil {
		t.Fatal("a bad host from the platform was accepted")
	}
}

func TestRegistryFromFlags_Rejects(t *testing.T) {
	good := credFile(t, "u:"+testRegPassword+"\n", 0o600)
	cases := map[string]struct {
		mirror, file string
	}{
		"host without a credential": {"harbor.example.test", ""},
		"credential without a host": {"", good},
		"invalid host":              {"bad host", good},
		"host with scheme":          {"https://harbor.example.test", good},
		"host with injection":       {"a$(id).example", good},
		"missing file":              {"harbor.example.test", filepath.Join(t.TempDir(), "nope")},
		"empty file":                {"harbor.example.test", credFile(t, "", 0o600)},
		"blank file":                {"harbor.example.test", credFile(t, "\n\n", 0o600)},
		"no colon":                  {"harbor.example.test", credFile(t, testRegPassword+"\n", 0o600)},
		"no user":                   {"harbor.example.test", credFile(t, ":"+testRegPassword+"\n", 0o600)},
		"no password":               {"harbor.example.test", credFile(t, "user:\n", 0o600)},
		"two lines":                 {"harbor.example.test", credFile(t, "u:"+testRegPassword+"\nv:w\n", 0o600)},
		"directory":                 {"harbor.example.test", t.TempDir()},
	}
	if runtime.GOOS != "windows" {
		cases["group readable"] = struct{ mirror, file string }{"harbor.example.test", credFile(t, "u:"+testRegPassword+"\n", 0o640)}
		cases["world readable"] = struct{ mirror, file string }{"harbor.example.test", credFile(t, "u:"+testRegPassword+"\n", 0o644)}
	}
	for name, c := range cases {
		s, err := registryFromFlags(c.mirror, c.file, nil)
		if err == nil {
			t.Errorf("%s: accepted (%+v)", name, s)
			continue
		}
		if strings.Contains(err.Error(), testRegPassword) {
			t.Errorf("%s: the error contains the password: %v", name, err)
		}
	}
}

func TestCheckRegistryFlags(t *testing.T) {
	if err := checkRegistryFlags("", ""); err != nil {
		t.Fatal(err)
	}
	// a credential file without a host is fine here: the platform may still name the host
	if err := checkRegistryFlags("", credFile(t, "u:p\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	if err := checkRegistryFlags("bad host", ""); err == nil {
		t.Fatal("bad host accepted")
	}
	if err := checkRegistryFlags("harbor.example.test", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing file accepted")
	}
}

// What registryFromFlags returns renders into a script that ValidateTemplateData accepts, and the
// robot$ user name needs no escaping by the user.
func TestRegistrySettingsRender(t *testing.T) {
	f := credFile(t, "robot$nodes:"+testRegPassword+"\n", 0o600)
	s, err := registryFromFlags("harbor.teknoir.cloud", f, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := templates.TemplateData{Domain: "example.test", Namespace: "ns", DeviceID: "dev-1", K3SToken: "tok",
		RegistryMirror: s.Mirror, RegistryUsername: s.Username, RegistryPassword: s.Password}
	for name, render := range map[string]func() ([]byte, error){
		"server": func() ([]byte, error) { return templates.RenderServer(d) },
	} {
		out, err := render()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(out), "_TN_REG_HOST='harbor.teknoir.cloud'") || !strings.Contains(string(out), "_TN_REG_USER='robot$nodes'") {
			t.Errorf("%s: the registry block is missing", name)
		}
	}
}
