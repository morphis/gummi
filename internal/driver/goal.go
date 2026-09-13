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
		for _, st := range res.Start {
			if running[st.ID] != nil {
				continue
			}
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
		if res.Finished {
			d.reviewsRun++
			d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "critiquing"})
			wg.Wait()
			return d.awaitCritique(ctx, f)
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
				Resume: string(f.ID), Next: resumeCmd(string(f.ID))})
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
	return Outcome{Status: StatusDone, ID: string(f.ID)}, nil
}
