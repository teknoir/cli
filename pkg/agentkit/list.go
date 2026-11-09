package agentkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// Problem says why the root was not read at all: a manifest with an entry that is not a path
	// inside the root, or a project root that is a symlink.
	Problem string `json:"problem,omitempty"`
}

// InSync reports whether the root holds exactly the embedded kit.
func (r *RootStatus) InSync() bool {
	return r.Problem == "" && !r.VersionOutdated && len(r.Missing) == 0 && len(r.Changed) == 0 &&
		len(r.Outdated) == 0 && len(r.Extra) == 0
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
	// A project root that is a symlink is reported, and nothing below it is read.
	links := p.projectSymlinks()
	for _, r := range projectRootPaths(p.o.ProjectDir) {
		if why, ok := links[r.Dir]; ok {
			res.Roots = append(res.Roots, RootStatus{
				Root: r.Dir, Agent: agentLabel(r.Agents), Kind: rootKind(r.Dir),
				EmbeddedVersion: p.o.KitVersion, Problem: why,
			})
		}
	}
	for _, r := range p.allRoots(p.has(AgentClaude)) {
		if belowAny(r.Dir, links) {
			continue
		}
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
// is not reported. Every file is read through the root, so nothing below it leads out of it, and a file
// that is not a regular file (a symlink, a directory) is reported as changed without being read.
func (p *op) rootStatus(r root) (*RootStatus, bool, error) {
	mfPath := manifestPath(r.Dir)
	rr, err := openRoot(r.Dir)
	if err != nil || rr == nil {
		if err == nil && p.o.Scope == ScopeManaged {
			// A managed install is checked as a whole: a root that is not there has every file missing.
			return p.absentRoot(r)
		}
		// A root that does not exist holds nothing.
		return nil, false, err
	}
	defer rr.Close()
	_, exists, err := statRel(rr, manifestName)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		if p.o.Scope == ScopeManaged {
			return p.absentRoot(r)
		}
		return nil, false, nil
	}
	kind := rootKind(r.Dir)
	st := &RootStatus{
		Root:            r.Dir,
		Agent:           agentLabel(r.Agents),
		Kind:            kind,
		EmbeddedVersion: p.o.KitVersion,
	}
	m, err := readManifestAt(rr, mfPath)
	if err != nil {
		var ke *manifestKeyError
		if errors.As(err, &ke) {
			st.Problem = ke.Error()
			return st, true, nil
		}
		return nil, false, err
	}
	st.KitVersion = m.KitVersion
	st.VersionOutdated = m.KitVersion != p.o.KitVersion
	kit, err := p.kitFiles(kind)
	if err != nil {
		return nil, false, fmt.Errorf("read the kit: %w", err)
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
		fi, exists, err := statRel(rr, rel)
		if err != nil {
			return nil, false, err
		}
		if !exists && blockedPrefix(rr, rel) == "" {
			st.Missing = append(st.Missing, rel)
			continue
		}
		if !regular(fi) {
			st.Changed = append(st.Changed, rel)
			continue
		}
		cur, err := rr.ReadFile(rel)
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
		_, exists, err := statRel(rr, rel)
		if err != nil {
			return nil, false, err
		}
		if !exists && blockedPrefix(rr, rel) == "" {
			st.Missing = append(st.Missing, rel)
		} else {
			// There, or blocked by a file or a link where a directory belongs: install refuses it.
			st.Changed = append(st.Changed, rel)
		}
	}
	return st, true, nil
}

// absentRoot is a managed root without a manifest: the install is not there, and every kit file is
// missing from it.
func (p *op) absentRoot(r root) (*RootStatus, bool, error) {
	kind := rootKind(r.Dir)
	kit, err := p.kitFiles(kind)
	if err != nil {
		return nil, false, fmt.Errorf("read the kit: %w", err)
	}
	return &RootStatus{
		Root: r.Dir, Agent: agentLabel(r.Agents), Kind: kind, EmbeddedVersion: p.o.KitVersion,
		Missing: sortedKeys(kit),
	}, true, nil
}

// belowAny reports whether path is at or below one of the links.
func belowAny(path string, links map[string]string) bool {
	for dir := range links {
		if atOrBelow(path, dir) {
			return true
		}
	}
	return false
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
	// list --project reads nothing below a project root that is a symlink, the MCP config file included.
	for dir, why := range p.projectSymlinks() {
		if atOrBelow(path, dir) {
			st.Note = why
			return st
		}
	}
	// A symlinked config file of a cloned project is not followed: it could lead outside the project, or
	// to a device that never ends.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 && p.o.Scope == ScopeProject {
		st.Note = path + " is a symlink: tnctl does not read it"
		return st
	}
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		st.Note = "not a regular file"
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		st.Note = "no config file"
		return st
	}
	switch a {
	case AgentCodex:
		parsed, err := tomlParse(string(data))
		if err != nil {
			st.Note = "not valid TOML"
			return st
		}
		srv, found := tomlServer(parsed)
		st.Registered = found && tomlServerIsOurs(srv)
		if found && !st.Registered && tomlServerIsKits(srv) {
			fix := "tnctl agent install --agent codex --mcp --force"
			if p.o.Scope == ScopeProject {
				fix = "tnctl agent install --project --agent codex --mcp --force"
			}
			st.Note = "the entry of an older tnctl, without env_vars: run " + fix
		}
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
