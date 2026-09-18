package driver

// Goals, headless. A goal walks the same drive loop as any card: its plan
// is the design conversation, its review and verify are the stages the
// loop already knows. What is new is its implement stage, which has no
// agent of its own: driveGoal conducts it, ticking the engine's conductor
// and running each card it starts as an ordinary autopilot drive in a
// goroutine of this process. Several drives read one engine event stream,
// so the stream is split per card by an eventHub.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
)

// goalPollInterval is how often a conducted goal re-ticks with nothing
// else to wake it: the conductor's idle detection needs time to pass.
const goalPollInterval = 5 * time.Second

// --- event hub ---------------------------------------------------------------

// eventHub splits the engine's single event stream by card. It is the one
// reader of engine.Events() once started; each subscriber gets a mailbox
// that never blocks the hub, so a slow drive cannot stall another card's
// session pump.
type eventHub struct {
	mu     sync.Mutex
	boxes  map[domain.FeatureID]*mailbox
	closed bool
}

type mailbox struct {
	mu   sync.Mutex
	q    []engine.Event
	wake chan struct{}
	out  chan engine.Event
	done chan struct{}
}

func newMailbox() *mailbox {
	m := &mailbox{wake: make(chan struct{}, 1), out: make(chan engine.Event), done: make(chan struct{})}
	go m.pump()
	return m
}

func (m *mailbox) push(ev engine.Event) {
	m.mu.Lock()
	m.q = append(m.q, ev)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *mailbox) pump() {
	defer close(m.out)
	for {
		m.mu.Lock()
		q := m.q
		m.q = nil
		m.mu.Unlock()
		for _, ev := range q {
			select {
			case m.out <- ev:
			case <-m.done:
				return
			}
		}
		select {
		case <-m.wake:
		case <-m.done:
			// drain what arrived before the stream closed, then close
			m.mu.Lock()
			rest := m.q
			m.q = nil
			m.mu.Unlock()
			for _, ev := range rest {
				m.out <- ev
			}
			return
		}
	}
}

func startEventHub(src <-chan engine.Event) *eventHub {
	h := &eventHub{boxes: map[domain.FeatureID]*mailbox{}}
	go func() {
		for ev := range src {
			h.mu.Lock()
			box := h.boxes[ev.Feature]
			h.mu.Unlock()
			if box != nil {
				box.push(ev)
			}
		}
		h.mu.Lock()
		h.closed = true
		for _, b := range h.boxes {
			close(b.done)
		}
		h.mu.Unlock()
	}()
	return h
}

// subscribe returns card id's event stream, creating it on first use.
func (h *eventHub) subscribe(id domain.FeatureID) <-chan engine.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b := h.boxes[id]; b != nil {
		return b.out
	}
	b := newMailbox()
	h.boxes[id] = b
	if h.closed {
		close(b.done)
	}
	return b.out
}

// stream is the event stream this driver reads.
func (d *Driver) stream() <-chan engine.Event {
	if d.events != nil {
		return d.events
	}
	return d.eng.Events()
}

// ensureHub moves this driver onto a hub, so goal cards can be driven
// beside it in the same process.
func (d *Driver) ensureHub(id domain.FeatureID) *eventHub {
	if d.hub == nil {
		d.hub = startEventHub(d.eng.Events())
		d.events = d.hub.subscribe(id)
	}
	return d.hub
}

// --- the conducted implement stage -------------------------------------------

type childResult struct {
	id  domain.FeatureID
	out Outcome
	err error
}

// goalTickEvent reports what a conductor tick did.
type goalTickEvent struct {
	Event    string   `json:"event"`
	ID       string   `json:"id"`
	Actions  []string `json:"actions,omitempty"`
	Started  []string `json:"started,omitempty"`
	Finished bool     `json:"finished,omitempty"`
}

// driveGoal conducts a goal's implement stage until its work settles and
// its review starts, then judges that review the way any work stage's
// critique is judged.
func (d *Driver) driveGoal(ctx context.Context, f domain.Feature) (Outcome, error) {
	d.enterStage(f.Stage)
	// past its plan a goal runs itself: whatever mode this process started
	// the conversation in, the goal's own review and verify cross unattended
	d.setGate(f.GateApproval)
	if err := d.seedRounds(ctx, f, domain.RoundKindReview); err != nil {
		return Outcome{}, err
	}
	hub := d.ensureHub(f.ID)
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "conducting"})
	if out, handled, err := d.resumeCritiqueLoop(ctx, f); handled || err != nil {
		return out, err
	}

	// each running card's drive, cancellable: a card the goal drops has
	// its session stopped under it, and its drive must stop waiting too
	running := map[domain.FeatureID]context.CancelFunc{}
	// a start for a card whose previous drive has not reported back yet
	// (the lead sent it back in the same moment its drive stopped) waits
	// here for that report, rather than being dropped
	deferred := map[domain.FeatureID]engine.GoalStart{}
	results := make(chan childResult, 1024)
	var wg sync.WaitGroup
	defer func() {
		// nothing leaves this stage while one of its cards is still being
		// driven: stop what is left and wait it out
		for _, cancel := range running {
			cancel()
		}
		wg.Wait()
	}()
	ticker := time.NewTicker(goalPollInterval)
	defer ticker.Stop()

	for {
		res, err := d.eng.GoalTick(ctx, f.ID)
		if err != nil {
			return Outcome{}, err
		}
		if len(res.Actions) > 0 || len(res.Start) > 0 {
			ev := goalTickEvent{Event: "goal", ID: string(f.ID), Finished: res.Finished}
			for _, a := range res.Actions {
				ev.Actions = append(ev.Actions, a.String())
			}
			for _, st := range res.Start {
				ev.Started = append(ev.Started, string(st.ID))
			}
			d.out.emit(ev)
		}
		for id, cancel := range running {
			if c, err := d.store.GetFeature(ctx, id); err == nil && (c.GoalDropped() || c.GoalID != f.ID) {
				cancel()
			}
		}
		start := func(st engine.GoalStart) {
			cctx, cancel := context.WithCancel(ctx)
			running[st.ID] = cancel
			stream := hub.subscribe(st.ID)
			wg.Add(1)
			go func(st engine.GoalStart) {
				defer wg.Done()
				out, err := d.driveGoalCard(cctx, st, stream)
				results <- childResult{id: st.ID, out: out, err: err}
			}(st)
		}
		for _, st := range res.Start {
			if running[st.ID] != nil {
				deferred[st.ID] = st
				continue
			}
			start(st)
		}
		if res.Finished {
			d.reviewsRun++
			d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "critiquing"})
			wg.Wait()
			return d.awaitCritique(ctx, f)
		}
		if res.Stalled != "" {
			// The backend cannot serve this goal at all. Nothing was
			// dropped and nothing is in flight that finishing would help,
			// so the run stops where it is and says what the backend
			// said — which generally names the hour it comes back.
			for _, cancel := range running {
				cancel()
			}
			wg.Wait()
			return d.goalStalled(ctx, f, res.Stalled), nil
		}
		if res.NeedsSubstrate.Waiting() {
			// the other ceiling, the same ending: only a person raises it
			wg.Wait()
			return d.goalNeedsSubstrate(ctx, f, res.NeedsSubstrate), nil
		}
		if res.NeedsBudget.Waiting() {
			// The envelope is spent and only a person raises it. Exit 5
			// with the report, the same status and the same answer a card
			// that runs dry already gets — `resume --envelope N` — rather
			// than dropping the card and reporting a diminished result as
			// though it were the one that was asked for.
			wg.Wait()
			return d.goalNeedsBudget(ctx, f, res.NeedsBudget), nil
		}
		if res.Again {
			continue
		}
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case r := <-results:
			if cancel := running[r.id]; cancel != nil {
				cancel()
			}
			delete(running, r.id)
			if st, ok := deferred[r.id]; ok {
				// the goal already decided what comes next for this card
				delete(deferred, r.id)
				start(st)
				continue
			}
			d.settleGoalCard(ctx, r)
		case ev, ok := <-d.stream():
			if !ok {
				return Outcome{}, errors.New("engine event stream closed")
			}
			_ = ev // a goal event, or a session of the goal's own: tick again
		case <-ticker.C:
		}
	}
}

// driveGoalCard drives one card of a goal to where its autopilot stops,
// holding the card's lock for the drive. A card another process is already
// driving is left to it.
func (d *Driver) driveGoalCard(ctx context.Context, st engine.GoalStart, stream <-chan engine.Event) (Outcome, error) {
	unlock, err := state.AcquireLock(d.ws.CardLockFile(st.ID))
	if err != nil {
		return Outcome{}, fmt.Errorf("%s is driven by another process: %w", st.ID, err)
	}
	defer unlock()
	child := New(d.eng, d.store, d.ws, nil, Options{
		GateApproval: GateAutopilot, StageTimeout: d.opts.StageTimeout,
		Autonomous: true, Verbose: d.opts.Verbose,
	})
	child.out = d.out
	child.events = stream
	child.hub = d.hub
	if st.Note != "" {
		child.bounceNote = st.Note
	}
	return child.drive(ctx, st.ID)
}

// settleGoalCard records why a card's drive ended where the drive itself
// left no record a goal can read — an error, or a drive that could not
// start — so the conductor sees a stuck card rather than a running one.
func (d *Driver) settleGoalCard(ctx context.Context, r childResult) {
	if r.err == nil && r.out.Status != StatusError {
		return
	}
	if c, err := d.store.GetFeature(ctx, r.id); err == nil && c.GoalDropped() {
		return // stopped because the goal dropped it
	}
	detail := "its drive failed"
	if r.err != nil {
		detail = "its drive failed: " + r.err.Error()
	}
	c, err := d.store.GetFeature(ctx, r.id)
	if err != nil {
		return
	}
	_ = d.store.AppendPark(ctx, r.id, c.Stage, state.ParkReasonGaveUp, detail, "", time.Now())
}

// goalVerifyNotPassed handles a goal whose verify did not pass. A goal that
// is already partial, or that has used its rework rounds, stops ready for
// you: the hand-over lists what was not met, and the decision is yours. A
// whole goal with rounds left goes back to its cards, and its lead reads
// the verify's evidence.
func (d *Driver) goalVerifyNotPassed(ctx context.Context, f domain.Feature, reason string) (Outcome, error) {
	n, err := rounds.Load(ctx, d.roundStore, f.ID, domain.RoundKindCorrective)
	if err != nil {
		return Outcome{}, err
	}
	why := "verify did not pass (" + reason + ")"
	if f.Goal.Partial != "" || n >= verdict.MaxRounds(domain.RoundKindCorrective) {
		if f.Goal.Partial == "" {
			_ = d.store.SetGoalPartial(ctx, f.ID, why+" after its rework rounds")
		}
		return d.autoAdvance(ctx, f)
	}
	if err := rounds.Bump(ctx, d.roundStore, f.ID, domain.RoundKindCorrective); err != nil {
		return Outcome{}, err
	}
	if _, err := d.store.Transition(ctx, f.ID, domain.StageImplement, d.actor); err != nil {
		return Outcome{}, err
	}
	cur, err := d.store.GetFeature(ctx, f.ID)
	if err != nil {
		return Outcome{}, err
	}
	if err := d.eng.RunWith(cur, "The goal's verify failed: read the goal doc's Verification plan for the evidence and fix what is not met."); err != nil {
		return Outcome{}, err
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(domain.StageImplement), Result: "reworking"})
	return Outcome{}, nil
}

// goalVerifyBlocked stops a goal whose own verify said the environment
// cannot run its checks. Nothing was judged, so nothing is sent back, no
// rework round is spent and the goal is not partial: it stays at verify,
// and the same resume runs its verify again.
func (d *Driver) goalVerifyBlocked(ctx context.Context, f domain.Feature) Outcome {
	reason := "the goal's verify could not run in this environment — see the goal doc's Verification plan; nothing was judged and nothing was sent back"
	d.logPark(f, state.ParkReasonBlocked, reason)
	return d.goalStalled(ctx, f, reason)
}

// goalDoneEvent is the headless hand-over of a goal ready for you.
type goalDoneEvent struct {
	Met       int                `json:"done_when_met"`
	Total     int                `json:"done_when_total"`
	Partial   string             `json:"partial,omitempty"`
	Landed    []string           `json:"landed,omitempty"`
	Dropped   []string           `json:"dropped,omitempty"`
	Decisions int                `json:"decisions_for_review"`
	Report    *engine.GoalReport `json:"report,omitempty"`
}

func (d *Driver) goalDone(ctx context.Context, f domain.Feature) *goalDoneEvent {
	r, err := d.eng.GoalReport(ctx, f.ID)
	if err != nil {
		return nil
	}
	ev := &goalDoneEvent{Partial: r.Partial, Decisions: len(r.Decisions), Report: &r}
	ev.Met, ev.Total = r.Met()
	for _, c := range r.Cards {
		switch c.State {
		case "landed":
			ev.Landed = append(ev.Landed, string(c.ID))
		case "dropped":
			ev.Dropped = append(ev.Dropped, string(c.ID))
		}
	}
	return ev
}

// resumeGoal applies a resume's goal-only decisions. handled reports that
// the resume ended here (an error or an outcome); otherwise the caller
// drives on.
func (d *Driver) resumeGoal(ctx context.Context, f domain.Feature, in ResumeInput) (Outcome, bool, error) {
	if d.opts.Envelope > f.Budget.Envelope {
		from := f.Budget.Envelope
		if err := d.eng.RaiseGoalBudget(ctx, f.ID, d.opts.Envelope); err != nil {
			return Outcome{}, true, err
		}
		d.out.emit(envelopeRaisedEvent{Event: "envelope", ID: string(f.ID), From: from, To: d.opts.Envelope})
	}
	if d.opts.SubstrateRuns > 0 || d.opts.SubstrateMinutes > 0 {
		if err := d.eng.RaiseGoalSubstrate(ctx, f.ID, d.opts.SubstrateRuns, d.opts.SubstrateMinutes); err != nil {
			return Outcome{}, true, err
		}
	}
	if f.Stage == domain.StageImplement {
		// someone is here: a card that stopped to wait for its environment
		// is worth another verify now, and only now
		if err := d.eng.GoalResumed(ctx, f.ID); err != nil {
			return Outcome{}, true, err
		}
	}
	switch {
	case in.Note != nil:
		if err := d.eng.GoalNote(ctx, f.ID, *in.Note); err != nil {
			return Outcome{}, true, err
		}
	case in.WrapUp:
		if err := d.eng.StopGoal(ctx, f.ID); err != nil {
			return Outcome{}, true, err
		}
	case in.Reverse != nil:
		why := ""
		if in.RequestChanges != nil {
			why = *in.RequestChanges
		}
		if err := d.eng.ReverseGoalDecision(ctx, f.ID, *in.Reverse, why); err != nil {
			return Outcome{}, true, err
		}
	case in.RequestChanges != nil && (f.Stage == domain.StageVerify || f.Stage == domain.StageImplement):
		if err := d.eng.SendBackGoal(ctx, f.ID, *in.RequestChanges, "user"); err != nil {
			return Outcome{}, true, err
		}
	case in.Approve && f.Stage == domain.StageVerify:
		return Outcome{}, true, fmt.Errorf("%s is ready for you; land it with `gummi merge %s`, send it back with --request-changes, or hand it off", f.ID, f.ID)
	case in.Approve, in.RequestChanges != nil, in.Answer != nil, in.Bounce != nil:
		return Outcome{}, false, d.applyPlainResume(ctx, f, in)
	}
	return Outcome{}, false, nil
}

// applyPlainResume is the card-level resume for a goal's own design
// conversation: an approval crosses its plan gate, an answer or a change
// note is a turn in it.
func (d *Driver) applyPlainResume(ctx context.Context, f domain.Feature, in ResumeInput) error {
	switch {
	case in.Approve:
		out, err := d.autoAdvance(ctx, f)
		if err != nil {
			return err
		}
		if out.terminal() {
			return fmt.Errorf("%s: the plan gate did not cross (%s)", f.ID, out.Status)
		}
	case in.RequestChanges != nil:
		d.opening = *in.RequestChanges
	case in.Answer != nil:
		d.opening = *in.Answer
		d.openingIsAnswer = true
	case in.Bounce != nil:
		return fmt.Errorf("%s is a goal; send it back with --request-changes instead of --bounce", f.ID)
	}
	return nil
}

// mergedGoalEvent reports a goal landed on main.
type mergedGoalEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	Cards  int    `json:"cards"`
}

// mergeGoal lands a verified goal: one merge commit joining its cards'
// commits on main, after a final catch-up and, when that brought anything
// in, a fresh run of the goal's checks.
func (d *Driver) mergeGoal(ctx context.Context, f domain.Feature, message string) (Outcome, error) {
	if f.Stage == domain.StageDone {
		return d.fail(ctx, string(f.ID), fmt.Errorf("%s is already done", f.ID))
	}
	if f.Stage != domain.StageVerify || f.VerifiedAt.IsZero() {
		return d.fail(ctx, string(f.ID), fmt.Errorf("%s is not ready for you yet (stage %s)", f.ID, f.Stage))
	}
	sha, err := d.eng.LandGoal(ctx, f.ID, message, d.actor)
	if err != nil {
		if errors.Is(err, engine.ErrGoalSentBack) {
			d.out.emit(escalationEvent{Event: "escalation", ID: string(f.ID), Stage: string(domain.StageImplement), Reason: err.Error(),
				Resume: string(f.ID), Next: d.resumeCmd(string(f.ID))})
			return Outcome{Status: StatusEscalation, ID: string(f.ID)}, nil
		}
		return d.fail(ctx, string(f.ID), err)
	}
	r, _ := d.eng.GoalReport(ctx, f.ID)
	landed := 0
	for _, c := range r.Cards {
		if c.State == "landed" {
			landed++
		}
	}
	d.out.emit(mergedGoalEvent{Event: "merged", ID: string(f.ID), Branch: f.BranchName(), Commit: sha, Cards: landed})
	return Outcome{Status: StatusVerified, ID: string(f.ID)}, nil
}

// goalNeedsBudget reports a goal stopped on a card it cannot fund. It is
// an exhaustion like any other — the envelope ran dry, a person raises it
// and resumes — so it wears the status the exit table already documents
// for one, and carries the hand-over so the reader can see what has
// landed, what is waiting, and what it asks for.
func (d *Driver) goalNeedsBudget(ctx context.Context, f domain.Feature, need engine.GoalNeedsBudget) Outcome {
	ev := goalStopEvent{
		Event: "exhausted", ID: string(f.ID), Stage: string(f.Stage),
		Reason: need.Reason, Card: string(need.Card), Needs: need.Needs,
		Resume: fmt.Sprintf("gummi resume %s --envelope <more than %d>", f.ID, f.Budget.Envelope),
	}
	if r, err := d.eng.GoalReport(ctx, f.ID); err == nil {
		ev.Goal = &r
	}
	d.out.emit(ev)
	return Outcome{Status: StatusExhausted, ID: string(f.ID)}
}

// goalNeedsSubstrate reports a goal stopped on a run it cannot afford. It
// is an exhaustion of the goal's other budget, so it wears the same status
// and the same shape of answer.
func (d *Driver) goalNeedsSubstrate(ctx context.Context, f domain.Feature, need engine.GoalNeedsSubstrate) Outcome {
	ev := goalStopEvent{
		Event: "exhausted", ID: string(f.ID), Stage: string(f.Stage), Reason: need.Reason,
		Resume: fmt.Sprintf("gummi resume %s --runs <more> --minutes <more>", f.ID),
	}
	if r, err := d.eng.GoalReport(ctx, f.ID); err == nil {
		ev.Goal = &r
	}
	d.out.emit(ev)
	return Outcome{Status: StatusExhausted, ID: string(f.ID)}
}

// goalStalled reports a goal whose agent backend could not serve it. It
// is an error exit — nothing was produced — but a resumable one that
// dropped nothing: every card keeps its branch, its spend and its place,
// and the same resume carries on once the backend is back.
func (d *Driver) goalStalled(ctx context.Context, f domain.Feature, reason string) Outcome {
	ev := goalStopEvent{
		Event: "stalled", ID: string(f.ID), Stage: string(f.Stage),
		Reason: reason, Resume: d.resumeCmd(string(f.ID)),
	}
	if r, err := d.eng.GoalReport(ctx, f.ID); err == nil {
		ev.Goal = &r
	}
	d.out.emit(ev)
	return Outcome{Status: StatusStalled, ID: string(f.ID)}
}

// goalStopEvent is the stream's record of a goal that stopped on a
// question only a person can answer.
type goalStopEvent struct {
	Event  string             `json:"event"`
	ID     string             `json:"id"`
	Stage  string             `json:"stage,omitempty"`
	Reason string             `json:"reason,omitempty"`
	Card   string             `json:"card,omitempty"`
	Needs  int                `json:"needs,omitempty"`
	Resume string             `json:"resume,omitempty"`
	Goal   *engine.GoalReport `json:"goal,omitempty"`
}

// goalReviewUnactionable ends a goal's own review loop. The critique
// asked for changes the goal cannot make — its cards are landed or
// dropped and its conductor has finished, or it has spent its rework
// rounds discovering the same thing — so the request is recorded as what
// makes the result partial and the goal goes on to its verify and its
// hand-over, rather than re-running the same critique or parking in a
// reader's inbox. reason is gatepolicy's, so the sentence the report
// carries names which of those endings it was.
func (d *Driver) goalReviewUnactionable(ctx context.Context, f domain.Feature, reason string) (Outcome, error) {
	if f.Goal.Partial == "" {
		_ = d.store.SetGoalPartial(ctx, f.ID, goalReviewPartial(reason))
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage),
		Result: "review not actionable — " + reason})
	if got, err := d.store.GetFeature(ctx, f.ID); err == nil {
		f = got
	}
	return d.autoAdvance(ctx, f)
}

// goalReviewPartial is the sentence a hand-over carries when the goal's
// own review is what made it partial. One place, so the TUI and the
// driver report the same ending in the same words.
func goalReviewPartial(reason string) string {
	switch reason {
	case "goal-review-cap":
		return "its review kept asking for changes its cards could not make"
	case "goal-review-unclear":
		return "its review finished with no clear verdict"
	default:
		return "its review asked for changes with no card left to make them"
	}
}
