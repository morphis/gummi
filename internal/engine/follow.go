package engine

import (
	"errors"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
)

// Follower rebuilds a session view from another process's live file
// (internal/livelog). It is the read side of what bindLiveLog publishes:
// feed it the records Follow delivers and its Snapshot renders exactly
// like a local session's, so a watcher — a board that doesn't own the
// card, `gummi watch` — can show the run without owning it.
//
// A Follower is a view, never a handle: it has no agent, no resolver, no
// way to answer a question or send a turn. Surfaces that render one must
// stay read-only.
//
// It is not safe for concurrent use; apply records and read Snapshot from
// one goroutine (the Bubble Tea update loop, a CLI's render loop).
type Follower struct {
	feature   domain.Feature
	role      agent.Role
	agentName string
	model     string
	state     SessionState
	busy      bool
	pid       int
	stopped   bool

	transcript []Message
	activity   []string
	spend      agent.Usage
	pendingAsk *Ask
	err        error

	// dropped counts records the writer lost to a full queue, so the view
	// can admit the gap instead of presenting an incomplete transcript as
	// the whole story.
	dropped int
}

// NewFollower starts an empty view of f. The feature is the caller's own
// (from the store), so the view carries its title and metadata; the live
// file's header overwrites the stage, which is the fresher truth.
func NewFollower(f domain.Feature) *Follower {
	return &Follower{feature: f}
}

// Apply folds one record into the view.
func (fl *Follower) Apply(r livelog.Record) {
	switch r.Kind {
	case livelog.KindReset, livelog.KindSession:
		// both mean a session took the card over: drop everything the
		// previous one left behind, then take the new identity.
		fl.resetSession()
		if r.Kind == livelog.KindSession {
			if r.Stage != "" {
				fl.feature.Stage = domain.Stage(r.Stage)
			}
			fl.role = agent.Role(r.Role)
			fl.agentName, fl.model, fl.pid = r.Agent, r.Model, r.PID
		}
	case livelog.KindUser:
		fl.transcript = append(fl.transcript, Message{Author: AuthorUser, Content: r.Text})
	case livelog.KindSystem:
		fl.transcript = append(fl.transcript, Message{Author: AuthorSystem, Content: r.Text})
	case livelog.KindDelta:
		fl.appendDelta(r.Text)
	case livelog.KindMessage:
		fl.finishAssistant(r.Text)
	case livelog.KindEdit:
		fl.editLastAssistant(r.Text)
	case livelog.KindTool:
		fl.appendTool(r)
	case livelog.KindResult:
		fl.resolveTool(r)
	case livelog.KindState:
		fl.state = SessionState(r.State)
	case livelog.KindBusy:
		fl.busy = r.Busy
	case livelog.KindSpend:
		fl.spend.Credits = r.Credits
		fl.spend.InputTokens = r.InputTokens
		fl.spend.OutputTokens = r.OutputTokens
		if r.Model != "" {
			fl.spend.Model = r.Model
		}
	case livelog.KindAsk:
		if r.Text == "" {
			fl.pendingAsk = nil
			return
		}
		// options are not mirrored: a watcher cannot answer, so the
		// question alone is what there is to show.
		fl.pendingAsk = &Ask{CallID: r.Call, Question: r.Text}
	case livelog.KindDropped:
		fl.dropped += r.Count
	case livelog.KindStopped:
		fl.stopped, fl.busy = true, false
		fl.pendingAsk = nil
		if r.Err != "" {
			fl.err = errors.New(r.Err)
		}
	}
}

// resetSession clears everything the previous session accumulated.
func (fl *Follower) resetSession() {
	fl.transcript, fl.activity = nil, nil
	fl.spend = agent.Usage{}
	fl.pendingAsk, fl.err = nil, nil
	fl.busy, fl.stopped, fl.dropped = false, false, 0
	fl.state = ""
}

// appendDelta mirrors Session.appendDelta: streaming text extends the
// open assistant bubble, or opens one.
func (fl *Follower) appendDelta(text string) {
	if text == "" {
		return
	}
	if n := len(fl.transcript); n > 0 && fl.transcript[n-1].Author == AuthorAssistant && fl.transcript[n-1].Streaming {
		fl.transcript[n-1].Content += text
		return
	}
	fl.transcript = append(fl.transcript, Message{Author: AuthorAssistant, Content: text, Streaming: true})
}

// finishAssistant mirrors Session.finishAssistant: the authoritative
// text supersedes whatever the deltas built, and an empty completion
// only closes the open bubble.
func (fl *Follower) finishAssistant(text string) {
	n := len(fl.transcript)
	streaming := n > 0 && fl.transcript[n-1].Author == AuthorAssistant && fl.transcript[n-1].Streaming
	// the same emptiness test the session applies, so a whitespace-only
	// completion closes the bubble on both sides instead of diverging.
	if strings.TrimSpace(text) == "" {
		if streaming {
			fl.transcript[n-1].Streaming = false
		}
		return
	}
	if streaming {
		fl.transcript[n-1].Content = text
		fl.transcript[n-1].Streaming = false
		return
	}
	fl.transcript = append(fl.transcript, Message{Author: AuthorAssistant, Content: text})
}

// editLastAssistant rewrites the most recent assistant message, the
// convention-path ask's strip of its gummi-ask block.
func (fl *Follower) editLastAssistant(content string) {
	for i := len(fl.transcript) - 1; i >= 0; i-- {
		if fl.transcript[i].Author == AuthorAssistant {
			fl.transcript[i].Content = content
			return
		}
	}
}

func (fl *Follower) appendTool(r livelog.Record) {
	m := Message{Author: AuthorTool, Content: r.Text, callID: r.Call, ToolOutput: r.Output}
	if r.OK {
		m.ToolStatus = ToolOK
	}
	if n := len(fl.transcript); n > 0 && fl.transcript[n-1].Author == AuthorAssistant && fl.transcript[n-1].Streaming {
		fl.transcript[n-1].Streaming = false
	}
	fl.activity = append(fl.activity, r.Text)
	fl.transcript = append(fl.transcript, m)
}

func (fl *Follower) resolveTool(r livelog.Record) {
	if r.Call == "" {
		return
	}
	for i := len(fl.transcript) - 1; i >= 0; i-- {
		if fl.transcript[i].callID != r.Call {
			continue
		}
		fl.transcript[i].callID = ""
		fl.transcript[i].ToolStatus = ToolOK
		if !r.OK {
			fl.transcript[i].ToolStatus = ToolFail
		}
		fl.transcript[i].ToolOutput = r.Output
		return
	}
}

// Snapshot renders the followed session in the same shape a local one
// produces, so every transcript surface can render either.
func (fl *Follower) Snapshot() Snapshot {
	return Snapshot{
		Feature:     fl.feature,
		Role:        fl.role,
		Interactive: false,
		State:       fl.state,
		AgentName:   fl.agentName,
		Model:       fl.model,
		Transcript:  append([]Message(nil), fl.transcript...),
		Activity:    append([]string(nil), fl.activity...),
		Spend:       fl.spend,
		Busy:        fl.busy,
		PendingAsk:  fl.pendingAsk,
		Err:         fl.err,
	}
}

// PID is the process that owns the session being followed (0 before the
// header arrives). Probe it with state.ProcessAlive to tell a live run
// from the file an exited one left behind.
func (fl *Follower) PID() int { return fl.pid }

// Stopped reports whether the followed session wrote its terminal record.
func (fl *Follower) Stopped() bool { return fl.stopped }

// Dropped is how many records the writer lost to a full queue — a gap in
// what the view can show, worth admitting rather than hiding.
func (fl *Follower) Dropped() int { return fl.dropped }
