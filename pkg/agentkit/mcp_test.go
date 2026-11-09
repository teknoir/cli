package agentkit

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>, writing it first with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s:\n got: %q\nwant: %q", name, got, string(want))
	}
}

// copyFile puts testdata/<src> at path, creating its directory.
func copyFile(t *testing.T, src, path string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", src))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mcpOpts(t *testing.T, a Agent, scope Scope) (*Options, string, string) {
	t.Helper()
	o, home, project := testOpts(t)
	o.Agents = []Agent{a}
	o.Scope = scope
	o.MCP = true
	return o, home, project
}

func TestMCPClaudeUser(t *testing.T) {
	o, home, _ := mcpOpts(t, AgentClaude, ScopeUser)
	path := filepath.Join(home, ".claude.json")
	copyFile(t, "claude/claude.json", path)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "claude/claude.golden.json", readFile(t, path))
	// The other servers and keys survive, and the entry is the kit's.
	var v struct {
		NumStartups int
		Projects    map[string]struct{ AllowedTools []string }
		MCPServers  map[string]map[string]any
		OAuth       map[string]any `json:"oauthAccount"`
	}
	if err := jsonUnmarshal(t, readFile(t, path), &v); err != nil {
		t.Fatal(err)
	}
	if v.NumStartups != 42 || v.Projects["/home/anders/git/ai"].AllowedTools[0] != "Bash(go test:*)" {
		t.Errorf("a foreign key was lost: %+v", v)
	}
	if v.MCPServers["github"]["url"] != "https://api.githubcopilot.com/mcp/" {
		t.Errorf("another MCP server was lost: %+v", v.MCPServers)
	}
	if v.MCPServers["teknoir"]["command"] != "tnctl" || v.OAuth["emailAddress"] != "anders@example.com" {
		t.Errorf("the entry: %+v", v.MCPServers)
	}
	// The file keeps its mode, and a re-run changes nothing.
	if after, err := os.Stat(path); err != nil || after.Mode() != st.Mode() {
		t.Errorf("mode %v %v", after.Mode(), err)
	}
	before := readFile(t, path)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("a second install changed the file:\n%s", got)
	}
	if !hasAction(res, ActionAlreadyRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
}

func TestMCPClaudeProjectMissing(t *testing.T) {
	o, _, project := mcpOpts(t, AgentClaude, ScopeProject)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".mcp.json")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %04o, want 0644", st.Mode().Perm())
	}
	golden(t, "claude/mcp.golden.json", readFile(t, path))
}

func TestMCPCodex(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	copyFile(t, "codex/config.toml", path)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/config.golden.toml", readFile(t, path))
	if !hasAction(res, ActionRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	// The comments and the other tables are still there, and the entry is theirs.
	got := readFile(t, path)
	for _, want := range []string{"# Codex configuration", "[mcp_servers.github]", "[profiles.fast]"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was lost:\n%s", want, got)
		}
	}
	// A second run is already registered and changes nothing.
	res, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionAlreadyRegister, path) || readFile(t, path) != got {
		t.Errorf("a second install changed the file: %v", actionsOf(res))
	}
}

func TestMCPCodexConflict(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	copyFile(t, "codex/conflict.toml", path)
	before := readFile(t, path)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("a differing entry was replaced without --force:\n%s", got)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "mcp_servers.teknoir") {
		t.Errorf("no warning: %v", res.Warnings)
	}
	if !hasAction(res, ActionSkip, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	// --force replaces that table only.
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/conflict.force.golden.toml", readFile(t, path))
	got := readFile(t, path)
	for _, want := range []string{"# A codex config that already names another teknoir server.", "[mcp_servers.github]", "[profiles.fast]"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was lost:\n%s", want, got)
		}
	}
	if strings.Contains(got, "startup_timeout_sec") || strings.Contains(got, "/opt/other/bin/tnctl") {
		t.Errorf("the old table is still there:\n%s", got)
	}
}

func TestMCPOpencodeJSON(t *testing.T) {
	o, home, _ := mcpOpts(t, AgentOpencode, ScopeUser)
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	copyFile(t, "opencode/opencode.json", path)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "opencode/opencode.golden.json", readFile(t, path))
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.jsonc")); !os.IsNotExist(err) {
		t.Errorf("a .jsonc was created next to the .json: %v", err)
	}
}

func TestMCPOpencodeJSONCPlain(t *testing.T) {
	o, _, project := mcpOpts(t, AgentOpencode, ScopeProject)
	path := filepath.Join(project, "opencode.jsonc")
	copyFile(t, "opencode/plain.jsonc", path)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "opencode/plain.golden.jsonc", readFile(t, path))
	if _, err := os.Stat(filepath.Join(project, "opencode.json")); !os.IsNotExist(err) {
		t.Errorf("a .json was created next to the .jsonc: %v", err)
	}
}

func TestMCPOpencodeJSONCComments(t *testing.T) {
	o, _, project := mcpOpts(t, AgentOpencode, ScopeProject)
	path := filepath.Join(project, "opencode.jsonc")
	copyFile(t, "opencode/comments.jsonc", path)
	before := readFile(t, path)
	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("the file with comments was edited:\n%s", got)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, path) || !strings.Contains(joined, opencodeSnippet) {
		t.Errorf("the warnings name no file and snippet: %v", res.Warnings)
	}
}

func TestMCPOpencodeBothFiles(t *testing.T) {
	o, _, project := mcpOpts(t, AgentOpencode, ScopeProject)
	copyFile(t, "opencode/both/opencode.json", filepath.Join(project, "opencode.json"))
	copyFile(t, "opencode/both/opencode.jsonc", filepath.Join(project, "opencode.jsonc"))
	jsonBefore := readFile(t, filepath.Join(project, "opencode.json"))
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	// Only the .jsonc is considered.
	golden(t, "opencode/both.golden.jsonc", readFile(t, filepath.Join(project, "opencode.jsonc")))
	if got := readFile(t, filepath.Join(project, "opencode.json")); got != jsonBefore {
		t.Errorf("the .json next to the .jsonc was edited:\n%s", got)
	}
}

func jsonUnmarshal(t *testing.T, s string, v any) error {
	t.Helper()
	return json.Unmarshal([]byte(s), v)
}

// The quoted table header [mcp_servers."teknoir"] is the same table.
func TestMCPCodexQuotedHeader(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	body := "model = \"gpt-5-codex\"\n\n[mcp_servers.\"teknoir\"]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\n" + codexEnvLine + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionAlreadyRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := readFile(t, path); got != body {
		t.Errorf("the file changed:\n%s", got)
	}
	// With --force the same table is rewritten, at the same place.
	o.Force = true
	body = "model = \"gpt-5-codex\"\n\n[mcp_servers.\"teknoir\"]\ncommand = \"/other/tnctl\"\nargs = [\"mcp\"]\n\n[profiles.fast]\nmodel = \"x\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "[mcp_servers.teknoir]\ncommand = \"tnctl\"") || strings.Contains(got, "/other/tnctl") {
		t.Errorf("the quoted table was not replaced:\n%s", got)
	}
	if !strings.Contains(got, "[profiles.fast]") {
		t.Errorf("the next table was lost:\n%s", got)
	}
}

// A codex config that does not exist yet is created with just the entry.
func TestMCPCodexMissingFile(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), tomlEntryBlock; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Uninstall removes the entry and leaves the (now empty) file.
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); strings.Contains(got, "teknoir") {
		t.Errorf("uninstall left the entry:\n%s", got)
	}
}

// Codex starts a stdio MCP server with a cleared environment, so the kit's entry must pass the
// variables tnctl reads: bot mode's, and the ones of the teamspace and domain a person targets.
func TestMCPCodexEntryPassesTheEnvironment(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	parsed, err := tomlParse(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := tomlServer(parsed)
	if !ok {
		t.Fatalf("the entry is not a table: %v", srv)
	}
	got, ok := srv["env_vars"].([]any)
	if !ok {
		t.Fatalf("env_vars %v, want an array", srv["env_vars"])
	}
	var names []string
	for _, v := range got {
		s, _ := v.(string)
		names = append(names, s)
	}
	want := []string{"TEKNOIRBOT_API", "TNCTL_BOT_KEY_FILE", "TEKNOIRBOT_NAMESPACE", "TEKNOIR_DOMAIN", "TNCTL_NAMESPACE", "TNCTL_DOMAIN"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("env_vars %v, want %v", names, want)
	}
}

// An entry the kit wrote before env_vars is a differing entry: a plain run keeps it and says so, and
// --force replaces it with the current one.
func TestMCPCodexOldEntryDiffers(t *testing.T) {
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	old := "model = \"gpt-5\"\n\n[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != old {
		t.Errorf("the old entry was replaced without --force:\n%s", got)
	}
	if !hasAction(res, ActionSkip, path) || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "different") {
		t.Errorf("the old entry was not reported as differing: %v %v", actionsOf(res), res.Warnings)
	}
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, "env_vars") {
		t.Errorf("--force did not replace the old entry:\n%s", got)
	}
}
