package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
)

// WebLive is the card's live block (GET /api/cards/{id}/live): what the
// TUI thread draws from session snapshots rather than from the card's
// log — the stage session running now, the watched run another process
// owns, the consult beside it and a freeform card's conversation. It
// reads snapshots only, so it runs inside Update like every projection.
// ok is false for a card the board does not have.
func (m *Shell) WebLive(id string) (webapi.Live, bool) {
	r, ok := m.rowByID(domain.FeatureID(id))
	if !ok {
		return webapi.Live{}, false
	}
	var live webapi.Live
	f := r.F
	// A freeform card's own busy-ness is live.Freeform's to carry — its
	// Conversation already holds the verb and the turns — so the generic
	// field is left alone here; setting both drew two busy rows in the
	// thread for the one session the card has.
	switch {
	case m.cardBusy(r) && !f.IsFreeform():
		live.Busy = true
		live.Verb = m.cardBusyWord(r)
	}
	if sess := m.sessionFor(f.ID); sess != nil && !r.DrivenAbroad {
		snap := sess.Snapshot()
		live.State = string(snap.State)
		live.Spent = snap.SpentCredits
		live.Stage, live.Role, live.Model = string(snap.Feature.Stage), string(snap.Role), runModel(snap)
		live.Session = snap.StartedAt
		if live.Busy {
			live.Since = snap.StartedAt
		}
		if liveSessionActive(snap) {
			live.Turns, live.Streaming, live.Tool = webTranscript(snap)
		}
		if snap.Err != nil {
			live.Err = threadfold.Sanitize(snap.Err.Error())
		}
	}
	if r.DrivenAbroad {
		fd := r.Foreign
		live.Elsewhere = &webapi.Elsewhere{
			PID: fd.PID, Stage: string(fd.Stage), Role: fd.Role, Agent: fd.Agent, Model: fd.Model,
			Since: fd.Since, Updated: fd.Updated, Busy: fd.Busy,
		}
		if fd.Busy && live.Since.IsZero() {
			live.Since = fd.Since
		}
	}
	if m.follow != nil && m.follow.feature == f.ID {
		snap := m.follow.Snapshot()
		if live.Elsewhere == nil {
			live.Elsewhere = &webapi.Elsewhere{PID: m.follow.pid}
		}
		live.Elsewhere.Watching = true
		live.Elsewhere.Note = m.follow.footer(snap)
		live.Stage, live.Role, live.Model = string(snap.Feature.Stage), string(snap.Role), runModel(snap)
		if live.Stage == "" {
			live.Stage = string(f.Stage)
		}
		live.Session = snap.StartedAt
		live.Spent = snap.SpentCredits
		live.Turns, live.Streaming, live.Tool = webTranscript(snap)
	}
	live.Consult = m.webConsult(r)
	live.Freeform = m.webFreeform(r)
	return live, true
}

// liveSessionActive reports whether a local session is still writing the
// segment its card's log ends in: then the log does not hold its turns
// yet, and the page draws them from the snapshot. A finished session
// (the engine keeps it after its run) has handed everything to the log.
func liveSessionActive(snap engine.Snapshot) bool {
	return snap.State != engine.StateDone
}

// webConsult is the card's consult exchange, where the TUI thread draws
// one (consultBlock): never on a freeform card, whose lines go to its
// own session.
func (m *Shell) webConsult(r featureRow) *webapi.Conversation {
	if r.F.IsFreeform() {
		return nil
	}
	c := m.consultFor(r.F.ID)
	asking := m.consultSending[r.F.ID]
	if c == nil {
		if asking == "" {
			return nil
		}
		return &webapi.Conversation{Busy: true, Verb: "asking", Sending: asking}
	}
	snap := c.Snapshot()
	if len(snap.Transcript) == 0 && asking == "" {
		return nil
	}
	conv := webConversation(snap, asking, "thinking")
	if m.store != nil {
		// Every question and answer is in the card's log (EventConsult),
		// and the thread draws them from there, where they were asked; the
		// live block keeps only what the log does not hold yet — the
		// answer being written, the call in flight, the line on its way.
		// Drawn from the snapshot too, a consult asked at todo sat below
		// every later stage, and was gone after a restart.
		conv.Turns = nil
		if !conv.Busy && conv.Streaming == "" && conv.Sending == "" && conv.Err == "" {
			return nil
		}
	}
	return conv
}

// webFreeform is a freeform card's conversation (freeformBlock).
func (m *Shell) webFreeform(r featureRow) *webapi.Conversation {
	if !r.F.IsFreeform() || m.engine == nil {
		return nil
	}
	sending := m.consultSending[r.F.ID]
	ff := m.engine.Freeform(r.F.ID)
	if ff == nil {
		// a session that has ended still shows what was said on it
		if snap, ok := m.engine.FreeformHistory(r.F.ID); ok && sending == "" {
			return webConversation(snap, "", "working")
		}
		if sending == "" {
			return nil
		}
		return &webapi.Conversation{Busy: true, Verb: "working", Sending: sending}
	}
	snap := ff.Snapshot()
	if len(snap.Transcript) == 0 && sending == "" {
		return nil
	}
	// the busy verb names what is running: gummi's own handoff-brief turn
	// says so, not the bare "working"
	verb := "working"
	if snap.Briefing {
		verb = engine.BriefDrafting
	}
	return webConversation(snap, sending, verb)
}

func webConversation(snap engine.Snapshot, sending, verb string) *webapi.Conversation {
	// a handoff brief in flight is activity the page should spin on, the
	// same way it spins on a turn — the brief turn runs on a session of
	// its own, so Busy alone would miss it
	busy := snap.Busy || snap.Briefing
	c := &webapi.Conversation{Busy: busy, Role: string(snap.Role), Spent: snap.SpentCredits, Model: runModel(snap)}
	c.Turns, c.Streaming, c.Tool = webTranscript(snap)
	if ctx := snap.Context; ctx.Tokens > 0 {
		c.Context = &webapi.AgentContext{Tokens: ctx.Tokens, Limit: ctx.Limit}
	}
	for _, t := range snap.Tasks {
		c.Tasks = append(c.Tasks, webapi.Task{Text: threadfold.Sanitize(t.Text), Status: string(t.Status)})
	}
	for _, x := range snap.Watches {
		c.Watches = append(c.Watches, boundTail(threadfold.Sanitize(x), webapi.LiveText))
	}
	for _, q := range snap.Queued {
		c.Queued = append(c.Queued, boundTail(threadfold.Sanitize(q), webapi.LiveText))
	}
	if busy {
		c.Verb = verb
	}
	if sending != "" && !delivered(snap, sending) {
		c.Sending = sending
		c.Busy = true
	}
	if snap.Err != nil {
		c.Err = threadfold.Sanitize(snap.Err.Error())
	}
	return c
}

// webTranscript takes a snapshot's transcript apart the way the live
// block draws it: the settled turns (the newest LiveTurns of them), the
// assistant message still arriving, and the tool call in flight.
func webTranscript(snap engine.Snapshot) (turns []webapi.Turn, streaming string, tool *webapi.ToolCall) {
	tr := snap.Transcript
	// A message is still arriving only while a turn is in flight. One a
	// pause or a failure cut off is flagged streaming forever, and the
	// page drew it under "writing" with a spinner for as long as the
	// session stayed around; it is the settled turn it stopped as.
	if n := len(tr); n > 0 && tr[n-1].Author == engine.AuthorAssistant && tr[n-1].Streaming && snap.Busy {
		streaming = boundTail(threadfold.Sanitize(tr[n-1].Content), webapi.LiveText)
		tr = tr[:n-1]
	}
	// Only the newest tool line of a session mid-turn is in flight. A
	// pending one anywhere else is a note, or a call its backend never
	// reported back on: its outcome is unknown, not running.
	inflight := -1
	for i := len(tr) - 1; i >= 0; i-- {
		if tr[i].Author == engine.AuthorTool {
			if tr[i].ToolStatus == engine.ToolPending && snap.Busy {
				c := webToolCall(tr[i], true)
				tool, inflight = &c, i
			}
			break
		}
	}
	from := 0
	if len(tr) > webapi.LiveTurns {
		from = len(tr) - webapi.LiveTurns
	}
	turns = make([]webapi.Turn, 0, len(tr)-from)
	for i := from; i < len(tr); i++ {
		msg := tr[i]
		if msg.Author == engine.AuthorTool {
			c := webToolCall(msg, i == inflight)
			turns = append(turns, webapi.Turn{Author: "tool", Tool: &c})
			continue
		}
		if msg.Author == engine.AuthorTasks {
			continue // Conversation.Tasks, pinned
		}
		turns = append(turns, webapi.Turn{
			Author: threadfold.AuthorLabel(string(msg.Author), string(snap.Role)),
			By:     state.PersonName(msg.By),
			Text:   boundTail(threadfold.Sanitize(msg.Content), webapi.LiveText),
		})
	}
	return turns, streaming, tool
}

// webToolCall is one live tool call. Content holds the call as the
// activity line shows it, Tool and Detail the call taken apart. inflight
// marks the call the session is waiting on; any other pending call has no
// status.
func webToolCall(msg engine.Message, inflight bool) webapi.ToolCall {
	label := threadfold.Sanitize(msg.Content)
	tool, detail := threadfold.Sanitize(msg.Tool), threadfold.Sanitize(msg.Detail)
	if tool == "" {
		t, d, _ := strings.Cut(label, "  ")
		tool, detail = strings.TrimSpace(t), strings.TrimSpace(d)
	}
	c := webapi.ToolCall{Tool: tool, Label: label, Detail: detail}
	switch msg.ToolStatus {
	case engine.ToolOK:
		c.Status = "ok"
	case engine.ToolFail:
		c.Status = "fail"
		c.Output = boundTail(threadfold.Sanitize(msg.ToolOutput), webapi.LiveText)
	default:
		switch {
		case inflight:
			c.Status = "running"
		case threadfold.WatchTool(tool):
			// a backend's own background watch (Claude Code's Monitor
			// tool) never gets a reported outcome while it runs, and the
			// turn that started it does not wait on it either — it stays
			// outstanding long after the call stops being the inflight
			// one, often past the turn going idle.
			c.Status = threadfold.StatusWatching
		}
	}
	return c
}

// boundTail keeps the last n bytes of s, cut at a rune boundary: the end
// of a message is the part still being written.
func boundTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < len(s) && i < 4; i++ {
		if s[i]&0xC0 != 0x80 {
			return "…" + s[i:]
		}
	}
	return "…" + s
}
