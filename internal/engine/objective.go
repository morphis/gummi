package engine

// A freeform card's objective (DESIGN §19.11): after each turn, a fresh
// tool-less auditor judges the work against what the person asked for,
// and gummi sends the next turn itself until it is met, stuck or out of
// money. The decision is domain.Objective.Next; this file runs the audit,
// the check and the turns, and keeps the store row in step.
//
// The loop hangs off drainQueue: a turn's end sends what the person
// queued first, and only with nothing waiting — and no question open —
// does the objective get its say.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/childproc"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

const (
	// auditTurnTimeout bounds one audit; an auditor that cannot answer in
	// this long counts as STUCK.
	auditTurnTimeout = 5 * time.Minute
	// objectiveCheckTimeout bounds the objective's check command.
	objectiveCheckTimeout = 10 * time.Minute
	// auditReplyMax is how much of the agent's last reply the auditor sees.
	auditReplyMax = 8000
)

// EventObjective fires once when a card's objective settles (met, stuck,
// exhausted, capped, failed): the one notification a running objective
// gives, in place of a ping for every turn.
const EventObjective EventKind = "objective"

// ObjectiveAuditing is the busy word while an objective's audit runs
// between turns, in place of "working".
const ObjectiveAuditing = "auditing"

// Objective is a card's objective, nil when it has none.
func (e *Engine) Objective(ctx context.Context, id domain.FeatureID) (*domain.Objective, error) {
	if ff := e.Freeform(id); ff != nil {
		o, _ := ff.objectiveView()
		return o, nil
	}
	if !e.cfg.Persist || e.cfg.Store == nil {
		return nil, nil
	}
	return e.cfg.Store.Objective(ctx, id)
}

// SetObjective sets a freeform card's objective, replacing any it had,
// and starts it: a session with nothing in flight is sent the objective
// as its next turn; one mid-turn takes it up when that turn ends. check is
// optional: a command that must exit 0 for the auditor's MET to count.
func (e *Engine) SetObjective(ctx context.Context, id domain.FeatureID, text, check string) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	return ff.setObjective(ctx, text, check)
}

// PauseObjective pauses a running objective. The turn in flight, if any,
// finishes; nothing more is sent for the objective until it is resumed.
func (e *Engine) PauseObjective(ctx context.Context, id domain.FeatureID) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	if !ff.pauseObjective() {
		return errors.New("there is no running objective to pause")
	}
	return nil
}

// StopObjective stops the turn in flight and pauses the objective — what
// stop does to any freeform turn while an objective runs.
func (e *Engine) StopObjective(ctx context.Context, id domain.FeatureID) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	if ff.Busy() {
		return e.InterruptFreeform(ctx, id)
	}
	ff.pauseObjective()
	return nil
}

// ResumeObjective resumes a paused objective: an idle session is audited
// at once, and a busy one when its turn ends. A settled objective does
// not resume — set it again to start it over.
func (e *Engine) ResumeObjective(ctx context.Context, id domain.FeatureID) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	o, _ := ff.objectiveView()
	if o == nil || o.State != domain.ObjectivePaused {
		return errors.New("there is no paused objective to resume")
	}
	o.State = domain.ObjectiveActive
	o.StuckStreak = 0
	if err := ff.writeObjective(ctx, o); err != nil {
		return err
	}
	ff.noteObjective("objective resumed")
	if sess := ff.Session(); sess != nil {
		ff.advanceObjective(sess)
	}
	return nil
}

// ClearObjective removes a paused or settled objective: the session goes
// back to moving only when its person types. A running one is paused
// first.
func (e *Engine) ClearObjective(ctx context.Context, id domain.FeatureID) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	if o, _ := ff.objectiveView(); o == nil {
		return errors.New("this session has no objective")
	}
	ff.pauseObjective()
	if err := ff.writeObjective(ctx, nil); err != nil {
		return err
	}
	ff.noteObjective("objective cleared")
	return nil
}

// ObjectiveCommand runs /objective's arguments on a card's session: what
// a face's menu sends without going through the composer.
func (e *Engine) ObjectiveCommand(ctx context.Context, id domain.FeatureID, args string) error {
	ff, err := e.objectiveSession(ctx, id)
	if err != nil {
		return err
	}
	return ff.runObjectiveCommand(ctx, args)
}

// objectiveSession is the open freeform session an objective verb acts
// on, opened if the card has none yet.
func (e *Engine) objectiveSession(ctx context.Context, id domain.FeatureID) (*FreeformSession, error) {
	if ff := e.Freeform(id); ff != nil {
		return ff, nil
	}
	f, err := e.feature(ctx, id)
	if err != nil {
		return nil, err
	}
	if !f.IsFreeform() {
		return nil, fmt.Errorf("%s is a %s card: its stages already loop, and only a freeform session takes an objective", id, f.Kind)
	}
	return e.OpenFreeform(ctx, f)
}

// lifetime is the engine's own context, which an audit runs on rather
// than on the request that set the objective.
func (e *Engine) lifetime() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

// objectiveView is a copy of the card's objective and whether its audit
// is running, read from the store the first time.
func (ff *FreeformSession) objectiveView() (*domain.Objective, bool) {
	ff.mu.Lock()
	loaded := ff.objLoaded
	ff.mu.Unlock()
	if !loaded {
		var o *domain.Objective
		if e := ff.engine; e.cfg.Persist && e.cfg.Store != nil {
			o, _ = e.cfg.Store.Objective(e.lifetime(), ff.id)
		}
		ff.mu.Lock()
		if !ff.objLoaded {
			ff.objective, ff.objLoaded = o, true
		}
		ff.mu.Unlock()
	}
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.objective == nil {
		return nil, ff.auditing
	}
	o := *ff.objective
	return &o, ff.auditing
}

// writeObjective stores o (nil clears it) and tells the faces.
func (ff *FreeformSession) writeObjective(ctx context.Context, o *domain.Objective) error {
	if e := ff.engine; e.cfg.Persist && e.cfg.Store != nil {
		if err := e.cfg.Store.SetObjective(context.WithoutCancel(ctx), ff.id, o); err != nil {
			return err
		}
	}
	ff.mu.Lock()
	if o != nil {
		c := *o
		o = &c
	}
	ff.objective, ff.objLoaded = o, true
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	return nil
}

func (ff *FreeformSession) setObjective(ctx context.Context, text, check string) error {
	text, check = strings.TrimSpace(text), strings.TrimSpace(check)
	if text == "" {
		return errors.New("an objective needs some text: /objective <what done looks like>")
	}
	o := domain.Objective{
		Text: text, Check: check, State: domain.ObjectiveActive,
		BaseRev: ff.headRev(ctx), SetAt: time.Now().UTC(), SetBy: personOf(ctx),
	}
	if err := ff.writeObjective(ctx, &o); err != nil {
		return err
	}
	sess, err := ff.ensureSession(ctx)
	if err != nil {
		return err
	}
	line := "objective set: " + text
	if check != "" {
		line += " (met once `" + check + "` passes)"
	}
	sess.appendActivity(line)
	ff.engine.persist(sess)
	// a session at rest starts on it now; a busy one is audited when its
	// turn ends, as is one holding a question (which the person answers)
	if sess.Busy() || sess.Snapshot().PendingAsk != nil || ff.busyBriefing() || len(ff.Queued()) > 0 {
		return nil
	}
	return ff.SendTurn(WithActor(ctx, state.ActorObjective), objectiveOpening(o), nil)
}

// pauseObjective pauses a running objective, reporting whether there was
// one.
func (ff *FreeformSession) pauseObjective() bool {
	o, _ := ff.objectiveView()
	if o == nil || o.State != domain.ObjectiveActive {
		return false
	}
	o.State = domain.ObjectivePaused
	if ff.writeObjective(ff.engine.lifetime(), o) != nil {
		return false
	}
	ff.noteObjective("objective paused")
	return true
}

// settleObjective ends a running objective in state with note: the record
// says so, and the faces get their one notification.
func (ff *FreeformSession) settleObjective(s domain.ObjectiveState, note string) {
	o, _ := ff.objectiveView()
	if o == nil || o.State != domain.ObjectiveActive {
		return
	}
	o.State, o.Note = s, note
	if ff.writeObjective(ff.engine.lifetime(), o) != nil {
		return
	}
	ff.announceSettled(*o)
}

func (ff *FreeformSession) announceSettled(o domain.Objective) {
	ff.noteObjective(fmt.Sprintf("objective %s after %d turns — %s", o.State, o.Turns, o.Note))
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventObjective})
}

// noteObjective puts an objective's line on the record.
func (ff *FreeformSession) noteObjective(line string) {
	sess := ff.Session()
	if sess == nil {
		return
	}
	sess.appendActivity(line)
	ff.engine.persist(sess)
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

func (ff *FreeformSession) beginAudit() bool {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.auditing {
		return false
	}
	ff.auditing = true
	return true
}

func (ff *FreeformSession) endAudit() {
	ff.mu.Lock()
	ff.auditing = false
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

// advanceObjective audits the turn that just ended, off the pump, when an
// objective is running and nothing else is owed first: no turn in flight,
// no question open, no brief drafting.
func (ff *FreeformSession) advanceObjective(sess *Session) {
	if !ff.objectiveDue(sess) || !ff.beginAudit() {
		return
	}
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	if !ff.engine.goBrief(func() {
		defer ff.endAudit()
		ff.runAudit(ff.engine.lifetime(), sess)
	}) {
		ff.endAudit()
	}
}

// restoreObjective is the one audit a restored session owes: the turn
// that ended before the restart was never audited.
func (ff *FreeformSession) restoreObjective(sess *Session) {
	if !ff.objectiveDue(sess) || !ff.beginAudit() {
		return
	}
	defer ff.endAudit()
	ff.runAudit(ff.engine.lifetime(), sess)
}

func (ff *FreeformSession) objectiveDue(sess *Session) bool {
	o, _ := ff.objectiveView()
	if o == nil || o.State != domain.ObjectiveActive {
		return false
	}
	return !sess.Busy() && sess.Snapshot().PendingAsk == nil && !ff.busyBriefing()
}

// runAudit is one round of the loop: audit, check, decide, and send the
// next turn or settle. A board closing mid-audit changes nothing: the
// objective stays active and the restored board audits again.
func (ff *FreeformSession) runAudit(ctx context.Context, sess *Session) {
	o, _ := ff.objectiveView()
	if o == nil || o.State != domain.ObjectiveActive {
		return
	}
	if sess.isExhausted() {
		ff.settleObjective(domain.ObjectiveExhausted, "the card's envelope ran out")
		return
	}
	reply, err := ff.audit(ctx, sess, *o)
	if ctx.Err() != nil {
		return
	}
	var v domain.AuditVerdict
	var note string
	if err != nil {
		v, note = domain.AuditStuck, "the audit failed: "+err.Error()
	} else {
		v, note = domain.ParseAudit(reply)
	}
	var check domain.CheckResult
	if v == domain.AuditMet && o.Check != "" {
		check = ff.runCheck(ctx, o.Check)
		if ctx.Err() != nil {
			return
		}
	}
	// the person may have paused, cleared or replaced it meanwhile
	cur, _ := ff.objectiveView()
	if cur == nil || cur.State != domain.ObjectiveActive || !cur.SetAt.Equal(o.SetAt) {
		return
	}
	next, send := cur.Next(v, note, check)
	if err := ff.writeObjective(ctx, &next); err != nil {
		sess.appendActivity("objective: could not save the audit (" + err.Error() + ")")
		return
	}
	if !send {
		ff.announceSettled(next)
		return
	}
	ff.noteObjective(fmt.Sprintf("audit: %s — %s", v, next.Note))
	// a line the person sent while the audit ran goes first; its turn's
	// end is audited in turn
	if sess.Busy() || len(ff.Queued()) > 0 || sess.Snapshot().PendingAsk != nil {
		return
	}
	if err := ff.SendTurn(WithActor(ctx, state.ActorObjective), objectiveContinuation(next), nil); err != nil {
		if ff.Session() != nil && ff.Session().isExhausted() {
			ff.settleObjective(domain.ObjectiveExhausted, "the card's envelope ran out")
			return
		}
		ff.settleObjective(domain.ObjectiveFailed, "the next turn could not be sent: "+err.Error())
	}
}

// resolveAuditorRole picks the auditor's model and backend as one
// decision: the profile's auditor when declared, else its scribe — the
// cheap model — else the session's own.
func (e *Engine) resolveAuditorRole(profileName string, own config.RoleConfig, ownBackend string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleAuditor, agent.RoleScribe} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return own, ownBackend
}

// audit runs the auditor: a fresh, tool-less session given the objective,
// the agent's last reply and what gummi sees on the branch — never the
// transcript. Its cost goes on the card.
func (ff *FreeformSession) audit(ctx context.Context, sess *Session, o domain.Objective) (string, error) {
	e := ff.engine
	f, err := e.feature(ctx, ff.id)
	if err != nil {
		return "", err
	}
	ff.mu.Lock()
	rc, backend, workDir := ff.rc, ff.backend, ff.workDir
	ff.mu.Unlock()
	arc, abackend := e.resolveAuditorRole(f.Profile, rc, backend)
	ag, err := e.sessionAgent(abackend)
	if err != nil {
		return "", fmt.Errorf("the auditor's backend: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, auditTurnTimeout)
	defer cancel()
	prompt := auditPrompt(o, lastReply(sess), ff.objectiveFacts(ctx, workDir, o))
	as, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:    workDir,
		Role:       agent.RoleAuditor,
		Model:      arc.Model,
		Permission: e.cfg.Permission,
		ReadOnly:   ConsultConfined(ag.Capabilities()),
		SystemHints: []string{
			"You audit another agent's work against an objective. Read-only: do not modify any file, " +
				"and answer from what you are given.",
		},
		FeatureID: string(ff.id),
		// No Tools and no MCP endpoint: its product is the verdict alone.
	})
	if err != nil {
		return "", fmt.Errorf("starting the audit: %w", err)
	}
	defer func() { _ = as.Close() }()
	if err := as.Send(ctx, prompt); err != nil {
		return "", err
	}
	return collectOneShot(ctx, e, ff, as, "audit")
}

// lastReply is the agent's last message on the session.
func lastReply(sess *Session) string {
	tr := sess.Snapshot().Transcript
	for i := len(tr) - 1; i >= 0; i-- {
		if tr[i].Author == AuthorAssistant && strings.TrimSpace(tr[i].Content) != "" {
			return tailRunes(tr[i].Content, auditReplyMax)
		}
	}
	return ""
}

// objectiveFacts is what gummi sees on the branch for itself since the
// objective was set: its commits, the diffstat, and what is uncommitted.
func (ff *FreeformSession) objectiveFacts(ctx context.Context, workDir string, o domain.Objective) string {
	if workDir == "" {
		return "The session has no worktree yet."
	}
	var b strings.Builder
	logArgs := []string{"log", "--oneline", "--no-decorate", "-n", "30"}
	if o.BaseRev != "" {
		logArgs = append(logArgs, o.BaseRev+"..HEAD")
	} else {
		logArgs = append(logArgs, "--since="+o.SetAt.Format(time.RFC3339))
	}
	commits, _ := objectiveGit(ctx, workDir, logArgs...)
	b.WriteString("Commits since the objective was set:\n" + orNone(commits) + "\n\n")
	if o.BaseRev != "" {
		stat, _ := objectiveGit(ctx, workDir, "diff", "--stat", o.BaseRev)
		b.WriteString("Diffstat against where it started (committed and not):\n" + orNone(stat) + "\n\n")
	}
	status, _ := objectiveGit(ctx, workDir, "status", "--porcelain")
	b.WriteString("Uncommitted changes:\n" + orNone(tailRunes(status, 2000)))
	return b.String()
}

// headRev is the branch tip of the session's worktree, "" without one.
func (ff *FreeformSession) headRev(ctx context.Context) string {
	dir := ff.WorkDir()
	if dir == "" {
		return ""
	}
	rev, _ := objectiveGit(ctx, dir, "rev-parse", "HEAD")
	return rev
}

func objectiveGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed git verbs in the card's own worktree
	cmd.Dir = dir
	childproc.Group(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// runCheck runs the objective's check in the worktree: MET counts only if
// it exits 0, and a failure's tail becomes the note.
func (ff *FreeformSession) runCheck(ctx context.Context, command string) domain.CheckResult {
	dir := ff.WorkDir()
	if dir == "" {
		return domain.CheckResult{Ran: true, Tail: "the session has no worktree to run the check in"}
	}
	ctx, cancel := context.WithTimeout(ctx, objectiveCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command) // #nosec G204 -- the person's own check, run where the agent runs commands anyway
	cmd.Dir = dir
	childproc.Group(cmd)
	out, err := cmd.CombinedOutput()
	r := domain.CheckResult{Ran: true, Passed: err == nil}
	if !r.Passed {
		r.Tail = tailLines(strings.TrimSpace(string(out)), 15)
		if r.Tail == "" {
			r.Tail = err.Error()
		}
	}
	ff.noteObjective(fmt.Sprintf("check `%s`: %s", command, map[bool]string{true: "passed", false: "failed"}[r.Passed]))
	return r
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func tailRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

func auditPrompt(o domain.Objective, reply, facts string) string {
	var b strings.Builder
	b.WriteString("A person set an agent this objective, and the agent has just finished a turn. " +
		"Judge whether the objective is met.\n\n")
	b.WriteString("Objective: " + o.Text + "\n")
	if o.Check != "" {
		b.WriteString("If you answer MET, gummi then runs `" + o.Check + "`, and the objective counts as met only if it passes.\n")
	}
	fmt.Fprintf(&b, "Turns gummi has sent toward it so far: %d of %d.\n\n", o.Turns, domain.ObjectiveTurnCap)
	b.WriteString("The agent's last reply:\n<<<\n" + orNone(reply) + "\n>>>\n\n")
	b.WriteString("What gummi sees on the branch:\n" + facts + "\n\n")
	b.WriteString("Answer with exactly one line, in one of these forms:\n" +
		"VERDICT: CONTINUE — <what is left, as one instruction for the agent's next turn>\n" +
		"VERDICT: MET — <the evidence that it is done>\n" +
		"VERDICT: STUCK — <what blocks it that more turns will not fix>\n" +
		"Say MET only when the evidence shows the objective done, not when the agent says it is. " +
		"Say STUCK when the agent is waiting on something only the person can give, or is going in circles.")
	return b.String()
}

func objectiveOpening(o domain.Objective) string {
	s := "Objective: " + o.Text + "\n\nWork toward it until it is done. After each of your turns an auditor checks " +
		"the work against it"
	if o.Check != "" {
		s += " and runs `" + o.Check + "`"
	}
	return s + ", and gummi sends you the next turn itself until it is met. If something only the person " +
		"can decide blocks you, ask them with ask_user rather than guessing. Commit what you mean to keep."
}

func objectiveContinuation(o domain.Objective) string {
	return fmt.Sprintf("Keep working toward the objective: %s\n\nThe auditor's note on your last turn: %s\n\n"+
		"(gummi sent this turn for the objective — turn %d of %d. If something only the person can decide "+
		"blocks you, ask them with ask_user.)", o.Text, o.Note, o.Turns, domain.ObjectiveTurnCap)
}
