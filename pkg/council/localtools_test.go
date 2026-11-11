package council

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// toolJSON is one tool's marshalled JSON.
func toolJSON(t *testing.T, tool anthropic.ToolUnionParam) map[string]any {
	t.Helper()
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal the tool: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal the tool: %v", err)
	}
	return m
}

// schemaOf is a tool's input schema.
func schemaOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	schema, ok := m["input_schema"].(map[string]any)
	if !ok {
		t.Fatalf("input_schema = %v", m["input_schema"])
	}
	return schema
}

func TestConveneToolSchema(t *testing.T) {
	m := toolJSON(t, conveneTool())
	if m["name"] != toolConvene {
		t.Errorf("name = %v", m["name"])
	}
	if m["description"] != "Hand the question to council members. Use it instead of answering when the question needs an investigation or code." {
		t.Errorf("description = %v", m["description"])
	}
	if m["strict"] != true {
		t.Errorf("strict = %v, want true", m["strict"])
	}
	schema := schemaOf(t, m)
	if schema["type"] != "object" {
		t.Errorf("type = %v", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "tasks" {
		t.Errorf("required = %v, want [tasks]", schema["required"])
	}
	props, _ := schema["properties"].(map[string]any)
	tasks, _ := props["tasks"].(map[string]any)
	if tasks["type"] != "array" {
		t.Errorf("tasks = %v", tasks)
	}
	items, _ := tasks["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Errorf("items.additionalProperties = %v, want false", items["additionalProperties"])
	}
	if req, _ := items["required"].([]any); len(req) != 3 {
		t.Errorf("items.required = %v, want member, task, why", items["required"])
	}
	itemProps, _ := items["properties"].(map[string]any)
	member, _ := itemProps["member"].(map[string]any)
	enum, _ := member["enum"].([]any)
	if len(enum) != 2 || enum[0] != "fleet" || enum[1] != "coder" {
		t.Errorf("member enum = %v", member["enum"])
	}
	for _, name := range []string{"task", "why"} {
		if p, _ := itemProps[name].(map[string]any); p["type"] != "string" {
			t.Errorf("%s = %v, want a string", name, itemProps[name])
		}
	}
}

func TestReadSkillToolSchema(t *testing.T) {
	m := toolJSON(t, readSkillTool())
	if m["name"] != toolReadSkill {
		t.Errorf("name = %v", m["name"])
	}
	if m["strict"] != true {
		t.Errorf("strict = %v, want true", m["strict"])
	}
	schema := schemaOf(t, m)
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	props, _ := schema["properties"].(map[string]any)
	name, _ := props["name"].(map[string]any)
	enum, _ := name["enum"].([]any)
	if len(enum) != len(skillNames) {
		t.Errorf("name enum = %v, want %v", name["enum"], skillNames)
	}
	for i, want := range skillNames {
		if enum[i] != want {
			t.Errorf("enum[%d] = %v, want %s", i, enum[i], want)
		}
	}
}

func TestWithLocal(t *testing.T) {
	forList := []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{Name: "whoami", InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}}}},
		{OfTool: &anthropic.ToolParam{
			Name:         "robots",
			InputSchema:  anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
			CacheControl: anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m},
		}},
	}
	got := withLocal(forList, conveneTool(), readSkillTool())
	if len(got) != 4 {
		t.Fatalf("tools = %d, want 4", len(got))
	}
	want := []string{"whoami", "robots", toolConvene, toolReadSkill}
	for i, name := range want {
		if got[i].OfTool == nil || got[i].OfTool.Name != name {
			t.Errorf("tools[%d] = %v, want %s", i, got[i].OfTool, name)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), `"cache_control"`) {
		t.Errorf("the list with the local tools carries a cache breakpoint: %s", raw)
	}
	orig, _ := json.Marshal(forList)
	if n := strings.Count(string(orig), `"cache_control"`); n != 1 {
		t.Errorf("the original list has %d cache breakpoints, want 1 kept", n)
	}
}

// TestRequestHasThreeBreakpoints: the system block, the last tool and the conversation tail.
func TestRequestHasThreeBreakpoints(t *testing.T) {
	server, body := newTestProxy(t)
	forList := []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{Name: "whoami", InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}}}},
		{OfTool: &anthropic.ToolParam{
			Name:         "robots",
			InputSchema:  anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
			CacheControl: anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m},
		}},
	}
	streamOnce(t, server, captainRouteSystem(factsOf("read", testFacts("b", "ns"), "p"), "auto"),
		withLocal(forList, conveneTool(), readSkillTool()))
	req := body()
	if n := strings.Count(req, `"cache_control"`); n != 3 {
		t.Errorf("the request has %d cache breakpoints, want 3 (system, last tool, tail): %s", n, req)
	}
	if !strings.Contains(req, `"tool_choice":{"type":"auto"}`) {
		t.Errorf("the request has no tool_choice auto: %s", req)
	}
}

func TestParseConvene(t *testing.T) {
	valid := `{"tasks":[{"member":"fleet","task":"find the robot","why":"it is the question"}]}`
	got, err := parseConvene(json.RawMessage(valid), "read")
	if err != nil {
		t.Fatalf("a valid input was refused: %v", err)
	}
	if len(got) != 1 || got[0].Member != "fleet" || got[0].Task != "find the robot" || got[0].Why != "it is the question" {
		t.Errorf("tasks = %+v", got)
	}
	// Trimming, and the three-task limit.
	three := `{"tasks":[{"member":"fleet","task":" a ","why":""},{"member":"fleet","task":"b","why":""},{"member":"coder","task":"c","why":""}]}`
	got, err = parseConvene(json.RawMessage(three), "read")
	if err != nil {
		t.Fatalf("three tasks were refused: %v", err)
	}
	if got[0].Task != "a" || len(got) != 3 {
		t.Errorf("tasks = %+v, want the task trimmed", got)
	}
	// A coder task is the one a level-none bot may convene.
	if _, err := parseConvene(json.RawMessage(`{"tasks":[{"member":"coder","task":"c","why":""}]}`), "none"); err != nil {
		t.Errorf("a coder task was refused at level none: %v", err)
	}
	// A fleet task at level none: refused.
	if _, err := parseConvene(json.RawMessage(valid), "none"); err == nil {
		t.Error("a fleet task was allowed at level none")
	}
	for _, tc := range []struct {
		name, input, level string
	}{
		{"no tasks", `{"tasks":[]}`, "read"},
		{"null tasks", `{"tasks":null}`, "read"},
		{"four tasks", `{"tasks":[{"member":"fleet","task":"a","why":""},{"member":"fleet","task":"b","why":""},{"member":"fleet","task":"c","why":""},{"member":"fleet","task":"d","why":""}]}`, "read"},
		{"two coders", `{"tasks":[{"member":"coder","task":"a","why":""},{"member":"coder","task":"b","why":""}]}`, "read"},
		{"unknown member", `{"tasks":[{"member":"captain","task":"a","why":""}]}`, "read"},
		{"no member", `{"tasks":[{"task":"a","why":""}]}`, "read"},
		{"empty task", `{"tasks":[{"member":"fleet","task":"  ","why":""}]}`, "read"},
		{"long task", `{"tasks":[{"member":"fleet","task":"` + strings.Repeat("x", maxConveneTask+1) + `","why":""}]}`, "read"},
		{"long why", `{"tasks":[{"member":"fleet","task":"a","why":"` + strings.Repeat("x", maxConveneWhy+1) + `"}]}`, "read"},
		{"unknown key", `{"tasks":[{"member":"fleet","task":"a","why":""}],"other":1}`, "read"},
		{"unknown item key", `{"tasks":[{"member":"fleet","task":"a","why":"","extra":1}]}`, "read"},
		{"not an object", `[]`, "read"},
		{"a string", `"tasks"`, "read"},
		{"no tasks key", `{"task":[]}`, "read"},
		{"tasks not a list", `{"tasks":{}}`, "read"},
		{"wrong member type", `{"tasks":[{"member":1,"task":"a","why":""}]}`, "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConvene(json.RawMessage(tc.input), tc.level)
			if err == nil {
				t.Fatalf("accepted %s: %+v", tc.name, got)
			}
			if strings.Contains(err.Error(), strings.Repeat("x", 32)) {
				t.Errorf("the error carries the input: %v", err)
			}
		})
	}
}

func TestCodeAsked(t *testing.T) {
	for _, prompt := range []string{
		"write a script that samples the topic",
		"Build me a dashboard",
		"deploy an app on the bot",
		"why does my python job fail?",
		"give me HTML",
	} {
		if !codeAsked(prompt) {
			t.Errorf("codeAsked(%q) = false", prompt)
		}
	}
	for _, prompt := range []string{
		"which robots are offline?",
		"what is the capital of the teamspace",
		"show me the application list",
	} {
		if codeAsked(prompt) {
			t.Errorf("codeAsked(%q) = true", prompt)
		}
	}
}

func TestChangeAsked(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   bool
	}{
		{"why is the app on r2 slow", false},
		{"make a dashboard", true},
		{"what makes r2 slow", false},
		{"update the image", true},
		{"which robots are offline?", false},
		{"deploy it to the fleet", true},
		{"set the edge limit to 0", true},
	} {
		if got := changeAsked(tc.prompt); got != tc.want {
			t.Errorf("changeAsked(%q) = %v, want %v", tc.prompt, got, tc.want)
		}
	}
	// codeAsked keeps its "app": the same prompt is code for a route that read nothing.
	if !codeAsked("why is the app on r2 slow") {
		t.Error("codeAsked lost its app")
	}
}

func TestParseConveneFleetNeedsALevel(t *testing.T) {
	fleet := json.RawMessage(`{"tasks":[{"member":"fleet","task":"look","why":""}]}`)
	for _, level := range []string{"", "none", "admin", "Read"} {
		if _, err := parseConvene(fleet, level); err == nil {
			t.Errorf("level %q: a fleet task was accepted", level)
		}
	}
	for _, level := range []string{"read", "creator"} {
		if _, err := parseConvene(fleet, level); err != nil {
			t.Errorf("level %q: %v", level, err)
		}
	}
}
