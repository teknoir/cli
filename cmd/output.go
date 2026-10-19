package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// outputFlag is the global -o/--output.
var outputFlag string

// outputFormat returns table, json or yaml.
func outputFormat() (string, error) {
	switch strings.ToLower(outputFlag) {
	case "", "table", "text":
		return "table", nil
	case "json":
		return "json", nil
	case "yaml", "yml":
		return "yaml", nil
	}
	return "", usageErr("unknown output format %q: use table, json or yaml", outputFlag)
}

// writeStructured prints raw JSON data as indented JSON or as YAML. Data goes to stdout;
// progress and warnings never do.
func writeStructured(w io.Writer, format string, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	if format == "yaml" {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// marshalRaw is json.Marshal as a RawMessage.
func marshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	return b
}

// withFields adds top level fields to a JSON object (existing keys are replaced); a raw
// message that is not an object is returned unchanged. Warnings get merged by addWarnings.
func withFields(raw json.RawMessage, fields map[string]any) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		if len(bytes.TrimSpace(raw)) == 0 {
			obj = map[string]json.RawMessage{}
		} else {
			return raw
		}
	}
	for k, v := range fields {
		obj[k] = marshalRaw(v)
	}
	return marshalRaw(obj)
}

// addWarnings appends client side warnings to the "warnings" list of a write result.
func addWarnings(raw json.RawMessage, warnings []string) json.RawMessage {
	if len(warnings) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		obj = map[string]json.RawMessage{}
	}
	var existing []string
	if w, ok := obj["warnings"]; ok {
		_ = json.Unmarshal(w, &existing)
	}
	return withFields(marshalRaw(obj), map[string]any{"warnings": append(existing, warnings...)})
}
