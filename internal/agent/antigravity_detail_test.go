package agent

import "testing"

// TestAntigravityToolDetailSpeaksAgysVocabulary: an MCP call names the
// tool it calls (not the server every call shares), a search shows its
// query over its directory, and a file tool its path — agy's CamelCase
// parameters, never the alphabetically first string.
func TestAntigravityToolDetailSpeaksAgysVocabulary(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{
			"mcp call names its tool",
			`{"step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"gummi-BG-079","ToolName":"spec_view","Arguments":"{}"}}}`,
			"spec_view",
		},
		{
			"search shows its query",
			`{"step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"grep_search","tool_info":{"name":"grep_search","parameters":{"SearchPath":"/w/internal","Query":"func Hello"}}}`,
			"func Hello",
		},
		{
			"file tool shows its path, repo-relative",
			`{"step_index":5,"state":"ACTIVE","step_type":"tool","tool_name":"write_to_file","tool_info":{"name":"write_to_file","parameters":{"CodeContent":"package x","TargetFile":"/w/x.go"}}}`,
			"x.go",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &antigravitySession{workdir: "/w"}
			evs := s.mapStep([]byte(c.line))
			if len(evs) != 1 || evs[0].Detail != c.want {
				t.Fatalf("events = %+v, want one call with detail %q", evs, c.want)
			}
		})
	}
}

// TestAntigravityToolResultWithoutOutputIsEmpty: a DONE step whose
// tool_info carries no output yields an empty result, never the call's
// own name and parameters echoed back as if they were its output.
func TestAntigravityToolResultWithoutOutputIsEmpty(t *testing.T) {
	s := &antigravitySession{model: "m"}
	s.mapLine([]byte(`{"event":"step_update","step_update":{"step_index":7,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"gummi-x","ToolName":"spec_view"}}}}`))
	s.mapLine([]byte(`{"event":"step_update","step_update":{"step_index":7,"state":"DONE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"gummi-x","ToolName":"spec_view"}}}}`))
	evs := s.mapLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"ok","usage":{}}}`))
	for _, ev := range evs {
		if ev.Kind == EventToolResult {
			if ev.Result.Output != "" {
				t.Fatalf("result output = %q, want empty", ev.Result.Output)
			}
			return
		}
	}
	t.Fatalf("no tool result in %+v", evs)
}
