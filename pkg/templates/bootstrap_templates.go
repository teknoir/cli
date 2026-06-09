package templates

import (
	"embed"
	"text/template"
)

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
}

//go:embed agent.sh.tmpl
var AgentTemplateStr string

//go:embed server.sh.tmpl
var ServerTemplateStr string

//go:embed docker_server.sh.tmpl
var DockerServerTemplateStr string

//go:embed partials/*.tmpl
var partialsFS embed.FS

var (
	AgentTemplate        *template.Template
	ServerTemplate       *template.Template
	DockerServerTemplate *template.Template
)

func init() {
	AgentTemplate = template.Must(template.New("agent").Parse(AgentTemplateStr))
	template.Must(AgentTemplate.ParseFS(partialsFS, "partials/*.tmpl"))

	ServerTemplate = template.Must(template.New("server").Parse(ServerTemplateStr))
	template.Must(ServerTemplate.ParseFS(partialsFS, "partials/*.tmpl"))

	DockerServerTemplate = template.Must(template.New("docker_server").Parse(DockerServerTemplateStr))
	template.Must(DockerServerTemplate.ParseFS(partialsFS, "partials/*.tmpl"))
}
