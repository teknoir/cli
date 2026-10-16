package templates

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
)

// allTemplatesFS holds every embedded template and partial. It is used only
// to fingerprint the templates (see TemplatesSHA256).
//
//go:embed *.sh.tmpl partials/*.tmpl
var allTemplatesFS embed.FS

// unsafeChars are rejected in every TemplateData field. The templates place
// values inside double-quoted and heredoc shell contexts, where these
// characters would allow quoting breakouts or expansion.
const unsafeChars = "\"`$\\"

type field struct {
	name      string
	value     string
	multiline bool
}

// ValidateTemplateData rejects TemplateData that is unsafe to render into a
// shell script. The characters " ` $ and \ are rejected in every field. A
// newline (or carriage return) is rejected in every field except RSAPrivate
// and RSAPublic, which are multi-line PEM material.
//
// The node configuration (role, robot, labels, taints, server address) is checked for the same
// characters here; its syntax is checked by NodeConfig.Validate and ValidateHost, which `tnctl
// bootstrap` calls before it asks the platform for the device.
//
// Errors name the field and the offending character class, never the value.
func ValidateTemplateData(d TemplateData) error {
	fields := []field{
		{"Domain", d.Domain, false},
		{"Namespace", d.Namespace, false},
		{"DeviceID", d.DeviceID, false},
		{"RSAPrivate", d.RSAPrivate, true},
		{"RSAPublic", d.RSAPublic, true},
		{"Username", d.Username, false},
		{"Password", d.Password, false},
		{"PublicSSHKey", d.PublicSSHKey, false},
		{"K3SToken", d.K3SToken, false},
		{"DockerSecret", d.DockerSecret, false},
		{"Node.Role", d.Node.Role, false},
		{"Node.Robot", d.Node.Robot, false},
		{"Node.Name", d.Node.Name, false},
		{"Server", d.Server, false},
	}
	for _, l := range d.Node.Labels {
		fields = append(fields, field{"Node.Labels", l, false})
	}
	for _, t := range d.Node.Taints {
		fields = append(fields, field{"Node.Taints", t, false})
	}
	for _, s := range d.TLSSANs {
		fields = append(fields, field{"TLSSANs", s, false})
	}
	for _, f := range fields {
		if strings.ContainsAny(f.value, unsafeChars) {
			return fmt.Errorf("template data field %s contains a character that is unsafe in a shell script (double quote, backtick, dollar or backslash)", f.name)
		}
		if !f.multiline && strings.ContainsAny(f.value, "\n\r") {
			return fmt.Errorf("template data field %s must not contain a newline", f.name)
		}
	}
	return nil
}

// RenderServer validates d and renders the server (k3s server node) bootstrap
// script. It produces the same bytes as ServerTemplate.Execute for valid input.
func RenderServer(d TemplateData) ([]byte, error) {
	if err := ValidateTemplateData(d); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := ServerTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("render server template: %w", err)
	}
	return buf.Bytes(), nil
}

// TemplatesSHA256 returns the hex sha256 fingerprint of the embedded agent,
// server and docker_server templates and all partials, taken in sorted path
// order. It changes whenever a template byte changes, so callers can detect
// that a rendered script is stale.
func TemplatesSHA256() string {
	return hashTemplateFS(allTemplatesFS)
}

// hashTemplateFS hashes every file of fsys in lexical path order. Each file
// contributes its path, a NUL, its length and its content, so moving bytes
// between files changes the digest.
func hashTemplateFS(fsys fs.FS) string {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		// Embedded and in-memory file systems cannot fail here.
		panic(fmt.Sprintf("templates: hash: %v", err))
	}
	return hex.EncodeToString(h.Sum(nil))
}
