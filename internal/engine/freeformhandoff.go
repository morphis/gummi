package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
)

// A freeform card's handoff brief: what a "continue this work as a spec"
// hands the minted card's architect. The work itself already transfers —
// the spec's branch is cut from the session's tip — so what this file
// carries is the conversational half, which otherwise dies with the
// session: what was asked, what was decided (including the answers the
// agent was given), what was done on the branch, and what remains.
//
// Two ways to get it, and the first is the one that works:
//
//   - The session writes its own brief. One synchronous turn, on the
//     session's own agent and model, asking exactly that. The turn runs
//     on a fresh, tool-less session of the same backend — the conversation
//     rides in as a replay hint — so it can neither read nor write
//     anything, the project memory included; its product is the brief
//     alone, and its usage is booked against the card like any turn's.
//   - Assembled from the persisted transcript. For a session that cannot
//     answer — closed, or the backend failed — the asks, the answered
//     questions and the last reply are laid out as a draft. Deterministic,
//     free, and never mistaken for the session's own words: the source is
//     reported alongside the text.
//
// Both are bounded by SpecBriefMax, and the caller shows the draft to the
// person for edit before anything mints — the brief is an input, not a
// verdict.

// SpecBriefMax bounds how much of a session's conversation rides into the
// spec card's brief: the architect needs what was asked and decided, not a
// transcript, and the branch it continues carries what was done. The cap
// trims the assembled draft's oldest asks first; a live brief is
// hard-trimmed to it.
const SpecBriefMax = 6000

// BriefSource says where a handoff brief came from, so a degraded draft is
// never mistaken for the session's own words.
type BriefSource string

const (
	// BriefLive: the session answered the brief turn itself.
	BriefLive BriefSource = "live"
	// BriefAssembled: laid out from the persisted transcript, because no
	// live session could answer.
	BriefAssembled BriefSource = "assembled"
)

// BriefDrafting is the busy word both faces show while the brief turn is
// in flight — what the agent is doing, rather than the bare "working".
const BriefDrafting = "drafting the handoff brief"

// setBriefing raises or clears the card's in-flight brief flag — the one
// thing both faces' busy word reads while the brief turn runs. It lives on
// the card's session, not on the turn's own (a fresh, tool-less session
// nothing renders), so a reader of the card sees gummi drafting from the
// first moment.
func (ff *FreeformSession) setBriefing(on bool) {
	ff.mu.Lock()
	ff.briefing = on
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

// beginBriefing claims the in-flight brief flag: the already-drafting
// refusal and its raise are one locked step, so two simultaneous dialog
// opens cannot both pass the check and both run brief turns, each spending
// the envelope and appending an exchange. It reports whether the claim
// succeeded; the caller releases with setBriefing(false) — including when
// the backend never gave the turn a session to run on, which rolls the
// claim back with nothing appended: the failure happened before the line.
func (ff *FreeformSession) beginBriefing() bool {
	ff.mu.Lock()
	already := ff.briefing
	if !already {
		ff.briefing = true
	}
	ff.mu.Unlock()
	if already {
		return false
	}
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	return true
}

// Briefing reports whether gummi's handoff-brief turn is in flight on this
// card.
func (ff *FreeformSession) Briefing() bool {
	return ff.busyBriefing()
}

func (ff *FreeformSession) busyBriefing() bool {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.briefing
}

// briefTurnTimeout bounds the live brief turn. The caller's context (a web
// request, a dialog fetch) bounds it too, but a caller without one — the
// terminal dialog fetch runs as a bare command — must not wait on a wedged
// backend forever.
const briefTurnTimeout = 3 * time.Minute

// SessionHandoffBrief returns the brief a "continue as a spec" handoff
// gives the minted card's architect, and where it came from.
//
// A live session is asked to write its own brief first: one synchronous
// turn, refused while the session is mid-turn, blocked on an unanswered
// question, or already drafting a brief — the same refusals any send
// rides, the last on a per-card in-flight guard — and falling back to the
// assembled draft when the backend fails to answer. A session with no live
// backend (closed, or restored after a restart) is never asked; its
// persisted transcript is all there is, and the draft is assembled from it.
func (e *Engine) SessionHandoffBrief(ctx context.Context, id domain.FeatureID) (string, BriefSource, error) {
	ff := e.Freeform(id)
	if ff != nil {
		if err := ff.briefRefusals(); err != nil {
			return "", "", err
		}
		sess := ff.Session()
		// A live backend is asked; anything else — a session restored
		// after a restart carries its conversation but no backend, and
		// spawning one just to summarize it is not the deal — is answered
		// from the persisted transcript.
		if sess != nil && sess.Live() && len(sess.Snapshot().Transcript) > 0 {
			brief, err := ff.liveBrief(ctx, sess)
			if err == nil {
				return trimBrief(brief), BriefLive, nil
			}
			if errors.Is(err, agent.ErrBusy) {
				// refused, not failed: a second dialog open lost the
				// in-flight guard while the first turn still runs, and
				// the caller hears that rather than a degraded draft
				// passed off as an answer.
				return "", "", err
			}
			// The backend could not answer. The conversation is still the
			// record, so the draft is assembled from it and the source says
			// so — a degraded brief must never read as the session's words.
		}
	}
	snap, _ := e.freeformTranscript(id)
	return AssembledBrief(snap), BriefAssembled, nil
}

// briefRefusals carries the refusals any send to this session rides: a
// turn in flight, a question the agent asked that nobody has answered,
// and a brief turn of this card's own already drafting. All mean the
// conversation is not settled enough to distill, and all fire before the
// brief turn spends anything; a simultaneous dialog open that slips past
// them still meets the in-flight guard's atomic claim in liveBrief.
func (ff *FreeformSession) briefRefusals() error {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return nil
	}
	snap := sess.Snapshot()
	if ff.busyBriefing() {
		return fmt.Errorf("%s is %s already; open the dialog again once it is done: %w",
			ff.id, BriefDrafting, agent.ErrBusy)
	}
	if snap.Busy {
		return fmt.Errorf("%s is mid-turn; write the spec once this turn ends: %w", ff.id, agent.ErrBusy)
	}
	if snap.PendingAsk != nil {
		return fmt.Errorf("%s is waiting on your answer — answer the question before writing a spec from it: %w",
			ff.id, agent.ErrBusy)
	}
	return nil
}

// briefTurnPrompt is the gummi-authored line the brief turn rides on: what
// is happening, and the contract the reply must answer. It is recorded on
// the session's transcript as the system author — the way a stage kickoff
// reads — and sent to the backend as the turn itself, so the thread shows
// the words the work actually rides on.
func briefTurnPrompt() string {
	return "You are being continued as a spec card: this session is closing and a feature card " +
		"will carry the work on, planned by an architect who was not part of this conversation. " +
		"Write the handoff brief that architect will read — what was asked of you here, what was " +
		"decided (including every question you asked and the answer you were given), what was done " +
		"on the branch, and what remains.\n\n" +
		"Answer in four sections, in order, headed asked, decided, done, remaining. " +
		"Keep the whole brief under " + strconv.Itoa(SpecBriefMax) + " characters — it becomes the spec's " +
		"opening, not a transcript. Your reply is the brief itself: write it and nothing else."
}

// liveBrief runs the brief turn: a fresh tool-less session of the same
// backend is asked to write the brief, the gummi-authored line is recorded
// on the session immediately before the turn is sent, and the reply lands
// beneath the line as the agent's own turn.
//
// The exchange is ordered so the thread never shows gummi drafting into
// nothing: the in-flight flag is claimed before the backend is asked for a
// session — the already-drafting refusal and its raise one locked step, so
// two simultaneous dialog opens cannot both pass and both run brief turns
// — the line is appended and the turn sent with nothing else writing to
// the transcript in between; and if the turn then fails, a closing note
// says so, so the closed card's record explains the unanswered line. A
// failure before the send (no session to run the turn on) rolls the claim
// back and records nothing: the line was never written, so there is
// nothing left unanswered.
func (ff *FreeformSession) liveBrief(ctx context.Context, sess *Session) (string, error) {
	e := ff.engine
	ff.mu.Lock()
	rc, backend, workDir := ff.rc, ff.backend, ff.workDir
	ff.mu.Unlock()
	ag, err := e.sessionAgent(backend)
	if err != nil {
		return "", fmt.Errorf("the session's backend: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, briefTurnTimeout)
	defer cancel()

	// The conversation rides in as text — the same bounded replay a
	// respawned backend gets — because the brief turn must not resume or
	// disturb the conversation the live backend is still holding.
	snap := sess.Snapshot()
	var hints []string
	if replay := freeformReplayHint(snap.Transcript, false); replay != "" {
		hints = append(hints, replay)
	}
	// The claim is the refusal's check and its raise in one step: whoever
	// loses it is the second of two simultaneous dialog opens, refused
	// rather than spent. Released after the turn — win or lose — and
	// rolled back by it when the backend never gave the turn a session.
	if !ff.beginBriefing() {
		return "", fmt.Errorf("%s is %s already; open the dialog again once it is done: %w",
			ff.id, BriefDrafting, agent.ErrBusy)
	}
	defer ff.setBriefing(false)

	briefSess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:    workDir,
		Role:       agent.RoleImplementer,
		Model:      rc.Model,
		Permission: e.cfg.Permission,
		SystemHints: append([]string{
			"You are writing a handoff summary of a conversation that is ending; " +
				"read-only — do not modify any file.",
		}, hints...),
		FeatureID: string(ff.id),
		// No Tools, and no MCP endpoint: the brief turn reads and writes
		// nothing — not the worktree, not the project memory tier. Its
		// product is the brief alone.
	})
	if err != nil {
		return "", fmt.Errorf("starting the brief turn: %w", err)
	}
	defer func() { _ = briefSess.Close() }()

	prompt := briefTurnPrompt()
	sess.appendSystem(prompt)
	e.persist(sess)
	if err := briefSess.Send(ctx, prompt); err != nil {
		ff.noteBriefFailure(sess, err)
		return "", err
	}
	brief, err := collectBrief(ctx, e, ff, briefSess)
	if err != nil {
		ff.noteBriefFailure(sess, err)
		return "", err
	}
	sess.appendAssistantReply(brief)
	e.persist(sess)
	e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	return brief, nil
}

// collectBrief drains the brief session's event stream to the turn's end
// and returns the reply, booking its usage against the card as the session's
// own turns are booked.
func collectBrief(ctx context.Context, e *Engine, ff *FreeformSession, briefSess agent.Session) (string, error) {
	var text assistantText
	for {
		select {
		case ev, ok := <-briefSess.Events():
			if !ok {
				if strings.TrimSpace(text.String()) == "" {
					return "", errors.New("the backend stopped before the brief was written")
				}
				return text.String(), nil
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventUsage:
				e.recordOneShotUsage(ff.id, domain.StageOpen, ev.Usage)
			case agent.EventIdle, agent.EventBudgetExhausted:
				if strings.TrimSpace(text.String()) == "" {
					return "", errors.New("the backend answered the brief turn with nothing")
				}
				return text.String(), nil
			case agent.EventError:
				return "", ev.Err
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// appendAssistantReply records a finished assistant message as its own
// transcript entry. The brief's reply is appended beside the gummi-authored
// line that asked for it; finishAssistant cannot do that here — it
// finalizes an in-flight streamed entry, and an interrupted turn leaves one
// behind that the brief must not overwrite.
func (s *Session) appendAssistantReply(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text, At: time.Now()})
	s.live.Emit(livelog.Record{Kind: livelog.KindMessage, Text: text})
}

// noteBriefFailure closes the exchange the failure cut short: the
// gummi-authored line is already on the transcript, so the record says why
// no reply followed it rather than showing gummi drafting into nothing.
func (ff *FreeformSession) noteBriefFailure(sess *Session, err error) {
	sess.appendSystem("the handoff brief turn did not answer (" + err.Error() + ") — " +
		"the draft was assembled from this conversation instead")
	ff.engine.persist(sess)
}

// freeformTranscript is the conversation a brief is assembled from: the
// open session's when one exists, the closed card's record otherwise.
func (e *Engine) freeformTranscript(id domain.FeatureID) (Snapshot, bool) {
	if ff := e.Freeform(id); ff != nil {
		if snap := ff.Snapshot(); len(snap.Transcript) > 0 {
			return snap, true
		}
	}
	if snap, ok := e.FreeformHistory(id); ok && len(snap.Transcript) > 0 {
		return snap, true
	}
	return Snapshot{}, false
}

// AssembledBrief lays a session's persisted transcript out as a handoff
// brief draft: the person's asks in order under asked, each ask_user
// question with its answer under decided, and the agent's last substantive
// reply as the state of the work under done. The asked list skips the
// echoes of ask_user answers — user turns carrying AnsweredBy — which are
// already captured under decided; counting them as asks would report every
// question twice.
func AssembledBrief(snap Snapshot) string {
	var asks, decided []string
	reply := ""
	pending := "" // the question an unanswered ask_user call left waiting
	for _, m := range snap.Transcript {
		switch m.Author {
		case AuthorTool:
			if q := askQuestion(m); q != "" {
				pending = q
			}
		case AuthorUser:
			body := strings.TrimSpace(m.Content)
			if body == "" {
				continue
			}
			if m.AnsweredBy != "" {
				// an ask_user answer echo: it belongs under decided, with
				// the question it closed when the record still carries it
				if pending != "" {
					decided = append(decided, pending+" — answered: "+body)
					pending = ""
				} else {
					decided = append(decided, body)
				}
				continue
			}
			asks = append(asks, body)
			pending = ""
		case AuthorAssistant:
			if body := strings.TrimSpace(m.Content); body != "" {
				reply = body
			}
		}
	}
	return boundAssembled(asks, decided, reply)
}

// askQuestion reads the question an ask_user call recorded, when the
// transcript entry carries it: the salient detail the backend reported, or
// the line's own text past the tool's name.
func askQuestion(m Message) string {
	if m.Tool != "" && m.Tool != askToolName {
		return ""
	}
	if !strings.HasPrefix(m.Content, askToolName) {
		return ""
	}
	if m.Detail != "" {
		return strings.TrimSpace(m.Detail)
	}
	_, q, _ := strings.Cut(strings.TrimPrefix(m.Content, askToolName), "  ")
	return strings.TrimSpace(q)
}

// boundAssembled renders the assembled draft's sections and bounds the
// whole at SpecBriefMax: the oldest asks are dropped first, then the
// oldest decisions, and the last reply and the section shape stay while
// anything at all fits.
func boundAssembled(asks, decided []string, reply string) string {
	size := func(a, d []string, r string) int {
		return len(renderAssembled(a, d, r, 0, 0))
	}
	droppedAsks, droppedDecided := 0, 0
	for len(asks) > 0 && size(asks, decided, reply) > SpecBriefMax {
		asks = asks[1:]
		droppedAsks++
	}
	for len(decided) > 0 && size(asks, decided, reply) > SpecBriefMax {
		decided = decided[1:]
		droppedDecided++
	}
	if size(asks, decided, reply) > SpecBriefMax && reply != "" {
		// the reply is what is left to spend: it gets whatever room the
		// fixed scaffolding leaves — its own closing newline included —
		// and no more
		rest := len(renderAssembled(asks, decided, "", droppedAsks, droppedDecided)) -
			len(doneEmptyNote)
		reply = trimBriefTo(reply, max(SpecBriefMax-rest-1, 1))
	}
	return renderAssembled(asks, decided, reply, droppedAsks, droppedDecided)
}

// doneEmptyNote is the done section's stand-in for a conversation that
// records nothing done — the line a trimmed reply replaces, so its length
// is what the reply's budget is measured against.
const doneEmptyNote = "- (the conversation records nothing done)\n"

// renderAssembled is the draft's text: the four sections, in order, with a
// note where the cap cut the oldest of a list off.
func renderAssembled(asks, decided []string, reply string, droppedAsks, droppedDecided int) string {
	var b strings.Builder
	list := func(head string, items []string, dropped int, empty string) {
		b.WriteString(head + "\n")
		switch {
		case dropped > 0 && len(items) == 0:
			b.WriteString("- (everything " + empty + " was left out to fit)\n")
		case dropped > 0:
			b.WriteString("- (older entries left out to fit)\n")
		case len(items) == 0:
			b.WriteString("- (nothing was " + empty + " in the conversation)\n")
		}
		for _, a := range items {
			b.WriteString("- " + strings.ReplaceAll(a, "\n", "\n  ") + "\n")
		}
	}
	list("asked:", asks, droppedAsks, "asked")
	b.WriteString("\n")
	list("decided:", decided, droppedDecided, "decided")
	b.WriteString("\ndone:\n")
	if reply == "" {
		b.WriteString(doneEmptyNote)
	} else {
		b.WriteString(reply + "\n")
	}
	b.WriteString("\nremaining:\n")
	b.WriteString("- (this draft is assembled from the conversation alone — the branch's diff shows how far the work got)")
	return b.String()
}

// trimBrief hard-trims a live brief to SpecBriefMax, cutting at a rune
// boundary and saying so.
func trimBrief(brief string) string {
	return trimBriefTo(brief, SpecBriefMax)
}

// trimBriefTo is trimBrief at an explicit bound: the trimmed text — its
// cut marker included — never exceeds it.
func trimBriefTo(brief string, max int) string {
	brief = strings.TrimSpace(brief)
	if len(brief) <= max {
		return brief
	}
	const marker = "\n[… trimmed to fit]"
	cut := max - len(marker)
	if cut < 1 {
		cut = 1
	}
	for cut > 0 && (brief[cut]&0xC0) == 0x80 {
		cut--
	}
	return strings.TrimRight(brief[:cut], " \t\n") + marker
}
