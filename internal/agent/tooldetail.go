package agent

import "strings"

// detailKeys is the priority order in which a tool call's arguments are
// probed for the one value worth showing on an activity line: command
// for shell tools, file path for read/edit/write, pattern for searches,
// the name of whatever was delegated to, then the softer
// description-ish fields. The first non-empty string wins, so a grep
// call with both pattern and path shows the pattern.
//
// skill and subagent_type name the two kinds of call that are otherwise
// invisible: invoking a skill and spawning a subagent both put their
// whole meaning in an argument no other tool uses, so without them the
// line reads "Skill" and stops — a record that something was delegated
// to, with no way to learn to what. A subagent survived this by luck,
// carrying a description as well; a skill did not.
var detailKeys = []string{
	"command", "cmd",
	"file_path", "filePath", "path", "file",
	"pattern", "query", "url",
	"skill", "subagent_type",
	"description", "prompt",
}

// detailCap bounds a detail string at the source so activity storage
// stays tidy; the UI truncates again to the pane width.
const detailCap = 160

// toolDetail extracts a tool call's salient argument for the activity
// ticker: the first detailKeys hit, workdir-relative and collapsed to
// one line. Empty when the args carry nothing displayable — the caller
// falls back to the bare tool name.
func toolDetail(workdir string, args map[string]any) string {
	for _, k := range detailKeys {
		if v, ok := args[k].(string); ok {
			if d := collapseDetail(workdir, v); d != "" {
				return d
			}
		}
	}
	// Monitor's WebSocket form (docs: tools-reference#websocket-source)
	// takes its endpoint nested under "ws" instead of a top-level string
	// key, so the loop above never finds it and a watch opened on a
	// socket would otherwise show as a bare "Monitor" with nothing to
	// say what it's watching.
	if ws, ok := args["ws"].(map[string]any); ok {
		if v, ok := ws["url"].(string); ok {
			if d := collapseDetail(workdir, v); d != "" {
				return d
			}
		}
	}
	// Subagent delegation (e.g. invoke_subagent) nests its target under
	// "subagents" or "Subagents" list.
	for _, listKey := range []string{"subagents", "Subagents"} {
		if raw, ok := args[listKey]; ok {
			if subs, ok := raw.([]any); ok {
				for _, item := range subs {
					if subMap, ok := item.(map[string]any); ok {
						for _, subKey := range []string{
							"role", "Role",
							"description", "Description",
							"type_name", "typeName", "TypeName", "type", "Type",
							"prompt", "Prompt", "initial_prompt", "initialPrompt",
						} {
							if val, ok := subMap[subKey].(string); ok && strings.TrimSpace(val) != "" {
								if d := collapseDetail(workdir, val); d != "" {
									return d
								}
							}
						}
					} else if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						if d := collapseDetail(workdir, s); d != "" {
							return d
						}
					}
				}
			}
		}
	}
	return ""
}

// collapseDetail normalizes a detail string for one-line display:
// worktree paths become repo-relative, whitespace collapses to single
// spaces (the double space is the name/detail separator downstream),
// and the result is capped at detailCap runes.
func collapseDetail(workdir, s string) string {
	if workdir != "" {
		s = strings.ReplaceAll(s, strings.TrimSuffix(workdir, "/")+"/", "")
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > detailCap {
		s = string(r[:detailCap]) + "…"
	}
	return s
}

// WatchTool reports whether tool is a backend's own persistent
// background watch rather than an ordinary, synchronous call: Claude
// Code's Monitor tool (docs: tools-reference#monitor-tool), which
// returns before the thing it is watching resolves and runs on, outside
// any turn, until it fires or hits its deadline.
func WatchTool(tool string) bool { return tool == "Monitor" }
