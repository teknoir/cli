package agentkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// RootStatus is one root that holds an install of the kit.
type RootStatus struct {
	Root            string   `json:"root"`
	Agent           string   `json:"agent,omitempty"`
	Kind            string   `json:"kind"`
	KitVersion      string   `json:"kitVersion"`
	EmbeddedVersion string   `json:"embeddedVersion"`
	VersionOutdated bool     `json:"versionOutdated,omitempty"`
	Missing         []string `json:"missing,omitempty"`
	Changed         []string `json:"changed,omitempty"`
	Outdated        []string `json:"outdated,omitempty"`
	Extra           []string `json:"extra,omitempty"`
}

// InSync reports whether the root holds exactly the embedded kit.
func (r *RootStatus) InSync() bool {
	return !r.VersionOutdated && len(r.Missing) == 0 && len(r.Changed) == 0 && len(r.Outdated) == 0 && len(r.Extra) == 0
}

// MCPStatus is one agent config file, and whether it registers the teknoir MCP server.
type MCPStatus struct {
	Path       string `json:"path"`
	Agent      string `json:"agent"`
	Registered bool   `json:"registered"`
	Note       string `json:"note,omitempty"`
}

// ListResult is what `tnctl agent list` reports for one scope.
type ListResult struct {
	Roots []RootStatus `json:"roots"`
	MCP   []MCPStatus  `json:"mcp"`
}

// InSync reports whether every root holds the embedded kit.
func (l *ListResult) InSync() bool {
	for i := range l.Roots {
		if !l.Roots[i].InSync() {
			return false
		}
	}
	return true
}

// List reports the installs of the scope of the options: every root that has a manifest, and which
// agent config files register the teknoir MCP server.
func List(o *Options) (*ListResult, error) {
	p := newOp(o)
	if err := p.validate(); err != nil {
		return nil, err
	}
	res := &ListResult{Roots: []RootStatus{}, MCP: []MCPStatus{}}
	for _, r := range p.allRoots(p.has(AgentClaude)) {
		st, ok, err := p.rootStatus(r)
		if err != nil {
			return nil, err
		}
		if ok {
			res.Roots = append(res.Roots, *st)
		}
	}
	if p.o.Scope != ScopeManaged {
		for _, a := range p.o.Agents {
			res.MCP = append(res.MCP, p.mcpStatus(a))
		}
	}
	return res, nil
}

// rootStatus compares one root with the embedded kit. A root without a manifest was never installed and
// is not reported.
func (p *op) rootStatus(r root) (*RootStatus, bool, error) {
	mfPath := manifestPath(r.Dir)
	m, err := readManifest(mfPath)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(mfPath); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	kind := rootKind(r.Dir)
	kit, err := p.kitFiles(kind)
	if err != nil {
		return nil, false, fmt.Errorf("read the kit: %w", err)
	}
	st := &RootStatus{
		Root:            r.Dir,
		Agent:           agentLabel(r.Agents),
		Kind:            kind,
		KitVersion:      m.KitVersion,
		EmbeddedVersion: p.o.KitVersion,
		VersionOutdated: m.KitVersion != p.o.KitVersion,
	}
	for _, rel := range sortedKeys(m.Files) {
		want, inKit := kit[rel]
		if !inKit {
			st.Extra = append(st.Extra, rel)
			continue
		}
		if m.Files[rel] != hashBytes(want) {
			st.Outdated = append(st.Outdated, rel)
		}
		cur, err := os.ReadFile(joinRel(r.Dir, rel))
		if errors.Is(err, fs.ErrNotExist) {
			st.Missing = append(st.Missing, rel)
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if hashBytes(cur) != m.Files[rel] {
			st.Changed = append(st.Changed, rel)
		}
	}
	for _, rel := range sortedKeys(kit) {
		if _, ok := m.Files[rel]; ok {
			continue
		}
		// A kit file the manifest does not know was never installed.
		if _, err := os.Stat(joinRel(r.Dir, rel)); errors.Is(err, fs.ErrNotExist) {
			st.Missing = append(st.Missing, rel)
		} else {
			st.Changed = append(st.Changed, rel)
		}
	}
	return st, true, nil
}

// mcpStatus reports whether an agent's config file registers the teknoir MCP server.
func (p *op) mcpStatus(a Agent) MCPStatus {
	st := MCPStatus{Agent: string(a)}
	var path string
	switch a {
	case AgentClaude:
		path, _ = p.claudeMCPFile()
	case AgentCodex:
		path = p.codexMCPFile()
	default:
		path, _ = p.opencodeMCPFile()
	}
	st.Path = path
	data, err := os.ReadFile(path)
	if err != nil {
		st.Note = "no config file"
		return st
	}
	switch a {
	case AgentCodex:
		var parsed map[string]any
		if err := toml.Unmarshal(data, &parsed); err != nil {
			st.Note = "not valid TOML"
			return st
		}
		srv, found := tomlServer(parsed)
		st.Registered = found && tomlServerIsOurs(srv)
	case AgentClaude:
		st.Registered = p.jsonHasEntry(data, []string{"mcpServers"}, claudeMCPEntry)
	case AgentOpencode:
		st.Registered = p.jsonHasEntry(data, []string{"mcp", "servers"}, opencodeMCPEntry)
	}
	return st
}

// jsonHasEntry reports whether data registers the entry where it belongs; a JSONC file with comments is
// reported as not registered.
func (p *op) jsonHasEntry(data []byte, container []string, entry []byte) bool {
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return false
	}
	cur, err := jsonLookup(top, container, mcpServerName)
	if err != nil || cur == nil {
		return false
	}
	return jsonEqual(cur, entry)
}
