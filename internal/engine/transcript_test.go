package engine

import (
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

func TestSetContextStickyLimit(t *testing.T) {
	s := &Session{}
	s.setContext(agent.Context{Tokens: 100, Limit: 8000})
	// a later event that omits the limit must not blank it
	s.setContext(agent.Context{Tokens: 250, Limit: 0})
	got := s.Snapshot().Context
	if got.Tokens != 250 || got.Limit != 8000 {
		t.Errorf("context = %+v, want tokens 250 / sticky limit 8000", got)
	}
}

func assistantContents(s *Session) []string {
	var out []string
	for _, m := range s.Snapshot().Transcript {
		if m.Author == AuthorAssistant {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestTranscriptSkipsEmptyReplies(t *testing.T) {
	s := &Session{}
	// agents emit many empty completions per turn (tool-call / reasoning
	// steps); none should appear as blank assistant bubbles.
	s.finishAssistant("")
	s.finishAssistant("   ")
	s.finishAssistant("Here is the plan.")
	s.finishAssistant("")
	got := assistantContents(s)
	if len(got) != 1 || got[0] != "Here is the plan." {
		t.Errorf("transcript = %q, want one real reply", got)
	}
}

func TestTranscriptEmptyFinishKeepsStreamedContent(t *testing.T) {
	s := &Session{}
	s.appendDelta("Hel")
	s.appendDelta("")   // an empty delta opens no bubble and adds nothing
	s.appendDelta("lo") // continues the same streamed message
	s.finishAssistant("")
	tr := s.Snapshot().Transcript
	if len(tr) != 1 || tr[0].Content != "Hello" || tr[0].Streaming {
		t.Errorf("streamed message = %+v, want finalized 'Hello'", tr)
	}
}

func TestTranscriptInterleavesToolCalls(t *testing.T) {
	s := &Session{}
	// message → tool → tool → message: history must keep this order
	s.appendDelta("I'll wire the toggle.")
	s.finishAssistant("I'll wire the toggle.")
	s.appendActivity("edit theme.go")
	s.appendActivity("run go test ./...")
	s.finishAssistant("Done, tests green.")
	snap := s.Snapshot()
	authors := make([]Author, len(snap.Transcript))
	for i, m := range snap.Transcript {
		authors[i] = m.Author
	}
	want := []Author{AuthorAssistant, AuthorTool, AuthorTool, AuthorAssistant}
	if len(authors) != len(want) {
		t.Fatalf("transcript = %+v, want %v", snap.Transcript, want)
	}
	for i := range want {
		if authors[i] != want[i] {
			t.Fatalf("transcript order = %v, want %v", authors, want)
		}
	}
	if snap.Transcript[1].Content != "edit theme.go" {
		t.Errorf("tool entry = %+v", snap.Transcript[1])
	}
	// the ticker still gets its flat copy for the dashboard
	if len(snap.Activity) != 2 || snap.Activity[1] != "run go test ./..." {
		t.Errorf("activity = %+v", snap.Activity)
	}
}

func TestToolCallMidStreamClosesBubble(t *testing.T) {
	s := &Session{}
	// a tool call landing mid-stream closes the open bubble so the next
	// delta starts a new one after the tool line, keeping order
	s.appendDelta("Looking at the failure.")
	s.appendActivity("read testdata/out.log")
	s.appendDelta("Found it — off by one.")
	snap := s.Snapshot()
	if len(snap.Transcript) != 3 {
		t.Fatalf("transcript = %+v, want 3 entries", snap.Transcript)
	}
	if snap.Transcript[0].Streaming {
		t.Error("pre-tool bubble left streaming")
	}
	if snap.Transcript[2].Author != AuthorAssistant || !snap.Transcript[2].Streaming {
		t.Errorf("post-tool delta = %+v, want a new streaming bubble", snap.Transcript[2])
	}
}

func TestTranscriptFinishReplacesStreamedContent(t *testing.T) {
	s := &Session{}
	s.appendDelta("Hel")
	s.finishAssistant("Hello, world") // authoritative full text replaces the stream
	tr := s.Snapshot().Transcript
	if len(tr) != 1 || tr[0].Content != "Hello, world" || tr[0].Streaming {
		t.Errorf("finalized message = %+v, want 'Hello, world'", tr)
	}
}
