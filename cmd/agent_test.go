package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentHome points HOME at a directory of the test's own, and runs the command from another (the
// project directory).
func agentHome(t *testing.T) (home, project string) {
	t.Helper()
	home, project = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return home, project
}

func TestAgentInstallUser(t *testing.T) {
	home, _ := agentHome(t)
	out, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "summary:") {
		t.Errorf("no summary:\n%s", out)
	}
	skill := filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatal(err)
	}
	// A re-run: all keep.
	out, _, err = runCLI(t, nil, "agent", "install", "--agent", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "write ") {
		t.Errorf("a re-run wrote:\n%s", out)
	}
	if !strings.Contains(out, "1 keep") {
		t.Errorf("a re-run:\n%s", out)
	}
}

// -o json prints the actions as an array of {action, path, agent, note}.
func TestAgentInstallJSON(t *testing.T) {
	agentHome(t)
	out, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var actions []struct {
		Action, Path, Agent, Note string
	}
	if err := json.Unmarshal([]byte(out), &actions); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(actions) != 1 || actions[0].Action != "write" || actions[0].Agent != "claude" {
		t.Errorf("actions %+v", actions)
	}
	if !strings.HasSuffix(actions[0].Path, filepath.Join("teknoir-platform", "SKILL.md")) {
		t.Errorf("path %s", actions[0].Path)
	}
}

// A changed file is exit 8, and the rest of the install still runs.
func TestAgentInstallRefusedExit(t *testing.T) {
	home, _ := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude,codex"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude,codex")
	if err == nil {
		t.Fatalf("no error:\n%s", out)
	}
	if code := exitCodeOf(err); code != codeConflict {
		t.Errorf("exit %d, want %d: %v", code, codeConflict, err)
	}
	if got, _ := os.ReadFile(skill); string(got) != "mine\n" {
		t.Errorf("the changed file was overwritten: %q", got)
	}
	// --force overwrites it.
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude", "--force"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(skill); !strings.Contains(string(got), "name: teknoir-platform") {
		t.Errorf("--force did not overwrite: %q", got)
	}
}

func TestAgentInstallFlags(t *testing.T) {
	agentHome(t)
	// An unknown agent is a usage error.
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "vim"); err == nil || exitCodeOf(err) != codeUsage {
		t.Errorf("unknown agent: %v", err)
	}
	// The scopes are mutually exclusive.
	if _, _, err := runCLI(t, nil, "agent", "install", "--user", "--project"); err == nil {
		t.Error("--user --project was accepted")
	}
	// --agents-md needs --project.
	if _, _, err := runCLI(t, nil, "agent", "install", "--agents-md"); err == nil || exitCodeOf(err) != codeUsage {
		t.Errorf("--agents-md: %v", err)
	}
	// --managed with --mcp is refused.
	if _, _, err := runCLI(t, nil, "agent", "install", "--managed", t.TempDir(), "--mcp"); err == nil {
		t.Error("--managed --mcp was accepted")
	}
}

// --project installs below the current directory, --dry-run writes nothing.
func TestAgentInstallProjectDryRun(t *testing.T) {
	_, project := agentHome(t)
	out, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude", "--mcp", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would register") {
		t.Errorf("the plan:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
		t.Errorf("--dry-run wrote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf("--dry-run wrote the MCP file: %v", err)
	}
	if _, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude", "--mcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills", "teknoir-platform", "SKILL.md")); err != nil {
		t.Error(err)
	}
	if b, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(b), `"tnctl"`) {
		t.Errorf(".mcp.json: %s %v", b, err)
	}
}

func TestAgentList(t *testing.T) {
	home, _ := agentHome(t)
	out, _, err := runCLI(t, nil, "agent", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no agent kit install found") {
		t.Errorf("an empty home:\n%s", out)
	}
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, nil, "agent", "list", "--user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(home, ".claude", "skills")) || !strings.Contains(out, "ok") {
		t.Errorf("list:\n%s", out)
	}
	// --check is 0 on a clean install.
	if _, _, err := runCLI(t, nil, "agent", "list", "--check", "--user"); err != nil {
		t.Errorf("--check on a clean install: %v", err)
	}
	// A changed file makes it non-zero.
	skill := filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, nil, "agent", "list", "--check", "--user")
	if err == nil || exitCodeOf(err) != codeRejected {
		t.Errorf("--check with a changed file: %v (out %s)", err, out)
	}
	if !strings.Contains(out, "1 changed") {
		t.Errorf("list:\n%s", out)
	}
	// A missing file too.
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, nil, "agent", "list", "--check", "--user")
	if err == nil || !strings.Contains(out, "1 missing") {
		t.Errorf("--check with a missing file: %v (out %s)", err, out)
	}
	// -o json prints the roots.
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, nil, "agent", "list", "--user", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Roots []struct {
			Root, KitVersion, EmbeddedVersion string
		}
		MCP []struct {
			Path       string
			Registered bool
		}
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(v.Roots) != 1 || v.Roots[0].KitVersion != v.Roots[0].EmbeddedVersion {
		t.Errorf("roots %+v", v.Roots)
	}
}

// `agent list` with no scope flag visits both scopes; `agent install` without --agent installs all.
func TestAgentListBothScopes(t *testing.T) {
	home, project := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md"),
		filepath.Join(home, ".agents", "skills", "teknoir-platform", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, _, err := runCLI(t, nil, "agent", "install", "--project"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(project, ".claude", "skills"),
		filepath.Join(project, ".agents", "skills"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list does not name %s:\n%s", want, out)
		}
	}
	if strings.Count(out, filepath.Join(project, ".agents", "skills")) != 1 {
		t.Errorf("a root is listed twice:\n%s", out)
	}
}

func TestAgentUninstall(t *testing.T) {
	home, _ := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "uninstall", "--agent", "claude", "--user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("not removed: %v", err)
	}
	if !strings.Contains(out, "delete") {
		t.Errorf("uninstall:\n%s", out)
	}
}

// A bot may install the kit: it is a file operation, not a fleet call. (The bot's refusals are by
// command path; "agent" is not one of them.)
func TestAgentAllowedInBotMode(t *testing.T) {
	t.Setenv(envBotAPI, "https://bot.example.com")
	t.Setenv(envBotKeyFile, "/nonexistent/key")
	t.Setenv(envBotNamespace, "teknoir-demo")
	agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "list", "--user"); err != nil {
		t.Fatalf("agent list in bot mode: %v", err)
	}
}
