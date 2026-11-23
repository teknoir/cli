package council

// Intake of a turn's council: the snapshot the platform sends in WorkTurn.Council (proto 2) is
// checked against this daemon's own catalog before anything runs. The check fails closed: a rule
// that is broken here would put a role the platform did not mean to give, or text that must never
// reach a prompt, into a turn, so the turn fails "bad_turn" instead.

import (
	"errors"
	"fmt"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/hub"
)

// The limits of a snapshot's fields.
const (
	// snapshotProto is the only snapshot protocol the daemon knows.
	snapshotProto = 2
	// maxSnapshotMembers is the most members an ask turn's council may have.
	maxSnapshotMembers = 5
	// The roster's own token cap, and one role's.
	minRosterTokens = 10_000
	maxRosterTokens = 500_000
	minRoleTokens   = 10_000
	maxRoleTokens   = 120_000
	minRoleRounds   = 1
	maxRoleRounds   = 10
	// The text limits, in runes.
	maxRoleName         = 40
	maxRoleDescription  = 300
	maxRolePersona      = 60
	maxRoleInstructions = 4000
)

// The role runtime the platform may name.
const (
	runtimeInproc = "inproc"
	runtimeCoder  = "coder"
	// coderTemplateID is the built-in Coder's id: the only role that runs code.
	coderTemplateID = "tpl:coder"
)

// The kinds of a role in a snapshot.
const (
	kindLead     = "lead"
	kindRole     = "member"
	kindReviewer = "reviewer"
)

var (
	// snapshotHash is the form of a snapshot's hash: the algorithm, then its hex digest.
	snapshotHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// roleSlug is the form of a role's slug.
	roleSlug = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)
)

// validateSnapshot checks a hub snapshot against this daemon's own catalog. It fails closed: any
// violation returns an error whose message is the reason; orchestrate fails the turn "bad_turn"
// with "The council for this turn is not valid: <reason>.".
//
// names is the tools this daemon may call at a level, and skills the guides it carries; a
// snapshot that names anything else is refused.
func validateSnapshot(s *hub.Snapshot, work *hub.WorkTurn, names func(level string) []string, skills []string) error {
	if s == nil {
		return errors.New("there is no council in this turn")
	}
	if s.Proto != snapshotProto {
		return fmt.Errorf("proto is %d, and this council knows proto %d", s.Proto, snapshotProto)
	}
	if !snapshotHash.MatchString(s.Hash) {
		return fmt.Errorf("hash %q is not \"sha256:\" and 64 lower-case hex digits", cutUTF8(s.Hash, 64, "…"))
	}
	if !isLevel(s.Level) {
		return fmt.Errorf("level %q is not none, read or creator", cutUTF8(s.Level, 64, "…"))
	}
	if s.Budget != nil && (s.Budget.MaxTokens < minRosterTokens || s.Budget.MaxTokens > maxRosterTokens) {
		return fmt.Errorf("budget.maxTokens is %d, and a council's is %d to %d", s.Budget.MaxTokens, minRosterTokens, maxRosterTokens)
	}

	switch work.Kind {
	case kindAsk:
		if s.Roster == nil {
			return errors.New("roster is missing on an ask turn")
		}
		if s.Lead == nil {
			return errors.New("lead is missing on an ask turn")
		}
		if s.Lead.Kind != kindLead {
			return fmt.Errorf("%s: kind is %q, and the lead's is %q", roleLabel(kindLead, s.Lead), s.Lead.Kind, kindLead)
		}
		if n := len(s.Members); n < 1 || n > maxSnapshotMembers {
			return fmt.Errorf("members has %d members, and an ask turn takes 1 to %d", n, maxSnapshotMembers)
		}
		seen := make(map[string]bool, len(s.Members))
		for i := range s.Members {
			m := &s.Members[i]
			if m.Kind != kindRole {
				return fmt.Errorf("%s: kind is %q, and a member's is %q", roleLabel(kindRole, m), m.Kind, kindRole)
			}
			if seen[m.Slug] {
				return fmt.Errorf("%s: the slug is used by two members of this council", roleLabel(kindRole, m))
			}
			seen[m.Slug] = true
			if m.Slug == memberCaptain {
				return fmt.Errorf("%s: a member may not take the lead's wire id %q", roleLabel(kindRole, m), memberCaptain)
			}
		}
		if s.Reviewer != nil && s.Reviewer.Kind != kindReviewer {
			return fmt.Errorf("%s: kind is %q, and the reviewer's is %q", roleLabel(kindReviewer, s.Reviewer), s.Reviewer.Kind, kindReviewer)
		}
		// The reviewer is a role of the same council: its slug must not share a lane with a member,
		// and the lead's wire id belongs to the lead alone (5.5 attributes events by slug).
		if s.Reviewer != nil && (seen[s.Reviewer.Slug] || s.Reviewer.Slug == memberCaptain) {
			return fmt.Errorf("%s: the slug is also a member's or the lead's wire id %q", roleLabel(kindReviewer, s.Reviewer), memberCaptain)
		}
	case kindMember:
		if s.Roster != nil {
			return errors.New("roster is set on a member turn")
		}
		if s.Lead != nil {
			return errors.New("lead is set on a member turn")
		}
		if s.Reviewer != nil {
			return errors.New("reviewer is set on a member turn")
		}
		if s.Budget != nil {
			return errors.New("budget is set on a member turn")
		}
		if len(s.Members) != 1 {
			return fmt.Errorf("members has %d members, and a member turn takes exactly one", len(s.Members))
		}
		m := &s.Members[0]
		if m.Kind != kindRole {
			return fmt.Errorf("%s: kind is %q, and a member's is %q", roleLabel(kindRole, m), m.Kind, kindRole)
		}
		if m.Slug == memberCaptain {
			return fmt.Errorf("%s: a member may not take the lead's wire id %q", roleLabel(kindRole, m), memberCaptain)
		}
		if m.Slug != work.Member {
			return fmt.Errorf("%s: slug is not the member this turn was sent to (%q)", roleLabel(kindRole, m), cutUTF8(work.Member, 64, "…"))
		}
	default:
		return fmt.Errorf("kind %q is not ask or member", cutUTF8(work.Kind, 64, "…"))
	}

	if s.Lead != nil {
		if err := validateRole(s.Lead, s.Level, names, skills); err != nil {
			return err
		}
	}
	for i := range s.Members {
		if err := validateRole(&s.Members[i], s.Level, names, skills); err != nil {
			return err
		}
	}
	if s.Reviewer != nil {
		if err := validateRole(s.Reviewer, s.Level, names, skills); err != nil {
			return err
		}
	}
	return nil
}

// roleLabel is how a role is named in a reason: its kind and its slug.
func roleLabel(kind string, r *hub.RoleSnapshot) string {
	return kind + " " + fmt.Sprintf("%q", cutUTF8(r.Slug, 64, "…"))
}

// validateRole checks one role of a snapshot: its slug, model, effort, level, runtime, tools,
// skills, results and budget, and its text.
func validateRole(r *hub.RoleSnapshot, snapshotLevel string, names func(level string) []string, skills []string) error {
	label := roleLabel(r.Kind, r)
	if !roleSlug.MatchString(r.Slug) {
		return fmt.Errorf("%s: slug is not valid (a lower-case letter, then 1 to 23 lower-case letters, digits and '-')", label)
	}
	switch r.Model {
	case "sonnet", "haiku":
	default:
		return fmt.Errorf("%s: model is %q, and a role's is sonnet or haiku", label, cutUTF8(r.Model, 64, "…"))
	}
	switch r.Effort {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("%s: effort is %q, and a role's is low, medium or high", label, cutUTF8(r.Effort, 64, "…"))
	}
	if !isLevel(r.Level) {
		return fmt.Errorf("%s: level is %q, and a role's is none, read or creator", label, cutUTF8(r.Level, 64, "…"))
	}
	if levelRank(r.Level) > levelRank(snapshotLevel) {
		return fmt.Errorf("%s: level is %q, above the council's level %q", label, r.Level, snapshotLevel)
	}

	coder := r.Runtime == runtimeCoder
	switch r.Runtime {
	case runtimeInproc:
	case runtimeCoder:
		if r.ID != coderTemplateID {
			return fmt.Errorf("%s: runtime is \"coder\", and only %s runs code", label, coderTemplateID)
		}
		if r.Kind != kindRole {
			return fmt.Errorf("%s: runtime is \"coder\", and only a member runs code", label)
		}
		for _, field := range []struct {
			name string
			n    int
		}{{"tools", len(r.Tools)}, {"groups", len(r.Groups)}, {"skills", len(r.Skills)}} {
			if field.n > 0 {
				return fmt.Errorf("%s: %s is set on the coder, and the coder takes none", label, field.name)
			}
		}
	default:
		return fmt.Errorf("%s: runtime is %q, and a role's is inproc or coder", label, cutUTF8(r.Runtime, 64, "…"))
	}

	allowed := map[string]bool{}
	if names != nil {
		for _, n := range names(r.Level) {
			allowed[n] = true
		}
	}
	tools := make(map[string]bool, len(r.Tools))
	for _, name := range r.Tools {
		if tools[name] {
			return fmt.Errorf("%s: tools names %q twice", label, cutUTF8(name, 64, "…"))
		}
		tools[name] = true
		if !allowed[name] {
			return fmt.Errorf("%s: tools names %q, which is not a tool at level %q", label, cutUTF8(name, 64, "…"), r.Level)
		}
	}
	known := make(map[string]bool, len(skills))
	for _, name := range skills {
		known[name] = true
	}
	for _, name := range r.Skills {
		if !known[name] {
			return fmt.Errorf("%s: skills names %q, which this computer has no guide for", label, cutUTF8(name, 64, "…"))
		}
	}
	hasResults := false
	for _, group := range r.Groups {
		if group == "results" {
			hasResults = true
		}
	}
	if r.Results && !hasResults {
		return fmt.Errorf("%s: results is true, and groups has no \"results\"", label)
	}

	switch {
	case coder, r.Kind != kindRole:
		if r.Budget != nil {
			return fmt.Errorf("%s: budget is set, and only an inproc member has one", label)
		}
	default: // an inproc member
		if r.Budget == nil {
			return fmt.Errorf("%s: budget is missing, and an inproc member has one", label)
		}
		if r.Budget.MaxToolRounds < minRoleRounds || r.Budget.MaxToolRounds > maxRoleRounds {
			return fmt.Errorf("%s: budget.maxToolRounds is %d, and a member's is %d to %d", label, r.Budget.MaxToolRounds, minRoleRounds, maxRoleRounds)
		}
		if r.Budget.MaxTokens < minRoleTokens || r.Budget.MaxTokens > maxRoleTokens {
			return fmt.Errorf("%s: budget.maxTokens is %d, and a member's is %d to %d", label, r.Budget.MaxTokens, minRoleTokens, maxRoleTokens)
		}
	}

	// The text, counted in runes.
	if n := utf8.RuneCountInString(r.Name); n < 1 || n > maxRoleName {
		return fmt.Errorf("%s: name has %d characters, and a name has 1 to %d", label, n, maxRoleName)
	}
	if badText(r.Name, false) {
		return fmt.Errorf("%s: name has a control or invisible character", label)
	}
	if !hasVisible(r.Name) {
		return fmt.Errorf("%s: name has no visible character", label)
	}
	if n := utf8.RuneCountInString(r.Description); n < 1 || n > maxRoleDescription {
		return fmt.Errorf("%s: description has %d characters, and a description has 1 to %d", label, n, maxRoleDescription)
	}
	if badText(r.Description, false) {
		return fmt.Errorf("%s: description has a control or invisible character, and a description is one line", label)
	}
	if r.Persona != nil {
		if n := utf8.RuneCountInString(*r.Persona); n > maxRolePersona {
			return fmt.Errorf("%s: persona has %d characters, and a persona has at most %d", label, n, maxRolePersona)
		}
		if badText(*r.Persona, false) {
			return fmt.Errorf("%s: persona has a control or invisible character, and a persona is one line", label)
		}
	}
	if n := utf8.RuneCountInString(r.Instructions); n > maxRoleInstructions {
		return fmt.Errorf("%s: instructions has %d characters, and instructions have at most %d", label, n, maxRoleInstructions)
	}
	if badText(r.Instructions, true) {
		return fmt.Errorf("%s: instructions have a control or invisible character other than a newline or a tab", label)
	}
	return nil
}

// isLevel reports whether level is one the snapshot knows.
func isLevel(level string) bool {
	switch level {
	case "none", "read", "creator":
		return true
	}
	return false
}

// levelRank orders the levels: none < read < creator.
func levelRank(level string) int {
	switch level {
	case "read":
		return 1
	case "creator":
		return 2
	}
	return 0
}

// badText reports whether s holds a character that must never reach a prompt: the string is not
// valid UTF-8, or a rune is a control character, U+2028 or U+2029, a format character other than
// ZWNJ and ZWJ, or in the tag block. allowLines lets a newline and a tab through.
//
// This is a deliberately weaker mirror of the platform's text rule: the platform also looks at the
// context of a joiner or a selector, and its Unicode tables may be newer than Go's. The daemon
// must never refuse what the platform accepts; it re-checks only what must never reach a prompt.
func badText(s string, allowLines bool) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if allowLines && (r == '\n' || r == '\t') {
			continue
		}
		if badRune(r) {
			return true
		}
	}
	return false
}

// badRune reports whether r is a control or invisible character by the daemon's rule.
func badRune(r rune) bool {
	switch r {
	case 0x200C, 0x200D: // ZWNJ and ZWJ: an emoji sequence or a cursive script needs them
		return false
	case 0x2028, 0x2029: // the line and paragraph separators
		return true
	}
	if r >= 0xE0000 && r <= 0xE007F { // the tag block, assigned or not
		return true
	}
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
}

// hasVisible reports whether s has a character a reader sees: any rune that is not white space, a
// control, a format character or a combining mark.
func hasVisible(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Mn, unicode.Me) {
			continue
		}
		return true
	}
	return false
}
