package agentkit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
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

// joinRel is the path of a manifest entry (slash-separated) below a root, for messages and actions. Every
// read, write and remove below a root goes through an os.Root instead.
func joinRel(rootDir, rel string) string { return filepath.Join(rootDir, filepath.FromSlash(rel)) }

// manifestKeyError is a manifest the kit cannot use: an entry that is not a path inside its root, or a
// manifest that is not a regular file (a symlink, a directory) and can therefore not be read through the
// root. The whole root is refused: a manifest is never edited, and nothing below it is touched.
type manifestKeyError struct {
	Path string
	Key  string
	// NotRegular marks a manifest that is not a regular file.
	NotRegular bool
}

func (e *manifestKeyError) Error() string {
	if e.NotRegular {
		return fmt.Sprintf("%s: not a regular file; fix or delete the manifest by hand", e.Path)
	}
	return fmt.Sprintf("%s: entry %q is not a path inside its root; fix or delete the manifest by hand", e.Path, e.Key)
}

// validManifestKey reports whether rel names a file the kit can have written below a root of kind: a
// path of its own, never the manifest, never with a backslash, a NUL byte or a "..", and of the shape the
// kit writes (a skill file <skill>/<file>, or the guard alone in a hooks root).
func validManifestKey(rel, kind string) bool {
	if rel == "" || rel == manifestName || strings.Contains(rel, `\`) || strings.ContainsRune(rel, 0) ||
		path.Clean(rel) != rel || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false
	}
	if kind == kindHooks {
		return rel == guardName
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// readManifest reads the manifest of a root. A missing file is an empty manifest, not an error. An entry
// that is not a path inside the root is a *manifestKeyError.
func readManifest(path string) (*manifest, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &manifest{Files: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseManifest(path, b)
}

// readManifestAt reads the manifest of a root through it, so a manifest that leads out of the root is not
// followed. A missing manifest is an empty one; one that is not a regular file is a *manifestKeyError,
// because a link out of the root cannot be read through it.
func readManifestAt(r *os.Root, path string) (*manifest, error) {
	fi, exists, _ := statRel(r, manifestName)
	if !exists {
		return &manifest{Files: map[string]string{}}, nil
	}
	if !regular(fi) {
		return nil, &manifestKeyError{Path: path, NotRegular: true}
	}
	b, err := r.ReadFile(manifestName)
	if err != nil {
		return nil, err
	}
	return parseManifest(path, b)
}

func parseManifest(path string, b []byte) (*manifest, error) {
	m := &manifest{Files: map[string]string{}}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%s is not a kit manifest: %w", path, err)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	kind := rootKind(filepath.Dir(path))
	for _, rel := range sortedKeys(m.Files) {
		if !validManifestKey(rel, kind) {
			return nil, &manifestKeyError{Path: path, Key: rel}
		}
	}
	return m, nil
}

// refuseManifestKey refuses the whole root of err when it is a manifestKeyError, and reports whether it
// did. Nothing below the root is touched.
func (p *op) refuseManifestKey(path, agent string, err error) bool {
	var ke *manifestKeyError
	if !errors.As(err, &ke) {
		return false
	}
	why := ke.Error()
	p.refuse(path, agent, why, why)
	return true
}

// writeManifest writes the manifest below the root, unless it is already there unchanged or this is a
// dry run.
func (p *op) writeManifest(r *os.Root, m *manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if old, err := r.ReadFile(manifestName); err == nil && bytes.Equal(old, b) {
		return nil
	}
	return p.writeRel(r, manifestName, b, 0o644)
}

// installSet installs one set of files into a root, following the manifest rules, and writes the new
// manifest last.
func (p *op) installSet(rootDir string, files map[string][]byte, agent string) error {
	mfPath := manifestPath(rootDir)
	r, err := openInstallRoot(rootDir, p.o.DryRun)
	if err != nil {
		return err
	}
	if r == nil {
		// A dry run over a root that does not exist: every kit file would be written.
		for _, rel := range sortedKeys(files) {
			p.r.act(ActionWrite, joinRel(rootDir, rel), agent, "new")
		}
		return nil
	}
	defer r.Close()
	old, err := readManifestAt(r, mfPath)
	if err != nil {
		if p.refuseManifestKey(mfPath, agent, err) {
			return nil
		}
		return err
	}
	next := &manifest{KitVersion: p.o.KitVersion, Tnctl: p.o.TnctlVersion, Files: map[string]string{}}
	for _, rel := range sortedKeys(files) {
		content := files[rel]
		target := joinRel(rootDir, rel)
		want := hashBytes(content)
		fi, exists, err := statRel(r, rel)
		if err != nil {
			return err
		}
		if !exists {
			if blocked := blockedPrefix(r, rel); blocked != "" {
				// A file or a link where a directory belongs: never written through or replaced.
				note := "not a regular file: not written; --force does not replace it"
				p.refuse(target, agent, note, target+": "+note)
				next.Files[rel] = want
				continue
			}
			p.r.act(ActionWrite, target, agent, "new")
			if err := p.writeRel(r, rel, content, modeFor(rel, content)); err != nil {
				return err
			}
			next.Files[rel] = want
			continue
		}
		if !regular(fi) {
			// A symlink, a directory or a device: never written through or replaced, even with
			// --force. The kit's hash is recorded, so a later run refuses again and uninstall keeps
			// the file.
			note := "not a regular file: not written; --force does not replace it"
			p.refuse(target, agent, note, target+": "+note)
			next.Files[rel] = want
			continue
		}
		cur, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		switch {
		case bytes.Equal(cur, content):
			p.r.act(ActionKeep, target, agent, "up to date")
			next.Files[rel] = want
		case old.Files[rel] == hashBytes(cur):
			p.r.act(ActionUpgrade, target, agent, "older kit version")
			if err := p.writeRel(r, rel, content, modeFor(rel, content)); err != nil {
				return err
			}
			next.Files[rel] = want
		case p.o.Force:
			p.r.act(ActionWrite, target, agent, "forced over a changed file")
			if err := p.writeRel(r, rel, content, modeFor(rel, content)); err != nil {
				return err
			}
			next.Files[rel] = want
		default:
			// The kit's hash is recorded: a later run refuses again until --force, and uninstall
			// keeps the file the user changed.
			p.refuseChanged(target, agent)
			next.Files[rel] = want
		}
	}
	// Files the kit no longer has: delete what is untouched, keep and report what the user changed.
	for _, rel := range sortedKeys(old.Files) {
		if _, ok := files[rel]; ok {
			continue
		}
		target := joinRel(rootDir, rel)
		fi, exists, err := statRel(r, rel)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if !regular(fi) {
			p.r.act(ActionKeep, target, agent, "not a regular file: kept")
			p.r.warn("%s is not a regular file: kept it", target)
			next.Files[rel] = old.Files[rel]
			continue
		}
		cur, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		if hashBytes(cur) == old.Files[rel] {
			p.r.act(ActionDelete, target, agent, "no longer in the kit")
			if err := p.removeRel(r, rel); err != nil {
				return err
			}
			continue
		}
		p.r.act(ActionKeep, target, agent, "no longer in the kit, but changed: kept")
		next.Files[rel] = old.Files[rel]
	}
	return p.writeManifest(r, next)
}

// uninstallSet deletes the files a root's manifest records, while they still match it, then removes the
// manifest and, when the root is left empty, the root too. A root without a manifest is not the kit's and
// is left as it is.
func (p *op) uninstallSet(rootDir string, agent string) error {
	r, err := openRoot(rootDir)
	if err != nil || r == nil {
		// A root that does not exist holds nothing.
		return err
	}
	removed, err := p.uninstallRoot(r, rootDir, agent)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if removed && !p.o.DryRun {
		removeEmptyRoot(rootDir)
	}
	return nil
}

// uninstallRoot is uninstallSet for an open root. removed reports whether the manifest was removed: only a
// root the kit installed into is deleted when the uninstall leaves it empty, never an empty directory the
// kit never wrote to.
func (p *op) uninstallRoot(r *os.Root, rootDir, agent string) (removed bool, err error) {
	mfPath := manifestPath(rootDir)
	_, exists, err := statRel(r, manifestName)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	m, err := readManifestAt(r, mfPath)
	if err != nil {
		if p.refuseManifestKey(mfPath, agent, err) {
			return false, nil
		}
		return false, err
	}
	for _, rel := range sortedKeys(m.Files) {
		target := joinRel(rootDir, rel)
		fi, exists, err := statRel(r, rel)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		if !regular(fi) {
			p.r.act(ActionKeep, target, agent, "not a regular file: kept")
			p.r.warn("%s is not a regular file: kept it", target)
			continue
		}
		cur, err := r.ReadFile(rel)
		if err != nil {
			return false, err
		}
		if hashBytes(cur) != m.Files[rel] {
			p.r.act(ActionKeep, target, agent, "changed since it was installed: kept")
			continue
		}
		p.r.act(ActionDelete, target, agent, "")
		if err := p.removeRel(r, rel); err != nil {
			return false, err
		}
	}
	p.r.act(ActionDelete, mfPath, agent, "manifest")
	if err := p.removeRel(r, manifestName); err != nil {
		return false, err
	}
	return true, nil
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
	if err := f.Sync(); err != nil {
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

// ---------- the roots: every operation below a root goes through its os.Root ----------

// tempPrefix is the name every temp file of the kit starts with.
const tempPrefix = ".tnctl-agent-kit-"

// openRoot opens dir as an os.Root, so a name with ".." or a symlink that leads out of the root cannot
// be followed. A missing directory is not an error: the root is nil.
func openRoot(dir string) (*os.Root, error) {
	r, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// openInstallRoot is openRoot for an install: it creates the directory first, except in a dry run,
// which creates nothing.
func openInstallRoot(dir string, dryRun bool) (*os.Root, error) {
	if !dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return openRoot(dir)
}

// statRel looks up rel below the root. exists is false when there is nothing there. Every other error
// os.Root can give (a name or a symlink that leads out of the root, a loop, ENOTDIR, EINVAL, EACCES) is
// reported as existing with a nil FileInfo, which is not a regular file: the caller never writes through,
// replaces or deletes such a path, and a single one never makes the run fail.
func statRel(r *os.Root, rel string) (fi os.FileInfo, exists bool, err error) {
	fi, err = r.Lstat(rel)
	switch {
	case err == nil:
		return fi, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	}
	return nil, true, nil
}

// regular reports whether fi is a regular file. A nil fi (a path that escapes the root) is not.
func regular(fi os.FileInfo) bool { return fi != nil && fi.Mode().IsRegular() }

// blockedPrefix returns the first directory below the root on the way to rel that exists and is not a
// real directory: a file, or a symlink whether it dangles or not. A file cannot be written there, so the
// kit refuses it rather than failing the run; "" means the way is clear.
func blockedPrefix(r *os.Root, rel string) string {
	dir := path.Dir(rel)
	if dir == "." {
		return ""
	}
	segs := strings.Split(dir, "/")
	for i := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		fi, err := r.Lstat(prefix)
		if err != nil {
			// Not there: what is below it cannot be either. Any other error is left to the write.
			continue
		}
		if !fi.IsDir() {
			return prefix
		}
	}
	return ""
}

// writeRel writes content to rel below the root atomically: a temp file next to it, renamed over it. A
// dry run writes nothing.
func (p *op) writeRel(r *os.Root, rel string, content []byte, mode os.FileMode) error {
	if p.o.DryRun {
		return nil
	}
	dir := path.Dir(rel)
	if dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, f, err := createTempRel(r, dir, mode)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		r.Remove(tmp)
		return err
	}
	if _, err := f.Write(content); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		r.Remove(tmp)
		return err
	}
	if err := r.Chmod(tmp, mode); err != nil {
		r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		r.Remove(tmp)
		return err
	}
	return nil
}

// createTempRel creates a new file in dir below the root, with a fresh random name.
func createTempRel(r *os.Root, dir string, mode os.FileMode) (string, *os.File, error) {
	for i := 0; i < 16; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		name := tempPrefix + hex.EncodeToString(b[:])
		if dir != "." {
			name = dir + "/" + name
		}
		f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return name, f, err
	}
	return "", nil, errors.New("agentkit: cannot create a temp file below the root")
}

// removeRel deletes rel below the root and prunes the directories that became empty, up to the root but
// not the root itself.
func (p *op) removeRel(r *os.Root, rel string) error {
	if p.o.DryRun {
		return nil
	}
	if err := r.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pruneEmptyDirs(r, path.Dir(rel))
	return nil
}

// pruneEmptyDirs removes the relative directories that became empty, up to the root but not the root.
func pruneEmptyDirs(r *os.Root, dir string) {
	for {
		dir = path.Clean(dir)
		if dir == "." || dir == "/" || dir == "" || dir == ".." || strings.HasPrefix(dir, "../") {
			return
		}
		if !dirEmpty(r, dir) {
			return
		}
		if err := r.Remove(dir); err != nil {
			return
		}
		dir = path.Dir(dir)
	}
}

// dirEmpty reports whether the directory dir, below the root, has no entries.
func dirEmpty(r *os.Root, dir string) bool {
	f, err := r.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return len(names) == 0
}

// removeEmptyRoot removes a root directory an uninstall left empty. Only a real directory goes, never a
// symlink: user scope follows a symlinked .claude, and the link stays.
func removeEmptyRoot(dir string) {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}

// remove deletes a file outside a root (AGENTS.md) and prunes the directories that became empty, up to
// the root but not the root itself.
func (p *op) remove(path, root string) error {
	if p.o.DryRun {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if !isBelow(dir, root) {
		return nil
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return nil
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil
	}
	defer r.Close()
	pruneEmptyDirs(r, filepath.ToSlash(rel))
	return nil
}

// isBelow reports whether path is below root (both cleaned). A name that starts with two dots is only a
// climb out when it is ".." or starts with "../": a directory named "..foo" is below.
func isBelow(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ---------- foreign files: the config files and AGENTS.md outside a root ----------

// foreignTarget returns the file an edit of a config file or of AGENTS.md goes to, and why the path may
// not be edited at all. It runs before the file is read and before any action is recorded.
//
// A symlinked file is never replaced by a regular one: in user scope the edit goes to the link's target
// (dotfiles, stow, home-manager keep working), and in project scope the link is refused, so tnctl never
// writes through a link into or out of a repository. AGENTS.md is the exception: a link to a Markdown file
// of the project outside .git is followed, because a repository may keep AGENTS.md -> CLAUDE.md.
//
// The reason is verb-neutral, ending in "edit it by hand:", so the caller can append "add <snippet>" on
// an install or "remove <snippet>" on an uninstall.
func (p *op) foreignTarget(path string) (target, why string) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, ""
	}
	if err != nil {
		return "", fmt.Sprintf("%s: %v: edit it by hand:", path, err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return path, ""
	}
	real, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Sprintf("%s is a dangling symlink: edit it by hand:", path)
	}
	if err != nil {
		return "", fmt.Sprintf("%s: %v: edit it by hand:", path, err)
	}
	if p.o.Scope == ScopeProject && filepath.Clean(path) == filepath.Clean(p.agentsMDPath()) {
		proj, err := filepath.EvalSymlinks(p.o.ProjectDir)
		switch {
		case err != nil || !isBelow(real, proj):
			return "", fmt.Sprintf("AGENTS.md is a symlink to %s outside the project: edit it by hand:", real)
		case !regularAt(real):
			return "", fmt.Sprintf("AGENTS.md is a symlink to %s, which is not a regular file: edit it by hand:", real)
		}
		// A link into .git, or to anything that is not Markdown, would have the kit write into a file the
		// repository cannot ship: only a Markdown file of the project is followed.
		rel, _ := filepath.Rel(proj, real)
		segs := strings.Split(filepath.ToSlash(rel), "/")
		if !strings.EqualFold(filepath.Ext(real), ".md") ||
			slices.ContainsFunc(segs, func(s string) bool { return strings.EqualFold(s, ".git") }) {
			return "", fmt.Sprintf("AGENTS.md is a symlink to %s, which is not a Markdown file of the project: edit it by hand:", real)
		}
		return real, ""
	}
	if p.o.Scope == ScopeProject {
		return "", fmt.Sprintf("%s is a symlink: edit it by hand:", path)
	}
	if !regularAt(real) {
		return "", fmt.Sprintf("%s is a symlink to %s, which is not a regular file: edit it by hand:", path, real)
	}
	if !dirWritable(filepath.Dir(real)) {
		return "", fmt.Sprintf("%s is a symlink to %s, in a directory tnctl cannot write (a read-only store): edit it by hand:", path, real)
	}
	return real, ""
}

// regularAt reports whether path is a regular file (following no link: EvalSymlinks already did).
func regularAt(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// preToolUseHandler is one handler of the guard's PreToolUse group.
type preToolUseHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// preToolUseGroup is the guard's PreToolUse group: it runs the guard for the tools of guardMatcher.
type preToolUseGroup struct {
	Matcher string              `json:"matcher"`
	Hooks   []preToolUseHandler `json:"hooks"`
}

// preToolUseEntry is the PreToolUse group --hooks adds to a Claude Code settings.json, for a command.
func preToolUseEntry(command string) ([]byte, error) {
	return marshalNoEscape(preToolUseGroup{Matcher: guardMatcher, Hooks: []preToolUseHandler{{Type: "command", Command: command}}})
}

// settingsSnippetType is the part of a settings.json the guard's entry lives in.
type settingsSnippetType struct {
	Hooks struct {
		PreToolUse []json.RawMessage `json:"PreToolUse"`
	} `json:"hooks"`
}

// settingsSnippet is the PreToolUse entry --hooks adds to a Claude Code settings.json, for a command, as
// one JSON object. It cannot fail: every value of it is a string.
func settingsSnippet(command string) string {
	group, err := preToolUseEntry(command)
	if err != nil {
		return ""
	}
	var v settingsSnippetType
	v.Hooks.PreToolUse = []json.RawMessage{group}
	b, err := marshalNoEscape(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// marshalNoEscape is json.Marshal without the HTML escaping of &, < and >: a foreign value the kit
// writes back keeps its bytes.
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// guardMatcher is the tool matcher of the guard: shell commands and every tool of the teknoir MCP
// server, whichever way it is installed (the read-only server, and the plugin that carries the kit).
const guardMatcher = "Bash|mcp__teknoir__.*|mcp__plugin_teknoir_teknoir__.*"

// legacyGuardMatcher is the matcher cd80af0 registered the guard under.
const legacyGuardMatcher = "Bash|mcp__teknoir__.*"
