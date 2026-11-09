package agentkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// The entry every agent gets: `tnctl mcp`, the read-only MCP server of the kit.
var (
	claudeMCPEntry   = json.RawMessage(`{"type":"stdio","command":"tnctl","args":["mcp"]}`)
	opencodeMCPEntry = json.RawMessage(`{"type":"local","command":["tnctl","mcp"]}`)
	// opencodeSnippet is what the user adds by hand to an OpenCode file with comments.
	opencodeSnippet = `{"mcp":{"servers":{"teknoir":{"type":"local","command":["tnctl","mcp"]}}}}`
	// mcpServerName is the name of the server in every config file.
	mcpServerName = "teknoir"
)

// installMCP registers the teknoir MCP server in the config file of each agent, merging into what is
// there and never clobbering other servers or keys.
func (p *op) installMCP() error {
	for _, a := range p.o.Agents {
		var err error
		switch a {
		case AgentClaude:
			err = p.registerClaudeMCP()
		case AgentCodex:
			err = p.registerCodexMCP()
		case AgentOpencode:
			err = p.registerOpencodeMCP()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// uninstallMCP removes the teknoir MCP server from the config file of each agent.
func (p *op) uninstallMCP() error {
	for _, a := range p.o.Agents {
		var err error
		switch a {
		case AgentClaude:
			path, def := p.claudeMCPFile()
			err = p.removeJSONEntry(path, def, false, []string{"mcpServers"}, claudeMCPEntry, string(AgentClaude))
		case AgentCodex:
			err = p.removeCodexMCP()
		case AgentOpencode:
			path, jsonc := p.opencodeMCPFile()
			err = p.removeJSONEntry(path, 0o644, jsonc, []string{"mcp", "servers"}, opencodeMCPEntry, string(AgentOpencode))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// claudeMCPFile is the file Claude Code reads the MCP servers from, and the mode of a new one.
func (p *op) claudeMCPFile() (string, os.FileMode) {
	if p.o.Scope == ScopeProject {
		return filepath.Join(p.o.ProjectDir, ".mcp.json"), 0o644
	}
	// The .claude.json of user scope holds the whole Claude Code state: a new one is private.
	return p.o.ClaudeConfigFile, 0o600
}

func (p *op) codexMCPFile() string {
	if p.o.Scope == ScopeProject {
		return filepath.Join(p.o.ProjectDir, ".codex", "config.toml")
	}
	return filepath.Join(p.o.CodexHome, "config.toml")
}

// opencodeMCPFile is the OpenCode config: the .jsonc when there is one, the .json when there is not,
// and .json when there is neither. A commented .jsonc is never edited, and neither file is ever
// created next to the other.
func (p *op) opencodeMCPFile() (string, bool) {
	dir := p.o.ProjectDir
	if p.o.Scope == ScopeUser {
		dir = p.o.OpencodeConfigDir
	}
	jsonc := filepath.Join(dir, "opencode.jsonc")
	// Lstat: a dangling opencode.jsonc link is found too, so the .json is not created beside it.
	if _, err := os.Lstat(jsonc); err == nil {
		return jsonc, true
	}
	return filepath.Join(dir, "opencode.json"), false
}

// ---------- Claude Code and OpenCode: JSON ----------

func (p *op) registerClaudeMCP() error {
	path, def := p.claudeMCPFile()
	return p.registerJSONEntry(path, def, false, []string{"mcpServers"}, "mcpServers", string(AgentClaude), claudeMCPEntry)
}

func (p *op) registerOpencodeMCP() error {
	path, jsonc := p.opencodeMCPFile()
	return p.registerJSONEntry(path, 0o644, jsonc, []string{"mcp", "servers"}, "mcp.servers", string(AgentOpencode), opencodeMCPEntry)
}

// registerJSONEntry merges the MCP entry into a JSON (or, with jsonc, possibly JSONC) config file.
// entryAt is the readable name of the entry's place, for messages.
func (p *op) registerJSONEntry(path string, def os.FileMode, jsonc bool, container []string, entryAt, agent string, entry json.RawMessage) error {
	snippet := "add " + jsonSnippet(container, entry)
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuse(path, agent, why+" "+snippet, why+" "+snippet)
		return nil
	}
	return p.withClaudeConfigLock(path, func() error {
		top, err := p.readJSONConfig(target, jsonc, agent, snippet)
		if top == nil {
			return err
		}
		existing, err := jsonLookup(top, container, mcpServerName)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		switch {
		case existing == nil:
			if err := jsonSetPath(top, container, mcpServerName, entry); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			p.r.act(ActionRegister, path, agent, entryAt)
		case jsonEqual(existing, entry):
			p.r.act(ActionAlreadyRegister, path, agent, entryAt)
			return nil
		case p.o.Force:
			if err := jsonSetPath(top, container, mcpServerName, entry); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			p.r.act(ActionRegister, path, agent, entryAt+": replaced a differing one (--force)")
		default:
			p.r.act(ActionSkip, path, agent, entryAt+".teknoir differs: kept it (use --force to replace it)")
			p.r.warn("%s already has a different %s.%s; kept it (use --force to replace it)", path, entryAt, mcpServerName)
			return nil
		}
		return p.writeJSON(target, top, def)
	})
}

// removeJSONEntry drops the MCP entry from a JSON config file, when it is ours.
func (p *op) removeJSONEntry(path string, def os.FileMode, jsonc bool, container []string, entry json.RawMessage, agent string) error {
	snippet := "remove " + jsonSnippet(container, entry)
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuse(path, agent, why+" "+snippet, why+" "+snippet)
		return nil
	}
	return p.withClaudeConfigLock(path, func() error {
		top, err := p.readJSONConfig(target, jsonc, agent, snippet)
		if top == nil {
			return err
		}
		existing, err := jsonLookup(top, container, mcpServerName)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		switch {
		case existing == nil:
			p.r.act(ActionSkip, path, agent, "no teknoir MCP entry")
			return nil
		case !jsonEqual(existing, entry):
			p.r.act(ActionSkip, path, agent, "a different teknoir MCP entry: kept it")
			p.r.warn("%s has a teknoir MCP entry that is not the kit's; kept it", path)
			return nil
		}
		if err := jsonRemovePath(top, container, mcpServerName); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		p.r.act(ActionDelete, path, agent, "teknoir MCP entry")
		return p.writeJSON(target, top, def)
	})
}

const (
	// claudeConfigLockWait is how long an edit of the user-scope Claude config waits for Claude Code's
	// lock before it gives up, as Claude Code's own lock does.
	claudeConfigLockWait = 5 * time.Second
	// claudeConfigLockStale is the age at which a lock is taken over: Claude Code removes its own lock
	// when it closes, so one this old was left behind by a process that died.
	claudeConfigLockStale = 10 * time.Second
)

// withClaudeConfigLock runs fn under the lock Claude Code itself holds while it saves its config file
// (~/.claude.json.lock, a lock directory), so tnctl's read, merge and write cannot lose a save Claude
// Code makes at the same moment. Only the user-scope Claude config file is locked; a dry run never takes
// the lock, and a symlinked config file is locked under the path Claude Code knows.
func (p *op) withClaudeConfigLock(path string, fn func() error) error {
	if p.o.DryRun || p.o.Scope != ScopeUser || p.o.ClaudeConfigFile == "" ||
		filepath.Clean(path) != filepath.Clean(p.o.ClaudeConfigFile) {
		return fn()
	}
	lock := path + ".lock"
	wait := p.o.lockTimeout
	if wait <= 0 {
		wait = claudeConfigLockWait
	}
	deadline := time.Now().Add(wait)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > claudeConfigLockStale {
			if rerr := os.Remove(lock); rerr == nil || errors.Is(rerr, fs.ErrNotExist) {
				continue
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Claude Code holds %s; run again", lock)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer os.Remove(lock)
	return fn()
}

// readJSONConfig reads a JSON config file, returning nil (and an error) when it cannot be edited. A
// .jsonc with comments, which encoding/json rejects but a JSONC reader accepts, is reported with the
// snippet to add by hand; the run ends non-zero and the file is left as it is.
func (p *op) readJSONConfig(path string, jsonc bool, agent, snippet string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &top); err != nil {
			if !jsonc {
				return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
			}
			note := "the file has comments: " + snippet + " by hand"
			p.r.act(ActionRefuse, path, agent, note)
			p.r.warn("%s has comments, so tnctl does not edit it. %s", path, note)
			p.refused = append(p.refused, path)
			p.reasons = append(p.reasons, fmt.Sprintf("%s has comments: edit it by hand and run the command again", path))
			return nil, nil
		}
	}
	return top, nil
}

// jsonSnippet is the entry as the JSON object that holds it, for the message that tells the user to add or
// remove it by hand: the container keys of the caller, outermost first.
func jsonSnippet(container []string, entry json.RawMessage) string {
	v := map[string]json.RawMessage{mcpServerName: entry}
	for i := len(container) - 1; i >= 0; i-- {
		b, _ := json.Marshal(v)
		v = map[string]json.RawMessage{container[i]: b}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonLookup returns the value at container.../name, or nil when the path is not there.
func jsonLookup(obj map[string]json.RawMessage, container []string, name string) (json.RawMessage, error) {
	for _, k := range container {
		raw, ok := obj[k]
		if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			return nil, nil
		}
		child := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &child); err != nil {
			return nil, fmt.Errorf("%q is not an object", k)
		}
		obj = child
	}
	raw, ok := obj[name]
	if !ok {
		return nil, nil
	}
	return raw, nil
}

// jsonSetPath sets container.../name to value, creating the objects along the way.
func jsonSetPath(obj map[string]json.RawMessage, container []string, name string, value json.RawMessage) error {
	if len(container) == 0 {
		obj[name] = value
		return nil
	}
	child := map[string]json.RawMessage{}
	raw, ok := obj[container[0]]
	if ok && len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &child); err != nil {
			return fmt.Errorf("%q is not an object", container[0])
		}
	}
	if err := jsonSetPath(child, container[1:], name, value); err != nil {
		return err
	}
	b, err := marshalNoEscape(child)
	if err != nil {
		return err
	}
	obj[container[0]] = b
	return nil
}

// jsonRemovePath deletes container.../name; an object that is left empty is dropped as well, so a file
// does not keep an empty mcpServers behind.
func jsonRemovePath(obj map[string]json.RawMessage, container []string, name string) error {
	if len(container) == 0 {
		delete(obj, name)
		return nil
	}
	raw, ok := obj[container[0]]
	if !ok {
		return nil
	}
	child := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &child); err != nil {
		return fmt.Errorf("%q is not an object", container[0])
	}
	if err := jsonRemovePath(child, container[1:], name); err != nil {
		return err
	}
	if len(child) == 0 {
		delete(obj, container[0])
		return nil
	}
	b, err := marshalNoEscape(child)
	if err != nil {
		return err
	}
	obj[container[0]] = b
	return nil
}

// jsonEqual compares two JSON values by value, not by their spelling.
func jsonEqual(a, b []byte) bool {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// ---------- Codex: TOML ----------

// tomlEntryBlock is the codex config entry of the MCP server.
const tomlEntryBlock = "[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\n"

// codexReplace is the verb of a refusal that asks the user to replace an entry that is there already.
const codexReplace = "replace mcp_servers.teknoir with"

// tomlSnippet is what the user does by hand: the verb ("add", "remove" or codexReplace) and the entry.
func tomlSnippet(verb string) string {
	return verb + " " + strings.TrimRight(tomlEntryBlock, "\n")
}

// refuseCodex refuses an edit of the Codex config, naming the snippet to add, remove or replace by hand.
func (p *op) refuseCodex(path, why, verb string) {
	snippet := tomlSnippet(verb)
	p.refuse(path, string(AgentCodex), why+" "+snippet, why+" "+snippet)
}

func (p *op) registerCodexMCP() error {
	path := p.codexMCPFile()
	agent := string(AgentCodex)
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuseCodex(path, why, "add")
		return nil
	}
	data, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(data)
	parsed, err := tomlParse(text)
	if err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	note := "mcp_servers." + mcpServerName
	existing, isTable := tomlServer(parsed)
	_, present := tomlServerValue(parsed)
	verb := "add"
	switch {
	case !present:
		text = appendTOML(text, tomlEntryBlock)
	case tomlServerIsOurs(existing):
		p.r.act(ActionAlreadyRegister, path, agent, note)
		return nil
	case !p.o.Force:
		p.r.act(ActionSkip, path, agent, note+" differs: kept it (use --force to replace it)")
		p.r.warn("%s already has a different [mcp_servers.%s]; kept it (use --force to replace it)", path, mcpServerName)
		return nil
	case !isTable:
		p.refuseCodex(path, fmt.Sprintf("%s defines mcp_servers.%s without a [mcp_servers.%s] table: edit it by hand:",
			path, mcpServerName, mcpServerName), codexReplace)
		return nil
	default:
		start, end, ok := tomlTableRange(text, "mcp_servers."+mcpServerName)
		if !ok {
			p.refuseCodex(path, fmt.Sprintf("%s defines mcp_servers.%s without a [mcp_servers.%s] table: edit it by hand:",
				path, mcpServerName, mcpServerName), codexReplace)
			return nil
		}
		text = text[:start] + tomlEntryBlock + tomlJoin(text[end:])
		note += ": replaced a differing one (--force)"
		verb = codexReplace
	}
	// The new text is checked before anything is written, a dry run included: tnctl never promises a
	// register that would break the file.
	entry, err := tomlKitEntry()
	if err != nil {
		return err
	}
	if !p.codexEditOK(path, string(data), text, entry, verb) {
		return nil
	}
	p.r.act(ActionRegister, path, agent, note)
	if p.o.DryRun {
		return nil
	}
	return p.overwriteFile(target, []byte(text), 0o644)
}

func (p *op) removeCodexMCP() error {
	path := p.codexMCPFile()
	agent := string(AgentCodex)
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuseCodex(path, why, "remove")
		return nil
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		p.r.act(ActionSkip, path, agent, "no config file")
		return nil
	}
	if err != nil {
		return err
	}
	text := string(data)
	parsed, err := tomlParse(text)
	if err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	existing, isTable := tomlServer(parsed)
	_, present := tomlServerValue(parsed)
	switch {
	case !present:
		p.r.act(ActionSkip, path, agent, "no mcp_servers."+mcpServerName)
		return nil
	case !isTable || !tomlServerIsOurs(existing):
		p.r.act(ActionSkip, path, agent, "a different mcp_servers."+mcpServerName+": kept it")
		p.r.warn("%s has a [mcp_servers.%s] that is not the kit's; kept it", path, mcpServerName)
		return nil
	}
	start, end, ok := tomlTableRange(text, "mcp_servers."+mcpServerName)
	if !ok {
		p.refuseCodex(path, fmt.Sprintf("%s defines mcp_servers.%s without a table header: edit it by hand:",
			path, mcpServerName), "remove")
		return nil
	}
	// Only the seam is trimmed: the blank line the entry was separated by. Nothing else in the file is
	// rewritten, so a multi-line string keeps every line it has.
	before, after := text[:start], text[end:]
	switch {
	case after == "":
		before = strings.TrimRight(before, "\n")
		if before != "" {
			before += "\n"
		}
	case before == "":
		// Our table came first: nothing of the user's may end up at the head of the file.
		after = strings.TrimLeft(after, "\n")
	case strings.HasSuffix(before, "\n\n") && strings.HasPrefix(after, "\n"):
		after = strings.TrimLeft(after, "\n")
	}
	next := before + after
	if !p.codexEditOK(path, string(data), next, nil, "remove") {
		return nil
	}
	p.r.act(ActionDelete, path, agent, "mcp_servers."+mcpServerName)
	if p.o.DryRun {
		return nil
	}
	return p.overwriteFile(target, []byte(next), 0o644)
}

// codexEditOK checks, before any write, that next is orig with exactly the mcp_servers.teknoir entry
// removed (put nil) or replaced by put. A text that does not parse, or an edit that would touch anything
// else, is refused and false comes back; the file is left as it is.
func (p *op) codexEditOK(path, orig, next string, put map[string]any, verb string) bool {
	if _, err := tomlParse(next); err != nil {
		p.refuseCodex(path, fmt.Sprintf("%s: the edit would not parse (%v); unchanged: edit it by hand:", path, err), verb)
		return false
	}
	if err := tomlSameExcept(orig, next, put); err != nil {
		p.refuseCodex(path, fmt.Sprintf("%s: tnctl cannot edit [mcp_servers.%s] safely here; edit it by hand:",
			path, mcpServerName), verb)
		return false
	}
	return true
}

// tomlParse parses a config into a map. Empty text is an empty map: go-toml leaves a nil one.
func tomlParse(text string) (map[string]any, error) {
	m := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	if err := toml.Unmarshal([]byte(text), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// tomlServerValue returns parsed["mcp_servers"]["teknoir"] as it is there: a table, a string, anything.
func tomlServerValue(parsed map[string]any) (any, bool) {
	servers, ok := parsed["mcp_servers"].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := servers[mcpServerName]
	return v, ok
}

// tomlServer returns parsed["mcp_servers"]["teknoir"] when it is a table.
func tomlServer(parsed map[string]any) (map[string]any, bool) {
	v, ok := tomlServerValue(parsed)
	if !ok {
		return nil, false
	}
	srv, ok := v.(map[string]any)
	return srv, ok
}

// tomlServerIsOurs reports whether the table is exactly the kit's entry.
func tomlServerIsOurs(srv map[string]any) bool {
	if len(srv) != 2 {
		return false
	}
	if cmd, ok := srv["command"].(string); !ok || cmd != "tnctl" {
		return false
	}
	args, ok := srv["args"].([]any)
	if !ok || len(args) != 1 {
		return false
	}
	a, ok := args[0].(string)
	return ok && a == "mcp"
}

// tomlKitEntry is the kit's own table, parsed, for the edit invariant.
func tomlKitEntry() (map[string]any, error) {
	m, err := tomlParse(tomlEntryBlock)
	if err != nil {
		return nil, err
	}
	srv, ok := tomlServer(m)
	if !ok {
		return nil, errors.New("the kit's own [mcp_servers.teknoir] does not parse")
	}
	return srv, nil
}

// tomlSameExcept requires next to be orig with only mcp_servers.teknoir changed: removed when put is nil,
// set to put otherwise. Both texts must parse; the caller checks next first.
func tomlSameExcept(orig, next string, put map[string]any) error {
	want, err := tomlParse(orig)
	if err != nil {
		return err
	}
	servers, _ := want["mcp_servers"].(map[string]any)
	if servers == nil && put != nil {
		servers = map[string]any{}
		want["mcp_servers"] = servers
	}
	if servers != nil {
		if put == nil {
			delete(servers, mcpServerName)
		} else {
			servers[mcpServerName] = put
		}
		if len(servers) == 0 {
			delete(want, "mcp_servers")
		}
	}
	got, err := tomlParse(next)
	if err != nil {
		return err
	}
	if servers, ok := got["mcp_servers"].(map[string]any); ok && len(servers) == 0 {
		delete(got, "mcp_servers")
	}
	if !reflect.DeepEqual(want, got) {
		return errors.New("the edit changes more than [mcp_servers.teknoir]")
	}
	return nil
}

// tomlJoin is the text that follows a replaced table: it keeps its own leading newline, and gets one when
// it has none, so it stays apart from the new table.
func tomlJoin(rest string) string {
	if rest == "" || strings.HasPrefix(rest, "\n") {
		return rest
	}
	return "\n" + rest
}

// appendTOML appends the entry as a table of its own, after a blank line.
func appendTOML(text, block string) string {
	if text == "" {
		return block
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\n" + block
}

// tomlHeaderRE matches a table header, [x] or [[x]], which ends the table above it.
var tomlHeaderRE = regexp.MustCompile(`^[ \t]*(\[\[[^\]]*\]\]|\[[^\[\]]*\])[ \t]*(#.*)?$`)

// tomlHeaderParts splits a header match into its key, without the brackets, and whether it is an array
// table ([[x]]), which can only end a table, never start one.
func tomlHeaderParts(header string) (key string, array bool) {
	if strings.HasPrefix(header, "[[") {
		return strings.TrimSuffix(strings.TrimPrefix(header, "[["), "]]"), true
	}
	return strings.TrimSuffix(strings.TrimPrefix(header, "["), "]"), false
}

// tomlCanonicalName is the name a header refers to: `mcp_servers."teknoir"` is mcp_servers.teknoir. A
// quoted key that holds a dot is one key of its own (`["mcp_servers.teknoir"]`), and keeps its quotes so
// it does not read as a dotted path.
func tomlCanonicalName(s string) string {
	parts := tomlSplitKey(s)
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
			if inner := p[1 : len(p)-1]; !strings.Contains(inner, ".") {
				p = inner
			}
		}
		parts[i] = p
	}
	return strings.Join(parts, ".")
}

// tomlSplitKey splits a header key on the dots outside quotes, so ["a.b"] stays one key.
func tomlSplitKey(s string) []string {
	var out []string
	start := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// tomlTableRange returns the byte range of the [name] table: its header line through the last line of
// its own, so the blank and comment lines that separate it from the next table stay with that table. Any
// header, [x] or [[x]], ends the table; when none follows, the range runs to the end of the text when
// only blank lines are left, and stops before the comment lines at the end of the file, which stay. Those
// comment lines start only where the text above them parses: a line that starts with # inside a
// multi-line string of the table is the table's, so the whole tail is then taken.
func tomlTableRange(text, name string) (int, int, bool) {
	lines := strings.SplitAfter(text, "\n")
	offs := make([]int, len(lines))
	off := 0
	for i, line := range lines {
		offs[i] = off
		off += len(line)
	}
	startLine := -1
	for i, line := range lines {
		m := tomlHeaderRE.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		key, array := tomlHeaderParts(m[1])
		if startLine < 0 {
			if !array && tomlCanonicalName(key) == name {
				startLine = i
			}
			continue
		}
		end := i
		for end > startLine+1 && separatorLine(lines[end-1]) {
			end--
		}
		return offs[startLine], offs[end], true
	}
	if startLine < 0 {
		return 0, 0, false
	}
	// No header follows: comment lines at the end of the file stay; a tail of blank lines alone goes with
	// the table.
	end := len(lines)
	for end > startLine+1 && separatorLine(lines[end-1]) {
		end--
	}
	// The run only starts where the text above it is complete TOML: lines that start with # inside a
	// multi-line string of the table are the table's, not comments of the user's.
	for ; end < len(lines); end++ {
		if strings.TrimSpace(text[offs[end]:]) == "" {
			break
		}
		if _, err := tomlParse(text[:offs[end]]); err == nil {
			return offs[startLine], offs[end], true
		}
	}
	return offs[startLine], len(text), true
}

// separatorLine reports whether a line is blank or holds only a comment: it belongs to the table below
// it, not to the one above.
func separatorLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}
