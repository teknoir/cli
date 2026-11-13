package council

// The council a test builds: the platform's Standard snapshots as fixtures, and custom councils
// made by editing one of them, so that a hash, an id and a budget stay the shape the platform sends.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

// stdSnapshot decodes one of the platform's Standard snapshots, at level none, read or creator. Each
// call gets its own copy: a test may edit what it gets.
func stdSnapshot(t *testing.T, level string) *hub.Snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "standard_snapshot_"+level+".json"))
	if err != nil {
		t.Fatalf("read the %s snapshot: %v", level, err)
	}
	var s hub.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode the %s snapshot: %v", level, err)
	}
	return &s
}

// stdAskCouncil is the council an ask turn runs from the Standard snapshot at level.
func stdAskCouncil(t *testing.T, level string) *turnCouncil {
	t.Helper()
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Council = stdSnapshot(t, level)
	c, err := councilFor(t, work, level)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// stdAskCouncilAt is a Standard council run at botLevel: the creator snapshot, whose roles are at
// the creator ceiling, so each one's run level is levelOrNone(botLevel). It is how a test reaches
// the levels whoami may answer with, "admin" and "" included.
func stdAskCouncilAt(t *testing.T, botLevel string) *turnCouncil {
	t.Helper()
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Council = stdSnapshot(t, "creator")
	c, err := councilFor(t, work, botLevel)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// stdMemberCouncil is the council a member turn sent to slug runs from the Standard snapshot at
// level.
func stdMemberCouncil(t *testing.T, level, slug string) *turnCouncil {
	t.Helper()
	work := newWork(kindMember, slug, "which robots are degraded?")
	c, err := councilFor(t, work, level)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// councilFor builds the council work runs at botLevel with the daemon's fake catalog.
func councilFor(t *testing.T, work *hub.WorkTurn, botLevel string) (*turnCouncil, error) {
	t.Helper()
	names, skills := fakeCatalog()
	return newTurnCouncil(work, botLevel, names, skills)
}

// catalogNames is the fake catalog's names at a level, sorted.
func catalogNames(level string) []string {
	names, _ := fakeCatalog()
	return names(level)
}

// customSnapshot decodes the creator snapshot and hands it to edit, which shapes a roster of the
// test's own. The creator snapshot is the one to start from: a role at creator may run below it
// after an access drop, which the read and none ones cannot test.
func customSnapshot(t *testing.T, edit func(*hub.Snapshot)) *hub.Snapshot {
	t.Helper()
	s := stdSnapshot(t, "creator")
	if edit != nil {
		edit(s)
	}
	return s
}

// customCouncilWith is an ask turn's council at botLevel over a snapshot the test edits itself.
func customCouncilWith(t *testing.T, s *hub.Snapshot, botLevel string) *turnCouncil {
	t.Helper()
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Council = s
	c, err := councilFor(t, work, botLevel)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// customCouncil is an ask turn's council at botLevel over the custom snapshot.
func customCouncil(t *testing.T, botLevel string, edit func(*hub.Snapshot)) *turnCouncil {
	t.Helper()
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Council = customSnapshot(t, edit)
	c, err := councilFor(t, work, botLevel)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// memberCouncilWithTools is a member turn's council at level whose one member has the given tools:
// the Standard member turn, with the tool list the daemon's own set (not the fake catalog) offers.
func memberCouncilWithTools(t *testing.T, level, slug string, toolNames []string) *turnCouncil {
	t.Helper()
	s := stdSnapshot(t, "creator")
	s.Lead, s.Reviewer, s.Roster, s.Budget = nil, nil, nil, nil
	kept := make([]hub.RoleSnapshot, 0, 1)
	for _, m := range s.Members {
		if m.Slug == slug {
			m.Level = "creator"
			m.Tools = toolNames
			kept = append(kept, m)
		}
	}
	s.Members = kept
	work := newWork(kindMember, slug, "which devices are online?")
	work.Council = s
	c, err := councilFor(t, work, level)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	return c
}

// stdRole is one role of the Standard council at level, as it runs.
func stdRole(t *testing.T, level, slug string) *runRole {
	t.Helper()
	c := stdAskCouncilAt(t, level)
	for _, r := range []*runRole{c.lead, c.reviewer} {
		if r != nil && r.Slug == slug {
			return r
		}
	}
	for _, r := range c.members {
		if r.Slug == slug {
			return r
		}
	}
	t.Fatalf("the Standard council has no role %q", slug)
	return nil
}

// roleOfSlug is a terse way to name a role of a council in a test.
func roleOfSlug(t *testing.T, c *turnCouncil, slug string) *runRole {
	t.Helper()
	r := c.member(slug)
	if r == nil {
		t.Fatalf("the council has no member %q", slug)
	}
	return r
}

// ---------- newTurnCouncil ----------

// The legacy path builds the Standard council from the embedded file, at the level the bot runs at,
// and its tools come from the daemon's own catalog rather than the file's.
func TestNewTurnCouncilLegacy(t *testing.T) {
	ask := newWork(kindAsk, "", "why is r2 degraded?")
	c, err := councilFor(t, ask, "read")
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	if c.snap.Hash != "legacy" || c.snap.Level != "read" || c.snap.Thorough {
		t.Errorf("the legacy council is %+v", c.snap)
	}
	if c.lead == nil || c.reviewer == nil || len(c.members) != 2 {
		t.Fatalf("the legacy council is %+v", c)
	}
	if c.lead.wire != memberCaptain || c.members[0].wire != "fleet" || c.members[1].wire != "coder" {
		t.Errorf("the wire ids are %q, %q and %q", c.lead.wire, c.members[0].wire, c.members[1].wire)
	}
	if got := c.enum(); !slices.Equal(got, []string{"fleet", "coder"}) {
		t.Errorf("enum = %v", got)
	}
	// The tools come from the daemon's catalog at the role's run level, not from the file.
	if got := c.members[0].Tools; !slices.Equal(got, []string{"list_robots", "whoami"}) {
		t.Errorf("Fleet's tools = %v", got)
	}
	if got := c.members[0].Skills; !slices.Equal(got, skillNames) {
		t.Errorf("Fleet's skills = %v", got)
	}
	// Every role is the platform's fixture at read: its run level, tools, skills, model, effort and
	// budget.
	want := stdSnapshot(t, "read")
	roles := append([]*runRole{c.lead, c.reviewer}, c.members...)
	wants := append([]*hub.RoleSnapshot{want.Lead, want.Reviewer}, func() []*hub.RoleSnapshot {
		out := make([]*hub.RoleSnapshot, 0, len(want.Members))
		for i := range want.Members {
			out = append(out, &want.Members[i])
		}
		return out
	}()...)
	for i, r := range roles {
		w := wants[i]
		if r.runLevel != w.Level || !slices.Equal(r.Tools, w.Tools) || !slices.Equal(r.Skills, w.Skills) ||
			r.Model != w.Model || r.Effort != w.Effort || !sameBudget(r.Budget, w.Budget) {
			t.Errorf("%s is runLevel %q tools %v skills %v model %q effort %q budget %v, the fixture is %+v",
				r.Slug, r.runLevel, r.Tools, r.Skills, r.Model, r.Effort, r.Budget, w)
		}
	}
	// An ask turn in council mode is thorough.
	ask.Options.Mode = modeCouncil
	c, err = councilFor(t, ask, "read")
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	if !c.snap.Thorough {
		t.Error("a council-mode legacy turn is not thorough")
	}
}

// sameBudget reports whether two role budgets are the same, nil included.
func sameBudget(a, b *hub.RoleBudget) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// A legacy member turn runs the one member it was sent to, and nothing else of the council.
func TestNewTurnCouncilLegacyMemberTurn(t *testing.T) {
	work := newWork(kindMember, "coder", "write a script that prints hello")
	c, err := councilFor(t, work, "read")
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	if c.snap.Roster != nil || c.snap.Lead != nil || c.snap.Reviewer != nil || c.snap.Budget != nil {
		t.Errorf("a member turn's snapshot is %+v", c.snap)
	}
	if c.lead != nil || c.reviewer != nil {
		t.Errorf("lead and reviewer = %+v, %+v", c.lead, c.reviewer)
	}
	if len(c.members) != 1 || c.members[0].Slug != "coder" || c.members[0].Runtime != runtimeCoder {
		t.Fatalf("members = %+v", c.members)
	}
	if c.members[0].runLevel != "read" {
		t.Errorf("runLevel = %q", c.members[0].runLevel)
	}
}

// A legacy role keeps only the guides the book carries, in the file's order, whatever order the
// book lists them in.
func TestNewTurnCouncilLegacySkillsFilteredByBook(t *testing.T) {
	names, _ := fakeCatalog()
	book := []string{"teknoir-council", "teknoir-platform", "teknoir-debug"}
	want := []string{"teknoir-platform", "teknoir-debug", "teknoir-council"}
	c, err := newTurnCouncil(newWork(kindAsk, "", "q"), "read", names, book)
	if err != nil {
		t.Fatalf("newTurnCouncil: %v", err)
	}
	if got := c.lead.Skills; !slices.Equal(got, want) {
		t.Errorf("the lead's skills = %v, want %v", got, want)
	}
	if got := c.member("fleet").Skills; !slices.Equal(got, want) {
		t.Errorf("Fleet's skills = %v, want %v", got, want)
	}
	if got := c.member("coder").Skills; len(got) != 0 {
		t.Errorf("the Coder's skills = %v, want none", got)
	}
	if got := c.reviewer.Skills; len(got) != 0 {
		t.Errorf("the reviewer's skills = %v, want none", got)
	}
	m, err := newTurnCouncil(newWork(kindMember, "fleet", "q"), "read", names, book)
	if err != nil {
		t.Fatalf("newTurnCouncil (member): %v", err)
	}
	if got := m.member("fleet").Skills; !slices.Equal(got, want) {
		t.Errorf("a member turn's Fleet skills = %v, want %v", got, want)
	}
}

// The tests' own copy of the Standard Fleet's budget cannot drift from the embedded file.
func TestLegacyStandardBudget(t *testing.T) {
	s, err := decodeLegacyStandard()
	if err != nil {
		t.Fatalf("decodeLegacyStandard: %v", err)
	}
	var fleet *hub.RoleSnapshot
	for i := range s.Members {
		if s.Members[i].Slug == "fleet" {
			fleet = &s.Members[i]
		}
	}
	if fleet == nil || fleet.Budget == nil {
		t.Fatal("the embedded council has no Fleet budget")
	}
	if fleet.Budget.MaxToolRounds != standardFleetRounds || fleet.Budget.MaxTokens != standardFleetTokens {
		t.Errorf("the embedded Fleet budget is %+v, and the tests assume %d rounds and %d tokens",
			fleet.Budget, standardFleetRounds, standardFleetTokens)
	}
}

// The snapshot path uses the platform's role as sent, and its run level is the lower of the role's
// level and the bot's.
func TestNewTurnCouncilSnapshotLevels(t *testing.T) {
	c := stdAskCouncilAt(t, "read") // the creator snapshot: its roles are at the creator ceiling.
	// The Standard reviewer is at none, and stays there: its level is its own ceiling.
	if got := c.reviewer.runLevel; got != "none" {
		t.Errorf("the reviewer runs at %q, want none", got)
	}
	for _, r := range []*runRole{c.lead, roleOfSlug(t, c, "fleet"), roleOfSlug(t, c, "coder")} {
		if r.Level != "creator" {
			t.Errorf("%s: the claim-time level is %q, want the snapshot's creator", r.Slug, r.Level)
		}
		if r.runLevel != "read" {
			t.Errorf("%s: runLevel = %q, want read", r.Slug, r.runLevel)
		}
	}
	// The platform's own tools are used as sent, not rebuilt from the daemon's catalog.
	if got := roleOfSlug(t, c, "fleet").Tools; !slices.Equal(got, []string{"inspect_pod", "list_robots", "whoami"}) {
		t.Errorf("Fleet's tools = %v", got)
	}
	if c.snap.Hash == "legacy" {
		t.Error("a snapshot council is marked legacy")
	}
	// A role at none, run at read: still none, and its tools and skills are the snapshot's as sent.
	c = customCouncil(t, "read", func(s *hub.Snapshot) {
		s.Members[0].Level = "none"
	})
	sent := stdSnapshot(t, "creator").Members[0]
	if got := roleOfSlug(t, c, "fleet").runLevel; got != "none" {
		t.Errorf("a none role at read runs at %q, want none", got)
	}
	if got := roleOfSlug(t, c, "fleet").Tools; !slices.Equal(got, sent.Tools) {
		t.Errorf("a none role's tools = %v, want the snapshot's %v as sent", got, sent.Tools)
	}
	if got := roleOfSlug(t, c, "fleet").Skills; !slices.Equal(got, sent.Skills) {
		t.Errorf("a none role's skills = %v, want the snapshot's %v", got, sent.Skills)
	}
	if got := c.enum(); !slices.Equal(got, []string{"fleet", "coder"}) {
		t.Errorf("enum = %v, want [fleet coder]", got)
	}
}

// Legacy isolation: every call decodes its own snapshot, so two turns at once never share one, and a
// member turn's edits never reach the next ask turn.
func TestNewTurnCouncilLegacyIsolation(t *testing.T) {
	first, err := councilFor(t, newWork(kindAsk, "", "q1"), "creator")
	if err != nil {
		t.Fatal(err)
	}
	none, err := councilFor(t, newWork(kindAsk, "", "q2"), "none")
	if err != nil {
		t.Fatal(err)
	}
	memberTurn, err := councilFor(t, newWork(kindMember, "fleet", "q3"), "read")
	if err != nil {
		t.Fatal(err)
	}
	last, err := councilFor(t, newWork(kindAsk, "", "q4"), "read")
	if err != nil {
		t.Fatal(err)
	}

	// The first council is untouched by the three that came after it.
	if first.lead == nil || first.lead.Level != "creator" {
		t.Errorf("the first council's lead is %+v", first.lead)
	}
	if got := first.lead.Tools; !slices.Equal(got, []string{"inspect_pod", "list_robots", "whoami"}) {
		t.Errorf("the first council's lead tools = %v", got)
	}
	if first.reviewer == nil || len(first.members) != 2 {
		t.Errorf("the first council lost its reviewer or members: %+v", first)
	}
	// The last one is a whole ask turn again.
	if last.lead == nil || last.reviewer == nil {
		t.Fatalf("the last council is %+v", last)
	}
	if got := last.enum(); !slices.Equal(got, []string{"fleet", "coder"}) {
		t.Errorf("the last council's enum = %v", got)
	}
	// And each one matches the platform's snapshot at its level: run levels, tools and skills.
	for level, c := range map[string]*turnCouncil{"creator": first, "none": none, "read": last} {
		want := stdSnapshot(t, level)
		if !slices.Equal(c.lead.Tools, want.Lead.Tools) || !slices.Equal(c.lead.Skills, want.Lead.Skills) {
			t.Errorf("%s: the lead's tools or skills differ from the fixture", level)
		}
		if c.lead.runLevel != want.Lead.Level {
			t.Errorf("%s: the lead runs at %q, the fixture is at %q", level, c.lead.runLevel, want.Lead.Level)
		}
		for i, m := range want.Members {
			if c.members[i].runLevel != m.Level || !slices.Equal(c.members[i].Tools, m.Tools) || !slices.Equal(c.members[i].Skills, m.Skills) {
				t.Errorf("%s: member %d is %+v, the fixture is %+v", level, i, c.members[i], m)
			}
		}
		if c.reviewer.runLevel != want.Reviewer.Level || !slices.Equal(c.reviewer.Tools, want.Reviewer.Tools) ||
			!slices.Equal(c.reviewer.Skills, want.Reviewer.Skills) {
			t.Errorf("%s: the reviewer is %+v, the fixture is %+v", level, c.reviewer, want.Reviewer)
		}
	}
	// The member turn is Fleet at read, as the platform's snapshot has it.
	wantFleet := stdSnapshot(t, "read").Members[0]
	if len(memberTurn.members) != 1 {
		t.Fatalf("the member turn's council has %d members", len(memberTurn.members))
	}
	if m := memberTurn.members[0]; m.runLevel != wantFleet.Level || !slices.Equal(m.Tools, wantFleet.Tools) ||
		!slices.Equal(m.Skills, wantFleet.Skills) {
		t.Errorf("the member turn's member is %+v, the fixture is %+v", m, wantFleet)
	}
}

// The embedded Standard council decodes, and it is the creator fixture byte for byte.
func TestLegacyStandardEmbedded(t *testing.T) {
	raw, err := os.ReadFile("testdata/standard_snapshot_creator.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyStandardJSON, raw) {
		t.Error("legacy_standard.json is not byte-equal to testdata/standard_snapshot_creator.json")
	}
	s, err := decodeLegacyStandard()
	if err != nil {
		t.Fatalf("decodeLegacyStandard: %v", err)
	}
	if s.Lead == nil || len(s.Members) != 2 || s.Reviewer == nil {
		t.Errorf("the embedded council is %+v", s)
	}
	if s.Members[0].Slug != "fleet" || s.Members[1].Slug != "coder" {
		t.Errorf("the members are %q and %q", s.Members[0].Slug, s.Members[1].Slug)
	}
}

// The contract (spec 4.5): every fixture's role tools are the fake catalog's names at the role's
// level, filtered to the role's groups through the platform's own group table, sorted.
func TestStandardFixturesToolsAreTheCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tool_groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	var groups map[string]string
	if err := json.Unmarshal(raw, &groups); err != nil {
		t.Fatalf("tool_groups.json: %v", err)
	}
	for _, level := range []string{"none", "read", "creator"} {
		s := stdSnapshot(t, level)
		roles := append([]*hub.RoleSnapshot{s.Lead, s.Reviewer}, func() []*hub.RoleSnapshot {
			out := make([]*hub.RoleSnapshot, 0, len(s.Members))
			for i := range s.Members {
				out = append(out, &s.Members[i])
			}
			return out
		}()...)
		for _, r := range roles {
			if r == nil {
				continue
			}
			var want []string
			for _, name := range catalogNames(r.Level) {
				group, known := groups[name]
				if !known {
					t.Fatalf("%s is in no group of the table", name)
				}
				if slices.Contains(r.Groups, group) {
					want = append(want, name)
				}
			}
			slices.Sort(want)
			if r.Runtime == runtimeCoder {
				want = nil
			}
			if !slices.Equal(r.Tools, want) {
				t.Errorf("%s %s at %s: tools %v, want %v", level, r.Slug, r.Level, r.Tools, want)
			}
		}
	}
}

// ---------- a council of the test's own ----------

// customRoster is a five-member council at creator with a lead of its own name and no reviewer. Its
// members cover what a roster may be: a creator member that may look inside pods, a read member
// with guides and the results group, a non-data member with the results group and nothing else, and
// two more that are there to fill the roster.
func customRoster(s *hub.Snapshot) {
	s.Hash = "sha256:" + strings.Repeat("b", 64)
	s.Level = "creator"
	s.Lead.Slug = "chief"
	s.Lead.Name = "Chief"
	s.Lead.Persona = strPtr("the chair")
	s.Lead.Description = "leads this council."
	s.Lead.Instructions = "Always explain the trade-offs.\nKeep it short."
	s.Lead.Tools = catalogNames("creator")
	s.Lead.Model, s.Lead.Effort = "haiku", "low"

	base := s.Members[0] // the Standard Fleet, the inproc member every role here is built from.
	mk := func(slug, name, persona, description, instructions, modelID, effort, level string,
		groups, tools, skills []string, budget *hub.RoleBudget) hub.RoleSnapshot {
		m := base
		m.Slug, m.Name = slug, name
		m.Persona = strPtr(persona)
		m.Description = description
		m.Instructions = instructions
		m.Model, m.Effort, m.Level = modelID, effort, level
		m.Groups, m.Tools, m.Skills = groups, tools, skills
		m.Results = slices.Contains(groups, "results")
		m.Budget = budget
		return m
	}
	probe := mk("probe", "Probe", "the inspector", "looks inside pods.",
		"Read the pod, then say what you saw.", "sonnet", "high", "creator",
		[]string{"inspect"}, []string{"inspect_pod"}, []string{"teknoir-debug"},
		&hub.RoleBudget{MaxToolRounds: 4, MaxTokens: 40000})
	reader := mk("reader", "Reader", "the reader", "reads the teamspace.",
		"Answer from the teamspace's own data.", "haiku", "low", "read",
		[]string{"core_read", "results"}, []string{"list_robots", "whoami"},
		[]string{"teknoir-robots", "teknoir-platform"},
		&hub.RoleBudget{MaxToolRounds: 6, MaxTokens: 60000})
	planner := mk("planner", "Planner", "", "plans on the reports.",
		"Plan from what the others found.", "sonnet", "medium", "creator",
		[]string{"results"}, []string{}, nil,
		&hub.RoleBudget{MaxToolRounds: 2, MaxTokens: 40000})
	late := mk("late", "Late", "the late one", "looks again.",
		"Take your time.", "haiku", "medium", "read",
		[]string{"core_read"}, []string{"whoami"}, nil,
		&hub.RoleBudget{MaxToolRounds: 3, MaxTokens: 30000})
	fifth := mk("fifth", "Fifth", "the fifth", "does the rest.",
		"Fill the gaps.", "haiku", "medium", "read",
		[]string{"core_read"}, []string{"whoami"}, nil,
		&hub.RoleBudget{MaxToolRounds: 3, MaxTokens: 30000})

	s.Members = []hub.RoleSnapshot{probe, reader, planner, late, fifth}
	s.Reviewer = nil
}

// leadAddendum is customRoster's lead instructions as roleAddendum writes them: its own paragraph.
const leadAddendum = "Notes from the people who set up this council (the rules above come first):\nAlways explain the trade-offs.\nKeep it short."

// assertLeadAddendumAfterBrief pins the lead's addendum as its own paragraph right after briefLine
// and right before the skills paragraph: the rules above it come first.
func assertLeadAddendumAfterBrief(t *testing.T, what, system, addendum string) {
	t.Helper()
	want := "\n\n" + briefLine + "\n\n" + addendum + "\n\n" + skillsLine
	if !strings.Contains(system, want) {
		t.Errorf("the %s prompt does not hold the addendum between the brief line and the guides:\n%s", what, system)
	}
	if strings.Count(system, addendum) != 1 {
		t.Errorf("the %s prompt holds the addendum %d times", what, strings.Count(system, addendum))
	}
}

// customTools is the fake catalog of the custom roster's tests: a tool to inspect a pod at creator,
// and the read tools the others use.
func customTools() map[string][]string {
	return map[string][]string{
		"none":    {"whoami"},
		"read":    {"list_robots", "whoami"},
		"creator": {"inspect_pod", "list_robots", "whoami"},
	}
}

// TestCustomRoster runs a turn of the custom council end to end and checks every request it makes.
func TestCustomRoster(t *testing.T) {
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, customRoster)
	h := goldenHarness(t, work)
	h.tools.catalog = customTools()
	h.whoamiAt("creator")

	h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1",
		taskOf("probe", "the pod's logs", "it is the question"),
		taskOf("reader", "the teamspace's robots", "the facts"),
		taskOf("planner", "a plan from the reports", "the plan"),
	)))
	h.model.push("member:Probe", toolStep("tool_use", apiCall{id: "tu_p", name: "inspect_pod", input: map[string]any{}}),
		textStep(reportJSON("the pod is healthy", "tu_p"), "end_turn"))
	h.model.push("member:Reader", textStep(reportJSON("two robots are degraded"), "end_turn"))
	h.model.push("member:Planner", textStep(reportJSON("roll the image forward"), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("the pod is healthy, so the image is the problem", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if left := h.model.leftover(); len(left) > 0 {
		t.Fatalf("the fake model was not asked for every scripted answer: %v left over", left)
	}
	route := h.model.requests("lead-route:Chief")
	if len(route) != 1 {
		t.Fatalf("%d route requests", len(route))
	}
	req := route[0]
	if req.Model != model.ModelHaiku || req.Effort != "low" {
		t.Errorf("the route is %s at %s, want %s at low", req.Model, req.Effort, model.ModelHaiku)
	}
	for _, want := range []string{
		"You are the Chief of the council of the TeknoirBot scout in teamspace teknoir-demo.",
		"The council: Probe looks inside pods. Reader reads the teamspace. Planner plans on the reports. Late looks again. Fifth does the rest.",
		"Notes from the people who set up this council (the rules above come first):\nAlways explain the trade-offs.\nKeep it short.",
		"read_skill gives you the Teknoir guides:",
	} {
		if !strings.Contains(req.System, want) {
			t.Errorf("the route prompt does not hold %q", want)
		}
	}
	assertLeadAddendumAfterBrief(t, "route", req.System, leadAddendum)
	// No reviewer, so no council-line sentence about one, and no Coder sentence anywhere.
	if strings.Contains(req.System, "convene the Coder") || strings.Contains(req.System, "only the Coder") {
		t.Errorf("the route prompt talks about a Coder the council has not:\n%s", req.System)
	}
	if strings.Contains(req.System, onlyCoderTask) {
		t.Error("the route prompt limits coder tasks, and this council has no Coder")
	}
	if strings.Contains(req.System, "You only read. Never claim you changed anything. Propose a platform change as the exact tnctl command for the person to run,") {
		t.Error("the route prompt offers a Coder that runs the change, and this council has none")
	}
	// The route's tools are the platform's, the lead's own, plus read_skill, read_result and the
	// convene.
	wantTools := []string{"inspect_pod", "list_robots", "whoami", toolReadSkill, toolReadResult, toolConvene}
	if got := toolNames(req); !slices.Equal(got, wantTools) {
		t.Errorf("the route tools = %v, want %v", got, wantTools)
	}

	// The synthesis is the same prompt without the route rule, and without the convene.
	answers := h.model.requests("lead-answer:Chief")
	if len(answers) != 1 {
		t.Fatalf("%d synthesis requests", len(answers))
	}
	syn := answers[0]
	if syn.Model != model.ModelHaiku || syn.Effort != "low" {
		t.Errorf("the synthesis is %s at %s, want %s at low", syn.Model, syn.Effort, model.ModelHaiku)
	}
	for _, want := range []string{
		"You are the Chief of the council of the TeknoirBot scout in teamspace teknoir-demo.",
		"The council: Probe looks inside pods. Reader reads the teamspace. Planner plans on the reports. Late looks again. Fifth does the rest.",
		"Notes from the people who set up this council (the rules above come first):\nAlways explain the trade-offs.\nKeep it short.",
		"read_skill gives you the Teknoir guides:",
	} {
		if !strings.Contains(syn.System, want) {
			t.Errorf("the synthesis prompt does not hold %q", want)
		}
	}
	assertLeadAddendumAfterBrief(t, "synthesis", syn.System, leadAddendum)
	if strings.Contains(syn.System, "convene_council") {
		t.Error("the synthesis prompt still routes")
	}
	// The synthesis is told why there was no critique, in the note of the critique it gets.
	if last := paramText(syn.Messages[len(syn.Messages)-1]); !strings.Contains(last, `"note":"The critique was not run (no reviewer in this council)."`) {
		t.Errorf("the synthesis has no note that the council has no reviewer: %s", cutUTF8(last, 400, "…"))
	}
	wantSynTools := []string{"inspect_pod", "list_robots", "whoami", toolReadSkill, toolReadResult}
	if got := toolNames(syn); !slices.Equal(got, wantSynTools) {
		t.Errorf("the synthesis tools = %v, want %v", got, wantSynTools)
	}
	// The three members' own requests.
	for _, tc := range []struct {
		role   string
		model  string
		effort string
		system []string
		not    []string
		tools  []string
	}{
		{
			role: "member:Probe", model: model.ModelSonnet, effort: "high",
			system: []string{
				"You are Probe, the inspector of the council of the TeknoirBot scout in teamspace teknoir-demo on the Teknoir platform.",
				levelCreatorLine,
				"Read the pod, then say what you saw.",
				"At most 4 tool rounds.",
				"read_skill gives you the Teknoir guides:",
				"teknoir-debug: ",
				"findings (each claim with the ids of the tool calls that show it, as evidence)",
			},
			not:   []string{"teknoir-robots: "},
			tools: []string{"inspect_pod", toolReadSkill},
		},
		{
			role: "member:Reader", model: model.ModelHaiku, effort: "low",
			system: []string{
				"You are Reader, the reader of the council of the TeknoirBot scout in teamspace teknoir-demo on the Teknoir platform.",
				levelReadLine,
				"Answer from the teamspace's own data.",
				"At most 6 tool rounds.",
				"teknoir-robots: ",
			},
			not:   []string{levelCreatorLine},
			tools: []string{"list_robots", "whoami", toolReadSkill, toolReadResult},
		},
		{
			role: "member:Planner", model: model.ModelSonnet, effort: "medium",
			system: []string{
				"You are Planner, a member of the council of the TeknoirBot scout in teamspace teknoir-demo on the Teknoir platform.",
				"Plan from what the others found.",
				"At most 2 tool rounds.",
				"findings (each claim, with an empty evidence list)",
			},
			not:   []string{levelCreatorLine, levelReadLine, levelNoneLine, "read_skill gives you", "teknoir-robots: "},
			tools: []string{toolReadResult},
		},
	} {
		t.Run(tc.role, func(t *testing.T) {
			reqs := h.model.requests(tc.role)
			if len(reqs) == 0 {
				t.Fatalf("%s made no request", tc.role)
			}
			r := reqs[0]
			if r.Model != tc.model || r.Effort != tc.effort {
				t.Errorf("the request is %s at %s, want %s at %s", r.Model, r.Effort, tc.model, tc.effort)
			}
			for _, want := range tc.system {
				if !strings.Contains(r.System, want) {
					t.Errorf("the prompt does not hold %q:\n%s", want, r.System)
				}
			}
			for _, unwanted := range tc.not {
				if strings.Contains(r.System, unwanted) {
					t.Errorf("the prompt holds %q, and it must not:\n%s", unwanted, r.System)
				}
			}
			if got := toolNames(r); !slices.Equal(got, tc.tools) {
				t.Errorf("the tools = %v, want %v", got, tc.tools)
			}
			// The first message is the lead's task for the member, under the lead's own name.
			if !strings.HasPrefix(paramText(r.Messages[0]), "Task from the Chief: ") {
				t.Errorf("the first message is %q", cutUTF8(paramText(r.Messages[0]), 60, "…"))
			}
		})
	}

	// read_skill's enum is the role's own guides, and Probe's request offers no pod tool to Reader,
	// nor the guide Reader has.
	probeTools := h.model.requests("member:Probe")[0].Tools
	if got := readSkillEnum(t, probeTools); !slices.Equal(got, []string{"teknoir-debug"}) {
		t.Errorf("Probe's read_skill enum = %v", got)
	}
	readerTools := h.model.requests("member:Reader")[0].Tools
	if got := readSkillEnum(t, readerTools); !slices.Equal(got, []string{"teknoir-robots", "teknoir-platform"}) {
		t.Errorf("Reader's read_skill enum = %v", got)
	}
	if names := toolNames(h.model.requests("member:Reader")[0]); slices.Contains(names, "inspect_pod") {
		t.Error("Reader's request offers inspect_pod")
	}

	// The events carry the members' slugs and names, and the lead's wire id is the Captain's.
	starts := p.ofType(hub.EventMemberStart)
	seen := map[string]string{}
	for _, e := range starts {
		d := dataOf(t, e)
		seen[d["member"].(string)] = d["name"].(string)
	}
	if seen["probe"] != "Probe" || seen["reader"] != "Reader" || seen["planner"] != "Planner" {
		t.Errorf("the member_start events are %v", seen)
	}
	if n := len(p.ofType(hub.EventMemberDone)); n != 3 {
		t.Errorf("%d member_done events, want 3", n)
	}
	// The convene enum is the roster's own.
	if got := conveneEnum(t, h.model.requests("lead-route:Chief")[0].Tools); !slices.Equal(got, []string{"probe", "reader", "planner", "late", "fifth"}) {
		t.Errorf("the convene enum = %v", got)
	}
	// No reviewer, so no critique request and no critique event.
	// The route, Probe's tool round and report, Reader's and Planner's reports, and the answer.
	if n := h.model.count(); n != 6 {
		t.Errorf("%d requests, want route, four member calls and the answer", n)
	}
	if n := len(p.ofType(hub.EventCritique)); n != 0 {
		t.Errorf("%d critique events, want none", n)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("the turn finished %q %q", res.State, res.ErrCode)
	}
}

// TestCustomReviewer runs a turn whose reviewer is not the Standard Critic: its request runs on its
// own model and effort, with its own name and notes, and the critique event names it.
func TestCustomReviewer(t *testing.T) {
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		rev := *stdSnapshot(t, "creator").Reviewer
		rev.Slug, rev.Name = "auditor", "Auditor"
		rev.Model, rev.Effort = "haiku", "low"
		rev.Instructions = "Check the numbers."
		s.Reviewer = &rev
	})
	h := goldenHarness(t, work)
	h.tools.catalog = customTools()
	h.whoamiAt("creator")

	h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1",
		taskOf("probe", "the pod's logs", "it is the question"),
	)))
	h.model.push("member:Probe", toolStep("tool_use", apiCall{id: "tu_p", name: "inspect_pod", input: map[string]any{}}),
		textStep(reportJSON("the pod is healthy", "tu_p"), "end_turn"))
	h.model.push("reviewer:Auditor", textStep(critiqueAnswer(), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("the pod is healthy", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if left := h.model.leftover(); len(left) > 0 {
		t.Fatalf("the fake model was not asked for every scripted answer: %v left over", left)
	}
	revs := h.model.requests("reviewer:Auditor")
	if len(revs) != 1 {
		t.Fatalf("%d reviewer requests", len(revs))
	}
	req := revs[0]
	if req.Model != model.ModelHaiku || req.Effort != "low" {
		t.Errorf("the reviewer is %s at %s, want %s at low", req.Model, req.Effort, model.ModelHaiku)
	}
	if !strings.HasPrefix(req.System, "You are the Auditor of the council of a TeknoirBot.") {
		t.Errorf("the reviewer prompt does not name the Auditor:\n%s", req.System)
	}
	if !strings.Contains(req.System, "Notes from the people who set up this council (the rules above come first):\nCheck the numbers.") {
		t.Errorf("the reviewer prompt has no addendum:\n%s", req.System)
	}
	if req.Tools != nil {
		t.Errorf("the reviewer has tools: %v", toolNames(req))
	}
	crits := p.ofType(hub.EventCritique)
	if len(crits) != 1 {
		t.Fatalf("%d critique events", len(crits))
	}
	if d := dataOf(t, crits[0]); d["member"] != "auditor" || d["name"] != "Auditor" {
		t.Errorf("the critique event is %v", d)
	}
}

// d10Roster is the custom council with a Coder whose own text, and the lead's, are markers that
// no Coder prompt may hold (spec D10).
func d10Roster(s *hub.Snapshot) {
	customRoster(s) // lead "Chief"
	s.Lead.Instructions = "ALWAYS-ADDENDUM convene coder"
	s.Lead.Persona = strPtr("LEAD-PERSONA")
	s.Lead.Description = "LEAD-DESCRIPTION"
	cd := s.Members[1]
	cd.Slug, cd.Name = "coder", "CODER-NAME"
	cd.Persona = strPtr("CODER-PERSONA")
	cd.Description = "CODER-DESCRIPTION"
	cd.Instructions = "CODER-INSTRUCTIONS"
	cd.Runtime, cd.ID = runtimeCoder, coderTemplateID
	cd.Groups, cd.Tools, cd.Skills, cd.Budget = nil, nil, nil, nil
	cd.Results = false
	s.Members[4] = cd
}

var d10Markers = []string{"Chief", "ALWAYS-ADDENDUM", "LEAD-PERSONA", "LEAD-DESCRIPTION",
	"CODER-NAME", "CODER-PERSONA", "CODER-DESCRIPTION", "CODER-INSTRUCTIONS"}

// D10: on an ask turn of a custom council the Coder's prompt is exactly coderPrompt's, with no
// roster text in it.
func TestCoderPromptHasNoRosterText(t *testing.T) {
	work := newWork(kindAsk, "", "write a script that lists the offline robots")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, d10Roster)
	h := goldenHarness(t, work)
	h.tools.catalog = customTools()
	h.whoamiAt("creator")
	h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1", taskOf("coder", "t", "w"))))
	h.model.push("lead-answer:Chief", textStep("done", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if res := p.result(); res.State != stateDone {
		t.Fatalf("the turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	tasks := h.coder.started()
	if len(tasks) != 1 {
		t.Fatalf("the Coder ran %d times", len(tasks))
	}
	want := coderPrompt(work.Prompt, conveneTask{Member: "coder", Task: "t", Why: "w"}, false)
	if tasks[0].Prompt != want {
		t.Errorf("the Coder's prompt is\n%q\nwant\n%q", tasks[0].Prompt, want)
	}
	for _, m := range d10Markers {
		if strings.Contains(tasks[0].Prompt, m) {
			t.Errorf("the Coder's prompt holds roster text %q", m)
		}
	}
}

// D10: a snapshot member turn sent to the Coder gives it the person's prompt and nothing else.
func TestCoderMemberTurnPromptHasNoRosterText(t *testing.T) {
	work := newWork(kindMember, "coder", "write a script that prints hello")
	s := customSnapshot(t, d10Roster)
	s.Lead, s.Reviewer, s.Roster, s.Budget = nil, nil, nil, nil
	s.Members = s.Members[4:]
	work.Council = s
	h := goldenHarness(t, work)
	h.whoamiAt("creator")
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if res := p.result(); res.State != stateDone {
		t.Fatalf("the turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	tasks := h.coder.started()
	if len(tasks) != 1 || tasks[0].Prompt != work.Prompt {
		t.Fatalf("the Coder tasks are %+v, want one with the prompt %q", tasks, work.Prompt)
	}
	for _, m := range d10Markers {
		if strings.Contains(tasks[0].Prompt, m) {
			t.Errorf("the Coder's prompt holds roster text %q", m)
		}
	}
}

// readSkillEnum is the enum of a request's read_skill tool, or nil when it has none.
func readSkillEnum(t *testing.T, list []anthropic.ToolUnionParam) []string {
	t.Helper()
	for _, tool := range list {
		if tool.OfTool == nil || tool.OfTool.Name != toolReadSkill {
			continue
		}
		return stringEnum(t, tool, "name")
	}
	return nil
}

// conveneEnum is the enum of a request's convene_council tool, or nil when it has none.
func conveneEnum(t *testing.T, list []anthropic.ToolUnionParam) []string {
	t.Helper()
	for _, tool := range list {
		if tool.OfTool == nil || tool.OfTool.Name != toolConvene {
			continue
		}
		return stringEnum(t, tool, "member")
	}
	return nil
}

// stringEnum is the enum of the schema property named prop, wherever in the schema it sits: the
// convene's member is inside the tasks array's items.
func stringEnum(t *testing.T, tool anthropic.ToolUnionParam, prop string) []string {
	t.Helper()
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal the tool: %v", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal the tool: %v", err)
	}
	var find func(any) []string
	find = func(x any) []string {
		m, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		if props, ok := m["properties"].(map[string]any); ok {
			if p, ok := props[prop].(map[string]any); ok {
				if e, ok := p["enum"].([]any); ok {
					out := make([]string, 0, len(e))
					for _, s := range e {
						out = append(out, s.(string))
					}
					return out
				}
			}
		}
		for _, v := range m {
			if got := find(v); got != nil {
				return got
			}
		}
		return nil
	}
	return find(v)
}

// Below the bot: a role claimed at creator, run at read, offers and calls only what read allows, and
// its prompt says which level it is at. The access drop filters the tools; it never fails the turn.
func TestAccessDropBelowTheBot(t *testing.T) {
	work := newWork(kindAsk, "", "which pods are unhealthy?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, customRoster)
	h := goldenHarness(t, work)
	h.whoamiAt("read")
	h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1",
		taskOf("probe", "the pod's logs", "it is the question"),
	)))
	h.model.push("member:Probe", toolStep("tool_use", apiCall{id: "tu_p", name: "inspect_pod", input: map[string]any{}}),
		textStep(reportJSON("I cannot look inside pods", "tu_p"), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("the pod's logs need creator access", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests("lead-route:Chief")[0]
	// The fake catalog's own order, filtered to what read allows.
	if got, want := toolNames(route), []string{"whoami", "list_robots", toolReadSkill, toolReadResult, toolConvene}; !slices.Equal(got, want) {
		t.Errorf("the route's tools at read = %v, want %v", got, want)
	}
	probes := h.model.requests("member:Probe")
	if len(probes) != 2 {
		t.Fatalf("%d Probe requests, want its refused round and its report", len(probes))
	}
	probe := probes[0]
	// Probe's own list was claimed at creator, where it held the pod tool: at read none of it is
	// allowed, so the request offers only the guide it was given.
	if got, want := toolNames(probe), []string{toolReadSkill}; !slices.Equal(got, want) {
		t.Errorf("Probe's tools at read = %v, want %v", got, want)
	}
	if !strings.Contains(probe.System, levelReadLine) || strings.Contains(probe.System, levelCreatorLine) {
		t.Errorf("Probe's prompt is not at read:\n%s", probe.System)
	}
	// The call it still made is refused, and the fake recorded no call.
	last := toolResults(probes[1].Messages[len(probes[1].Messages)-1])
	if len(last) != 1 || !strings.Contains(last[0], `"inspect_pod" is not a tool a council member at level "read" may call`) {
		t.Errorf("the inspect_pod result is %q", last)
	}
	if called := h.tools.called(); slices.Contains(called, "inspect_pod") {
		t.Errorf("inspect_pod ran at read: %v", called)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("the turn finished %q %q", res.State, res.ErrCode)
	}
}

// Below the bot, Standard: the creator snapshot run at read. Fleet's list drops inspect_pod, its
// prompt is at read, and the call it still makes is refused before it reaches the platform.
func TestAccessDropBelowTheBotStandard(t *testing.T) {
	h := councilReadHolds(t, "read", "inspect_pod")
	h.useSnapshot(t, "creator")
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if left := h.model.leftover(); len(left) > 0 {
		t.Fatalf("the fake model was not asked for every scripted answer: %v left over", left)
	}
	route := h.model.requests(roleRoute)[0]
	if got, want := toolNames(route), []string{"whoami", "list_robots", toolReadSkill, toolReadResult, toolConvene}; !slices.Equal(got, want) {
		t.Errorf("the route's tools at read = %v, want %v", got, want)
	}
	fleet := h.model.requests(roleFleet)
	if len(fleet) != 2 {
		t.Fatalf("%d Fleet requests, want its refused round and its report", len(fleet))
	}
	if got, want := toolNames(fleet[0]), []string{"whoami", "list_robots", toolReadSkill, toolReadResult}; !slices.Equal(got, want) {
		t.Errorf("Fleet's tools at read = %v, want %v", got, want)
	}
	if !strings.Contains(fleet[0].System, levelReadLine) || strings.Contains(fleet[0].System, levelCreatorLine) {
		t.Errorf("Fleet's prompt is not at read:\n%s", fleet[0].System)
	}
	last := toolResults(fleet[1].Messages[len(fleet[1].Messages)-1])
	if len(last) != 1 || last[0] != `"inspect_pod" is not a tool a council member at level "read" may call` {
		t.Errorf("the inspect_pod result is %q", last)
	}
	if called := h.tools.called(); slices.Contains(called, "inspect_pod") {
		t.Errorf("inspect_pod ran at read: %v", called)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("the turn finished %q %q", res.State, res.ErrCode)
	}
}

// Per-role tools: a tool the level allows but the role's own list does not is refused as not a tool
// of this member; a local tool the role was not given the same way; and read_skill refuses a guide
// the role does not have.
func TestPerRoleTools(t *testing.T) {
	work := newWork(kindAsk, "", "which robots are degraded?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, customRoster)
	h := goldenHarness(t, work)
	h.whoamiAt("creator")
	h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1",
		taskOf("probe", "the robot", "it is the question"),
		taskOf("planner", "a plan", "the plan"),
		taskOf("reader", "the robots", "the facts"),
	)))
	// Probe may inspect a pod and not list the robots; it may not read a result it has no group for.
	h.model.push("member:Probe",
		toolStep("tool_use",
			apiCall{id: "tu_p1", name: "list_robots", input: map[string]any{}},
			apiCall{id: "tu_p2", name: toolReadResult, input: map[string]any{"ref": "r1", "offset": 0}},
			apiCall{id: "tu_p3", name: toolReadSkill, input: map[string]any{"name": "teknoir-robots", "offset": 0}}),
		textStep(reportJSON("the pod is healthy"), "end_turn"))
	// Planner has no guides, so it may not read one at all.
	h.model.push("member:Planner", toolStep("tool_use", apiCall{id: "tu_pl1", name: toolReadSkill, input: map[string]any{"name": "teknoir-platform", "offset": 0}}),
		textStep(reportJSON("roll the image forward"), "end_turn"))
	// Reader only reports: it calls no tool, so a list_robots call recorded below is Probe's.
	h.model.push("member:Reader", textStep(reportJSON("two robots are degraded"), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("the pod is healthy", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	probe := h.model.requests("member:Probe")
	if len(probe) != 2 {
		t.Fatalf("%d Probe requests", len(probe))
	}
	results := toolResults(probe[1].Messages[len(probe[1].Messages)-1])
	if len(results) != 3 {
		t.Fatalf("%d results, want three", len(results))
	}
	for i, want := range []string{
		`"list_robots" is not a tool this council member may call`,
		`"read_result" is not a tool this council member may call`,
		"there is no guide named teknoir-robots; use one of the guides your instructions list",
	} {
		if !strings.Contains(results[i], want) {
			t.Errorf("result %d is %q, want %q", i, results[i], want)
		}
	}
	if called := h.tools.called(); slices.Contains(called, "list_robots") {
		t.Errorf("Probe's refused call ran: %v", called)
	}
	planner := h.model.requests("member:Planner")
	pr := toolResults(planner[1].Messages[len(planner[1].Messages)-1])
	if len(pr) != 1 || !strings.Contains(pr[0], `"read_skill" is not a tool this council member may call`) {
		t.Errorf("Planner's read_skill result is %q", pr)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("the turn finished %q %q", res.State, res.ErrCode)
	}
}

// A version-pinned role renders its own instructions: the version is the platform's, and the
// daemon uses what the snapshot holds.
func TestPinnedRoleInstructions(t *testing.T) {
	s := customSnapshot(t, customRoster)
	s.Members[0].Version = 4
	s.Members[0].Instructions = "This is version 4's way of reading a pod."
	c := customCouncilWith(t, s, "creator")
	got := memberSystem(c.members[0], factsOf("creator", testFacts("scout", "teknoir-demo"), "Anders"), "creator", testBook())
	if !strings.Contains(got, "This is version 4's way of reading a pod.") {
		t.Errorf("the pinned role's instructions are not in its prompt:\n%s", got)
	}
}

// A data role at none is not convened, and a member turn sent to one fails no_access with its name.
func TestDataRoleAtNone(t *testing.T) {
	s := customSnapshot(t, customRoster)
	s.Members[0].Level = "none"
	s.Members[0].Tools = []string{}
	c := customCouncilWith(t, s, "read")
	if _, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"probe","task":"look","why":""}]}`), c); err == nil ||
		err.Error() != "at access level none only reader, planner, late or fifth tasks may be convened" {
		t.Errorf("a data task convened at none: %v", err)
	}
	if got := captainRouteSystem(factsOf("read", nil, "Anders"), "council", c, testBook()); !strings.Contains(got,
		" At access level none only Reader, Planner, Late or Fifth can be convened.") {
		t.Errorf("the level-none sentence:\n%s", got)
	}
	// The same council as a turn: the platform's report of the bot is at read, and the route system
	// carries the level-none sentence naming the members that may still be convened.
	ask := customSnapshot(t, customRoster)
	ask.Members[0].Level, ask.Members[0].Tools = "none", []string{}
	aw := newWork(kindAsk, "", "why is r2 degraded?")
	aw.Council = ask
	ha := goldenHarness(t, aw)
	ha.whoamiAt("read")
	ha.model.push("lead-route:Chief", textStep("r2 is fine", "end_turn"))
	ha.start(t)
	ha.waitFinish(t)
	ha.stop(t)
	reqs := ha.model.requests("lead-route:Chief")
	if len(reqs) == 0 || !strings.Contains(reqs[0].System,
		" At access level none only Reader, Planner, Late or Fifth can be convened.") {
		t.Errorf("the route system of the turn lacks the level-none sentence")
	}
	// The member turn fails with the role's name.
	work := newWork(kindMember, "probe", "look inside the pod")
	work.Council = s
	work.Council.Lead, work.Council.Reviewer, work.Council.Roster = nil, nil, nil
	work.Council.Members = []hub.RoleSnapshot{s.Members[0]}
	h := goldenHarness(t, work)
	h.whoamiAt("read")
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	res := p.result()
	if res.State != stateFailed || res.ErrCode != codeNoAccess || res.ErrMsg != "At access level none, Probe has no tools to use." {
		t.Errorf("the member turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	if n := h.model.count(); n != 0 {
		t.Errorf("%d model calls, want none", n)
	}
}

// The refusals of a convene at none, for a council with none, one and two members that may be
// convened, and the same council's level-none sentence in its prompt.
func TestConveneAtNoneTexts(t *testing.T) {
	onlyCoder := stdAskCouncil(t, "none")
	if _, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"fleet","task":"look","why":""}]}`), onlyCoder); err == nil ||
		err.Error() != "at access level none only a coder task may be convened" {
		t.Errorf("one convenable: %v", err)
	}
	// Two convenable members, in roster order: Planner and Late. Late works on reports only, so it is
	// not a data role; probe, reader and fifth are data members at level none.
	two := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		for i := range s.Members {
			if s.Members[i].Runtime != runtimeCoder {
				s.Members[i].Level, s.Members[i].Tools = "read", []string{"whoami"}
			}
		}
		s.Members[3].Groups, s.Members[3].Tools, s.Members[3].Results = []string{"results"}, []string{}, true
	}), "none")
	if _, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"probe","task":"look","why":""}]}`), two); err == nil ||
		err.Error() != "at access level none only planner or late tasks may be convened" {
		t.Errorf("two convenable: %v", err)
	}
	if got := captainRouteSystem(factsOf("none", nil, "Anders"), "council", two, testBook()); !strings.Contains(got,
		" At access level none only Planner or Late can be convened.") {
		t.Errorf("two convenable, the sentence:\n%s", got)
	}
	// None convenable.
	none := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		for i := range s.Members {
			// Every member works with platform data, so none of them may be convened at none.
			s.Members[i].Level, s.Members[i].Tools = "none", []string{"whoami"}
			s.Members[i].Groups, s.Members[i].Results = []string{"core_read"}, false
		}
	}), "none")
	if _, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"probe","task":"look","why":""}]}`), none); err == nil ||
		err.Error() != "at access level none no member may be convened" {
		t.Errorf("none convenable: %v", err)
	}
	// And the prompts: the sentence names the members that may be convened, and is absent when every
	// one of them may.
	f := factsOf("none", nil, "Anders")
	if got := captainRouteSystem(f, "council", onlyCoder, testBook()); !strings.Contains(got, " At access level none only the Coder can be convened.") {
		t.Errorf("the Standard council's sentence is gone:\n%s", got)
	}
	if got := captainRouteSystem(f, "council", none, testBook()); !strings.Contains(got, " At access level none no member can be convened.") {
		t.Errorf("a council with no convenable member:\n%s", got)
	}
	allRead := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		for i := range s.Members {
			s.Members[i].Level, s.Members[i].Tools = "read", []string{"whoami"}
		}
	}), "read")
	if got := captainRouteSystem(factsOf("read", nil, "Anders"), "council", allRead, testBook()); strings.Contains(got, "At access level none") {
		t.Errorf("a council whose members may all be convened still has the sentence:\n%s", got)
	}
}

// A custom member's own budget, not the Standard 10/120k, ends its tool loop, and its stopped
// report carries its own name.
func TestRoleBudgetForcesReport(t *testing.T) {
	probeOnly := func(budget hub.RoleBudget) func(*hub.Snapshot) {
		return func(s *hub.Snapshot) {
			customRoster(s)
			s.Members[0].Budget = &budget // Probe
		}
	}
	run := func(t *testing.T, budget hub.RoleBudget, probe ...*scriptStep) (*fakeModel, []map[string]any) {
		work := newWork(kindAsk, "", "which pods are unhealthy?")
		work.Options.Mode = modeCouncil
		work.Council = customSnapshot(t, probeOnly(budget))
		h := goldenHarness(t, work)
		h.tools.catalog = customTools()
		h.whoamiAt("creator")
		h.model.push("lead-route:Chief", toolStep("tool_use", conveneCall("tu_1",
			taskOf("probe", "the pod's logs", "it is the question"))))
		h.model.push("member:Probe", probe...)
		h.model.push("lead-answer:Chief", textStep("the answer", "end_turn"))
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		var dones []map[string]any
		for _, e := range p.ofType(hub.EventMemberDone) {
			dones = append(dones, dataOf(t, e))
		}
		return h.model, dones
	}
	pod := func(id string) *scriptStep {
		return toolStep("tool_use", apiCall{id: id, name: "inspect_pod", input: map[string]any{}})
	}

	t.Run("rounds", func(t *testing.T) {
		m, _ := run(t, hub.RoleBudget{MaxToolRounds: 1, MaxTokens: 40_000},
			pod("tu_a"), textStep(reportJSON("the pod is healthy"), "end_turn"))
		probes := m.requests("member:Probe")
		if len(probes) != 2 {
			t.Fatalf("%d Probe requests, want 2", len(probes))
		}
		if probes[0].ToolChoiceNone || !probes[1].ToolChoiceNone {
			t.Errorf("forced = %v, %v; want false, true", probes[0].ToolChoiceNone, probes[1].ToolChoiceNone)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		big := pod("tu_a")
		big.usage = model.Usage{Input: 10_001} // MaxTokens may not go below minRoleTokens (10_000)
		m, _ := run(t, hub.RoleBudget{MaxToolRounds: 4, MaxTokens: 10_000},
			big, textStep(reportJSON("the pod is healthy"), "end_turn"))
		probes := m.requests("member:Probe")
		if len(probes) != 2 || !probes[1].ToolChoiceNone {
			t.Fatalf("Probe's answer after its own token limit is not forced: %d requests", len(probes))
		}
	})
	t.Run("stopped name", func(t *testing.T) {
		_, dones := run(t, hub.RoleBudget{MaxToolRounds: 4, MaxTokens: 40_000},
			&scriptStep{err: errors.New("boom")})
		if len(dones) != 1 {
			t.Fatalf("%d member_done events", len(dones))
		}
		sum := dones[0]["report"].(map[string]any)["summary"].(string)
		if !strings.HasPrefix(sum, "Probe stopped: ") {
			t.Errorf("the stopped report's summary is %q", sum)
		}
	})
}

// A coder-runtime member the council renamed is still written "the " + its own name in the
// level-none sentence (spec 7.3); the Standard council's is "the Coder".
func TestCoderNameInTheNoneSentence(t *testing.T) {
	c := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		cd := s.Members[1]
		cd.Slug, cd.Name = "coder-b", "Coder B"
		cd.Runtime, cd.ID = runtimeCoder, coderTemplateID
		cd.Groups, cd.Tools, cd.Skills, cd.Budget = nil, nil, nil, nil
		cd.Results = false
		s.Members[4] = cd
		// The others work on platform data, so none of them may be convened at none.
		for i := 0; i < 4; i++ {
			s.Members[i].Level, s.Members[i].Tools = "none", []string{"whoami"}
			s.Members[i].Groups, s.Members[i].Results = []string{"core_read"}, false
		}
	}), "none")
	if got := captainRouteSystem(factsOf("none", nil, "Anders"), "council", c, testBook()); !strings.Contains(got,
		" At access level none only the Coder B can be convened.") {
		t.Errorf("a renamed coder's sentence:\n%s", got)
	}
	if got := captainRouteSystem(factsOf("none", nil, "Anders"), "council", stdAskCouncil(t, "none"), testBook()); !strings.Contains(got,
		" At access level none only the Coder can be convened.") {
		t.Errorf("the Standard council's sentence:\n%s", got)
	}
}

// Two members that run code: a plan may not convene both.
func TestTwoCoderTasks(t *testing.T) {
	c := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		second := s.Members[1]
		second.Slug, second.Name, second.Runtime, second.ID = "coder-b", "Coder B", runtimeCoder, coderTemplateID
		second.Groups, second.Tools, second.Skills, second.Budget = nil, nil, nil, nil
		s.Members[1] = second
		// The roster's first member runs code as well.
		s.Members[0] = second
		s.Members[0].Slug, s.Members[0].Name = "coder", "Coder"
	}), "creator")
	_, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"coder","task":"a","why":""},{"member":"coder-b","task":"b","why":""}]}`), c)
	if err == nil || err.Error() != "at most one coder task" {
		t.Errorf("two coder tasks: %v", err)
	}
}

// routeAnswerlessText is a council-mode turn whose lead answers in text: the daemon builds the plan
// itself, from the members the council may convene.
func routeAnswerlessText(t *testing.T) *testHarness {
	t.Helper()
	s := customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		level := func(r *hub.RoleSnapshot, level string, tools []string) {
			r.Level, r.Tools = level, tools
		}
		// m1 is data at none: it may not be convened.
		s.Members[0].Slug, s.Members[0].Name = "m1", "M1"
		level(&s.Members[0], "none", []string{"whoami"})
		s.Members[0].Groups = []string{"core_read"}
		// The council runs at read, and so does its lead: a lead above the council is refused.
		s.Level = "read"
		level(s.Lead, "read", catalogNames("read"))
		// m2, m3 and m4 run at read and may be convened.
		for i, slug := range []string{"m2", "m3", "m4"} {
			s.Members[i+1].Slug, s.Members[i+1].Name = slug, strings.ToUpper(slug)
			level(&s.Members[i+1], "read", []string{"whoami"})
		}
		// And the Coder, which only a prompt that asks for code may convene.
		s.Members[4] = s.Members[1]
		s.Members[4].Slug, s.Members[4].Name = "coder", "Coder"
		s.Members[4].Runtime, s.Members[4].ID = runtimeCoder, coderTemplateID
		s.Members[4].Groups, s.Members[4].Tools, s.Members[4].Skills, s.Members[4].Budget = nil, nil, nil, nil
		s.Members[4].Results = false
		s.Reviewer = stdSnapshot(t, "read").Reviewer
	})
	work := newWork(kindAsk, "", "which robots are offline?")
	work.Options.Mode = modeCouncil
	work.Council = s
	h := goldenHarness(t, work)
	h.whoamiAt("read")
	// The lead answers in text; the daemon then runs the plan it built.
	h.model.push("lead-route:Chief", textStep("the council should look", "end_turn"))
	return h
}

// The plan a council turn builds when its lead answers in text: the convenable members in roster
// order, the Coder last and only when the person asked for code or a change.
func TestRouteAnswerlessPlan(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   [][]string
	}{
		{"which robots are offline?", [][]string{{"m2", "m3", "m4"}}},
		{"write a script that lists the offline robots", [][]string{{"m2", "m3", "coder"}}},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			h := routeAnswerlessText(t)
			h.hub.queue[0].Prompt = tc.prompt
			// The members' own answers, and the reviewer's.
			h.model.push("member:M2", textStep(reportJSON("two robots are offline"), "end_turn"))
			h.model.push("member:M3", textStep(reportJSON("two robots are offline"), "end_turn"))
			if slices.Contains(tc.want[0], "m4") {
				h.model.push("member:M4", textStep(reportJSON("two robots are offline"), "end_turn"))
			}
			// The Standard reviewer is named Critic, so its prompt reads as roleCritic.
			h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
			h.model.push("lead-answer:Chief", textStep("two robots are offline", "end_turn"))
			h.start(t)
			p := h.waitFinish(t)
			h.stop(t)
			if left := h.model.leftover(); len(left) > 0 {
				t.Fatalf("unused scripted answers: %v", left)
			}

			plans := p.ofType(hub.EventPlan)
			if len(plans) != 1 {
				t.Fatalf("%d plan events", len(plans))
			}
			tasks, _ := dataOf(t, plans[0])["tasks"].([]any)
			var got [][]string
			for _, task := range tasks {
				m := task.(map[string]any)
				if m["task"] != tc.prompt {
					t.Errorf("a task's prompt is %q, want %q", m["task"], tc.prompt)
				}
				if m["why"] != "The person asked for the council." {
					t.Errorf("a task's why is %q", m["why"])
				}
				got = append(got, []string{m["member"].(string)})
			}
			var flat []string
			for _, g := range got {
				flat = append(flat, g[0])
			}
			if !slices.Equal(flat, tc.want[0]) {
				t.Errorf("the plan is %v, want %v", flat, tc.want[0])
			}
		})
	}
}

// A plan task for a slug the council has not is reported, never dropped in silence: emitPlan names
// it, and the work phase gives it a report of its own and no member events.
func TestUnknownPlanSlug(t *testing.T) {
	c := stdAskCouncil(t, "read")
	tr := &turn{work: newWork(kindAsk, "", "x"), council: c, poster: newFakePoster(newWork(kindAsk, "", "x"))}
	tr.emitPlan(modeCouncil, []conveneTask{{Member: "ghost", Task: "x"}})
	plans := tr.poster.(*fakePoster).ofType(hub.EventPlan)
	if len(plans) != 1 {
		t.Fatalf("%d plan events", len(plans))
	}
	first := dataOf(t, plans[0])["tasks"].([]any)[0].(map[string]any)
	if first["member"] != "ghost" || first["name"] != "ghost" {
		t.Errorf("the plan task is %v", first)
	}

	work := newWork(kindAsk, "", "x")
	work.Council = stdSnapshot(t, "read")
	wp := &turn{
		work:    work,
		d:       &Daemon{cfg: Config{Tools: newFakeTools(), Logf: func(string, ...any) {}}},
		council: c,
		poster:  newFakePoster(work),
	}
	results, out := wp.workPhase(context.Background(), "read", json.RawMessage(harnessFacts), []conveneTask{{Member: "ghost", Task: "x"}}, false)
	if out != nil {
		t.Fatalf("workPhase: %v", *out)
	}
	if len(results) != 1 || results[0].report == nil || results[0].report.Summary != "ghost is not in this council." {
		t.Fatalf("the results are %+v", results)
	}
	if !results[0].report.partial {
		t.Error("the report of a slug the council has not is not marked partial")
	}
	if need, why := needCritique(false, false, false, results); !need || why != "incomplete" {
		t.Errorf("needCritique = %v, %q, want true, \"incomplete\"", need, why)
	}
	p := wp.poster.(*fakePoster)
	if n := len(p.ofType(hub.EventMemberStart)) + len(p.ofType(hub.EventMemberDone)); n != 0 {
		t.Errorf("%d member events for a slug the council has not", n)
	}
}

// A council with no Coder still routes: the lead may read a tool and then convene a member, and the
// plan runs.
func TestCouncilWithoutACoder(t *testing.T) {
	work := newWork(kindAsk, "", "which robots are degraded?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		s.Members = s.Members[:3] // probe, reader and planner: no Coder.
	})
	h := goldenHarness(t, work)
	h.whoamiAt("creator")
	h.model.push("lead-route:Chief",
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", conveneCall("tu_2", taskOf("reader", "the robots", "the facts"))))
	h.model.push("member:Reader", textStep(reportJSON("two robots are degraded"), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("two robots are degraded", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if res := p.result(); res.State != stateDone {
		t.Errorf("the turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	if got := len(p.ofType(hub.EventPlan)); got != 1 {
		t.Errorf("%d plan events", got)
	}
	route := h.model.requests("lead-route:Chief")
	if len(route) == 0 {
		t.Fatal("no route request")
	}
	for i, req := range route {
		if strings.Contains(req.System, onlyCoderTask) {
			t.Errorf("route request %d limits coder tasks, and this council has no Coder", i)
		}
	}
}

// The Coder's effort follows the turn's, as its role's does: an ask turn at low effort convenes the
// Coder at low.
func TestCoderEffortOverride(t *testing.T) {
	work := newWork(kindAsk, "", "write a script that samples the temperature")
	work.Options.Effort = "low"
	h := goldenHarness(t, work)
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugCoder, "a sampler", "the person asked"))))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the script is on the bot", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	tasks := h.coder.started()
	if len(tasks) != 1 || tasks[0].Effort != "low" {
		t.Fatalf("the coder tasks are %+v", tasks)
	}
	starts := p.ofType(hub.EventMemberStart)
	if len(starts) != 1 || dataOf(t, starts[0])["effort"] != "low" {
		t.Errorf("the member_start events are %v", starts)
	}
}

// The legacy path asks the daemon's own catalog at the run level, so a tool the fake catalog has and
// the fixture's test catalog had not is offered and called.
func TestLegacyCatalog(t *testing.T) {
	h := goldenHarness(t, newWork(kindAsk, "", "which robots are there?"))
	h.tools.catalog = map[string][]string{
		"none":    {"whoami"},
		"read":    {"whoami", "list_robots", "get_robot"},
		"creator": {"whoami", "list_robots", "get_robot", "inspect_pod"},
	}
	h.model.push(roleRoute, textStep("an answer", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)
	route := h.model.requests(roleRoute)[0]
	if got, want := toolNames(route), []string{"whoami", "list_robots", "get_robot", toolReadSkill, toolReadResult, toolConvene}; !slices.Equal(got, want) {
		t.Errorf("the legacy route tools = %v, want %v", got, want)
	}

	// A legacy member turn for a member the embedded council has not fails bad_turn.
	bad := goldenHarness(t, newWork(kindMember, "analyst", "which robots are degraded?"))
	bad.start(t)
	p := bad.waitFinish(t)
	bad.stop(t)
	if res := p.result(); res.State != stateFailed || res.ErrCode != codeBadTurn || res.ErrMsg != msgBadTurn {
		t.Errorf("the member turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	if n := bad.model.count(); n != 0 {
		t.Errorf("%d model calls, want none", n)
	}
}

// Intake: a council the daemon cannot read, or one that breaks a rule, fails the turn before the
// bot is even asked who it is.
func TestCouncilIntakeFailsBeforeWhoami(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		work := newWork(kindAsk, "", "why is r2 degraded?")
		work.CouncilErr = fmt.Errorf("the council is not readable: %w", errors.New("x"))
		h := goldenHarness(t, work)
		calls := countWhoami(h)
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		res := p.result()
		want := msgBadCouncil + work.CouncilErr.Error() + "."
		if res.State != stateFailed || res.ErrCode != codeBadTurn || res.ErrMsg != want {
			t.Errorf("the turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
		}
		if n := h.model.count(); n != 0 {
			t.Errorf("%d model calls, want none", n)
		}
		if n := calls(); n != 0 {
			t.Errorf("whoami was called %d times: the council is checked before the platform is asked", n)
		}
	})
	t.Run("unreadable, long reason is cut", func(t *testing.T) {
		work := newWork(kindAsk, "", "why is r2 degraded?")
		// 300 bytes is 150 é: cutUTF8 counts bytes and never splits a rune.
		work.CouncilErr = errors.New(strings.Repeat("é", 1000))
		h := goldenHarness(t, work)
		calls := countWhoami(h)
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		res := p.result()
		want := msgBadCouncil + strings.Repeat("é", 150) + "…" + "."
		if res.State != stateFailed || res.ErrCode != codeBadTurn || res.ErrMsg != want {
			t.Errorf("the turn finished %q %q with a %d-byte message, want %q", res.State, res.ErrCode, len(res.ErrMsg), want)
		}
		if !strings.HasSuffix(res.ErrMsg, "….") {
			t.Errorf("the message does not end in the cut mark and a full stop")
		}
		if n := h.model.count(); n != 0 {
			t.Errorf("%d model calls, want none", n)
		}
		if n := calls(); n != 0 {
			t.Errorf("whoami was called %d times: the council is checked before the platform is asked", n)
		}
	})
	t.Run("unreadable, long ASCII reason is cut at 300 bytes", func(t *testing.T) {
		work := newWork(kindAsk, "", "why is r2 degraded?")
		work.CouncilErr = errors.New(strings.Repeat("a", 1000))
		h := goldenHarness(t, work)
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		res := p.result()
		want := msgBadCouncil + strings.Repeat("a", 300) + "…" + "."
		if res.State != stateFailed || res.ErrCode != codeBadTurn || res.ErrMsg != want {
			t.Errorf("the turn finished %q %q with a %d-byte message, want %q", res.State, res.ErrCode, len(res.ErrMsg), want)
		}
	})
	t.Run("six members", func(t *testing.T) {
		s := stdSnapshot(t, "read")
		s.Members = append(s.Members, extraMembers(4)...)
		names, skills := fakeCatalog()
		want := validateSnapshot(s, newWork(kindAsk, "", "x"), names, skills)
		if want == nil {
			t.Fatal("the six-member snapshot passed validateSnapshot")
		}
		work := newWork(kindAsk, "", "why is r2 degraded?")
		work.Council = s
		h := goldenHarness(t, work)
		calls := countWhoami(h)
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		res := p.result()
		if wantMsg := "The council for this turn is not valid: " + want.Error() + "."; res.State != stateFailed ||
			res.ErrCode != codeBadTurn || res.ErrMsg != wantMsg {
			t.Errorf("the turn finished %q %q %q, want %q", res.State, res.ErrCode, res.ErrMsg, wantMsg)
		}
		if n := h.model.count(); n != 0 {
			t.Errorf("%d model calls, want none", n)
		}
		if n := calls(); n != 0 {
			t.Errorf("whoami was called %d times: the council is checked before the platform is asked", n)
		}
	})
}

// countWhoami counts the whoami calls the harness makes from now on.
func countWhoami(h *testHarness) func() int {
	inner := h.whoami
	var calls int
	h.whoami = func(ctx context.Context) (string, json.RawMessage, error) {
		calls++
		return inner(ctx)
	}
	return func() int { return calls }
}

// The lead's digest and read_skill enum are its own Skills, not every guide the book carries.
func TestLeadOwnSkills(t *testing.T) {
	c := customCouncilWith(t, customSnapshot(t, func(s *hub.Snapshot) {
		customRoster(s)
		s.Lead.Skills = []string{"teknoir-debug"}
	}), "creator")
	f := factsOf("creator", nil, "Anders")
	for what, sys := range map[string]string{
		"route":     captainRouteSystem(f, "council", c, testBook()),
		"synthesis": captainAnswerSystem(f, c, testBook()),
	} {
		if !strings.Contains(sys, "\n\n"+skillsLine+"teknoir-debug: ") || strings.Contains(sys, "teknoir-robots: ") {
			t.Errorf("the %s digest is not the lead's own guides:\n%s", what, sys)
		}
	}
	if got := readSkillEnum(t, routeLocalTools(c, c.lead, modeCouncil)); !slices.Equal(got, []string{"teknoir-debug"}) {
		t.Errorf("the lead's read_skill enum = %v", got)
	}
}

// A member turn to a role that works on reports only runs while the bot is Off: no_access is for a
// data role at none, never for every role at none.
func TestReportsRoleMemberTurnAtNone(t *testing.T) {
	s := customSnapshot(t, customRoster)
	work := newWork(kindMember, "planner", "plan from the reports")
	work.Council = s
	work.Council.Lead, work.Council.Reviewer, work.Council.Roster, work.Council.Budget = nil, nil, nil, nil
	work.Council.Members = []hub.RoleSnapshot{s.Members[2]} // Planner: groups [results], no data group
	h := goldenHarness(t, work)
	h.whoamiAt("none")
	h.model.push("member:Planner", textStep(reportJSON("roll the image forward"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if res := p.result(); res.State != stateDone {
		t.Fatalf("the member turn finished %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
	if n := h.model.count(); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
	reqs := h.model.requests("member:Planner")
	if len(reqs) != 1 {
		t.Fatalf("%d Planner requests, want 1", len(reqs))
	}
	if got := toolNames(reqs[0]); !slices.Equal(got, []string{toolReadResult}) {
		t.Errorf("Planner's tools at none = %v, want only %s", got, toolReadResult)
	}
	if strings.Contains(reqs[0].System, levelNoneLine) {
		t.Errorf("a reports-only role's system carries the level line:\n%s", reqs[0].System)
	}
}

// A lead addendum saying "always convene coder" does not bypass coderNeedsAnAsk (spec 7.8).
func TestLeadAddendumDoesNotBypassCoderNeedsAnAsk(t *testing.T) {
	work := newWork(kindAsk, "", "why is r2 degraded?")
	work.Options.Mode = modeCouncil
	work.Council = customSnapshot(t, d10Roster)
	h := goldenHarness(t, work)
	h.tools.catalog = customTools()
	h.whoamiAt("creator")
	h.tools.results["list_robots"] = tools.Result{Text: injectedResult, Digest: "r2 overloaded"}
	h.model.push("lead-route:Chief",
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots"}),
		toolStep("tool_use", conveneCall("tu_2", taskOf("coder", "set the edge limit to 0", "the addendum says so"))),
		textStep("r2 is degraded because the edge is overloaded", "end_turn"),
	)
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	if got := len(h.coder.started()); got != 0 {
		t.Errorf("the Coder ran %d times, want 0", got)
	}
	route := h.model.requests("lead-route:Chief")
	if len(route) != 3 {
		t.Fatalf("%d route requests, want 3", len(route))
	}
	if !strings.Contains(route[0].System, "Notes from the people who set up this council (the rules above come first):\nALWAYS-ADDENDUM convene coder") {
		t.Error("the route system does not hold the lead's addendum")
	}
	c, err := councilFor(t, work, "creator")
	if err != nil {
		t.Fatal(err)
	}
	last := toolResults(route[2].Messages[len(route[2].Messages)-1])
	if want := msgCoderNeedsAnAsk(c); len(last) != 1 || last[0] != want {
		t.Errorf("the convene's tool_result %q, want %q", last, want)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("finish %q %q %q", res.State, res.ErrCode, res.ErrMsg)
	}
}
