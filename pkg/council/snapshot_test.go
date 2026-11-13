package council

// Tests of validateSnapshot: the turn's council, checked against this daemon's catalog before any
// of it runs. Each rule has its own case, and the hub's shared text vectors are re-read here.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/hub"
)

// fakeCatalog is the tools a test's daemon may call at each level, and the guides it carries. It is
// the same catalog the rest of the package's tests use.
func fakeCatalog() (func(level string) []string, []string) {
	names := func(level string) []string {
		out := append([]string{}, testToolNames[level]...)
		sortNames(out)
		return out
	}
	return names, skillNames
}

// sortNames sorts a string slice in place.
func sortNames(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// validAskSnapshot is the Standard council of the platform's spec at level read, built from the
// spec's example with the fake catalog's tools.
func validAskSnapshot() *hub.Snapshot {
	return &hub.Snapshot{
		Proto:  2,
		Hash:   "sha256:" + strings.Repeat("a", 64),
		Roster: &hub.RosterRef{ID: "tpl:standard", Name: "standard", DisplayName: "Standard", Version: 1},
		Level:  "read",
		Budget: nil,
		Lead: &hub.RoleSnapshot{
			ID: "tpl:captain", Version: 1, Slug: "captain", Name: "Captain",
			Description:  "leads the council: routes each question, convenes members and writes the answer.",
			Instructions: "", Kind: kindLead, Runtime: runtimeInproc,
			Model: "sonnet", Effort: "high", Level: "read",
			Groups:  []string{"core_read", "metrics", "mqtt", "inspect", "results"},
			Tools:   []string{"list_robots", "whoami"},
			Results: true,
			Skills:  []string{"teknoir-platform", "teknoir-robots"},
		},
		Members: []hub.RoleSnapshot{
			{
				ID: "tpl:fleet", Version: 1, Slug: "fleet", Name: "Fleet", Persona: strPtr("the fact finder"),
				Description:  "finds facts on the platform with read tools.",
				Instructions: "Answer the task with facts from the read tools: never guess a value.",
				Kind:         kindRole, Runtime: runtimeInproc,
				Model: "haiku", Effort: "medium", Level: "read",
				Groups:  []string{"core_read", "metrics", "mqtt", "inspect", "results"},
				Tools:   []string{"list_robots", "whoami"},
				Results: true,
				Skills:  []string{"teknoir-platform", "teknoir-robots"},
				Budget:  &hub.RoleBudget{MaxToolRounds: 10, MaxTokens: 120000},
			},
			{
				ID: "tpl:coder", Version: 1, Slug: "coder", Name: "Coder", Persona: nil,
				Description:  "writes and runs code on the bot's computer.",
				Instructions: "", Kind: kindRole, Runtime: runtimeCoder,
				Model: "sonnet", Effort: "high", Level: "read",
				Groups: []string{}, Tools: []string{}, Skills: []string{}, Budget: nil,
			},
		},
		Reviewer: &hub.RoleSnapshot{
			ID: "tpl:critic", Version: 1, Slug: "critic", Name: "Critic", Persona: nil,
			Description:  "checks the members' reports before you answer.",
			Instructions: "", Kind: kindReviewer, Runtime: runtimeInproc,
			Model: "sonnet", Effort: "medium", Level: "none",
			Groups: []string{}, Tools: []string{}, Skills: []string{}, Budget: nil,
		},
	}
}

// validMemberSnapshot is a member turn sent to Fleet, at level read.
func validMemberSnapshot() *hub.Snapshot {
	ask := validAskSnapshot()
	fleet := ask.Members[0]
	return &hub.Snapshot{
		Proto:   2,
		Hash:    ask.Hash,
		Level:   "read",
		Members: []hub.RoleSnapshot{fleet},
	}
}

func strPtr(s string) *string { return &s }

func TestValidateSnapshotValid(t *testing.T) {
	names, skills := fakeCatalog()
	t.Run("ask", func(t *testing.T) {
		work := newWork(kindAsk, "", "why is r2 degraded?")
		if err := validateSnapshot(validAskSnapshot(), work, names, skills); err != nil {
			t.Fatalf("a valid ask snapshot was refused: %v", err)
		}
	})
	t.Run("member", func(t *testing.T) {
		work := newWork(kindMember, "fleet", "which robots are degraded?")
		if err := validateSnapshot(validMemberSnapshot(), work, names, skills); err != nil {
			t.Fatalf("a valid member snapshot was refused: %v", err)
		}
	})
}

// TestValidateSnapshotRules is one row per rule: the change to a valid snapshot, and what the
// reason must name. wantAll are substrings the reason must hold.
func TestValidateSnapshotRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		work func() *hub.WorkTurn
		edit func(s *hub.Snapshot)
		want string
		all  []string
	}{
		{
			name: "nil",
			edit: func(s *hub.Snapshot) {},
			want: "no council",
		},
		{
			name: "proto 1",
			edit: func(s *hub.Snapshot) { s.Proto = 1 },
			want: "proto",
		},
		{
			name: "a bad hash",
			edit: func(s *hub.Snapshot) { s.Hash = "sha256:xyz" },
			want: "hash",
		},
		{
			name: "a hash without the algorithm",
			edit: func(s *hub.Snapshot) { s.Hash = strings.Repeat("a", 64) },
			want: "hash",
		},
		{
			name: "an upper-case hash",
			edit: func(s *hub.Snapshot) { s.Hash = "sha256:" + strings.Repeat("A", 64) },
			want: "hash",
		},
		{
			// Only the snapshot's level is wrong: every role is valid at "none", the one level below
			// the unknown "admin", and none names only whoami.
			name: "a bad snapshot level",
			edit: func(s *hub.Snapshot) {
				s.Level = "admin"
				s.Lead.Level, s.Lead.Tools = "none", []string{"whoami"}
				s.Members[0].Level, s.Members[0].Tools = "none", []string{"whoami"}
				s.Members[1].Level = "none"
				s.Reviewer.Level = "none"
			},
			want: "level", all: []string{`level "admin" is not none, read or creator`},
		},
		{
			name: "a roster budget too low",
			edit: func(s *hub.Snapshot) { s.Budget = &hub.RosterBudget{MaxTokens: 9_999} },
			want: "budget",
		},
		{
			name: "a roster budget too high",
			edit: func(s *hub.Snapshot) { s.Budget = &hub.RosterBudget{MaxTokens: 500_001} },
			want: "budget",
		},
		{
			name: "no roster on an ask turn",
			edit: func(s *hub.Snapshot) { s.Roster = nil },
			want: "roster",
		},
		{
			name: "no lead on an ask turn",
			edit: func(s *hub.Snapshot) { s.Lead = nil },
			want: "lead",
		},
		{
			name: "the lead's kind is member",
			edit: func(s *hub.Snapshot) { s.Lead.Kind = kindRole },
			want: "lead", all: []string{`"captain"`, "kind"},
		},
		{
			name: "no members",
			edit: func(s *hub.Snapshot) { s.Members = nil },
			want: "members",
		},
		{
			name: "six members",
			edit: func(s *hub.Snapshot) {
				s.Members = append(s.Members, extraMembers(4)...)
			},
			want: "members",
		},
		{
			name: "a member whose kind is reviewer",
			edit: func(s *hub.Snapshot) { s.Members[0].Kind = kindReviewer },
			want: "member", all: []string{`"fleet"`, "kind"},
		},
		{
			name: "two members with one slug",
			edit: func(s *hub.Snapshot) { s.Members[1].Slug = "fleet" },
			want: "member", all: []string{`"fleet"`, "two members"},
		},
		{
			name: "a member with the lead's wire id",
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = memberCaptain },
			want: "member", all: []string{`"captain"`, "wire id"},
		},
		{
			name: "a reviewer of another kind",
			edit: func(s *hub.Snapshot) { s.Reviewer.Kind = kindRole },
			want: "reviewer", all: []string{`"critic"`, "kind"},
		},
		{
			// The reviewer is a role of the same council: one lane per slug (5.5 attributes by slug).
			name: "a reviewer with a member's slug",
			edit: func(s *hub.Snapshot) { s.Reviewer.Slug = "fleet" },
			want: "reviewer", all: []string{`reviewer "fleet": the slug is also a member's or the lead's wire id "captain"`},
		},
		{
			name: "a reviewer with the lead's wire id",
			edit: func(s *hub.Snapshot) { s.Reviewer.Slug = memberCaptain },
			want: "reviewer", all: []string{`"captain"`, "wire id"},
		},
		// A member turn: nothing but its one member.
		{
			name: "a member turn with a roster",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) {
				s.Roster = &hub.RosterRef{ID: "tpl:standard", Name: "standard", DisplayName: "Standard", Version: 1}
			},
			want: "roster",
		},
		{
			name: "a member turn with a lead",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) {
				lead := validAskSnapshot().Lead
				s.Lead = lead
			},
			want: "lead",
		},
		{
			name: "a member turn with a reviewer",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) {
				s.Reviewer = validAskSnapshot().Reviewer
			},
			want: "reviewer",
		},
		{
			name: "a member turn with a budget",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) {
				s.Budget = &hub.RosterBudget{MaxTokens: 100_000}
			},
			want: "budget",
		},
		{
			name: "a member turn with two members",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) {
				s.Members = append(s.Members, extraMembers(1)...)
			},
			want: "members",
		},
		{
			name: "a member turn to another member",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = "other" },
			want: "slug", all: []string{`"other"`, `"fleet"`},
		},
		{
			name: "a member turn whose member has the lead's wire id",
			work: memberTurnWork,
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = memberCaptain },
			want: "member", all: []string{`"captain"`, "wire id"},
		},
		{
			name: "an unknown turn kind",
			work: func() *hub.WorkTurn { return newWork("other", "", "x") },
			edit: func(s *hub.Snapshot) {},
			want: "kind",
		},
		// The roles.
		{
			name: "a bad slug",
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = "Fleet" },
			want: "slug", all: []string{`member "Fleet": slug is not valid (`},
		},
		{
			name: "a bad reviewer slug",
			edit: func(s *hub.Snapshot) { s.Reviewer.Slug = "Critic" },
			want: "slug", all: []string{`reviewer "Critic": slug is not valid (`},
		},
		{
			name: "a one-letter slug",
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = "f" },
			want: "slug",
		},
		{
			// The reason names the kind of the role it refuses, not only a member's.
			name: "a bad lead slug",
			edit: func(s *hub.Snapshot) { s.Lead.Slug = "Captain" },
			want: "slug", all: []string{`lead "Captain": slug is not valid (`},
		},
		{
			name: "a 25-letter slug",
			edit: func(s *hub.Snapshot) { s.Members[0].Slug = "f" + strings.Repeat("x", 24) },
			want: "slug",
		},
		{
			name: "a bad model",
			edit: func(s *hub.Snapshot) { s.Lead.Model = "opus" },
			want: "model", all: []string{`"captain"`},
		},
		{
			name: "a bad effort",
			edit: func(s *hub.Snapshot) { s.Lead.Effort = "max" },
			want: "effort", all: []string{`"captain"`},
		},
		{
			// No tools, so the member's own level is the only rule that can refuse it: nothing at
			// level "admin" is allowed, and the tools rule would otherwise fire first.
			name: "a bad role level",
			edit: func(s *hub.Snapshot) { s.Members[0].Level, s.Members[0].Tools = "admin", []string{} },
			want: "level", all: []string{`"fleet"`, "a role's is none, read or creator"},
		},
		{
			name: "a role above the snapshot's level",
			edit: func(s *hub.Snapshot) { s.Lead.Level = "creator" },
			want: "level", all: []string{`"captain"`, "above"},
		},
		{
			name: "a tool the level does not allow",
			edit: func(s *hub.Snapshot) { s.Members[0].Tools = []string{"inspect_pod"} },
			want: "tools", all: []string{`"fleet"`, `"inspect_pod"`},
		},
		{
			// A role's tools are its own level's names, not the council's a level higher.
			name: "a tool of the council's level but not the role's",
			edit: func(s *hub.Snapshot) {
				s.Level = "creator" // the member stays at read
				s.Members[0].Tools = []string{"inspect_pod", "list_robots", "whoami"}
			},
			want: "tools", all: []string{`"fleet"`, `"inspect_pod"`, `level "read"`},
		},
		{
			name: "a tool no level allows",
			edit: func(s *hub.Snapshot) { s.Members[0].Tools = []string{"rm_rf"} },
			want: "tools", all: []string{`"fleet"`, `"rm_rf"`},
		},
		{
			name: "a tool named twice",
			edit: func(s *hub.Snapshot) { s.Members[0].Tools = []string{"whoami", "whoami"} },
			want: "tools", all: []string{`"fleet"`, "twice"},
		},
		{
			name: "a skill the computer lacks",
			edit: func(s *hub.Snapshot) { s.Members[0].Skills = []string{"teknoir-none"} },
			want: "skills", all: []string{`"fleet"`, `"teknoir-none"`},
		},
		{
			name: "results true without the group",
			edit: func(s *hub.Snapshot) {
				s.Members[0].Groups = []string{"core_read"}
			},
			want: "results", all: []string{`"fleet"`},
		},
		{
			name: "no budget on an inproc member",
			edit: func(s *hub.Snapshot) { s.Members[0].Budget = nil },
			want: "budget", all: []string{`"fleet"`},
		},
		{
			name: "a member with no tool rounds",
			edit: func(s *hub.Snapshot) { s.Members[0].Budget.MaxToolRounds = 0 },
			want: "maxToolRounds", all: []string{`"fleet"`},
		},
		{
			name: "a member with 11 tool rounds",
			edit: func(s *hub.Snapshot) { s.Members[0].Budget.MaxToolRounds = 11 },
			want: "maxToolRounds", all: []string{`"fleet"`},
		},
		{
			name: "a member with too few tokens",
			edit: func(s *hub.Snapshot) { s.Members[0].Budget.MaxTokens = 9_999 },
			want: "maxTokens", all: []string{`"fleet"`},
		},
		{
			name: "a member with too many tokens",
			edit: func(s *hub.Snapshot) { s.Members[0].Budget.MaxTokens = 120_001 },
			want: "maxTokens", all: []string{`"fleet"`},
		},
		{
			name: "a budget on the lead",
			edit: func(s *hub.Snapshot) {
				s.Lead.Budget = &hub.RoleBudget{MaxToolRounds: 10, MaxTokens: 120000}
			},
			want: "budget", all: []string{`"captain"`},
		},
		{
			name: "a budget on the reviewer",
			edit: func(s *hub.Snapshot) {
				s.Reviewer.Budget = &hub.RoleBudget{MaxToolRounds: 10, MaxTokens: 120000}
			},
			want: "budget", all: []string{`"critic"`},
		},
		{
			name: "a coder that is not tpl:coder",
			edit: func(s *hub.Snapshot) { s.Members[1].ID = "tpl:other"; s.Members[1].Runtime = runtimeCoder },
			want: "coder", all: []string{`"coder"`},
		},
		{
			name: "a coder with tools",
			edit: func(s *hub.Snapshot) { s.Members[1].Tools = []string{"whoami"} },
			want: "coder", all: []string{`"coder"`, "tools"},
		},
		{
			name: "a coder with groups",
			edit: func(s *hub.Snapshot) { s.Members[1].Groups = []string{"core_read"} },
			want: "coder", all: []string{`"coder"`, "groups"},
		},
		{
			name: "a coder with skills",
			edit: func(s *hub.Snapshot) { s.Members[1].Skills = []string{"teknoir-platform"} },
			want: "coder", all: []string{`"coder"`, "skills"},
		},
		{
			name: "a coder that is the lead",
			edit: func(s *hub.Snapshot) {
				s.Lead.Runtime = runtimeCoder
				s.Lead.ID = coderTemplateID
			},
			want: "coder", all: []string{`"captain"`, "member"},
		},
		{
			name: "an unknown runtime",
			edit: func(s *hub.Snapshot) { s.Members[0].Runtime = "opencode" },
			want: "runtime", all: []string{`"fleet"`},
		},
		// The text.
		{
			name: "a name of no characters",
			edit: func(s *hub.Snapshot) { s.Members[0].Name = "" },
			want: "name", all: []string{`"fleet"`},
		},
		{
			name: "a 41-character name",
			edit: func(s *hub.Snapshot) { s.Members[0].Name = strings.Repeat("N", 41) },
			want: "name", all: []string{`"fleet"`, "41"},
		},
		{
			name: "a control character in the name",
			edit: func(s *hub.Snapshot) { s.Members[0].Name = "Fle\x01et" },
			want: "name", all: []string{`"fleet"`, "control"},
		},
		{
			name: "no visible character in the name",
			edit: func(s *hub.Snapshot) { s.Members[0].Name = "   " },
			want: "name", all: []string{`"fleet"`, "visible"},
		},
		{
			name: "an empty description",
			edit: func(s *hub.Snapshot) { s.Lead.Description = "" },
			want: "description", all: []string{`"captain"`},
		},
		{
			name: "a 301-character description",
			edit: func(s *hub.Snapshot) { s.Lead.Description = strings.Repeat("d", 301) },
			want: "description", all: []string{`"captain"`, "301"},
		},
		{
			name: "a description on two lines",
			edit: func(s *hub.Snapshot) { s.Lead.Description = "one\ntwo" },
			want: "description", all: []string{`"captain"`, "one line"},
		},
		{
			name: "a 61-character persona",
			edit: func(s *hub.Snapshot) { s.Members[0].Persona = strPtr(strings.Repeat("p", 61)) },
			want: "persona", all: []string{`"fleet"`, "61"},
		},
		{
			name: "a persona on two lines",
			edit: func(s *hub.Snapshot) { s.Members[0].Persona = strPtr("one\ttwo") },
			want: "persona", all: []string{`"fleet"`, "one line"},
		},
		{
			name: "4001-character instructions",
			edit: func(s *hub.Snapshot) { s.Members[0].Instructions = strings.Repeat("i", 4001) },
			want: "instructions", all: []string{`"fleet"`, "4001"},
		},
		{
			name: "a NUL in the instructions",
			edit: func(s *hub.Snapshot) { s.Members[0].Instructions = "one\x00two" },
			want: "instructions", all: []string{`"fleet"`, "control"},
		},
		{
			name: "a carriage return in the instructions",
			edit: func(s *hub.Snapshot) { s.Members[0].Instructions = "one\r\ntwo" },
			want: "instructions", all: []string{`"fleet"`, "control"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *hub.Snapshot
			var work *hub.WorkTurn
			if tc.work != nil {
				work = tc.work()
				s = validMemberSnapshot()
			} else {
				work = newWork(kindAsk, "", "why is r2 degraded?")
				s = validAskSnapshot()
			}
			if tc.name == "nil" {
				s = nil
			} else {
				tc.edit(s)
			}
			names, skills := fakeCatalog()
			err := validateSnapshot(s, work, names, skills)
			if err == nil {
				t.Fatal("the snapshot was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the reason %q does not hold %q", err.Error(), tc.want)
			}
			for _, w := range tc.all {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the reason %q does not hold %q", err.Error(), w)
				}
			}
		})
	}
}

// memberTurnWork is a member turn for Fleet.
func memberTurnWork() *hub.WorkTurn {
	return newWork(kindMember, "fleet", "which robots are degraded?")
}

// extraMembers is n more members, each valid on its own.
func extraMembers(n int) []hub.RoleSnapshot {
	out := make([]hub.RoleSnapshot, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hub.RoleSnapshot{
			ID: "tpl:extra", Version: 1, Slug: "extra" + strings.Repeat("x", i), Name: "Extra",
			Description: "another member.", Instructions: "",
			Kind: kindRole, Runtime: runtimeInproc,
			Model: "haiku", Effort: "medium", Level: "read",
			Groups: []string{"core_read"}, Tools: []string{"whoami"}, Results: false, Skills: []string{},
			Budget: &hub.RoleBudget{MaxToolRounds: 5, MaxTokens: 40000},
		})
	}
	return out
}

// TestValidSnapshotBoundaries covers the limits that must pass: 4000 instructions of multi-byte
// characters, a 40-character name, a 300-character description and a 60-character persona.
func TestValidSnapshotBoundaries(t *testing.T) {
	s := validAskSnapshot()
	// 4000 runes of a three-byte character.
	s.Members[0].Instructions = strings.Repeat("界", maxRoleInstructions)
	if n := utf8.RuneCountInString(s.Members[0].Instructions); n != 4000 {
		t.Fatalf("the instructions are %d runes, not 4000", n)
	}
	s.Members[0].Name = strings.Repeat("N", 40)
	s.Members[0].Description = strings.Repeat("d", 300)
	s.Members[0].Persona = strPtr(strings.Repeat("p", 60))
	s.Budget = &hub.RosterBudget{MaxTokens: 500_000}
	s.Members[0].Budget = &hub.RoleBudget{MaxToolRounds: 10, MaxTokens: 120_000}
	names, skills := fakeCatalog()
	if err := validateSnapshot(s, newWork(kindAsk, "", "x"), names, skills); err != nil {
		t.Fatalf("the boundary values were refused: %v", err)
	}
}

// TestValidSnapshotShapes: the largest and leanest council the platform may send passes — five
// members, no reviewer, a 24-character slug, and the lowest and the highest budgets (spec 2.4, 7.1:
// the daemon must never refuse what the hub accepts).
func TestValidSnapshotShapes(t *testing.T) {
	names, skills := fakeCatalog()

	lean := validAskSnapshot()
	lean.Reviewer = nil
	lean.Members = append(lean.Members, extraMembers(3)...)
	if len(lean.Members) != maxSnapshotMembers {
		t.Fatalf("the council has %d members, not %d", len(lean.Members), maxSnapshotMembers)
	}
	lean.Members[2].Slug = "a" + strings.Repeat("b", 23) // 24 characters: the longest slug
	lean.Members[2].Budget = &hub.RoleBudget{MaxToolRounds: minRoleRounds, MaxTokens: minRoleTokens}
	lean.Budget = &hub.RosterBudget{MaxTokens: minRosterTokens}
	if err := validateSnapshot(lean, newWork(kindAsk, "", "x"), names, skills); err != nil {
		t.Fatalf("a valid council was refused: %v", err)
	}

	// The highest budget of each, on the Standard council.
	rich := validAskSnapshot()
	rich.Members[0].Budget = &hub.RoleBudget{MaxToolRounds: maxRoleRounds, MaxTokens: maxRoleTokens}
	rich.Budget = &hub.RosterBudget{MaxTokens: maxRosterTokens}
	if err := validateSnapshot(rich, newWork(kindAsk, "", "x"), names, skills); err != nil {
		t.Fatalf("a valid council was refused: %v", err)
	}
}

// ---------- the text rule ----------

// textVector is one string the hub's shared fixture holds.
type textVector struct {
	Name  string `json:"name"`
	Text  string `json:"text"`
	Field string `json:"field"`
}

// textVectors is the hub's shared fixture. It is pure ASCII JSON: every other character is a
// \uXXXX escape, astral ones as surrogate pairs, which encoding/json joins back.
type textVectors struct {
	Accept        []textVector `json:"accept"`
	RefuseBoth    []textVector `json:"refuseBoth"`
	RefuseHubOnly []textVector `json:"refuseHubOnly"`
}

// badForField applies the daemon's rule for the field the vector names: displayName is a name, and
// instructions have the newline and tab allowance.
func badForField(v textVector) bool {
	switch v.Field {
	case "instructions":
		return badText(v.Text, true)
	default:
		return badText(v.Text, false)
	}
}

// TestTextVectors runs the hub's shared vectors: every string it accepts the daemon accepts, and
// every string it refuses on both sides the daemon refuses. An entry of refuseHubOnly is not
// asserted: the hub's rule is the stronger one, and the daemon must never refuse what it takes.
func TestTextVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/text_vectors.json")
	if err != nil {
		t.Fatalf("read the text vectors: %v", err)
	}
	var v textVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse the text vectors: %v", err)
	}
	if len(v.Accept) == 0 || len(v.RefuseBoth) == 0 {
		t.Fatalf("the vectors are empty: %d accepted, %d refused by both", len(v.Accept), len(v.RefuseBoth))
	}
	for _, e := range v.Accept {
		if badForField(e) {
			t.Errorf("%s: the daemon refuses %q, and the hub accepts it", e.Name, e.Text)
		}
		// A name also needs a visible character; the hub's accepted names have one.
		if e.Field == "displayName" && !hasVisible(e.Text) {
			t.Errorf("%s: the daemon finds no visible character in %q", e.Name, e.Text)
		}
	}
	for _, e := range v.RefuseBoth {
		if !badForField(e) {
			t.Errorf("%s: the daemon accepts %q, and the hub refuses it", e.Name, e.Text)
		}
	}
}

// TestBadTextOwn covers the rule's own cases: invalid UTF-8, a joiner in an emoji sequence and a
// variation selector are accepted, and a lone control is refused.
func TestBadTextOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		bad  bool
	}{
		{"invalid UTF-8", "a\xff", true},
		{"an emoji with a joiner", "Fleet 👩‍💻", false},
		{"a heart with a variation selector", "❤️", false},
		{"a ZWNJ", "می\u200cخواهم", false},
		{"a tag character", "a\U000e0041", true},
		{"an unassigned tag character", "a\U000e0000", true},
		{"a bidi control", "a\u202eb", true},
		{"a line separator", "a\u2028b", true},
		{"a paragraph separator", "a\u2029b", true},
		{"a BOM", "\ufeffa", true},
		{"a plain word", "Fleet", false},
		{"a no-break space", "Fleet x", false},
	} {
		if got := badText(tc.text, false); got != tc.bad {
			t.Errorf("%s: badText(%q) = %v, want %v", tc.name, tc.text, got, tc.bad)
		}
	}
	// The newline and the tab pass only where lines are allowed.
	for _, tc := range []struct {
		text   string
		lines  bool
		wantNo bool
	}{
		{"one\ntwo", true, false},
		{"one\ttwo", true, false},
		{"one\ntwo", false, true},
		{"one\ttwo", false, true},
	} {
		if got := badText(tc.text, tc.lines); got != tc.wantNo {
			t.Errorf("badText(%q, %v) = %v, want %v", tc.text, tc.lines, got, tc.wantNo)
		}
	}
}

// TestHasVisible: a string of spaces, format characters or combining marks has nothing a reader
// sees; a letter, a digit, a punctuation mark or an emoji does.
func TestHasVisible(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"", false},
		{"  　", false},
		{"́", false},      // a combining mark alone
		{"\u200b", false}, // a zero-width space: a format character
		{"️", false},      // a variation selector alone
		{"A", true},
		{"1", true},
		{"!", true},
		{"👍", true},
		{" 👍 ", true},
	} {
		if got := hasVisible(tc.text); got != tc.want {
			t.Errorf("hasVisible(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
