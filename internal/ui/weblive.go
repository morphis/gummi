package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
		// a workflow step's card: its conversation is its live block
		if r, ok = m.childRow(domain.FeatureID(id)); !ok {
			return webapi.Live{}, false
		}
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
	if r.DrivenAbroad {
		fd := r.Foreign
		live.Elsewhere = &webapi.Elsewhere{
			PID: fd.PID, Stage: fd.Stage, Role: fd.Role, Agent: fd.Agent, Model: fd.Model,
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
		live.Turns, live.Omitted, live.Streaming, live.Tool = webTranscript(snap)
	}
	live.Consult = m.webConsult(r)
	live.Freeform = m.webFreeform(r)
	return live, true
}

// WebLiveEarlier is GET /api/cards/{id}/live/earlier: the turns before
// turn before of one of the card's live conversations — conv "stage" (a
// session another gummi runs, which this board follows) or "freeform" —
// that the block left out. It
// reads the same snapshot WebLive does, so the numbers agree. ok is false
// for a card the board does not have or a conversation it is not having.
func (m *Shell) WebLiveEarlier(id, conv string, before int) (webapi.Earlier, bool) {
	r, ok := m.rowByID(domain.FeatureID(id))
	if !ok {
		return webapi.Earlier{}, false
	}
	switch conv {
	case "stage":
		if m.follow != nil && m.follow.feature == r.F.ID {
			return webEarlier(m.follow.Snapshot(), before), true
		}
	case "freeform":
		if !r.F.IsFreeform() || m.engine == nil {
			break
		}
		if ff := m.engine.Freeform(r.F.ID); ff != nil {
			return webEarlier(ff.Snapshot(), before), true
		}
		if snap, ok := m.engine.FreeformHistory(r.F.ID); ok {
			return webEarlier(snap, before), true
		}
	}
	return webapi.Earlier{}, false
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
	conv.Notice = c.Notice()
	if m.store != nil {
		// Every question and answer is in the card's log (EventConsult),
		// and the thread draws them from there, where they were asked; the
		// live block keeps only what the log does not hold yet — the
		// answer being written, the call in flight, the line on its way.
		// Drawn from the snapshot too, a consult asked at todo sat below
		// every later stage, and was gone after a restart.
		conv.Turns, conv.Omitted = nil, 0
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
	// the busy verb names what is running: an objective's audit says so,
	// not the bare "working"
	verb := "working"
	if snap.Auditing && !snap.Busy {
		verb = engine.ObjectiveAuditing
	}
	c := webConversation(snap, sending, verb)
	if o := snap.Objective; o != nil {
		c.Objective = &webapi.Objective{
			Text: threadfold.Sanitize(o.Text), Check: threadfold.Sanitize(o.Check), State: string(o.State),
			Turns: o.Turns, Cap: domain.ObjectiveTurnCap, Note: threadfold.Sanitize(o.Note), Auditing: snap.Auditing,
		}
	}
	return c
}

func webConversation(snap engine.Snapshot, sending, verb string) *webapi.Conversation {
	// an objective's audit is activity the page should spin on, the same
	// way it spins on a turn — the audit runs on a session of its own, so
	// Busy alone would miss it
	busy := snap.Busy || snap.Auditing
	c := &webapi.Conversation{Busy: busy, Role: string(snap.Role), Spent: snap.SpentCredits, Model: runModel(snap)}
	c.Turns, c.Omitted, c.Streaming, c.Tool = webTranscript(snap)
	if ctx := snap.Context; ctx.Tokens > 0 {
		c.Context = &webapi.AgentContext{Tokens: ctx.Tokens, Limit: ctx.Limit}
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
// block draws it: the settled turns (the newest LiveTurns of them, and how
// many came before those), the assistant message still arriving, and the
// tool call in flight.
func webTranscript(snap engine.Snapshot) (turns []webapi.Turn, omitted int, streaming string, tool *webapi.ToolCall) {
	tr, streaming, tool, inflight := settledTranscript(snap)
	idx := turnIndex(tr)
	omitted = max(0, len(idx)-webapi.LiveTurns)
	return webTurns(snap, tr, idx[omitted:], inflight), omitted, streaming, tool
}

// webEarlier is the turns of snap's transcript the live block leaves out
// that come before turn before: at most LiveTurns of them, and where the
// first one stands.
func webEarlier(snap engine.Snapshot, before int) webapi.Earlier {
	tr, _, _, inflight := settledTranscript(snap)
	idx := turnIndex(tr)
	omitted := max(0, len(idx)-webapi.LiveTurns)
	to := min(max(before, 0), omitted)
	from := max(0, to-webapi.LiveTurns)
	return webapi.Earlier{From: from, Turns: webTurns(snap, tr, idx[from:to], inflight), Omitted: omitted}
}

// settledTranscript is snap's transcript without the message still
// arriving, which it returns apart, with the tool call in flight and that
// call's place in the transcript (-1 when there is none).
func settledTranscript(snap engine.Snapshot) (tr []engine.Message, streaming string, tool *webapi.ToolCall, inflight int) {
	// a run's notes are its block's to say (threadfold.IsRunNote)
	tr = threadfold.WithoutRunNotes(snap.Transcript)
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
	inflight = -1
	for i := len(tr) - 1; i >= 0; i-- {
		if tr[i].Author == engine.AuthorTool {
			if tr[i].ToolStatus == engine.ToolPending && snap.Busy {
				c := webToolCall(tr[i], true)
				tool, inflight = &c, i
			}
			break
		}
	}
	return tr, streaming, tool, inflight
}

// turnIndex is where each turn of tr stands in it: every message. A turn's number — what
// Omitted counts and Earlier's before names — is its place in this list.
func turnIndex(tr []engine.Message) []int {
	idx := make([]int, 0, len(tr))
	for i := range tr {
		idx = append(idx, i)
	}
	return idx
}

// webTurns is the transcript messages at idx as the page draws them.
func webTurns(snap engine.Snapshot, tr []engine.Message, idx []int, inflight int) []webapi.Turn {
	turns := make([]webapi.Turn, 0, len(idx))
	for _, i := range idx {
		msg := tr[i]
		if msg.Author == engine.AuthorTool {
			c := webToolCall(msg, i == inflight)
			turns = append(turns, webapi.Turn{Author: "tool", Tool: &c, Time: msg.At})
			continue
		}
		turn := webapi.Turn{
			Author: threadfold.AuthorLabel(string(msg.Author), string(snap.Role)),
			By:     threadfold.TurnBy(msg.By),
			Text:   boundTail(threadfold.Sanitize(msg.Content), webapi.LiveText),
			Time:   msg.At,
		}
		for _, r := range msg.Images {
			turn.Attachments = append(turn.Attachments, webapi.AttachmentRef{ID: r.ID, Name: r.Name, MediaType: r.MediaType, Size: r.Size})
		}
		turns = append(turns, turn)
	}
	return turns
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
	c := webapi.ToolCall{Tool: tool, Label: label, Detail: detail, Parent: msg.Parent}
	if !msg.At.IsZero() && msg.DoneAt.After(msg.At) {
		c.Ms = msg.DoneAt.Sub(msg.At).Milliseconds()
	}
	switch msg.ToolStatus {
	case engine.ToolOK:
		c.Status = "ok"
		c.Output = boundTail(threadfold.Sanitize(msg.ToolOutput), webapi.LiveText)
	case engine.ToolFail:
		c.Status = "fail"
		c.Output = boundTail(threadfold.Sanitize(msg.ToolOutput), webapi.LiveText)
	default:
		switch {
		case inflight:
			c.Status = "running"
		case threadfold.WatchTool(tool):
			// a backend's own background watch (Claude Code's Monitor
			// tool, or a subagent it spawned) never gets a reported outcome while it runs, and the
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
func boundTail(s string, n int) string { //nolint:unparam // n is the cap; the live text limit is the only one today
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
