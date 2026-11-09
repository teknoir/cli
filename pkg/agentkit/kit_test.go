package agentkit

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	kit "github.com/teknoir/cli/agentkit"
)

// The embedded kit holds what an install needs, and its manifests are well formed.
func TestEmbeddedKit(t *testing.T) {
	for _, name := range []string{
		"skills/teknoir-platform/SKILL.md",
		"hooks/teknoir-guard",
		".claude-plugin/plugin.json",
		".mcp.json",
		"AGENTS.snippet.md",
	} {
		if _, err := fs.Stat(kit.FS, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// README.md documents the kit and is not installed.
	if _, err := fs.Stat(kit.FS, "README.md"); err == nil {
		t.Error("README.md is embedded")
	}

	var plugin map[string]any
	if err := json.Unmarshal(kitFile(t, ".claude-plugin/plugin.json"), &plugin); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	if plugin["name"] != "teknoir" || plugin["version"] != kit.Version || plugin["license"] != "Apache-2.0" {
		t.Errorf("plugin.json: %v", plugin)
	}
	if _, ok := plugin["hooks"]; ok {
		t.Error("plugin.json has a hooks key: an executable hook must be installed explicitly")
	}
	if _, err := fs.Stat(kit.FS, ".claude-plugin/hooks/hooks.json"); err == nil {
		t.Error("the plugin carries hooks/hooks.json")
	}

	var mcp struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(kitFile(t, ".mcp.json"), &mcp); err != nil {
		t.Fatalf(".mcp.json: %v", err)
	}
	if s := mcp.MCPServers["teknoir"]; s.Command != "tnctl" || len(s.Args) != 1 || s.Args[0] != "mcp" {
		t.Errorf(".mcp.json: %+v", mcp)
	}

	// The guard is a script that drains its input and allows.
	guard := string(kitFile(t, "hooks/teknoir-guard"))
	if !strings.HasPrefix(guard, "#!/bin/sh\n") || !strings.Contains(guard, "cat >/dev/null") || !strings.HasSuffix(guard, "exit 0\n") {
		t.Errorf("the guard:\n%s", guard)
	}
}

// Every SKILL.md's frontmatter has exactly the keys the kit promises, and names the kit version.
func TestSkillFrontmatter(t *testing.T) {
	dirs, err := fs.ReadDir(kit.FS, "skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no skills")
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		t.Run(d.Name(), func(t *testing.T) {
			body := string(kitFile(t, "skills/"+d.Name()+"/SKILL.md"))
			if !strings.HasPrefix(body, "---\n") {
				t.Fatal("no frontmatter")
			}
			end := strings.Index(body[4:], "\n---\n")
			if end < 0 {
				t.Fatal("no end of the frontmatter")
			}
			fm := body[4 : 4+end]
			// It must parse as YAML: an unquoted value with ": " in it does not.
			var meta map[string]any
			if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
				t.Fatalf("the frontmatter is not YAML: %v\n%s", err, fm)
			}
			want := []string{"description", "license", "metadata", "name"} // sorted
			if got := sortedKeys(meta); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("frontmatter keys %v, want %v", got, want)
			}
			if sub, ok := meta["metadata"].(map[string]any); !ok || len(sub) != 1 {
				t.Errorf("metadata %v, want only teknoir-kit-version", meta["metadata"])
			} else if _, ok := sub["teknoir-kit-version"]; !ok {
				t.Errorf("metadata %v, want teknoir-kit-version", sub)
			}
			if meta["name"] != d.Name() {
				t.Errorf("the skill is not named after its directory: %v\n%s", meta["name"], fm)
			}
			if !strings.Contains(fm, `teknoir-kit-version: "`+kit.Version+`"`) {
				t.Errorf("the kit version:\n%s", fm)
			}
			if !strings.Contains(fm, "license: Apache-2.0") {
				t.Errorf("the license:\n%s", fm)
			}
		})
	}
	// The entry skill names the safe start and the credentials rule.
	body := string(kitFile(t, "skills/teknoir-platform/SKILL.md"))
	for _, must := range []string{"tnctl whoami", "credentials"} {
		if !strings.Contains(body, must) {
			t.Errorf("teknoir-platform SKILL.md does not mention %q", must)
		}
	}
}

// The installer puts AGENTS.snippet.md between its own markers: a marker in the snippet would make two
// sections, and the next install would refuse AGENTS.md.
func TestAgentsSnippetHasNoMarkers(t *testing.T) {
	snippet := string(kitFile(t, "AGENTS.snippet.md"))
	if strings.Contains(snippet, "teknoir-agent-kit:") {
		t.Errorf("AGENTS.snippet.md carries a kit marker:\n%s", snippet)
	}
	if strings.TrimSpace(snippet) == "" {
		t.Error("AGENTS.snippet.md is empty")
	}
}

// The repository's marketplace file offers the kit as ./agentkit.
func TestMarketplace(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Skipf("no marketplace.json in the repository: %v", err)
	}
	var m struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []struct {
			Name        string `json:"name"`
			Source      string `json:"source"`
			Description string `json:"description"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("marketplace.json: %v", err)
	}
	if m.Name != "teknoir-agent-kit" || m.Owner.Name != "Teknoir" {
		t.Errorf("marketplace.json: %+v", m)
	}
	if len(m.Plugins) != 1 || m.Plugins[0].Name != "teknoir" || m.Plugins[0].Source != "./agentkit" {
		t.Errorf("plugins: %+v", m.Plugins)
	}
	var plugin map[string]any
	if err := json.Unmarshal(kitFile(t, ".claude-plugin/plugin.json"), &plugin); err != nil {
		t.Fatal(err)
	}
	if m.Plugins[0].Description != plugin["description"] {
		t.Errorf("the marketplace description differs from the plugin's")
	}
}

// The skill directory is named after the skill, and no skill carries an unknown file.
func TestSkillDirectories(t *testing.T) {
	dirs, err := fs.ReadDir(kit.FS, "skills")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`(?m)^name: (\S+)$`)
	for _, d := range dirs {
		if !d.IsDir() {
			t.Errorf("skills/%s is not a directory", d.Name())
			continue
		}
		body := string(kitFile(t, "skills/"+d.Name()+"/SKILL.md"))
		if m := name.FindStringSubmatch(body); m == nil || m[1] != d.Name() {
			t.Errorf("skills/%s/SKILL.md: name %v", d.Name(), m)
		}
	}
}
