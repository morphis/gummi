package workflowpolicy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// watch walks a watch step (DESIGN §28.3): a row in `watching` that waits
// on its PR, runs one pass at a time for the event the PR raises, pushes
// after it and waits again, until the PR is merged (or green and approved)
// or closed. It reads the row's memory and the engine's last read of the
// PR, and the clock only through Snapshot.Now.
func (d *decider) watch(prefix string, s domain.Step) (outcome, string) {
	path := join(prefix, s.ID)
	inst, has := d.byPath[path]
	switch {
	case !has || inst.State == domain.StepWaiting:
		d.start(path, s.ID)
		return pending, ""
	case inst.State == domain.StepDone:
		return ok, ""
	case inst.State == domain.StepFailed:
		return failed, "step " + path + " failed"
	case inst.State == domain.StepStopped:
		return failed, "step " + path + " was stopped"
	case inst.State != domain.StepWatching || inst.Watch == nil:
		return pending, ""
	}
	w, spec := inst.Watch, s.Watch
	ask := func(a WatchAsk, key, why string) {
		if w.Asked != "" {
			return
		}
		d.acts = append(d.acts, Action{Kind: AskWatch, Path: path, Ask: a, Key: key, Reason: why})
	}

	f := w.Observed
	end := func(state domain.StepState, why string) {
		d.acts = append(d.acts, Action{Kind: EndWatch, Path: path, Settle: state, Reason: why})
	}
	// a PR that is merged or closed ends the watch, whatever a person was
	// asked or a pass was doing, once no step of a pass is still running
	var open *domain.WatchPass
	if n := len(w.Passes); n > 0 {
		if p := w.Passes[n-1]; p.Settled != "failed" && !p.Pushed {
			open = &p
		}
	}
	if f != nil && f.ReadError == "" && (open == nil || !open.Preparing) && !d.hasLive(path) {
		switch f.State {
		case "MERGED":
			end(domain.StepDone, "merged")
			return pending, ""
		case "CLOSED":
			if spec.OnClosed == domain.OnClosedContinue {
				end(domain.StepDone, "closed unmerged; the recipe goes on")
			} else {
				end(domain.StepFailed, "the PR was closed unmerged")
			}
			return pending, ""
		}
		if open == nil && spec.UntilOr() == domain.UntilGreenApproved && greenApproved(f, d.in.Now.Sub(w.LastChange)) {
			end(domain.StepDone, "green and approved")
			return pending, ""
		}
	}
	// a pass that has not been pushed (or given up on) is open
	if open != nil {
		return d.pass(path, s, *open, ask)
	}
	if w.Asked != "" {
		return pending, ""
	}
	if f == nil {
		return pending, ""
	}
	if f.ReadError != "" {
		ask(AskReadError, "read", f.ReadError)
		return pending, ""
	}
	if spec.Quiet > 0 && !w.LastChange.IsZero() && d.in.Now.Sub(w.LastChange) >= spec.Quiet {
		ask(AskQuiet, "quiet@"+strconv.FormatInt(w.LastChange.Unix(), 10), fmt.Sprintf("nothing has happened to the PR for %s", spec.Quiet))
		return pending, ""
	}
	// feedback is withheld while the filter cannot be read, so the person is
	// asked at once, whether or not an event stands or a check is pending:
	// nothing is acted on meanwhile
	if f.Unreadable != "" {
		ask(AskUnreadable, "unreadable", f.Unreadable)
		return pending, ""
	}
	if f.ChecksHead != f.Head || anyBucket(f, domain.WatchCheckPending) {
		return pending, ""
	}
	standing := d.standing(spec, w, f)
	if len(standing) == 0 {
		return pending, ""
	}
	ev := standing[0]
	k, ids := ev.key, ev.ids
	allowed := slices.Contains(w.Allowed, k)
	if !allowed {
		for _, q := range w.Passes {
			if q.Settled == "done" && q.Key == k {
				ask(AskSame, "same:"+k, fmt.Sprintf("%s came back on %s after pass %d", ev.event, short(f.Head), q.N))
				return pending, ""
			}
		}
		if last := w.Passes; len(last) > 0 {
			q := last[len(last)-1]
			if q.Event == ev.event && q.Settled == "done" && q.StartHead == q.EndHead && q.StartTree == q.EndTree {
				ask(AskUnchanged, "unchanged:"+k, fmt.Sprintf("%s still stands after pass %d changed nothing", ev.event, q.N))
				return pending, ""
			}
		}
	}
	if spec.Max > 0 && len(w.Passes) >= spec.Max+w.Extra {
		ask(AskMax, "max:"+k, fmt.Sprintf("the watch has run %d passes", len(w.Passes)))
		return pending, ""
	}
	if hm := spec.On[ev.event].Max; hm > 0 {
		n := 0
		for _, q := range w.Passes {
			if q.Event == ev.event {
				n++
			}
		}
		if n >= hm+w.ExtraEvent[ev.event] {
			ask(AskMax, "maxev:"+k, fmt.Sprintf("the watch has run %d passes on %s", n, ev.event))
			return pending, ""
		}
	}
	if w.Held {
		d.starts = append(d.starts, Action{Kind: ParkWatch, Path: path, Event: ev.event, Reason: fmt.Sprintf("the board restarted while the run waited on its PR; resume to act on %s", ev.event)})
		return pending, ""
	}
	d.starts = append(d.starts, Action{Kind: StartPass, Path: path, Step: s.ID, Event: ev.event, Pass: len(w.Passes) + 1, Key: k, Seen: ids})
	return pending, ""
}

// pass walks the open pass p of the watch at path.
func (d *decider) pass(path string, s domain.Step, p domain.WatchPass, ask func(WatchAsk, string, string)) (outcome, string) {
	n := strconv.Itoa(p.N)
	if p.Preparing {
		return pending, ""
	}
	if p.Settled == "done" {
		// steps ran; the push is under way, or failed
		if p.PushError != "" {
			ask(AskPush, "push:"+n, p.PushError)
		}
		return pending, ""
	}
	res, _ := d.seq(join(path, n), d.in.Recipe.PassEntries(s.ID, p.Event))
	switch res {
	case ok:
		d.acts = append(d.acts, Action{Kind: PushPass, Path: path, Pass: p.N, Event: p.Event})
	case failed, stopped:
		if !d.hasLive(join(path, n)) {
			ask(AskFailedPass, "failed:"+n, fmt.Sprintf("a step of pass %d on %s failed", p.N, p.Event))
		}
	}
	return pending, ""
}

func (d *decider) hasLive(prefix string) bool {
	for _, s := range d.in.Steps {
		if strings.HasPrefix(s.Path, prefix+"/") && s.State.Live() {
			return true
		}
	}
	return false
}

type standingEvent struct {
	event domain.WatchEvent
	key   string
	ids   []string
}

// standing lists the events the PR raises now, in the order a watch reads
// them: only those with a handler, and not ones a person said to wait past.
func (d *decider) standing(spec *domain.Watch, w *domain.WatchState, f *domain.WatchFacts) []standingEvent {
	var out []standingEvent
	add := func(ev domain.WatchEvent, key string, ids []string) {
		if _, handled := spec.On[ev]; !handled || slices.Contains(w.Ignored, key) {
			return
		}
		out = append(out, standingEvent{ev, key, ids})
	}
	if f.Mergeable == "CONFLICTING" || f.MergeState == "BEHIND" {
		add(domain.EventBaseMoved, "base-moved@"+f.Base+":"+f.Head, nil)
	}
	if anyBucket(f, domain.WatchCheckFail) {
		add(domain.EventChecksFailed, "checks-failed@"+f.Head, nil)
	}
	var changes, comments []string
	for _, fb := range f.Feedback {
		if slices.Contains(w.Seen, fb.ID) {
			continue
		}
		switch {
		case fb.Kind == "thread", fb.Kind == "review" && fb.State == "CHANGES_REQUESTED":
			changes = append(changes, fb.ID)
		case fb.Kind == "comment":
			comments = append(comments, fb.ID)
		}
	}
	slices.Sort(changes)
	slices.Sort(comments)
	if len(changes) > 0 {
		add(domain.EventChangesRequested, "changes-requested@"+strings.Join(changes, ","), changes)
	}
	if len(comments) > 0 {
		add(domain.EventComment, "comment@"+strings.Join(comments, ","), comments)
	}
	slices.SortStableFunc(out, func(a, b standingEvent) int {
		return slices.Index(domain.WatchEvents, a.event) - slices.Index(domain.WatchEvents, b.event)
	})
	return out
}

func anyBucket(f *domain.WatchFacts, bucket string) bool {
	for _, c := range f.Checks {
		if c.Bucket == bucket {
			return true
		}
	}
	return false
}

// noChecksGrace is how long a head with no checks at all must stand
// before green-approved reads it as green: a repository's CI registers its
// checks a little after a push, and a head read in that moment has none.
const noChecksGrace = 5 * time.Minute

// greenApproved reports whether every check on the head passed and the
// review decision is APPROVED. A head with no checks is green once nothing
// has changed for noChecksGrace (still is how long), so a repository
// without CI ends the watch too.
func greenApproved(f *domain.WatchFacts, still time.Duration) bool {
	if f.ReviewDecision != "APPROVED" || f.ChecksHead != f.Head {
		return false
	}
	if len(f.Checks) == 0 {
		return still >= noChecksGrace
	}
	for _, c := range f.Checks {
		if c.Bucket != domain.WatchCheckPass && c.Bucket != domain.WatchCheckSkipping {
			return false
		}
	}
	return true
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
