package threadfold

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// ItemType is what one thread item is, and so how a page draws it.
type ItemType string

const (
	// ItemStage divides the thread where a stage session began: stage,
	// role, model and flavor, and — once it has exited — its verdict,
	// what it cost and its outcome mark.
	ItemStage ItemType = "stage"
	// ItemMessage is a turn the agent or gummi itself wrote.
	ItemMessage ItemType = "message"
	// ItemYou is a turn a person typed.
	ItemYou ItemType = "you"
	// ItemTools is a run of consecutive tool calls, grouped.
	ItemTools ItemType = "tools"
	// ItemReceipt is a one-line record of something decided: a gate
	// crossed, an ask answered, a park, a mode change, a decision
	// superseded before anyone answered it.
	ItemReceipt ItemType = "receipt"
	// ItemVerify is a run of gummi's own check results ("check <name>"
	// rows), with the verdict the session exited on.
	ItemVerify ItemType = "verify"
	// ItemDecision marks where the decision the card is waiting on now
	// was raised. The decision itself — its options, whether it is still
	// open — is the card's, not the thread's; this only places it.
	ItemDecision ItemType = "decision"
	// ItemNote is any other logged fact: a goal's log entry, or a row of
	// a kind this fold has no sentence for (its kind, verbatim).
	ItemNote ItemType = "note"
	// ItemStretch is a rule opening or closing a period autopilot ran
	// the card.
	ItemStretch ItemType = "stretch"
)

// How a person's turn reached the card, where the log can say.
const (
	// ViaSteered is a line typed into a stage session while it ran.
	ViaSteered = "steered"
	// ViaAnswer is a line that answered an ask — the echo of the ask's
	// own answer in the session transcript.
	ViaAnswer = "answer"
	// ViaConsult is a turn of the card's consult conversation: a question
	// a person asked it (ItemYou) or its answer (ItemMessage). It is read
	// only; it never steered the card.
	ViaConsult = "consult"
)

// Item is one thing a thread draws.
//
// Seq is the seq of the newest event the item folds, so a client pages
// with ?after=<seq> (Since). An item can grow after it was first
// delivered — a tool group gains a call, a stage divider learns its
// verdict when the stage exits — and when it does its Seq moves forward
// with it, so the next page carries it again. Key is the item's stable
// identity across those deliveries: a client upserts by Key rather than
// appending, and a grown item replaces the one it already drew.
type Item struct {
	Seq   int64        `json:"seq"`
	Key   string       `json:"key"`
	T     ItemType     `json:"t"`
	At    time.Time    `json:"time"`
	Stage domain.Stage `json:"stage,omitempty"`

	// ItemStage
	Role    string  `json:"role,omitempty"`
	Model   string  `json:"model,omitempty"`
	Flavor  string  `json:"flavor,omitempty"`
	Exited  bool    `json:"exited,omitempty"`
	Verdict string  `json:"verdict,omitempty"`
	Credits float64 `json:"credits,omitempty"`
	// Outcome is the receipt's mark: state.StatusOK, state.StatusFail,
	// or "" for neutral (Segment.Outcome).
	Outcome string `json:"outcome,omitempty"`

	// ItemMessage / ItemYou: the author as the thread labels it
	// (AuthorLabel) and the turn's markdown, sanitized. ItemNote carries
	// its sentence in Text too, and Outcome its status.
	Author string `json:"author,omitempty"`
	Text   string `json:"text,omitempty"`
	Via    string `json:"via,omitempty"`
	// By names the person who typed an ItemYou, when the line carried a
	// name (a web viewer); empty for the terminal's own "you".
	By string `json:"by,omitempty"`
	// Attachments are the images an ItemYou turn carried, in order.
	Attachments []engine.AttachmentRef `json:"attachments,omitempty"`

	Tools    []ToolCall   `json:"tools,omitempty"`
	Receipt  *Receipt     `json:"receipt,omitempty"`
	Checks   []Check      `json:"checks,omitempty"`
	Decision *Decision    `json:"decision,omitempty"`
	Stretch  *StretchMark `json:"stretch,omitempty"`
}

// ToolCall is one call in an ItemTools group.
type ToolCall struct {
	Tool   string `json:"tool"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Status is state.StatusOK, state.StatusFail, StatusWatching (a
	// backend's own background watch, still outstanding — WatchTool), or
	// "" while an ordinary call has no reported outcome.
	Status string `json:"status,omitempty"`
	MS     int64  `json:"ms,omitempty"`
	// Output is kept for a failed call only: the tail a person needs to
	// see why. A successful call's output is not the thread's to show.
	Output string `json:"output,omitempty"`
}

// Receipt is an ItemReceipt's record.
type Receipt struct {
	// Kind is the event the receipt records: "gate", "ask", "park",
	// "autopilot" (a stored mode change) or "decision" (superseded).
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	// Text is the sentence the TUI thread prints for the same row.
	Text string `json:"text"`
	// By is who decided, as the thread names them ("you", "autopilot",
	// or a machine actor's own name); empty where nobody did.
	By string `json:"by,omitempty"`
}

// Check is one gummi-checks result in an ItemVerify.
type Check struct {
	Name string `json:"name"`
	// Status is gummi's word for the result: "pass", "FAIL (exit 1)",
	// "TIMEOUT (killed by deadline)", …
	Status string `json:"status"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	MS     int64  `json:"ms,omitempty"`
}

// Decision is an ItemDecision's reference to the decision it places.
type Decision struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
}

// StretchMark is an ItemStretch's rule.
type StretchMark struct {
	// Edge is "open" or "close".
	Edge  string `json:"edge"`
	Label string `json:"label"`
	// How, Reason and Tally are a closing rule's: how the period ended,
	// the sentence its closing event carried, and what autopilot decided
	// inside it ("2 gates · 1 answer", empty when nothing).
	How    StretchClose `json:"how,omitempty"`
	Reason string       `json:"reason,omitempty"`
	Tally  string       `json:"tally,omitempty"`
	Mode   string       `json:"mode,omitempty"`
}

// Options is what Items needs besides the log.
type Options struct {
	// Spend is the card's stage_spend rows (Store.StageBreakdown), the
	// meter a stage divider's credits fall back to (ReceiptCredits).
	Spend []state.StageSpend
	// Live reports whether a session is driving the card right now
	// (state.CardIsLive) — what decides whether a period the log still
	// calls running is running or orphaned (CloseOrphaned).
	Live bool
}

// Items folds a card's event log into the items a page draws, oldest
// first. It reads the same segment fold (Segments), answered-decision
// set and stretches the TUI thread renders from, and every sentence it
// carries is the one the TUI prints for the same row.
//
// Unlike the TUI, which folds every stage but the live one to a single
// receipt line, Items keeps every stage open: a divider, then everything
// that happened in it. Folding is the page's choice to make. events may
// hold tool_result rows; they are merged onto their calls first
// (state.FoldToolResults).
func Items(events []state.CardEvent, opt Options) []Item {
	events = state.FoldToolResults(events)
	segs := Segments(events)
	stretches := CloseOrphaned(Stretches(events), events, opt.Live)
	answered := AnsweredDecisions(events)

	credits := segmentCredits(segs, opt.Spend)
	enterAt := map[int]int{} // event index → the segment it opens
	segOf := map[int]int{}   // event index → the segment it belongs to
	for k, seg := range segs {
		enterAt[seg.EnterIdx] = k
		for _, idx := range seg.EvIdx {
			segOf[idx] = k
		}
	}
	opens := map[int]Stretch{}
	closes := map[int]Stretch{}
	var trailing []Stretch // closed by judgement, not by a row
	for _, st := range stretches {
		opens[st.From] = st
		switch {
		case st.Running():
		case st.To < len(events):
			closes[st.To] = st
		default:
			trailing = append(trailing, st)
		}
	}
	current := currentDecision(events, answered)
	echoes := askEchoes(events, segOf)

	var out []Item
	group := -1 // index into out of the tool or verify group still growing
	emit := func(it Item) {
		out = append(out, it)
		group = -1
	}

	for i, ev := range events {
		if k, ok := enterAt[i]; ok {
			emit(stageItem(segs[k], credits[k], ev, events))
		}
		if st, ok := opens[i]; ok {
			emit(Item{
				Seq: ev.Seq, Key: "open:" + seqKey(ev.Seq), T: ItemStretch, At: st.OpenedAt, Stage: ev.Stage,
				Stretch: &StretchMark{Edge: "open", Label: StretchOpenLabel, Mode: st.Mode},
			})
		}
		closedHere := false
		if st, ok := closes[i]; ok {
			emit(closeItem(st, ev.Seq, events))
			// A park or a handback is said by the rule itself; printing the
			// event too would say one ending twice (the TUI's live block).
			closedHere = ev.Kind == state.EventPark || ev.Kind == state.EventAutopilot
		}
		if ev.Kind == state.EventConsult {
			// a consult turn goes where it was asked, whatever stage the
			// card was in and whether or not any stage had started yet
			emit(consultItem(ev))
			continue
		}
		k, inSeg := segOf[i]
		if !inSeg || closedHere {
			continue
		}
		seg := segs[k]
		switch ev.Kind {
		case state.EventTool:
			var p state.ToolPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			// A verify that had no gummi-checks to run is a verify item
			// with no checks and the reason as its text — never a group
			// of zero failures, which a page reads as "all passed".
			if strings.HasPrefix(p.Label, engine.NoChecksRow+" — ") {
				it := Item{Seq: ev.Seq, Key: "verify:" + seqKey(ev.Seq), T: ItemVerify, At: ev.At, Stage: ev.Stage, Text: Sanitize(p.Label)}
				if seg.Exited {
					it.Verdict, it.Exited = seg.Verdict, true
					it.Seq = max(it.Seq, events[seg.ExitIdx].Seq)
				}
				emit(it)
				group = -1
				continue
			}
			if name, status, ok := checkRow(p.Label); ok {
				c := Check{Name: name, Status: status, OK: ev.Status == state.StatusOK, MS: p.MS}
				if !c.OK {
					c.Output = Sanitize(ev.Output)
				}
				if group >= 0 && out[group].T == ItemVerify {
					out[group].Checks = append(out[group].Checks, c)
					out[group].Seq = max(out[group].Seq, ev.Seq)
					continue
				}
				it := Item{Seq: ev.Seq, Key: "verify:" + seqKey(ev.Seq), T: ItemVerify, At: ev.At, Stage: ev.Stage, Checks: []Check{c}}
				if seg.Exited {
					it.Verdict, it.Exited = seg.Verdict, true
					it.Seq = max(it.Seq, events[seg.ExitIdx].Seq)
				}
				emit(it)
				group = len(out) - 1
				continue
			}
			call := toolCall(p, ev)
			if group >= 0 && out[group].T == ItemTools {
				out[group].Tools = append(out[group].Tools, call)
				out[group].Seq = max(out[group].Seq, ev.Seq)
				continue
			}
			emit(Item{Seq: ev.Seq, Key: "tools:" + seqKey(ev.Seq), T: ItemTools, At: ev.At, Stage: ev.Stage, Tools: []ToolCall{call}})
			group = len(out) - 1
		case state.EventMessage:
			var p MessagePayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			it := Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), At: ev.At, Stage: ev.Stage,
				Author: AuthorLabel(p.Author, seg.Role), Text: Sanitize(p.Content)}
			if p.Author == string(engine.AuthorUser) {
				it.T, it.Via, it.By = ItemYou, ViaSteered, state.PersonName(p.By)
				it.Attachments = attachmentRefs(p.Images)
				if echoes[k][p.Content] {
					it.Via = ViaAnswer
				}
			} else {
				it.T = ItemMessage
			}
			emit(it)
		case state.EventDecisionOpen:
			var p state.DecisionPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			if answered[p.ID] {
				continue // collapsed into the gate or ask row that answers it
			}
			if i == current {
				emit(Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), T: ItemDecision, At: ev.At, Stage: ev.Stage,
					Decision: &Decision{ID: p.ID, Kind: p.Kind, Question: Sanitize(p.Question)}})
				continue
			}
			emit(receiptItem(ev, Receipt{Kind: "decision", Text: SupersededLine(p)}))
		default:
			if it, ok := eventItem(ev, InStretch(stretches, i)); ok {
				emit(it)
			}
		}
	}
	// A period nothing in the log closed, closed instead by the judgement
	// that its driver is gone: its rule goes after everything, dated from
	// the last thing the run wrote.
	if n := len(events); n > 0 {
		for _, st := range trailing {
			emit(closeItem(st, events[n-1].Seq, events))
		}
	}
	return out
}

// Since returns the items a client that has seen everything up to seq
// after still needs: every item whose Seq is newer, including one it
// already drew that has grown since (upsert it by Key).
func Since(items []Item, after int64) []Item {
	var out []Item
	for _, it := range items {
		if it.Seq > after {
			out = append(out, it)
		}
	}
	return out
}

// eventItem is the item for an event whose whole fact is one receipt or
// note: gates, asks, parks, mode changes, goal log entries, and whatever
// kind this fold has no sentence for. false for an event the thread
// deliberately draws nothing for (an autopilot boundary, drawn as a
// stretch rule instead).
func eventItem(ev state.CardEvent, inStretch bool) (Item, bool) {
	switch ev.Kind {
	case state.EventGate:
		var p state.GatePayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if line := DecisionLine(ev, inStretch); line != "" {
			by := p.Actor
			if inStretch {
				by = "autopilot"
			}
			return receiptItem(ev, Receipt{Kind: "gate", OK: true, Text: Sanitize(line), By: by}), true
		}
		return receiptItem(ev, Receipt{Kind: "gate", OK: true, Text: GateLine(p), By: GateCrosser(p)}), true
	case state.EventAsk:
		var p state.AskPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if line := DecisionLine(ev, inStretch); line != "" {
			return receiptItem(ev, Receipt{Kind: "ask", OK: true, Text: Sanitize(line), By: "autopilot"}), true
		}
		return receiptItem(ev, Receipt{Kind: "ask", OK: true, Text: AskLine(p), By: AskAnswerer(p)}), true
	case state.EventPark:
		var p state.ParkPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return receiptItem(ev, Receipt{Kind: "park", Text: ParkLine(p)}), true
	case state.EventAutopilot:
		var p state.AutopilotPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if p.Event != "" {
			return Item{}, false // a boundary: the stretch rule says it
		}
		return receiptItem(ev, Receipt{Kind: "autopilot", OK: true, Text: ModeLine(p)}), true
	case state.EventGoal:
		var p state.GoalPayload
		text := ev.Kind
		if err := json.Unmarshal([]byte(ev.Payload), &p); err == nil && p.Action != "" {
			text = GoalSentence(p)
		}
		return Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), T: ItemNote, At: ev.At, Stage: ev.Stage, Text: text, Outcome: ev.Status}, true
	default:
		return Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), T: ItemNote, At: ev.At, Stage: ev.Stage, Text: ev.Kind}, true
	}
}

// attachmentRefs converts a message payload's stored attachment refs to
// the fold's own shape.
func attachmentRefs(refs []state.AttachmentRef) []engine.AttachmentRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]engine.AttachmentRef, len(refs))
	for i, r := range refs {
		out[i] = engine.AttachmentRef{ID: r.ID, Name: r.Name, MediaType: r.MediaType, Size: r.Size}
	}
	return out
}

// consultItem is one consult turn: the person's question, or the consult
// agent's answer.
func consultItem(ev state.CardEvent) Item {
	var p MessagePayload
	_ = json.Unmarshal([]byte(ev.Payload), &p)
	it := Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), At: ev.At, Stage: ev.Stage,
		Text: Sanitize(p.Content), Via: ViaConsult}
	if p.Author == string(engine.AuthorUser) {
		it.T, it.Author, it.By = ItemYou, AuthorLabel(p.Author, ""), state.PersonName(p.By)
		it.Attachments = attachmentRefs(p.Images)
		return it
	}
	it.T, it.Author = ItemMessage, string(agent.RoleConsult)
	return it
}

func receiptItem(ev state.CardEvent, r Receipt) Item {
	return Item{Seq: ev.Seq, Key: "ev:" + seqKey(ev.Seq), T: ItemReceipt, At: ev.At, Stage: ev.Stage, Receipt: &r}
}

// stageItem is a segment's divider. Its Seq is its stage_exit's once the
// segment has one, so a divider delivered while the stage ran is
// delivered again when it settles.
func stageItem(seg Segment, credits float64, enter state.CardEvent, events []state.CardEvent) Item {
	it := Item{
		Seq: enter.Seq, Key: "stage:" + seqKey(enter.Seq), T: ItemStage, At: seg.EnterAt, Stage: seg.Stage,
		Role: seg.Role, Model: seg.Model, Flavor: seg.Flavor,
		Exited: seg.Exited, Verdict: seg.Verdict, Credits: roundCredits(credits), Outcome: seg.Outcome(),
	}
	if seg.Exited {
		it.Seq = max(it.Seq, events[seg.ExitIdx].Seq)
	}
	return it
}

func closeItem(st Stretch, seq int64, events []state.CardEvent) Item {
	return Item{
		Seq: seq, Key: "close:" + seqKey(events[st.From].Seq), T: ItemStretch, At: st.ClosedAt,
		Stretch: &StretchMark{
			Edge: "close", Label: StretchLabel(st.Closed), How: st.Closed,
			Reason: Sanitize(st.Reason), Tally: st.Tally(), Mode: st.Mode,
		},
	}
}

// segmentCredits is what each segment's divider says it cost, resolved
// the way the TUI's folded receipts resolve it (ReceiptCredits) over the
// segments the TUI folds: every one but a still-open last, whose spend is
// still running and is the live session's to report.
func segmentCredits(segs []Segment, rows []state.StageSpend) []float64 {
	out := make([]float64, len(segs))
	folded := segs
	if n := len(segs); n > 0 && !segs[n-1].Exited {
		folded = segs[:n-1]
		out[n-1] = segs[n-1].Credits
	}
	spend := SpendByStage(rows)
	counts := map[domain.Stage]int{}
	for _, seg := range folded {
		counts[seg.Stage]++
	}
	unclaimed, unknown := Unclaimed(spend, folded)
	for k, seg := range folded {
		out[k] = ReceiptCredits(seg, spend, counts[seg.Stage], Remainder(seg, unclaimed, unknown))
	}
	return out
}

// roundCredits rounds to the tenth the thread prints credits at.
func roundCredits(c float64) float64 { return float64(int(c*10+0.5)) / 10 }

// currentDecision is the index of the decision_open the card is still
// waiting on, as far as its log can say: the newest one nothing has
// answered, with no later decision raised and no later stage entered.
// Any other unanswered decision was superseded. -1 when there is none.
func currentDecision(events []state.CardEvent, answered map[string]bool) int {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case state.EventStageEnter:
			return -1
		case state.EventDecisionOpen:
			var p state.DecisionPayload
			_ = json.Unmarshal([]byte(events[i].Payload), &p)
			if answered[p.ID] {
				return -1
			}
			return i
		}
	}
	return -1
}

// askEchoes is, per segment, the answers a person gave to its asks. The
// session records such an answer as a turn of the person's own, so a
// turn whose text is one of them is that answer, not a steer.
func askEchoes(events []state.CardEvent, segOf map[int]int) map[int]map[string]bool {
	out := map[int]map[string]bool{}
	for i, ev := range events {
		if ev.Kind != state.EventAsk {
			continue
		}
		k, ok := segOf[i]
		if !ok {
			continue
		}
		var p state.AskPayload
		if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.Answer == "" || AskedBy(p) == state.ActorAutopilot {
			continue
		}
		if out[k] == nil {
			out[k] = map[string]bool{}
		}
		out[k][p.Answer] = true
	}
	return out
}

// checkRow parses gummi's own check result row, "check <name>: <status>"
// (the engine's verify step writes one per check).
func checkRow(label string) (name, status string, ok bool) {
	rest, found := strings.CutPrefix(label, "check ")
	if !found {
		return "", "", false
	}
	name, status, found = strings.Cut(rest, ":")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(name), strings.TrimSpace(status), true
}

// toolCall is one tool row as a ToolCall. A row written before Tool and
// Detail existed carries only its rendered Label ("Bash  make test"), and
// is taken apart at the double space the label was joined with.
func toolCall(p state.ToolPayload, ev state.CardEvent) ToolCall {
	label := Sanitize(p.Label)
	tool, detail := Sanitize(p.Tool), Sanitize(p.Detail)
	if tool == "" {
		t, d, _ := strings.Cut(label, "  ")
		tool = strings.TrimSpace(t)
		if detail == "" {
			detail = strings.TrimSpace(d)
		}
	}
	c := ToolCall{Tool: tool, Label: label, Detail: detail, Status: ev.Status, MS: p.MS}
	if ev.Status == state.StatusFail {
		c.Output = Sanitize(ev.Output)
	} else if ev.Status == "" && WatchTool(tool) {
		c.Status = StatusWatching
	}
	return c
}

func seqKey(seq int64) string { return strconv.FormatInt(seq, 10) }
