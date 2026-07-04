package engine

import "testing"

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

func TestTranscriptFinishReplacesStreamedContent(t *testing.T) {
	s := &Session{}
	s.appendDelta("Hel")
	s.finishAssistant("Hello, world") // authoritative full text replaces the stream
	tr := s.Snapshot().Transcript
	if len(tr) != 1 || tr[0].Content != "Hello, world" || tr[0].Streaming {
		t.Errorf("finalized message = %+v, want 'Hello, world'", tr)
	}
}
