package templates

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".golden.sh")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden_Execute(t *testing.T) {
	tests := []struct {
		name string
		exec func(*bytes.Buffer) error
	}{
		{"server", func(b *bytes.Buffer) error { return ServerTemplate.Execute(b, goldenData()) }},
		{"agent", func(b *bytes.Buffer) error { return AgentTemplate.Execute(b, goldenData()) }},
		{"docker_server", func(b *bytes.Buffer) error { return DockerServerTemplate.Execute(b, goldenData()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tt.exec(&buf); err != nil {
				t.Fatal(err)
			}
			if want := readGolden(t, tt.name); !bytes.Equal(buf.Bytes(), want) {
				t.Fatalf("%s output differs from testdata/%s.golden.sh (got %d bytes, want %d)", tt.name, tt.name, buf.Len(), len(want))
			}
		})
	}
}

func TestRenderServer_EqualsGolden(t *testing.T) {
	got, err := RenderServer(goldenData())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, readGolden(t, "server")) {
		t.Fatal("RenderServer output differs from testdata/server.golden.sh")
	}
}

func TestValidateTemplateData_AcceptsGolden(t *testing.T) {
	if err := ValidateTemplateData(goldenData()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTemplateData_Rejects(t *testing.T) {
	const secret = "SECRETVALUE"
	setters := map[string]func(*TemplateData, string){
		"Domain":       func(d *TemplateData, v string) { d.Domain = v },
		"Namespace":    func(d *TemplateData, v string) { d.Namespace = v },
		"DeviceID":     func(d *TemplateData, v string) { d.DeviceID = v },
		"RSAPrivate":   func(d *TemplateData, v string) { d.RSAPrivate = v },
		"RSAPublic":    func(d *TemplateData, v string) { d.RSAPublic = v },
		"Username":     func(d *TemplateData, v string) { d.Username = v },
		"Password":     func(d *TemplateData, v string) { d.Password = v },
		"PublicSSHKey": func(d *TemplateData, v string) { d.PublicSSHKey = v },
		"K3SToken":     func(d *TemplateData, v string) { d.K3SToken = v },
		"DockerSecret": func(d *TemplateData, v string) { d.DockerSecret = v },
	}
	multiline := map[string]bool{"RSAPrivate": true, "RSAPublic": true}
	chars := map[string]string{"double quote": `"`, "backtick": "`", "dollar": "$", "backslash": `\`, "newline": "\n"}

	for fname, set := range setters {
		for cname, c := range chars {
			t.Run(fname+"/"+cname, func(t *testing.T) {
				d := goldenData()
				set(&d, "pre"+secret+c+secret+"post")
				err := ValidateTemplateData(d)
				if c == "\n" && multiline[fname] {
					if err != nil {
						t.Fatalf("newline must be allowed in %s: %v", fname, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("expected %s to reject %s", fname, cname)
				}
				if !strings.Contains(err.Error(), fname) {
					t.Errorf("error %q does not name field %s", err, fname)
				}
				for _, leak := range []string{secret, c + secret, "pre", "post"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("error %q leaks input fragment %q", err, leak)
					}
				}
				if _, rerr := RenderServer(d); rerr == nil {
					t.Error("RenderServer accepted unsafe data")
				}
			})
		}
	}
}

func TestTemplatesSHA256(t *testing.T) {
	a, b := TemplatesSHA256(), TemplatesSHA256()
	if a != b || len(a) != 64 {
		t.Fatalf("not deterministic or not hex sha256: %q %q", a, b)
	}
}

func TestHashTemplateFS_ChangesWithOneByte(t *testing.T) {
	fsys := fstest.MapFS{
		"agent.sh.tmpl":         {Data: []byte("agent")},
		"server.sh.tmpl":        {Data: []byte("server")},
		"docker_server.sh.tmpl": {Data: []byte("docker")},
		"partials/a.sh.tmpl":    {Data: []byte("partial-a")},
		"partials/b.sh.tmpl":    {Data: []byte("partial-b")},
	}
	base := hashTemplateFS(fsys)
	if base != hashTemplateFS(fsys) {
		t.Fatal("not deterministic")
	}
	for path := range fsys {
		mod := fstest.MapFS{}
		for k, v := range fsys {
			mod[k] = &fstest.MapFile{Data: append([]byte(nil), v.Data...)}
		}
		mod[path].Data[0] ^= 1
		if hashTemplateFS(mod) == base {
			t.Errorf("changing a byte of %s did not change the hash", path)
		}
	}
}

func TestTemplatesSHA256_MatchesEmbeddedFS(t *testing.T) {
	// The embedded FS must contain exactly the templates that are parsed.
	for _, p := range []string{"agent.sh.tmpl", "server.sh.tmpl", "docker_server.sh.tmpl", "partials/header.sh.tmpl"} {
		if _, err := allTemplatesFS.ReadFile(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}
