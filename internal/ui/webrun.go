package ui

// A session's workflow runs on the web face (DESIGN §25): the run's block
// in the thread (threadfold's fold, fed the session's runs from the
// store), a step opened beside the thread, the session's specs, and what
// a person does to a run — pause, resume, stop, revert, a
// message to a step and the answer to a step's question. Every one is the
// engine's own call, and every word the block says is threadfold's: the
// web face decides nothing about a run.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
)

// childRow is a session's hidden child card (a workflow step's) as a row,
// when its session is on the board: never a row of the board, but its
// thread and live block are read by its card (§25.10).
func (m *Shell) childRow(id domain.FeatureID) (featureRow, bool) {
	f, ok := m.children[id]
	if !ok {
		return featureRow{}, false
	}
	if _, onBoard := m.rowByID(f.Child.Of); !onBoard {
		return featureRow{}, false
	}
	return featureRow{F: f}, true
}

// isRunSession says whether a card runs workflows: a session that is no
// child and no agent in a home of its own.
func isRunSession(f domain.Feature) bool { return hasSessionSpec(f) }

// runInputs reads the session's runs for the thread's fold: each run and
// its steps from the store, its spec's title, the files it has changed
// (from its snapshots, never a fresh one), and the session of each live
// step and of each ended one working again.
//
// A run that ended before start (threadfold.RunLeftBehind) is skipped
// before any of its reads are made; a zero start skips none.
func (d *WebDocs) runInputs(ctx context.Context, start time.Time) []threadfold.RunInput {
	if !isRunSession(d.f) {
		return nil
	}
	runs, err := d.store.SessionRuns(ctx, d.f.ID)
	if err != nil || len(runs) == 0 {
		return nil
	}
	titles := map[int]string{}
	if specs, err := d.store.SessionSpecs(ctx, d.f.ID); err == nil {
		for _, sp := range specs {
			titles[sp.N] = sp.Title
		}
	}
	out := make([]threadfold.RunInput, 0, len(runs))
	always := d.eng != nil && d.eng.WorkflowAlwaysAllows(ctx, d.f.ID)
	for i, r := range runs {
		if threadfold.RunLeftBehind(r.State, r.EndedAt, start) {
			continue
		}
		steps, _ := d.store.RunStepsLite(ctx, d.f.ID, r.N)
		in := threadfold.RunInput{Run: r, Steps: steps, SpecTitle: titles[r.Spec], Live: map[string]threadfold.StepLive{}, Envelope: float64(d.f.Budget.Envelope)}
		in.AlwaysAllowed = always && i == len(runs)-1
		if d.eng != nil {
			in.Files = d.eng.RunFiles(ctx, r, steps)
			in.Phase = d.eng.RunPhase(d.f.ID, r.N)
			for _, a := range d.eng.RunArtifactList(r) {
				in.Artifacts = append(in.Artifacts, threadfold.RunArtifact{Name: a.Name, File: a.File, Exists: a.Exists})
			}
			for _, s := range steps {
				if s.Card == "" {
					continue
				}
				// an ended step is read too: a person's follow-up has it
				// working again
				if now, ok := d.eng.StepNow(s.Card); ok && (s.State.Live() || now.Busy) {
					in.Live[s.Path] = threadfold.StepLive{Busy: now.Busy, Spent: now.Spent, Action: now.Action, Ask: now.Ask, Model: now.Model}
				}
			}
		}
		out = append(out, in)
	}
	return out
}

// runFold reads what the fold of the session's runs needs: the runs the
// conversation as it stands still draws (runInputs, threadfold.CurrentRuns),
// and when a person last said something to the session, which folds the
// run that ended last (threadfold.FoldRuns).
func (d *WebDocs) runFold(ctx context.Context) ([]threadfold.RunInput, time.Time) {
	if d.eng == nil {
		return d.runInputs(ctx, time.Time{}), time.Time{}
	}
	tr := sessionTranscript(d.eng, d.f.ID)
	return d.runInputs(ctx, threadfold.ConversationStart(tr)), threadfold.PersonSaid(tr)
}

// sessionTranscript is the session's conversation as the engine holds it,
// open or ended, nil when it holds none.
func sessionTranscript(eng *engine.Engine, id domain.FeatureID) []engine.Message {
	if ff := eng.Freeform(id); ff != nil {
		return ff.Snapshot().Transcript
	}
	if snap, ok := eng.FreeformHistory(id); ok {
		return snap.Transcript
	}
	return nil
}

// placeRuns places the session's runs among the live conversation's
// turns (webapi.Conversation.Runs), each where it was proposed
// (threadfold.RunPlace); a run proposed before the first of them stays
// among the thread's settled items. at is each run's proposal moment.
func placeRuns(live *webapi.Live, at map[int]time.Time) {
	if live == nil || live.Freeform == nil || len(live.Freeform.Turns) == 0 || len(at) == 0 {
		return
	}
	c := *live.Freeform
	marks := make([]threadfold.TurnMark, len(c.Turns))
	for i, t := range c.Turns {
		marks[i] = threadfold.TurnMark{At: t.Time, Activity: t.Author == "tool" || t.Author == "thinking"}
	}
	ns := make([]int, 0, len(at))
	for n := range at {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	c.Runs = nil
	for _, n := range ns {
		if i := threadfold.RunPlace(marks, at[n]); i >= 0 {
			c.Runs = append(c.Runs, webapi.RunPlace{N: n, Before: i})
		}
	}
	live.Freeform = &c
}

// PlaceLiveRuns places the session's runs among live's conversation
// (placeRuns), reading only when each was proposed: GET …/live's own
// read, off the board's update loop.
func (d *WebDocs) PlaceLiveRuns(ctx context.Context, live *webapi.Live) {
	if live == nil || live.Freeform == nil || !isRunSession(d.f) {
		return
	}
	runs, err := d.store.SessionRuns(ctx, d.f.ID)
	if err != nil {
		return
	}
	at := make(map[int]time.Time, len(runs))
	for _, r := range runs {
		at[r.N] = r.CreatedAt
	}
	placeRuns(live, at)
}

// webRunBlock maps a folded run onto the wire.
func webRunBlock(r *threadfold.Run) *webapi.RunBlock {
	if r == nil {
		return nil
	}
	out := &webapi.RunBlock{
		N: r.N, Rev: r.Rev, Recipe: r.Recipe, Spec: r.Spec, SpecTitle: r.SpecTitle, SpecRev: r.SpecRev,
		State: r.State, Reason: r.Reason, Stopping: r.Stopping, Spent: r.Spent,
		Started: r.Started, Ended: r.Ended, ActiveMs: r.ActiveMs, Resumed: r.Resumed, Phase: r.Phase,
		Strip: []webapi.RunStripEntry{}, Rows: []webapi.RunStepRow{},
		Files: r.Files, Commits: r.Commits, Report: r.Report,
		CanPause: r.CanPause, CanResume: r.CanResume, CanStop: r.CanStop, CanRevert: r.CanRevert,
		CanWithdraw: r.CanWithdraw, CanRetry: r.CanRetry, CanForward: r.CanForward, AlwaysAllowed: r.AlwaysAllowed,
		CanSave: r.CanSave, SaveName: r.SaveName, SavedAs: r.SavedAs,
		Mark: r.Mark, Line: r.Line, Folded: r.Folded,
	}
	var strip func(e threadfold.StripEntry) webapi.RunStripEntry
	strip = func(e threadfold.StripEntry) webapi.RunStripEntry {
		w := webapi.RunStripEntry{ID: e.ID, Kind: e.Kind, State: e.State, Iter: e.Iter, Max: e.Max}
		for _, b := range e.Branches {
			w.Branches = append(w.Branches, strip(b))
		}
		return w
	}
	for _, e := range r.Strip {
		out.Strip = append(out.Strip, strip(e))
	}
	for _, row := range r.Rows {
		w := webapi.RunStepRow{
			Key: row.Key, Path: row.Path, Repeat: row.Repeat, Iter: row.Iter, ID: row.ID, Role: row.Role, State: row.State,
			Spent: row.Spent, Line: row.Line, Flag: row.Flag, Card: row.Card, Folded: row.Folded, Steps: row.Steps,
			Verdict: row.Verdict, Waiting: row.Waiting, Started: row.Started, Ended: row.Ended, Model: row.Model,
		}
		w.Question = webQuestion(row.Question)
		w.Pass, w.Event = row.Pass, row.Event
		if wr := row.Watch; wr != nil {
			w.Watch = &webapi.RunWatch{
				PR: wr.PR, URL: wr.URL, Until: wr.Until, Pending: wr.Pending, Threads: wr.Threads,
				KeptOut: wr.KeptOut, Passes: wr.Passes, Held: wr.Held,
			}
		}
		out.Rows = append(out.Rows, w)
	}
	if p := r.Pending; p != nil {
		out.Pending = &webapi.RunPending{Kind: p.Kind, Question: p.Question, Options: p.Options, Step: p.Step}
	}
	if l := r.Live; l != nil {
		out.Live = &webapi.RunLive{
			Name: l.Name, Mood: l.Mood, Who: l.Who, Action: l.Action, Card: l.Card,
			Question: l.Question, Line: l.Line, CanTopUp: l.CanTopUp, CanResume: l.CanResume,
		}
		for _, st := range l.Steps {
			out.Live.Steps = append(out.Live.Steps, webapi.RunLiveStep{ID: st.ID, Path: st.Path, Card: st.Card, State: st.State})
		}
	}
	for _, a := range r.Artifacts {
		out.Artifacts = append(out.Artifacts, webapi.RunArtifact{Name: a.Name, File: a.File, Exists: a.Exists})
	}
	for _, res := range r.Results {
		out.Results = append(out.Results, webapi.RunResult{
			Item: res.Item, Verdict: res.Verdict, Checked: res.Checked, Detail: res.Detail, Before: res.Before, Change: res.Change,
		})
	}
	if b := r.Baseline; b != nil {
		out.Baseline = &webapi.RunBaseline{Running: b.Running, Err: b.Err}
		for _, it := range b.Items {
			out.Baseline.Items = append(out.Baseline.Items, webapi.RunBaselineItem{Item: it.Item, Verdict: it.Verdict, Line: it.Line, Problem: it.Problem})
		}
	}
	return out
}

// webBaseline maps a proposal's baseline onto the wire, nil for none.
func webBaseline(b *engine.ProposalBaseline) *webapi.RunBaseline {
	if b == nil {
		return nil
	}
	out := &webapi.RunBaseline{Running: b.Running, Err: threadfold.Sanitize(b.Err)}
	for _, it := range b.Items {
		out.Items = append(out.Items, webapi.RunBaselineItem{Item: it.Item, Verdict: string(it.Verdict), Line: it.Line, Problem: it.Problem})
	}
	return out
}

// webQuestion maps a step's question onto the wire, nil for none.
func webQuestion(q *threadfold.StepQuestion) *webapi.StepQuestion {
	if q == nil {
		return nil
	}
	out := &webapi.StepQuestion{Call: q.Call, Question: q.Question, Multi: q.Multi}
	for _, o := range q.Options {
		out.Options = append(out.Options, webapi.QuestionOption{Label: o.Label, Detail: o.Detail, Recommended: o.Recommended})
	}
	return out
}

// ---------------------------------------------------------------- specs

// Specs is GET /api/cards/{id}/specs: the session's specs, each with its
// state, newest revision, runs and done-when count.
func (d *WebDocs) Specs(ctx context.Context) (webapi.SpecList, error) {
	out := webapi.SpecList{Specs: []webapi.SpecEntry{}}
	if !isRunSession(d.f) {
		return out, ErrNoCard
	}
	out.CanStart = d.eng != nil && d.f.Stage != domain.StageDone
	specs, err := d.store.SessionSpecs(ctx, d.f.ID)
	if err != nil {
		return out, err
	}
	runs, err := d.store.SessionRuns(ctx, d.f.ID)
	if err != nil {
		return out, err
	}
	for _, sp := range specs {
		e := webapi.SpecEntry{N: sp.N, Title: sp.Title, State: string(sp.State)}
		if revs, _ := d.store.SpecRevisions(ctx, d.f.ID, sp.N); len(revs) > 0 {
			e.Rev = revs[0].Rev
		}
		if path, ok := d.store.SessionSpecFile(ctx, d.pool.Root(), d.f.ID, sp.N); ok {
			if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- the session's own spec file
				if items, _, err := spec.ParseDoneWhen(string(raw)); err == nil {
					e.DoneWhen = len(items)
				}
			}
		}
		var last *domain.Run
		for i := range runs {
			r := &runs[i]
			if r.Spec != sp.N || r.State == domain.RunDenied {
				continue
			}
			e.Runs = append(e.Runs, d.specRun(ctx, *r))
			if r.State.Open() {
				e.Frozen = r.SpecRev
			}
			if r.State.Ended() {
				last = r
			}
		}
		if last != nil {
			for _, res := range last.Results {
				if res.Verdict == domain.ItemMet {
					e.Met++
				}
			}
		}
		out.Specs = append(out.Specs, e)
	}
	return out, nil
}

// specRun is a run as its spec lists it: its recipe and state, its
// spend, and once ended its glyph, its checked items met and when it
// ended — the words its folded line in the thread says (threadfold).
func (d *WebDocs) specRun(ctx context.Context, r domain.Run) webapi.SpecRun {
	steps, _ := d.store.RunStepsLite(ctx, d.f.ID, r.N)
	f := threadfold.FoldRun(threadfold.RunInput{Run: r, Steps: steps})
	out := webapi.SpecRun{N: r.N, Recipe: f.Recipe, State: f.State, Spent: f.Spent, Mark: f.Mark}
	if r.State.Ended() {
		out.Met, out.Checked = threadfold.RunMet(f)
		out.Ended = r.EndedAt
	}
	return out
}

// sessionSpecRuns are spec n's runs, newest first, the ones never run
// left out.
func (d *WebDocs) sessionSpecRuns(ctx context.Context, runs []domain.Run, n int) []webapi.SpecRun {
	var out []webapi.SpecRun
	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; r.Spec == n && r.State != domain.RunDenied {
			out = append(out, d.specRun(ctx, r))
		}
	}
	return out
}

// ---------------------------------------------------------------- a step

// step finds the step whose card is card, on this session: the step, its
// run and the step card.
func (d *WebDocs) step(ctx context.Context, card string) (domain.RunStep, domain.Run, domain.Feature, error) {
	if !isRunSession(d.f) {
		return domain.RunStep{}, domain.Run{}, domain.Feature{}, ErrNoCard
	}
	st, err := d.store.RunStepByCard(ctx, domain.FeatureID(card))
	if err != nil || st == nil || st.Session != d.f.ID {
		return domain.RunStep{}, domain.Run{}, domain.Feature{}, ErrNoCard
	}
	run, err := d.store.Run(ctx, d.f.ID, st.Run)
	if err != nil || run == nil {
		return domain.RunStep{}, domain.Run{}, domain.Feature{}, ErrNoCard
	}
	f, err := d.store.GetFeature(ctx, st.Card)
	if err != nil {
		return domain.RunStep{}, domain.Run{}, domain.Feature{}, ErrNoCard
	}
	return *st, *run, f, nil
}

// Step is GET /api/cards/{id}/steps/{card}: the step's head, its brief
// and its artifacts. Its transcript is its card's own thread.
func (d *WebDocs) Step(ctx context.Context, card string) (webapi.StepFace, error) {
	st, run, f, err := d.step(ctx, card)
	if err != nil {
		return webapi.StepFace{}, err
	}
	block := threadfold.FoldRun(threadfold.RunInput{Run: run})
	out := webapi.StepFace{
		Card: string(st.Card), Session: string(d.f.ID), Run: run.N, Recipe: block.Recipe, Path: st.Path, ID: st.StepID,
		Role: string(st.Role), Slot: st.Slot, State: string(st.State), Reason: threadfold.Sanitize(st.Reason),
		Spent: st.Spent, Started: st.StartedAt, Ended: st.EndedAt, Live: st.State.Live(),
		Brief: threadfold.Sanitize(st.Prompt), Output: threadfold.Sanitize(st.Output),
	}
	out.Crumb = stepCrumb(block.Recipe, run.N, st)
	out.Model = f.SessionModel
	if out.Model == "" && f.SessionBackend != "" {
		out.Model = f.SessionBackend + "'s default model"
	}
	if d.eng != nil {
		if now, ok := d.eng.StepNow(st.Card); ok {
			if now.Model != "" {
				out.Model = now.Model
			}
			if st.State.Live() && now.Spent > out.Spent {
				out.Spent = now.Spent
			}
			if st.State.Live() && now.Ask != nil {
				out.Question = webQuestion(threadfold.AskQuestion(now.Ask))
			}
		}
		for _, a := range d.eng.StepArtifacts(run, st) {
			out.Artifacts = append(out.Artifacts, webapi.StepArtifact{Name: a.Name, Path: d.rel(a.Path), Exists: a.Exists, Writes: a.Writes})
		}
	}
	out.Says = "a follow-up in " + st.StepID + "’s conversation: it may change the tree, and re-runs nothing after it"
	if st.State.Live() {
		out.Says = "a turn to " + st.StepID + " while it runs: its output is whatever it says last"
	}
	out.CanRevert = st.StartTree != "" && !st.State.Live() && run.State != domain.RunRunning &&
		run.RevertedTo != domain.RevertedToStart && (d.eng == nil || d.eng.RunPhase(d.f.ID, run.N) == "")
	return out, nil
}

// stepCrumb is the way back to a step: the session, the run, the
// iteration it sits in, the step.
func stepCrumb(recipe string, n int, st domain.RunStep) []string {
	crumb := []string{"Session", fmt.Sprintf("%s run %d", recipe, n)}
	parts := strings.Split(st.Path, "/")
	for i := 1; i < len(parts)-1; i++ {
		var k int
		if _, err := fmt.Sscanf(parts[i], "%d", &k); err == nil && k > 0 {
			crumb = append(crumb, fmt.Sprintf("%s ↺ %d", parts[i-1], k))
		}
	}
	return append(crumb, st.StepID)
}

// StepDiff is GET /api/cards/{id}/steps/{card}/diff: the step's own
// diff, from its start snapshot to its end one (to now while it runs).
func (d *WebDocs) StepDiff(ctx context.Context, card string) (webapi.Diff, error) {
	out := webapi.Diff{Files: []webapi.DiffFile{}, Annotations: []webapi.Annotation{}}
	st, _, _, err := d.step(ctx, card)
	if err != nil {
		return out, err
	}
	if d.eng == nil {
		return out, ErrDetached
	}
	if st.StartTree == "" {
		out.Why = "the step has not started"
		return out, nil
	}
	patch, _, err := d.eng.StepDiff(ctx, st)
	if err != nil {
		return out, err
	}
	out.BaseRev, out.Rev = shortTree(domain.PrimaryTree(st.StartTree)), shortTree(domain.PrimaryTree(st.EndTree))
	for _, f := range diffannot.Parse(diffannot.Lines(patch)) {
		out.Files = append(out.Files, webDiffFile(f, nil, false))
	}
	if len(out.Files) == 0 {
		out.Why = "the step has changed nothing in the tree"
	}
	return out, nil
}

func shortTree(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// StepSend is POST /api/cards/{id}/steps/{card}/send: a person's message
// to the step's agent, a turn on its card (§25.10).
func (d *WebDocs) StepSend(ctx context.Context, card, text, person string) (webapi.StepFace, error) {
	if _, _, _, err := d.step(ctx, card); err != nil {
		return webapi.StepFace{}, err
	}
	if d.eng == nil {
		return webapi.StepFace{}, ErrDetached
	}
	if err := closedToReview(d.f, "its steps take no more messages"); err != nil {
		return webapi.StepFace{}, err
	}
	if strings.TrimSpace(text) == "" {
		return webapi.StepFace{}, invalid("a message needs some text")
	}
	if err := d.eng.StepSend(engine.WithActor(ctx, state.PersonActor(person)), domain.FeatureID(card), text); err != nil {
		return webapi.StepFace{}, engineRefusal(err)
	}
	return d.Step(ctx, card)
}

// StepAnswer is POST /api/cards/{id}/steps/{card}/answer: the answer to
// the step's open question, one of its options or the person's words.
func (d *WebDocs) StepAnswer(ctx context.Context, card, call, answer, person string) error {
	if _, _, _, err := d.step(ctx, card); err != nil {
		return err
	}
	if d.eng == nil {
		return ErrDetached
	}
	if strings.TrimSpace(answer) == "" {
		return invalid("an answer needs some text")
	}
	return engineRefusal(d.eng.AnswerCall(ctx, domain.FeatureID(card), call, answer, state.PersonActor(person)))
}

// ---------------------------------------------------------------- the run

// RunAct is POST /api/cards/{id}/runs/{n}/{pause,resume,stop,withdraw,
// askagain,forward}: a person pausing, resuming or stopping the session's
// open run, which must be run n; withdrawing run n while it is queued;
// taking back their always-allow of the session's runs; or forwarding to
// run n's watch what its trust filter kept out.
func (d *WebDocs) RunAct(ctx context.Context, n int, act, person string) error {
	if !isRunSession(d.f) {
		return ErrNoCard
	}
	if d.eng == nil {
		return ErrDetached
	}
	open, err := d.store.OpenRun(ctx, d.f.ID)
	if err != nil {
		return err
	}
	switch act {
	case "withdraw":
		return engineRefusal(d.eng.WorkflowWithdraw(ctx, d.f.ID, n, "by "+personWord(person)))
	case "askagain":
		return engineRefusal(d.eng.WorkflowAskAgain(ctx, d.f.ID))
	case "retry":
		return engineRefusal(d.eng.WorkflowRetry(ctx, d.f.ID, n))
	case "forward":
		return engineRefusal(d.eng.WorkflowForward(ctx, d.f.ID, n, personWord(person)))
	}
	if open == nil || open.N != n {
		return &conflictError{msg: fmt.Sprintf("run %d is not open", n)}
	}
	switch act {
	case "pause":
		err = d.eng.WorkflowPause(ctx, d.f.ID)
	case "resume":
		err = d.eng.WorkflowResume(ctx, d.f.ID)
	case "stop":
		err = d.eng.WorkflowStop(ctx, d.f.ID, "stopped by "+personWord(person))
	default:
		return invalid("no such act %q on a run", act)
	}
	return engineRefusal(err)
}

func personWord(person string) string {
	if person = strings.TrimSpace(person); person != "" {
		return person
	}
	return "the person"
}

// RevertPreview is GET /api/cards/{id}/runs/{n}/revert: what reverting
// run n would put back.
func (d *WebDocs) RevertPreview(ctx context.Context, n int) (webapi.RevertPreview, error) {
	return d.RevertPreviewTo(ctx, n, "")
}

// RevertPreviewTo is RevertPreview to where step started (?step=), the
// run's start when step is "".
func (d *WebDocs) RevertPreviewTo(ctx context.Context, n int, step string) (webapi.RevertPreview, error) {
	if !isRunSession(d.f) {
		return webapi.RevertPreview{}, ErrNoCard
	}
	if d.eng == nil {
		return webapi.RevertPreview{}, ErrDetached
	}
	p, err := d.eng.WorkflowRevertPreviewTo(ctx, d.f.ID, n, step)
	if err != nil {
		return webapi.RevertPreview{}, engineRefusal(err)
	}
	return webapi.RevertPreview{Run: p.Run, Step: p.Step, Paths: nonNil(p.Paths), Tree: p.Tree, Note: p.Note}, nil
}

// Revert is POST /api/cards/{id}/runs/{n}/revert: the person's confirmed
// revert of run n, against the tree its preview was read from (tree).
func (d *WebDocs) Revert(ctx context.Context, n int, tree string) (webapi.RevertDone, error) {
	return d.RevertTo(ctx, n, "", tree)
}

// RevertTo is Revert to where step started, the run's start when step is
// "".
func (d *WebDocs) RevertTo(ctx context.Context, n int, step, tree string) (webapi.RevertDone, error) {
	if !isRunSession(d.f) {
		return webapi.RevertDone{}, ErrNoCard
	}
	if d.eng == nil {
		return webapi.RevertDone{}, ErrDetached
	}
	if err := closedToReview(d.f, "its tree is no run's to put back"); err != nil {
		return webapi.RevertDone{}, err
	}
	paths, err := d.eng.WorkflowRevertTo(ctx, d.f.ID, n, step, tree)
	if err != nil {
		return webapi.RevertDone{}, engineRefusal(err)
	}
	return webapi.RevertDone{Run: n, Paths: nonNil(paths)}, nil
}

// SaveRecipe is POST /api/cards/{id}/runs/{n}/save: the person saving
// run n's inline recipe as the workspace recipe name. It is a person's
// act: no agent's tool reaches it.
func (d *WebDocs) SaveRecipe(ctx context.Context, n int, name string) (webapi.SaveRecipeDone, error) {
	if !isRunSession(d.f) {
		return webapi.SaveRecipeDone{}, ErrNoCard
	}
	if d.eng == nil {
		return webapi.SaveRecipeDone{}, ErrDetached
	}
	if strings.TrimSpace(name) == "" {
		return webapi.SaveRecipeDone{}, invalid("a recipe needs a name")
	}
	path, err := d.eng.WorkflowSaveRecipe(ctx, d.f.ID, n, name)
	if err != nil {
		return webapi.SaveRecipeDone{}, engineRefusal(err)
	}
	return webapi.SaveRecipeDone{Run: n, Name: strings.TrimSpace(name), Path: d.rel(path)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// webProposal is a run's open proposal or revised plan as its decision's
// card, nil for any other decision.
func (m *Shell) webProposal(ctx context.Context, f domain.Feature) *webapi.RunProposal {
	if m.engine == nil || !isRunSession(f) {
		return nil
	}
	run, d := m.engine.WorkflowDecision(ctx, f.ID)
	if run == nil || d == nil || (d.Kind != domain.RunDecideProposal && d.Kind != domain.RunDecidePlan) {
		return nil
	}
	p, err := m.engine.WorkflowProposal(ctx, f.ID, run.N)
	if err != nil {
		return nil
	}
	out := &webapi.RunProposal{
		Run: p.Run, Kind: string(p.Kind), Recipe: threadfold.Sanitize(p.Recipe), Description: threadfold.Sanitize(p.Description),
		Spec: p.Spec, SpecTitle: threadfold.Sanitize(p.SpecTitle), SpecRev: p.SpecRev, Steps: []webapi.ProposalStep{},
		Warnings: p.Warnings, PoolLeft: p.PoolLeft, Bounded: p.Bounded, Dirty: p.Dirty, Branch: p.Branch,
		Baseline: webBaseline(p.Baseline),
	}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, webProposalStep(s))
	}
	for _, s := range p.Replaces {
		out.Replaces = append(out.Replaces, webProposalStep(s))
	}
	for _, c := range p.Checks {
		out.Checks = append(out.Checks, webapi.ProposalCheck{Item: c.Item, Says: threadfold.Sanitize(c.Says), Cmd: threadfold.Sanitize(c.Cmd)})
	}
	return out
}

// webProposalStep is one entry of a proposal's step tree as the page reads
// it.
func webProposalStep(s engine.ProposalStep) webapi.ProposalStep {
	return webapi.ProposalStep{
		ID: s.ID, Depth: s.Depth, Kind: s.Kind, Role: string(s.Role), Slot: s.Slot, Model: threadfold.Sanitize(s.Model),
		Prompt: threadfold.Sanitize(s.Prompt), Max: s.Max, Until: s.Until, OnMax: string(s.OnMax), Join: string(s.Join),
	}
}

// RunDiff is GET /api/cards/{id}/runs/{n}/diff: run n's whole diff, from
// the tree it started on to the tree now, as the Changes tab draws one.
func (d *WebDocs) RunDiff(ctx context.Context, n int) (webapi.Diff, error) {
	if !isRunSession(d.f) {
		return webapi.Diff{}, ErrNoCard
	}
	if d.eng == nil {
		return webapi.Diff{}, ErrDetached
	}
	patch, _, err := d.eng.RunDiff(ctx, d.f.ID, n)
	if err != nil {
		return webapi.Diff{}, engineRefusal(err)
	}
	out := webapi.Diff{Files: []webapi.DiffFile{}, Annotations: []webapi.Annotation{}}
	for _, f := range diffannot.Parse(diffannot.Lines(patch)) {
		out.Files = append(out.Files, webDiffFile(f, nil, false))
	}
	if len(out.Files) == 0 {
		out.Why = fmt.Sprintf("the tree is as run %d found it", n)
	}
	return out, nil
}

// RunArtifact is GET /api/cards/{id}/runs/{n}/artifacts/{name}: one
// artifact run n's recipe declares, as text.
func (d *WebDocs) RunArtifact(ctx context.Context, n int, name string) (webapi.RunArtifactText, error) {
	if !isRunSession(d.f) {
		return webapi.RunArtifactText{}, ErrNoCard
	}
	if d.eng == nil {
		return webapi.RunArtifactText{}, ErrDetached
	}
	a, text, cut, err := d.eng.ReadRunArtifact(ctx, d.f.ID, n, name)
	if err != nil {
		return webapi.RunArtifactText{}, &conflictError{msg: err.Error()}
	}
	return webapi.RunArtifactText{Name: a.Name, File: a.File, Text: threadfold.Sanitize(text), Cut: cut}, nil
}
