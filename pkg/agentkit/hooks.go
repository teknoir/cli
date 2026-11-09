package agentkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// projectGuardCommand is the hook command of a project-scope install: Claude Code sets
// $CLAUDE_PROJECT_DIR for a hook command, so the committed settings.json works on every machine.
const projectGuardCommand = `"$CLAUDE_PROJECT_DIR"/.claude/hooks/teknoir-guard`

// shellQuote quotes s for the shell that runs a hook command, so a path with a space, a quote or a
// dollar sign is one word.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// guardCommand is the command our PreToolUse handler runs in this scope.
func (p *op) guardCommand() string {
	if p.o.Scope == ScopeProject {
		return projectGuardCommand
	}
	return shellQuote(p.absGuardPath())
}

// absGuardPath is where the guard is installed in this scope.
func (p *op) absGuardPath() string { return filepath.Join(p.claudeDir(), "hooks", guardName) }

// installHooks installs the guard hook for Claude Code and registers it as a PreToolUse handler in its
// settings.json. The managed scope installs the guard on its own (install) and only prints the entry.
func (p *op) installHooks() error {
	hooksDir := filepath.Join(p.claudeDir(), "hooks")
	content, err := fs.ReadFile(p.o.FS, "hooks/"+guardName)
	if err != nil {
		return err
	}
	if err := p.installSet(hooksDir, map[string][]byte{guardName: content}, string(AgentClaude)); err != nil {
		return err
	}
	settings := filepath.Join(p.claudeDir(), "settings.json")
	if p.wasRefused(p.absGuardPath()) || p.wasRefused(manifestPath(hooksDir)) {
		// The hook would run whatever is at the guard's path, or nothing: it is not registered.
		p.r.act(ActionSkip, settings, string(AgentClaude), "the guard was refused: its hook is not registered")
		return nil
	}
	return p.mergeSettings(settings, false)
}

// uninstallHooks removes the guard hook and its settings entry.
func (p *op) uninstallHooks() error {
	dir := p.claudeDir()
	if err := p.mergeSettings(filepath.Join(dir, "settings.json"), true); err != nil {
		return err
	}
	return p.uninstallSet(filepath.Join(dir, "hooks"), string(AgentClaude))
}

// mergeSettings adds the guard's PreToolUse handler to a Claude Code settings.json, or with remove takes
// it out, keeping every other key, group and handler as it is. A group is only ever changed in its own
// handlers array, and is dropped only when that array is left empty.
func (p *op) mergeSettings(path string, remove bool) error {
	command := p.guardCommand()
	snippet := "add " + settingsSnippet(command)
	if remove {
		snippet = "remove " + settingsSnippet(command)
	}
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuse(path, string(AgentClaude), why+" "+snippet, why+" "+snippet)
		return nil
	}
	data, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := top["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("hooks in %s is not an object: %w", path, err)
		}
	}
	var entries []json.RawMessage
	if raw, ok := hooks["PreToolUse"]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("hooks.PreToolUse in %s is not an array: %w", path, err)
		}
	}
	groups := parseHookGroups(entries)
	legacy := p.absGuardPath()
	registered, upgraded, changed, removed := false, false, false, false
	var moved []string // the matchers a handler of ours was moved from, each once
	nMoved, dups := 0, 0
	for _, g := range groups {
		if g.opaque {
			continue
		}
		ours := g.matcher() == guardMatcher
		gChanged := false
		var kept []map[string]json.RawMessage
		for _, h := range g.handlers {
			cmd, isCommand := handlerCommand(h)
			guardHere := isCommand && matchesGuard(cmd, command, legacy)
			switch {
			case !guardHere:
				if isCommand && !remove && guardScript(cmd) {
					p.r.warn("%s: another teknoir-guard hook is registered: %s", path, cmd)
				}
				kept = append(kept, h)
			case remove:
				// Ours goes, whatever group it is in.
				gChanged, changed, removed = true, true, true
			case !ours:
				// Ours does not belong in a group of another matcher: it would run twice over.
				nMoved++
				if m := g.matcher(); !slices.Contains(moved, m) {
					moved = append(moved, m)
				}
				gChanged, changed = true, true
			case registered:
				// A second handler of this run's command: one is enough.
				dups++
				gChanged, changed = true, true
			case cmd == command:
				registered = true
				kept = append(kept, h)
			default:
				// The unquoted absolute path an older install wrote.
				kept = append(kept, setHandlerCommand(h, command))
				gChanged, changed, upgraded, registered = true, true, true, true
			}
		}
		if gChanged {
			g.handlers = kept
			g.changed = true
		}
	}
	for _, m := range moved {
		if m != legacyGuardMatcher {
			p.r.warn("%s: the guard's handler under matcher %q now runs under the kit's matcher %q", path, m, guardMatcher)
		}
	}

	switch {
	case remove:
		if !removed {
			p.r.act(ActionSkip, path, string(AgentClaude), "no teknoir guard entry")
			return nil
		}
		p.r.act(ActionDelete, path, string(AgentClaude), "PreToolUse entry")
	case !registered:
		entry, err := preToolUseEntry(command)
		if err != nil {
			return err
		}
		groups = append(groups, &hookGroup{raw: entry})
		changed = true
		note := "PreToolUse entry"
		if len(moved) > 0 {
			quoted := make([]string, 0, len(moved))
			for _, m := range moved {
				quoted = append(quoted, strconv.Quote(m))
			}
			note += " (moved from matcher " + strings.Join(quoted, ", ") + ")"
		}
		p.r.act(ActionRegister, path, string(AgentClaude), note)
	case upgraded:
		p.r.act(ActionUpgrade, path, string(AgentClaude), "PreToolUse entry: quoted guard command")
	case changed:
		p.r.act(ActionUpgrade, path, string(AgentClaude), fmt.Sprintf("PreToolUse entry: removed %d other handler(s) of the guard", nMoved+dups))
	default:
		p.r.act(ActionAlreadyRegister, path, string(AgentClaude), "PreToolUse entry")
	}
	if !changed {
		return nil
	}

	out := make([]json.RawMessage, 0, len(groups))
	for _, g := range groups {
		b, err := g.marshal()
		if err != nil {
			return err
		}
		if b != nil {
			out = append(out, b)
		}
	}
	if err := setJSONList(&hooks, "PreToolUse", out); err != nil {
		return err
	}
	if len(hooks) == 0 {
		delete(top, "hooks")
	} else if err := setJSONValue(&top, "hooks", hooks); err != nil {
		return err
	}
	return p.writeJSON(target, top, 0o644)
}

// hookGroup is one PreToolUse group of a settings.json: its keys, and its handlers. A group that is
// not changed is written back as the bytes it was read from.
type hookGroup struct {
	raw      json.RawMessage
	keys     map[string]json.RawMessage
	handlers []map[string]json.RawMessage
	changed  bool
	// opaque marks a group the kit does not read: it is written back as it was.
	opaque bool
}

// parseHookGroups decodes the PreToolUse groups. A group the kit cannot read (not an object, or a hooks
// value that is not an array of objects) is kept as it is and never changed: it holds no handler of ours.
func parseHookGroups(entries []json.RawMessage) []*hookGroup {
	groups := make([]*hookGroup, 0, len(entries))
	for _, raw := range entries {
		g := &hookGroup{raw: raw, keys: map[string]json.RawMessage{}}
		if err := json.Unmarshal(raw, &g.keys); err != nil {
			g.opaque = true
			groups = append(groups, g)
			continue
		}
		if hooks, ok := g.keys["hooks"]; ok {
			if err := json.Unmarshal(hooks, &g.handlers); err != nil {
				g.opaque = true
			}
		}
		groups = append(groups, g)
	}
	return groups
}

// matcher is the group's matcher, "" when it has none.
func (g *hookGroup) matcher() string {
	var s string
	if raw, ok := g.keys["matcher"]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// marshal is the group as JSON: its original bytes when nothing changed, and nil for a group that is
// dropped because its handlers array is empty.
func (g *hookGroup) marshal() (json.RawMessage, error) {
	if !g.changed {
		return g.raw, nil
	}
	if len(g.handlers) == 0 {
		return nil, nil
	}
	b, err := marshalNoEscape(g.handlers)
	if err != nil {
		return nil, err
	}
	g.keys["hooks"] = b
	return marshalNoEscape(g.keys)
}

// handlerCommand is the command a handler runs, and whether it is a command handler at all.
func handlerCommand(h map[string]json.RawMessage) (string, bool) {
	var typ, cmd string
	if raw, ok := h["type"]; ok {
		_ = json.Unmarshal(raw, &typ)
	}
	if typ != "command" {
		return "", false
	}
	raw, ok := h["command"]
	if !ok {
		return "", false
	}
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return "", false
	}
	return cmd, true
}

// setHandlerCommand is h with its command replaced.
func setHandlerCommand(h map[string]json.RawMessage, command string) map[string]json.RawMessage {
	b, err := marshalNoEscape(command)
	if err != nil {
		return h
	}
	out := make(map[string]json.RawMessage, len(h))
	for k, v := range h {
		out[k] = v
	}
	out["command"] = b
	return out
}

// matchesGuard reports whether command is exactly this kit's guard command of the scope, or the
// unquoted path an older install wrote (the legacy form). No other form counts.
func matchesGuard(command, current, legacy string) bool {
	c := strings.TrimSpace(command)
	return c == current || c == legacy
}

// guardScript reports whether a command that is not ours still runs a script named teknoir-guard: the
// user's own guard, or one the kit does not manage here.
func guardScript(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(strings.Trim(fields[0], `'"`)) == guardName
}

// setJSONValue puts v into obj under key, creating obj when it is nil.
func setJSONValue(obj *map[string]json.RawMessage, key string, v any) error {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return err
	}
	if *obj == nil {
		*obj = map[string]json.RawMessage{}
	}
	(*obj)[key] = raw
	return nil
}

// setJSONList puts a list into obj under key; an empty list removes the key, so a settings file does
// not keep an empty hooks array behind.
func setJSONList(obj *map[string]json.RawMessage, key string, list []json.RawMessage) error {
	if len(list) == 0 {
		delete(*obj, key)
		return nil
	}
	return setJSONValue(obj, key, list)
}

// writeJSON writes an object with 2-space indent, keeping the file's mode (def when it is new). Keys are
// sorted: an existing file's key order is not kept. The file ends in exactly one newline, as it was
// written.
func (p *op) writeJSON(path string, v any, def os.FileMode) error {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	return p.overwriteFile(path, append(out.Bytes(), '\n'), def)
}
