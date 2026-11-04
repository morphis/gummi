package ui

import (
	"strconv"
	"testing"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// A tool line whose backend never reported back is not running once the
// session has stopped: only the newest pending call of a session mid-turn
// is in flight.
func TestWebTranscriptOnlyTheCallInFlightRuns(t *testing.T) {
	tr := []engine.Message{
		{Author: engine.AuthorTool, Content: "read  a.go"},
		{Author: engine.AuthorAssistant, Content: "done"},
		{Author: engine.AuthorTool, Content: "edit  b.go"},
	}
	turns, _, _, tool := webTranscript(engine.Snapshot{Transcript: tr})
	if tool != nil {
		t.Fatalf("a stopped session has no call in flight, got %+v", tool)
	}
	for _, tn := range turns {
		if tn.Tool != nil && tn.Tool.Status != "" {
			t.Fatalf("%q: status %q, want none", tn.Tool.Label, tn.Tool.Status)
		}
	}

	turns, _, _, tool = webTranscript(engine.Snapshot{Transcript: tr, Busy: true})
	if tool == nil || tool.Status != "running" || tool.Label != "edit  b.go" {
		t.Fatalf("the newest call of a busy session is in flight, got %+v", tool)
	}
	if st := turns[0].Tool.Status; st != "" {
		t.Fatalf("an older pending call: status %q, want none", st)
	}
	if st := turns[2].Tool.Status; st != "running" {
		t.Fatalf("the call in flight: status %q, want running", st)
	}
}

// A line the person sent with images shows them in its bubble while the
// session is open, as its settled item does.
func TestWebTranscriptCarriesTheImagesAYouTurnSent(t *testing.T) {
	ref := engine.AttachmentRef{ID: "abc", Name: "shot.png", MediaType: "image/png", Size: 141}
	tr := []engine.Message{
		{Author: engine.AuthorUser, Content: "what colour is this?", Images: []engine.AttachmentRef{ref}},
		{Author: engine.AuthorAssistant, Content: "green"},
	}
	turns, _, _, _ := webTranscript(engine.Snapshot{Transcript: tr})
	want := webapi.AttachmentRef{ID: "abc", Name: "shot.png", MediaType: "image/png", Size: 141}
	if len(turns) != 2 || len(turns[0].Attachments) != 1 || turns[0].Attachments[0] != want {
		t.Fatalf("you turn attachments = %+v, want [%+v]", turns, want)
	}
	if turns[1].Attachments != nil {
		t.Errorf("the reply carries attachments: %+v", turns[1].Attachments)
	}
}

// A long conversation's live block carries its newest LiveTurns and says
// how many came before; the rest is served on request, a window at a time,
// numbered the same way.
func TestWebTranscriptALongConversationIsServedInWindows(t *testing.T) {
	var tr []engine.Message
	for i := range 450 {
		tr = append(tr, engine.Message{Author: engine.AuthorAssistant, Content: "t" + strconv.Itoa(i)})
	}
	snap := engine.Snapshot{Transcript: tr}
	turns, omitted, _, _ := webTranscript(snap)
	if omitted != 250 || len(turns) != webapi.LiveTurns || turns[0].Text != "t250" || turns[len(turns)-1].Text != "t449" {
		t.Fatalf("live block: omitted %d, %d turns from %q", omitted, len(turns), turns[0].Text)
	}
	e := webEarlier(snap, omitted)
	if e.From != 50 || e.Omitted != 250 || len(e.Turns) != webapi.LiveTurns || e.Turns[0].Text != "t50" || e.Turns[len(e.Turns)-1].Text != "t249" {
		t.Fatalf("the window before the block: from %d, %d turns from %q", e.From, len(e.Turns), e.Turns[0].Text)
	}
	if e = webEarlier(snap, e.From); e.From != 0 || len(e.Turns) != 50 || e.Turns[49].Text != "t49" {
		t.Fatalf("the first window: from %d, %d turns", e.From, len(e.Turns))
	}
	// asking past the block's own start serves only what it left out
	if e = webEarlier(snap, 9999); e.From != 50 || e.Turns[len(e.Turns)-1].Text != "t249" {
		t.Fatalf("a before past the block: from %d, ending %q", e.From, e.Turns[len(e.Turns)-1].Text)
	}
	if turns, omitted, _, _ = webTranscript(engine.Snapshot{Transcript: tr[:10]}); omitted != 0 || len(turns) != 10 {
		t.Fatalf("a short conversation: omitted %d, %d turns", omitted, len(turns))
	}
}

// A Monitor watch (Claude Code's background-watch tool) never gets a
// reported outcome while it runs, and it keeps running past the turn
// that started it going idle: unlike an ordinary pending call, it must
// still read as active rather than as "outcome unknown", whether or not
// the session is busy or this is its newest call.
func TestWebTranscriptAMonitorWatchKeepsWatching(t *testing.T) {
	tr := []engine.Message{
		{Author: engine.AuthorTool, Tool: "Monitor", Detail: "tail -f build.log", Content: "Monitor  tail -f build.log"},
		{Author: engine.AuthorAssistant, Content: "started the watch"},
	}
	turns, _, _, tool := webTranscript(engine.Snapshot{Transcript: tr})
	if tool != nil {
		t.Fatalf("an idle session has no call in flight, got %+v", tool)
	}
	if st := turns[0].Tool.Status; st != "watching" {
		t.Fatalf("an idle session's outstanding watch: status %q, want watching", st)
	}
}

// A message a pause (or a failure) cut off is not still being written:
// the session is not mid-turn, and the page drew it under "writing" with a
// spinner for as long as the paused session stayed around.
func TestWebTranscriptACutOffMessageIsNotStreaming(t *testing.T) {
	tr := []engine.Message{{Author: engine.AuthorAssistant, Content: "Converged on the", Streaming: true}}
	turns, _, streaming, _ := webTranscript(engine.Snapshot{Transcript: tr, State: engine.StatePaused})
	if streaming != "" {
		t.Fatalf("a paused session streams %q", streaming)
	}
	if len(turns) != 1 || turns[0].Text != "Converged on the" {
		t.Fatalf("the cut-off message is not kept as the turn it stopped as: %+v", turns)
	}
	if _, _, streaming, _ = webTranscript(engine.Snapshot{Transcript: tr, Busy: true}); streaming != "Converged on the" {
		t.Fatalf("a busy session's tail is what it is writing, got %q", streaming)
	}
}
