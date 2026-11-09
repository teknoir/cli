package agentkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// manifest is <root>/.tnctl-agent-kit.json: what the kit wrote into that root. Files maps a path
// relative to the root (slash-separated) to "sha256:<hex>" of the content the kit wrote.
type manifest struct {
	KitVersion string            `json:"kitVersion"`
	Tnctl      string            `json:"tnctl"`
	Files      map[string]string `json:"files"`
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifestPath is the manifest of a root.
func manifestPath(rootDir string) string { return filepath.Join(rootDir, manifestName) }

// joinRel is the path of a manifest entry (slash-separated) below a root.
func joinRel(rootDir, rel string) string { return filepath.Join(rootDir, filepath.FromSlash(rel)) }

// readManifest reads the manifest of a root. A missing file is an empty manifest, not an error.
func readManifest(path string) (*manifest, error) {
	m := &manifest{Files: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%s is not a kit manifest: %w", path, err)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m, nil
}

// writeManifest writes the manifest, with the files in sorted order, unless it is already there
// unchanged or this is a dry run.
func (p *op) writeManifest(path string, m *manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	return p.writeFile(path, b, 0o644)
}

// writeFile writes one file atomically: a temp file in the same directory, then a rename. A dry run
// writes nothing.
func (p *op) writeFile(path string, content []byte, mode os.FileMode) error {
	if p.o.DryRun {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tnctl-agent-kit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// overwriteFile writes a file the kit does not install into the usual way: an existing file keeps its
// mode, a missing one gets def.
func (p *op) overwriteFile(path string, content []byte, def os.FileMode) error {
	mode := def
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return p.writeFile(path, content, mode)
}

// remove deletes a file (nothing with DryRun) and prunes the directories that became empty, up to the
// root but not the root itself.
func (p *op) remove(path, root string) error {
	if p.o.DryRun {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pruneEmptyDirs(filepath.Dir(path), root)
	return nil
}

func pruneEmptyDirs(dir, root string) {
	root = filepath.Clean(root)
	for {
		if filepath.Clean(dir) == root || !isBelow(dir, root) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// isBelow reports whether path is below root (both cleaned).
func isBelow(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !bytes.HasPrefix([]byte(rel), []byte(".."))
}

// installSet installs one set of files into a root, following the manifest rules, and writes the new
// manifest last.
func (p *op) installSet(rootDir string, files map[string][]byte, agent string) error {
	mfPath := manifestPath(rootDir)
	old, err := readManifest(mfPath)
	if err != nil {
		return err
	}
	next := &manifest{KitVersion: p.o.KitVersion, Tnctl: p.o.TnctlVersion, Files: map[string]string{}}
	for _, rel := range sortedKeys(files) {
		content := files[rel]
		target := joinRel(rootDir, rel)
		want := hashBytes(content)
		cur, err := os.ReadFile(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			p.r.act(ActionWrite, target, agent, "new")
			if err := p.writeFile(target, content, modeFor(rel, content)); err != nil {
				return err
			}
			next.Files[rel] = want
		case err != nil:
			return err
		case bytes.Equal(cur, content):
			p.r.act(ActionKeep, target, agent, "up to date")
			next.Files[rel] = want
		default:
			recorded := old.Files[rel]
			switch {
			case recorded == hashBytes(cur):
				p.r.act(ActionUpgrade, target, agent, "older kit version")
				if err := p.writeFile(target, content, modeFor(rel, content)); err != nil {
					return err
				}
				next.Files[rel] = want
			case p.o.Force:
				p.r.act(ActionWrite, target, agent, "forced over a changed file")
				if err := p.writeFile(target, content, modeFor(rel, content)); err != nil {
					return err
				}
				next.Files[rel] = want
			default:
				p.r.act(ActionRefuse, target, agent, "changed since it was installed: use --force to overwrite")
				p.refused = append(p.refused, target)
				// The kit's hash is recorded: a later run refuses again until --force, and uninstall
				// keeps the file the user changed.
				next.Files[rel] = want
			}
		}
	}
	// Files the kit no longer has: delete what is untouched, keep and report what the user changed.
	for _, rel := range sortedKeys(old.Files) {
		if _, ok := files[rel]; ok {
			continue
		}
		target := joinRel(rootDir, rel)
		cur, err := os.ReadFile(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if hashBytes(cur) == old.Files[rel] {
			p.r.act(ActionDelete, target, agent, "no longer in the kit")
			if err := p.remove(target, rootDir); err != nil {
				return err
			}
			continue
		}
		p.r.act(ActionKeep, target, agent, "no longer in the kit, but changed: kept")
		next.Files[rel] = old.Files[rel]
	}
	return p.writeManifest(mfPath, next)
}

// uninstallSet deletes the files a root's manifest records, while they still match it, then removes the
// manifest.
func (p *op) uninstallSet(rootDir string, agent string) error {
	mfPath := manifestPath(rootDir)
	m, err := readManifest(mfPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(mfPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, rel := range sortedKeys(m.Files) {
		target := joinRel(rootDir, rel)
		cur, err := os.ReadFile(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if hashBytes(cur) != m.Files[rel] {
			p.r.act(ActionKeep, target, agent, "changed since it was installed: kept")
			continue
		}
		p.r.act(ActionDelete, target, agent, "")
		if err := p.remove(target, rootDir); err != nil {
			return err
		}
	}
	p.r.act(ActionDelete, mfPath, agent, "manifest")
	return p.remove(mfPath, filepath.Dir(rootDir))
}

// settingsSnippet is the PreToolUse entry --hooks adds to a Claude Code settings.json, for a guard at
// path, as one JSON object.
func settingsSnippet(guard string) string {
	return fmt.Sprintf(`{"hooks":{"PreToolUse":[%s]}}`, preToolUseEntry(guard))
}

func preToolUseEntry(guard string) string {
	return fmt.Sprintf(`{"matcher":%q,"hooks":[{"type":"command","command":%q}]}`, guardMatcher, guard)
}

// guardMatcher is the tool matcher of the guard: shell commands and every tool of the teknoir MCP
// server, the read-only one and any other.
const guardMatcher = "Bash|mcp__teknoir__.*"
