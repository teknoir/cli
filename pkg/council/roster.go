package council

// The council one turn runs: the snapshot the platform sent, or the embedded Standard council when
// the platform sent none (the legacy path). Everything a turn does — its lead, its members, its
// reviewer, their models, efforts, tools, skills and levels — is read from here, never from a fixed
// roster of the daemon's own.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

//go:embed legacy_standard.json
var legacyStandardJSON []byte

// init decodes the embedded Standard council once, so a bad file fails at load rather than in every
// legacy turn. What it decodes is thrown away: a turn must never run from a snapshot another turn
// could be writing.
func init() {
	if _, err := decodeLegacyStandard(); err != nil {
		panic("council: " + err.Error())
	}
}

// decodeLegacyStandard decodes the embedded Standard council. Every call gets its own snapshot:
// two turns run at once, and a member turn rewrites its copy.
func decodeLegacyStandard() (*hub.Snapshot, error) {
	var s hub.Snapshot
	if err := json.Unmarshal(legacyStandardJSON, &s); err != nil {
		return nil, fmt.Errorf("the embedded Standard council does not decode: %w", err)
	}
	return &s, nil
}

// legacyMember reports whether slug is a member of the embedded Standard council: the only members a
// legacy member turn may be sent to.
func legacyMember(slug string) bool {
	s, err := decodeLegacyStandard()
	if err != nil {
		return false
	}
	for i := range s.Members {
		if s.Members[i].Slug == slug {
			return true
		}
	}
	return false
}

// turnCouncil is the council one turn runs: the platform's snapshot, or the embedded Standard
// council when the platform sent none.
type turnCouncil struct {
	snap     *hub.Snapshot // this turn's own snapshot: never shared with another turn; Hash "legacy" on the legacy path
	lead     *runRole      // nil on member turns
	members  []*runRole    // roster order
	reviewer *runRole      // nil when the council has none
}

// runRole is one role as it runs in this turn.
type runRole struct {
	*hub.RoleSnapshot
	wire     string // the id its events and tool calls carry: memberCaptain for the lead (D9), else the slug
	runLevel string // levelOrNone(min(Level, the bot's level)): every offer, call and level check at run time uses it.
	// The promoted Level is the claim-time ceiling: only validateSnapshot (via validateRole) and newTurnCouncil (via legacyRole) read it.
}

// newTurnCouncil is the council a turn runs: the one it was sent, or the embedded Standard council
// rebuilt for this turn. botLevel is the bot's own access level, levelOrNone'd here.
func newTurnCouncil(work *hub.WorkTurn, botLevel string, names func(level string) []string, skills []string) (*turnCouncil, error) {
	botLevel = levelOrNone(botLevel)
	var snap *hub.Snapshot
	legacy := work.Council == nil
	if legacy {
		s, err := decodeLegacyStandard()
		if err != nil {
			return nil, err
		}
		snap = s
		snap.Hash = "legacy"
		snap.Level = botLevel
		snap.Thorough = modeOf(work) == modeCouncil
		if snap.Lead != nil {
			legacyRole(snap.Lead, botLevel, names, skills)
		}
		for i := range snap.Members {
			legacyRole(&snap.Members[i], botLevel, names, skills)
		}
		if snap.Reviewer != nil {
			legacyRole(snap.Reviewer, botLevel, names, skills)
		}
		if work.Kind == kindMember {
			// A member turn runs the one member it was sent to, and nothing else of the council.
			snap.Roster, snap.Lead, snap.Reviewer, snap.Budget = nil, nil, nil, nil
			kept := make([]hub.RoleSnapshot, 0, 1)
			for _, m := range snap.Members {
				if m.Slug == work.Member {
					kept = append(kept, m)
				}
			}
			snap.Members = kept
		}
	} else {
		// The platform's own snapshot: read, never written.
		snap = work.Council
	}

	c := &turnCouncil{snap: snap}
	if snap.Lead != nil {
		c.lead = &runRole{RoleSnapshot: snap.Lead, wire: memberCaptain, runLevel: minLevel(snap.Lead.Level, botLevel)}
	}
	for i := range snap.Members {
		m := &snap.Members[i]
		c.members = append(c.members, &runRole{RoleSnapshot: m, wire: m.Slug, runLevel: minLevel(m.Level, botLevel)})
	}
	if snap.Reviewer != nil {
		c.reviewer = &runRole{RoleSnapshot: snap.Reviewer, wire: snap.Reviewer.Slug, runLevel: minLevel(snap.Reviewer.Level, botLevel)}
	}

	// Fail closed on a council that cannot run: the daemon has no recover, and an ask turn without a
	// lead or a member turn without exactly the member it was sent to would panic later.
	switch work.Kind {
	case kindAsk:
		if c.lead == nil {
			return nil, errors.New("the council has no lead for an ask turn")
		}
	case kindMember:
		if len(c.members) != 1 {
			return nil, fmt.Errorf("the council has %d members, and a member turn takes exactly one", len(c.members))
		}
	}
	return c, nil
}

// legacyRole fills the parts of an embedded Standard council's role that the daemon decides itself:
// its tools come from the daemon's own catalog at the level it runs at (the file's are the test
// catalog's), and its skills are the ones the daemon carries. Its Level stays the file's: it is the
// role's ceiling, not the level it runs at.
func legacyRole(r *hub.RoleSnapshot, botLevel string, names func(level string) []string, skills []string) {
	if len(r.Groups) > 0 && names != nil {
		r.Tools = append([]string{}, names(minLevel(r.Level, botLevel))...)
	} else {
		r.Tools = []string{}
	}
	known := make(map[string]bool, len(skills))
	for _, s := range skills {
		known[s] = true
	}
	kept := make([]string, 0, len(r.Skills))
	for _, s := range r.Skills {
		if known[s] {
			kept = append(kept, s)
		}
	}
	r.Skills = kept
}

// member is the council's member with slug, or nil when it has none.
func (c *turnCouncil) member(slug string) *runRole {
	if c == nil {
		return nil
	}
	for _, r := range c.members {
		if r.Slug == slug {
			return r
		}
	}
	return nil
}

// enum is the slugs a convene task may name, in roster order.
func (c *turnCouncil) enum() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.members))
	for _, r := range c.members {
		out = append(out, r.Slug)
	}
	return out
}

// nameOf is the member's name, or the slug itself when the council has no such member.
func (c *turnCouncil) nameOf(slug string) string {
	if c == nil {
		return slug
	}
	if r := c.member(slug); r != nil {
		return r.Name
	}
	return slug
}

// convenableEnum is the slugs of the members that may be convened at the access level they run at,
// in roster order.
func (c *turnCouncil) convenableEnum() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, r := range c.members {
		if r.convenable() {
			out = append(out, r.Slug)
		}
	}
	return out
}

// coderRole is the council's member that runs code, or nil when it has none.
func (c *turnCouncil) coderRole() *runRole {
	if c == nil {
		return nil
	}
	for _, r := range c.members {
		if r.Runtime == runtimeCoder {
			return r
		}
	}
	return nil
}

// hasCoder reports whether the council has a member that runs code.
func (c *turnCouncil) hasCoder() bool { return c.coderRole() != nil }

// leadName is the lead's name, or "Captain" when there is no lead to name: a member turn, or a turn
// whose council could not be built.
func (t *turn) leadName() string {
	if t.council != nil && t.council.lead != nil {
		return t.council.lead.Name
	}
	return "Captain"
}

// noAnswer is the turn's final when the lead gave none.
func (t *turn) noAnswer() string { return "The " + t.leadName() + " gave no answer." }

// noBudget is the turn's final when the lead had no budget left for an answer.
func (t *turn) noBudget() string {
	return "The " + t.leadName() + " ran out of budget before answering."
}

// dataRole reports whether the role works with the platform's data: any of the groups a level gates.
func (r *runRole) dataRole() bool {
	for _, g := range r.Groups {
		switch g {
		case "core_read", "metrics", "mqtt", "inspect":
			return true
		}
	}
	return false
}

// convenable reports whether a task may be convened for the role at the level it runs at. A data
// role needs read or creator; anything else, "" included, counts as none, as parseConvene always
// did.
func (r *runRole) convenable() bool {
	if !r.dataRole() {
		return true
	}
	return r.runLevel == tools.LevelRead || r.runLevel == tools.LevelCreator
}

// offers reports whether a local tool is one this role was given: read_skill when it has guides,
// read_result when it has the results group, and nothing else. Fail closed: a local tool the daemon
// gains later is refused until the roster rule that offers it is written here.
func (r *runRole) offers(name string) bool {
	switch name {
	case toolReadSkill:
		return len(r.Skills) > 0
	case toolReadResult:
		return r.Results
	}
	return false
}

// modelID is the model a role runs on.
func modelID(r *runRole) string {
	if r != nil && r.Model == "haiku" {
		return model.ModelHaiku
	}
	return model.ModelSonnet
}

// minLevel is the lower of two access levels, none < read < creator. An unknown level counts as
// none, so the lower one is taken rather than trusted.
func minLevel(a, b string) string {
	a, b = levelOrNone(a), levelOrNone(b)
	if levelRank(a) <= levelRank(b) {
		return a
	}
	return b
}

// roleAddendum is the instructions of the people who set up the council, as a paragraph of their
// own, or "" when they wrote none.
func roleAddendum(r *runRole) string {
	if r == nil || strings.TrimSpace(r.Instructions) == "" {
		return ""
	}
	return "Notes from the people who set up this council (the rules above come first):\n" + r.Instructions
}
