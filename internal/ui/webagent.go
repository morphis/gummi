package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// The board agent on the web face (DESIGN §16): the conversation the TUI
// hosts in its agent tab, bound to the board rather than a card. The page
// reads the same session the tab draws and sends through the same
// functions the tab's composer and its /profile, /model and /clear run.

// WebAgent is GET /api/agent.
func (m *Shell) WebAgent() webapi.Agent {
	a := webapi.Agent{
		Opening: m.boardOpening, Err: m.boardErr,
		Profiles: []webapi.AgentProfile{}, Models: []webapi.AgentModel{}, Items: []webapi.Item{},
	}
	if m.engine != nil {
		for _, p := range m.engine.BoardProfiles() {
			a.Profiles = append(a.Profiles, webapi.AgentProfile{Name: p.Name, Backend: p.Backend, Model: p.Model})
		}
		for _, km := range m.engine.KnownModels() {
			a.Models = append(a.Models, webapi.AgentModel{Model: km.Model, Uses: km.Uses})
		}
	}
	if m.board == nil {
		return a
	}
	snap := m.board.Snapshot()
	a.Open, a.Profile, a.Model, a.Backend = true, m.board.Profile(), runModel(snap), snap.AgentName
	var streaming string
	var tool *webapi.ToolCall
	a.Items, streaming, tool = webBoardItems(snap.Transcript)
	a.Live = webBoardLive(snap, streaming, tool)
	if c := snap.Context; c.Tokens > 0 {
		a.Context = &webapi.AgentContext{Tokens: c.Tokens, Limit: c.Limit}
	}
	return a
}

// webBoardLive is the board agent's live block: nil until it has done
// anything, and while a turn is in flight the busy word the TUI's board
// thread shows under its spinner (boardBusyWord).
func webBoardLive(snap engine.Snapshot, streaming string, tool *webapi.ToolCall) *webapi.Live {
	if !snap.Busy && streaming == "" && snap.SpentCredits <= 0 {
		return nil
	}
	return &webapi.Live{
		Busy: snap.Busy, Verb: boardBusyWord(snap), Streaming: streaming, Tool: tool,
		Spent: snap.SpentCredits, State: string(snap.State),
	}
}

// webBoardItems folds the board conversation into thread items: a
// person's lines, the agent's messages, runs of tool calls as one block,
// and gummi's own notes. The message still streaming is not an item; it
// comes back as streaming, and a call still running as tool, for the live
// block. Keys are the transcript position an item starts at, which a
// conversation that only grows keeps stable.
func webBoardItems(tr []engine.Message) (items []webapi.Item, streaming string, tool *webapi.ToolCall) {
	items = []webapi.Item{}
	last := len(tr) - 1
	for i, msg := range tr {
		seq := int64(i + 1)
		key := "b" + strconv.Itoa(i)
		switch msg.Author {
		case engine.AuthorUser:
			items = append(items, webapi.Item{Key: key, Seq: seq, T: webapi.ItemYou, Time: msg.At, Text: sanitize(msg.Content), By: msg.AnsweredBy})
		case engine.AuthorAssistant:
			if msg.Streaming && i == last {
				streaming = sanitize(msg.Content)
				continue
			}
			items = append(items, webapi.Item{Key: key, Seq: seq, T: webapi.ItemMessage, Time: msg.At, Text: sanitize(msg.Content)})
		case engine.AuthorThinking:
			items = append(items, webapi.Item{Key: key, Seq: seq, T: webapi.ItemMessage, Author: string(engine.AuthorThinking), Time: msg.At, Text: sanitize(msg.Content)})
		case engine.AuthorTool:
			call := webapi.ToolCall{Tool: msg.Tool, Label: sanitize(msg.Content), Detail: sanitize(msg.Detail), Status: "running"}
			switch msg.ToolStatus {
			case engine.ToolOK:
				call.Status = "ok"
			case engine.ToolFail:
				call.Status = "fail"
			}
			if !msg.DoneAt.IsZero() && !msg.At.IsZero() {
				call.Ms = msg.DoneAt.Sub(msg.At).Milliseconds()
			}
			if call.Status == "running" && i == last {
				c := call
				tool = &c
			}
			if n := len(items); n > 0 && items[n-1].T == webapi.ItemTools {
				items[n-1].Tools = append(items[n-1].Tools, call)
				items[n-1].Seq = seq
				continue
			}
			items = append(items, webapi.Item{Key: key, Seq: seq, T: webapi.ItemTools, Time: msg.At, Tools: []webapi.ToolCall{call}})
		default:
			items = append(items, webapi.Item{Key: key, Seq: seq, T: webapi.ItemNote, Time: msg.At, Text: sanitize(msg.Content)})
		}
	}
	return groupActivity(items), streaming, tool
}

// groupActivity folds each run of tool calls and thoughts between two other
// items into one activity item. A run keeps its first item's key, so it
// stays the same row as it grows.
func groupActivity(items []webapi.Item) []webapi.Item {
	out := make([]webapi.Item, 0, len(items))
	for _, it := range items {
		if !isActivity(it) {
			out = append(out, it)
			continue
		}
		if n := len(out); n > 0 && out[n-1].T == webapi.ItemActivity {
			a := &out[n-1]
			a.Items = append(a.Items, it)
			a.Seq = max(a.Seq, it.Seq)
			continue
		}
		out = append(out, webapi.Item{Key: "act:" + it.Key, Seq: it.Seq, T: webapi.ItemActivity, Time: it.Time, Stage: it.Stage, Items: []webapi.Item{it}})
	}
	return out
}

func isActivity(it webapi.Item) bool {
	return it.T == webapi.ItemTools || it.T == webapi.ItemMessage && it.Author == string(engine.AuthorThinking)
}

// WebAgentOpen is POST /api/agent/open: what visiting the agent tab does,
// or — with a profile or model named — what /profile and /model do before
// there is a conversation to lose. An open session is left as it is.
// A failed open is retried, which in the tab is /clear's job.
func (m *Shell) WebAgentOpen(req webapi.AgentOpenRequest) (tea.Cmd, error) {
	switch {
	case m.engine == nil:
		return nil, webErr(WebUnavailable, "%s", m.noAgent(" (set a model/provider to enable agents)"))
	case m.board != nil:
		return nil, nil
	case m.boardOpening:
		return nil, webErr(WebConflict, "the board session is still opening")
	}
	if req.Profile != "" || req.Model != "" {
		opts, err := m.webBoardOpts(req.Profile, req.Model)
		if err != nil {
			return nil, err
		}
		return m.reopenBoard(opts), nil
	}
	m.boardErr = ""
	return m.ensureBoardSession(), nil
}

// webBoardOpts resolves a profile the way /profile does (the declared
// spelling, or a refusal — never the engine's silent fallback to the
// default) and keeps the live session's profile when only a model is
// named, as /model does.
func (m *Shell) webBoardOpts(profile, model string) (engine.BoardOpts, error) {
	opts := engine.BoardOpts{Model: strings.TrimSpace(model)}
	if profile = strings.TrimSpace(profile); profile != "" {
		if opts.Profile = m.boardProfileNamed(profile); opts.Profile == "" {
			return opts, webErr(WebBadRequest, "no profile named %s", profile)
		}
	} else if m.board != nil {
		opts.Profile = m.board.Profile()
	}
	return opts, nil
}

// WebAgentSend is POST /api/agent/send: a line from the agent tab's
// composer. /clear is the one board command a page types; the rest are
// the tab's popups, and the profile switch has its own route.
func (m *Shell) WebAgentSend(text string) (tea.Cmd, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, webErr(WebBadRequest, "nothing to send")
	}
	if word, arg, ok := boardCommandWord(text); ok && arg == "" && strings.EqualFold("/"+word, boardClearCommand) {
		return m.clearBoardConversation(), nil
	}
	if m.board == nil {
		return nil, webErr(WebConflict, "the board session is not open — open it first")
	}
	return m.sendBoardMessage(text), nil
}

// WebAgentInterrupt is POST /api/agent/interrupt: the tab's esc on a turn
// in flight.
func (m *Shell) WebAgentInterrupt() (tea.Cmd, error) {
	if m.board == nil {
		return nil, webErr(WebConflict, "the board session is not open")
	}
	return m.interruptBoardSession(), nil
}

// WebAgentProfile is POST /api/agent/profile: /profile and /model. The
// TUI asks before ending a conversation someone could miss
// (confirmBoardReopen); the page asks instead, and sends back the token
// the question came with (webConfirmToken).
func (m *Shell) WebAgentProfile(req webapi.AgentProfileRequest) (tea.Cmd, error) {
	if m.engine == nil {
		return nil, webErr(WebUnavailable, "%s", m.noAgent(" (set a model/provider to enable agents)"))
	}
	if strings.TrimSpace(req.Profile) == "" && strings.TrimSpace(req.Model) == "" {
		return nil, webErr(WebBadRequest, "name a profile or a model to switch to")
	}
	if m.boardOpening {
		return nil, webErr(WebConflict, "the board session is still opening — try again in a moment")
	}
	opts, err := m.webBoardOpts(req.Profile, req.Model)
	if err != nil {
		return nil, err
	}
	if m.boardReopenLoses() {
		to := "the " + opts.Profile + " profile"
		if opts.Model != "" {
			to = "model " + opts.Model
		}
		q := "switch the board to " + to + "? the current conversation ends; a fresh one starts under it"
		in := webInput{confirm: req.Confirm}
		if tok := webConfirmToken("confirm-board-reopen", "", q); !in.takeConfirm(tok) {
			return nil, &WebError{Code: WebConflict, Reason: webapi.ConflictConfirm, Text: q, Confirm: tok}
		}
	}
	return m.reopenBoard(opts), nil
}
