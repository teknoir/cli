package agentkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// settingsWithOthers is a Claude Code settings.json that already has hooks and permissions. Its values
// hold &, < and >, which a rewrite must not escape.
const settingsWithOthers = `{
  "permissions": {
    "allow": ["Bash(go test:*)", "Read(//home/anders/git/**)"],
    "deny": ["Bash(rm -rf:* && echo a > b)"]
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

// preToolUseRaw reads the PreToolUse groups of a settings.json, each compact.
func preToolUseRaw(t *testing.T, path string) []string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, path)), &top); err != nil {
		t.Fatal(err)
	}
	var hooks map[string]json.RawMessage
	if raw, ok := top["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			t.Fatal(err)
		}
	}
	var groups []json.RawMessage
	if raw, ok := hooks["PreToolUse"]; ok {
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for _, g := range groups {
		var b bytes.Buffer
		if err := json.Compact(&b, g); err != nil {
			t.Fatal(err)
		}
		out = append(out, b.String())
	}
	return out
}

// groupHandlerCommands are the command handlers of one compact PreToolUse group.
func groupHandlerCommands(t *testing.T, group string) []string {
	t.Helper()
	var g struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(group), &g); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range g.Hooks {
		out = append(out, h.Command)
	}
	return out
}

// groupMatcher is the matcher of one compact PreToolUse group.
func groupMatcher(t *testing.T, group string) string {
	t.Helper()
	var g struct {
		Matcher string `json:"matcher"`
	}
	if err := json.Unmarshal([]byte(group), &g); err != nil {
		t.Fatal(err)
	}
	return g.Matcher
}

// guardHandlers counts the handlers that run the guard in a settings.json, and returns them.
func guardHandlers(t *testing.T, path, command string) []string {
	t.Helper()
	var out []string
	for _, g := range preToolUseRaw(t, path) {
		for _, cmd := range groupHandlerCommands(t, g) {
			if cmd == command {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// jsonString is v as a JSON string literal.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := marshalNoEscape(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
	// The other entry is untouched; ours names the guard by its quoted absolute path.
	if entries[0]["matcher"] != "Write" {
		t.Errorf("the other entry changed: %v", entries[0])
	}
	if entries[1]["matcher"] != guardMatcher {
		t.Errorf("the guard matcher: %v", entries[1])
	}
	cmd := entries[1]["hooks"].([]any)[0].(map[string]any)["command"]
	if cmd != shellQuote(guard) {
		t.Errorf("the guard command %v, want %s", cmd, shellQuote(guard))
	}
	// The rest of the settings file is there, its values unescaped.
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
	if got := readFile(t, path); !strings.Contains(got, "Bash(rm -rf:* && echo a > b)") {
		t.Errorf("a foreign value was escaped:\n%s", got)
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

// A home with a space in it: the command is shell-quoted, so the guard runs, a re-run adds no duplicate
// and an uninstall leaves no entry behind.
func TestHooksCommandQuoted(t *testing.T) {
	o, home, _ := testOpts(t)
	home = filepath.Join(home, "John Smith")
	o.Home = home
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	guard := filepath.Join(home, ".claude", "hooks", guardName)
	path := settingsPath(t, home)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if n := len(preToolUse(t, path)); n != 1 {
		t.Fatalf("%d PreToolUse entries, want 1: %v", n, preToolUse(t, path))
	}
	// A second install leaves exactly one entry, and that entry's command reaches the guard through a
	// shell: a stub guard of the same name writes a marker of its own.
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, shellQuote(guard)); len(got) != 1 {
		t.Fatalf("%d handlers run the guard, want 1: %v", len(got), got)
	}
	marker := filepath.Join(home, "marker")
	writeAt(t, guard, "#!/bin/sh\ncat >/dev/null\ntouch \""+marker+"\"\nexit 0\n", 0o755)
	command := guardHandlers(t, path, shellQuote(guard))[0]
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("sh -c %s: %v: %s", command, err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the shell did not reach the guard with %s: %v", command, err)
	}
	// Uninstall leaves no entry behind.
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, shellQuote(guard)); len(got) != 0 {
		t.Errorf("%d handlers still run the guard: %v", len(got), got)
	}
	if strings.Contains(readFile(t, path), guardName) {
		t.Errorf("settings.json still names the guard:\n%s", readFile(t, path))
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

// Project scope: the committed settings.json names the guard through $CLAUDE_PROJECT_DIR, so it works on
// every machine that clones the repository.
func TestHooksProjectScope(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.Hooks = ScopeProject, []Agent{AgentClaude}, true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".claude", "settings.json")
	guard := filepath.Join(project, ".claude", "hooks", guardName)
	if !strings.Contains(readFile(t, path), `"command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/teknoir-guard"`) {
		t.Errorf("settings.json:\n%s", readFile(t, path))
	}
	if strings.Contains(readFile(t, path), project) {
		t.Errorf("settings.json holds this machine's path:\n%s", readFile(t, path))
	}
	if st, err := os.Stat(guard); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("guard %v %v", st, err)
	}
	// A re-run registers nothing, and an uninstall removes the entry.
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, projectGuardCommand); len(got) != 1 {
		t.Errorf("%d handlers run the guard, want 1", len(got))
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, projectGuardCommand); len(got) != 0 {
		t.Errorf("%d handlers still run the guard", len(got))
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

// Uninstall takes out our handler only: a foreign handler beside it in the same group stays, and so does
// a group that runs another teknoir-guard. Install next to that other guard still adds ours.
func TestHooksUninstallKeepsForeignHandlers(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	guardCmd := shellQuote(filepath.Join(home, ".claude", "hooks", guardName))
	path := settingsPath(t, home)
	foreign := `{"matcher":"Write","hooks":[{"type":"command","command":"/usr/local/bin/check-write"}]}`
	otherGuard := `{"matcher":"Edit","hooks":[{"type":"command","command":"/opt/x/teknoir-guard"}]}`
	lint := "cd /x && make lint 2>&1 >/dev/null"
	mixed := `{"matcher":"` + guardMatcher + `","hooks":[{"type":"command","command":` + jsonString(t, guardCmd) +
		`},{"type":"command","command":` + jsonString(t, lint) + `}]}`
	writeSettings(t, path, `{"hooks":{"PreToolUse":[`+foreign+`,`+otherGuard+`,`+mixed+`]}}`)

	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	groups := preToolUseRaw(t, path)
	if len(groups) != 3 {
		t.Fatalf("%d groups, want 3: %v", len(groups), groups)
	}
	if groups[0] != foreign {
		t.Errorf("the foreign group changed:\n got %s\nwant %s", groups[0], foreign)
	}
	if groups[1] != otherGuard {
		t.Errorf("the other teknoir-guard group changed:\n got %s\nwant %s", groups[1], otherGuard)
	}
	// Our handler is gone from the group, the user's lint hook stays with its unescaped command.
	if cmds := groupHandlerCommands(t, groups[2]); len(cmds) != 1 || cmds[0] != lint {
		t.Errorf("the mixed group after uninstall: %v", cmds)
	}
	if got := readFile(t, path); !strings.Contains(got, lint) {
		t.Errorf("the user's command was escaped or lost:\n%s", got)
	}

	// An install next to the other teknoir-guard adds our group, and warns about that guard.
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, guardCmd); len(got) != 1 {
		t.Errorf("%d handlers run the guard, want 1: %v", len(got), preToolUseRaw(t, path))
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "another teknoir-guard hook is registered: /opt/x/teknoir-guard") {
		t.Errorf("no warning about the other guard: %v", res.Warnings)
	}
	// The other guard and its group are still there, and the lint command is still unescaped.
	other := false
	for _, g := range preToolUseRaw(t, path) {
		if cmds := groupHandlerCommands(t, g); len(cmds) == 1 && cmds[0] == "/opt/x/teknoir-guard" {
			other = true
		}
	}
	if !other {
		t.Errorf("the other teknoir-guard group is gone: %v", preToolUseRaw(t, path))
	}
	if got := readFile(t, path); !strings.Contains(got, lint) {
		t.Errorf("the user's command was escaped or lost:\n%s", got)
	}
}

// A settings.json as the first version of the kit wrote it (the old matcher, the unquoted guard path)
// beside a foreign group: one install leaves exactly one handler, with the new matcher and the quoted
// command, and the foreign group is untouched. A second install changes nothing.
func TestHooksUpgradeLegacyEntry(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	legacyGuard := filepath.Join(home, ".claude", "hooks", guardName)
	path := settingsPath(t, home)
	foreign := `{"matcher":"Write","hooks":[{"type":"command","command":"/usr/local/bin/check-write"}]}`
	legacy := `{"matcher":"` + legacyGuardMatcher + `","hooks":[{"type":"command","command":` + jsonString(t, legacyGuard) + `}]}`
	writeSettings(t, path, `{"hooks":{"PreToolUse":[`+legacy+`,`+foreign+`]}}`)

	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := guardHandlers(t, path, shellQuote(legacyGuard)); len(got) != 1 {
		t.Fatalf("%d handlers run the guard, want 1: %v", len(got), preToolUseRaw(t, path))
	}
	// The legacy handler in the old-matcher group is gone: exactly one handler runs the guard, in either
	// form, and only the foreign group and ours are there.
	if both := guardHandlersEither(t, path, legacyGuard); both != 1 {
		t.Errorf("%d handlers run the guard in either form, want 1: %v", both, preToolUseRaw(t, path))
	}
	if n := len(preToolUseRaw(t, path)); n != 2 {
		t.Errorf("%d groups, want the foreign one and ours: %v", n, preToolUseRaw(t, path))
	}
	// The legacy matcher is the one the kit itself used: the note names the move, but no warning does.
	if note := noteOf(res, ActionRegister, path); !strings.Contains(note, fmt.Sprintf("moved from matcher %q", legacyGuardMatcher)) {
		t.Errorf("the note %q does not name the move from the legacy matcher", note)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, legacyGuardMatcher) {
			t.Errorf("a warning names the legacy matcher: %q", w)
		}
	}
	for _, g := range preToolUseRaw(t, path) {
		for _, cmd := range groupHandlerCommands(t, g) {
			if cmd != shellQuote(legacyGuard) {
				continue
			}
			if got := groupMatcher(t, g); got != guardMatcher {
				t.Errorf("the guard's matcher is %q, want %q", got, guardMatcher)
			}
		}
	}
	// The foreign group is byte-identical.
	found := false
	for _, g := range preToolUseRaw(t, path) {
		if g == foreign {
			found = true
		}
	}
	if !found {
		t.Errorf("the foreign group changed: %v", preToolUseRaw(t, path))
	}
	// A second install changes nothing.
	before := readFile(t, path)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("a second install changed settings.json:\n%s", got)
	}
	if n := len(guardHandlers(t, path, shellQuote(legacyGuard))); n != 1 {
		t.Errorf("a second install left %d handlers", n)
	}
	if both := guardHandlersEither(t, path, legacyGuard); both != 1 {
		t.Errorf("a second install left %d handlers in either form, want 1: %v", both, preToolUseRaw(t, path))
	}
}

// guardHandlersEither counts the handlers of a settings.json that run the guard command as it is written
// now, quoted, or as an older install wrote it, unquoted.
func guardHandlersEither(t *testing.T, path, guard string) int {
	t.Helper()
	n := 0
	for _, g := range preToolUseRaw(t, path) {
		for _, cmd := range groupHandlerCommands(t, g) {
			if cmd == guard || cmd == shellQuote(guard) {
				n++
			}
		}
	}
	return n
}

// A group with the kit's matcher but the unquoted legacy path is upgraded in place: the group, and every
// other handler in it, stay.
func TestHooksUpgradeInPlace(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	legacyGuard := filepath.Join(home, ".claude", "hooks", guardName)
	path := settingsPath(t, home)
	group := `{"matcher":"` + guardMatcher + `","hooks":[{"type":"command","command":` + jsonString(t, legacyGuard) +
		`},{"type":"command","command":"/usr/local/bin/my-audit"}]}`
	writeSettings(t, path, `{"hooks":{"PreToolUse":[`+group+`]}}`)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionUpgrade, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	for _, a := range res.Actions {
		if a.Action == ActionUpgrade && a.Note != "PreToolUse entry: quoted guard command" {
			t.Errorf("the upgrade note: %q", a.Note)
		}
	}
	groups := preToolUseRaw(t, path)
	if len(groups) != 1 {
		t.Fatalf("%d groups, want the one that was there: %v", len(groups), groups)
	}
	cmds := groupHandlerCommands(t, groups[0])
	if len(cmds) != 2 || cmds[0] != shellQuote(legacyGuard) || cmds[1] != "/usr/local/bin/my-audit" {
		t.Errorf("the handlers after the upgrade: %v", cmds)
	}
}

// The handlers of the guard that an install moves or drops are reported, in the dry run and in the real
// run: a handler of ours under a matcher of the user's is moved to the kit's, and a second one of the
// same command is collapsed.
func TestHooksReportsMovedHandlers(t *testing.T) {
	// both installs, first as a dry run and then for real, and returns both results.
	both := func(t *testing.T, o *Options, path string) (*Result, *Result) {
		t.Helper()
		before := readFile(t, path)
		o.DryRun = true
		dry, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != before {
			t.Errorf("the dry run wrote settings.json:\n%s", got)
		}
		o.DryRun = false
		res, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		return dry, res
	}
	t.Run("moved", func(t *testing.T) {
		t.Parallel()
		o, home, _ := testOpts(t)
		o.Agents, o.Hooks = []Agent{AgentClaude}, true
		guard := filepath.Join(home, ".claude", "hooks", guardName)
		ours := jsonString(t, shellQuote(guard))
		widened := "Bash|Edit|Write|mcp__teknoir__.*"
		path := settingsPath(t, home)
		writeSettings(t, path, `{"hooks":{"PreToolUse":[{"matcher":"`+widened+`","hooks":[{"type":"command","command":`+ours+`}]}]}}`)

		dry, res := both(t, o, path)
		for _, r := range []*Result{dry, res} {
			if !hasAction(r, ActionRegister, path) {
				t.Errorf("actions %v", actionsOf(r))
			}
			note := noteOf(r, ActionRegister, path)
			if want := `moved from matcher "Bash|Edit|Write|mcp__teknoir__.*"`; !strings.Contains(note, want) {
				t.Errorf("the note %q does not say %q", note, want)
			}
			if joined := strings.Join(r.Warnings, "\n"); !strings.Contains(joined, `under matcher "Bash|Edit|Write|mcp__teknoir__.*"`) {
				t.Errorf("no warning about the move: %v", r.Warnings)
			}
		}
		// The handler now runs under the kit's matcher alone, and a further install writes nothing.
		if n := len(preToolUseRaw(t, path)); n != 1 {
			t.Errorf("%d groups, want one: %v", n, preToolUseRaw(t, path))
		}
		after := readFile(t, path)
		res, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if !hasAction(res, ActionAlreadyRegister, path) {
			t.Errorf("actions %v", actionsOf(res))
		}
		if got := readFile(t, path); got != after {
			t.Errorf("a further install changed settings.json:\n%s", got)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		t.Parallel()
		o, home, _ := testOpts(t)
		o.Agents, o.Hooks = []Agent{AgentClaude}, true
		guard := filepath.Join(home, ".claude", "hooks", guardName)
		ours := jsonString(t, shellQuote(guard))
		path := settingsPath(t, home)
		group := `{"matcher":"` + guardMatcher + `","hooks":[{"type":"command","command":` + ours + `},{"type":"command","command":` + ours + `}]}`
		writeSettings(t, path, `{"hooks":{"PreToolUse":[`+group+`]}}`)

		dry, res := both(t, o, path)
		for _, r := range []*Result{dry, res} {
			if !hasAction(r, ActionUpgrade, path) {
				t.Errorf("actions %v", actionsOf(r))
			}
			if note := noteOf(r, ActionUpgrade, path); !strings.Contains(note, "removed 1 other handler(s)") {
				t.Errorf("the note %q", note)
			}
		}
		if got := guardHandlers(t, path, shellQuote(guard)); len(got) != 1 {
			t.Errorf("%d handlers run the guard, want 1: %v", len(got), preToolUseRaw(t, path))
		}
		// A further install finds one handler and writes nothing.
		after := readFile(t, path)
		res, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if !hasAction(res, ActionAlreadyRegister, path) {
			t.Errorf("actions %v", actionsOf(res))
		}
		if got := readFile(t, path); got != after {
			t.Errorf("a further install changed settings.json:\n%s", got)
		}
	})
	t.Run("same matcher twice", func(t *testing.T) {
		t.Parallel()
		o, home, _ := testOpts(t)
		o.Agents, o.Hooks = []Agent{AgentClaude}, true
		guard := filepath.Join(home, ".claude", "hooks", guardName)
		ours := jsonString(t, shellQuote(guard))
		path := settingsPath(t, home)
		g := `{"matcher":"Bash","hooks":[{"type":"command","command":` + ours + `}]}`
		writeSettings(t, path, `{"hooks":{"PreToolUse":[`+g+`,`+g+`]}}`)

		dry, res := both(t, o, path)
		for _, r := range []*Result{dry, res} {
			n := 0
			for _, w := range r.Warnings {
				if strings.Contains(w, `under matcher "Bash"`) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d warnings name the matcher, want 1: %v", n, r.Warnings)
			}
			if note := noteOf(r, ActionRegister, path); !strings.HasSuffix(note, `moved from matcher "Bash")`) {
				t.Errorf("the note %q", note)
			}
		}
	})
	t.Run("kit group and the same matcher twice", func(t *testing.T) {
		t.Parallel()
		o, home, _ := testOpts(t)
		o.Agents, o.Hooks = []Agent{AgentClaude}, true
		guard := filepath.Join(home, ".claude", "hooks", guardName)
		ours := jsonString(t, shellQuote(guard))
		path := settingsPath(t, home)
		kit := `{"matcher":"` + guardMatcher + `","hooks":[{"type":"command","command":` + ours + `}]}`
		g := `{"matcher":"Bash","hooks":[{"type":"command","command":` + ours + `}]}`
		writeSettings(t, path, `{"hooks":{"PreToolUse":[`+kit+`,`+g+`,`+g+`]}}`)

		dry, res := both(t, o, path)
		for _, r := range []*Result{dry, res} {
			if note := noteOf(r, ActionUpgrade, path); !strings.Contains(note, "removed 2 other handler(s)") {
				t.Errorf("the note %q, actions %v", note, actionsOf(r))
			}
		}
		if got := guardHandlers(t, path, shellQuote(guard)); len(got) != 1 {
			t.Errorf("%d handlers run the guard, want 1: %v", len(got), preToolUseRaw(t, path))
		}
	})
}

// Two handlers of our command (a hand edit, or two old installs): one install leaves exactly one.
func TestHooksDuplicateHandlerCollapsed(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	guard := filepath.Join(home, ".claude", "hooks", guardName)
	path := settingsPath(t, home)
	ours := `{"matcher":"` + guardMatcher + `","hooks":[{"type":"command","command":` + jsonString(t, shellQuote(guard)) + `}]}`
	writeSettings(t, path, `{"hooks":{"PreToolUse":[`+ours+`,`+ours+`]}}`)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := guardHandlers(t, path, shellQuote(guard)); len(got) != 1 {
		t.Errorf("%d handlers run the guard, want 1: %v", len(got), preToolUseRaw(t, path))
	}
}

// noteOf is the note of the first action of action on path.
func noteOf(res *Result, action, path string) string {
	for _, a := range res.Actions {
		if a.Action == action && a.Path == path {
			return a.Note
		}
	}
	return ""
}

// A stale unquoted absolute path from an older install goes on uninstall, whatever its matcher, and a
// group that held only it is dropped with it.
func TestHooksUninstallLegacyEntry(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	legacyGuard := filepath.Join(home, ".claude", "hooks", guardName)
	path := settingsPath(t, home)
	writeSettings(t, path, settingsWithOthers)
	// What an older install left: our old group, with the unquoted path and the old matcher.
	body := readFile(t, path)
	legacy := `, {"matcher":"` + legacyGuardMatcher + `","hooks":[{"type":"command","command":` + jsonString(t, legacyGuard) + `}]}`
	body = strings.Replace(body, `"PreToolUse": [
      {`, `"PreToolUse": [
      `+strings.TrimPrefix(legacy, ", ")+`, {`, 1)
	writeSettings(t, path, body)

	res, err := Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionDelete, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := guardHandlers(t, path, legacyGuard); len(got) != 0 {
		t.Errorf("%d legacy handlers left: %v", len(got), got)
	}
	entries := preToolUse(t, path)
	if len(entries) != 1 || entries[0]["matcher"] != "Write" {
		t.Errorf("PreToolUse after uninstall: %v", entries)
	}
}

// The guard is refused (a symlink where it belongs): its handler is not registered, and settings.json is
// left as it was.
func TestHooksGuardRefusedNoEntry(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("settings=%v", existing), func(t *testing.T) {
			o, home, _ := testOpts(t)
			o.Agents, o.Hooks = []Agent{AgentClaude}, true
			path := settingsPath(t, home)
			if existing {
				writeSettings(t, path, settingsWithOthers)
			}
			mustSymlink(t, "/bin/true", filepath.Join(home, ".claude", "hooks", guardName))
			res, err := Install(o)
			if _, ok := Refused(err); !ok {
				t.Fatalf("err %v, want the refusal of the guard", err)
			}
			if !hasAction(res, ActionSkip, path) {
				t.Errorf("actions %v", actionsOf(res))
			}
			if !existing {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("settings.json was written: %v", err)
				}
				return
			}
			if got := readFile(t, path); got != settingsWithOthers {
				t.Errorf("settings.json changed:\n%s", got)
			}
			for _, g := range preToolUseRaw(t, path) {
				if len(groupHandlerCommands(t, g)) > 0 && strings.Contains(g, guardName) {
					t.Errorf("a handler of the refused guard was registered: %s", g)
				}
			}
		})
	}
}

// The hooks root's manifest is refused: the guard is not written, so its hook is not registered.
func TestHooksManifestRefusedNoEntry(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	hooksDir := filepath.Join(home, ".claude", "hooks")
	writeAt(t, filepath.Join(hooksDir, manifestName), `{"kitVersion":"0.2.0","files":{"../x":"`+hashOf("x\n")+`"}}`+"\n", 0o644)
	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	if !hasAction(res, ActionSkip, settingsPath(t, home)) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if _, err := os.Stat(settingsPath(t, home)); !os.IsNotExist(err) {
		t.Errorf("settings.json was written for a refused hooks root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, guardName)); !os.IsNotExist(err) {
		t.Errorf("the guard was written below a refused hooks root: %v", err)
	}
}

// A settings.json with a foreign group the kit cannot read (a handler after a JSON error) is edited
// around it: the unreadable group is kept as it was, and our entry is still registered.
func TestHooksForeignGroupUnreadable(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	path := settingsPath(t, home)
	// A group that is not an object, and one whose hooks is not a list of objects: both stay as they are.
	writeSettings(t, path, `{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":"/usr/local/bin/x"},{"matcher":"Edit","hooks":[{"type":"command","command":"/a"},{"type":"command","command":42}]}]}}`)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionRegister, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if n := len(preToolUseRaw(t, path)); n != 3 {
		t.Errorf("%d groups, want the two foreign ones and ours: %v", n, preToolUseRaw(t, path))
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if n := len(preToolUseRaw(t, path)); n != 2 {
		t.Errorf("%d groups after uninstall, want the two foreign ones: %v", n, preToolUseRaw(t, path))
	}
}
