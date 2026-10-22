package templates

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
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

// loginNameRe is the device login name the scripts accept: a lowercase POSIX
// login name (Debian's default NAME_REGEX, at most 32 characters). It keeps
// sudoers keywords such as ALL and names with '.' (which sudo's includedir
// skips) out of the sudoers drop-in.
var loginNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

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
		{"RegistryMirror", d.RegistryMirror, false},
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
	if err := validateRegistry(d); err != nil {
		return err
	}
	if d.Username != "" && (!loginNameRe.MatchString(d.Username) || d.Username == "root") {
		return fmt.Errorf("template data field Username must be a lowercase login name (a-z, 0-9, _ and -, starting with a letter or _, at most 32 characters, not root)")
	}
	return nil
}

// validateRegistry checks the registry mirror and its credential. The credential is embedded in
// single quotes (see partials/registries.sh.tmpl), so a `$` is fine; a single quote, a backslash, a
// double quote (it ends up inside a double-quoted YAML scalar after escaping, but the shell variable
// is the place to refuse), whitespace and control characters are not. Errors never contain the value.
func validateRegistry(d TemplateData) error {
	if ValidateRegistryMirror(d.RegistryMirror) != nil {
		return fmt.Errorf("template data field RegistryMirror must be a host name (optionally with :port) or %q", RegistryMirrorNone)
	}
	if d.RegistryMirror == "" || d.RegistryMirror == RegistryMirrorNone {
		if d.RegistryUsername != "" || d.RegistryPassword != "" {
			return fmt.Errorf("template data fields RegistryUsername and RegistryPassword need a RegistryMirror host")
		}
		return nil
	}
	if d.RegistryUsername == "" || d.RegistryPassword == "" {
		return fmt.Errorf("template data fields RegistryUsername and RegistryPassword must both be set for RegistryMirror %s", d.RegistryMirror)
	}
	for _, f := range []field{{"RegistryUsername", d.RegistryUsername, false}, {"RegistryPassword", d.RegistryPassword, false}} {
		if len(f.value) > 512 {
			return fmt.Errorf("template data field %s is longer than 512 characters", f.name)
		}
		for _, r := range f.value {
			if r == '\'' || r == '"' || r == '\\' || r == '`' || r <= ' ' || r == 0x7f {
				return fmt.Errorf("template data field %s contains a character that is unsafe in a shell script (quote, backtick, backslash, whitespace or control character)", f.name)
			}
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
