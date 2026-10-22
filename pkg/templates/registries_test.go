package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const (
	regPassword = "S3cretRobotPassw0rdFORtest"
	regUsername = "robot$nodes"
	regMarker   = "# managed by tnctl bootstrap"
)

// registriesBlock renders the registries partial for d, the way server.sh.tmpl and agent.sh.tmpl include it.
func registriesBlock(t *testing.T, d TemplateData) string {
	t.Helper()
	var b strings.Builder
	if err := ServerTemplate.ExecuteTemplate(&b, "registries.sh.tmpl", d); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func regData(mirror string) TemplateData {
	d := goldenData()
	d.RegistryMirror = mirror
	d.RegistryUsername = regUsername
	d.RegistryPassword = regPassword
	if mirror == "none" || mirror == "" {
		d.RegistryUsername, d.RegistryPassword = "", ""
	}
	return d
}

type regRun struct {
	dir    string // K3S_CONFIG_PATH
	output string // combined stdout and stderr
	err    error
}

func (r regRun) file() string { return filepath.Join(r.dir, "registries.yaml") }

// runRegistries runs the partial in dir under `set -e` and `umask 027` (the header of the bootstrap
// script sets both), with info/warn/fatal as in the header. SUDO is empty, as for root.
func runRegistries(t *testing.T, dir string, d TemplateData) regRun {
	t.Helper()
	script := "set -e\numask 027\n" +
		"info() { echo \"[INFO] $*\"; }\nwarn() { echo \"[WARN] $*\" >&2; }\nfatal() { echo \"[ERROR] $*\" >&2; exit 1; }\n" +
		"SUDO=\nK3S_CONFIG_PATH=" + dir + "\n" + registriesBlock(t, d) + "echo done\n"
	var out bytes.Buffer
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return regRun{dir: dir, output: out.String(), err: err}
}

func (r regRun) mustOK(t *testing.T) regRun {
	t.Helper()
	if r.err != nil || !strings.HasSuffix(r.output, "done\n") {
		t.Fatalf("script failed: %v\n%s", r.err, r.output)
	}
	return r
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

func noPassword(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, regPassword) {
		t.Errorf("%s contains the password", what)
	}
}

const wantFresh = regMarker + `
# k3s private registry mirrors: pull-through cache on harbor.example.test. containerd asks Harbor first and
# falls back to the upstream registry. Written by tnctl bootstrap --registry-mirror; do not edit.
mirrors:
  docker.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "dockerhub/$1"
  ghcr.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "ghcr/$1"
  gcr.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "gcr/$1"
  quay.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "quay/$1"
  registry.k8s.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "k8s/$1"
  nvcr.io:
    endpoint:
      - "https://harbor.example.test"
    rewrite:
      "^(.*)$": "nvcr/$1"
configs:
  "harbor.example.test":
    auth:
      username: "robot$nodes"
      password: "` + regPassword + `"
`

func TestRegistries_Fresh(t *testing.T) {
	dir := t.TempDir()
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	if got := readFile(t, r.file()); got != wantFresh {
		t.Fatalf("registries.yaml:\n%s\nwant:\n%s", got, wantFresh)
	}
	fi, _ := os.Stat(r.file())
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 (under umask 027)", fi.Mode().Perm())
	}
	if !strings.Contains(r.output, "Registry mirror harbor.example.test configured") {
		t.Errorf("output: %s", r.output)
	}
	noPassword(t, "output", r.output)
	if left, _ := filepath.Glob(filepath.Join(dir, "registries.yaml.*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestRegistries_RerunIsANoOp(t *testing.T) {
	dir := t.TempDir()
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	ino, before := inode(t, r.file()), readFile(t, r.file())
	if err := os.Chmod(r.file(), 0o644); err != nil { // loosened by someone: repaired, content untouched
		t.Fatal(err)
	}
	r2 := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	if !strings.Contains(r2.output, "already configured") {
		t.Errorf("output: %s", r2.output)
	}
	if inode(t, r.file()) != ino || readFile(t, r.file()) != before {
		t.Error("a re-run rewrote the file")
	}
	if fi, _ := os.Stat(r.file()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v after a re-run", fi.Mode().Perm())
	}
	noPassword(t, "output", r2.output)
	if left, _ := filepath.Glob(filepath.Join(dir, "registries.yaml.*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestRegistries_ChangedCredentialAndHostAreWritten(t *testing.T) {
	dir := t.TempDir()
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	d := regData("harbor.example.test")
	d.RegistryPassword = "RotatedPassw0rd"
	runRegistries(t, dir, d).mustOK(t)
	if got := readFile(t, r.file()); !strings.Contains(got, `password: "RotatedPassw0rd"`) || strings.Contains(got, regPassword) {
		t.Errorf("rotated password not written:\n%s", got)
	}
	d = regData("other.example.test:8443")
	runRegistries(t, dir, d).mustOK(t)
	if got := readFile(t, r.file()); !strings.Contains(got, `"https://other.example.test:8443"`) || strings.Contains(got, "harbor.example.test") {
		t.Errorf("new host not written:\n%s", got)
	}
}

func TestRegistries_ForeignFileIsKept(t *testing.T) {
	dir := t.TempDir()
	foreign := "mirrors:\n  docker.io:\n    endpoint:\n      - \"https://mine.example\"\n"
	p := filepath.Join(dir, "registries.yaml")
	if err := os.WriteFile(p, []byte(foreign), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, mirror := range []string{"harbor.example.test", "none"} {
		r := runRegistries(t, dir, regData(mirror)).mustOK(t)
		if readFile(t, p) != foreign {
			t.Errorf("%s: the foreign file was changed", mirror)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
			t.Errorf("%s: mode of the foreign file changed to %v", mirror, fi.Mode().Perm())
		}
		if !strings.Contains(r.output, "[WARN]") || !strings.Contains(r.output, "was not written by tnctl bootstrap") {
			t.Errorf("%s: no warning:\n%s", mirror, r.output)
		}
		noPassword(t, "output", r.output)
	}
	// a comment that only contains the marker text is not the marker
	if err := os.WriteFile(p, []byte("# not "+regMarker+"\nmirrors: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	if !strings.Contains(r.output, "[WARN]") || strings.Contains(readFile(t, p), "harbor.example.test") {
		t.Errorf("a file with the marker text inside a comment was taken as ours:\n%s", r.output)
	}
}

// playbooks/registry-mirror.yml (ansible) writes the same content under its own first line; tnctl treats
// that file as its own: it replaces it (a stale credential must not stay) and `none` removes it.
func TestRegistries_AcceptsThePlaybooksMarker(t *testing.T) {
	const pbMarker = "# managed by teknoir"
	dir := t.TempDir()
	p := filepath.Join(dir, "registries.yaml")
	if err := os.WriteFile(p, []byte(pbMarker+"\nmirrors: {}\nconfigs:\n  \"old.example\":\n    auth:\n      password: \"stale\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	got := readFile(t, p)
	if strings.Contains(r.output, "[WARN]") || strings.Contains(got, "stale") || !strings.HasPrefix(got, regMarker+"\n") || !strings.Contains(got, regPassword) {
		t.Errorf("the playbook's file was not replaced:\n%s\n%s", r.output, got)
	}
	if err := os.WriteFile(p, []byte(pbMarker+"\nmirrors: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runRegistries(t, dir, regData("none")).mustOK(t)
	if _, err := os.Stat(p); !os.IsNotExist(err) || !strings.Contains(r.output, "Registry mirror removed") {
		t.Errorf("none did not remove the playbook's file: %v\n%s", err, r.output)
	}
	noPassword(t, "output", r.output)
}

func TestRegistries_NoneRemovesAManagedFile(t *testing.T) {
	dir := t.TempDir()
	r := runRegistries(t, dir, regData("harbor.example.test")).mustOK(t)
	r2 := runRegistries(t, dir, regData("none")).mustOK(t)
	if _, err := os.Stat(r.file()); !os.IsNotExist(err) {
		t.Errorf("the managed file is still there: %v", err)
	}
	if !strings.Contains(r2.output, "Registry mirror removed") {
		t.Errorf("output: %s", r2.output)
	}
	// without a file, none is a quiet no-op
	r3 := runRegistries(t, dir, regData("none")).mustOK(t)
	if strings.TrimSpace(r3.output) != "done" {
		t.Errorf("none without a file printed: %q", r3.output)
	}
	noPassword(t, "output", r2.output+r3.output)
	// none never carries a credential into the script
	if blk := registriesBlock(t, regData("none")); strings.Contains(blk, "_TN_REG_PASS") || strings.Contains(blk, regPassword) {
		t.Error("the none variant mentions a credential")
	}
}

func TestRegistries_EmptyMirrorRendersNothingAndLeavesTheFileAlone(t *testing.T) {
	d := regData("")
	if blk := registriesBlock(t, d); blk != "" {
		t.Fatalf("an empty RegistryMirror must render nothing, got:\n%s", blk)
	}
	for _, tpl := range []string{"server", "agent"} {
		var b bytes.Buffer
		var err error
		if tpl == "server" {
			err = ServerTemplate.Execute(&b, d)
		} else {
			err = AgentTemplate.Execute(&b, d)
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "registries.yaml") || strings.Contains(b.String(), "_TN_REG_") {
			t.Errorf("%s script without a mirror mentions the registry code", tpl)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "registries.yaml")
	for _, content := range []string{"mine: true\n", regMarker + "\nmirrors: {}\n"} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runRegistries(t, dir, d).mustOK(t)
		if readFile(t, p) != content {
			t.Errorf("an empty mirror changed %q", content)
		}
	}
}

func TestRegistries_IncludedBeforeTheInstaller(t *testing.T) {
	d := goldenData()
	for name, render := range map[string]func() (string, error){
		"server": func() (string, error) {
			var b bytes.Buffer
			err := ServerTemplate.Execute(&b, d)
			return b.String(), err
		},
		"agent": func() (string, error) {
			var b bytes.Buffer
			err := AgentTemplate.Execute(&b, d)
			return b.String(), err
		},
	} {
		out, err := render()
		if err != nil {
			t.Fatal(err)
		}
		reg, inst := strings.Index(out, "_TN_REG_FILE="), strings.Index(out, "$SUDO ./k3s_installer.sh")
		if reg < 0 || inst < 0 || reg > inst {
			t.Errorf("%s: the registry block (%d) must come before the k3s installer (%d)", name, reg, inst)
		}
		if strings.Count(out, "_TN_REG_FILE=") != 1 {
			t.Errorf("%s: the registry block appears %d times", name, strings.Count(out, "_TN_REG_FILE="))
		}
	}
	var b bytes.Buffer
	if err := DockerServerTemplate.Execute(&b, d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "registries.yaml") {
		t.Error("the Docker-mode script must not write registries.yaml")
	}
}

// A password with characters that need care (a `#` and a `:` in YAML, `!` and `%` in printf) round-trips
// byte for byte, and a `$` in the user name is not expanded by the shell.
func TestRegistries_SpecialCharactersSurvive(t *testing.T) {
	dir := t.TempDir()
	d := regData("harbor.example.test")
	d.RegistryUsername = "robot$a$HOME"
	d.RegistryPassword = "p#a:s!s%s%d-_.~"
	if err := ValidateTemplateData(d); err != nil {
		t.Fatal(err)
	}
	r := runRegistries(t, dir, d).mustOK(t)
	got := readFile(t, r.file())
	if !strings.Contains(got, `username: "robot$a$HOME"`) || !strings.Contains(got, `password: "p#a:s!s%s%d-_.~"`) {
		t.Errorf("credential mangled:\n%s", got)
	}
}

// The password is in the script (it has to be), in single quotes, so a value that tries to close the
// quote or run something is refused by the validation before a script exists.
func TestRegistries_ValidateRegistryData(t *testing.T) {
	ok := func(mod func(*TemplateData)) error {
		d := regData("harbor.example.test")
		mod(&d)
		return ValidateTemplateData(d)
	}
	if err := ok(func(*TemplateData) {}); err != nil {
		t.Fatalf("the robot$nodes credential must be accepted: %v", err)
	}
	for _, h := range []string{"", "none", "harbor.teknoir.cloud", "harbor.example.test:8443", "localhost", "10.0.0.5"} {
		if err := ValidateRegistryMirror(h); err != nil {
			t.Errorf("host %q rejected: %v", h, err)
		}
	}
	for _, h := range []string{"a b", "a/b", `a"b`, "a'b", "a$b", "a;b", "https://harbor.example.test", "-x", "a:b:c", "x:99999999", "harbor.example.test\n", "$(id)"} {
		if err := ValidateRegistryMirror(h); err == nil {
			t.Errorf("host %q accepted", h)
		}
	}
	bad := map[string]func(*TemplateData){
		"quote closes":        func(d *TemplateData) { d.RegistryPassword = "x'; touch /tmp/pwn; '" },
		"single quote":        func(d *TemplateData) { d.RegistryPassword = "a'b" },
		"double quote":        func(d *TemplateData) { d.RegistryPassword = `a"b` },
		"backslash":           func(d *TemplateData) { d.RegistryPassword = `a\b` },
		"backtick":            func(d *TemplateData) { d.RegistryPassword = "a`id`b" },
		"newline":             func(d *TemplateData) { d.RegistryPassword = "a\nb" },
		"carriage return":     func(d *TemplateData) { d.RegistryPassword = "a\rb" },
		"space":               func(d *TemplateData) { d.RegistryPassword = "a b" },
		"nul":                 func(d *TemplateData) { d.RegistryPassword = "a\x00b" },
		"del":                 func(d *TemplateData) { d.RegistryPassword = "a\x7fb" },
		"too long":            func(d *TemplateData) { d.RegistryPassword = strings.Repeat("a", 513) },
		"user quote":          func(d *TemplateData) { d.RegistryUsername = "robot'x" },
		"user newline":        func(d *TemplateData) { d.RegistryUsername = "robot\nx" },
		"user empty":          func(d *TemplateData) { d.RegistryUsername = "" },
		"password empty":      func(d *TemplateData) { d.RegistryPassword = "" },
		"credential w/o host": func(d *TemplateData) { d.RegistryMirror = "" },
		"credential on none":  func(d *TemplateData) { d.RegistryMirror = "none" },
		"host with quote":     func(d *TemplateData) { d.RegistryMirror = "a'b" },
		"host with dollar":    func(d *TemplateData) { d.RegistryMirror = "a$b.example" },
	}
	for name, mod := range bad {
		err := ok(mod)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		noPassword(t, name+" error", err.Error())
		for _, leak := range []string{"touch", "a'b", "a\nb"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: error %q leaks %q", name, err, leak)
			}
		}
		if _, rerr := RenderServer(func() TemplateData { d := regData("harbor.example.test"); mod(&d); return d }()); rerr == nil {
			t.Errorf("%s: RenderServer accepted it", name)
		}
	}
}

// A credential hidden in a quote-closing value must never become shell: run the rendered block with a
// value that the validation would refuse, to prove why it must (the test documents the single-quote context).
func TestRegistries_TheValidationIsWhatKeepsTheScriptSafe(t *testing.T) {
	d := regData("harbor.example.test")
	d.RegistryPassword = "x'; echo INJECTED; '"
	if ValidateTemplateData(d) == nil {
		t.Fatal("validation must refuse a quote in the password")
	}
	blk := registriesBlock(t, d) // unvalidated render: the quote does close the string
	if !strings.Contains(blk, "_TN_REG_PASS='x'; echo INJECTED; ''") {
		t.Fatalf("test premise changed:\n%s", blk)
	}
}
