package workflowrecipe

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// VerdictShape reads a verdict artifact against its until's schema
// (domain.Until.Schema): the problems that make it of another shape, in
// field order, or none when it fits. The verdict field is always required,
// as a string.
func VerdictShape(raw []byte, schema map[string]string) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return []string{"it is not a JSON object"}
	}
	want := maps.Clone(schema)
	if want == nil {
		want = map[string]string{}
	}
	want["verdict"] = "string"
	var problems []string
	for _, field := range slices.Sorted(maps.Keys(want)) {
		v, has := obj[field]
		if !has {
			problems = append(problems, field+" is missing")
			continue
		}
		if got := jsonType(v); got != want[field] {
			problems = append(problems, fmt.Sprintf("%s is %s, not %s", field, got, want[field]))
		}
	}
	return problems
}

// jsonType names a JSON value's type as a schema says it.
func jsonType(v json.RawMessage) string {
	var x any
	if err := json.Unmarshal(v, &x); err != nil {
		return "not JSON"
	}
	switch x.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "null"
}
