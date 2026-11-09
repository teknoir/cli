package agentkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// installHooks installs the guard hook for Claude Code and registers it as a PreToolUse hook in its
// settings.json. A managed install writes the guard only and prints the settings entry for the image's
// settings file.
func (p *op) installHooks() error {
	dir := p.claudeDir()
	guard := filepath.Join(dir, "hooks", guardName)
	content, err := fs.ReadFile(p.o.FS, "hooks/"+guardName)
	if err != nil {
		return err
	}
	if err := p.installSet(filepath.Join(dir, "hooks"), map[string][]byte{guardName: content}, string(AgentClaude)); err != nil {
		return err
	}
	if p.o.Scope == ScopeManaged {
		p.r.warn("write this entry to the agent's settings.json (%s):", filepath.Join(dir, "settings.json"))
		p.r.warn("  %s", settingsSnippet(filepath.Join(dir, "hooks", guardName)))
		return nil
	}
	return p.mergeSettings(filepath.Join(dir, "settings.json"), guard, false)
}

// uninstallHooks removes the guard hook and its settings entry.
func (p *op) uninstallHooks() error {
	dir := p.claudeDir()
	guard := filepath.Join(dir, "hooks", guardName)
	if p.o.Scope != ScopeManaged {
		if err := p.mergeSettings(filepath.Join(dir, "settings.json"), guard, true); err != nil {
			return err
		}
	}
	return p.uninstallSet(filepath.Join(dir, "hooks"), string(AgentClaude))
}

// mergeSettings adds (or, with remove, drops) the guard's PreToolUse entry in a Claude Code
// settings.json, keeping every other key, hook and permission as it is.
func (p *op) mergeSettings(path, guard string, remove bool) error {
	snippet := "add " + settingsSnippet(guard)
	if remove {
		snippet = "remove " + settingsSnippet(guard)
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

	ours, kept := -1, entries[:0:0]
	for i, e := range entries {
		if entryUsesGuard(e, guard) {
			ours = i
			continue
		}
		kept = append(kept, e)
	}
	entries = kept

	switch {
	case remove:
		if ours < 0 {
			p.r.act(ActionSkip, path, string(AgentClaude), "no teknoir guard entry")
			return nil
		}
		p.r.act(ActionDelete, path, string(AgentClaude), "PreToolUse entry")
	case ours >= 0:
		p.r.act(ActionAlreadyRegister, path, string(AgentClaude), "PreToolUse entry")
		return nil
	default:
		entries = append(entries, json.RawMessage(preToolUseEntry(guard)))
		p.r.act(ActionRegister, path, string(AgentClaude), "PreToolUse entry")
	}

	if err := setJSONList(&hooks, "PreToolUse", entries); err != nil {
		return err
	}
	if len(hooks) == 0 {
		delete(top, "hooks")
	} else if err := setJSONValue(&top, "hooks", hooks); err != nil {
		return err
	}
	return p.writeJSON(target, top, 0o644)
}

// entryUsesGuard reports whether a PreToolUse entry runs the guard at guard, or any teknoir-guard.
func entryUsesGuard(raw json.RawMessage, guard string) bool {
	var e struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return false
	}
	for _, h := range e.Hooks {
		fields := strings.Fields(h.Command)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == guard || filepath.Base(fields[0]) == guardName {
			return true
		}
	}
	return false
}

// setJSONValue puts v into obj under key, creating obj when it is nil.
func setJSONValue(obj *map[string]json.RawMessage, key string, v any) error {
	raw, err := json.Marshal(v)
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
// sorted: an existing file's key order is not kept.
func (p *op) writeJSON(path string, v any, def os.FileMode) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	return p.overwriteFile(path, out.Bytes(), def)
}
