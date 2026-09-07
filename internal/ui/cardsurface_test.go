package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// The card surface's invariants, asserted rather than described.
//
// Each of these is a property the redesign rests on, and each is a
// property that a later change can break quietly — an id added to the
// answer set with nothing to run it, a key answered for an option nobody
// can see, a narration generated for a card nobody is waiting on. They
// are cheap to state here and expensive to find on a board.

// surfaceInputs is a broad sweep of the states a card can stop in — every
// stage crossed with every kind, plus the session states, attention
// kinds and verdicts that override stage guidance. The invariants below
// hold over the whole sweep, not over the handful of shapes that were
// convenient to write down.
func surfaceInputs() []nextInput {
	var out []nextInput
	sessions := []engine.SessionState{"", engine.StateQueued, engine.StateRunning, engine.StatePaused, engine.StateDone, engine.StateInteractive}
	attns := []attnKind{"", attnGate, attnFailure, attnBudget, attnQuestion}
	verdicts := []reviewVerdict{verdictUnclear, verdictPass, verdictFail, verdictChanges, verdictBlocked}
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		for _, stage := range domain.Stages {
			for _, sess := range sessions {
				for _, attn := range attns {
					for _, verdict := range verdicts {
						for _, landed := range []bool{false, true} {
							out = append(out, nextInput{
								stage: stage, kind: kind, sess: sess, attn: attn,
								verdict: verdict, landed: landed, hasWorktree: true,
							})
						}
					}
				}
			}
		}
	}
	// the gate blockers and the ask, which are their own arms
	return append(out, []nextInput{
		{stage: domain.StagePlan, kind: domain.KindFeature, openSpecQs: 2},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, openDiffComments: 1},
		{stage: domain.StagePlan, kind: domain.KindBug, undrafted: []string{"Root cause"}},
		{stage: domain.StageImplement, kind: domain.KindFeature, sess: engine.StateRunning, hasAsk: true},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, failedCheck: "go vet"},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictFail, verifyBounces: 3},
		{stage: domain.StagePlan, kind: domain.KindFeature, sess: engine.StateInteractive, live: true},
	}...)
}

// runnableIDs are the ids runCardAction can actually perform without a
// key: its own switch. Everything else has to carry an accelerator
// boardVerb answers, which is what runCardAction does with a keyed
// action.
var runnableIDs = map[string]bool{
	expandID: true, "topup": true, "duplicate": true, "profile": true,
	"ask": true, "changes": true, "gate": true, "run": true,
	"prlink": true, "prunlink": true, "prpull": true,
}

// TestInvariantCompleteness — invariant 1. Every id the answer set can
// return is something the reader can reach and enter can run: either it
// carries a key boardVerb answers, or runCardAction has a case for it.
// No workflow answer may live only in "/".
//
// The failure this prevents is a row on screen, highlighted, named, that
// enter silently does nothing with.
func TestInvariantCompleteness(t *testing.T) {
	seen := map[string]bool{}
	for _, in := range surfaceInputs() {
		for _, a := range stageActions(in) {
			if seen[a.id+"/"+a.key] {
				continue
			}
			seen[a.id+"/"+a.key] = true
			if a.key != "" {
				// keyed: runCardAction hands it to boardVerb, which must
				// have a case for the letter. The board's own table is the
				// list of letters it answers.
				var known bool
				for _, b := range boardKeyTable() {
					if b == a.key {
						known = true
					}
				}
				if !known {
					t.Errorf("answer %q wears key %q, which the board does not answer", a.id, a.key)
				}
				continue
			}
			if !runnableIDs[a.id] {
				t.Errorf("answer %q has no key and no runCardAction case — enter would do nothing with it", a.id)
			}
		}
	}
}

// boardKeyTable is the set of accelerators boardVerb answers, taken from
// the board's own binding table so this test cannot drift from the
// handler the way a hand-written list would.
func boardKeyTable() []string {
	m := populatedShell(120, 34)
	var keys []string
	for _, b := range m.boardBindings() {
		keys = append(keys, b.key)
	}
	// enter is the run action's accelerator and is listed; the rest of
	// the table is single letters.
	return keys
}

// TestInvariantLockstep — invariant 2. The visible option set and the
// keys the handler answers agree exactly.
//
// Stated for the composer's verb vocabulary, which is where a reader
// actually reaches an action by name on the card page: every verb must
// either fire, degrade to a NON-EMPTY pre-filtered menu, or route to a
// refusal that says why. None may silently do nothing.
//
// This is the divergence foreignBlockedKeys already exists to prevent —
// an action absent from the list but answered by its key — now stated
// for every card rather than only for a foreign-driven one.
func TestInvariantLockstep(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		for _, stage := range []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify} {
			m := attachedBoard(t, 120, 34)
			m.cardOpen = true
			m.rows[m.sel].F.Kind = kind
			m.rows[m.sel].F.Stage = stage
			r := m.rows[m.sel]

			for verb := range verbs {
				if !m.verbDegrades(r, verb) {
					// it fires; the routing tests cover what it fires.
					continue
				}
				cm := newCommandMenu(m.globalCommands(), m.runCommand)
				cm.filter.SetValue(verb)
				if len(cm.visible()) == 0 {
					t.Errorf("%s/%s: /%s degrades to a menu with no rows — a dead end",
						kind, stage, verb)
				}
			}
		}
	}
}

// TestInvariantLockstepStopHere is the specific divergence the merged
// answer set could have introduced: "stop here" wears p, and boardVerb's
// p pauses only a NON-interactive session — with an interactive one it
// opens the dependency picker instead. An answer that names one act and
// performs another is worse than no answer.
func TestInvariantLockstepStopHere(t *testing.T) {
	for _, in := range surfaceInputs() {
		for _, a := range stageActions(in) {
			if a.id != "pause" {
				continue
			}
			switch in.sess {
			case "", engine.StatePaused, engine.StateInteractive:
				t.Errorf("stop here offered with sess=%q, where p does not pause", in.sess)
			}
		}
	}
}

// TestInvariantFreeAtRest — invariant 4. Nothing is narrated for a
// running, queued, done or landed card.
//
// The caching half of the invariant ("nothing regenerates while the
// newest event id and the blocking counts are unchanged") is vacuous
// while the narration is deterministic: it is a pure function of state
// that costs nothing to call, so there is nothing to cache and nothing
// to go stale. It becomes real with the model-written sentence, and the
// cache key is named in narration.go's own doc for when it does.
func TestInvariantFreeAtRest(t *testing.T) {
	for _, in := range surfaceInputs() {
		quiet := in.landed || in.stage == domain.StageDone ||
			in.sess == engine.StateQueued ||
			(in.sess == engine.StateRunning && !in.hasAsk)
		if !quiet {
			continue
		}
		if narrationStop(in) {
			t.Errorf("%+v: narrated a card nobody is waiting on", in)
		}
	}

	// and the gate is the one cardNarration actually consults, so a
	// quiet card gets no paragraph however much its state would
	// otherwise have to say
	m := attachedBoard(t, 120, 34)
	row := m.rows[m.sel]
	for _, in := range []nextInput{
		{stage: domain.StageVerify, kind: domain.KindFeature, sess: engine.StateRunning, verdict: verdictFail, failedCheck: "go vet"},
		{stage: domain.StageVerify, kind: domain.KindFeature, sess: engine.StateQueued, attn: attnFailure},
		{stage: domain.StageDone, kind: domain.KindFeature, attn: attnGate},
		{stage: domain.StageVerify, kind: domain.KindFeature, landed: true, attn: attnGate, verdict: verdictFail},
	} {
		if got := m.cardNarration(in, row); len(got) != 0 {
			t.Errorf("%+v: narrated a quiet card: %+v", in, got)
		}
	}
}

// TestInvariantFreeAtRestRegeneration — invariant 4's OTHER half, which
// Phase 0 could only state vacuously (nothing was generated, so nothing
// could be regenerated). Now that one claim costs a model turn, "never
// regenerated while the newest event id and the blocking counts are
// unchanged" is a real property with a real cache behind it, and this is
// where it is stated. The behavioural half — a pass is dispatched once
// per state and not once per frame — is TestNarrationNotRegeneratedFor-
// TheSameState; this pins the key itself, because a key that ignored one
// of its inputs would pass that test while pinning a stale sentence to a
// card that has moved on.
func TestInvariantFreeAtRestRegeneration(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	id := m.rows[m.sel].F.ID
	base := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate}

	same := m.narrationKey(base, id)
	if again := m.narrationKey(base, id); again != same {
		t.Fatalf("the same state keyed twice: %q then %q", same, again)
	}

	// Every input the design names must move the key. A blocker resolved
	// appends nothing to the log but changes what the stop MEANS, which
	// is exactly why the counts are in the key beside the log's newest
	// seq.
	for name, mutate := range map[string]func(*nextInput){
		"an open spec thread":     func(in *nextInput) { in.openSpecQs = 1 },
		"an open diff comment":    func(in *nextInput) { in.openDiffComments = 1 },
		"an undrafted section":    func(in *nextInput) { in.undrafted = []string{"Verification plan"} },
		"the card's stage moving": func(in *nextInput) { in.stage = domain.StageImplement },
	} {
		in := base
		mutate(&in)
		if got := m.narrationKey(in, id); got == same {
			t.Errorf("%s does not move the cache key (%q)", name, got)
		}
	}

	// and the log itself, which is the version stamp the design names
	m.cardEvents[id] = append(m.cardEvents[id], state.CardEvent{Feature: id, Seq: 9999})
	if got := m.narrationKey(base, id); got == same {
		t.Errorf("a new event does not move the cache key (%q)", got)
	}
}

// TestInvariantDeterminism — invariant 5. The option list is a pure
// function of nextInput: same input, same answers, no agent, no store,
// no engine anywhere in reach.
//
// The narration is a separate thing on purpose, and the split is what
// DESIGN §6.3 turns on — a model may describe the situation and may
// never change the options. If this test ever needs a Shell, that split
// has been broken.
func TestInvariantDeterminism(t *testing.T) {
	for _, in := range surfaceInputs() {
		first := stageActions(in)
		for i := 0; i < 3; i++ {
			again := stageActions(in)
			if len(first) != len(again) {
				t.Fatalf("%+v: answer count moved between calls: %d then %d", in, len(first), len(again))
			}
			for j := range first {
				if first[j] != again[j] {
					t.Fatalf("%+v: answer %d moved between calls: %+v then %+v", in, j, first[j], again[j])
				}
			}
		}
	}
}

// TestAnswerSetHoldsOnlyAnswers is the merge itself, asserted: no stop
// ever offers bounce, "request changes" and a re-run as three separate
// rows, and no stop offers a reading surface as an option — those are
// the page's tabs.
func TestAnswerSetHoldsOnlyAnswers(t *testing.T) {
	for _, in := range surfaceInputs() {
		acts := stageActions(in)
		back := 0
		for _, a := range acts {
			if a.label == "send it back" {
				back++
			}
		}
		if back > 1 {
			t.Errorf("%+v: %d rows labelled \"send it back\"", in, back)
		}
		// the blocked-gate lead is the one row that may name a reading
		// surface, and it is not an option among the answers — it is the
		// "you cannot cross yet" sentence, which leads.
		for i, a := range acts {
			if (a.id == "spec" || a.id == "diff") && i != 0 {
				t.Errorf("%+v: %q offered as an answer at position %d — reading surfaces are tabs", in, a.id, i)
			}
		}
		for _, a := range acts {
			if a.id == "transcript" {
				t.Errorf("%+v: the transcript is back — the thread is the transcript", in)
			}
		}
	}
}

// TestNarrationNeverContradictsTheAnswers: the loop-breaker is the one
// sentence that argues against an answer, and it must argue in prose
// without changing the rows. Anything the narration says is advice; the
// answer set is the contract.
func TestNarrationNeverContradictsTheAnswers(t *testing.T) {
	base := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, verdict: verdictFail,
	}
	for _, bounces := range []int{0, 1, 5} {
		in := base
		in.verifyBounces = bounces
		if got, want := keysOf(stageActions(in)), keysOf(stageActions(base)); got != want {
			t.Errorf("%d prior bounces changed the answers: %q, want %q", bounces, got, want)
		}
	}
	// and it does say something, or the guard was simply deleted
	loud := base
	loud.verifyBounces = 2
	if !strings.Contains(whyItStopped(loud), "3 times") {
		t.Errorf("the loop-breaker says nothing: %q", whyItStopped(loud))
	}
}
