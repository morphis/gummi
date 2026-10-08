package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A freeform session's own commands: the ones a person expects of any
// coding agent (/clear, /compact, /context…), offered the same on every
// backend. gummi holds the conversation — the transcript, and the replay
// a respawned backend is handed — so it answers these itself rather than
// passing them through to an agent that may or may not know them, and may
// answer them somewhere no one reads. A project file of the same name wins
// over any of them, as it does over /compact.
//
// Three kinds:
//   - instant (/context, /cost, /help): answered by gummi on the thread as
//     an activity line, never sent to the agent and never replayed to it;
//   - conversation (/clear, /retry): change what the agent remembers, the
//     way a rewind does, and never the branch;
//   - /compact: the backend's own compaction where it has one; elsewhere a
//     turn that asks the agent for a summary, after which the conversation
//     is replaced by that summary and the backend restarted on it;
//   - /handoff: a turn that asks the agent for a handoff on every backend,
//     after which the conversation is emptied and the handoff sent to a
//     fresh backend as its first turn.
var (
	handoffCommand = ProjectCommand{
		Name:        "handoff",
		Description: "hand the work to a fresh start of the agent: it writes a handoff, which becomes the first message of an empty conversation",
		builtin:     true,
	}
	clearCommand = ProjectCommand{
		Name:        "clear",
		Description: "start the conversation afresh — the branch and its files stay as they are",
		builtin:     true,
	}
	retryCommand = ProjectCommand{
		Name:        "retry",
		Description: "take back your last message and send it again",
		builtin:     true,
	}
	contextCommand = ProjectCommand{
		Name:        "context",
		Description: "how full the agent's context window is — sends nothing",
		builtin:     true,
	}
	costCommand = ProjectCommand{
		Name:        "cost",
		Description: "what this session and the card have spent — sends nothing",
		builtin:     true,
	}
	helpCommand = ProjectCommand{
		Name:        "help",
		Description: "list the commands this session takes — sends nothing",
		builtin:     true,
	}
)

// sessionCommands is every built-in, in the order a menu offers them.
var sessionCommands = []ProjectCommand{compactCommand, handoffCommand, clearCommand, retryCommand, contextCommand, costCommand, helpCommand}

// builtinFor is the built-in msg names, when cmds resolve it to one rather
// than to a project file, and the arguments after it.
func builtinFor(cmds []ProjectCommand, msg string) (ProjectCommand, string, bool) {
	c, args, ok := FindProjectCommand(cmds, msg)
	if !ok || !c.builtin {
		return ProjectCommand{}, "", false
	}
	return c, args, true
}

// compactPrompt is what a backend without compaction of its own is asked,
// for /compact. Its reply becomes the whole conversation the next backend
// is handed.
const compactPrompt = "Summarize this conversation so far, for a fresh session of yourself " +
	"that will continue the work with nothing but your summary and the worktree. " +
	"Cover: what the person asked for and any later changes to it, the decisions made " +
	"and why, what has been done (files and commits), what is in progress, and what is " +
	"left or open. Be complete but terse. Reply with the summary only, and do not change any files."

// handoffPrompt is what the agent is asked, for /handoff. Where /compact
// asks for a record of the conversation, this asks for a brief to act on:
// its reply is sent, as it stands, as the first turn of the next backend.
const handoffPrompt = "Write a handoff for a fresh session of yourself that will pick this work up " +
	"with nothing but your handoff and the worktree. It will be sent to that session as its first " +
	"message, so write it to that session, as what to do next. Cover: the goal and any later changes " +
	"to it, the decisions made and why, the current state (files touched, commits made, what is left " +
	"uncommitted), what was tried and did not work, and the next steps in order. Point to files, " +
	"commits and documents rather than restating them, and leave out secrets. Reply with the handoff " +
	"only: do not change any files, and do not write it to a file."

// handoffOpening heads the turn a handoff is sent as, so the thread reads
// it as what it is and the fresh session knows where it came from.
const handoffOpening = "Handoff from the previous session of this conversation, which was restarted on it:\n\n"

// runBuiltin answers a built-in that is not a turn for the agent. handled
// is false for one that is (/compact on any backend), which sendTurn
// sends on its way.
func (ff *FreeformSession) runBuiltin(ctx context.Context, c ProjectCommand, _ string, cmds []ProjectCommand) (handled bool, err error) {
	switch c.Name {
	case clearCommand.Name:
		return true, ff.Clear()
	case retryCommand.Name:
		return true, ff.Retry(ctx)
	case contextCommand.Name, costCommand.Name, helpCommand.Name:
		sess, err := ff.ensureSession(ctx)
		if err != nil {
			return true, err
		}
		var out string
		switch c.Name {
		case contextCommand.Name:
			out = contextReport(sess)
		case costCommand.Name:
			out = costReport(sess)
		default:
			out = helpReport(cmds)
		}
		sess.appendToolDone("gummi /"+c.Name, true, out)
		ff.engine.persist(sess)
		ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
		return true, nil
	}
	return false, nil
}

// ensureSession is the session an instant command reports on: the one
// held, or a backend spawned for it when none is yet.
func (ff *FreeformSession) ensureSession(ctx context.Context) (*Session, error) {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess != nil {
		return sess, nil
	}
	return ff.ensureBackend(ctx)
}

func contextReport(sess *Session) string {
	c := sess.Snapshot().Context
	if c.Limit <= 0 {
		if c.Tokens > 0 {
			return fmt.Sprintf("The agent holds %s tokens of context; its backend does not say how many it can hold.", kTokens(c.Tokens))
		}
		return "The agent's backend has not reported how full its context is yet. /compact still works: it summarizes the conversation and starts the agent again on the summary."
	}
	pct := 100 * c.Tokens / c.Limit
	s := fmt.Sprintf("Context: %d%% full — %s of %s tokens.", pct, kTokens(c.Tokens), kTokens(c.Limit))
	if pct >= 70 {
		s += " /compact summarizes the conversation and frees most of it; /clear starts afresh."
	}
	return s
}

func costReport(sess *Session) string {
	snap := sess.Snapshot()
	lines := []string{fmt.Sprintf("This backend has spent %.1f cr.", snap.SpentCredits)}
	if card := sess.CardSpent(); card > 0 {
		lines = append(lines, fmt.Sprintf("The card has spent %.1f cr in all.", card))
	}
	if b := sess.Budget(); b > 0 {
		left := max(b-snap.SpentCredits, 0)
		lines = append(lines, fmt.Sprintf("Its envelope leaves %.1f cr for this backend (%.1f cr left).", b, left))
	}
	return strings.Join(lines, "\n")
}

func helpReport(cmds []ProjectCommand) string {
	var b strings.Builder
	b.WriteString("Commands this session takes:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  /%s — %s", c.Name, c.Description)
		if c.Source != "" {
			fmt.Fprintf(&b, " (%s)", c.Source)
		}
		b.WriteString("\n")
	}
	b.WriteString("The card's own commands (/land, /close, /model…) are in its menu.")
	return b.String()
}

func kTokens(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// clearedNote opens the note /clear leaves: the one entry a cleared
// conversation holds, and so not something a second /clear clears.
const clearedNote = "Cleared the conversation."

// ErrNothingToClear is Clear's answer when the conversation is empty.
var ErrNothingToClear = errors.New("the conversation is already empty")

// Clear empties a freeform conversation: the agent starts afresh on its
// next turn, with nothing but a note that the branch was not cleared.
// Like Rewind it touches the conversation only — what was committed or
// written stays — and it is refused mid-turn and over an open question.
func (ff *FreeformSession) Clear() error {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return ErrNothingToClear
	}
	if sess.Busy() {
		return errors.New("the agent is mid-turn — stop it first, then clear")
	}
	if sess.Snapshot().PendingAsk != nil {
		return errors.New("a question is open — answer it first, then clear")
	}
	if !sess.truncateAll() {
		return ErrNothingToClear
	}
	sess.appendSystem(clearedNote + " The branch was not cleared: anything committed or " +
		"written before is still there — check git log and git status before assuming anything about the code.")
	ff.restartBackend(sess)
	return nil
}

// Retry takes back the person's last message and sends it again, as a
// rewind to before it followed by a send. Images that went with it do not
// go again.
func (ff *FreeformSession) Retry(ctx context.Context) error {
	text, err := ff.Rewind(1)
	if err != nil {
		return err
	}
	return ff.SendTurn(ctx, text, nil)
}

// restartBackend forgets the backend's own conversation so the next turn
// respawns on what the transcript now holds (the replay), and stops the
// one running. The rewind's tail, shared by /clear and /compact.
func (ff *FreeformSession) restartBackend(sess *Session) {
	sess.setAgentSessionID("")
	if sess.Live() {
		ff.settle()
		sess.setState(StateDone)
		sess.stop()
		ff.dropLock()
	} else {
		ff.engine.persist(sess)
	}
	ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
}

// finishCompact replaces the conversation with the summary the agent
// just wrote for /compact, and restarts the backend on it. A turn that
// wrote no summary (stopped, failed) leaves the conversation as it was
// and says so.
func (ff *FreeformSession) finishCompact(sess *Session) {
	summary, ok := sess.lastReplySince("/" + compactCommand.Name)
	if !ok {
		sess.appendActivity("/compact wrote no summary — the conversation was left as it was")
		ff.engine.persist(sess)
		ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
		return
	}
	sess.truncateAll()
	sess.appendSystem("Compacted the conversation. What it came to, in the agent's own summary:\n\n" + summary)
	ff.restartBackend(sess)
}

// finishHandoff empties the conversation once the agent has written its
// handoff for /handoff, restarts the backend, and sends the handoff to the
// fresh one as its first turn, on behalf of whoever asked (ctx). A turn
// that wrote none (stopped, failed) leaves the conversation as it was and
// says so. sent reports whether the handoff went out as a turn, whose end
// then drains what was queued behind it.
func (ff *FreeformSession) finishHandoff(ctx context.Context, sess *Session) (sent bool) {
	brief, ok := sess.lastReplySince("/" + handoffCommand.Name)
	if !ok {
		sess.appendActivity("/handoff wrote no handoff — the conversation was left as it was")
		ff.engine.persist(sess)
		ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
		return false
	}
	sess.truncateAll()
	ff.restartBackend(sess)
	// an error is the session's own and already on it (SendTurn)
	return ff.SendTurn(ctx, handoffOpening+brief, nil) == nil
}

// truncateAll drops the whole conversation but the pinned checklist,
// reporting whether anything had been said in it: a cleared
// conversation's own note alone is nothing to clear.
func (s *Session) truncateAll() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tasks *Message
	said := false
	for i := range s.transcript {
		switch s.transcript[i].Author {
		case AuthorTasks:
			m := s.transcript[i]
			tasks = &m
		case AuthorUser, AuthorAssistant:
			said = true
		case AuthorSystem:
			// a compacted summary is the conversation; the cleared note is not
			said = said || !strings.HasPrefix(s.transcript[i].Content, clearedNote)
		}
	}
	if !said {
		return false
	}
	s.transcript = nil
	if tasks != nil {
		s.transcript = append(s.transcript, *tasks)
	}
	s.streamOpen = false
	return true
}

// lastReplySince is the agent's last message after the newest person's
// line that starts with prefix.
func (s *Session) lastReplySince(prefix string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := ""
	for i := len(s.transcript) - 1; i >= 0; i-- {
		m := s.transcript[i]
		if m.Author == AuthorAssistant && reply == "" {
			reply = strings.TrimSpace(m.Content)
		}
		if m.Author == AuthorUser {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Content)), prefix) && reply != "" {
				return reply, true
			}
			return "", false
		}
	}
	return "", false
}
