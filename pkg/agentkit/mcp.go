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
	// ~/.claude.json holds the whole Claude Code state: a new one is private.
	return filepath.Join(p.o.Home, ".claude.json"), 0o600
}

func (p *op) codexMCPFile() string {
	if p.o.Scope == ScopeProject {
		return filepath.Join(p.o.ProjectDir, ".codex", "config.toml")
	}
	return filepath.Join(p.o.Home, ".codex", "config.toml")
}

// opencodeMCPFile is the OpenCode config: the .jsonc when there is one, the .json when there is not,
// and .json when there is neither. A commented .jsonc is never edited, and neither file is ever
// created next to the other.
func (p *op) opencodeMCPFile() (string, bool) {
	dir := p.o.ProjectDir
	if p.o.Scope == ScopeUser {
		dir = filepath.Join(p.o.Home, ".config", "opencode")
	}
	jsonc := filepath.Join(dir, "opencode.jsonc")
	if _, err := os.Stat(jsonc); err == nil {
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
	top, err := p.readJSONConfig(path, jsonc, agent, opencodeSnippet)
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
	return p.writeJSON(path, top, def)
}

// removeJSONEntry drops the MCP entry from a JSON config file, when it is ours.
func (p *op) removeJSONEntry(path string, def os.FileMode, jsonc bool, container []string, entry json.RawMessage, agent string) error {
	top, err := p.readJSONConfig(path, jsonc, agent, "remove "+opencodeSnippet)
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
	return p.writeJSON(path, top, def)
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
			p.reason = fmt.Sprintf("%s has comments: edit it by hand and run the command again", path)
			return nil, nil
		}
	}
	return top, nil
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
	b, err := json.Marshal(child)
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
	b, err := json.Marshal(child)
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

func (p *op) registerCodexMCP() error {
	path := p.codexMCPFile()
	data, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(data)
	var parsed map[string]any
	if strings.TrimSpace(text) != "" {
		if err := toml.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("%s is not valid TOML: %w", path, err)
		}
	}
	existing, found := tomlServer(parsed)
	note := "mcp_servers.teknoir"
	switch {
	case found && tomlServerIsOurs(existing):
		p.r.act(ActionAlreadyRegister, path, string(AgentCodex), note)
		return nil
	case found && !p.o.Force:
		p.r.act(ActionSkip, path, string(AgentCodex), note+" differs: kept it (use --force to replace it)")
		p.r.warn("%s already has a different [mcp_servers.%s]; kept it (use --force to replace it)", path, mcpServerName)
		return nil
	case found:
		start, end, ok := tomlTableRange(text, "mcp_servers."+mcpServerName)
		if !ok {
			return fmt.Errorf("%s defines mcp_servers.%s without a [mcp_servers.%s] table: replace it by hand", path, mcpServerName, mcpServerName)
		}
		text = text[:start] + tomlReplacement(tomlEntryBlock, text[end:]) + text[end:]
		note += ": replaced a differing one (--force)"
	default:
		text = appendTOML(text, tomlEntryBlock)
	}
	p.r.act(ActionRegister, path, string(AgentCodex), note)
	if p.o.DryRun {
		return nil
	}
	if err := p.overwriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	// A write that breaks the file would be worse than no entry at all: put the original back.
	if err := toml.Unmarshal([]byte(text), &parsed); err != nil {
		if existed {
			_ = p.overwriteFile(path, data, 0o644)
		} else {
			_ = os.Remove(path)
		}
		return fmt.Errorf("%s would no longer parse as TOML; it is unchanged: %w", path, err)
	}
	return nil
}

func (p *op) removeCodexMCP() error {
	path := p.codexMCPFile()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		p.r.act(ActionSkip, path, string(AgentCodex), "no config file")
		return nil
	}
	if err != nil {
		return err
	}
	var parsed map[string]any
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	existing, found := tomlServer(parsed)
	switch {
	case !found:
		p.r.act(ActionSkip, path, string(AgentCodex), "no mcp_servers."+mcpServerName)
		return nil
	case !tomlServerIsOurs(existing):
		p.r.act(ActionSkip, path, string(AgentCodex), "a different mcp_servers."+mcpServerName+": kept it")
		p.r.warn("%s has a [mcp_servers.%s] that is not the kit's; kept it", path, mcpServerName)
		return nil
	}
	text := string(data)
	start, end, ok := tomlTableRange(text, "mcp_servers."+mcpServerName)
	if !ok {
		return fmt.Errorf("%s defines mcp_servers.%s without a table header: remove it by hand", path, mcpServerName)
	}
	text = text[:start] + text[end:]
	// Drop the blank line the entry was separated by.
	text = strings.Replace(text, "\n\n\n", "\n\n", 1)
	p.r.act(ActionDelete, path, string(AgentCodex), "mcp_servers."+mcpServerName)
	if p.o.DryRun {
		return nil
	}
	return p.overwriteFile(path, []byte(text), 0o644)
}

// tomlServer returns parsed["mcp_servers"]["teknoir"].
func tomlServer(parsed map[string]any) (map[string]any, bool) {
	servers, ok := parsed["mcp_servers"].(map[string]any)
	if !ok {
		return nil, false
	}
	srv, ok := servers[mcpServerName].(map[string]any)
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

// tomlReplacement is the new table when other tables follow it: a blank line keeps them apart.
func tomlReplacement(block, rest string) string {
	if strings.TrimSpace(rest) == "" {
		return block
	}
	return block + "\n"
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

var tomlHeaderRE = regexp.MustCompile(`^[ \t]*\[([^\[\]]*)\][ \t]*(#.*)?$`)

// tomlCanonicalName strips spaces and quotes from the parts of a dotted name:
// `mcp_servers."teknoir"` is mcp_servers.teknoir.
func tomlCanonicalName(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

// tomlTableRange returns the byte range of the [name] table: its header line to the next table header
// (or the end of the text).
func tomlTableRange(text, name string) (int, int, bool) {
	start := -1
	off := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if m := tomlHeaderRE.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			if start < 0 {
				if tomlCanonicalName(m[1]) == name {
					start = off
				}
			} else {
				return start, off, true
			}
		}
		off += len(line)
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(text), true
}
