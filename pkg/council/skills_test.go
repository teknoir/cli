package council

// Tests of the skill book and of the kit overlay: the guides a bot's computer has, the kit they fall
// back to, and the order the council advertises them in.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/teknoir/cli/agentkit"
	"github.com/teknoir/cli/pkg/council/hub"
)

// mapKit is an fs.FS of guides: name → body, every guide with a description.
func mapKit(bodies map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range bodies {
		m["skills/"+name+"/SKILL.md"] = &fstest.MapFile{
			Data: []byte("---\ndescription: the guide " + name + "\n---\n" + body),
		}
	}
	return m
}

// embeddedGuide is the embedded kit's SKILL.md of name.
func embeddedGuide(t *testing.T, name string) string {
	t.Helper()
	data, err := agentkit.FS.ReadFile("skills/" + name + "/SKILL.md")
	if err != nil {
		t.Fatalf("the embedded kit has no %s: %v", name, err)
	}
	return string(data)
}

// TestSkillBookNamesOrder: the known kit order first (only the guides that are present), then any
// other readable guide alphabetically.
func TestSkillBookNamesOrder(t *testing.T) {
	// Four of the seven, and two the kit does not know.
	kit := mapKit(map[string]string{
		"teknoir-robots":   "robots",
		"teknoir-platform": "platform",
		"teknoir-council":  "council",
		"teknoir-debug":    "debug",
		"zeta-extra":       "zeta",
		"alpha-extra":      "alpha",
	})
	got := newSkillBook(kit).names()
	want := []string{"teknoir-platform", "teknoir-robots", "teknoir-debug", "teknoir-council", "alpha-extra", "zeta-extra"}
	if !equal(got, want) {
		t.Errorf("names() = %v, want %v", got, want)
	}
}

// A guide without a SKILL.md, and a file (not a directory) under skills/, are left out.
func TestSkillBookSkipsUnreadable(t *testing.T) {
	kit := fstest.MapFS{
		"skills/teknoir-platform/SKILL.md": &fstest.MapFile{Data: []byte("---\ndescription: p\n---\nbody")},
		"skills/no-skill/README.md":        &fstest.MapFile{Data: []byte("nothing")},
		"skills/a-file":                    &fstest.MapFile{Data: []byte("not a guide")},
	}
	book := newSkillBook(kit)
	if got, want := book.names(), []string{"teknoir-platform"}; !equal(got, want) {
		t.Errorf("names() = %v, want %v", got, want)
	}
	if digest := book.digest(book.names()); digest != "teknoir-platform: p" {
		t.Errorf("digest = %q", digest)
	}
}

// An FS that does not exist at all has no guides, and nothing panics.
func TestSkillBookEmpty(t *testing.T) {
	for _, fsys := range []fs.FS{nil, fstest.MapFS{}} {
		book := newSkillBook(fsys)
		if got := book.names(); len(got) != 0 {
			t.Errorf("names() = %v, want none", got)
		}
		if digest := book.digest(nil); digest != "" {
			t.Errorf("digest = %q, want empty", digest)
		}
		if _, _, err := book.read("teknoir-platform", 0); err == nil {
			t.Error("read found a guide in an empty kit")
		}
	}
}

// TestSkillBookMatchesTheEmbeddedKit: the book of the embedded kit is today's behaviour, guide for
// guide: the kit order, one "<name>: <description>" line per guide, and the same pages.
func TestSkillBookMatchesTheEmbeddedKit(t *testing.T) {
	book := newSkillBook(agentkit.FS)
	if got := book.names(); !equal(got, skillNames) {
		t.Errorf("names() = %v, want the kit order %v", got, skillNames)
	}
	lines := make([]string, 0, len(skillNames))
	for _, name := range skillNames {
		description, body := splitSkill(embeddedGuide(t, name))
		if description == "" {
			t.Fatalf("%s has no description", name)
		}
		lines = append(lines, name+": "+description)

		text, digest, err := book.read(name, 0)
		if err != nil {
			t.Fatalf("read(%s): %v", name, err)
		}
		// The digest is the body's first 200 bytes, cut at a rune start.
		if want := cutUTF8(body, maxSkillDigest, "…"); digest != want {
			t.Errorf("%s: digest %q, want %q", name, digest, want)
		}
		// The page starts at the body's first byte and is at most one page of it.
		if len(body) <= maxSkillText {
			if text != body {
				t.Errorf("%s: the page is not the whole body", name)
			}
			continue
		}
		if !strings.HasPrefix(text, body[:maxSkillText-3]) {
			t.Errorf("%s: the page does not start with the body", name)
		}
		if !strings.Contains(text, "[more: read_skill with offset ") {
			t.Errorf("%s: a cut page has no [more] line", name)
		}
	}
	if got, want := book.digest(skillNames), strings.Join(lines, "\n"); got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
	// A subset gives the lines of that subset, in its order.
	if got, want := book.digest([]string{"teknoir-robots", "teknoir-platform"}), lines[1]+"\n"+lines[0]; got != want {
		t.Errorf("digest of a subset = %q, want %q", got, want)
	}
	// A name the book does not carry gives no line.
	if got := book.digest([]string{"nope", "teknoir-platform"}); got != lines[0] {
		t.Errorf("digest with an unknown name = %q", got)
	}
}

// equal compares two string slices.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------- the overlay ----------

// writeGuide writes one guide into dir.
func writeGuide(t *testing.T, dir, name, description, body string) {
	t.Helper()
	p := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ndescription: "+description+"\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The overlay serves the directory's file first, and the embedded kit's second, file by file.
func TestKitFSOverlayPrefersTheDir(t *testing.T) {
	dir := t.TempDir()
	writeGuide(t, dir, "teknoir-platform", "the platform, as the bot's kit has it", "the bot's platform guide")
	writeGuide(t, dir, "zeta-extra", "the bot's own guide", "zeta's body")
	off := KitFS(dir)

	data, err := fs.ReadFile(off, "skills/teknoir-platform/SKILL.md")
	if err != nil {
		t.Fatalf("read the platform guide: %v", err)
	}
	description, body := splitSkill(string(data))
	if description != "the platform, as the bot's kit has it" || body != "the bot's platform guide" {
		t.Errorf("the overlay gave the embedded guide, not the dir's: %q / %q", description, body)
	}
	// A guide the dir lacks comes from the embedded kit.
	data, err = fs.ReadFile(off, "skills/teknoir-robots/SKILL.md")
	if err != nil {
		t.Fatalf("read the robots guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-robots") {
		t.Error("the fallback guide is not the embedded one")
	}
	// A file neither has is not there.
	if _, err := fs.ReadFile(off, "skills/nope/SKILL.md"); err == nil {
		t.Error("the overlay read a guide neither kit has")
	}
	// The book of the overlay: the kit order with the dir's platform guide, then the dir's own.
	book := newSkillBook(off)
	want := append(append([]string{}, skillNames...), "zeta-extra")
	if got := book.names(); !equal(got, want) {
		t.Errorf("names() = %v, want %v", got, want)
	}
	if got := book.digest([]string{"teknoir-platform"}); got != "teknoir-platform: the platform, as the bot's kit has it" {
		t.Errorf("the digest = %q, want the dir's guide", got)
	}
}

// A kit dir that is missing, not a directory, empty or "" falls back to the embedded kit entirely.
// (An unreadable one is TestKitFSUnreadableDir's.)
func TestKitFSMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	book := newSkillBook(KitFS(missing))
	if got := book.names(); !equal(got, skillNames) {
		t.Errorf("names() = %v, want exactly the embedded kit's %v", got, skillNames)
	}
	for _, name := range skillNames {
		data, err := fs.ReadFile(KitFS(missing), "skills/"+name+"/SKILL.md")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(data) != embeddedGuide(t, name) {
			t.Errorf("%s is not the embedded guide", name)
		}
	}
	// An empty directory is the embedded kit too, and so is an empty dir name.
	if got := newSkillBook(KitFS("")).names(); !equal(got, skillNames) {
		t.Errorf("KitFS(\"\") = %v", got)
	}
	if got := newSkillBook(KitFS(t.TempDir())).names(); !equal(got, skillNames) {
		t.Errorf("an empty dir = %v", got)
	}
	// A path that is a file, not a directory.
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := newSkillBook(KitFS(file)).names(); !equal(got, skillNames) {
		t.Errorf("a file as the dir = %v", got)
	}
}

// The overlay's ReadDir is both kits, de-duplicated and sorted.
func TestKitFSReadDir(t *testing.T) {
	dir := t.TempDir()
	writeGuide(t, dir, "teknoir-platform", "p", "body")
	writeGuide(t, dir, "zeta-extra", "z", "body")
	entries, err := fs.ReadDir(KitFS(dir), "skills")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := append(append([]string{}, skillNames...), "zeta-extra")
	sortNames(want) // the merged list is sorted
	if !equal(names, want) {
		t.Errorf("ReadDir(skills) = %v, want %v", names, want)
	}
	// A path only the embedded kit has, and one neither has.
	if _, err := fs.ReadDir(KitFS(dir), "hooks"); err != nil {
		t.Errorf("ReadDir(hooks) of the fallback: %v", err)
	}
	if _, err := fs.ReadDir(KitFS(dir), "nope"); err == nil {
		t.Error("ReadDir found a path neither kit has")
	}
}

// An unreadable directory, or an unreadable part of one, falls back to the embedded kit.
func TestKitFSUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	for _, sub := range []string{".", "skills", filepath.Join("skills", "teknoir-platform")} {
		dir := t.TempDir()
		writeGuide(t, dir, "teknoir-platform", "the dir's", "body")
		p := filepath.Join(dir, sub)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
		if got := newSkillBook(KitFS(dir)).names(); !equal(got, skillNames) {
			t.Errorf("%s unreadable: names() = %v, want the embedded kit's %v", sub, got, skillNames)
		}
		data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-platform/SKILL.md")
		if err != nil {
			t.Fatalf("%s unreadable: read the platform guide: %v", sub, err)
		}
		if string(data) != embeddedGuide(t, "teknoir-platform") {
			t.Errorf("%s unreadable: the guide is not the embedded one", sub)
		}
	}
}

// A directory that can be traversed but not listed (mode 0o111: its files can be opened by name,
// but it cannot be read as a list) is a miss as a whole, not only for its listing: no file of it is
// served either, so the book is the embedded kit's and not a mix of the two.
func TestKitFSUnlistableDirFallsBackEntirely(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	dir := t.TempDir()
	writeGuide(t, dir, "teknoir-platform", "the dir's", "body")
	if err := os.Chmod(dir, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	want := newSkillBook(agentkit.FS).digest([]string{"teknoir-platform"})
	if got := newSkillBook(KitFS(dir)).digest([]string{"teknoir-platform"}); got != want {
		t.Errorf("the digest = %q, want the embedded guide's %q", got, want)
	}
	data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-platform/SKILL.md")
	if err != nil {
		t.Fatalf("read the platform guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-platform") {
		t.Error("a file of an unlistable dir was served")
	}
}

// An unreadable SKILL.md of the dir falls back to the embedded guide, file by file.
func TestKitFSUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	dir := t.TempDir()
	writeGuide(t, dir, "teknoir-platform", "the dir's", "body")
	writeGuide(t, dir, "zeta-extra", "z", "z's body")
	p := filepath.Join(dir, "skills", "teknoir-platform", "SKILL.md")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-platform/SKILL.md")
	if err != nil {
		t.Fatalf("read the platform guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-platform") {
		t.Error("an unreadable SKILL.md did not fall back to the embedded guide")
	}
	want := append(append([]string{}, skillNames...), "zeta-extra")
	if got := newSkillBook(KitFS(dir)).names(); !equal(got, want) {
		t.Errorf("names() = %v, want %v", got, want)
	}
}

// `skills` as a regular file, not a directory, falls back to the embedded kit entirely.
func TestKitFSSkillsIsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skills"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := newSkillBook(KitFS(dir)).names(); !equal(got, skillNames) {
		t.Errorf("names() = %v, want the embedded kit's %v", got, skillNames)
	}
	data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-robots/SKILL.md")
	if err != nil {
		t.Fatalf("read the robots guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-robots") {
		t.Error("the guide is not the embedded one")
	}
}

// A plain file where a guide's directory should be cannot be read as a guide, so the embedded guide
// of that name is served and the file is not a guide of the book.
func TestKitFSGuideIsAPlainFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "teknoir-platform"), []byte("not a guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-platform/SKILL.md")
	if err != nil {
		t.Fatalf("read the platform guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-platform") {
		t.Error("a plain file did not fall back to the embedded guide")
	}
	book := newSkillBook(KitFS(dir))
	if got := book.names(); !equal(got, skillNames) {
		t.Errorf("names() = %v, want the embedded kit's %v", got, skillNames)
	}
	if got := book.digest([]string{"teknoir-platform"}); got == "" {
		t.Error("the platform guide has no digest")
	}
}

// A symlinked guide directory is a guide: its own SKILL.md is read.
func TestSkillBookFollowsSymlinkedDir(t *testing.T) {
	kit, other := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(kit, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "teknoir-platform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "teknoir-platform", "SKILL.md"), []byte("---\nname: teknoir-platform\ndescription: LINKED\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "teknoir-platform"), filepath.Join(kit, "skills", "teknoir-platform")); err != nil {
		t.Skip(err)
	}
	b := newSkillBook(KitFS(kit))
	if got := b.names(); len(got) != len(skillNames) || got[0] != "teknoir-platform" {
		t.Fatalf("names = %v", got)
	}
	if got := b.digest([]string{"teknoir-platform"}); got != "teknoir-platform: LINKED" {
		t.Fatalf("digest = %q", got)
	}
}

// kitBodies is the seven kit guides, as mapKit bodies.
func kitBodies() map[string]string {
	bodies := make(map[string]string, len(skillNames))
	for _, name := range skillNames {
		bodies[name] = "body"
	}
	return bodies
}

// The book holds only names the caps can carry, and only as many guides as the header takes.
func TestSkillBookKeepsToTheCaps(t *testing.T) {
	t.Run("names the caps refuse are left out", func(t *testing.T) {
		kit := mapKit(map[string]string{"Bad_Name": "b", "x.bak": "x", "teknoir-debug": "d", "alpha-extra": "a"})
		book := newSkillBook(kit)
		if got, want := book.names(), []string{"teknoir-debug", "alpha-extra"}; !equal(got, want) {
			t.Fatalf("names() = %v, want %v", got, want)
		}
		for _, n := range []string{"x.bak", "Bad_Name"} {
			_, _, err := book.read(n, 0)
			want := "there is no guide named " + n + "; use one of the guides your instructions list"
			if err == nil || err.Error() != want {
				t.Errorf("read(%q) = %v, want %q: the caps never announce it", n, err, want)
			}
		}
	})

	t.Run("extras stop at 64 names", func(t *testing.T) {
		bodies := kitBodies()
		for i := 0; i < 70; i++ {
			bodies[fmt.Sprintf("x-%02d", i)] = "body"
		}
		book := newSkillBook(mapKit(bodies))
		got := book.names()
		want := append([]string{}, skillNames...)
		for i := 0; len(want) < maxAdvertisedSkills; i++ {
			want = append(want, fmt.Sprintf("x-%02d", i)) // x-00 .. x-56: the sorted extras that fit
		}
		if !equal(got, want) {
			t.Fatalf("names() = %v, want %v", got, want)
		}
		joined := strings.Join(got, ",")
		if len(joined) > maxSkillsHeaderLen {
			t.Errorf("the joined header is %d bytes", len(joined))
		}
		if err := new(hub.Client).SetCaps(hub.Caps{Proto: 2, Kit: agentkit.Version, Tools: []string{"whoami"}, Skills: got}); err != nil {
			t.Errorf("the caps refuse the book's names: %v", err)
		}
		// A guide past the cap is not readable either.
		if _, _, err := book.read("x-69", 0); err == nil {
			t.Error("the book reads an extra it did not announce")
		}
	})

	t.Run("extras stop at 2048 bytes", func(t *testing.T) {
		bodies := kitBodies()
		for i := 0; i < 60; i++ {
			bodies[fmt.Sprintf("x%02d", i)+strings.Repeat("y", 60)] = "body" // 63 bytes a name
		}
		book := newSkillBook(mapKit(bodies))
		got := book.names()
		joined := strings.Join(got, ",")
		if len(joined) > maxSkillsHeaderLen {
			t.Fatalf("the joined header is %d bytes, want at most %d", len(joined), maxSkillsHeaderLen)
		}
		if len(got) != len(skillNames)+30 {
			t.Fatalf("%d names, want the 7 kit guides and 30 extras (header %d bytes)", len(got), len(joined))
		}
		if last, want := got[len(got)-1], "x29"+strings.Repeat("y", 60); last != want {
			t.Errorf("the last name is %q, want %q", last, want)
		}
		if _, _, err := book.read("x59"+strings.Repeat("y", 60), 0); err == nil {
			t.Error("the book reads an extra past the header's bytes")
		}
		if err := new(hub.Client).SetCaps(hub.Caps{Proto: 2, Kit: agentkit.Version, Tools: []string{"whoami"}, Skills: got}); err != nil {
			t.Errorf("the caps refuse the book's names: %v", err)
		}
	})

	// Once one extra does not fit, no later one is kept, even a short one that would: the kept
	// extras are a sorted prefix, so the same kit always announces the same names.
	t.Run("extras are a sorted prefix", func(t *testing.T) {
		bodies := kitBodies()
		for i := 0; i < 31; i++ {
			bodies[fmt.Sprintf("a%02d", i)+strings.Repeat("b", 60)] = "body"
		}
		bodies["zz"] = "body"
		book := newSkillBook(mapKit(bodies))
		got := book.names()
		if len(got) != len(skillNames)+30 {
			t.Fatalf("%d names, want %d: %v", len(got), len(skillNames)+30, got)
		}
		if last, want := got[len(got)-1], "a29"+strings.Repeat("b", 60); last != want {
			t.Errorf("the last name is %q, want %q", last, want)
		}
		if _, _, err := book.read("zz", 0); err == nil {
			t.Error("the book kept a short extra after one that did not fit")
		}
	})
}

// read_skill serves only the guides the role was given: an extra guide of the kit dir that the role
// does not name is not read.
func TestReadSkillRefusesExtraGuides(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "x"))
	h.cfg.Skills = mapKit(map[string]string{"teknoir-platform": "p", "zeta-extra": "z"})
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr := &turn{d: d, work: newWork(kindAsk, "", "x")}
	res := runReadSkill(tr, stdRole(t, "read", "fleet"), json.RawMessage(`{"name":"zeta-extra","offset":0}`))
	if !res.IsError {
		t.Fatalf("read_skill read an extra guide: %q", res.Text)
	}
	if want := "there is no guide named zeta-extra; use one of the guides your instructions list"; res.Text != want {
		t.Errorf("text = %q, want %q", res.Text, want)
	}
	res = runReadSkill(tr, stdRole(t, "read", "fleet"), json.RawMessage(`{"name":"teknoir-platform","offset":0}`))
	if res.IsError {
		t.Fatalf("read_skill refused a guide of the kit: %q", res.Text)
	}
	if res.Text != "p" {
		t.Errorf("the page = %q, want the guide's body", res.Text)
	}
}

// An extra guide of the kit dir is served to a role whose skills name it: its enum, its digest and
// a read.
func TestReadSkillServesANamedExtraGuide(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "x"))
	h.cfg.Skills = mapKit(map[string]string{"teknoir-platform": "p", "zeta-extra": "z"})
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr := &turn{d: d, work: newWork(kindAsk, "", "x")}
	r := stdRole(t, "read", "fleet")
	r.Skills = []string{"zeta-extra"}
	res := runReadSkill(tr, r, json.RawMessage(`{"name":"zeta-extra","offset":0}`))
	if res.IsError || res.Text != "z" {
		t.Fatalf("read_skill = %+v, want the extra guide's body", res)
	}
	if got, want := d.book.digest(r.Skills), "zeta-extra: the guide zeta-extra"; got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
	if enum := stringEnum(t, readSkillTool(r.Skills), "name"); !equal(enum, []string{"zeta-extra"}) {
		t.Errorf("enum = %v, want [zeta-extra]", enum)
	}
	// The same role cannot read a kit guide it was not given.
	if res := runReadSkill(tr, r, json.RawMessage(`{"name":"teknoir-platform","offset":0}`)); !res.IsError {
		t.Error("read_skill served a guide the role does not name")
	}
	// A role that does not name the extra guide gets it in neither its enum nor its digest, and
	// cannot read it.
	other := stdRole(t, "read", "fleet")
	other.Skills = []string{"teknoir-platform"}
	if enum := stringEnum(t, readSkillTool(other.Skills), "name"); !equal(enum, []string{"teknoir-platform"}) {
		t.Errorf("a role without the extra has the enum %v", enum)
	}
	if digest := d.book.digest(other.Skills); strings.Contains(digest, "zeta-extra") {
		t.Errorf("a role without the extra has it in its digest: %q", digest)
	}
	if res := runReadSkill(tr, other, json.RawMessage(`{"name":"zeta-extra","offset":0}`)); !res.IsError {
		t.Error("read_skill served the extra guide to a role that does not name it")
	}
}

// The daemon builds one book in New, from Config.Skills, and reads no guide before that.
func TestDaemonBook(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "x"))
	h.cfg.Skills = mapKit(map[string]string{"teknoir-platform": "the test's platform guide"})
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.book == nil {
		t.Fatal("the daemon has no book")
	}
	if got, want := d.book.names(), []string{"teknoir-platform"}; !equal(got, want) {
		t.Errorf("names() = %v, want %v", got, want)
	}
	// A nil Config.Skills is DefaultKitDir, then the embedded kit: whatever the host has installed,
	// the book holds the embedded kit's guides, and never an empty one.
	h.cfg.Skills = nil
	d, err = New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := d.SkillNames(); len(got) < len(skillNames) || !equal(got[:len(skillNames)], skillNames) {
		t.Errorf("with no Config.Skills the book is %v, and it must start with the embedded kit's %v", got, skillNames)
	}
}

// SkillNames is the daemon's own book: the names the caps announce and validateSnapshot checks.
func TestDaemonSkillNames(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "x"))
	h.cfg.Skills = mapKit(map[string]string{"alpha-extra": "a", "teknoir-debug": "d"})
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := d.SkillNames(), []string{"teknoir-debug", "alpha-extra"}; !equal(got, want) {
		t.Errorf("SkillNames() = %v, want %v", got, want)
	}
	// The embedded kit is the seven, in the order the prompts and read_skill keep.
	h.cfg.Skills = agentkit.FS
	d, err = New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := d.SkillNames(); !equal(got, skillNames) {
		t.Errorf("SkillNames() = %v, want %v", got, skillNames)
	}
	// A kit with no guide has none.
	h.cfg.Skills = fstest.MapFS{}
	d, err = New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := d.SkillNames(); len(got) != 0 {
		t.Errorf("SkillNames() = %v, want none", got)
	}
}
