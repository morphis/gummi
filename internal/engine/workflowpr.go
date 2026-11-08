package engine

// A run that publishes its PR and waits on it (DESIGN §28). A pr step is
// one agentpublish act; a watch step polls the session's PR without a
// model, files what it reads (the trust filter applied) in its row's
// memory, and the conductor (workflowpolicy) decides from that memory when
// a pass of the recipe's handler for an event runs, when gummi pushes
// after it, and when the person is asked.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agentpublish"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/workflowpolicy"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

const (
	watchPollMin = 30 * time.Second
	watchPollMax = 5 * time.Minute
	// watchTransientReads is how many failed reads in a row are put down to
	// the network before the person is asked.
	watchTransientReads = 3
	// watchPushLag is how long after a push a read that still shows the old
	// head is taken as GitHub being behind.
	watchPushLag = 2 * time.Minute
	// eventTextCap bounds the {{event}} text a pass is given.
	eventTextCap = 32 << 10
	feedbackCap  = 4000
)

// Answers to a watch's question, as the person reads them.
const (
	WatchMoreRun   = "Run one more pass"
	WatchKeepWait  = "Keep waiting without acting"
	WatchReadAgain = "Try again"
)

// runPublishHost lends agentpublish the run's own acts: the run holds the
// tree, so the "a workflow run is working here" refusal is the run itself.
type runPublishHost struct{ publishHost }

func (runPublishHost) TreeBusy(context.Context, domain.FeatureID) string { return "" }

func (e *Engine) runHost() runPublishHost { return runPublishHost{publishHost{e}} }

func prSteps(r domain.Recipe) []domain.Step {
	var out []domain.Step
	for _, s := range workflowrecipe.Steps(r) {
		if s.PR != nil || s.Watch != nil {
			out = append(out, s)
		}
	}
	return out
}

// --- pr steps -----------------------------------------------------------------

func (e *Engine) runPRStep(ctx context.Context, run *domain.Run, sess domain.Feature, inst *domain.RunStep, step domain.Step) error {
	fail := func(why string) error {
		e.endStep(ctx, inst, domain.StepFailed, why, "", e.endTreeOf(ctx, run))
		return nil
	}
	if !e.AgentMayPublish(ctx, sess.ID) {
		return fail(agentpublish.NotAllowed().Error())
	}
	out, perr := agentpublish.Do(ctx, e.runHost(), sess, agentpublish.Act(step.PR.Act), agentpublish.Args{Draft: step.PR.Draft})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if perr != nil {
		return fail(perr.Error())
	}
	e.endStep(ctx, inst, domain.StepDone, "", out.Text, e.endTreeOf(ctx, run))
	return nil
}

// --- the watch step's start -----------------------------------------------------

func (e *Engine) startWatch(ctx context.Context, run *domain.Run, sess domain.Feature, inst *domain.RunStep, step domain.Step, vars workflowrecipe.Vars) error {
	fail := func(why string) error {
		e.endStep(ctx, inst, domain.StepFailed, why, "", e.endTreeOf(ctx, run))
		return nil
	}
	cur, err := e.cfg.Store.GetFeature(ctx, sess.ID)
	if err != nil {
		return err
	}
	if cur.PullRequest.Empty() {
		return fail("this session has no pull request to watch; a pr: create step before the watch opens one")
	}
	text, err := workflowrecipe.FillPlain(step.Watch.Trust, vars)
	if err != nil {
		return fail("trust: " + err.Error())
	}
	trust, err := workflowrecipe.ParseTrust(text)
	if err != nil {
		return fail("trust: " + err.Error())
	}
	unlock := e.lockRuns(run.Session)
	defer unlock()
	row, err := e.cfg.Store.RunStep(ctx, run.Session, run.N, inst.Path)
	if err != nil || row == nil || !row.State.Live() {
		return err
	}
	now := e.now()
	row.State = domain.StepWatching
	row.Watch = &domain.WatchState{PR: cur.PullRequest, Trust: trust, LastChange: now, NextPoll: now}
	if err := e.cfg.Store.UpdateRunStep(ctx, row); err != nil {
		return err
	}
	e.runNote(run.Session, fmt.Sprintf("run %d: %s waits on the PR", run.N, inst.Path))
	return nil
}

// --- finding a step's definition by path ---------------------------------------

// entriesFor are the entries the step at path is defined in: the recipe's,
// or for a step in a watch's pass the handler's.
func (e *Engine) entriesFor(ctx context.Context, run *domain.Run, path string) []domain.Entry {
	w, ok := run.Recipe.WatchStep()
	if !ok {
		return run.Recipe.Steps
	}
	rest, under := strings.CutPrefix(path, w.ID+"/")
	if !under {
		return run.Recipe.Steps
	}
	seg, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(seg)
	if err != nil {
		return run.Recipe.Steps
	}
	row, err := e.cfg.Store.RunStep(ctx, run.Session, run.N, w.ID)
	if err != nil || row == nil || row.Watch == nil || n < 1 || n > len(row.Watch.Passes) {
		return run.Recipe.Steps
	}
	return run.Recipe.PassEntries(w.ID, row.Watch.Passes[n-1].Event)
}

// stepDef is the step definition id at path.
func (e *Engine) stepDef(ctx context.Context, run *domain.Run, path, id string) (domain.Step, bool) {
	return workflowrecipe.Find(domain.Recipe{Steps: e.entriesFor(ctx, run, path)}, id)
}

// passVars sets the event a pass's step is given: {{event}} for a handler's
// steps, {{watch.event}} where a goto pass re-runs the recipe's own, and the
// event input of a recipe handler that declares one.
func (e *Engine) passVars(ctx context.Context, run *domain.Run, path string, vars *workflowrecipe.Vars) {
	w, ok := run.Recipe.WatchStep()
	if !ok {
		return
	}
	rest, under := strings.CutPrefix(path, w.ID+"/")
	if !under {
		return
	}
	seg, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(seg)
	row, rerr := e.cfg.Store.RunStep(ctx, run.Session, run.N, w.ID)
	if err != nil || rerr != nil || row == nil || row.Watch == nil || n < 1 || n > len(row.Watch.Passes) {
		return
	}
	p := row.Watch.Passes[n-1]
	h := w.Watch.On[p.Event]
	switch h.Form() {
	case "goto":
		vars.WatchEvent = p.Text
	case "recipe":
		if child, err := workflowrecipe.Parse([]byte(h.RecipeYAML)); err == nil {
			if _, declared := child.Inputs["event"]; declared {
				given := map[string]string{"event": p.Text}
				inputs := map[string]string{}
				for k, v := range vars.Inputs {
					if _, d := child.Inputs[k]; d {
						inputs[k] = v
					}
				}
				for k, v := range given {
					inputs[k] = v
				}
				if resolved, err := workflowrecipe.ResolveInputs(child, inputs); err == nil {
					vars.Inputs = resolved
				}
			}
		}
		vars.Event = p.Text
	default:
		vars.Event = p.Text
	}
}

// --- polling -------------------------------------------------------------------

func (e *Engine) pollBounds() (lo, hi time.Duration) {
	if d := e.cfg.WatchPollEvery; d > 0 {
		return d, d
	}
	return watchPollMin, watchPollMax
}

func watchKey(session domain.FeatureID, n int, path string) string {
	return fmt.Sprintf("watch/%s/%d/%s", session, n, path)
}

// pollWatches starts a read of every watching row that is due, off the tick.
func (e *Engine) pollWatches(run *domain.Run, steps []domain.RunStep) {
	if run.State != domain.RunRunning || run.Stopping {
		return
	}
	now := e.now()
	for _, s := range steps {
		if s.State == domain.StepWatching && s.Watch != nil && !now.Before(s.Watch.NextPoll) {
			e.pollWatchAsync(run, s)
		}
	}
}

func (e *Engine) pollWatchAsync(run *domain.Run, row domain.RunStep) {
	rt := e.wfRT()
	session, n, path := run.Session, run.N, row.Path
	key := watchKey(session, n, path)
	if !rt.claim(key) {
		return
	}
	spec, ok := run.Recipe.WatchStep()
	if !ok {
		rt.release(key)
		return
	}
	prev := *row.Watch
	if !e.goWorkflow(func() {
		changed := false
		defer func() {
			rt.release(key)
			if changed {
				e.workflowEvent(session)
			}
		}()
		ctx := e.lifetime()
		sess, err := e.cfg.Store.GetFeature(ctx, session)
		if err != nil {
			return
		}
		facts, transient := e.readWatch(ctx, sess, spec.Watch, &prev)
		unlock := e.lockRuns(session)
		defer unlock()
		cur, err := e.cfg.Store.RunStep(ctx, session, n, path)
		if err != nil || cur == nil || cur.State != domain.StepWatching || cur.Watch == nil {
			return
		}
		// a person's answer landed while this read was out: it read the
		// world as the answer found it, so its result would undo the answer
		// (the next poll reads again)
		if cur.Watch.Asked != prev.Asked || len(cur.Watch.Answers) != len(prev.Answers) {
			return
		}
		changed = e.applyRead(cur.Watch, facts, transient)
		_ = e.cfg.Store.UpdateRunStepWatch(ctx, cur)
	}) {
		rt.release(key)
	}
}

// readWatch reads the PR (DESIGN §28.2): the cheap status first, the checks
// only when the head moved or one was pending, the feedback only when the PR
// changed — through the allowance, and the trust filter applied here and
// nowhere else. transient marks a failed read worth trying again before the
// person is asked.
func (e *Engine) readWatch(ctx context.Context, sess domain.Feature, spec *domain.Watch, w *domain.WatchState) (domain.WatchFacts, bool) {
	ctx, cancel := context.WithTimeout(ctx, agentpublish.Timeout)
	defer cancel()
	facts := domain.WatchFacts{ReadAt: e.now()}
	ref, perr := agentpublish.WatchTarget(ctx, e.runHost(), sess)
	if perr != nil {
		facts.ReadError = perr.Error()
		return facts, false
	}
	gh := pr.GHBinary()
	view, err := pr.WatchStatus(ctx, gh, ref)
	if err != nil {
		facts.ReadError = "reading the PR: " + err.Error()
		return facts, true
	}
	facts.State, facts.Head, facts.Base, facts.BaseRef = view.State, view.Head, view.Base, view.BaseRef
	facts.Mergeable, facts.MergeState, facts.ReviewDecision, facts.UpdatedAt = view.Mergeable, view.MergeState, view.ReviewDecision, view.UpdatedAt
	prev := w.Observed
	if prev != nil && prev.ReadError != "" {
		prev = nil
	}
	checksAt := time.Time{}
	if prev != nil {
		facts.FetchedAt = prev.FetchedAt
		if checksAt = prev.ChecksAt; checksAt.IsZero() {
			checksAt = prev.FetchedAt
		}
		facts.ChecksAt = checksAt
	}
	// the checks are read again when the head moved, one was pending, none
	// had reported, the PR was touched, or a while has passed
	if prev == nil || prev.ChecksHead != facts.Head || hasBucket(prev.Checks, domain.WatchCheckPending) ||
		(len(prev.Checks) == 0 && facts.ReadAt.Sub(checksAt) > time.Minute) ||
		prev.UpdatedAt != facts.UpdatedAt || facts.ReadAt.Sub(checksAt) > watchPollMax {
		// a repository with no CI is asked a minute apart, not every poll
		facts.ChecksAt = facts.ReadAt
		checks, err := pr.FetchChecks(ctx, gh, ref)
		if err != nil {
			facts.ReadError = "reading the PR's checks: " + err.Error()
			return facts, true
		}
		facts.ChecksHead = checks.HeadSHA
		if facts.ChecksHead == "" {
			facts.ChecksHead = facts.Head
		}
		if len(checks.Failing()) > 0 && checks.Count(pr.CheckPending) == 0 {
			// a failing check whose log was read on this same head keeps it:
			// a comment on a red PR does not download the logs again
			logs := map[string]string{}
			if prev != nil && prev.ChecksHead == checks.HeadSHA {
				for _, c := range prev.Checks {
					if c.Log != "" {
						logs[c.Name] = c.Log
					}
				}
			}
			read := pr.Checks{HeadSHA: checks.HeadSHA}
			for _, it := range checks.Items {
				if _, have := logs[it.Name]; !have && it.Bucket == pr.CheckFail && (len(spec.Checks) == 0 || slices.Contains(spec.Checks, it.Name)) {
					read.Items = append(read.Items, it)
				}
			}
			if len(read.Items) > 0 {
				pr.FetchFailedLogs(ctx, gh, &read)
			}
			for _, it := range read.Items {
				logs[it.Name] = it.Log
			}
			for i := range checks.Items {
				checks.Items[i].Log = logs[checks.Items[i].Name]
			}
		}
		for _, c := range checks.Items {
			facts.Checks = append(facts.Checks, domain.WatchCheck{Name: c.Name, Bucket: c.Bucket, URL: c.URL, Log: c.Log})
		}
	} else {
		facts.ChecksHead, facts.Checks = prev.ChecksHead, prev.Checks
	}
	if prev == nil || prev.UpdatedAt != facts.UpdatedAt || prev.Head != facts.Head || prev.Unreadable != "" ||
		prev.FetchedGen != w.ForwardGen || facts.ReadAt.Sub(prev.FetchedAt) > watchPollMax {
		facts.FetchedAt, facts.FetchedGen = facts.ReadAt, w.ForwardGen
		items, unread, err := pr.FetchFeedback(ctx, gh, ref)
		if err != nil {
			facts.ReadError = "reading the PR's feedback: " + err.Error()
			return facts, true
		}
		e.applyTrust(ctx, gh, ref, w.Trust, w.Forwarded, items, &facts)
		facts.Unread = unread
	} else {
		facts.Feedback, facts.KeptOut, facts.KeptOutByKind, facts.Threads = prev.Feedback, prev.KeptOut, prev.KeptOutByKind, prev.Threads
		facts.Unread, facts.KeptOutPrints, facts.FetchedGen = prev.Unread, prev.KeptOutPrints, prev.FetchedGen
	}
	return facts, false
}

func hasBucket(cs []domain.WatchCheck, bucket string) bool {
	for _, c := range cs {
		if c.Bucket == bucket {
			return true
		}
	}
	return false
}

// applyTrust sorts the feedback by the trust filter: an item is trusted
// when everyone who wrote or last edited any part of it is inside the
// filter. A lookup that fails leaves the filter unreadable and the feedback
// empty — never a guess. The lookups are made for this read alone, so
// membership is read again at every event.
func (e *Engine) applyTrust(ctx context.Context, gh string, ref domain.PullRequestRef, t domain.TrustFilter, forwarded []string, items []pr.FeedbackItem, facts *domain.WatchFacts) {
	viewer := ""
	teams := map[domain.OrgTeam][]string{}
	perms := map[string]string{}
	var failure string
	trusted := func(login string) bool {
		login = strings.ToLower(login)
		if login == "" {
			return false // a deleted account is nobody the filter names
		}
		if slices.Contains(t.Logins, login) {
			return true
		}
		if t.Self {
			if viewer == "" {
				v, err := pr.Viewer(ctx, gh)
				if err != nil {
					failure = "the login gummi publishes as could not be read: " + err.Error()
					return false
				}
				viewer = strings.ToLower(v)
			}
			if login == viewer {
				return true
			}
		}
		for _, tm := range t.Teams {
			members, ok := teams[tm]
			if !ok {
				m, err := pr.TeamMembers(ctx, gh, tm.Org, tm.Team)
				if err != nil {
					failure = fmt.Sprintf("members of @%s/%s could not be read: %v (gh needs read:org)", tm.Org, tm.Team, err)
					return false
				}
				members, teams[tm] = m, m
			}
			if slices.Contains(members, login) {
				return true
			}
		}
		if t.MinRole != "" {
			perm, ok := perms[login]
			if !ok {
				p, err := pr.Permission(ctx, gh, ref.Repo, login)
				switch {
				case err == pr.ErrNotCollaborator:
					p = ""
				case err != nil:
					failure = fmt.Sprintf("@%s's permission on %s could not be read: %v", login, ref.Repo, err)
					return false
				}
				perm, perms[login] = p, p
			}
			return pr.PermissionAtLeast(perm, t.MinRole)
		}
		return false
	}
	facts.Feedback, facts.KeptOut, facts.KeptOutByKind, facts.Threads, facts.Unreadable = nil, 0, nil, 0, ""
	facts.KeptOutPrints = nil
	for _, it := range items {
		if it.Kind == "thread" {
			facts.Threads++
		}
		if it.Kind == "review" && it.State != "CHANGES_REQUESTED" && strings.TrimSpace(it.Body) == "" {
			continue // an approval or a plain review without words says nothing to act on
		}
		// a thread read only in part has people the filter never saw: kept
		// out, unless a person forwarded it as it stands
		fp := pr.FeedbackPrint(it)
		fwd := slices.Contains(forwarded, fp)
		ok := fwd || !it.Partial
		for _, who := range it.People {
			if fwd {
				break
			}
			if !trusted(who) {
				ok = false
				break
			}
		}
		if failure != "" {
			facts.Feedback, facts.KeptOut, facts.KeptOutByKind, facts.KeptOutPrints = nil, 0, nil, nil
			facts.Unreadable = failure
			return
		}
		if !ok {
			facts.KeptOut++
			if facts.KeptOutByKind == nil {
				facts.KeptOutByKind = map[string]int{}
			}
			facts.KeptOutByKind[it.Kind]++
			facts.KeptOutPrints = append(facts.KeptOutPrints, fp)
			continue
		}
		body := it.Body
		if len(body) > feedbackCap {
			body = body[:runeCut(body, feedbackCap)] + "…"
		}
		facts.Feedback = append(facts.Feedback, domain.WatchFeedback{
			ID: it.ID, Kind: it.Kind, Author: first(it.People), URL: it.URL, Body: body, State: it.State,
		})
	}
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// applyRead files a read in the watch's memory: what changed moves
// LastChange (quiet runs from it) and resets the backoff; a stable PR is
// read less and less often.
func (e *Engine) applyRead(w *domain.WatchState, f domain.WatchFacts, transient bool) bool {
	now := e.now()
	lo, hi := e.pollBounds()
	if f.ReadError != "" && transient {
		if w.ReadFails++; w.ReadFails < watchTransientReads {
			w.NextPoll = now.Add(lo)
			return false
		}
	}
	if f.ReadError == "" {
		w.ReadFails = 0
		if w.AwaitHead != "" {
			if f.Head == w.AwaitHead || f.State != "OPEN" || now.Sub(w.AwaitSince) > watchPushLag {
				w.AwaitHead = ""
			} else {
				// GitHub still shows the head from before the push
				w.NextPoll = now.Add(lo)
				return false
			}
		}
	}
	prev := w.Observed
	if f.ReadError != "" && prev != nil {
		why := f.ReadError
		f = *prev
		f.ReadAt = now
		if w.ReadFails > 0 {
			f.ReadError = "the PR could not be read for " + strconv.Itoa(w.ReadFails) + " reads in a row"
		} else {
			f.ReadError = why
		}
	}
	changed := prev == nil || watchChanged(prev, &f)
	w.Observed = &f
	switch {
	case changed:
		w.LastChange, w.Backoff = now, lo
	case w.Backoff < lo:
		w.Backoff = lo
	default:
		w.Backoff = min(w.Backoff*2, hi)
	}
	wait := w.Backoff
	if hasBucket(f.Checks, domain.WatchCheckPending) {
		wait = min(wait, max(lo, time.Minute))
	}
	w.NextPoll = now.Add(wait)
	return changed
}

func watchChanged(a, b *domain.WatchFacts) bool {
	if a.State != b.State || a.Head != b.Head || a.Base != b.Base || a.Mergeable != b.Mergeable ||
		a.MergeState != b.MergeState || a.ReviewDecision != b.ReviewDecision || a.KeptOut != b.KeptOut ||
		a.Unreadable != b.Unreadable || a.ReadError != b.ReadError || len(a.Checks) != len(b.Checks) || len(a.Feedback) != len(b.Feedback) {
		return true
	}
	for i := range a.Checks {
		if a.Checks[i].Name != b.Checks[i].Name || a.Checks[i].Bucket != b.Checks[i].Bucket {
			return true
		}
	}
	for i := range a.Feedback {
		if a.Feedback[i].ID != b.Feedback[i].ID {
			return true
		}
	}
	return false
}

// --- the conductor's watch actions ---------------------------------------------

// watchActionLocked executes a watch's action from Decide; the caller holds
// the run lock. It reports whether the run row changed.
func (e *Engine) watchActionLocked(ctx context.Context, run *domain.Run, a workflowpolicy.Action) (dirty bool, err error) {
	st := e.cfg.Store
	row, err := st.RunStep(ctx, run.Session, run.N, a.Path)
	if err != nil || row == nil || row.Watch == nil || row.State != domain.StepWatching {
		return false, err
	}
	w := row.Watch
	switch a.Kind {
	case workflowpolicy.StartPass:
		return false, e.startPassLocked(ctx, run, row, a)
	case workflowpolicy.PushPass:
		if len(w.Passes) == 0 || a.Pass != len(w.Passes) {
			return false, nil
		}
		p := &w.Passes[a.Pass-1]
		p.Settled, p.EndHead = "done", e.sessionHead(ctx, run.Session)
		p.EndTree, _ = e.snapshotRun(ctx, run.Session, run.N)
		if err := st.UpdateRunStep(ctx, row); err != nil {
			return false, err
		}
		e.pushPassAsync(run.Session, run.N, a.Path, a.Pass)
		return false, nil
	case workflowpolicy.AskWatch:
		if w.Asked == a.Key {
			return false, nil
		}
		d := e.watchDecision(run, row, a)
		if !e.raiseRunDecision(run, d) {
			return false, nil
		}
		w.Asked = a.Key
		e.runNote(run.Session, fmt.Sprintf("run %d waits on the person: %s", run.N, a.Reason))
		return true, st.UpdateRunStep(ctx, row)
	case workflowpolicy.ParkWatch:
		if err := e.parkRunLocked(ctx, run, "parked: the board restarted while it waited on its PR"); err != nil {
			return false, err
		}
		if e.raiseRunDecision(run, domain.RunDecision{
			Kind: domain.RunDecideResume, Step: a.Path,
			Question: fmt.Sprintf("Run %d saw %s on its PR after gummi restarted. Resume to act on it, or stop the run?", run.N, a.Event),
			Options:  []string{WorkflowResumeRun, WorkflowStopRun},
		}) {
			return true, nil
		}
		return false, nil
	case workflowpolicy.EndWatch:
		now := e.now()
		row.State, row.Reason, row.EndedAt = a.Settle, a.Reason, now
		row.EndTree, _ = e.snapshotRun(ctx, run.Session, run.N)
		head := ""
		if w.Observed != nil {
			head = w.Observed.Head
		}
		row.Output = fmt.Sprintf("%s\n%s\nhead %s", a.Reason, w.PR.URL, head)
		w.Asked = ""
		if run.Pending != nil && run.Pending.Kind == domain.RunDecideWatch && run.Pending.Step == a.Path {
			run.Pending = nil
			dirty = true
		}
		if err := st.UpdateRunStep(ctx, row); err != nil {
			return false, err
		}
		e.runNote(run.Session, fmt.Sprintf("run %d: %s %s: %s", run.N, a.Path, a.Settle, a.Reason))
		return dirty, nil
	}
	return false, nil
}

func (e *Engine) sessionHead(ctx context.Context, session domain.FeatureID) string {
	f, err := e.cfg.Store.GetFeature(ctx, session)
	if err != nil {
		return ""
	}
	mgr, err := e.mgr(ctx, &f)
	if err != nil {
		return ""
	}
	h, _ := mgr.Head(ctx, &f)
	return h
}

func (e *Engine) startPassLocked(ctx context.Context, run *domain.Run, row *domain.RunStep, a workflowpolicy.Action) error {
	w := row.Watch
	spec, ok := run.Recipe.WatchStep()
	if !ok || w.Observed == nil || a.Pass != len(w.Passes)+1 {
		return nil
	}
	tree, _ := e.snapshotRun(ctx, run.Session, run.N)
	p := domain.WatchPass{
		N: a.Pass, Event: a.Event, Key: a.Key, Head: w.Observed.Head,
		StartHead: e.sessionHead(ctx, run.Session), StartTree: tree,
	}
	if a.Event == domain.EventBaseMoved {
		p.Preparing = true
	} else {
		p.Text = eventText(a.Event, w.Observed, spec.Watch, a.Seen, "")
	}
	w.Passes = append(w.Passes, p)
	w.Seen = append(w.Seen, a.Seen...)
	if err := e.cfg.Store.UpdateRunStep(ctx, row); err != nil {
		return err
	}
	e.runNote(run.Session, fmt.Sprintf("run %d: %s: pass %d on %s", run.N, a.Path, a.Pass, a.Event))
	if p.Preparing {
		e.prepareBaseAsync(run.Session, run.N, a.Path, a.Pass)
	}
	return nil
}

// prepareBaseAsync fetches the moved base with the person's credential, so
// the pass's step only has a local ref to merge (it is never given one).
func (e *Engine) prepareBaseAsync(session domain.FeatureID, n int, path string, pass int) {
	rt := e.wfRT()
	key := fmt.Sprintf("base/%s/%d/%s/%d", session, n, path, pass)
	if !rt.claim(key) {
		return
	}
	if !e.goWorkflow(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		ctx := e.lifetime()
		f, err := e.cfg.Store.GetFeature(ctx, session)
		if err != nil {
			return
		}
		ref, why := "", ""
		mgr, err := e.mgr(ctx, &f)
		if err == nil {
			if err = mgr.Fetch(ctx); err == nil {
				if tgt, terr := mgr.PushTarget(ctx, &f); terr == nil {
					ref = tgt.Remote
				}
			}
		}
		if err != nil {
			why = "fetching the base failed: " + err.Error()
		}
		unlock := e.lockRuns(session)
		defer unlock()
		row, err := e.cfg.Store.RunStep(ctx, session, n, path)
		if err != nil || row == nil || row.Watch == nil || len(row.Watch.Passes) < pass {
			return
		}
		w := row.Watch
		p := &w.Passes[pass-1]
		if why != "" {
			// no pass without the base: the person says whether to read again
			w.Passes = w.Passes[:pass-1]
			if w.Observed != nil {
				w.Observed.ReadError = why
			}
		} else {
			run, _ := e.cfg.Store.Run(ctx, session, n)
			spec, ok := domain.Watch{}, false
			if run != nil {
				var s domain.Step
				if s, ok = run.Recipe.WatchStep(); ok {
					spec = *s.Watch
				}
			}
			p.Text = eventText(domain.EventBaseMoved, w.Observed, &spec, nil, ref)
			p.Preparing = false
		}
		_ = e.cfg.Store.UpdateRunStep(ctx, row)
	}) {
		rt.release(key)
	}
}

// pushPassAsync pushes the session's branch once a pass has run its steps:
// a plain push (agentpublish refuses a force), or nothing when the pass left
// the branch where the PR already is.
func (e *Engine) pushPassAsync(session domain.FeatureID, n int, path string, pass int) {
	rt := e.wfRT()
	key := fmt.Sprintf("push/%s/%d/%s/%d", session, n, path, pass)
	if !rt.claim(key) {
		return
	}
	if !e.goWorkflow(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		ctx := e.lifetime()
		f, err := e.cfg.Store.GetFeature(ctx, session)
		if err != nil {
			return
		}
		row, err := e.cfg.Store.RunStep(ctx, session, n, path)
		if err != nil || row == nil || row.Watch == nil || len(row.Watch.Passes) < pass {
			return
		}
		observed := ""
		if row.Watch.Observed != nil {
			observed = row.Watch.Observed.Head
		}
		head := e.sessionHead(ctx, session)
		pushErr := ""
		if head != observed {
			if _, perr := agentpublish.Do(ctx, e.runHost(), f, agentpublish.ActPush, agentpublish.Args{}); perr != nil {
				pushErr = perr.Error()
			}
		}
		if ctx.Err() != nil {
			return
		}
		unlock := e.lockRuns(session)
		defer unlock()
		row, err = e.cfg.Store.RunStep(ctx, session, n, path)
		if err != nil || row == nil || row.Watch == nil || len(row.Watch.Passes) < pass {
			return
		}
		w := row.Watch
		p := &w.Passes[pass-1]
		if pushErr != "" {
			p.PushError = pushErr
		} else {
			p.Pushed, p.PushError = true, ""
			if head != observed {
				p.PushedSHA = head
				w.AwaitHead, w.AwaitSince = head, e.now()
				e.runNote(session, fmt.Sprintf("run %d: %s: pass %d pushed %s", n, path, pass, short7(head)))
			}
			// what was read before the push describes the commit before it
			w.Observed = nil
			w.NextPoll, w.Backoff = e.now(), 0
		}
		_ = e.cfg.Store.UpdateRunStep(ctx, row)
	}) {
		rt.release(key)
	}
}

func short7(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// eventText is the facts of an event, for a pass's {{event}}: what failed,
// who said what, where the base went. Never instructions.
func eventText(ev domain.WatchEvent, f *domain.WatchFacts, spec *domain.Watch, ids []string, baseRemote string) string {
	var b strings.Builder
	switch ev {
	case domain.EventChecksFailed:
		fmt.Fprintf(&b, "Checks failed on commit %s:\n", short7(f.Head))
		for _, c := range f.Checks {
			if c.Bucket != domain.WatchCheckFail {
				continue
			}
			fmt.Fprintf(&b, "\n- %s %s\n", c.Name, c.URL)
			switch {
			case c.Log != "":
				b.WriteString("  log tail:\n" + indent(c.Log, "    ") + "\n")
			case len(spec.Checks) > 0 && !slices.Contains(spec.Checks, c.Name):
				b.WriteString("  log not read: not among the checks this recipe reads\n")
			}
		}
	case domain.EventChangesRequested, domain.EventComment:
		fmt.Fprintf(&b, "Feedback on the pull request, from people the recipe trusts:\n")
		for _, fb := range f.Feedback {
			if !slices.Contains(ids, fb.ID) {
				continue
			}
			state := ""
			if fb.State != "" {
				state = " (" + strings.ToLower(strings.ReplaceAll(fb.State, "_", " ")) + ")"
			}
			fmt.Fprintf(&b, "\n- %s by @%s%s %s\n%s\n", fb.Kind, fb.Author, state, fb.URL, indent(fb.Body, "    "))
		}
	case domain.EventBaseMoved:
		ref := "refs/remotes/" + baseRemote + "/" + f.BaseRef
		fmt.Fprintf(&b, "The base %s moved to %s; gummi fetched it as %s. Merge that ref into this branch.\n", f.BaseRef, short7(f.Base), ref)
	}
	out := b.String()
	if len(out) > eventTextCap {
		out = out[:runeCut(out, eventTextCap)] + "\n… (cut)\n"
	}
	return strings.TrimRight(out, "\n")
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// --- questions -----------------------------------------------------------------

func (e *Engine) watchDecision(run *domain.Run, row *domain.RunStep, a workflowpolicy.Action) domain.RunDecision {
	w := row.Watch
	head := ""
	if w.Observed != nil {
		head = short7(w.Observed.Head)
	}
	opts := []string{WatchKeepWait, WatchMoreRun, WorkflowStopRun}
	var tail string
	switch a.Ask {
	case workflowpolicy.AskMax:
		opts = []string{WatchMoreRun, WatchKeepWait, WorkflowStopRun}
		tail = "Run one more pass, keep waiting without acting, or stop the run?"
	case workflowpolicy.AskFailedPass:
		opts = []string{WatchMoreRun, WatchKeepWait, WorkflowStopRun}
		tail = "Run the pass again, keep waiting without acting, or stop the run?"
	case workflowpolicy.AskSame, workflowpolicy.AskUnchanged:
		tail = "Keep waiting without acting, run one more pass, or stop the run?"
	case workflowpolicy.AskPush:
		opts = []string{WatchReadAgain, WatchKeepWait, WorkflowStopRun}
		tail = "Try the push again, go on waiting without it, or stop the run?"
	case workflowpolicy.AskUnreadable, workflowpolicy.AskReadError:
		opts = []string{WatchReadAgain, WorkflowStopRun}
		tail = "Nothing is acted on meanwhile. Read again, or stop the run?"
	case workflowpolicy.AskQuiet:
		opts = []string{WatchKeepWait, WorkflowStopRun}
		tail = "Keep waiting, or stop the run?"
	}
	return domain.RunDecision{
		Kind: domain.RunDecideWatch, Step: a.Path, Options: opts,
		Question: fmt.Sprintf("Run %d waits on %s#%d (head %s): %s. %s", run.N, w.PR.Repo, w.PR.Number, head, a.Reason, tail),
	}
}

// answerWatchLocked applies the person's answer to a watch's question; the
// caller holds the run lock and sets run.Stopping on a stop.
func (e *Engine) answerWatchLocked(ctx context.Context, run *domain.Run, d domain.RunDecision, answer string) (bool, error) {
	st := e.cfg.Store
	row, err := st.RunStep(ctx, run.Session, run.N, d.Step)
	if err != nil || row == nil || row.Watch == nil {
		return false, fmt.Errorf("step %s is gone", d.Step)
	}
	w := row.Watch
	key := w.Asked
	kind, rest, _ := strings.Cut(key, ":")
	if strings.HasPrefix(key, "quiet@") {
		kind = "quiet"
	}
	eventKey := rest
	var passN int
	if kind == "failed" || kind == "push" {
		passN, _ = strconv.Atoi(rest)
	}
	pass := func() *domain.WatchPass {
		if passN >= 1 && passN <= len(w.Passes) {
			return &w.Passes[passN-1]
		}
		return nil
	}
	word := ""
	switch answer {
	case WatchMoreRun:
		word = workflowpolicy.WatchMore
		switch kind {
		case "max":
			w.Extra++
		case "maxev":
			if ev, _, ok := strings.Cut(eventKey, "@"); ok {
				if w.ExtraEvent == nil {
					w.ExtraEvent = map[domain.WatchEvent]int{}
				}
				w.ExtraEvent[domain.WatchEvent(ev)]++
			}
		case "same", "unchanged":
			w.Allowed = append(w.Allowed, eventKey)
		case "failed":
			if p := pass(); p != nil {
				p.Settled = "failed"
			}
			w.Extra++
		}
	case WatchKeepWait:
		word = workflowpolicy.WatchWait
		switch kind {
		case "max", "maxev", "same", "unchanged":
			w.Ignored = append(w.Ignored, eventKey)
		case "failed":
			if p := pass(); p != nil {
				p.Settled = "failed"
				w.Ignored = append(w.Ignored, p.Key)
			}
		case "quiet":
			w.LastChange = e.now()
		case "push":
			if p := pass(); p != nil {
				p.Pushed, p.PushError = true, ""
				w.Observed, w.NextPoll = nil, e.now()
			}
		}
	case WatchReadAgain:
		word = workflowpolicy.WatchAgain
		switch kind {
		case "push":
			if p := pass(); p != nil {
				p.Settled, p.PushError = "", ""
			}
		default:
			w.ReadFails, w.NextPoll = 0, e.now()
			if w.Observed != nil {
				w.Observed.ReadError, w.Observed.Unreadable = "", ""
				// the feedback is read afresh on the next poll: with the
				// PR unchanged, clearing Unreadable alone would reuse what
				// the failed read left
				w.Observed.FetchedAt = time.Time{}
			}
		}
	case WorkflowStopRun:
		word = workflowpolicy.WatchStop
		run.Stopping, run.Reason = true, "stopped by the person at a question about its PR"
	default:
		return false, nil
	}
	if w.Answers == nil {
		w.Answers = map[string]string{}
	}
	w.Answers[key] = word
	w.Asked = ""
	e.runNote(run.Session, fmt.Sprintf("run %d: %s at %s", run.N, strings.ToLower(answer), d.Step))
	return true, st.UpdateRunStep(ctx, row)
}

// --- a restart -----------------------------------------------------------------

// resumeWatching reads a run left running by a gummi that is gone: when the
// only thing it was doing was waiting on its PR, polling goes on and the
// first pass after the restart waits for a person's Resume (DESIGN §28.6).
func (e *Engine) resumeWatching(ctx context.Context, run *domain.Run) bool {
	st := e.cfg.Store
	if run.Pending != nil || run.Stopping {
		return false
	}
	steps, err := st.RunSteps(ctx, run.Session, run.N)
	if err != nil {
		return false
	}
	var watching *domain.RunStep
	for i := range steps {
		s := &steps[i]
		switch {
		case s.State == domain.StepWatching && watching == nil && s.Watch != nil:
			watching = s
		case s.State.Settled():
		default:
			return false
		}
	}
	if watching == nil {
		return false
	}
	if n := len(watching.Watch.Passes); n > 0 {
		if p := watching.Watch.Passes[n-1]; !p.Pushed && p.Settled != "failed" {
			return false
		}
	}
	watching.Watch.Held, watching.Watch.NextPoll, watching.Watch.Observed = true, e.now(), nil
	if st.UpdateRunStep(ctx, watching) != nil {
		return false
	}
	e.wfRT().conduct(run.Session, run.N)
	e.runNote(run.Session, fmt.Sprintf("run %d: polling its PR again after a restart; it acts on nothing until you resume it", run.N))
	return true
}

// releaseHeld lets a resumed run act on what a restart held back.
func (e *Engine) releaseHeld(ctx context.Context, run *domain.Run) {
	steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
	for i := range steps {
		s := &steps[i]
		if s.State != domain.StepWatching || s.Watch == nil {
			continue
		}
		if s.Watch.Held {
			s.Watch.Held, s.Watch.NextPoll = false, e.now()
			_ = e.cfg.Store.UpdateRunStep(ctx, s)
		}
		// a fetch or a push the last gummi was in the middle of is lost
		if n := len(s.Watch.Passes); n > 0 {
			p := s.Watch.Passes[n-1]
			switch {
			case p.Preparing:
				e.prepareBaseAsync(run.Session, run.N, s.Path, p.N)
			case p.Settled == "done" && !p.Pushed && p.PushError == "":
				e.pushPassAsync(run.Session, run.N, s.Path, p.N)
			}
		}
	}
}

// --- the proposal ---------------------------------------------------------------

// checkPublishAllowance refuses a recipe that acts on the session's PR for
// a session a person has not allowed to publish.
func (e *Engine) checkPublishAllowance(ctx context.Context, sess domain.Feature, r domain.Recipe) error {
	if len(prSteps(r)) == 0 || e.AgentMayPublish(ctx, sess.ID) {
		return nil
	}
	return fmt.Errorf("this recipe opens or waits on the session's pull request, and this session may not publish: " +
		`a person turns on "agent may publish" for it first`)
}

// checkWatchTrust fills the watch's trust with the run's inputs and refuses
// one that does not parse, naming the step.
func checkWatchTrust(r domain.Recipe, inputs map[string]string) error {
	w, ok := r.WatchStep()
	if !ok {
		return nil
	}
	text, err := workflowrecipe.FillPlain(w.Watch.Trust, workflowrecipe.Vars{Inputs: inputs})
	if err == nil {
		_, err = workflowrecipe.ParseTrust(text)
	}
	if err != nil {
		return fmt.Errorf("step %s: trust: %v", w.ID, err)
	}
	return nil
}

// writePRAllowance lists what allowing a recipe allows on the session's
// PR: each act, and what the watch waits for, acts on and for whom.
func (e *Engine) writePRAllowance(ctx context.Context, b *strings.Builder, run *domain.Run) {
	e.writePRAllowanceOf(ctx, b, run.Recipe, run.Inputs)
}

// writePRAllowanceOf is writePRAllowance for a recipe and the inputs its
// templates are filled from: a revised plan's question reads it too.
func (e *Engine) writePRAllowanceOf(ctx context.Context, b *strings.Builder, recipe domain.Recipe, inputs map[string]string) {
	steps := prSteps(recipe)
	if len(steps) == 0 {
		return
	}
	b.WriteString("\nActs on the session's pull request (through its publish allowance, never forced):\n")
	for _, s := range steps {
		if s.PR == nil {
			continue
		}
		what := s.PR.Act + " the PR"
		switch s.PR.Act {
		case "create":
			what = "create the PR"
			if s.PR.Draft {
				what += ", as a draft"
			}
		case "push":
			what = "push the branch"
		case "update":
			what = "update the PR"
		case "ready":
			what = "mark the PR ready for review"
		}
		fmt.Fprintf(b, "- %s: %s\n", s.ID, what)
	}
	ws, ok := recipe.WatchStep()
	if !ok {
		return
	}
	w := ws.Watch
	b.WriteString("- after each pass: push the branch\n")
	text, _ := workflowrecipe.FillPlain(w.Trust, workflowrecipe.Vars{Inputs: inputs})
	filter, perr := workflowrecipe.ParseTrust(text)
	self := ""
	if filter.Self && perr == nil {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := pr.Viewer(tctx, pr.GHBinary())
		cancel()
		if err == nil {
			self = v
		}
	}
	until := "until a person merges it"
	if w.UntilOr() == domain.UntilGreenApproved {
		until = "until every check passed and it is approved"
	}
	fmt.Fprintf(b, "\n%s waits on the PR %s, %s:\n", ws.ID, until, filter.Describe(self))
	for _, ev := range domain.WatchEvents {
		h, ok := w.On[ev]
		if !ok {
			continue
		}
		when := ""
		if ev == domain.EventChecksFailed {
			when = " (once every check on the head has settled)"
		}
		switch h.Form() {
		case "recipe":
			name := h.Recipe
			if name == "" {
				name = "inline"
			}
			fmt.Fprintf(b, "- %s%s: the %s recipe, frozen here\n", ev, when, name)
		case "goto":
			fmt.Fprintf(b, "- %s%s: steps from %s again\n", ev, when, h.Goto)
		default:
			var ids []string
			for _, s := range h.Steps {
				ids = append(ids, fmt.Sprintf("%s (%s)", s.ID, s.Role))
			}
			fmt.Fprintf(b, "- %s%s: %s\n", ev, when, strings.Join(ids, ", "))
		}
	}
	var lim []string
	if w.Max > 0 {
		lim = append(lim, fmt.Sprintf("at most %d passes", w.Max))
	}
	if w.Quiet > 0 {
		lim = append(lim, "after "+w.Quiet.String()+" of nothing it asks you")
	}
	if w.OnClosed == domain.OnClosedContinue {
		lim = append(lim, "closed unmerged: the run goes on")
	} else {
		lim = append(lim, "closed unmerged: the run ends partial")
	}
	b.WriteString(strings.Join(lim, "; ") + ".\n")
}

// freezeHandlers writes into every recipe handler that recipe's YAML as the
// workspace has it now, and reports whether it changed one. A recipe that
// holds a watch, a pr step or a dispatch cannot be a handler.
func (e *Engine) freezeHandlers(r *domain.Recipe) (bool, error) {
	changed := false
	for i := range r.Steps {
		s := r.Steps[i].Step
		if s == nil || s.Watch == nil {
			continue
		}
		evs := make([]string, 0, len(s.Watch.On))
		for ev := range s.Watch.On {
			evs = append(evs, string(ev))
		}
		sort.Strings(evs)
		for _, ev := range evs {
			h := s.Watch.On[domain.WatchEvent(ev)]
			if h.Recipe == "" {
				continue
			}
			f, err := workflowrecipe.Load(h.Recipe, e.pool.Root())
			if err != nil {
				return changed, fmt.Errorf("step %s: the %s handler's recipe: %w", s.ID, ev, err)
			}
			held := ""
			workflowrecipe.Walk(f.Recipe.Steps, func(st domain.Step, _ []domain.Entry) {
				switch {
				case st.Watch != nil:
					held = "watch"
				case st.PR != nil && held == "":
					held = "pr"
				case st.Dispatch != nil && held == "":
					held = "dispatch"
				}
			})
			if held != "" {
				return changed, fmt.Errorf("step %s: the %s handler's recipe %s holds a %s step of its own, and a handler cannot", s.ID, ev, h.Recipe, held)
			}
			if text := string(f.YAML); text != h.RecipeYAML {
				h.RecipeYAML, h.Entries = text, f.Recipe.Steps
				s.Watch.On[domain.WatchEvent(ev)] = h
				changed = true
			}
		}
	}
	return changed, nil
}

// WorkflowForward is a person forwarding to run n's watch the feedback its
// trust filter kept out (DESIGN §28.3): each item as it stands now is read
// as trusted, and the watch reads the PR again at once. An item someone
// edits after it was forwarded is kept out again.
func (e *Engine) WorkflowForward(ctx context.Context, session domain.FeatureID, n int, by string) error {
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.openRunLocked(ctx, session)
	if err != nil {
		return err
	}
	if run == nil || run.N != n {
		return fmt.Errorf("run %d is not open", n)
	}
	steps, err := e.cfg.Store.RunSteps(ctx, session, n)
	if err != nil {
		return err
	}
	for i := range steps {
		s := &steps[i]
		if s.State != domain.StepWatching || s.Watch == nil || s.Watch.Observed == nil || len(s.Watch.Observed.KeptOutPrints) == 0 {
			continue
		}
		w := s.Watch
		count := len(w.Observed.KeptOutPrints)
		for _, fp := range w.Observed.KeptOutPrints {
			if !slices.Contains(w.Forwarded, fp) {
				w.Forwarded = append(w.Forwarded, fp)
			}
		}
		// read the feedback again on the next poll, which is now
		w.ForwardGen++
		w.NextPoll = e.now()
		if err := e.cfg.Store.UpdateRunStep(ctx, s); err != nil {
			return err
		}
		e.runNote(session, fmt.Sprintf("run %d: %s forwarded %s from outside the trust filter to its watch",
			n, by, domain.WatchWords(count, "item")))
		e.wfRT().conduct(session, n)
		e.workflowEvent(session)
		return nil
	}
	return fmt.Errorf("run %d's watch holds nothing from outside its trust filter", n)
}
