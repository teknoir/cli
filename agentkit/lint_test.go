package agentkit

// The Go version of the kit lint that tools/kit-lint.py runs: frontmatter, size, links, scripts and
// banned text of every skill under skills/. The Python script is the source of truth for the rules;
// keep the two in step.

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// kitVersion is the version every skill's metadata.teknoir-kit-version must carry. A later job switches
// this to the package's Version; until then it is a constant.
const kitVersion = "0.1.0"

var (
	// nameRE is a skill name: lower-case words joined by single dashes.
	nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// linkRE finds the target of a markdown link.
	linkRE = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	// codePathRE finds a backticked path into the skill's own folders.
	codePathRE = regexp.MustCompile("`((?:scripts|references|templates|assets)/[^`\\s]+)`")
	// botModeHeadingRE finds a heading that names bot mode.
	botModeHeadingRE = regexp.MustCompile(`(?im)^#+ .*bot mode`)
	// frontmatterKeys are the only keys a SKILL.md frontmatter may carry.
	frontmatterKeys = []string{"name", "description", "license", "metadata"}
)

// bannedText is a phrase no kit file may contain, and why.
type bannedText struct {
	re  *regexp.Regexp
	why string
}

// bannedTexts mirrors BANNED in tools/kit-lint.py.
var bannedTexts = []bannedText{
	{regexp.MustCompile(`Claude Code`), "names a product: write 'your agent' or 'the TeknoirBot agent'"},
	{regexp.MustCompile(`\$\{CLAUDE_SKILL_DIR\}`), "agent-specific variable: use paths relative to the skill directory"},
	{regexp.MustCompile(`bot mqtt-token`), "superseded: use `tnctl mqtt stream`"},
	{regexp.MustCompile(`tnctl (robot|device) get\b`), "no such command: `tnctl robot <name>` / `tnctl device <name>`"},
	{regexp.MustCompile(`tnctl robot types\b`), "no such command: `tnctl api GET <ns>/types`"},
	{regexp.MustCompile(`/var/lib/teknoirbot`), "stale key path: `$TNCTL_BOT_KEY_FILE` (/run/teknoirbot/key on a bot)"},
	{regexp.MustCompile(`tnctl inspect (pods|events)\b`), "no such command: `tnctl inspect pod|logs|release`"},
	{regexp.MustCompile(`\bteknoir\.(cloud|online|airgapped)\b`), "hard-coded platform domain: write <domain>"},
	{regexp.MustCompile(`\btnctl\s+(-o|--output|-n|--namespace|-d|--domain|--config)\b`), "global flag before the subcommand: permission rules match `tnctl <cmd> <sub>` prefixes"},
}

// TestSkillsAreWellFormed lints the skills that ship with the kit.
func TestSkillsAreWellFormed(t *testing.T) {
	if _, err := fs.Stat(os.DirFS("."), "skills"); err != nil {
		t.Skip("no skills/ next to the test")
	}
	for _, problem := range lintSkills(os.DirFS(".")) {
		t.Error(problem)
	}
}

// lintSkills checks every skill directory under skills/ in fsys, which is rooted at agentkit.
func lintSkills(fsys fs.FS) []string {
	var problems []string
	entries, err := fs.ReadDir(fsys, "skills")
	if err != nil {
		return []string{fmt.Sprintf("skills/: %v", err)}
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		problems = append(problems, lintSkill(fsys, name)...)
	}
	return problems
}

// lintSkill checks one skill directory and its files.
func lintSkill(fsys fs.FS, name string) []string {
	var problems []string
	skillDir := path.Join("skills", name)
	skillMD := path.Join(skillDir, "SKILL.md")
	text, err := fs.ReadFile(fsys, skillMD)
	if err != nil {
		return []string{fmt.Sprintf("%s: SKILL.md missing", name)}
	}
	label := fmt.Sprintf("%s/SKILL.md", name)

	fm, err := parseFrontmatter(text)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", label, err)}
	}

	// Keys: exactly the four allowed ones.
	for _, key := range frontmatterKeys {
		if _, ok := fm[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s: key missing: %s", label, key))
		}
	}
	var extra []string
	for key := range fm {
		allowed := false
		for _, k := range frontmatterKeys {
			if key == k {
				allowed = true
			}
		}
		if !allowed {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		problems = append(problems, fmt.Sprintf("%s: keys not allowed: %v", label, extra))
	}

	// name equals the directory and is a valid skill name.
	if got, ok := fm["name"].(string); !ok || got != name || !nameRE.MatchString(name) || len(name) > 64 {
		problems = append(problems, fmt.Sprintf("%s: name must equal the directory and match %s", label, nameRE.String()))
	}

	// description: 1-1024 characters, no angle brackets.
	description, isString := fm["description"].(string)
	if !isString || utf8.RuneCountInString(description) < 1 || utf8.RuneCountInString(description) > 1024 {
		problems = append(problems, fmt.Sprintf("%s: description must be a string of 1-1024 characters", label))
	} else if strings.ContainsAny(description, "<>") {
		problems = append(problems, fmt.Sprintf("%s: description must not contain < or >", label))
	}

	if license, ok := fm["license"].(string); !ok || license != "Apache-2.0" {
		problems = append(problems, fmt.Sprintf("%s: license must be Apache-2.0", label))
	}

	if !hasKitVersion(fm["metadata"]) {
		problems = append(problems, fmt.Sprintf("%s: metadata.teknoir-kit-version must be the string %q", label, kitVersion))
	}

	// Under 500 lines, and a bot mode section.
	lines := strings.Count(string(text), "\n")
	if !strings.HasSuffix(string(text), "\n") {
		lines++
	}
	if lines >= 500 {
		problems = append(problems, fmt.Sprintf("%s: %d lines, must be under 500", label, lines))
	}
	if !botModeHeadingRE.MatchString(string(text)) {
		problems = append(problems, fmt.Sprintf("%s: no 'Bot mode' heading", label))
	}

	// Links and backticked paths in SKILL.md, resolved from the skill directory.
	for _, target := range skillTargets(text, true) {
		problems = append(problems, checkLink(fsys, skillDir, label, target)...)
	}

	// Everything else in the skill.
	walkErr := fs.WalkDir(fsys, skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := relTo(skillDir, p)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				problems = append(problems, fmt.Sprintf("%s/%s: no __pycache__ in a skill (it would be embedded in tnctl)", name, rel))
			}
			return nil
		}
		if strings.HasSuffix(rel, ".pyc") || strings.HasSuffix(rel, ".pyo") {
			problems = append(problems, fmt.Sprintf("%s/%s: no compiled Python in a skill", name, rel))
			return nil
		}
		// SKILL.md's own links were checked above, from the skill directory; its text is checked below
		// like every other file's.
		if rel != "SKILL.md" && strings.HasSuffix(rel, ".md") {
			// A reference's links resolve from the reference's own directory.
			text, readErr := fs.ReadFile(fsys, p)
			if readErr != nil {
				return readErr
			}
			for _, target := range skillTargets(text, false) {
				resolved := path.Join(path.Dir(rel), stripAnchor(target))
				problems = append(problems, checkLink(fsys, skillDir, fmt.Sprintf("%s/%s", name, rel), resolved)...)
			}
		}
		if strings.HasPrefix(rel, "scripts/") {
			content, readErr := fs.ReadFile(fsys, p)
			if readErr != nil {
				return readErr
			}
			if !strings.HasPrefix(string(content), "#!") {
				problems = append(problems, fmt.Sprintf("%s/%s: a script must start with #!", name, rel))
			}
		}
		problems = append(problems, lintText(fsys, p, fmt.Sprintf("%s/%s", name, rel))...)
		return nil
	})
	if walkErr != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", name, walkErr))
	}
	return problems
}

// parseFrontmatter splits a SKILL.md into its frontmatter map. The frontmatter must start on line 1
// and be one YAML map.
func parseFrontmatter(text []byte) (map[string]any, error) {
	lines := strings.Split(string(text), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("frontmatter must start on line 1 with ---")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		var doc any
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &doc); err != nil {
			return nil, fmt.Errorf("frontmatter is not valid YAML: %v", err)
		}
		if doc == nil {
			return map[string]any{}, nil
		}
		fm, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("frontmatter is not a map")
		}
		return fm, nil
	}
	return nil, fmt.Errorf("frontmatter has no closing ---")
}

// hasKitVersion reports whether metadata is a map whose teknoir-kit-version is the kit version string.
func hasKitVersion(metadata any) bool {
	md, ok := metadata.(map[string]any)
	if !ok {
		return false
	}
	version, ok := md["teknoir-kit-version"].(string)
	return ok && version == kitVersion
}

// skillTargets returns the markdown link targets of text, and withPaths also its backticked paths into
// the skill's own folders.
func skillTargets(text []byte, withPaths bool) []string {
	seen := map[string]bool{}
	var targets []string
	for _, match := range linkRE.FindAllStringSubmatch(string(text), -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			targets = append(targets, match[1])
		}
	}
	if withPaths {
		for _, match := range codePathRE.FindAllStringSubmatch(string(text), -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				targets = append(targets, match[1])
			}
		}
	}
	sort.Strings(targets)
	return targets
}

// checkLink reports a target that is missing, or that points outside the skill other than to a file of
// a sibling skill. target is relative to skillDir.
func checkLink(fsys fs.FS, skillDir, label, target string) []string {
	if target == "" || strings.HasPrefix(target, "#") ||
		strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "mailto:") {
		return nil
	}
	target = stripAnchor(target)
	if target == "" {
		return nil
	}
	full := path.Clean(path.Join(skillDir, target))
	if full == skillDir || strings.HasPrefix(full, skillDir+"/") {
		if !exists(fsys, full) {
			return []string{fmt.Sprintf("%s: link or path %s does not exist in the skill", label, target)}
		}
		return nil
	}
	skillsRoot := path.Dir(skillDir)
	if !strings.HasPrefix(full, skillsRoot+"/") {
		return []string{fmt.Sprintf("%s: link %s leaves the skill and is not ../<sibling skill>/<file>", label, target)}
	}
	rel := strings.TrimPrefix(full, skillsRoot+"/")
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) < 2 || !nameRE.MatchString(parts[0]) {
		return []string{fmt.Sprintf("%s: link %s leaves the skill and is not ../<sibling skill>/<file>", label, target)}
	}
	if !exists(fsys, full) {
		return []string{fmt.Sprintf("%s: link %s names a sibling skill file that does not exist", label, target)}
	}
	return nil
}

// lintText reports the banned phrases of every line of a file.
func lintText(fsys fs.FS, p, label string) []string {
	content, err := fs.ReadFile(fsys, p)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", label, err)}
	}
	if !utf8.Valid(content) {
		return []string{fmt.Sprintf("%s: not UTF-8 text", label)}
	}
	var problems []string
	for i, line := range strings.Split(string(content), "\n") {
		for _, b := range bannedTexts {
			if b.re.MatchString(line) {
				snippet := strings.TrimSpace(line)
				if utf8.RuneCountInString(snippet) > 100 {
					snippet = string([]rune(snippet)[:100])
				}
				problems = append(problems, fmt.Sprintf("%s:%d: %s: %s", label, i+1, b.why, snippet))
			}
		}
	}
	return problems
}

// exists reports whether fsys has an entry at p.
func exists(fsys fs.FS, p string) bool {
	_, err := fs.Stat(fsys, p)
	return err == nil
}

// stripAnchor drops a link's #fragment.
func stripAnchor(target string) string {
	if i := strings.Index(target, "#"); i >= 0 {
		return target[:i]
	}
	return target
}

// TestLintCatchesBadSkills runs the checker over in-memory skills: each bad one must be reported, and a
// good one, with a link to a sibling skill, must pass.
func TestLintCatchesBadSkills(t *testing.T) {
	// goodSkillMD builds a SKILL.md for skill name with a body appended after the bot mode section.
	goodSkillMD := func(name, body string) string {
		return fmt.Sprintf(`---
name: %s
description: A test skill whose description says what it does and when to use it, in one sentence long enough to be realistic.
license: Apache-2.0
metadata:
  teknoir-kit-version: "%s"
---

# %s

Paths in this skill are relative to this skill's directory.

## Bot mode

Nothing differs in this skill.

%s
`, name, kitVersion, name, body)
	}
	// withFrontmatter builds a SKILL.md around an explicit frontmatter block.
	withFrontmatter := func(frontmatter string) string {
		return "---\n" + frontmatter + "---\n\n# a\n\n## Bot mode\n\nNothing.\n"
	}
	const goodFrontmatter = `name: a
description: A test skill whose description says what it does and when to use it, in one sentence long enough to be realistic.
license: Apache-2.0
metadata:
  teknoir-kit-version: "` + kitVersion + `"
`

	tests := []struct {
		name    string
		files   map[string]string
		wantErr string // a substring of the problem; "" means the skill must pass
	}{
		{
			name: "good skill with a sibling link",
			files: map[string]string{
				"skills/a/SKILL.md":           goodSkillMD("a", "See [b](../b/SKILL.md) and [more](references/more.md)."),
				"skills/a/references/more.md": "More detail; it links back to [the skill](../SKILL.md).\n",
				"skills/b/SKILL.md":           goodSkillMD("b", ""),
			},
		},
		{
			name:    "missing key",
			files:   map[string]string{"skills/a/SKILL.md": withFrontmatter(strings.Replace(goodFrontmatter, "license: Apache-2.0\n", "", 1))},
			wantErr: "key missing: license",
		},
		{
			name:    "extra key",
			files:   map[string]string{"skills/a/SKILL.md": withFrontmatter(goodFrontmatter + "compatibility: needs tnctl\n")},
			wantErr: "keys not allowed",
		},
		{
			name:    "name is not the directory",
			files:   map[string]string{"skills/a/SKILL.md": withFrontmatter(strings.Replace(goodFrontmatter, "name: a\n", "name: c\n", 1))},
			wantErr: "name must equal the directory",
		},
		{
			name: "description too long",
			files: map[string]string{"skills/a/SKILL.md": withFrontmatter(strings.Replace(
				goodFrontmatter, "description: A test skill", "description: A"+strings.Repeat("x", 1024), 1))},
			wantErr: "description must be a string of 1-1024",
		},
		{
			name:    "500 lines",
			files:   map[string]string{"skills/a/SKILL.md": goodSkillMD("a", strings.Repeat("padding line\n", 500))},
			wantErr: "lines, must be under 500",
		},
		{
			name:    "dead link",
			files:   map[string]string{"skills/a/SKILL.md": goodSkillMD("a", "See [more](references/none.md).")},
			wantErr: "does not exist in the skill",
		},
		{
			name: "dead sibling link",
			files: map[string]string{
				"skills/a/SKILL.md":     goodSkillMD("a", "See [other](../other/x.md)."),
				"skills/other/SKILL.md": goodSkillMD("other", ""),
			},
			wantErr: "sibling skill file that does not exist",
		},
		{
			name:    "link out of skills",
			files:   map[string]string{"skills/a/SKILL.md": goodSkillMD("a", "See [x](../../x.md).")},
			wantErr: "leaves the skill",
		},
		{
			name:    "banned phrase",
			files:   map[string]string{"skills/a/SKILL.md": goodSkillMD("a", "Start it in Claude Code.")},
			wantErr: "names a product",
		},
		{
			name: "script without a shebang",
			files: map[string]string{
				"skills/a/SKILL.md":       goodSkillMD("a", "Run `scripts/run.sh`."),
				"skills/a/scripts/run.sh": "echo hi\n",
			},
			wantErr: "must start with #!",
		},
		{
			name: "compiled python in a skill",
			files: map[string]string{
				"skills/a/SKILL.md":                  goodSkillMD("a", ""),
				"skills/a/scripts/__pycache__/x.pyc": "x",
			},
			wantErr: "no __pycache__ in a skill",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, content := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(content)}
			}
			problems := lintSkills(fsys)
			if tc.wantErr == "" {
				for _, problem := range problems {
					t.Errorf("unexpected problem: %s", problem)
				}
				return
			}
			for _, problem := range problems {
				if strings.Contains(problem, tc.wantErr) {
					return
				}
			}
			t.Errorf("no problem contains %q; got %v", tc.wantErr, problems)
		})
	}
}

// relTo returns target relative to base; both are slash paths inside fsys.
func relTo(base, target string) (string, error) {
	base = path.Clean(base)
	target = path.Clean(target)
	if base == target {
		return ".", nil
	}
	if !strings.HasPrefix(target, base+"/") {
		return "", fmt.Errorf("%s is not below %s", target, base)
	}
	return strings.TrimPrefix(target, base+"/"), nil
}
