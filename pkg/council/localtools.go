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
	toolConvene   = "convene_council"
	toolReadSkill = "read_skill"
)

// The council's members a task may name, and the limits of a convene_council input.
const (
	memberFleet = "fleet"
	memberCoder = "coder"

	maxConveneTasks = 3
	maxConveneTask  = 4000
	maxConveneWhy   = 1000

	// tasksKey is the input's single key.
	tasksKey = "tasks"
)

// conveneMembers are the members a task may name.
var conveneMembers = []string{memberFleet, memberCoder}

// conveneTool is the convene_council tool.
func conveneTool() anthropic.ToolUnionParam {
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
							"member": map[string]any{"type": "string", "enum": conveneMembers},
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

// readSkillTool is the read_skill tool.
func readSkillTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        toolReadSkill,
		Description: anthropic.String("Read one of the Teknoir guides listed in your instructions."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"name": map[string]any{"type": "string", "enum": skillNames},
			},
			Required: []string{"name"},
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

// parseConvene validates a convene_council input: a JSON object {tasks: [...]}, 1 to 3 tasks, member
// "fleet" or "coder", at most one "coder", task 1 to 4000 characters (runes) after trimming, why at
// most 1000 characters; at level none no "fleet" task. The error is a short sentence for the model.
func parseConvene(input json.RawMessage, level string) ([]conveneTask, error) {
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
		switch it.Member {
		case memberFleet:
			// Fail closed: "" and any level the platform may add later count as none.
			if level != "read" && level != "creator" {
				return nil, errors.New("at access level none only a coder task may be convened")
			}
		case memberCoder:
			coders++
		case "":
			return nil, errors.New(`a task has no member (fleet or coder)`)
		default:
			return nil, fmt.Errorf("%q is not a member (fleet or coder)", cutUTF8(it.Member, 64, "…"))
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
