package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kit "github.com/teknoir/cli/agentkit"
)

// agentHome points HOME at a directory of the test's own, and runs the command from another (the
// project directory).
func agentHome(t *testing.T) (home, project string) {
	t.Helper()
	home, project = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The user-scope config directories: a test never installs into (or deletes from) the real one of
	// the developer running it.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
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
	if want := fmt.Sprintf("%d keep", kitSkillFiles(t)); !strings.Contains(out, want) {
		t.Errorf("a re-run does not say %q:\n%s", want, out)
	}
}

// kitSkillFiles is the number of files below the embedded kit's skills/.
func kitSkillFiles(t *testing.T) int {
	t.Helper()
	n := 0
	err := fs.WalkDir(kit.FS, "skills", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
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
	if len(actions) != kitSkillFiles(t) {
		t.Errorf("%d actions, want one per kit file (%d): %+v", len(actions), kitSkillFiles(t), actions)
	}
	entry := false
	for _, a := range actions {
		if a.Action != "write" || a.Agent != "claude" {
			t.Errorf("action %+v", a)
		}
		entry = entry || strings.HasSuffix(a.Path, filepath.Join("teknoir-platform", "SKILL.md"))
	}
	if !entry {
		t.Errorf("no write of teknoir-platform/SKILL.md: %+v", actions)
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
	// The scopes are mutually exclusive: a usage error, exit 2.
	for _, scopes := range [][]string{
		{"--user", "--project"},
		{"--user", "--managed", t.TempDir()},
		{"--project", "--managed", t.TempDir()},
	} {
		args := append([]string{"agent", "install"}, scopes...)
		if _, _, err := runCLI(t, nil, args...); err == nil || exitCodeOf(err) != codeUsage {
			t.Errorf("%v: %v, want exit %d", scopes, err, codeUsage)
		}
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

// --check with nothing installed exits 5 in the user scope, the project scope and both.
func TestAgentListCheckNothingInstalled(t *testing.T) {
	agentHome(t)
	for _, args := range [][]string{
		{"agent", "list", "--check"},
		{"agent", "list", "--user", "--check"},
		{"agent", "list", "--project", "--check"},
	} {
		out, _, err := runCLI(t, nil, args...)
		if err == nil || exitCodeOf(err) != codeRejected {
			t.Errorf("%v: %v, want exit %d", args, err, codeRejected)
		}
		if !strings.Contains(out, "no agent kit install found") {
			t.Errorf("%v:\n%s", args, out)
		}
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

// ---------- where user scope writes: CLAUDE_CONFIG_DIR, CODEX_HOME and XDG_CONFIG_HOME ----------

// One run for each of the three variables: the kit goes where the agent reads it, and nothing goes to
// the default directory.
func TestAgentInstallConfigDirs(t *testing.T) {
	for _, tc := range []struct {
		name, env, agents string
		want              func(cfg, home string) string
	}{
		{"CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR", "claude|--mcp|--hooks",
			func(cfg, _ string) string { return filepath.Join(cfg, "skills", "teknoir-platform", "SKILL.md") }},
		{"CODEX_HOME", "CODEX_HOME", "codex|--mcp",
			func(cfg, _ string) string { return filepath.Join(cfg, "config.toml") }},
		{"XDG_CONFIG_HOME", "XDG_CONFIG_HOME", "opencode|--mcp",
			func(cfg, _ string) string { return filepath.Join(cfg, "opencode", "opencode.json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := agentHome(t)
			cfg := t.TempDir()
			t.Setenv(tc.env, cfg)
			args := []string{"agent", "install", "--agent", strings.Split(tc.agents, "|")[0]}
			args = append(args, strings.Split(tc.agents, "|")[1:]...)
			if _, _, err := runCLI(t, nil, args...); err != nil {
				t.Fatalf("%v", err)
			}
			got := tc.want(cfg, home)
			if _, err := os.Stat(got); err != nil {
				t.Errorf("%s: %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) && tc.env == "CLAUDE_CONFIG_DIR" {
				t.Errorf("the kit went to ~/.claude as well: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) && tc.env == "CLAUDE_CONFIG_DIR" {
				t.Errorf(".claude.json went to ~: %v", err)
			}
			// list sees it there, and an uninstall removes it from there.
			out, _, err := runCLI(t, nil, "agent", "list", "--user")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, cfg) {
				t.Errorf("list does not name %s:\n%s", cfg, out)
			}
			if _, _, err := runCLI(t, nil, "agent", "uninstall", "--user", "--agent", strings.Split(tc.agents, "|")[0], "--mcp"); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if tc.env == "CLAUDE_CONFIG_DIR" {
				// The skills are the kit's files: they go.
				if _, err := os.Stat(got); !os.IsNotExist(err) {
					t.Errorf("%s survived the uninstall: %v", got, err)
				}
			} else {
				// The config file is the user's: the entry goes, the file stays.
				if b, err := os.ReadFile(got); err != nil || strings.Contains(string(b), "teknoir") {
					t.Errorf("%s still names teknoir: %s %v", got, b, err)
				}
			}
		})
	}
}

// An empty variable is unset: the default paths are used.
func TestAgentInstallEmptyConfigDirs(t *testing.T) {
	home, _ := agentHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	unsetEnv(t, "CODEX_HOME")
	unsetEnv(t, "XDG_CONFIG_HOME")
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude,codex,opencode", "--mcp"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".config", "opencode", "opencode.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// CLAUDE_CONFIG_DIR holds everything of Claude Code's: the skills, the guard, settings.json and its
// .claude.json, and nothing goes to the home directory.
func TestAgentClaudeConfigDirEverything(t *testing.T) {
	home, _ := agentHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude", "--mcp", "--hooks"); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(cfg, "hooks", "teknoir-guard")
	if _, err := os.Stat(guard); err != nil {
		t.Errorf("the guard: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(cfg, "settings.json")); err != nil || !strings.Contains(string(b), guard) {
		t.Errorf("settings.json: %s %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(cfg, ".claude.json")); err != nil || !strings.Contains(string(b), `"teknoir"`) {
		t.Errorf(".claude.json: %s %v", b, err)
	}
	for _, p := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was written: %v", p, err)
		}
	}
}

// No variable set: every agent's files go to the default paths below HOME.
func TestAgentAllConfigDirsEmpty(t *testing.T) {
	home, project := agentHome(t) // all three are ""
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude,codex,opencode", "--mcp", "--hooks"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".claude.json"), filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".config", "opencode", "opencode.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Errorf("a user-scope install wrote into the current directory: %v", entries)
	}
}

// Project scope ignores all three variables: the config files are the project's own.
func TestAgentProjectIgnoresConfigDirs(t *testing.T) {
	_, project := agentHome(t)
	dirs := map[string]string{}
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		dirs[v] = t.TempDir()
		t.Setenv(v, dirs[v])
	}
	if _, _, err := runCLI(t, nil, "agent", "install", "--project", "--mcp", "--hooks"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".claude/settings.json", ".mcp.json", ".codex/config.toml", "opencode.json"} {
		if _, err := os.Stat(filepath.Join(project, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for v, d := range dirs {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Errorf("project scope wrote into $%s: %v", v, entries)
		}
	}
}

// unsetEnv makes a variable unset for the test.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	os.Unsetenv(name)
}

// A project that commits .claude as a symlink: install and uninstall exit 8 and change nothing.
func TestAgentProjectSymlinkExit(t *testing.T) {
	for _, op := range []string{"install", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			agentHome(t)
			project, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(project, ".agents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(project, ".claude")); err != nil {
				t.Fatal(err)
			}
			out, _, err := runCLI(t, nil, "agent", op, "--project")
			if err == nil || exitCodeOf(err) != codeConflict {
				t.Fatalf("%s: exit %v, want %d: %v\n%s", op, err, codeConflict, err, out)
			}
			if !strings.Contains(err.Error(), filepath.Join(project, ".claude")+" is a symlink") {
				t.Errorf("the error does not name the link: %v", err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Errorf("the link's target was written to: %v %v", entries, err)
			}
			if fi, err := os.Lstat(filepath.Join(project, ".claude")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link is gone: %v %v", fi, err)
			}
		})
	}
}

// agent list reports a symlinked project root as its state, and exits 0 without --check.
func TestAgentListProjectSymlink(t *testing.T) {
	agentHome(t)
	project, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project, ".claude")); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "list", "--project")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "is a symlink") {
		t.Errorf("list:\n%s", out)
	}
	if _, _, err := runCLI(t, nil, "agent", "list", "--project", "--check"); err == nil || exitCodeOf(err) != codeRejected {
		t.Errorf("--check: %v, want exit %d", err, codeRejected)
	}
}

// agent list reports a manifest with a key outside its root as the root's state, and does not fail.
func TestAgentListManifestProblem(t *testing.T) {
	home, _ := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--agent", "claude"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".claude", "skills")
	mf := filepath.Join(root, ".tnctl-agent-kit.json")
	body := `{"kitVersion":"0.1.0","tnctl":"test","files":{"../x":"sha256:00"}}`
	if err := os.WriteFile(mf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "list", "--user")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, `is not a path inside its root`) {
		t.Errorf("list:\n%s", out)
	}
	if _, _, err := runCLI(t, nil, "agent", "list", "--user", "--check"); err == nil || exitCodeOf(err) != codeRejected {
		t.Errorf("--check: %v, want exit %d", err, codeRejected)
	}
}

// A changed kit file and a commented .jsonc are both refused in one run: the error names both, the
// other agents' files are still written, and the exit code is 8.
func TestAgentInstallTwoRefusals(t *testing.T) {
	_, project := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(project, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonc := filepath.Join(project, "opencode.jsonc")
	if err := os.WriteFile(jsonc, []byte("{\n  // my comment\n  \"model\": \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp")
	if err == nil || exitCodeOf(err) != codeConflict {
		t.Fatalf("exit %v, want %d:\n%s", err, codeConflict, out)
	}
	msg := err.Error()
	for _, want := range []string{skill, jsonc} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %s: %v", want, msg)
		}
	}
	if got, _ := os.ReadFile(skill); string(got) != "mine\n" {
		t.Errorf("the changed kit file was written: %q", got)
	}
	if got, _ := os.ReadFile(jsonc); !strings.Contains(string(got), "// my comment") || strings.Contains(string(got), "teknoir") {
		t.Errorf("the commented .jsonc was edited:\n%s", got)
	}
	// Another agent's file of the same run is still written.
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "teknoir-platform", "SKILL.md")); err != nil {
		t.Errorf("the rest of the run did not write: %v", err)
	}
}

// Two refusals in one run, one for each agent, and the run goes on: what the kit writes after the first
// refused file (the shared skills root and the MCP file) is written after the refusals too.
func TestAgentInstallTwoRefusalsRunGoesOn(t *testing.T) {
	_, project := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(project, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.jsonc"), []byte("{\n  // my comment\n  \"model\": \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove what the first run wrote after the refused file, so the checks below see the second run.
	if err := os.RemoveAll(filepath.Join(project, ".agents")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(project, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp")
	if err == nil || exitCodeOf(err) != codeConflict {
		t.Fatalf("exit %v, want %d", err, codeConflict)
	}
	for _, p := range []string{
		filepath.Join(project, ".agents", "skills", "teknoir-platform", "SKILL.md"),
		filepath.Join(project, ".agents", "skills", ".tnctl-agent-kit.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the run stopped after the refusal: %v", err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(b), `"teknoir"`) {
		t.Errorf(".mcp.json was not registered after the refusal: %s %v", b, err)
	}
}

// A dry run over the same two refusals says "would refuse" and writes nothing.
func TestAgentInstallRefusalsDryRun(t *testing.T) {
	_, project := agentHome(t)
	if _, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(project, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonc := filepath.Join(project, "opencode.jsonc")
	if err := os.WriteFile(jsonc, []byte("{\n  // my comment\n  \"model\": \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "install", "--project", "--agent", "claude,opencode", "--mcp", "--dry-run")
	if err == nil || exitCodeOf(err) != codeConflict {
		t.Fatalf("exit %v, want %d:\n%s", err, codeConflict, out)
	}
	if strings.Count(out, "would refuse") < 2 {
		t.Errorf("the dry run does not report both refusals:\n%s", out)
	}
}

// `agent list --managed DIR --check` on a directory nothing was installed into is a failure: the image
// test must not pass on an install that never happened.
func TestAgentListManagedEmptyCheck(t *testing.T) {
	agentHome(t)
	dir := t.TempDir()
	out, _, err := runCLI(t, nil, "agent", "list", "--managed", dir)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("list does not say the roots are not installed:\n%s", out)
	}
	if _, _, err := runCLI(t, nil, "agent", "list", "--managed", dir, "--check"); err == nil || exitCodeOf(err) != codeRejected {
		t.Errorf("--check on an empty managed dir: %v, want exit %d", err, codeRejected)
	}
	// The same on a directory that does not exist at all.
	if _, _, err := runCLI(t, nil, "agent", "list", "--managed", filepath.Join(dir, "missing"), "--check"); err == nil || exitCodeOf(err) != codeRejected {
		t.Errorf("--check on a missing managed dir: %v, want exit %d", err, codeRejected)
	}
}

// A managed install without the guard fails --check: the image always gets the guard, and the test says
// so when it is gone.
func TestAgentListManagedMissingGuard(t *testing.T) {
	agentHome(t)
	dir := t.TempDir()
	if _, _, err := runCLI(t, nil, "agent", "install", "--managed", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, nil, "agent", "list", "--managed", dir, "--check"); err != nil {
		t.Errorf("--check on a clean managed install: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "hooks", "teknoir-guard")); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "agent", "list", "--managed", dir, "--check")
	if err == nil || exitCodeOf(err) != codeRejected {
		t.Fatalf("--check without the guard: %v, want exit %d", err, codeRejected)
	}
	if !strings.Contains(out, "1 missing") {
		t.Errorf("list:\n%s", out)
	}
}

// -o json keeps stdout a parseable array, and the managed settings snippet goes to stderr with the other
// warnings.
func TestAgentInstallManagedJSONWarnings(t *testing.T) {
	agentHome(t)
	dir := t.TempDir()
	out, errs, err := runCLI(t, nil, "agent", "install", "--managed", dir, "--hooks", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errs)
	}
	var actions []struct {
		Action, Path, Agent, Note string
	}
	if err := json.Unmarshal([]byte(out), &actions); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	found := false
	for _, a := range actions {
		if strings.Contains(a.Note, "settings entry to add by hand") && strings.Contains(a.Note, "PreToolUse") {
			found = true
		}
	}
	if !found {
		t.Errorf("no action carries the snippet: %s", out)
	}
	if !strings.Contains(errs, "warning: ") || !strings.Contains(errs, "managed-settings") {
		t.Errorf("stderr holds no warning:\n%s", errs)
	}
	if strings.Contains(out, "warning:") {
		t.Errorf("a warning went to stdout:\n%s", out)
	}
}

// The docs' synopsis of uninstall names --agents-md, as the flag exists.
func TestAgentCommandsSynopsis(t *testing.T) {
	agentHome(t)
	out, _, err := runCLI(t, nil, "agent", "uninstall", "--help")
	if err != nil {
		t.Fatal(err)
	}
	// The usage line holds the synopsis agentkit/README.md quotes.
	if !strings.Contains(out, "Usage:\n  tnctl agent uninstall [--agent ...] [--user|--project|--managed DIR] [--mcp] [--hooks] [--agents-md]") {
		t.Errorf("the uninstall synopsis does not name --agents-md:\n%s", out)
	}
	out, _, err = runCLI(t, nil, "agent", "list", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Only the user scope (default: both scopes)") {
		t.Errorf("the --user help does not say what list visits:\n%s", out)
	}
}
