package council

// The guides of the council's custom kit: a skill book read once per daemon from the kit directory
// the bot's computer installed the guides in, falling back to the guides embedded in tnctl.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/teknoir/cli/agentkit"
)

// kitSkillsDir is where the kit keeps its guides, inside the kit's own root.
const kitSkillsDir = "skills"

// kitSkillName is the rule for a name in X-Teknoir-Council-Skills (spec 5.4). A guide whose
// directory name breaks it can never be named by a role, so the book leaves it out: one stray
// directory must not refuse every cap the daemon announces.
var kitSkillName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// The limits of the skills header (spec 5.4), mirrored from hub/caps.go.
const (
	maxAdvertisedSkills = 64
	maxSkillsHeaderLen  = 2048
)

// skillBook is the guides of one kit, read once: what each says, for the digest a prompt carries,
// the page read_skill gives, and the names the council may use.
type skillBook struct {
	// skills are the guides the book carries, by name: read, and named as the caps announce them.
	skills map[string]skill
	// known are the guides of the known kit order that the book carries.
	known []string
	// extra are the other guides the book carries, sorted.
	extra []string
}

// newSkillBook reads every entry under skills/ of fsys whose <name>/SKILL.md can be read: a
// symlinked directory counts, and a plain file is left out because its SKILL.md cannot be read. An
// entry whose name breaks the caps rule is left out, and so is an extra guide past the header's
// limits. A guide left out is not readable either, so what the book holds is what the caps announce.
func newSkillBook(fsys fs.FS) *skillBook {
	b := &skillBook{skills: map[string]skill{}}
	if fsys == nil {
		return b
	}
	entries, err := fs.ReadDir(fsys, kitSkillsDir)
	if err != nil {
		return b
	}
	read := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !kitSkillName.MatchString(name) {
			continue // the hub could never name it, and advertising it would refuse every cap
		}
		data, err := fs.ReadFile(fsys, path.Join(kitSkillsDir, name, "SKILL.md"))
		if err != nil {
			continue
		}
		description, body := splitSkill(string(data))
		b.skills[name] = skill{
			description: cutUTF8(description, maxSkillDescription, "…"),
			text:        body,
			digest:      cutUTF8(body, maxSkillDigest, "…"),
		}
		read[name] = true
	}
	for _, name := range skillNames {
		if read[name] {
			b.known = append(b.known, name)
		}
	}
	extra := make([]string, 0, len(read))
	for name := range read {
		if !slices.Contains(skillNames, name) {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	// Keep the extras within the header's limits, dropping them in sorted order; the known kit
	// order always fits. A dropped guide goes from the book too, so neither the digest nor
	// read_skill serves one the caps did not announce.
	n, size := len(b.known), len(strings.Join(b.known, ","))
	keep := 0
	for _, name := range extra {
		add := len(name)
		if n > 0 {
			add++ // the comma
		}
		if n+1 > maxAdvertisedSkills || size+add > maxSkillsHeaderLen {
			break
		}
		n, size, keep = n+1, size+add, keep+1
	}
	for _, name := range extra[keep:] {
		delete(b.skills, name)
	}
	b.extra = extra[:keep]
	return b
}

// names is the guides the book carries, in the order the council advertises them: the known kit
// order first, then any other guide it kept alphabetically.
func (b *skillBook) names() []string {
	out := make([]string, 0, len(b.known)+len(b.extra))
	out = append(out, b.known...)
	return append(out, b.extra...)
}

// digest is one line per named guide: "<name>: <description>" from its frontmatter. A name the book
// does not carry, and a guide without a description, gives no line.
func (b *skillBook) digest(names []string) string {
	lines := make([]string, 0, len(names))
	for _, name := range names {
		s, ok := b.skills[name]
		if !ok || s.description == "" {
			continue
		}
		lines = append(lines, name+": "+s.description)
	}
	return strings.Join(lines, "\n")
}

// read is one page of the SKILL.md of name without its frontmatter: at most 16 KiB from offset, cut
// back to a rune start, with a line saying the offset of the next page while there is more. The
// digest is the guide's first 200 bytes, the same on every page. A name the book does not carry, an
// offset past the guide, the offset of a non-empty guide's very end and an offset inside a
// character are errors.
func (b *skillBook) read(name string, offset int) (text, digest string, err error) {
	s, ok := b.skills[name]
	if !ok {
		return "", "", errors.New("there is no guide named " + cutUTF8(name, 64, "…") + "; use one of the guides your instructions list")
	}
	body := s.text
	if offset < 0 {
		return "", "", fmt.Errorf("offset %d is outside the guide %q: its bytes are 0..%d", offset, name, len(body))
	}
	// The range is checked before anything is read at offset: offset == len(body) is the end of the
	// guide, not a byte that can be inspected, and reading it would panic the daemon.
	if offset == len(body) {
		if body == "" {
			return "", s.digest, nil // an empty guide has one page: the empty one
		}
		return "", "", fmt.Errorf("offset %d is the end of the guide %q; there is no more", offset, name)
	}
	if offset > len(body) {
		return "", "", fmt.Errorf("offset must be below %d: the guide %q has %d bytes", len(body), name, len(body))
	}
	if !utf8.RuneStart(body[offset]) {
		return "", "", fmt.Errorf("offset %d is inside a character: use the offset a [more: ...] line gave", offset)
	}
	end := offset + maxSkillText
	if end > len(body) {
		end = len(body)
	}
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	page := body[offset:end]
	if end < len(body) {
		page += fmt.Sprintf("\n\n[more: read_skill with offset %d]", end)
	}
	return page, s.digest, nil
}

// KitFS is the guides of a bot's computer: the kit directory dir first, and the kit embedded in
// tnctl second, file by file. A directory that cannot be listed at all (missing, not a directory,
// unreadable) falls back to the embedded kit entirely, and anything of the directory that cannot be
// read falls back to the embedded kit's own file, one file at a time.
func KitFS(dir string) fs.FS {
	o := kitOverlay{}
	if d := strings.TrimSpace(dir); d != "" {
		if _, err := os.ReadDir(d); err == nil {
			o.dir = os.DirFS(d)
		}
	}
	return o
}

// kitOverlay serves one FS first and another second, file by file: a file of the first wins, and a
// listing is both, de-duplicated and sorted.
type kitOverlay struct {
	dir fs.FS
}

// Open opens name in the first FS that has it. Any error from the directory is a miss: the embedded
// kit serves the file.
func (o kitOverlay) Open(name string) (fs.File, error) {
	if o.dir != nil {
		if f, err := o.dir.Open(name); err == nil {
			return f, nil
		}
	}
	return agentkit.FS.Open(name)
}

// ReadFile reads name from the first FS that has it. Any error from the directory, and anything
// there that is not a regular file of at most maxKitFile bytes (a FIFO, a device, a huge file), is a
// miss: the embedded kit serves the file.
func (o kitOverlay) ReadFile(name string) ([]byte, error) {
	if o.dir != nil {
		if data, ok := readKitFile(o.dir, name); ok {
			return data, nil
		}
	}
	return fs.ReadFile(agentkit.FS, name)
}

// maxKitFile is the most one file of the kit directory may hold.
const maxKitFile = 1 << 20

// readKitFile reads name from fsys if it is a regular file of at most maxKitFile bytes; anything
// else (a FIFO, a device, a directory, a larger file, any error) is a miss.
func readKitFile(fsys fs.FS, name string) ([]byte, bool) {
	fi, err := fs.Stat(fsys, name) // os.Stat through os.DirFS: follows a symlink, never opens the file
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxKitFile {
		return nil, false
	}
	f, err := fsys.Open(name)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKitFile+1))
	if err != nil || len(data) > maxKitFile {
		return nil, false
	}
	return data, true
}

// ReadDir lists name in both FSs, de-duplicated and sorted. Any error from the directory is a miss.
func (o kitOverlay) ReadDir(name string) ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	seen := map[string]bool{}
	add := func(entries []fs.DirEntry) {
		for _, e := range entries {
			if seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			out = append(out, e)
		}
	}
	if o.dir != nil {
		if entries, err := fs.ReadDir(o.dir, name); err == nil {
			add(entries)
		}
	}
	entries, err := fs.ReadDir(agentkit.FS, name)
	switch {
	case err == nil:
		add(entries)
	case len(out) == 0:
		// Neither FS has it: the embedded kit's error is the one to report.
		return nil, err
	}
	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}
