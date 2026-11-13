package council

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// The council's own tools, which no MCP server provides.
const (
	toolConvene    = "convene_council"
	toolReadSkill  = "read_skill"
	toolReadResult = "read_result"
)

// The Standard council's Coder slug, and the limits of a convene_council input.
const (
	// memberCoder is the slug of the Coder in the Standard council (tpl:coder). Only tests name it: the
	// legacy path reads its members from legacy_standard.json, and a council may have no Coder or more
	// than one (find the Coder by Runtime == runtimeCoder, never by this slug).
	memberCoder = "coder"

	maxConveneTasks = 3
	maxConveneTask  = 4000
	maxConveneWhy   = 1000

	// tasksKey is the input's single key.
	tasksKey = "tasks"
)

// orList is names as a sentence: "a", "a or b", "a, b or c".
func orList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// conveneTool is the convene_council tool, naming the members of this turn's council.
func conveneTool(enum []string) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        toolConvene,
		Description: anthropic.String("Hand the question to council members. Use it instead of answering when the question needs an investigation or code."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				tasksKey: map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"member": map[string]any{"type": "string", "enum": enum},
							"task":   map[string]any{"type": "string"},
							"why":    map[string]any{"type": "string"},
						},
						"required": []string{"member", "task", "why"},
					},
				},
			},
			Required: []string{tasksKey},
			ExtraFields: map[string]any{
				"additionalProperties": false,
			},
		},
		Strict: anthropic.Bool(true),
	}}
}

// readSkillTool is the read_skill tool, whose enum is the role's own guides. A guide is read a page
// at a time: offset 0 first, then the offset the page's [more: ...] line gives.
func readSkillTool(names []string) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        toolReadSkill,
		Description: anthropic.String("Read one of the Teknoir guides listed in your instructions: pass offset 0 first, then the offset a [more: ...] line gives."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"name":   map[string]any{"type": "string", "enum": names},
				"offset": map[string]any{"type": "integer"},
			},
			Required: []string{"name", "offset"},
			ExtraFields: map[string]any{
				"additionalProperties": false,
			},
		},
		Strict: anthropic.Bool(true),
	}}
}

// withLocal returns a new list: a copy of forList whose last element's CacheControl is cleared
// (Tools.For marks it; model.params marks the request's last tool), followed by local. forList
// itself is not changed.
func withLocal(forList []anthropic.ToolUnionParam, local ...anthropic.ToolUnionParam) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(forList)+len(local))
	out = append(out, forList...)
	if n := len(out); n > 0 && out[n-1].OfTool != nil {
		tool := *out[n-1].OfTool
		tool.CacheControl = anthropic.CacheControlEphemeralParam{}
		out[n-1].OfTool = &tool
	}
	return append(out, local...)
}

// conveneTask is one task of a convene_council call.
type conveneTask struct {
	Member string
	Task   string
	Why    string
}

// parseConvene validates a convene_council input against this turn's council: a JSON object
// {tasks: [...]}, 1 to 3 tasks, each naming a member of the council, at most one of them the Coder,
// task 1 to 4000 characters (runes) after trimming, why at most 1000 characters; a data member is
// convened only at the level it runs at. The error is a short sentence for the model.
func parseConvene(input json.RawMessage, c *turnCouncil) ([]conveneTask, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil || raw == nil {
		return nil, errors.New("give {tasks: [...]}")
	}
	list, ok := raw[tasksKey]
	if !ok {
		return nil, errors.New("give {tasks: [...]}")
	}
	if len(raw) != 1 {
		return nil, errors.New("give only tasks")
	}
	var items []struct {
		Member string `json:"member"`
		Task   string `json:"task"`
		Why    string `json:"why"`
	}
	d := json.NewDecoder(bytes.NewReader(list))
	d.DisallowUnknownFields()
	if err := d.Decode(&items); err != nil {
		return nil, errors.New("tasks must be a list of {member, task, why}")
	}
	if len(items) < 1 {
		return nil, errors.New("tell at least one task")
	}
	if len(items) > maxConveneTasks {
		return nil, fmt.Errorf("at most %d tasks", maxConveneTasks)
	}
	out := make([]conveneTask, 0, len(items))
	coders := 0
	for _, it := range items {
		task := strings.TrimSpace(it.Task)
		if task == "" {
			return nil, errors.New("a task is empty")
		}
		if utf8.RuneCountInString(task) > maxConveneTask {
			return nil, fmt.Errorf("a task is longer than %d characters", maxConveneTask)
		}
		if utf8.RuneCountInString(it.Why) > maxConveneWhy {
			return nil, fmt.Errorf("a why is longer than %d characters", maxConveneWhy)
		}
		if it.Member == "" {
			return nil, errors.New(`a task has no member (` + orList(c.enum()) + `)`)
		}
		r := c.member(it.Member)
		if r == nil {
			return nil, fmt.Errorf("%q is not a member (%s)", cutUTF8(it.Member, 64, "…"), orList(c.enum()))
		}
		switch {
		case r.Runtime == runtimeCoder:
			coders++
		case r.dataRole() && !r.convenable():
			conv := c.convenableEnum()
			switch len(conv) {
			case 1:
				return nil, errors.New("at access level none only a " + conv[0] + " task may be convened")
			case 0:
				return nil, errors.New("at access level none no member may be convened")
			}
			return nil, errors.New("at access level none only " + orList(conv) + " tasks may be convened")
		}
		out = append(out, conveneTask{Member: it.Member, Task: task, Why: it.Why})
	}
	if coders > 1 {
		return nil, errors.New("at most one coder task")
	}
	return out, nil
}

// codeAsked matches a prompt that asks for code.
var codeAskedRe = regexp.MustCompile(`(?i)\b(code|script|program|build|dashboard|app|deploy|python|html)\b`)

// codeAsked reports whether a prompt asks for code.
func codeAsked(prompt string) bool {
	return codeAskedRe.MatchString(prompt)
}

// changeAsked matches a prompt that asks for code or for a change of the platform. Unlike
// codeAsked it has no "app": "why is the app on r2 slow" is a question, not a request.
var changeAskedRe = regexp.MustCompile(`(?i)\b(code|script|program|build|deploy|install|write|create|make|fix|change|update|set|configure|add|remove|edit|patch|dashboard|python|html)\b`)

// changeAsked reports whether the person's prompt asks for code or a change. It gates a Coder
// task the Captain convenes after it has read platform data: the person must have asked, not a
// log, a message, a configuration or a tool result.
func changeAsked(prompt string) bool {
	return changeAskedRe.MatchString(prompt)
}
