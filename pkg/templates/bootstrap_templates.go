package templates

import (
	"embed"
	"fmt"
	"regexp"
	"text/template"
)

// DefaultK3SVersion is the k3s release the bootstrap scripts install unless told otherwise
// (`tnctl bootstrap --k3s-version`, or INSTALL_K3S_VERSION when the script runs). It is the stable
// channel (https://update.k3s.io/v1-release/channels) at the time of writing, and the release the
// CA harness in test/k3s-ca proves with the re-issued root. Server and agent scripts use the same
// value, so a cluster is not installed with two versions by accident.
const DefaultK3SVersion = "v1.36.5+k3s1"

var k3sVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`)

// ValidateK3SVersion accepts an exact release (v1.36.5+k3s1), or the channels "stable" (the
// installer's own default: whatever stable is when the script runs) and "latest". The value ends up
// in a shell script, so nothing else is let through.
func ValidateK3SVersion(v string) error {
	if v == "" || v == "stable" || v == "latest" || k3sVersionRE.MatchString(v) {
		return nil
	}
	return fmt.Errorf("invalid k3s version %q: want an exact release like %s, or stable or latest", v, DefaultK3SVersion)
}

type TemplateData struct {
	Domain       string
	Namespace    string
	DeviceID     string
	RSAPrivate   string
	RSAPublic    string
	Username     string
	Password     string
	PublicSSHKey string
	K3SToken     string
	DockerSecret string
	// K3SVersion is the k3s release to install: an exact release, "stable" or "latest".
	// Empty means DefaultK3SVersion.
	K3SVersion string
	// K3SUpgrade makes the generated scripts replace an installed k3s with K3SVersion
	// (TN_K3S_UPGRADE=true is the default in the script). Without it a re-run keeps the installed version.
	K3SUpgrade bool
	// K3SForce allows that upgrade to skip minor versions (TN_K3S_FORCE=true is the default in the script).
	K3SForce bool
}

func (d TemplateData) k3sVersion() string {
	if d.K3SVersion == "" {
		return DefaultK3SVersion
	}
	return d.K3SVersion
}

// K3SPin is the value for INSTALL_K3S_VERSION, or "" when the installer is to pick (stable, latest).
func (d TemplateData) K3SPin() string {
	if v := d.k3sVersion(); v != "stable" && v != "latest" {
		return v
	}
	return ""
}

// K3SChannel is the value for INSTALL_K3S_CHANNEL: "latest", or "" (the installer's default,
// stable, and the pinned releases).
func (d TemplateData) K3SChannel() string {
	if d.k3sVersion() == "latest" {
		return "latest"
	}
	return ""
}

//go:embed agent.sh.tmpl
var AgentTemplateStr string

//go:embed server.sh.tmpl
var ServerTemplateStr string

//go:embed docker_server.sh.tmpl
var DockerServerTemplateStr string

//go:embed partials/*.tmpl
var partialsFS embed.FS

// TnK3sCAScript is the k3s CA library (setup, check, remediate). It is embedded in the server
// bootstrap script as the template "tn-k3s-ca.sh" and is also usable stand-alone.
//
//go:embed scripts/tn-k3s-ca.sh
var TnK3sCAScript string

// TnK3sInstallScript is the k3s release policy and cgroup v1 handling (tn_inst_prepare), embedded
// in the server and the agent bootstrap script as the template "tn-k3s-install.sh".
//
//go:embed scripts/tn-k3s-install.sh
var TnK3sInstallScript string

//go:embed scripts/*.sh
var scriptsFS embed.FS

var (
	AgentTemplate        *template.Template
	ServerTemplate       *template.Template
	DockerServerTemplate *template.Template
)

func init() {
	AgentTemplate = template.Must(template.New("agent").Parse(AgentTemplateStr))
	template.Must(AgentTemplate.ParseFS(partialsFS, "partials/*.tmpl"))
	template.Must(AgentTemplate.ParseFS(scriptsFS, "scripts/tn-k3s-install.sh"))

	ServerTemplate = template.Must(template.New("server").Parse(ServerTemplateStr))
	template.Must(ServerTemplate.ParseFS(partialsFS, "partials/*.tmpl"))
	template.Must(ServerTemplate.ParseFS(scriptsFS, "scripts/*.sh"))

	DockerServerTemplate = template.Must(template.New("docker_server").Parse(DockerServerTemplateStr))
	template.Must(DockerServerTemplate.ParseFS(partialsFS, "partials/*.tmpl"))
}
