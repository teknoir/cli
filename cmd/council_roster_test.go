package cmd

import (
	"strconv"
	"strings"
	"testing"
)

// ---------- the test fixtures ----------

// rosterRef is one stored reference of a test council.
type rosterRef struct {
	slug, scope string // scope "" is sent as null
	pin         int    // 0: no pin
	gone        bool
}

// refID is the reference's roleId, as roleViewJSON makes a member's id. A raw id stays itself.
func (f rosterRef) refID() string {
	switch {
	case strings.HasPrefix(f.slug, "rol_"):
		return f.slug
	case f.scope == "template":
		return "tpl:" + f.slug
	}
	return "rol_" + f.slug
}

func (f rosterRef) json() string {
	scope, pin := "null", "null"
	if f.scope != "" {
		scope = q(f.scope)
	}
	if f.pin > 0 {
		pin = strconv.Itoa(f.pin)
	}
	return `{"roleId":` + q(f.refID()) + `,"role":` + q(f.slug) + `,"scope":` + scope + `,"version":` + pin +
		`,"gone":` + strconv.FormatBool(f.gone) + `}`
}

// rosterFix is one council as the hub's GET bots/<bot>/council lists it (a RosterView).
type rosterFix struct {
	name, display, scope string // scope: template or bot
	version              int
	isDefault            bool
	status               string // "" is sent as ok
	reasons              []string
	lead                 rosterRef
	members              []rosterRef
	reviewer             *rosterRef
	description          string   // "" is sent as null
	budget               int      // 0 is sent as null
	roles                []string // RoleView entries (roleViewJSON)
	noRefs               bool     // an older platform: no "refs"
}

// id is the council's id: tpl:<name> for a premade one, else ros_ and the name padded with A to 22.
func (r rosterFix) id() string {
	if r.scope == "template" {
		return "tpl:" + r.name
	}
	return "ros_" + (r.name + strings.Repeat("A", 22))[:22]
}

func (r rosterFix) json() string {
	status := r.status
	if status == "" {
		status = "ok"
	}
	slugs := make([]string, 0, len(r.members))
	refs := make([]string, 0, len(r.members))
	for _, m := range r.members {
		slugs = append(slugs, m.slug)
		refs = append(refs, m.json())
	}
	reviewer, reviewerRef := "null", "null"
	if r.reviewer != nil {
		reviewer, reviewerRef = q(r.reviewer.slug), r.reviewer.json()
	}
	description, budget := "null", "null"
	if r.description != "" {
		description = q(r.description)
	}
	if r.budget > 0 {
		budget = `{"maxTokens":` + strconv.Itoa(r.budget) + `}`
	}
	s := `{"id":` + q(r.id()) + `,"name":` + q(r.name) + `,"displayName":` + q(r.display) + `,"scope":` + q(r.scope) +
		`,"version":` + strconv.Itoa(r.version) + `,"lead":` + q(r.lead.slug) + `,"members":` + strs(slugs) +
		`,"reviewer":` + reviewer + `,"isDefault":` + strconv.FormatBool(r.isDefault) + `,"legacyCompatible":false` +
		`,"status":` + q(status) + `,"reasons":` + strs(r.reasons) + `,"description":` + description +
		`,"budget":` + budget + `,"roles":[` + strings.Join(r.roles, ",") + `]`
	if !r.noRefs {
		s += `,"refs":{"lead":` + r.lead.json() + `,"members":[` + strings.Join(refs, ",") + `],"reviewer":` + reviewerRef + `}`
	}
	return s + `}`
}

// rosterStub is council.roster when the bot's default council is gone.
const rosterStub = `{"id":"ros_GONEAAAAAAAAAAAAAAAAAA","name":"ros_GONEAAAAAAAAAAAAAAAAAA","displayName":"ros_GONEAAAAAAAAAAAAAAAAAA","scope":"bot",` +
	`"version":0,"lead":"","members":[],"reviewer":null,"isDefault":true,"legacyCompatible":false,"status":"invalid",` +
	`"reasons":["This bot's default council is gone: make another council the default."],"description":null,` +
	`"budget":null,"roles":[],"refs":{"lead":null,"members":[],"reviewer":null}}`

// goneHint is the second line under a gone reference or an export's role_gone (brief section 1),
// written out so that a change to the production text fails the tests.
const goneHint = "fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>"

const canEditOK = `{"ok":true}`

// councilViewJSON is GET bots/<bot>/council's answer (and PUT default's): the councils given, canEdit
// as raw JSON, and council.roster the one whose isDefault is set (the stub when none is).
func councilViewJSON(canEdit string, rosters ...rosterFix) string {
	entries := make([]string, 0, len(rosters))
	def, defName := rosterStub, "ros_GONEAAAAAAAAAAAAAAAAAA"
	for _, r := range rosters {
		entries = append(entries, r.json())
		if r.isDefault {
			def, defName = r.json(), r.name
		}
	}
	return `{"council":{"proto":2,"defaultRoster":` + q(defName) + `,"canEdit":` + canEdit + `,"roster":` + def +
		`,"rosters":[` + strings.Join(entries, ",") + `],"available":[],"catalog":null,"caps":null}}`
}

// rosterAnswer is PUT bots/<bot>/council/rosters/<name>'s answer.
func rosterAnswer(r rosterFix, action string) string {
	return `{"roster":` + r.json() + `,"action":` + q(action) + `}`
}

func tplRole(slug, name, kind string) string {
	return roleViewJSON(slug, name, "template", kind, "ok", nil, nil)
}

// The fixtures. A test that changes one copies it first and builds new slices and a new reviewer:
// never append to a fixture's slice or write through its reviewer pointer.
var (
	fxStandard = rosterFix{name: "standard", display: "Standard", scope: "template", version: 1, isDefault: true,
		lead:     rosterRef{slug: "captain", scope: "template"},
		members:  []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template"}},
		reviewer: &rosterRef{slug: "critic", scope: "template"},
		roles: []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member"),
			tplRole("coder", "Coder", "member"), tplRole("critic", "Critic", "reviewer")}}
	fxReview = rosterFix{name: "review", display: "Review", scope: "template", version: 1, budget: 150000,
		lead:     rosterRef{slug: "captain", scope: "template"},
		members:  []rosterRef{{slug: "architect", scope: "template"}, {slug: "cost", scope: "template"}},
		reviewer: &rosterRef{slug: "skeptic", scope: "template"},
		roles: []string{tplRole("captain", "Captain", "lead"), tplRole("architect", "Architect", "member"),
			tplRole("cost", "Cost", "member"), tplRole("skeptic", "Skeptic", "reviewer")}}
	fxOpsA = rosterFix{name: "ops-a", display: "Ops A", scope: "bot", version: 3, description: "Day to day.", budget: 150000,
		lead: rosterRef{slug: "captain", scope: "template"},
		members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}},
		roles: []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member"),
			roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil),
			roleViewJSON("helper", "Helper", "bot", "member", "ok", nil, nil)}}
)

const exportOpsA = `apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-a
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Ops A
  description: Day to day.
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
    - role: triage
      scope: teamspace
      version: 4
    - role: helper
      scope: bot
  reviewer: null
  budget:
    maxTokens: 150000
`

const exportStandard = `apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: standard
spec:
  scope: template
  version: 1
  displayName: Standard
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
    - role: coder
      scope: template
  reviewer:
    role: critic
    scope: template
`

// The PUT bodies the edits of fxOpsA and fxStandard send.
const (
	opsASpecHead = `{"displayName":"Ops A","description":"Day to day.","lead":{"role":"captain","scope":"template"},"members":[{"role":"fleet","scope":"template"},{"role":"triage","scope":"teamspace","version":4},{"role":"helper","scope":"bot"}`
	opsASpecTail = `],"reviewer":null,"budget":{"maxTokens":150000}}`
)

// opsAGone is fxOpsA with its member helper archived: the council cannot answer.
func opsAGone() rosterFix {
	r := fxOpsA
	r.status = "invalid"
	r.reasons = []string{"helper: this member was archived or deleted; replace it in this council."}
	r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
		{slug: "helper", scope: "bot", gone: true}}
	r.roles = []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member"),
		roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil)}
	return r
}

// ---------- deriveRosterName ----------

// TestDeriveRosterName names a bot's new council from its name, cut to 24 and numbered while taken.
func TestDeriveRosterName(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz1234"
	takenRange := func(prefix string, from, to int, extra ...string) map[string]bool {
		m := map[string]bool{}
		for _, e := range extra {
			m[e] = true
		}
		for n := from; n <= to; n++ {
			m[prefix+"-"+strconv.Itoa(n)] = true
		}
		return m
	}
	for _, c := range []struct {
		bot   string
		taken map[string]bool
		want  string
	}{
		{"scout", nil, "scout-council"},
		{"scout", map[string]bool{"scout-council": true}, "scout-council-2"},
		{"scout", map[string]bool{"scout-council": true, "scout-council-2": true}, "scout-council-3"},
		{"7up", nil, "c-7up-council"},
		{"9lives", nil, "c-9lives-council"},
		{"0day", nil, "c-0day-council"},
		{long, nil, "abcdefghijklmnopqrstuvwx"},
		{long, map[string]bool{"abcdefghijklmnopqrstuvwx": true}, "abcdefghijklmnopqrstuv-2"},
		{"abcdefghijklmnopqrstuvw", nil, "abcdefghijklmnopqrstuvw"},
		{"abcdefghijklmnopqrstu", nil, "abcdefghijklmnopqrstu-co"},
		{"abcdefghijklmnopqrstu", map[string]bool{"abcdefghijklmnopqrstu-co": true}, "abcdefghijklmnopqrstu-2"},
		{"abcdefghijklmnopqrstu", takenRange("abcdefghijklmnopqrstu", 2, 9, "abcdefghijklmnopqrstu-co"), "abcdefghijklmnopqrstu-10"},
		{"abcdefghijklmnopqrst--xy", map[string]bool{"abcdefghijklmnopqrst--xy": true}, "abcdefghijklmnopqrst-2"},
		{long, takenRange("abcdefghijklmnopqrstuv", 2, 9, "abcdefghijklmnopqrstuvwx"), "abcdefghijklmnopqrstu-10"},
		{"a" + strings.Repeat("-", 23) + "b", nil, "a-2"},
	} {
		got := deriveRosterName(c.bot, c.taken)
		if got != c.want {
			t.Errorf("deriveRosterName(%q, %v) = %q, want %q", c.bot, c.taken, got, c.want)
		}
		if !councilSlugRE.MatchString(got) {
			t.Errorf("deriveRosterName(%q) = %q, not a council's name", c.bot, got)
		}
		if c.taken[got] {
			t.Errorf("deriveRosterName(%q) = %q, which is taken", c.bot, got)
		}
	}
}

// ---------- council roster list ----------

// TestCouncilRosterList lists a bot's councils and says on stderr which cannot answer.
func TestCouncilRosterList(t *testing.T) {
	noBotMode(t)
	opsA := fxOpsA
	opsA.status = "invalid"
	opsA.reasons = []string{"helper: this member was archived or deleted; replace it in this council."}
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxReview, opsA))

	out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "NAME      DISPLAY NAME  SCOPE     LEAD     MEMBERS              REVIEWER  DEFAULT  STATUS\n" +
		"standard  Standard      template  captain  fleet,coder          critic    yes      ok\n" +
		"review    Review        template  captain  architect,cost       skeptic   -        ok\n" +
		"ops-a     Ops A         bot       captain  fleet,triage,helper  -         -        unavailable\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)
	wantErr := "· ops-a: helper: this member was archived or deleted; replace it in this council.\n"
	if errs != wantErr {
		t.Errorf("stderr %q, want %q", errs, wantErr)
	}
	wantReqs(t, p, "GET /bots/scout/council")

	t.Run("no reasons", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		noReason := fxOpsA
		noReason.status = "invalid"
		noReason.reasons = nil
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxReview, noReason))
		_, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if errs != "· ops-a\n" {
			t.Errorf("stderr %q, want %q", errs, "· ops-a\n")
		}
	})

	t.Run("not checked", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		unchecked := fxOpsA
		unchecked.status = "unchecked"
		unchecked.reasons = nil
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxReview, unchecked))
		out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if !strings.HasSuffix(out, "\nops-a     Ops A         bot       captain  fleet,triage,helper  -         -        not checked\n") {
			t.Errorf("stdout ends %q", out)
		}
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})
}

// TestCouncilRosterListJSON prints the councils as the platform sent them, entries unchanged.
func TestCouncilRosterListJSON(t *testing.T) {
	want := "[" + fxStandard.json() + "," + fxReview.json() + "," + fxOpsA.json() + "]"
	// One run: a fresh API answering the bot's council, and the command line.
	run := func(t *testing.T, answer string, args ...string) (string, string) {
		t.Helper()
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, answer)
		out, errs, err := councilArgs(t, p.env(t), args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		return out, errs
	}
	answer := councilViewJSON(canEditOK, fxStandard, fxReview, fxOpsA)

	out, errs := run(t, answer, "roster", "list", "--bot", "scout", "--json")
	compactEq(t, "json", out, want)
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}

	out, _ = run(t, answer, "-o", "yaml", "roster", "list", "--bot", "scout", "--json")
	compactEq(t, "json", out, want)

	if out, _ = run(t, answer, "-o", "json", "roster", "list", "--bot", "scout"); !strings.HasPrefix(out, "NAME") {
		t.Errorf("stdout %q, want the table", out)
	}

	// list never calls outputFormat: -o neither reads nor refuses a word it does not know.
	if out, _ = run(t, answer, "-o", "xml", "roster", "list", "--bot", "scout"); !strings.HasPrefix(out, "NAME") {
		t.Errorf("stdout %q, want the table", out)
	}

	entry := strings.Replace(fxOpsA.json(), `{"id":`, `{"future":"<a&b>","id":`, 1)
	out, _ = run(t, strings.Replace(councilViewJSON(canEditOK, fxStandard, fxReview, fxOpsA), fxOpsA.json(), entry, 1), "roster", "list", "--bot", "scout", "--json")
	compactEq(t, "json", out, "["+fxStandard.json()+","+fxReview.json()+","+entry+"]")
	if !strings.Contains(out, `"future": "<a&b>"`) {
		t.Errorf("stdout %q loses the entry it does not know", out)
	}

	if out, _ = run(t, councilViewJSON(canEditOK), "roster", "list", "--bot", "scout", "--json"); out != "[]\n" {
		t.Errorf("stdout %q, want %q", out, "[]\n")
	}

	t.Run("default gone, no note", func(t *testing.T) {
		noBotMode(t)
		std := fxStandard
		std.isDefault = false
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, std, fxReview, opsAGone()))
		out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout", "--json")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		compactEq(t, "json", out, "["+std.json()+","+fxReview.json()+","+opsAGone().json()+"]")
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})
}

// TestCouncilRosterListHints points at the next step when the default is gone or the bot has no
// council of its own.
func TestCouncilRosterListHints(t *testing.T) {
	const ownLine = "· scout has no council of its own yet; tnctl council roster add <council> <member> --bot scout --as scout-council saves a copy with that member\n"
	const goneLine = "· scout's default council is gone; tnctl council roster use <council> --bot scout makes another one the default\n"
	const goneCreator = "· scout's default council is gone; the bot's creator or a teamspace admin makes another one the default\n"
	run := func(t *testing.T, answer string) (string, string) {
		t.Helper()
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, answer)
		out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		wantReqs(t, p, "GET /bots/scout/council")
		return out, errs
	}
	t.Run("own", func(t *testing.T) {
		_, errs := run(t, councilViewJSON(canEditOK, fxStandard, fxReview))
		if errs != ownLine {
			t.Errorf("stderr %q, want %q", errs, ownLine)
		}
	})
	t.Run("default gone", func(t *testing.T) {
		std := fxStandard
		std.isDefault = false
		out, errs := run(t, councilViewJSON(canEditOK, std, fxOpsA))
		if errs != goneLine {
			t.Errorf("stderr %q, want %q", errs, goneLine)
		}
		if strings.Contains(out, "ros_GONE") {
			t.Errorf("stdout names the stub council: %q", out)
		}
	})
	t.Run("both", func(t *testing.T) {
		std := fxStandard
		std.isDefault = false
		_, errs := run(t, councilViewJSON(canEditOK, std, fxReview))
		if errs != goneLine+ownLine {
			t.Errorf("stderr %q, want %q", errs, goneLine+ownLine)
		}
	})
	t.Run("none", func(t *testing.T) {
		out, errs := run(t, councilViewJSON(canEditOK))
		if out != "" {
			t.Errorf("stdout %q", out)
		}
		if errs != "· no councils for scout\n" {
			t.Errorf("stderr %q", errs)
		}
	})
	t.Run("both, may not edit", func(t *testing.T) {
		std := fxStandard
		std.isDefault = false
		_, errs := run(t, councilViewJSON(`{"ok":false,"reason":"Only the bot's creator or a teamspace admin may change its council."}`, std, fxReview))
		if errs != goneCreator {
			t.Errorf("stderr %q, want %q", errs, goneCreator)
		}
	})
	t.Run("reason and default gone", func(t *testing.T) {
		std := fxStandard
		std.isDefault = false
		opsA := fxOpsA
		opsA.status = "invalid"
		opsA.reasons = []string{"helper: this member was archived or deleted; replace it in this council."}
		_, errs := run(t, councilViewJSON(canEditOK, std, opsA))
		want := "· ops-a: helper: this member was archived or deleted; replace it in this council.\n" + goneLine
		if errs != want {
			t.Errorf("stderr %q, want %q", errs, want)
		}
	})
	t.Run("premade named like the bot", func(t *testing.T) {
		tplScout := rosterFix{name: "scout-council", display: "Scout council", scope: "template", version: 1,
			lead:    rosterRef{slug: "captain", scope: "template"},
			members: []rosterRef{{slug: "fleet", scope: "template"}}}
		_, errs := run(t, councilViewJSON(canEditOK, fxStandard, tplScout))
		want := "· scout has no council of its own yet; tnctl council roster add <council> <member> --bot scout --as scout-council-2 saves a copy with that member\n"
		if errs != want {
			t.Errorf("stderr %q, want %q", errs, want)
		}
	})
}

// TestCouncilRosterListSafe keeps control characters and tabs in hub text out of the table.
func TestCouncilRosterListSafe(t *testing.T) {
	noBotMode(t)
	ops := rosterFix{name: "ops-a", display: "Ops\tA\n\x1b[31mred", scope: "bot", version: 1,
		lead:    rosterRef{slug: "captain", scope: "template"},
		members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "x\ty", scope: "bot"}},
		status:  "invalid", reasons: []string{"bad\nreason\x1b"}}
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, ops))
	out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if n := strings.Count(out, "\n"); n != 3 {
		t.Errorf("stdout has %d lines, want 3: %q", n, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("stdout carries an escape sequence: %q", out)
	}
	if strings.Contains(out, "\t") {
		t.Errorf("stdout carries a tab: %q", out)
	}
	if !strings.Contains(out, "Ops A red") {
		t.Errorf("stdout lacks the display name without its colour: %q", out)
	}
	if !strings.Contains(out, "fleet,x\\ty") {
		t.Errorf("stdout lacks the member with a literal tab: %q", out)
	}
	noTrailingSpaces(t, out)
	if errs != "· ops-a: bad reason\n" {
		t.Errorf("stderr %q", errs)
	}

	t.Run("bare", func(t *testing.T) {
		noBotMode(t)
		bare := rosterFix{name: "bare", scope: "bot", version: 1, reviewer: &rosterRef{}}
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, bare))
		out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		want := "NAME      DISPLAY NAME  SCOPE     LEAD     MEMBERS      REVIEWER  DEFAULT  STATUS\n" +
			"standard  Standard      template  captain  fleet,coder  critic    yes      ok\n" +
			"bare      -             bot       -        -            -         -        ok\n"
		if out != want {
			t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
		}
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})

	t.Run("odd", func(t *testing.T) {
		noBotMode(t)
		odd := rosterFix{name: "odd\tname", display: "A display name that runs well past thirty runes",
			scope: "b\tot", version: 1, lead: rosterRef{slug: "captain", scope: "template"},
			members:  []rosterRef{{slug: "fleet", scope: "template"}},
			reviewer: &rosterRef{slug: "r\tv", scope: "bot"}}
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, odd))
		out, errs, err := councilArgs(t, p.env(t), "roster", "list", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if n := strings.Count(out, "\n"); n != 3 {
			t.Errorf("stdout has %d lines, want 3: %q", n, out)
		}
		if strings.Contains(out, "\t") {
			t.Errorf("stdout carries a tab: %q", out)
		}
		for _, want := range []string{"odd\\tname", "b\\tot", "r\\tv", "A display name that runs well…"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q: %q", want, out)
			}
		}
	})
}

// ---------- council roster get ----------

// opsGet is test 6's council: opsAGone() (member helper gone, status invalid) with the premade reviewer critic.
func opsGet() rosterFix {
	r := opsAGone()
	r.reviewer = &rosterRef{slug: "critic", scope: "template"}
	r.roles = []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member"),
		roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil), tplRole("critic", "Critic", "reviewer")}
	return r
}

// TestCouncilRosterGet shows one council: its status and budget, then its members.
func TestCouncilRosterGet(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, opsGet()))
	out, errs, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "council:     Ops A (ops-a)\n" +
		"scope:       bot\n" +
		"version:     3\n" +
		"default:     no\n" +
		"status:      unavailable: helper: this member was archived or deleted; replace it in this council.\n" +
		"budget:      150000 tokens per question\n" +
		"description: Day to day.\n" +
		"\n" +
		"PART      SLUG     NAME     SCOPE      VERSION     MODEL   EFFORT  STATUS\n" +
		"lead      captain  Captain  template   1           sonnet  medium  ok\n" +
		"member    fleet    Fleet    template   1           sonnet  medium  ok\n" +
		"member    triage   Triage   teamspace  4 (pinned)  sonnet  medium  ok\n" +
		"member    helper   -        bot        -           -       -       gone\n" +
		"reviewer  critic   Critic   template   1           sonnet  medium  ok\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	wantReqs(t, p, "GET /bots/scout/council")
}

// TestCouncilRosterGetPremade shows a premade council with its budget and its members.
func TestCouncilRosterGetPremade(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
	out, errs, err := councilArgs(t, p.env(t), "roster", "get", "standard", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "council:     Standard (standard)\n" +
		"scope:       template\n" +
		"version:     1\n" +
		"default:     yes\n" +
		"status:      ok\n" +
		"budget:      no council cap\n" +
		"description: -\n" +
		"\n" +
		"PART      SLUG     NAME     SCOPE     VERSION  MODEL   EFFORT  STATUS\n" +
		"lead      captain  Captain  template  1        sonnet  medium  ok\n" +
		"member    fleet    Fleet    template  1        sonnet  medium  ok\n" +
		"member    coder    Coder    template  1        sonnet  medium  ok\n" +
		"reviewer  critic   Critic   template  1        sonnet  medium  ok\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
}

// TestCouncilRosterGetWithoutRefs matches an older platform's references by slug and part.
func TestCouncilRosterGetWithoutRefs(t *testing.T) {
	noBotMode(t)
	old := opsGet()
	old.noRefs = true
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, old))
	out, errs, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "PART      SLUG     NAME     SCOPE      VERSION  MODEL   EFFORT  STATUS\n" +
		"lead      captain  Captain  template   1        sonnet  medium  ok\n" +
		"member    fleet    Fleet    template   1        sonnet  medium  ok\n" +
		"member    triage   Triage   teamspace  1        sonnet  medium  ok\n" +
		"member    helper   -        -          -        -       -       gone\n" +
		"reviewer  critic   Critic   template   1        sonnet  medium  ok\n"
	table := out[strings.Index(out, "\n\n")+2:]
	if table != want {
		t.Errorf("table:\n%q\nwant:\n%q", table, want)
	}
	noTrailingSpaces(t, out)
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}

	t.Run("twin", func(t *testing.T) {
		noBotMode(t)
		twin := rosterFix{name: "ops-a", display: "Twin", scope: "bot", version: 2,
			lead:    rosterRef{slug: "captain", scope: "template"},
			members: []rosterRef{{slug: "captain", scope: "bot"}},
			roles: []string{tplRole("captain", "Captain", "lead"),
				roleViewJSON("captain", "Captain B", "bot", "member", "ok", nil, nil)}}
		want := "PART    SLUG     NAME       SCOPE     VERSION  MODEL   EFFORT  STATUS\n" +
			"lead    captain  Captain    template  1        sonnet  medium  ok\n" +
			"member  captain  Captain B  bot       1        sonnet  medium  ok\n"
		for _, c := range []struct {
			noRefs bool
		}{{false}, {true}} {
			tw := twin
			tw.noRefs = c.noRefs
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, tw))
			out, errs, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			table := out[strings.Index(out, "\n\n")+2:]
			if table != want {
				t.Errorf("noRefs %v: table:\n%q\nwant:\n%q", c.noRefs, table, want)
			}
		}
	})

	t.Run("empty reviewer", func(t *testing.T) {
		noBotMode(t)
		r := fxOpsA
		r.noRefs = true
		r.reviewer = &rosterRef{}
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, r))
		out, _, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "reviewer") {
				t.Errorf("stdout has a reviewer row: %q", line)
			}
		}
	})
}

// TestCouncilRosterGetCells shows each cell of a council the way its value asks for.
func TestCouncilRosterGetCells(t *testing.T) {
	rows := []struct {
		name     string
		council  rosterFix
		contains string
	}{
		{"invalid, no reason", func() rosterFix { r := fxOpsA; r.status = "invalid"; r.reasons = nil; return r }(),
			"\nstatus:      unavailable\n"},
		{"unchecked", func() rosterFix {
			r := fxOpsA
			r.status = "unchecked"
			r.reasons = []string{"not checked yet"}
			return r
		}(), "\nstatus:      not checked\n"},
		{"description", func() rosterFix { r := fxOpsA; r.description = "Day to\nday.\x1b[31m"; return r }(),
			"\ndescription: Day to day.\n\n"},
		{"no scope", func() rosterFix { r := fxOpsA; r.scope = ""; return r }(), "\nscope:       -\n"},
		{"ref scope null", func() rosterFix {
			r := fxOpsA
			r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4}, {slug: "helper"}}
			return r
		}(), "\nmember  helper   Helper   bot        1           sonnet  medium  ok\n"},
		{"lead gone", func() rosterFix {
			r := fxOpsA
			r.lead = rosterRef{slug: "captain", scope: "template", gone: true}
			return r
		}(),
			"\nlead    captain  -       template   -           -       -       gone\n"},
		{"colour", func() rosterFix {
			r := fxOpsA
			roles := make([]string, 0, len(fxOpsA.roles))
			for i, role := range fxOpsA.roles {
				if i == 0 {
					role = strings.Replace(role, `"model":"sonnet"`, `"model":"son\u001b[31mnet"`, 1)
				}
				roles = append(roles, role)
			}
			r.roles = roles
			return r
		}(), "\nlead    captain  Captain  template   1           sonnet  medium  ok\n"},
		{"gone, pinned", func() rosterFix {
			r := opsGet()
			r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4}, {slug: "helper", scope: "bot", pin: 2, gone: true}}
			return r
		}(), "\nmember    helper   -        bot        2 (pinned)  -       -       gone\n"},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, c.council))
			out, errs, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if !strings.Contains(out, c.contains) {
				t.Errorf("stdout %q does not contain %q", out, c.contains)
			}
			if c.name == "colour" && strings.Contains(out, "\x1b") {
				t.Errorf("stdout carries an escape sequence: %q", out)
			}
			noTrailingSpaces(t, out)
		})
	}

	t.Run("gone member with a tab", func(t *testing.T) {
		noBotMode(t)
		r := opsGet()
		r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot", gone: true}, {slug: "x\ty", scope: "bot", gone: true}}
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, r))
		out, _, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "\t") {
			t.Errorf("stdout carries a tab: %q", out)
		}
		if !strings.Contains(out, "x\\ty") {
			t.Errorf("stdout lacks the member with a literal tab: %q", out)
		}
		noTrailingSpaces(t, out)
	})

	t.Run("budget of zero", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		answer := strings.ReplaceAll(councilViewJSON(canEditOK, fxStandard, fxOpsA), `"budget":{"maxTokens":150000}`, `"budget":{"maxTokens":0}`)
		p.on("GET", "/bots/scout/council", 200, answer)
		out, _, err := councilArgs(t, p.env(t), "roster", "get", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "\nbudget:      no council cap\n") {
			t.Errorf("stdout %q", out)
		}
		noTrailingSpaces(t, out)
	})
}

// TestCouncilRosterGetJSON prints the council as the platform sent it.
func TestCouncilRosterGetJSON(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
	out, errs, err := councilArgs(t, p.env(t), "-o", "json", "roster", "get", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	compactEq(t, "json", out, fxOpsA.json())
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	wantReqs(t, p, "GET /bots/scout/council")

	entry := strings.Replace(fxOpsA.json(), `{"id":`, `{"future":"<a&b>","id":`, 1)
	p2 := newChatAPI(t)
	p2.on("GET", "/bots/scout/council", 200, strings.Replace(councilViewJSON(canEditOK, fxStandard, fxOpsA), fxOpsA.json(), entry, 1))
	out, errs, err = councilArgs(t, p2.env(t), "-o", "json", "roster", "get", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	compactEq(t, "json", out, entry)
	if !strings.Contains(out, `"future": "<a&b>"`) {
		t.Errorf("stdout %q loses the entry it does not know", out)
	}
}

// TestCouncilRosterGetYAML prints the platform's export of the council, byte for byte.
func TestCouncilRosterGetYAML(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
	p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
	out, errs, err := councilArgs(t, p.env(t), "-o", "yaml", "roster", "get", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != exportOpsA {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, exportOpsA)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")

	t.Run("role gone", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		msg := "This council names a member that no longer exists; replace it before you export the council."
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 409, councilErrBody("role_gone", msg, ""))
		out, _, err := councilArgs(t, p.env(t), "-o", "yaml", "roster", "get", "ops-a", "--bot", "scout")
		if err == nil {
			t.Fatal("the export was refused but the command succeeded")
		}
		want := msg + "\n" + goneHint
		if err.Error() != want {
			t.Errorf("error %q, want %q", err.Error(), want)
		}
		if code := exitCodeOf(err); code != codeError {
			t.Errorf("exit %d, want %d", code, codeError)
		}
		if out != "" {
			t.Errorf("stdout %q", out)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})

	t.Run("not allowed", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		msg := "Only people who may chat with scout can export its councils."
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 403, councilErrBody("chat_not_allowed", msg, ""))
		_, _, err := councilArgs(t, p.env(t), "-o", "yaml", "roster", "get", "ops-a", "--bot", "scout")
		if err == nil {
			t.Fatal("the export was refused but the command succeeded")
		}
		if err.Error() != msg {
			t.Errorf("error %q, want %q", err.Error(), msg)
		}
		if code := exitCodeOf(err); code != codeAuth {
			t.Errorf("exit %d, want %d", code, codeAuth)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})
}

// TestCouncilRosterGetMissing names the councils the bot has when the name is not one of them.
func TestCouncilRosterGetMissing(t *testing.T) {
	want := "There is no council 'nope' for scout.\ncouncils: standard, ops-a"
	for _, args := range [][]string{
		{"roster", "get", "nope", "--bot", "scout"},
		{"-o", "yaml", "roster", "get", "nope", "--bot", "scout"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		_, _, err := councilArgs(t, p.env(t), args...)
		if err == nil {
			t.Fatalf("%v found a council", args)
		}
		if err.Error() != want {
			t.Errorf("%v: error %q, want %q", args, err.Error(), want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", args, code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	}

	t.Run("no councils", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK))
		_, _, err := councilArgs(t, p.env(t), "roster", "get", "nope", "--bot", "scout")
		if err == nil {
			t.Fatal("the command found a council")
		}
		if want := "There is no council 'nope' for scout."; err.Error() != want {
			t.Errorf("error %q, want %q", err.Error(), want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})

	t.Run("odd name", func(t *testing.T) {
		noBotMode(t)
		odd := rosterFix{name: "x\ny", display: "X", scope: "bot", version: 1,
			lead: rosterRef{slug: "captain", scope: "template"}}
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, odd))
		_, _, err := councilArgs(t, p.env(t), "roster", "get", "nope", "--bot", "scout")
		if err == nil {
			t.Fatal("the command found a council")
		}
		if want := "There is no council 'nope' for scout.\ncouncils: standard, x\\ny"; err.Error() != want {
			t.Errorf("error %q, want %q", err.Error(), want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})
}

// TestCouncilRosterUsage refuses what the commands can check before sending anything.
func TestCouncilRosterUsage(t *testing.T) {
	for _, c := range []struct {
		args   []string
		want   string
		prefix bool
	}{
		{args: []string{"roster", "list"}, want: "council roster list needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster list --bot scout"},
		{args: []string{"roster", "list", "scout", "--bot", "scout"}, want: "council roster list takes no arguments; name the bot with --bot, for example: tnctl council roster list --bot scout"},
		{args: []string{"roster", "list", "--bot", ""}, want: `"" is not a bot name: use the bot's name, such as scout`},
		{args: []string{"roster", "get", "ops"}, want: "council roster get needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster get ops --bot scout"},
		{args: []string{"roster", "get", "Ops", "--bot", "scout"}, want: `"Ops" is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter`},
		{args: []string{"roster", "get", "--bot", "scout"}, want: "council roster get needs one council's name, for example: tnctl council roster get ops --bot scout", prefix: true},
		{args: []string{"-o", "xml", "roster", "get", "BAD"}, want: `unknown output format "xml": use table, json or yaml`},
		{args: []string{"roster", "use", "ops", "--bot", "Scout"}, want: `"Scout" is not a bot name: use the bot's name, such as scout`},
		{args: []string{"roster", "use", "x", "--bot", "scout"}, want: `"x" is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter`},
		{args: []string{"roster", "use", "--bot", "scout"}, want: "council roster use needs one council's name, for example: tnctl council roster use ops --bot scout", prefix: true},
		{args: []string{"roster", "use", "ops"}, want: "council roster use needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster use ops --bot scout"},
		{args: []string{"roster", "delete", "ops"}, want: "council roster delete needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster delete ops-scout --bot scout"},
		{args: []string{"roster", "delete", "ros_abc", "--bot", "scout"}, want: `"ros_abc" is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter`},
		{args: []string{"roster", "delete", "--bot", "scout"}, want: "council roster delete needs one council's name, for example: tnctl council roster delete ops-scout --bot scout", prefix: true},
		{args: []string{"roster", "add", "ops-a", "--bot", "scout"}, want: "council roster add needs a council and a member, for example: tnctl council roster add ops-scout triage --bot scout", prefix: true},
		{args: []string{"roster", "add", "ops-a", "triage"}, want: "council roster add needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster add ops-scout triage --bot scout"},
		{args: []string{"roster", "add", "Ops-A", "triage", "--bot", "scout"}, want: `"Ops-A" is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter`},
		{args: []string{"roster", "add", "ops-a", "Tri", "--bot", "scout"}, want: `"Tri" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{args: []string{"roster", "add", "ops-a", "team/triage", "--bot", "scout"}, want: `"team/triage" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{args: []string{"roster", "add", "ops-a", "triage", "--bot", "scout", "--scope", "shared"}, want: `--scope is bot, teamspace or template, not "shared"`},
		{args: []string{"roster", "add", "ops-a", "teamspace/triage", "--bot", "scout", "--scope", "bot"}, want: `--scope bot and "teamspace/triage" name different scopes`},
		{args: []string{"roster", "add", "ops-a", "triage", "--bot", "scout", "--as", "Ops-B"}, want: `--as is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Ops-B"`},
		{args: []string{"roster", "add", "ops-a", "triage", "--bot", "scout", "--as", ""}, want: `--as is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not ""`},
		{args: []string{"roster", "remove", "ops-a", "--bot", "scout"}, want: "council roster remove needs a council and a member, for example: tnctl council roster remove ops-scout triage --bot scout", prefix: true},
		{args: []string{"roster", "remove", "ops-a", "triage"}, want: "council roster remove needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster remove ops-scout triage --bot scout"},
		{args: []string{"roster", "remove", "ops-a", "triage", "--bot", "scout", "--scope", ""}, want: `--scope is bot, teamspace or template, not ""`},
		{args: []string{"roster", "remove", "ops-a", "triage", "--bot", "scout", "--as", "Ops-B"}, want: `--as is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Ops-B"`},
		{args: []string{"roster", "remove", "ops-a", "Tri", "--bot", "scout"}, want: `"Tri" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		// The order: two faults at a time, the first one named.
		{args: []string{"-o", "xml", "roster", "add", "BAD", "Tri"}, want: `unknown output format "xml": use table, json or yaml`},
		{args: []string{"roster", "add", "BAD", "Tri", "--as", "X"}, want: "council roster add needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster add ops-scout triage --bot scout"},
		{args: []string{"roster", "add", "BAD", "Tri", "--bot", "scout", "--as", "X"}, want: `"BAD" is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter`},
		{args: []string{"roster", "add", "ops-a", "Tri", "--bot", "scout", "--scope", "nope", "--as", "X"}, want: `"Tri" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{args: []string{"roster", "add", "ops-a", "triage", "--bot", "scout", "--scope", "nope", "--as", "X"}, want: `--scope is bot, teamspace or template, not "nope"`},
		{args: []string{"-o", "xml", "roster", "get", "ops", "--bot", ""}, want: `unknown output format "xml": use table, json or yaml`},
		{args: []string{"roster", "get", "BAD", "--bot", ""}, want: `"" is not a bot name: use the bot's name, such as scout`},
		{args: []string{"roster", "add", "BAD", "Tri", "--bot", "Scout"}, want: `"Scout" is not a bot name: use the bot's name, such as scout`},
		{args: []string{"roster", "get", "ops", "--bot", "scout", "--as", "x"}, want: "unknown flag: --as", prefix: true},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		_, _, err := councilArgs(t, p.env(t), c.args...)
		if err == nil {
			t.Errorf("%v ran", c.args)
			continue
		}
		if c.prefix {
			if !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v: error %q, want the prefix %q", c.args, err, c.want)
			}
		} else if err.Error() != c.want {
			t.Errorf("%v: error %q, want %q", c.args, err, c.want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", c.args, code, codeUsage)
		}
		wantReqs(t, p)
	}
}

// ---------- council roster use ----------

// defaultAnswer is PUT bots/<bot>/council/default's answer with these councils, the default marked.
func defaultAnswer(rosters ...rosterFix) string { return councilViewJSON(canEditOK, rosters...) }

// TestCouncilRosterUse makes a council the bot's default.
func TestCouncilRosterUse(t *testing.T) {
	noBotMode(t)
	std := fxStandard
	std.isDefault = false
	opsA := fxOpsA
	opsA.isDefault = true
	p := newChatAPI(t)
	p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, opsA))
	out, errs, err := councilArgs(t, p.env(t), "roster", "use", "ops-a", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "scout's default council is now Ops A (ops-a)\n" {
		t.Errorf("stdout %q", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	puts := p.seen("PUT", "/bots/scout/council/default")
	if len(puts) != 1 {
		t.Fatalf("%d PUTs, want 1", len(puts))
	}
	compactEq(t, "body", puts[0].Body, `{"roster":"ops-a"}`)
	wantReqs(t, p, "PUT /bots/scout/council/default")

	t.Run("unavailable", func(t *testing.T) {
		noBotMode(t)
		bad := opsA
		bad.status = "invalid"
		bad.reasons = []string{"helper: this member was archived or deleted; replace it in this council."}
		p := newChatAPI(t)
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, bad))
		_, errs, err := councilArgs(t, p.env(t), "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		want := "· council ops-a — unavailable: helper: this member was archived or deleted; replace it in this council.\n"
		if errs != want {
			t.Errorf("stderr %q, want %q", errs, want)
		}
	})

	t.Run("not checked", func(t *testing.T) {
		noBotMode(t)
		u := opsA
		u.status = "unchecked"
		p := newChatAPI(t)
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, u))
		_, errs, err := councilArgs(t, p.env(t), "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})

	t.Run("json", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		answer := strings.Replace(defaultAnswer(std, opsA), `{"council":{`, `{"council":{"future":"<a&b>",`, 1)
		p.on("PUT", "/bots/scout/council/default", 200, answer)
		out, errs, err := councilArgs(t, p.env(t), "-o", "json", "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		compactEq(t, "json", out, answer)
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})

	t.Run("json unavailable", func(t *testing.T) {
		noBotMode(t)
		bad := opsA
		bad.status = "invalid"
		bad.reasons = []string{"helper: this member was archived or deleted; replace it in this council."}
		p := newChatAPI(t)
		answer := defaultAnswer(std, bad)
		p.on("PUT", "/bots/scout/council/default", 200, answer)
		out, errs, err := councilArgs(t, p.env(t), "-o", "json", "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		compactEq(t, "json", out, answer)
		if errs != "· council ops-a — unavailable: helper: this member was archived or deleted; replace it in this council.\n" {
			t.Errorf("stderr %q", errs)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, opsA))
		out, errs, err := councilArgs(t, p.env(t), "-o", "yaml", "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if !strings.HasPrefix(out, "council:\n") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("nameless default", func(t *testing.T) {
		noBotMode(t)
		nameless := rosterFix{scope: "bot", version: 1, isDefault: true, lead: rosterRef{slug: "captain", scope: "template"}}
		p := newChatAPI(t)
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, nameless))
		out, _, err := councilArgs(t, p.env(t), "roster", "use", "ops-a", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if out != "scout's default council is now ops-a\n" {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("premade", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(fxStandard))
		out, _, err := councilArgs(t, p.env(t), "roster", "use", "standard", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if out != "scout's default council is now Standard (standard)\n" {
			t.Errorf("stdout %q", out)
		}
	})
}

// TestCouncilRosterUseRefused maps the hub's refusals of a default switch.
func TestCouncilRosterUseRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"unknown roster", 404, councilErrBody("unknown_roster", "There is no council 'nope' for scout.", ""), codeUsage,
			"There is no council 'nope' for scout.\ntnctl council roster list --bot scout lists its councils"},
		{"not allowed", 403, councilErrBody("council_edit_not_allowed", "Only the bot's creator or a teamspace admin may change its council.", ""), codeAuth,
			"Only the bot's creator or a teamspace admin may change its council."},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("PUT", "/bots/scout/council/default", c.status, c.body)
			out, _, err := councilArgs(t, p.env(t), "roster", "use", "nope", "--bot", "scout")
			if err == nil {
				t.Fatal("the switch was refused but the command succeeded")
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			wantReqs(t, p, "PUT /bots/scout/council/default")
		})
	}
}

// ---------- council roster add ----------

// afterAdd is fxOpsA at version 4 with watcher added.
func afterAdd() rosterFix {
	r := fxOpsA
	r.version = 4
	r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
		{slug: "helper", scope: "bot"}, {slug: "watcher", scope: "teamspace"}}
	roles := make([]string, 0, len(fxOpsA.roles)+1)
	roles = append(roles, fxOpsA.roles...)
	roles = append(roles, roleViewJSON("watcher", "Watcher", "teamspace", "member", "ok", nil, nil))
	r.roles = roles
	return r
}

// changeRoutes answers a bot's council and the export of one council.
func changeRoutes(t *testing.T, p *chatAPI, council, export, roster string) {
	t.Helper()
	p.on("GET", "/bots/scout/council", 200, council)
	p.on("GET", "/bots/scout/council/rosters/"+roster+"/export", 200, export)
}

// TestCouncilRosterAdd adds a member to one of the bot's own councils.
func TestCouncilRosterAdd(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
	p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "updated"))
	out, errs, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "updated council ops-a (version 4) for scout; members: fleet, triage, helper, watcher\n" {
		t.Errorf("stdout %q", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	puts := p.seen("PUT", "/bots/scout/council/rosters/ops-a")
	if len(puts) != 1 {
		t.Fatalf("%d PUTs, want 1", len(puts))
	}
	wantBody := `{"spec":` + opsASpecHead + `,{"role":"watcher"}` + opsASpecTail +
		`,"expectVersion":3,"expectId":` + q(fxOpsA.id()) + `}`
	compactEq(t, "body", puts[0].Body, wantBody)
	wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
		"PUT /bots/scout/council/rosters/ops-a")

	t.Run("scope in the member", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "updated"))
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "teamspace/watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		puts := p.seen("PUT", "/bots/scout/council/rosters/ops-a")
		if len(puts) != 1 {
			t.Fatalf("%d PUTs, want 1", len(puts))
		}
		wantBody := `{"spec":` + opsASpecHead + `,{"role":"watcher","scope":"teamspace"}` + opsASpecTail +
			`,"expectVersion":3,"expectId":` + q(fxOpsA.id()) + `}`
		compactEq(t, "body", puts[0].Body, wantBody)
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
			"PUT /bots/scout/council/rosters/ops-a")
	})

	t.Run("scope in the flag", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "updated"))
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout", "--scope", "teamspace")
		if err != nil {
			t.Fatal(err)
		}
		puts := p.seen("PUT", "/bots/scout/council/rosters/ops-a")
		if len(puts) != 1 {
			t.Fatalf("%d PUTs, want 1", len(puts))
		}
		wantBody := `{"spec":` + opsASpecHead + `,{"role":"watcher","scope":"teamspace"}` + opsASpecTail +
			`,"expectVersion":3,"expectId":` + q(fxOpsA.id()) + `}`
		compactEq(t, "body", puts[0].Body, wantBody)
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
			"PUT /bots/scout/council/rosters/ops-a")
	})

	t.Run("no action", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), ""))
		out, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "saved council ops-a (version 4)") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("action with a line", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "updated\nnow"))
		out, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "updated now council ops-a") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("action with colour only", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "\x1b[0m"))
		out, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "saved council ops-a (version 4)") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("members from the answer", func(t *testing.T) {
		noBotMode(t)
		after2 := afterAdd()
		after2.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}, {slug: "\x1b[31mrol_ABCDEFGHIJKLMNOPQRSTUV"}}
		roles := make([]string, 0, len(fxOpsA.roles)+1)
		roles = append(roles, fxOpsA.roles...)
		roles = append(roles, roleViewJSON("watcher", "Watcher", "teamspace", "member", "ok", nil, nil))
		after2.roles = roles
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(after2, "updated"))
		out, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		want := "updated council ops-a (version 4) for scout; members: fleet, triage, helper, rol_ABCDEFGHIJKLMNOPQRSTUV\n"
		if out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
	})
}

// TestCouncilRosterCopyPremadeDefault copies a premade council that is the bot's default, and makes
// the copy the default.
func TestCouncilRosterCopyPremadeDefault(t *testing.T) {
	noBotMode(t)
	cp := func() rosterFix {
		return rosterFix{name: "scout-council", display: "scout-council", scope: "bot", version: 1,
			lead: rosterRef{slug: "captain", scope: "template"},
			members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template"},
				{slug: "triage", scope: "teamspace"}},
			reviewer: &rosterRef{slug: "critic", scope: "template"}}
	}
	std := fxStandard
	std.isDefault = false
	cpDefault := cp()
	cpDefault.isDefault = true
	wantOut := "created council scout-council (version 1) for scout, a copy of standard; members: fleet, coder, triage\n" +
		"scout's default council is now scout-council\n"
	const wantSpec = `{"displayName":"scout-council","lead":{"role":"captain","scope":"template"},` +
		`"members":[{"role":"fleet","scope":"template"},{"role":"coder","scope":"template"},{"role":"triage"}],` +
		`"reviewer":{"role":"critic","scope":"template"}}`
	wantReqsList := []string{"GET /bots/scout/council", "GET /bots/scout/council/rosters/standard/export",
		"PUT /bots/scout/council/rosters/scout-council", "PUT /bots/scout/council/default"}

	copyRoutes := func(t *testing.T, answer, defAnswer string) *chatAPI {
		t.Helper()
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
		p.on("PUT", "/bots/scout/council/rosters/scout-council", 200, answer)
		p.on("PUT", "/bots/scout/council/default", 200, defAnswer)
		return p
	}

	p := copyRoutes(t, rosterAnswer(cp(), "created"), defaultAnswer(std, cpDefault))
	out, errs, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != wantOut {
		t.Errorf("stdout %q, want %q", out, wantOut)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	copies := p.seen("PUT", "/bots/scout/council/rosters/scout-council")
	if len(copies) != 1 {
		t.Fatalf("%d copy PUTs, want 1", len(copies))
	}
	compactEq(t, "copy body", copies[0].Body, `{"spec":`+wantSpec+`,"expectVersion":0}`)
	if _, ok := bodyKeys(t, copies[0].Body)["expectId"]; ok {
		t.Errorf("a copy sends expectId: %q", copies[0].Body)
	}
	defs := p.seen("PUT", "/bots/scout/council/default")
	if len(defs) != 1 {
		t.Fatalf("%d default PUTs, want 1", len(defs))
	}
	compactEq(t, "default body", defs[0].Body, `{"roster":"scout-council"}`)
	wantReqs(t, p, wantReqsList...)

	t.Run("json", func(t *testing.T) {
		noBotMode(t)
		p := copyRoutes(t, rosterAnswer(cp(), "created"), defaultAnswer(std, cpDefault))
		out, errs, err := councilArgs(t, p.env(t), "-o", "json", "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		compactEq(t, "json", out, rosterAnswer(cp(), "created"))
		if errs != "· scout's default council is now scout-council\n" {
			t.Errorf("stderr %q", errs)
		}
		wantReqs(t, p, wantReqsList...)
	})

	t.Run("empty council answer", func(t *testing.T) {
		noBotMode(t)
		p := copyRoutes(t, rosterAnswer(cp(), "created"), `{"council":{"proto":2,"canEdit":{"ok":true},"rosters":[]}}`)
		out, _, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
		if err != nil {
			t.Fatal(err)
		}
		if out != wantOut {
			t.Errorf("stdout %q, want %q", out, wantOut)
		}
	})
}

// TestCouncilRosterCopyNoSwitch copies a council that is not the bot's default: the copy does not
// become the default.
func TestCouncilRosterCopyNoSwitch(t *testing.T) {
	noBotMode(t)
	std := fxStandard
	std.isDefault = false
	opsA := fxOpsA
	opsA.isDefault = true
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, std, opsA))
	p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
	cp := rosterFix{name: "scout-council", display: "scout-council", scope: "bot", version: 1,
		lead: rosterRef{slug: "captain", scope: "template"},
		members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template"},
			{slug: "triage", scope: "teamspace"}},
		reviewer: &rosterRef{slug: "critic", scope: "template"}}
	p.on("PUT", "/bots/scout/council/rosters/scout-council", 200, rosterAnswer(cp, "created"))
	// A default switch would answer 200: it must not be asked.
	p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, opsA))
	out, errs, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "created council scout-council (version 1) for scout, a copy of standard; members: fleet, coder, triage\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/standard/export",
		"PUT /bots/scout/council/rosters/scout-council")

	t.Run("own default", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, std, opsA))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		opsB := fxOpsA
		opsB.name, opsB.display, opsB.version = "ops-b", "ops-b", 1
		opsB.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}, {slug: "watcher", scope: "teamspace"}}
		roles := make([]string, 0, len(fxOpsA.roles)+1)
		roles = append(roles, fxOpsA.roles...)
		roles = append(roles, roleViewJSON("watcher", "Watcher", "teamspace", "member", "ok", nil, nil))
		opsB.roles = roles
		p.on("PUT", "/bots/scout/council/rosters/ops-b", 200, rosterAnswer(opsB, "created"))
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(std, opsA))
		out, errs, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout", "--as", "ops-b")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		want := "created council ops-b (version 1) for scout, a copy of ops-a; members: fleet, triage, helper, watcher\n"
		if out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
		puts := p.seen("PUT", "/bots/scout/council/rosters/ops-b")
		if len(puts) != 1 {
			t.Fatalf("%d PUTs, want 1", len(puts))
		}
		wantBody := `{"spec":` + strings.Replace(opsASpecHead, `"Ops A"`, `"ops-b"`, 1) +
			`,{"role":"watcher"}` + opsASpecTail + `,"expectVersion":0}`
		compactEq(t, "body", puts[0].Body, wantBody)
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
			"PUT /bots/scout/council/rosters/ops-b")
	})

	t.Run("copy unavailable", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, std, opsA))
		p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
		cpBad := rosterFix{name: "scout-council", display: "scout-council", scope: "bot", version: 1,
			status: "invalid", reasons: []string{"coder: needs a reviewer"},
			lead: rosterRef{slug: "captain", scope: "template"},
			members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template"},
				{slug: "triage", scope: "teamspace"}},
			reviewer: &rosterRef{slug: "critic", scope: "template"}}
		p.on("PUT", "/bots/scout/council/rosters/scout-council", 200, rosterAnswer(cpBad, "created"))
		out, errs, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		want := "created council scout-council (version 1) for scout, a copy of standard; members: fleet, coder, triage\n"
		if out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
		if errs != "· council scout-council — unavailable: coder: needs a reviewer\n" {
			t.Errorf("stderr %q", errs)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/standard/export",
			"PUT /bots/scout/council/rosters/scout-council")
	})
}

// TestCouncilRosterCopySwitchFails keeps the copy and says how to make it the default when the switch
// is refused.
func TestCouncilRosterCopySwitchFails(t *testing.T) {
	cp := rosterFix{name: "scout-council", display: "scout-council", scope: "bot", version: 1,
		lead: rosterRef{slug: "captain", scope: "template"},
		members: []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template"},
			{slug: "triage", scope: "teamspace"}},
		reviewer: &rosterRef{slug: "critic", scope: "template"}}
	wantOut := "created council scout-council (version 1) for scout, a copy of standard; members: fleet, coder, triage\n"
	for _, c := range []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"unknown roster", 404, councilErrBody("unknown_roster", "There is no council 'scout-council' for scout.", ""), codeUsage,
			"There is no council 'scout-council' for scout.\ncouncil scout-council was saved, but it is not the default: tnctl council roster use scout-council --bot scout makes it the default"},
		{"not allowed", 403, councilErrBody("council_edit_not_allowed", "Only the bot's creator or a teamspace admin may change its council.", ""), codeAuth,
			"Only the bot's creator or a teamspace admin may change its council.\ncouncil scout-council was saved, but it is not the default: tnctl council roster use scout-council --bot scout makes it the default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
			p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
			p.on("PUT", "/bots/scout/council/rosters/scout-council", 200, rosterAnswer(cp, "created"))
			p.on("PUT", "/bots/scout/council/default", c.status, c.body)
			out, _, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout", "--as", "scout-council")
			if err == nil {
				t.Fatal("the default switch was refused but the command succeeded")
			}
			if out != wantOut {
				t.Errorf("stdout %q, want %q", out, wantOut)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/standard/export",
				"PUT /bots/scout/council/rosters/scout-council", "PUT /bots/scout/council/default")
		})
	}
}

// scoutBot is the bot's own council a derived name may collide with.
func scoutBot() rosterFix {
	return rosterFix{name: "scout-council", display: "Scout council", scope: "bot", version: 1,
		lead:    rosterRef{slug: "captain", scope: "template"},
		members: []rosterRef{{slug: "fleet", scope: "template"}}}
}

// TestCouncilRosterPremadeNeedsAs refuses a premade council's change without --as.
func TestCouncilRosterPremadeNeedsAs(t *testing.T) {
	const want = "'standard' is a premade council and cannot be changed: use --as scout-council to save a copy"
	for _, args := range [][]string{
		{"roster", "add", "standard", "triage", "--bot", "scout"},
		{"roster", "remove", "standard", "fleet", "--bot", "scout"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		_, _, err := councilArgs(t, p.env(t), args...)
		if err == nil {
			t.Fatalf("%v changed a premade council", args)
		}
		if err.Error() != want {
			t.Errorf("%v: error %q, want %q", args, err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", args, code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	}

	t.Run("derived name taken", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA, scoutBot()))
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "standard", "triage", "--bot", "scout")
		if err == nil {
			t.Fatal("a premade council was changed")
		}
		want := "'standard' is a premade council and cannot be changed: use --as scout-council-2 to save a copy"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})
}

// TestCouncilRosterAsTaken refuses an --as name the bot already has.
func TestCouncilRosterAsTaken(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"roster", "add", "ops-a", "triage", "--bot", "scout", "--as", "standard"},
			"there is a council 'standard' for scout already: pick another name with --as, such as scout-council"},
		{[]string{"roster", "add", "standard", "triage", "--bot", "scout", "--as", "ops-a"},
			"there is a council 'ops-a' for scout already: pick another name with --as, such as scout-council"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		_, _, err := councilArgs(t, p.env(t), c.args...)
		if err == nil {
			t.Fatalf("%v was accepted", c.args)
		}
		if err.Error() != c.want {
			t.Errorf("%v: error %q, want %q", c.args, err, c.want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", c.args, code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	}

	t.Run("derived name taken", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA, scoutBot()))
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "triage", "--bot", "scout", "--as", "scout-council")
		if err == nil {
			t.Fatal("a taken name was accepted")
		}
		want := "there is a council 'scout-council' for scout already: pick another name with --as, such as scout-council-2"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})
}

// TestCouncilRosterCanEdit refuses a change before the export when the person may not change the
// bot's councils.
func TestCouncilRosterCanEdit(t *testing.T) {
	const reason = "Only the bot’s creator or a teamspace admin may change its council."
	for _, c := range []struct {
		name   string
		answer string
		args   []string
		exit   int
		want   string
	}{
		{"own", councilViewJSON(`{"ok":false,"reason":"`+reason+`"}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, codeAuth, reason},
		{"premade", councilViewJSON(`{"ok":false,"reason":"`+reason+`"}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "standard", "watcher", "--bot", "scout"}, codeAuth, reason},
		{"no reason", councilViewJSON(`{"ok":false}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, codeAuth, "you may not change scout's councils"},
		{"no council", councilViewJSON(`{"ok":false,"reason":"`+reason+`"}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "nope", "watcher", "--bot", "scout"}, codeUsage,
			"There is no council 'nope' for scout.\ncouncils: standard, ops-a"},
		{"as taken", councilViewJSON(`{"ok":false,"reason":"`+reason+`"}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "ops-a", "watcher", "--bot", "scout", "--as", "standard"}, codeAuth, reason},
		{"colour only", councilViewJSON(`{"ok":false,"reason":"\u001b[31m"}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, codeAuth, "you may not change scout's councils"},
		{"two lines", councilViewJSON(`{"ok":false,"reason":"Only the creator\nmay \u001b[31mchange it."}`, fxStandard, fxOpsA),
			[]string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, codeAuth, "Only the creator may change it."},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, c.answer)
			p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
			_, _, err := councilArgs(t, p.env(t), c.args...)
			if err == nil {
				t.Fatalf("%v was allowed", c.args)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			wantReqs(t, p, "GET /bots/scout/council")
		})
	}

	t.Run("gone before can edit", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(`{"ok":false,"reason":"`+reason+`"}`, fxStandard, opsAGone()))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err == nil {
			t.Fatal("the change was allowed")
		}
		if err.Error() != reason {
			t.Errorf("error %q, want %q", err, reason)
		}
		if code := exitCodeOf(err); code != codeAuth {
			t.Errorf("exit %d, want %d", code, codeAuth)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})
}

// TestCouncilRosterAlreadyMember says so, and sends nothing, when the council already has the member.
func TestCouncilRosterAlreadyMember(t *testing.T) {
	noChange := func(t *testing.T, p *chatAPI, args ...string) (string, string) {
		t.Helper()
		out, errs, err := councilArgs(t, p.env(t), args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		return out, errs
	}
	baseRoutes := func(t *testing.T) *chatAPI {
		t.Helper()
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
		return p
	}
	for _, member := range []string{"fleet", "triage", "teamspace/triage"} {
		t.Run(member, func(t *testing.T) {
			noBotMode(t)
			p := baseRoutes(t)
			out, errs := noChange(t, p, "roster", "add", "ops-a", member, "--bot", "scout")
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			if want := "· " + strings.TrimPrefix(member, "teamspace/") + " is already a member of ops-a: nothing to change\n"; errs != want {
				t.Errorf("stderr %q, want %q", errs, want)
			}
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
		})
	}

	t.Run("another scope", func(t *testing.T) {
		noBotMode(t)
		p := baseRoutes(t)
		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "bot/triage", "--bot", "scout")
		if err == nil {
			t.Fatal("a member of another scope was accepted")
		}
		want := "ops-a has a member 'triage' already (teamspace/triage): member slugs are unique in a council"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})

	t.Run("json", func(t *testing.T) {
		noBotMode(t)
		p := baseRoutes(t)
		out, errs := noChange(t, p, "-o", "json", "roster", "add", "ops-a", "fleet", "--bot", "scout")
		compactEq(t, "json", out, `{"roster":`+fxOpsA.json()+`,"action":"unchanged"}`)
		if errs != "· fleet is already a member of ops-a: nothing to change\n" {
			t.Errorf("stderr %q", errs)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})

	t.Run("json, entry it does not know", func(t *testing.T) {
		noBotMode(t)
		entry := strings.Replace(fxOpsA.json(), `{"id":`, `{"future":"<a&b>","id":`, 1)
		p2 := newChatAPI(t)
		p2.on("GET", "/bots/scout/council", 200, strings.Replace(councilViewJSON(canEditOK, fxStandard, fxOpsA), fxOpsA.json(), entry, 1))
		p2.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		p2.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
		out, errs, err := councilArgs(t, p2.env(t), "-o", "json", "roster", "add", "ops-a", "fleet", "--bot", "scout")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		compactEq(t, "json", out, `{"roster":`+entry+`,"action":"unchanged"}`)
		if !strings.Contains(out, `"future": "<a&b>"`) {
			t.Errorf("stdout %q loses the entry it does not know", out)
		}
		wantReqs(t, p2, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})

	t.Run("yaml", func(t *testing.T) {
		noBotMode(t)
		p := baseRoutes(t)
		out, errs := noChange(t, p, "-o", "yaml", "roster", "add", "ops-a", "fleet", "--bot", "scout")
		if !strings.Contains(out, "action: unchanged\n") {
			t.Errorf("stdout %q", out)
		}
		if !strings.Contains(out, "name: ops-a\n") {
			t.Errorf("stdout %q", out)
		}
		if strings.HasPrefix(out, "{") {
			t.Errorf("stdout is JSON: %q", out)
		}
		if errs != "· fleet is already a member of ops-a: nothing to change\n" {
			t.Errorf("stderr %q", errs)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})

	t.Run("premade copy", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
		p.on("PUT", "/bots/scout/council/rosters/scout-council", 200, rosterAnswer(fxStandard, "created"))
		out, errs := noChange(t, p, "roster", "add", "standard", "fleet", "--bot", "scout", "--as", "scout-council")
		if out != "" {
			t.Errorf("stdout %q", out)
		}
		if errs != "· fleet is already a member of standard: nothing to change, so scout-council was not saved\n" {
			t.Errorf("stderr %q", errs)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/standard/export")
	})
}

// TestCouncilRosterAddRefused maps the hub's refusals of a council's PUT.
func TestCouncilRosterAddRefused(t *testing.T) {
	const ambiguous = "More than one member is called 'watcher' (teamspace, bot): name the scope."
	rows := []struct {
		name   string
		args   []string
		export string
		path   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"invalid members", nil, "", "", 400,
			councilErrBody("invalid", "a council has 1 to 5 members", `"fields":[{"path":"spec.members","message":"a council has 1 to 5 members"}]`),
			codeRejected, "spec.members: a council has 1 to 5 members"},
		{"invalid reviewer", nil, "", "", 400,
			councilErrBody("invalid", "a council with the Coder needs a reviewer", `"fields":[{"path":"spec.reviewer","message":"a council with the Coder needs a reviewer"}]`),
			codeRejected, "spec.reviewer: a council with the Coder needs a reviewer"},
		{"unknown member", nil, "", "", 400,
			councilErrBody("unknown_member", "'watcher' is not a member this bot can use.", `"fields":[{"path":"spec.members[3]","message":"'watcher' is not a member this bot can use."}]`),
			codeUsage, "spec.members[3]: 'watcher' is not a member this bot can use.\n" +
				"tnctl council role list --bot scout --templates lists the members scout can use"},
		{"ambiguous", nil, "", "", 400,
			councilErrBody("ambiguous", ambiguous, `"fields":[{"path":"spec.members[3]","message":"`+ambiguous+`"}]`),
			codeUsage, "spec.members[3]: " + ambiguous + "\n" +
				"name its scope (bot, teamspace or template): tnctl council roster add ops-a <scope>/watcher --bot scout"},
		{"conflict", nil, "", "", 409,
			councilErrBody("conflict", "anna@example.com changed this council at 10:42 UTC (version 4). Reload to see it, then make your change again.",
				`"current":{"version":4,"updatedBy":"anna@example.com","updatedAt":1}`),
			codeConflict, "anna@example.com changed this council at 10:42 UTC (version 4). Reload to see it, then make your change again."},
		{"not allowed", nil, "", "", 403,
			councilErrBody("council_edit_not_allowed", "Only the bot's creator or a teamspace admin may change its council.", ""),
			codeAuth, "Only the bot's creator or a teamspace admin may change its council."},
		// A copy's refusals.
		{"exists", []string{"roster", "add", "standard", "watcher", "--bot", "scout", "--as", "scout-council"}, exportStandard, "scout-council", 409,
			councilErrBody("exists", "A council 'scout-council' exists already.", ""),
			codeConflict, "A council 'scout-council' exists already."},
		{"limit", []string{"roster", "add", "standard", "watcher", "--bot", "scout", "--as", "scout-council"}, exportStandard, "scout-council", 409,
			councilErrBody("limit", "This bot has 8 councils. Delete one you no longer use.", ""),
			codeRejected, "This bot has 8 councils. Delete one you no longer use."},
		{"ambiguous copy", []string{"roster", "add", "standard", "watcher", "--bot", "scout", "--as", "scout-council"}, exportStandard, "scout-council", 400,
			councilErrBody("ambiguous", ambiguous, `"fields":[{"path":"spec.members[2]","message":"`+ambiguous+`"}]`),
			codeUsage, "spec.members[2]: " + ambiguous + "\n" +
				"name its scope (bot, teamspace or template): tnctl council roster add standard <scope>/watcher --bot scout --as scout-council"},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
			p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
			put, path := "PUT /bots/scout/council/rosters/ops-a", "/bots/scout/council/rosters/ops-a"
			if c.export != "" {
				p.on("GET", "/bots/scout/council/rosters/standard/export", 200, c.export)
				put, path = "PUT /bots/scout/council/rosters/"+c.path, "/bots/scout/council/rosters/"+c.path
			}
			p.onSeq("PUT", path, chatReply{c.status, c.body}, chatReply{200, rosterAnswer(fxOpsA, "updated")})
			p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(fxStandard))
			args := c.args
			if args == nil {
				args = []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}
			}
			_, _, err := councilArgs(t, p.env(t), args...)
			if err == nil {
				t.Fatal("the PUT was refused but the command succeeded")
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			want := []string{"GET /bots/scout/council"}
			if c.export != "" {
				want = append(want, "GET /bots/scout/council/rosters/standard/export")
			} else {
				want = append(want, "GET /bots/scout/council/rosters/ops-a/export")
			}
			wantReqs(t, p, append(want, put)...)
		})
	}
}

// TestCouncilRosterGone refuses a change to a council that names a member that is gone, and names
// the step that fixes it.
func TestCouncilRosterGone(t *testing.T) {
	const helperLine = "council ops-a names a member that was archived or deleted: helper\n" +
		"take it out first: tnctl council roster remove ops-a helper --bot scout"
	const leadLine = "council ops-a has a lead that was archived or deleted: captain\n" + goneHint
	gone := func(t *testing.T) *chatAPI {
		t.Helper()
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, opsAGone()))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(fxStandard))
		return p
	}
	refused := func(t *testing.T, answer string, args []string, want string) {
		t.Helper()
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, answer)
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
		p.on("PUT", "/bots/scout/council/default", 200, defaultAnswer(fxStandard))
		_, _, err := councilArgs(t, p.env(t), args...)
		if err == nil {
			t.Fatalf("%v was allowed", args)
		}
		if err.Error() != want {
			t.Errorf("%v: error %q, want %q", args, err, want)
		}
		if code := exitCodeOf(err); code != codeError {
			t.Errorf("%v: exit %d, want %d", args, code, codeError)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	}

	for _, args := range [][]string{
		{"roster", "add", "ops-a", "watcher", "--bot", "scout"},
		{"roster", "add", "ops-a", "watcher", "--bot", "scout", "--as", "ops-b"},
		{"roster", "add", "ops-a", "helper", "--bot", "scout"},
		{"roster", "remove", "ops-a", "fleet", "--bot", "scout"},
		{"roster", "remove", "ops-a", "teamspace/helper", "--bot", "scout"},
	} {
		refused(t, councilViewJSON(canEditOK, fxStandard, opsAGone()), args, helperLine)
	}

	t.Run("remove the gone member", func(t *testing.T) {
		for _, member := range []string{"helper", "bot/helper"} {
			noBotMode(t)
			p := gone(t)
			_, errs, err := councilArgs(t, p.env(t), "roster", "remove", "ops-a", member, "--bot", "scout")
			if err != nil {
				t.Fatalf("%s: %v\n%s", member, err, errs)
			}
			puts := p.seen("PUT", "/bots/scout/council/rosters/ops-a")
			if len(puts) != 1 {
				t.Fatalf("%s: %d PUTs, want 1", member, len(puts))
			}
			wantSpec := `{"displayName":"Ops A","description":"Day to day.","lead":{"role":"captain","scope":"template"},` +
				`"members":[{"role":"fleet","scope":"template"},{"role":"triage","scope":"teamspace","version":4}],` +
				`"reviewer":null,"budget":{"maxTokens":150000}}`
			compactEq(t, "body", puts[0].Body, `{"spec":`+wantSpec+`,"expectVersion":3,"expectId":`+q(fxOpsA.id())+`}`)
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
				"PUT /bots/scout/council/rosters/ops-a")
		}
	})

	t.Run("lead gone", func(t *testing.T) {
		r := opsAGone()
		r.lead = rosterRef{slug: "captain", scope: "template", gone: true}
		r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}}
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, leadLine)
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "remove", "ops-a", "captain", "--bot", "scout"}, leadLine)
	})

	t.Run("reviewer gone", func(t *testing.T) {
		want := "council ops-a has a reviewer that was archived or deleted: critic\n" + goneHint
		r := fxOpsA
		r.reviewer = &rosterRef{slug: "critic", scope: "template", gone: true}
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, want)
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "remove", "ops-a", "critic", "--bot", "scout"}, want)
	})

	t.Run("lead before member", func(t *testing.T) {
		r := opsAGone()
		r.lead = rosterRef{slug: "captain", scope: "template", gone: true}
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, leadLine)
	})

	t.Run("member before reviewer", func(t *testing.T) {
		r := opsAGone()
		r.reviewer = &rosterRef{slug: "critic", scope: "template", gone: true}
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"},
			"council ops-a names a member that was archived or deleted: helper\n"+goneHint)
		want := "council ops-a has a reviewer that was archived or deleted: critic\n" + goneHint
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "remove", "ops-a", "helper", "--bot", "scout"}, want)
	})

	t.Run("two members gone", func(t *testing.T) {
		r := opsAGone()
		r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4, gone: true},
			{slug: "helper", scope: "bot", gone: true}}
		r.roles = []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member")}
		ans := councilViewJSON(canEditOK, fxStandard, r)
		triage := "council ops-a names a member that was archived or deleted: triage\n" + goneHint
		refused(t, ans, []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, triage)
		refused(t, ans, []string{"roster", "remove", "ops-a", "helper", "--bot", "scout"}, triage)
		refused(t, ans, []string{"roster", "remove", "ops-a", "triage", "--bot", "scout"},
			"council ops-a names a member that was archived or deleted: helper\n"+goneHint)
	})

	t.Run("premade", func(t *testing.T) {
		std := fxStandard
		std.status = "invalid"
		std.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template", gone: true}}
		std.roles = []string{tplRole("captain", "Captain", "lead"), tplRole("fleet", "Fleet", "member"),
			tplRole("critic", "Critic", "reviewer")}
		want := "council standard names a member that was archived or deleted: coder\n" + goneHint
		refused(t, councilViewJSON(canEditOK, std, fxOpsA),
			[]string{"roster", "add", "standard", "watcher", "--bot", "scout", "--as", "scout-council"}, want)
	})

	t.Run("raw id", func(t *testing.T) {
		r := fxOpsA
		r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}, {slug: "rol_ABCDEFGHIJKLMNOPQRSTUV", gone: true}}
		want := "council ops-a names a member that was archived or deleted: rol_ABCDEFGHIJKLMNOPQRSTUV\n" + goneHint
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, want)
	})

	t.Run("odd slug", func(t *testing.T) {
		r := fxOpsA
		r.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "triage", scope: "teamspace", pin: 4},
			{slug: "helper", scope: "bot"}, {slug: "bad\x1b[31m\nrole", gone: true}}
		want := "council ops-a names a member that was archived or deleted: bad\\u001b[31m\\nrole\n" + goneHint
		refused(t, councilViewJSON(canEditOK, fxStandard, r), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout"}, want)
	})

	t.Run("steps before the gone check", func(t *testing.T) {
		std := fxStandard
		std.status = "invalid"
		std.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "coder", scope: "template", gone: true}}
		for _, c := range []struct {
			answer string
			args   []string
			want   string
		}{
			{councilViewJSON(canEditOK, std, fxOpsA), []string{"roster", "add", "standard", "watcher", "--bot", "scout"},
				"'standard' is a premade council and cannot be changed: use --as scout-council to save a copy"},
			{councilViewJSON(canEditOK, fxStandard, opsAGone()), []string{"roster", "add", "ops-a", "watcher", "--bot", "scout", "--as", "standard"},
				"there is a council 'standard' for scout already: pick another name with --as, such as scout-council"},
		} {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, c.answer)
			p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
			p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
			_, _, err := councilArgs(t, p.env(t), c.args...)
			if err == nil {
				t.Fatalf("%v was allowed", c.args)
			}
			if err.Error() != c.want {
				t.Errorf("%v: error %q, want %q", c.args, err, c.want)
			}
			if code := exitCodeOf(err); code != codeUsage {
				t.Errorf("%v: exit %d, want %d", c.args, code, codeUsage)
			}
			wantReqs(t, p, "GET /bots/scout/council")
		}
	})

	t.Run("without refs", func(t *testing.T) {
		r := opsAGone()
		r.noRefs = true
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, r))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		refusal := councilErrBody("unknown_member", "'helper' is not a member this bot can use.",
			`"fields":[{"path":"spec.members[2]","message":"'helper' is not a member this bot can use."}]`)
		p.onSeq("PUT", "/bots/scout/council/rosters/ops-a", chatReply{400, refusal}, chatReply{200, rosterAnswer(fxOpsA, "updated")})

		_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err == nil {
			t.Fatal("the PUT was refused but the command succeeded")
		}
		want := "spec.members[2]: 'helper' is not a member this bot can use.\n" +
			"tnctl council role list --bot scout --templates lists the members scout can use"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("add: exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
			"PUT /bots/scout/council/rosters/ops-a")

		noBotMode(t)
		p2 := newChatAPI(t)
		p2.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, r))
		p2.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
		p2.onSeq("PUT", "/bots/scout/council/rosters/ops-a", chatReply{400, refusal}, chatReply{200, rosterAnswer(fxOpsA, "updated")})
		_, _, err = councilArgs(t, p2.env(t), "roster", "remove", "ops-a", "fleet", "--bot", "scout")
		if err == nil {
			t.Fatal("the PUT was refused but the command succeeded")
		}
		if want := "spec.members[2]: 'helper' is not a member this bot can use."; err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("remove: exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p2, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
			"PUT /bots/scout/council/rosters/ops-a")
	})
}

// TestCouncilRosterExportFails maps the export's refusals, before the PUT.
func TestCouncilRosterExportFails(t *testing.T) {
	gone := "This council names a member that no longer exists; replace it before you export the council."
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"role gone", 409, councilErrBody("role_gone", gone, ""), gone + "\n" + goneHint},
		{"wrong kind", 200, "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\nspec: {}\n",
			`the platform's council export could not be read: kind "CouncilRole" is not CouncilRoster`},
		{"bad budget", 200, strings.Replace(exportOpsA, "maxTokens: 150000", "maxTokens: lots", 1),
			"the platform's council export could not be read: yaml: unmarshal errors:\\n  line 24: cannot unmarshal !!str `lots` into int"},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
			p.on("GET", "/bots/scout/council/rosters/ops-a/export", c.status, c.body)
			p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
			_, _, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
			if err == nil {
				t.Fatal("the export was refused but the command succeeded")
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != codeError {
				t.Errorf("exit %d, want %d", code, codeError)
			}
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
		})
	}
}

// TestCouncilRosterRemove drops a member from one of the bot's own councils.
func TestCouncilRosterRemove(t *testing.T) {
	wantSpec := `{"displayName":"Ops A","description":"Day to day.","lead":{"role":"captain","scope":"template"},` +
		`"members":[{"role":"fleet","scope":"template"},{"role":"helper","scope":"bot"}],` +
		`"reviewer":null,"budget":{"maxTokens":150000}}`
	for _, member := range []string{"triage", "teamspace/triage"} {
		t.Run(member, func(t *testing.T) {
			noBotMode(t)
			after := fxOpsA
			after.version = 4
			after.members = []rosterRef{{slug: "fleet", scope: "template"}, {slug: "helper", scope: "bot"}}
			p := newChatAPI(t)
			changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
			p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(after, "updated"))
			out, errs, err := councilArgs(t, p.env(t), "roster", "remove", "ops-a", member, "--bot", "scout")
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if out != "updated council ops-a (version 4) for scout; members: fleet, helper\n" {
				t.Errorf("stdout %q", out)
			}
			puts := p.seen("PUT", "/bots/scout/council/rosters/ops-a")
			if len(puts) != 1 {
				t.Fatalf("%d PUTs, want 1", len(puts))
			}
			compactEq(t, "body", puts[0].Body, `{"spec":`+wantSpec+`,"expectVersion":3,"expectId":`+q(fxOpsA.id())+`}`)
			if errs != "" {
				t.Errorf("stderr %q", errs)
			}
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export",
				"PUT /bots/scout/council/rosters/ops-a")
		})
	}
}

// TestCouncilRosterRemoveNotMember names the member, the lead or the reviewer rather than dropping
// something else.
func TestCouncilRosterRemoveNotMember(t *testing.T) {
	for _, c := range []struct {
		args   []string
		export string
		want   string
	}{
		{[]string{"roster", "remove", "ops-a", "watcher", "--bot", "scout"}, exportOpsA,
			"'watcher' is not a member of ops-a. Members: fleet, triage, helper."},
		{[]string{"roster", "remove", "ops-a", "bot/triage", "--bot", "scout"}, exportOpsA,
			"'bot/triage' is not a member of ops-a. Members: fleet, triage, helper."},
		{[]string{"roster", "remove", "ops-a", "triage", "--bot", "scout", "--scope", "bot"}, exportOpsA,
			"'bot/triage' is not a member of ops-a. Members: fleet, triage, helper."},
		{[]string{"roster", "remove", "ops-a", "captain", "--bot", "scout"}, exportOpsA,
			"'captain' leads ops-a; it is not a member: the lead is changed in YAML (tnctl council apply) for now"},
		{[]string{"roster", "remove", "ops-a", "bot/captain", "--bot", "scout"}, exportOpsA,
			"'captain' leads ops-a; it is not a member: the lead is changed in YAML (tnctl council apply) for now"},
		{[]string{"roster", "remove", "standard", "critic", "--bot", "scout", "--as", "scout-council"}, exportStandard,
			"'critic' is the reviewer of standard, not a member: change the reviewer in the web chat's Council tab or in YAML (tnctl council apply)"},
		{[]string{"roster", "remove", "standard", "template/critic", "--bot", "scout", "--as", "scout-council"}, exportStandard,
			"'critic' is the reviewer of standard, not a member: change the reviewer in the web chat's Council tab or in YAML (tnctl council apply)"},
	} {
		t.Run(strings.Join(c.args[2:4], " "), func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
			p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, exportOpsA)
			p.on("GET", "/bots/scout/council/rosters/standard/export", 200, exportStandard)
			p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
			_, _, err := councilArgs(t, p.env(t), c.args...)
			if err == nil {
				t.Fatalf("%v removed something", c.args)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != codeUsage {
				t.Errorf("exit %d, want %d", code, codeUsage)
			}
			council := "ops-a"
			if strings.Contains(c.args[2], "standard") {
				council = "standard"
			}
			wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/"+council+"/export")
		})
	}

	t.Run("escaped members", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		export := strings.Replace(exportOpsA, "    - role: helper\n", "    - role: \"help\\eer\"\n", 1)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		p.on("GET", "/bots/scout/council/rosters/ops-a/export", 200, export)
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(fxOpsA, "updated"))
		_, _, err := councilArgs(t, p.env(t), "roster", "remove", "ops-a", "watcher", "--bot", "scout")
		if err == nil {
			t.Fatal("something was removed")
		}
		if want := "'watcher' is not a member of ops-a. Members: fleet, triage, help\\u001ber."; err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council/rosters/ops-a/export")
	})
}

// TestCouncilRosterChangeMissing names the councils the bot has when the name is not one of them.
func TestCouncilRosterChangeMissing(t *testing.T) {
	want := "There is no council 'nope' for scout.\ncouncils: standard, ops-a"
	for _, args := range [][]string{
		{"roster", "add", "nope", "triage", "--bot", "scout"},
		{"roster", "remove", "nope", "triage", "--bot", "scout"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, councilViewJSON(canEditOK, fxStandard, fxOpsA))
		_, _, err := councilArgs(t, p.env(t), args...)
		if err == nil {
			t.Fatalf("%v found a council", args)
		}
		if err.Error() != want {
			t.Errorf("%v: error %q, want %q", args, err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", args, code, codeUsage)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	}
}

// TestCouncilRosterChangeJSON prints the PUT's whole answer.
func TestCouncilRosterChangeJSON(t *testing.T) {
	noBotMode(t)
	ans := strings.Replace(rosterAnswer(afterAdd(), "updated"), `{"roster":{`, `{"roster":{"future":"<a&b>",`, 1)
	p := newChatAPI(t)
	changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
	p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, ans)
	out, errs, err := councilArgs(t, p.env(t), "-o", "json", "roster", "add", "ops-a", "watcher", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	compactEq(t, "json", out, ans)
	if !strings.Contains(out, `"future": "<a&b>"`) {
		t.Errorf("stdout %q loses the key it does not know", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}

	t.Run("yaml", func(t *testing.T) {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(afterAdd(), "updated"))
		out, _, err := councilArgs(t, p.env(t), "-o", "yaml", "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "action: updated") {
			t.Errorf("stdout %q", out)
		}
		if strings.Contains(out, "updated council") {
			t.Errorf("stdout holds the table line: %q", out)
		}
	})
}

// TestCouncilRosterChangeUnavailable says on stderr that the changed council cannot answer.
func TestCouncilRosterChangeUnavailable(t *testing.T) {
	bad := afterAdd()
	bad.status = "invalid"
	bad.reasons = []string{"watcher: needs a reviewer"}
	for _, args := range [][]string{
		{"roster", "add", "ops-a", "watcher", "--bot", "scout"},
		{"-o", "json", "roster", "add", "ops-a", "watcher", "--bot", "scout"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(bad, "updated"))
		out, errs, err := councilArgs(t, p.env(t), args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if args[0] == "roster" && !strings.Contains(out, "updated council ops-a (version 4) for scout") {
			t.Errorf("stdout %q", out)
		}
		if args[0] == "-o" {
			compactEq(t, "json", out, rosterAnswer(bad, "updated"))
		}
		if errs != "· council ops-a — unavailable: watcher: needs a reviewer\n" {
			t.Errorf("stderr %q", errs)
		}
	}

	t.Run("not checked", func(t *testing.T) {
		noBotMode(t)
		u := afterAdd()
		u.status = "unchecked"
		p := newChatAPI(t)
		changeRoutes(t, p, councilViewJSON(canEditOK, fxStandard, fxOpsA), exportOpsA, "ops-a")
		p.on("PUT", "/bots/scout/council/rosters/ops-a", 200, rosterAnswer(u, "updated"))
		out, errs, err := councilArgs(t, p.env(t), "roster", "add", "ops-a", "watcher", "--bot", "scout")
		if err != nil {
			t.Fatal(err)
		}
		if out != "updated council ops-a (version 4) for scout; members: fleet, triage, helper, watcher\n" {
			t.Errorf("stdout %q", out)
		}
		if errs != "" {
			t.Errorf("stderr %q", errs)
		}
	})
}

// TestCouncilRosterDelete deletes one of the bot's own councils.
func TestCouncilRosterDelete(t *testing.T) {
	for _, args := range [][]string{
		{"roster", "delete", "ops-a", "--bot", "scout"},
		{"-o", "json", "roster", "delete", "ops-a", "--bot", "scout"},
		{"-o", "xml", "roster", "delete", "ops-a", "--bot", "scout"},
	} {
		noBotMode(t)
		p := newChatAPI(t)
		p.on("DELETE", "/bots/scout/council/rosters/ops-a", 204, "")
		out, errs, err := councilArgs(t, p.env(t), args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errs)
		}
		if out != "deleted council ops-a of scout\n" {
			t.Errorf("%v: stdout %q", args, out)
		}
		if errs != "· chats that asked it now ask scout's default council\n" {
			t.Errorf("%v: stderr %q", args, errs)
		}
		wantReqs(t, p, "DELETE /bots/scout/council/rosters/ops-a")
	}
}

// TestCouncilRosterDeleteRefused maps the hub's refusals of a delete.
func TestCouncilRosterDeleteRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   []string
		status int
		body   string
		exit   int
		want   string
	}{
		{"the default", []string{"roster", "delete", "ops-a", "--bot", "scout"}, 409,
			councilErrBody("roster_default", "This is scout's default council. Make another one the default first.", ""),
			codeRejected, "This is scout's default council. Make another one the default first.\n" +
				"tnctl council roster use <council> --bot scout makes another council the default"},
		{"unknown", []string{"roster", "delete", "ops-a", "--bot", "scout"}, 404,
			councilErrBody("unknown_roster", "There is no council 'ops-a' for scout.", ""), codeUsage,
			"There is no council 'ops-a' for scout.\ntnctl council roster list --bot scout lists its councils"},
		{"premade", []string{"roster", "delete", "standard", "--bot", "scout"}, 400,
			councilErrBody("template_read_only", "Premade members and councils are read-only: give it a new name to clone it.", ""),
			codeRejected, "Premade members and councils are read-only: give it a new name to clone it."},
		{"not allowed", []string{"roster", "delete", "ops-a", "--bot", "scout"}, 403,
			councilErrBody("council_edit_not_allowed", "Only the bot's creator or a teamspace admin may change its council.", ""),
			codeAuth, "Only the bot's creator or a teamspace admin may change its council."},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			name := c.args[2]
			p.on("DELETE", "/bots/scout/council/rosters/"+name, c.status, c.body)
			out, _, err := councilArgs(t, p.env(t), c.args...)
			if err == nil {
				t.Fatal("the delete was refused but the command succeeded")
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			wantReqs(t, p, "DELETE /bots/scout/council/rosters/"+name)
		})
	}
}

// TestCouncilRosterApplyAlias runs council apply from council roster.
func TestCouncilRosterApplyAlias(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	out, errs, err := councilArgs(t, p.env(t), "roster", "apply", "-f", councilFile(t, "kind: CouncilRoster\n"))
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != applyTable {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, applyTable)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 1 {
		t.Errorf("%d POSTs, want 1", n)
	}
	root := newCouncilCmd(&cliEnv{})
	roster := councilSub(root, "roster")
	if roster == nil {
		t.Fatal("council has no roster subcommand")
	}
	alias := councilSub(roster, "apply")
	if alias == nil {
		t.Fatal("council roster has no apply subcommand")
	}
	if alias == councilSub(root, "apply") {
		t.Error("council roster apply is council apply")
	}
	if alias == councilSub(councilSub(root, "role"), "apply") {
		t.Error("council roster apply is council role apply")
	}
}

// TestCouncilRosterHelp pins every roster subcommand's texts and flags.
func TestCouncilRosterHelp(t *testing.T) {
	noBotMode(t)
	root := newCouncilCmd(&cliEnv{})
	roster := councilSub(root, "roster")
	if roster == nil {
		t.Fatal("council has no roster subcommand")
	}
	names := make([]string, 0, len(roster.Commands()))
	for _, c := range roster.Commands() {
		names = append(names, c.Name())
	}
	if got, want := strings.Join(names, ", "), "add, apply, delete, get, list, remove, use"; got != want {
		t.Errorf("council roster subcommands %q, want %q", got, want)
	}
	texts := []struct{ name, use, short, long, example string }{
		{"list", "list --bot <bot>", "List a bot's councils: the premade ones and its own",
			"List the councils a bot's chats can ask: the premade ones first, then the bot's own by name.\n" +
				"DEFAULT marks the council a new chat asks. STATUS is ok, unavailable (the reasons follow on\n" +
				"stderr) or not checked. --json prints the councils as the platform sent them.",
			"  tnctl council roster list --bot scout"},
		{"get", "get <name> --bot <bot>", "Show one council: its lead, members, reviewer and status",
			"Show one of a bot's councils: its status and budget, then one row per member: the lead, the\n" +
				"members in order and the reviewer, with each one's scope, version (pinned when the council asks\n" +
				"for that version), model and status. A member that was archived or deleted shows as gone.\n" +
				"\n" +
				"-o json prints the council as the platform sent it. -o yaml prints its council file as the\n" +
				"platform exports it, to edit and apply with tnctl council apply -f; a premade council's file\n" +
				"needs a new metadata.name, spec.scope: bot and spec.bot: <bot>, and no spec.version, before it\n" +
				"can be applied.",
			"  tnctl council roster get ops --bot scout\n" +
				"  tnctl council roster get ops-scout --bot scout -o yaml > ops-scout.yaml"},
		{"use", "use <name> --bot <bot>", "Make a council the bot's default: the one a new chat asks",
			"Make a council the bot's default. A new chat asks the default council unless it names another\n" +
				"(tnctl chat --roster). Only the bot's creator or a teamspace admin may change it (exit 6\n" +
				"otherwise). -o json|yaml prints the bot's council as the platform answers.",
			"  tnctl council roster use ops --bot scout"},
		{"add", "add <name> <member> --bot <bot>", "Add a member to a council",
			"Add a member to one of a bot's councils, after its members. <member> is a slug such as triage, or\n" +
				"<scope>/<slug> (scope bot, teamspace or template) when more than one member has that slug; tnctl\n" +
				"council role list --bot <bot> --templates lists them.\n" +
				"\n" +
				"A premade council cannot be changed: --as <new-name> saves a copy with the change, and when the\n" +
				"premade council was the bot's default, the copy becomes the default. On one of the bot's own\n" +
				"councils, --as saves a copy and leaves the council as it was.\n" +
				"\n" +
				"A council has a lead, 1 to 5 members and, when the Coder is a member, a reviewer: the platform\n" +
				"checks the changed council. -o json|yaml prints the council as the platform answers.\n" +
				"\n" +
				"Exit codes: 0 done, or nothing to change; 1 the council names a member that was archived or\n" +
				"deleted; 2 wrong arguments, an unknown council or member, a slug more than one member has, a\n" +
				"premade council without --as, or an --as name that is taken; 3 no bot of that name; 5 the\n" +
				"platform refused the changed council; 6 not allowed; 8 someone changed the council meanwhile:\n" +
				"run it again.",
			"  tnctl council roster add ops-scout triage --bot scout\n" +
				"  tnctl council roster add standard stream-watcher --bot scout --as scout-council"},
		{"remove", "remove <name> <member> --bot <bot>", "Remove a member from a council",
			"Remove a member from one of a bot's councils. <member> is the member's slug, such as triage, or\n" +
				"<scope>/<slug>. The lead and the reviewer are not members: the reviewer is changed in the web\n" +
				"chat's Council tab or in YAML (tnctl council apply), the lead in YAML for now.\n" +
				"\n" +
				"A premade council cannot be changed: --as <new-name> saves a copy with the change, and when the\n" +
				"premade council was the bot's default, the copy becomes the default. On one of the bot's own\n" +
				"councils, --as saves a copy and leaves the council as it was.\n" +
				"\n" +
				"A council keeps at least one member: the platform checks the changed council. -o json|yaml\n" +
				"prints the council as the platform answers.\n" +
				"\n" +
				"Exit codes: 0 done; 1 the council names another member that was archived or deleted; 2 wrong\n" +
				"arguments, an unknown council, a member that is not in it, a premade council without --as, or an\n" +
				"--as name that is taken; 3 no bot of that name; 5 the platform refused the changed council; 6 not\n" +
				"allowed; 8 someone changed the council meanwhile: run it again.",
			"  tnctl council roster remove ops-scout triage --bot scout"},
		{"delete", "delete <name> --bot <bot>", "Delete one of a bot's own councils",
			"Delete one of a bot's own councils. Chats that asked it go back to the bot's default council.\n" +
				"The default council cannot be deleted: make another one the default first (tnctl council roster\n" +
				"use). Premade councils cannot be deleted. tnctl asks nothing: to keep a copy, save it first with\n" +
				"tnctl council roster get <name> --bot <bot> -o yaml > <name>.yaml; tnctl council apply -f\n" +
				"<name>.yaml makes it again.",
			"  tnctl council roster delete ops-scout --bot scout"},
	}
	for _, c := range texts {
		sub := councilSub(roster, c.name)
		if sub == nil {
			t.Errorf("council roster has no %s subcommand", c.name)
			continue
		}
		if sub.Use != c.use {
			t.Errorf("%s Use %q, want %q", c.name, sub.Use, c.use)
		}
		if sub.Short != c.short {
			t.Errorf("%s Short %q, want %q", c.name, sub.Short, c.short)
		}
		if sub.Long != c.long {
			t.Errorf("%s Long:\n%s", c.name, sub.Long)
		}
		if sub.Example != c.example {
			t.Errorf("%s Example %q, want %q", c.name, sub.Example, c.example)
		}
		if sub.Flags().Lookup("yes") != nil {
			t.Errorf("%s has --yes", c.name)
		}
	}
	flagText := map[string]map[string]string{
		"list":   {"bot": "the TeknoirBot whose councils to list", "json": "print the councils as the platform sent them, as JSON"},
		"get":    {"bot": "the TeknoirBot whose council to show"},
		"use":    {"bot": "the TeknoirBot whose default council to set"},
		"add":    {"bot": "the TeknoirBot whose council to change", "scope": "the member's scope when more than one member has its slug: bot, teamspace or template", "as": "save the changed council as a new council with this name (a premade council needs it)"},
		"remove": {"bot": "the TeknoirBot whose council to change", "scope": "the member's scope when more than one member has its slug: bot, teamspace or template", "as": "save the changed council as a new council with this name (a premade council needs it)"},
		"delete": {"bot": "the TeknoirBot whose council to delete"},
	}
	for name, flags := range flagText {
		sub := councilSub(roster, name)
		if sub == nil {
			continue
		}
		for flag, want := range flags {
			f := sub.Flags().Lookup(flag)
			if f == nil {
				t.Errorf("%s has no --%s", name, flag)
				continue
			}
			if f.Usage != want {
				t.Errorf("%s --%s usage %q, want %q", name, flag, f.Usage, want)
			}
		}
	}
}
