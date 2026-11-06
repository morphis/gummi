package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
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
//     fresh backend as its first turn;
//   - /workflow: a turn that asks the agent to propose a workflow run
//     (DESIGN §25.5) rather than do the work — the person's way of
//     asking for one without saying how.
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
	objectiveCommand = ProjectCommand{
		Name: "objective",
		Description: "keep the agent going until something is done: /objective [--check 'cmd'] <what done looks like>; " +
			"/objective pause|resume|stop|clear; alone, where it stands",
		builtin: true,
	}
	renameCommand = ProjectCommand{
		Name: "rename",
		Description: "retitle this session: /rename <title> — its branch takes the title's words too, " +
			"unless it has been pushed; alone, its name and branch",
		builtin: true,
	}
	attachCommand = ProjectCommand{
		Name:        "attach",
		Description: "attach another repository of the workspace to this session: /attach <repo> [base] — its branch is cut there too, in a worktree beside this one",
		builtin:     true,
	}
	detachCommand = ProjectCommand{
		Name:        "detach",
		Description: "take an attached repository off this session: /detach <repo> — refused while its branch holds commits that have not landed or its tree has changes",
		builtin:     true,
	}
	workflowCommand = ProjectCommand{
		Name: "workflow",
		Description: "have the agent propose a workflow run instead of doing the work: /workflow [recipe] [on spec n] [what for]; " +
			"alone, the recipe that fits the work so far",
		builtin: true,
	}
	helpCommand = ProjectCommand{
		Name:        "help",
		Description: "list the commands this session takes — sends nothing",
		builtin:     true,
	}
)

// sessionCommands is every built-in, in the order a menu offers them.
var sessionCommands = []ProjectCommand{compactCommand, handoffCommand, workflowCommand, objectiveCommand, renameCommand, attachCommand, detachCommand, clearCommand, retryCommand, contextCommand, costCommand, helpCommand}

// SessionCommands is every session's own commands, the ones a session
// answers before its backend has found any project file.
func SessionCommands() []ProjectCommand { return append([]ProjectCommand(nil), sessionCommands...) }

// SessionCommandsFor is the session's commands: SessionCommands less
// /workflow while the session has workflows off.
func (e *Engine) SessionCommandsFor(ctx context.Context, id domain.FeatureID) []ProjectCommand {
	if e.WorkflowsOn(ctx, id) {
		return SessionCommands()
	}
	var out []ProjectCommand
	for _, c := range sessionCommands {
		if c.Name != workflowCommand.Name {
			out = append(out, c)
		}
	}
	return out
}

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

// WorkflowAskLine is the /workflow line a face sends for a person asking
// for a run on the session's spec n: what a spec's "Propose a run" does.
func WorkflowAskLine(n int) string {
	return fmt.Sprintf("/%s on spec %d", workflowCommand.Name, n)
}

// workflowAskPrompt is what the agent is sent for /workflow: the person
// asks for a run, in the words after the command, and the agent writes
// the spec and proposes it — it does not start on the work itself.
func workflowAskPrompt(args string) string {
	s := "The person asks for a workflow run"
	if args = strings.TrimSpace(args); args != "" {
		s += ": \"" + args + "\""
	} else {
		s += " for the work in this conversation"
	}
	return s + ". Do not do the work yourself in this turn. Pick the recipe — the person may have named one; " +
		"workflow_recipes lists them — and write the spec it runs against, or bring the one named up to date " +
		"(spec_start, spec_replace_section): the steps read only the spec, never this conversation. Run its " +
		"done-when checks so you know which fail, then propose the run with workflow_run. If what the person " +
		"wants is unclear, ask them with ask_user first."
}

// handoffOpening heads the turn a handoff is sent as, so the thread reads
// it as what it is and the fresh session knows where it came from.
const handoffOpening = "Handoff from the previous session of this conversation, which was restarted on it:\n\n"

// runBuiltin answers a built-in that is not a turn for the agent. handled
// is false for one that is (/compact on any backend), which sendTurn
// sends on its way.
func (ff *FreeformSession) runBuiltin(ctx context.Context, c ProjectCommand, args string, cmds []ProjectCommand) (handled bool, err error) {
	switch c.Name {
	case objectiveCommand.Name:
		return true, ff.runObjectiveCommand(ctx, args)
	case renameCommand.Name:
		return true, ff.runRenameCommand(ctx, args)
	case attachCommand.Name, detachCommand.Name:
		return true, ff.runRepoCommand(ctx, c.Name, args)
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
	var backend string
	var names []string
	for _, c := range cmds {
		if c.Backend != "" {
			backend = c.Backend
			names = append(names, "/"+c.Name)
			continue
		}
		fmt.Fprintf(&b, "  /%s — %s", c.Name, c.Description)
		if c.Source != "" {
			fmt.Fprintf(&b, " (%s)", c.Source)
		}
		b.WriteString("\n")
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "%s's own commands and skills, sent to it as typed: %s\n", backend, strings.Join(names, " "))
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

// truncateAll drops the whole conversation, reporting whether anything had been said in it: a cleared
// conversation's own note alone is nothing to clear.
func (s *Session) truncateAll() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	said := false
	for i := range s.transcript {
		switch s.transcript[i].Author {
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

// runObjectiveCommand answers /objective: a verb (pause, resume, stop,
// clear), nothing (where the objective stands), or a new objective with
// an optional --check command ahead of its text.
func (ff *FreeformSession) runObjectiveCommand(ctx context.Context, args string) error {
	e := ff.engine
	args = strings.TrimSpace(args)
	switch strings.ToLower(args) {
	case "pause":
		return e.PauseObjective(ctx, ff.id)
	case "resume":
		return e.ResumeObjective(ctx, ff.id)
	case "stop":
		return e.StopObjective(ctx, ff.id)
	case "clear":
		return e.ClearObjective(ctx, ff.id)
	case "":
		sess, err := ff.ensureSession(ctx)
		if err != nil {
			return err
		}
		o, auditing := ff.objectiveView()
		sess.appendToolDone("gummi /"+objectiveCommand.Name, true, objectiveReport(o, auditing))
		e.persist(sess)
		e.send(Event{Feature: ff.id, Kind: EventUpdated})
		return nil
	}
	check, text, err := ParseObjectiveArgs(args)
	if err != nil {
		return err
	}
	return ff.setObjective(ctx, text, check)
}

// ParseObjectiveArgs splits /objective's arguments into the check command
// and the objective's text: "--check 'go test ./...' make it pass". The
// command may be quoted with ', " or `, or be a single word.
func ParseObjectiveArgs(args string) (check, text string, err error) {
	args = strings.TrimSpace(args)
	rest, ok := strings.CutPrefix(args, "--check")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '=') {
		return "", args, nil
	}
	rest = strings.TrimLeft(rest, " =")
	if rest == "" {
		return "", "", errors.New("--check needs a command: /objective --check 'go test ./...' <what done looks like>")
	}
	if q := rest[0]; q == '\'' || q == '"' || q == '`' {
		end := strings.IndexByte(rest[1:], q)
		if end < 0 {
			return "", "", errors.New("the --check command's quote is not closed")
		}
		check, text = rest[1:1+end], rest[2+end:]
	} else {
		check, text, _ = strings.Cut(rest, " ")
	}
	check, text = strings.TrimSpace(check), strings.TrimSpace(text)
	if check == "" {
		return "", "", errors.New("--check needs a command")
	}
	if text == "" {
		return "", "", errors.New("an objective needs some text after its check")
	}
	return check, text, nil
}

func objectiveReport(o *domain.Objective, auditing bool) string {
	if o == nil {
		return "This session has no objective. /objective <what done looks like> sets one, and the agent keeps " +
			"going after each turn until an auditor finds it met."
	}
	s := fmt.Sprintf("Objective (%s, %d of %d turns): %s", o.State, o.Turns, domain.ObjectiveTurnCap, o.Text)
	if o.Check != "" {
		s += "\nCheck: " + o.Check
	}
	if o.Note != "" {
		s += "\nLast note: " + o.Note
	}
	if auditing {
		s += "\nAuditing the last turn now."
	}
	return s
}

// runRepoCommand is /attach and /detach: the person's way, from either
// face's composer, to the engine's AttachRepo and DetachRepo. The answer
// is a line in the thread; a refusal is the error the composer shows.
func (ff *FreeformSession) runRepoCommand(ctx context.Context, name, args string) error {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return fmt.Errorf("/%s takes the name of a repository", name)
	}
	var line string
	if name == attachCommand.Name {
		base := ""
		if len(fields) > 1 {
			base = fields[1]
		}
		att, err := ff.engine.AttachRepo(ctx, ff.id, fields[0], base)
		if err != nil {
			return err
		}
		line = "attached " + att.Repo
	} else {
		if err := ff.engine.DetachRepo(ctx, ff.id, fields[0]); err != nil {
			return err
		}
		line = "detached " + fields[0]
	}
	if sess := ff.Session(); sess != nil {
		sess.appendToolDone("gummi /"+name, true, line+". The agent opens it at its next turn.")
		ff.engine.persist(sess)
	}
	ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
	return nil
}
