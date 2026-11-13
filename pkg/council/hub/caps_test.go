package hub

// Tests of the proto-2 handshake and of the snapshot types: the caps headers a K1 carries, the
// rules that refuse a value, and the snapshot a turn's council arrives in.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// workTurn answers every K1 with one turn, and records the requests as the bot API does.
func workTurn(t *testing.T) *botAPI {
	t.Helper()
	return newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		switch r.URL.Path {
		case "/council/work":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"turn":{"id":"trn_1","claimId":"clm_1","kind":"ask","prompt":"hi"}}`)
		case "/council/turns/trn_1/finish":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// headerOf is the single value of a header, and whether it was sent at all. A header sent as several
// lines fails the test: the hub (Node) would read them joined with ", ", not with ",".
func headerOf(t *testing.T, r req, name string) (string, bool) {
	t.Helper()
	v, ok := r.header[name]
	if !ok {
		return "", false
	}
	if len(v) != 1 {
		t.Errorf("%s was sent as %d header lines %q, want exactly one", name, len(v), v)
	}
	return strings.Join(v, ","), true
}

func TestCapsHeadersOnNext(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if err := c.SetCaps(Caps{
		Proto:  2,
		Kit:    "0.2.0",
		Tools:  []string{"whoami", "list_robots", "inspect_pod"},
		Skills: []string{"teknoir-platform", "teknoir-robots"},
	}); err != nil {
		t.Fatalf("SetCaps: %v", err)
	}
	if _, err := c.Next(context.Background(), 1); err != nil {
		t.Fatalf("Next: %v", err)
	}
	r := b.requests()[0]
	for name, want := range map[string]string{
		HeaderProto:  "2",
		HeaderKit:    "0.2.0",
		HeaderTools:  "inspect_pod,list_robots,whoami", // sorted
		HeaderSkills: "teknoir-platform,teknoir-robots",
	} {
		if got, ok := headerOf(t, r, name); !ok || got != want {
			t.Errorf("%s = %q (sent %v), want %q", name, got, ok, want)
		}
	}
	if got := r.header.Get("X-Teknoir-Council"); got != "tnctl/9.9.9" {
		t.Errorf("X-Teknoir-Council = %q", got)
	}
}

func TestCapsEmptySkillsSendsEmptyHeader(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if err := c.SetCaps(Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}}); err != nil {
		t.Fatalf("SetCaps: %v", err)
	}
	if _, err := c.Next(context.Background(), 1); err != nil {
		t.Fatalf("Next: %v", err)
	}
	r := b.requests()[0]
	v, ok := r.header[HeaderSkills]
	if !ok {
		t.Fatalf("%s was not sent at all; an empty list sends an empty header", HeaderSkills)
	}
	if len(v) != 1 || v[0] != "" {
		t.Errorf("%s = %q, want one empty value", HeaderSkills, v)
	}
}

func TestNoCapsNoHeaders(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if _, err := c.Next(context.Background(), 1); err != nil {
		t.Fatalf("Next: %v", err)
	}
	r := b.requests()[0]
	for _, name := range []string{HeaderProto, HeaderKit, HeaderTools, HeaderSkills} {
		if _, ok := r.header[name]; ok {
			t.Errorf("%s was sent with no caps set", name)
		}
	}
}

// Only K1 carries the capability headers: K2 and K3 do not.
func TestCapsOnlyOnWork(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if err := c.SetCaps(Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}}); err != nil {
		t.Fatalf("SetCaps: %v", err)
	}
	turn, err := c.Next(context.Background(), 1)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	p := c.Poster(turn, nil)
	p.Emit(EventTurnStart, map[string]any{"mode": "auto"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*1e9)
	defer cancel()
	if err := p.Finish(ctx, "done", "ok", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	reqs := b.requests()
	if len(reqs) < 3 {
		t.Fatalf("%d requests, want the K1, a K2 and a K3", len(reqs))
	}
	for _, r := range reqs[1:] {
		for _, name := range []string{HeaderProto, HeaderKit, HeaderTools, HeaderSkills} {
			if _, ok := r.header[name]; ok {
				t.Errorf("%s %s carries %s", r.method, r.path, name)
			}
		}
	}
}

// capsCase is one SetCaps that must fail, and whether the caps before it stay in place.
type capsCase struct {
	name string
	caps Caps
}

// goodCaps is a valid set of caps every case starts from.
func goodCaps() Caps {
	return Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"list_robots", "whoami"}, Skills: []string{"teknoir-platform"}}
}

func TestSetCapsRules(t *testing.T) {
	// 129 short, distinct, valid names: 129*4+128 = 644 bytes joined, far under 4096, so only the
	// count limit (128) is broken.
	names129 := make([]string, 0, 129)
	for i := 0; i < 129; i++ {
		names129 = append(names129, fmt.Sprintf("t%03d", i))
	}
	skills65 := make([]string, 0, 65)
	for i := 0; i < 65; i++ {
		skills65 = append(skills65, fmt.Sprintf("s%02d", i))
	}
	for _, tc := range []capsCase{
		{"proto 1", Caps{Proto: 1, Kit: "0.2.0", Tools: []string{"whoami"}}},
		{"proto 3", Caps{Proto: 3, Kit: "0.2.0", Tools: []string{"whoami"}}},
		{"an empty kit", Caps{Proto: 2, Kit: "", Tools: []string{"whoami"}}},
		{"a bad kit", Caps{Proto: 2, Kit: "0.2 0", Tools: []string{"whoami"}}},
		{"a kit over 40", Caps{Proto: 2, Kit: strings.Repeat("1", 41), Tools: []string{"whoami"}}},
		{"no tools", Caps{Proto: 2, Kit: "0.2.0"}},
		{"129 tools", Caps{Proto: 2, Kit: "0.2.0", Tools: names129}},
		{"a bad tool name", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"Whoami"}}},
		{"a tool name with a dash", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"list-robots"}}},
		{"a tool name too long", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"t" + strings.Repeat("o", 64)}}},
		{"a 4097-byte tools header", Caps{Proto: 2, Kit: "0.2.0", Tools: toolsOfBytes("ab")}},
		{"65 skills", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: skills65}},
		{"a bad skill name", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: []string{"teknoir_platform"}}},
		{"a 2049-byte skills header", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: skillsOfBytes(33)}},
		{"a duplicate tool", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami", "whoami"}}},
		{"a duplicate skill", Caps{Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: []string{"a", "a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := workTurn(t)
			c := newClient(t, b.server.URL, keyFile(t))
			if err := c.SetCaps(goodCaps()); err != nil {
				t.Fatalf("the valid caps were refused: %v", err)
			}
			if err := c.SetCaps(tc.caps); err == nil {
				t.Fatal("SetCaps accepted the caps")
			}
			// The previous caps stay: the headers are the valid ones.
			if _, err := c.Next(context.Background(), 1); err != nil {
				t.Fatalf("Next: %v", err)
			}
			r := b.requests()[0]
			if got := r.header.Get(HeaderTools); got != "list_robots,whoami" {
				t.Errorf("%s = %q, want the caps set before the refused SetCaps", HeaderTools, got)
			}
			if got := r.header.Get(HeaderKit); got != "0.2.0" {
				t.Errorf("%s = %q, want the caps set before the refused SetCaps", HeaderKit, got)
			}
		})
	}
}

// A refused SetCaps leaves a client without caps without caps: nothing is sent.
func TestSetCapsFirstRefusalSendsNothing(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if err := c.SetCaps(Caps{Proto: 2, Kit: "bad kit", Tools: []string{"whoami"}}); err == nil {
		t.Fatal("SetCaps accepted a bad kit")
	}
	if _, err := c.Next(context.Background(), 1); err != nil {
		t.Fatalf("Next: %v", err)
	}
	for _, name := range []string{HeaderProto, HeaderKit, HeaderTools, HeaderSkills} {
		if _, ok := b.requests()[0].header[name]; ok {
			t.Errorf("%s was sent after a refused SetCaps", name)
		}
	}
}

// SetCaps may race with Next: every K1 carries either the old caps or the new ones, never a mix.
func TestSetCapsConcurrentWithNext(t *testing.T) {
	b := workTurn(t)
	c := newClient(t, b.server.URL, keyFile(t))
	if err := c.SetCaps(goodCaps()); err != nil {
		t.Fatalf("SetCaps: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		other := Caps{Proto: 2, Kit: "1.0.0", Tools: []string{"inspect_pod"}, Skills: []string{"teknoir-debug"}}
		for i := 0; i < 50; i++ {
			if err := c.SetCaps(other); err != nil {
				t.Errorf("SetCaps: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		if _, err := c.Next(context.Background(), 1); err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	<-done
	for _, r := range b.requests() {
		kit, tools := r.header.Get(HeaderKit), r.header.Get(HeaderTools)
		if (kit == "0.2.0") != (tools == "list_robots,whoami") {
			t.Fatalf("a K1 mixes two caps: kit %q, tools %q", kit, tools)
		}
	}
}

// toolsOfBytes is 63 valid tool names of 64 bytes plus last: the joined header is 4095+len(last)
// bytes, so "a" is exactly 4096 and "ab" exactly 4097.
func toolsOfBytes(last string) []string {
	out := make([]string, 0, 64)
	for i := 0; i < 63; i++ {
		out = append(out, fmt.Sprintf("t%02d", i)+strings.Repeat("x", 61))
	}
	return append(out, last)
}

// skillsOfBytes is 63 valid skill names of 31 bytes plus one of lastLen: the joined header is
// 2016+lastLen bytes, so 32 is exactly 2048 and 33 exactly 2049.
func skillsOfBytes(lastLen int) []string {
	out := make([]string, 0, 64)
	for i := 0; i < 63; i++ {
		out = append(out, fmt.Sprintf("s%02d", i)+strings.Repeat("x", 28))
	}
	return append(out, "s63"+strings.Repeat("x", lastLen-3))
}

// TestSetCapsAtTheLimits accepts every value at its upper limit, and refuses each one just past it:
// an off-by-one in any limit, or a limit lowered below what the daemon itself announces, fails here.
func TestSetCapsAtTheLimits(t *testing.T) {
	names128 := make([]string, 0, 128)
	for i := 0; i < 128; i++ {
		names128 = append(names128, fmt.Sprintf("t%03d", i))
	}
	skills64 := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		skills64 = append(skills64, fmt.Sprintf("s%02d", i))
	}
	atTools, atSkills := toolsOfBytes("a"), skillsOfBytes(32)
	// The fixtures must sit exactly on the limits, or the cases below prove nothing.
	if n := len(strings.Join(atTools, ",")); n != maxToolsHeader {
		t.Fatalf("the tools fixture is %d bytes, want %d", n, maxToolsHeader)
	}
	if n := len(strings.Join(atSkills, ",")); n != maxSkillsHeader {
		t.Fatalf("the skills fixture is %d bytes, want %d", n, maxSkillsHeader)
	}

	for name, caps := range map[string]Caps{
		"128 tools":                 {Proto: 2, Kit: "0.2.0", Tools: names128},
		"a 4096-byte tools header":  {Proto: 2, Kit: "0.2.0", Tools: atTools},
		"64 skills":                 {Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: skills64},
		"a 2048-byte skills header": {Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: atSkills},
		"a 40-character kit":        {Proto: 2, Kit: strings.Repeat("1", 40), Tools: []string{"whoami"}},
		"64-character names": {
			Proto: 2, Kit: "0.2.0",
			Tools:  []string{"t" + strings.Repeat("o", 63)},
			Skills: []string{"s" + strings.Repeat("o", 63)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := new(Client).SetCaps(caps); err != nil {
				t.Fatalf("SetCaps refused caps at the limit: %v", err)
			}
		})
	}

	for name, caps := range map[string]Caps{
		"129 tools":                 {Proto: 2, Kit: "0.2.0", Tools: append(append([]string{}, names128...), "t128")},
		"a 4097-byte tools header":  {Proto: 2, Kit: "0.2.0", Tools: toolsOfBytes("ab")},
		"65 skills":                 {Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: append(append([]string{}, skills64...), "s64")},
		"a 2049-byte skills header": {Proto: 2, Kit: "0.2.0", Tools: []string{"whoami"}, Skills: skillsOfBytes(33)},
		"a 41-character kit":        {Proto: 2, Kit: strings.Repeat("1", 41), Tools: []string{"whoami"}},
		"a 65-character tool name":  {Proto: 2, Kit: "0.2.0", Tools: []string{"t" + strings.Repeat("o", 64)}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := new(Client).SetCaps(caps); err == nil {
				t.Error("SetCaps accepted caps past the limit")
			}
		})
	}
}

// ---------- the snapshot ----------

// specExample is the Standard snapshot of the platform's spec (3.4) at level read, with the
// example's "…" placeholders filled with real values. Its roster budget is 150000 where the spec's
// example has null, so the budget's own keys are decoded too.
const specExample = `{
  "proto": 2,
  "hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "roster": {"id": "tpl:standard", "name": "standard", "displayName": "Standard", "version": 1},
  "level": "read", "thorough": false, "unattended": false, "budget": {"maxTokens": 150000},
  "lead": {"id": "tpl:captain", "version": 1, "slug": "captain", "name": "Captain", "persona": null,
    "description": "leads the council: routes each question, convenes members and writes the answer.",
    "instructions": "", "kind": "lead", "runtime": "inproc", "model": "sonnet", "effort": "high", "level": "read",
    "groups": ["core_read", "metrics", "mqtt", "inspect", "results"],
    "tools": ["get_config", "get_device", "get_overview", "get_problems", "get_robot", "list_devices", "list_fleets",
      "list_kinds", "list_robot_types", "list_robots", "list_teamspaces", "mqtt_get_sample", "mqtt_read",
      "mqtt_sample", "prom_query", "whoami"],
    "results": true,
    "skills": ["teknoir-platform", "teknoir-robots", "teknoir-config", "teknoir-debug", "teknoir-mqtt-streams",
      "teknoir-k3s-apps", "teknoir-council"],
    "budget": null},
  "members": [
    {"id": "tpl:fleet", "version": 1, "slug": "fleet", "name": "Fleet", "persona": "the fact finder",
     "description": "finds facts on the platform with read tools (robots, devices, problems, configuration, metrics, MQTT samples, pods and logs).",
     "instructions": "Answer the task with facts from the read tools: never guess a value.",
     "kind": "member", "runtime": "inproc", "model": "haiku", "effort": "medium", "level": "read",
     "groups": ["core_read", "metrics", "mqtt", "inspect", "results"],
     "tools": ["get_config", "get_device", "get_overview", "get_problems", "get_robot", "list_devices", "list_fleets",
      "list_kinds", "list_robot_types", "list_robots", "list_teamspaces", "mqtt_get_sample", "mqtt_read",
      "mqtt_sample", "prom_query", "whoami"], "results": true,
     "skills": ["teknoir-platform", "teknoir-robots", "teknoir-config", "teknoir-debug", "teknoir-mqtt-streams",
      "teknoir-k3s-apps", "teknoir-council"],
     "budget": {"maxToolRounds": 10, "maxTokens": 120000}},
    {"id": "tpl:coder", "version": 1, "slug": "coder", "name": "Coder", "persona": null,
     "description": "writes and runs code on the bot's computer (scripts, stream processors, small HTML dashboards, apps in the guest namespace \"apps\", image builds to the bot's registry) and can run tnctl as the bot.",
     "instructions": "", "kind": "member", "runtime": "coder", "model": "sonnet", "effort": "high", "level": "read",
     "groups": [], "tools": [], "results": false, "skills": [], "budget": null}
  ],
  "reviewer": {"id": "tpl:critic", "version": 1, "slug": "critic", "name": "Critic", "persona": null,
    "description": "checks the members' reports before you answer.", "instructions": "", "kind": "reviewer",
    "runtime": "inproc", "model": "sonnet", "effort": "medium", "level": "none", "groups": [], "tools": [],
    "results": false, "skills": [], "budget": null}
}`

// decodeSnapshot is the snapshot of a JSON text.
func decodeSnapshot(t *testing.T, raw string) *Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal the snapshot: %v", err)
	}
	return &s
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := decodeSnapshot(t, specExample)
	if s.Proto != 2 || s.Hash == "" || s.Level != "read" || s.Thorough || s.Unattended {
		t.Errorf("the header fields: %+v", s)
	}
	if s.Roster == nil || s.Roster.ID != "tpl:standard" || s.Roster.Name != "standard" ||
		s.Roster.DisplayName != "Standard" || s.Roster.Version != 1 {
		t.Errorf("roster = %+v", s.Roster)
	}
	if s.Budget == nil || s.Budget.MaxTokens != 150000 {
		t.Errorf("budget = %+v", s.Budget)
	}
	if s.Lead == nil || s.Lead.Slug != "captain" || s.Lead.Kind != "lead" || s.Lead.Runtime != "inproc" ||
		s.Lead.Model != "sonnet" || s.Lead.Effort != "high" || s.Lead.Level != "read" || !s.Lead.Results {
		t.Errorf("lead = %+v", s.Lead)
	}
	if s.Lead.ID != "tpl:captain" || s.Lead.Version != 1 || s.Lead.Name != "Captain" {
		t.Errorf("the lead's id, version or name = %q/%d/%q", s.Lead.ID, s.Lead.Version, s.Lead.Name)
	}
	if s.Lead.Instructions != "" {
		t.Errorf("the lead's instructions = %q, want none", s.Lead.Instructions)
	}
	if n := len(s.Lead.Skills); n != 7 || s.Lead.Skills[0] != "teknoir-platform" || s.Lead.Skills[6] != "teknoir-council" {
		t.Errorf("the lead's skills = %v, want the seven in kit order", s.Lead.Skills)
	}
	if s.Lead.Description != "leads the council: routes each question, convenes members and writes the answer." {
		t.Errorf("the lead's description = %q", s.Lead.Description)
	}
	if got, want := strings.Join(s.Lead.Groups, ","), "core_read,metrics,mqtt,inspect,results"; got != want {
		t.Errorf("the lead's groups = %q, want %q", got, want)
	}
	if s.Lead.Persona != nil {
		t.Errorf("the lead's persona = %v, want nil", *s.Lead.Persona)
	}
	if got := len(s.Lead.Tools); got != 16 {
		t.Errorf("the lead has %d tools, want 16", got)
	}
	if len(s.Members) != 2 {
		t.Fatalf("%d members, want 2", len(s.Members))
	}
	fleet := s.Members[0]
	if fleet.Slug != "fleet" || fleet.Persona == nil || *fleet.Persona != "the fact finder" ||
		fleet.Budget == nil || fleet.Budget.MaxToolRounds != 10 || fleet.Budget.MaxTokens != 120000 {
		t.Errorf("fleet = %+v", fleet)
	}
	if fleet.ID != "tpl:fleet" || fleet.Version != 1 || fleet.Name != "Fleet" || fleet.Kind != "member" ||
		fleet.Model != "haiku" || fleet.Effort != "medium" {
		t.Errorf("fleet's id, version, name, kind, model or effort: %+v", fleet)
	}
	if fleet.Instructions != "Answer the task with facts from the read tools: never guess a value." {
		t.Errorf("fleet's instructions = %q", fleet.Instructions)
	}
	if !strings.HasPrefix(fleet.Description, "finds facts on the platform with read tools") {
		t.Errorf("fleet's description = %q", fleet.Description)
	}
	if len(fleet.Tools) != 16 || fleet.Tools[0] != "get_config" || len(fleet.Skills) != 7 || !fleet.Results {
		t.Errorf("fleet's tools, skills or results: %d tools, %d skills, results %v", len(fleet.Tools), len(fleet.Skills), fleet.Results)
	}
	coder := s.Members[1]
	if coder.Slug != "coder" || coder.Runtime != "coder" || coder.Budget != nil || len(coder.Groups) != 0 {
		t.Errorf("coder = %+v", coder)
	}
	if coder.ID != "tpl:coder" || coder.Version != 1 || coder.Kind != "member" || coder.Level != "read" || coder.Results || coder.Instructions != "" {
		t.Errorf("coder's fields: %+v", coder)
	}
	if s.Reviewer == nil || s.Reviewer.Slug != "critic" || s.Reviewer.Kind != "reviewer" || s.Reviewer.Level != "none" {
		t.Errorf("reviewer = %+v", s.Reviewer)
	}

	// The round trip keeps every key of the spec's example with its value: a wrong JSON tag drops or
	// renames a field, which the struct-to-struct comparison below cannot see.
	var want, got any
	if err := json.Unmarshal([]byte(specExample), &want); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal the round trip: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("the round trip lost or renamed a field:\n%s", raw)
	}
	// The boolean tags: the example carries both false, which a wrong tag also gives.
	flags := decodeSnapshot(t, strings.Replace(specExample, `"thorough": false, "unattended": false`, `"thorough": true, "unattended": true`, 1))
	if !flags.Thorough || !flags.Unattended {
		t.Errorf("thorough and unattended = %v, %v, want both true", flags.Thorough, flags.Unattended)
	}
}

// Unknown keys are ignored, at the top and inside a role: a later platform adds fields the daemon
// does not know, and a proto bump is what changes what the known ones mean.
func TestSnapshotUnknownFieldsIgnored(t *testing.T) {
	raw := strings.Replace(specExample, `"proto": 2,`, `"proto": 2, "futureTop": {"a": [1, 2]},`, 1)
	raw = strings.Replace(raw, `"slug": "captain",`, `"slug": "captain", "futureField": "x",`, 1)
	s := decodeSnapshot(t, raw)
	if s.Proto != 2 || s.Lead == nil || s.Lead.Slug != "captain" {
		t.Errorf("the known fields came out wrong: %+v", s)
	}
}

// A turn without a council has none.
func TestWorkTurnCouncilAbsent(t *testing.T) {
	var turn WorkTurn
	if err := json.Unmarshal([]byte(`{"id":"trn_1","kind":"ask"}`), &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Council != nil {
		t.Errorf("council = %+v, want nil", turn.Council)
	}
	// And a turn with one decodes it.
	turn = WorkTurn{}
	if err := json.Unmarshal([]byte(`{"id":"trn_1","kind":"ask","council":`+specExample+`,"options":{"mode":"council","thorough":true}}`), &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Council == nil || turn.Council.Lead == nil || turn.Council.Lead.Slug != "captain" {
		t.Errorf("council = %+v", turn.Council)
	}
	if !turn.Options.Thorough || turn.Options.Mode != "council" {
		t.Errorf("options = %+v", turn.Options)
	}
}
