package agentkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsWithOthers is a Claude Code settings.json that already has hooks and permissions.
const settingsWithOthers = `{
  "permissions": {
    "allow": ["Bash(go test:*)", "Read(//home/anders/git/**)"],
    "deny": ["Bash(rm -rf:*)"]
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write",
        "hooks": [{"type": "command", "command": "/usr/local/bin/check-write"}]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Edit",
        "hooks": [{"type": "command", "command": "/usr/local/bin/after-edit"}]
      }
    ]
  },
  "model": "opus"
}
`

func settingsPath(t *testing.T, home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// preToolUse reads the PreToolUse entries of a settings.json.
func preToolUse(t *testing.T, path string) []map[string]any {
	t.Helper()
	var v struct {
		PreToolUse []map[string]any `json:"PreToolUse"`
	}
	var top struct {
		Hooks *json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &top); err != nil {
		t.Fatal(err)
	}
	if top.Hooks == nil {
		return nil
	}
	if err := json.Unmarshal(*top.Hooks, &v); err != nil {
		t.Fatal(err)
	}
	return v.PreToolUse
}

func TestHooksMergeAndRerun(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	path := settingsPath(t, home)
	writeSettings(t, path, settingsWithOthers)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(home, ".claude", "hooks", guardName)
	if !hasAction(res, ActionRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	entries := preToolUse(t, path)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse %v", entries)
	}
	// The other entry is untouched; ours names the guard by its absolute path.
	if entries[0]["matcher"] != "Write" {
		t.Errorf("the other entry changed: %v", entries[0])
	}
	if entries[1]["matcher"] != guardMatcher {
		t.Errorf("the guard matcher: %v", entries[1])
	}
	cmd := entries[1]["hooks"].([]any)[0].(map[string]any)["command"]
	if cmd != guard {
		t.Errorf("the guard command %v, want %s", cmd, guard)
	}
	// The rest of the settings file is there.
	var top map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &top); err != nil {
		t.Fatal(err)
	}
	perm := top["permissions"].(map[string]any)
	if len(perm["allow"].([]any)) != 2 || top["model"] != "opus" {
		t.Errorf("foreign keys lost: %v", top)
	}
	if _, ok := top["hooks"].(map[string]any)["PostToolUse"]; !ok {
		t.Errorf("PostToolUse lost: %v", top)
	}
	// A re-run adds no duplicate.
	before := readFile(t, path)
	res, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("a re-run changed settings.json:\n%s", got)
	}
	if !hasAction(res, ActionAlreadyRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if n := len(preToolUse(t, path)); n != 2 {
		t.Errorf("a re-run added a duplicate: %d entries", n)
	}
}

// Without --hooks nothing touches settings.json.
func TestHooksNotAsked(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents = []Agent{AgentClaude}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsPath(t, home)); !os.IsNotExist(err) {
		t.Errorf("settings.json written without --hooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "hooks")); !os.IsNotExist(err) {
		t.Errorf("the guard written without --hooks: %v", err)
	}
}

// Projects scope: .claude/settings.json, already with our entry written by hand at another path: the
// re-run adds a second (different command) only if it is a different guard.
func TestHooksProjectScope(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.Hooks = ScopeProject, []Agent{AgentClaude}, true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".claude", "settings.json")
	guard := filepath.Join(project, ".claude", "hooks", guardName)
	if !strings.Contains(readFile(t, path), `"command": "`+guard+`"`) {
		t.Errorf("settings.json:\n%s", readFile(t, path))
	}
	if st, err := os.Stat(guard); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("guard %v %v", st, err)
	}
}

// codex and opencode get the skills and the MCP server, and a note that hooks are Claude Code only.
func TestHooksOtherAgents(t *testing.T) {
	o, _, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentCodex}, true
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Claude Code only") {
		t.Errorf("warnings %v", res.Warnings)
	}
}

// Uninstall removes our entry and the guard, and leaves the other hooks.
func TestHooksUninstall(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	path := settingsPath(t, home)
	writeSettings(t, path, settingsWithOthers)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	o.MCP, o.Hooks = false, true
	res, err := Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionDelete, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	entries := preToolUse(t, path)
	if len(entries) != 1 || entries[0]["matcher"] != "Write" {
		t.Errorf("PreToolUse after uninstall: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "hooks", guardName)); !os.IsNotExist(err) {
		t.Errorf("the guard is still there: %v", err)
	}
	if strings.Contains(readFile(t, path), "teknoir-guard") {
		t.Errorf("settings.json still names the guard:\n%s", readFile(t, path))
	}
}
