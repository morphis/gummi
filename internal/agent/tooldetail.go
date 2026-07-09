package agent

import "strings"

// detailKeys is the priority order in which a tool call's arguments are
// probed for the one value worth showing on an activity line: command
// for shell tools, file path for read/edit/write, pattern for searches,
// then the softer description-ish fields. The first non-empty string
// wins, so a grep call with both pattern and path shows the pattern.
var detailKeys = []string{
	"command", "cmd",
	"file_path", "filePath", "path", "file",
	"pattern", "query", "url",
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
