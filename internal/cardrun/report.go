package cardrun

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// Input is everything Report needs, gathered by whoever has a store.
// Passing it rather than a handle is what keeps this package pure: the
// TUI reads these lazily off a board row, `status` reads them from a
// cold database, and both get the same answer.
type Input struct {
	Feature domain.Feature
	// Events is the card's whole log, oldest first. Report folds tool
	// results onto their calls itself, so a caller may pass the log
	// exactly as the store returned it.
	Events []state.CardEvent
	// Spend is the session-grained rollup (state.Store.SessionBreakdown).
	// It, not the stage_exit payloads, is what a session's cost is read
	// from: it is written per usage sample, so it is complete even for a
	// pass that was interrupted, exhausted, or is still running.
	Spend    []state.StageSpend
	Rounds   map[domain.RoundKind]int
	Baseline []state.CheckResult
}

// humanActors are the ways a person answers a checkpoint themselves:
// "user" is the TUI, "caller" is the headless driver's attended mode.
//
// It is written this way round on purpose, and the reasoning is
// receipt.go's: the machine actors are open-ended — "auto" is the
// unattended loop, "review" is the automatic review→fix chain,
// "autopilot" is the switch, and any future loop names itself — so
// enumerating those is how a reader silently starts miscounting the day
// one is added. The set of ways a human can reach a gate is bounded, and
// that is the smaller, more stable thing to name.
var humanActors = map[string]bool{
	"user":   true,
	"caller": true,
}

// Report derives everything one card's record can say about how it ran.
//
// It is a pure function: no store, no context, no clock. Every duration
// comes from the timestamps in the log, so a card is reported the same
// way a second after it finishes and a month later.
func Report(in Input) Run {
	evs := state.FoldToolResults(in.Events)
	run := Run{
		ID:    in.Feature.ID,
		Title: in.Feature.Title,
		Kind:  in.Feature.Kind,
		Stage: in.Feature.Stage,
		Envelope: Envelope{
			Granted: in.Feature.Budget.Envelope,
			Spent:   in.Feature.Spend.Credits,
		},
	}
	var claimed []bool
	run.Sessions, claimed = sessions(evs, in.Spend)
	run.Money = money(run.Sessions, in.Spend, claimed, in.Feature)
	run.Clock = clock(run.Sessions, evs, in.Feature)
	run.Hands = hands(evs, in.Baseline)
	run.Judgment = judgment(evs, in.Rounds)
	return run
}

// enterPayload is a stage_enter event's payload: who ran this pass.
type enterPayload struct {
	Role   string `json:"role"`
	Model  string `json:"model"`
	Flavor string `json:"flavor"`
}

// exitPayload is a stage_exit event's payload.
//
// CtxPeak/CtxLimit are the one fact nothing else kept: how close the pass
// came to its context window, stamped here because the session row
// holding the live figure is deleted the moment the stage ends.
//
// Credits is read only as a fallback for history. A session's cost
// properly comes from the session-keyed rollup, which is written per
// usage sample and is therefore complete where this payload is not — a
// pass that was interrupted or is still running leaves no stage_exit at
// all. But a card that ran before the rollup carried session keys has
// this payload and nothing else at that grain, and reconstructing its
// passes from it is strictly better than reporting them as free.
type exitPayload struct {
	Verdict  string  `json:"verdict"`
	Credits  float64 `json:"credits"`
	CtxPeak  int64   `json:"ctx_peak"`
	CtxLimit int64   `json:"ctx_limit"`
}

// sessions walks the log's stage boundaries into one entry per pass, then
// marks the passes that were the card doing something over again.
//
// Not every pass closes. A session that was interrupted, or that ended
// without reaching the state the mirror writes a stage_exit from, leaves
// an enter with no exit — the same incompleteness that makes the
// stage_exit payload an unreliable place to read a pass's cost from.
// Such a pass is not dropped: two different things leave that shape, and
// only one of them is a gap.
//
//   - A pass superseded by a later stage_enter is over, whatever the log
//     failed to say: a card holds its own exclusive lock for as long as
//     anything drives it, so two stage sessions never run on one card at
//     once. It is closed at the moment the next one began, and marked
//     EndInferred so nothing reads that boundary as recorded.
//   - The last pass, with nothing after it, is genuinely still open —
//     which on a live card is the pass someone is asking about, and the
//     one it would be worst to drop.
func sessions(evs []state.CardEvent, spend []state.StageSpend) ([]Session, []bool) {
	var out []Session
	open := -1
	for _, ev := range evs {
		switch ev.Kind {
		case state.EventStageEnter:
			if open >= 0 {
				out[open].Ended = ev.At
				out[open].Closed = true
				out[open].EndInferred = true
			}
			var p enterPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			open = len(out)
			out = append(out, Session{
				Stage: ev.Stage, Role: p.Role, Flavor: p.Flavor, Model: p.Model,
				Started: ev.At,
			})
		case state.EventStageExit:
			if open < 0 {
				continue
			}
			var p exitPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			out[open].Ended = ev.At
			out[open].Closed = true
			out[open].Verdict = p.Verdict
			out[open].ContextPeak = p.CtxPeak
			out[open].ContextLimit = p.CtxLimit
			out[open].exitCredits = p.Credits
			open = -1
		case state.EventMessage:
			if open >= 0 {
				out[open].Turns++
			}
		case state.EventTool:
			if open < 0 {
				continue
			}
			out[open].Tools++
			if ev.Status == state.StatusFail {
				out[open].ToolFails++
			}
		}
	}
	claimed := attachSpend(out, spend)
	markRedone(out)
	return out, claimed
}

// attachSpend files each rollup row on the pass that spent it, and
// returns which rows it filed — the rest are the card's spend outside
// any pass (money's Charges).
//
// The rollup's session key is the generation the mirror stamped on that
// pass's events — the pass's own start, in Unix nanoseconds, which is
// also the moment its stage_enter records (engine's Session.generation).
// So a pass claims the rows filed under its own start first, exactly.
// Only then are any keys still unclaimed matched to the passes still
// without one, by their order within a (stage, role): the order both
// were written in, which is how a record whose times do not line up
// with its keys is still read. The order is never tried first, because
// it is wrong the moment a pass spent nothing: a pass cut off before its
// first sample leaves no row, and matching by order handed its
// successor's row to it and left the successor to be reconstructed
// from its own stage_exit — the same credits counted twice.
//
// Rows with no session key at all (written before the key existed, or
// by something that is not a stage session — a one-shot, a goal's lead)
// belong to no pass, and neither does a row filed under a pass's key by
// another role (a backend's helper call): each is left for the card's
// totals to pick up rather than guessed onto one.
func attachSpend(sess []Session, spend []state.StageSpend) []bool {
	claimed := make([]bool, len(spend))
	if len(sess) == 0 {
		return claimed
	}
	byKey := map[string][]int{}
	for i, r := range spend {
		if r.Session != "" {
			byKey[r.Session] = append(byKey[r.Session], i)
		}
	}
	taken := map[string]bool{}
	file := func(p *Session, key string) {
		taken[key] = true
		p.Key = key
		for _, i := range byKey[key] {
			r := spend[i]
			if r.Role != p.Role || r.Stage != p.Stage {
				continue
			}
			claimed[i] = true
			p.Credits += r.Credits
			p.Estimated += r.EstimatedCredits
			p.InputTokens += r.InputTokens
			p.CachedTokens += r.CachedTokens
			p.OutputTokens += r.OutputTokens
			if p.Model == "" {
				p.Model = r.Model
			}
		}
	}
	exact := make([]bool, len(sess))
	for i := range sess {
		if sess[i].Started.IsZero() {
			continue
		}
		key := strconv.FormatInt(sess[i].Started.UnixNano(), 10)
		if len(byKey[key]) > 0 {
			file(&sess[i], key)
			exact[i] = true
		}
	}

	// The fallback: every distinct key no pass claimed by its start, per
	// (stage, role), in the order the rollup first wrote them.
	type slot struct{ stage, role string }
	keys := map[slot][]string{}
	seen := map[string]bool{}
	ordered := make([]int, 0, len(spend))
	for i := range spend {
		ordered = append(ordered, i)
	}
	sort.SliceStable(ordered, func(a, b int) bool {
		return spend[ordered[a]].UpdatedAt.Before(spend[ordered[b]].UpdatedAt)
	})
	for _, i := range ordered {
		r := spend[i]
		if r.Session == "" || taken[r.Session] {
			continue
		}
		s := slot{string(r.Stage), r.Role}
		id := s.stage + "\x00" + s.role + "\x00" + r.Session
		if !seen[id] {
			seen[id] = true
			keys[s] = append(keys[s], r.Session)
		}
	}
	used := map[slot]int{}
	for i := range sess {
		if exact[i] {
			continue
		}
		s := slot{string(sess[i].Stage), sess[i].Role}
		n := used[s]
		if n >= len(keys[s]) {
			// No keyed row for this pass. On a card recorded before the
			// rollup carried session keys that is every pass, and the
			// stage_exit payload is the only per-pass figure that exists —
			// so it is used, and the pass says so (Reconstructed) rather
			// than presenting a reconstruction as a measurement.
			if sess[i].exitCredits > 0 {
				sess[i].Credits = sess[i].exitCredits
				sess[i].Reconstructed = true
			}
			continue
		}
		used[s] = n + 1
		file(&sess[i], keys[s][n])
	}
	return claimed
}

// markRedone flags every pass the card had already done once.
//
// The identity of a piece of work is (stage, role, flavour): a second
// implement by the implementer is the implementation again, and a second
// critique of it is the critique again. A rebase pass is its own flavour,
// so it is never itself a redo — but a stage that rebased and then
// re-proved its work is repeating that work for a different reason than a
// stage sent back by a verdict, and the two are labelled apart.
func markRedone(sess []Session) {
	type work struct{ stage, role, flavor string }
	seen := map[work]bool{}
	rebased := map[string]bool{} // stages that have had a rebase pass
	for i := range sess {
		w := work{string(sess[i].Stage), sess[i].Role, sess[i].Flavor}
		if seen[w] {
			sess[i].Redo = true
			sess[i].RedoReason = Corrected
			if rebased[w.stage] {
				sess[i].RedoReason = Reproved
			}
		}
		seen[w] = true
		if sess[i].Flavor == "rebase" {
			rebased[w.stage] = true
		}
	}
}

// money splits the card's realized spend by where it went and by whether
// the card had been there before.
//
// The total is the feature's own counter — the figure the board prints
// on the card and the envelope is drawn against — and every split sums
// back to it. The passes take what they spent; whatever of the counter
// no pass accounts for is itemized as Charges (see Charge) and totalled
// as Elsewhere, so FirstPass + Rework + Elsewhere is Credits on every
// card whose two meters agree.
func money(sess []Session, spend []state.StageSpend, claimed []bool, f domain.Feature) Money {
	m := Money{
		Credits:   f.Spend.Credits,
		Estimated: f.Spend.EstimatedCredits,
	}
	byStage, byRole, byModel := map[string]float64{}, map[string]float64{}, map[string]float64{}
	var rowsTotal float64
	for _, r := range spend {
		byStage[string(r.Stage)] += r.Credits
		byRole[r.Role] += r.Credits
		byModel[r.Model] += r.Credits
		rowsTotal += r.Credits
		m.InputTokens += r.InputTokens
		m.CachedTokens += r.CachedTokens
		m.OutputTokens += r.OutputTokens
	}
	// What a reconstructed pass took off its stage_exit is already in an
	// unkeyed row of the same (stage, role): that is the row the rollup
	// wrote for it before it carried keys. It is netted out of that row
	// rather than counted again beside the pass.
	type slot struct{ stage, role string }
	recon := map[slot]float64{}
	for _, s := range sess {
		if s.Reconstructed {
			recon[slot{string(s.Stage), s.Role}] += s.Credits
		}
		switch {
		case !s.Redo:
			m.FirstPass += s.Credits
		case s.RedoReason == Reproved:
			m.Rework += s.Credits
			m.Reproved += s.Credits
		default:
			m.Rework += s.Credits
			m.Corrected += s.Credits
		}
	}
	for i, r := range spend {
		if claimed[i] {
			continue
		}
		c := chargeOf(r)
		netted := false
		if k := (slot{string(r.Stage), r.Role}); r.Session == "" && recon[k] > 0 {
			net := min(recon[k], c.Credits)
			recon[k] -= net
			c.Credits -= net
			c.Estimated = min(c.Estimated, c.Credits)
			netted = true
		}
		if c.Credits <= crumb && (netted || c.tokens() == 0) {
			continue
		}
		m.Charges = append(m.Charges, c)
	}
	for _, c := range Unrecorded(f, rowsTotal) {
		m.Charges = append(m.Charges, c)
		byStage[c.Bucket()] += c.Credits
		byRole[c.Role] += c.Credits
		byModel[unknownModel] += c.Credits
	}
	elsewhere := map[string]float64{}
	for _, c := range m.Charges {
		m.Elsewhere += c.Credits
		if c.Credits > 0 {
			elsewhere[c.Role] += c.Credits
		}
	}
	m.ElsewhereBy = buckets(elsewhere)
	m.ByStage = buckets(byStage)
	m.ByRole = buckets(byRole)
	m.ByModel = buckets(byModel)
	return m
}

// crumb is the smallest share of a credit a split reports. The counter
// and the rollup are summed in floating point from the same samples, and
// a difference below this is their rounding, not spend nobody recorded.
const crumb = 0.005

// unknownModel is the rollup's own name for a sample that named no model
// (state.Store.RecordStageSpend), used for spend that has no row at all.
const unknownModel = "unknown"

// chargeOf is a rollup row as a Charge, stamped with its last sample.
func chargeOf(r state.StageSpend) Charge {
	return Charge{
		Stage: r.Stage, Role: r.Role, Model: r.Model, At: r.UpdatedAt,
		Credits: r.Credits, Estimated: r.EstimatedCredits,
		InputTokens: r.InputTokens, CachedTokens: r.CachedTokens, OutputTokens: r.OutputTokens,
	}
}

// Unrecorded is the part of a card's counter that no rollup row records:
// the counter less rowsTotal, the sum of the card's stage_spend rows.
// The two are written beside each other for every stage session, lead
// turn and one-shot, so on most cards this is nothing. What it is when
// it is not: a decomposition pass at ingest, which books the counter
// alone (state.Store.AddDecomposeSpend), and spend older than the
// rollup. Both are charged to the card's creation — the moment a
// decomposition runs, and the only one the record holds for the other.
//
// It is exported because the all-time ledger folds the same counters
// and rows at workspace scale, and a second derivation of what is
// missing from the rows is how the two scales would start to disagree.
func Unrecorded(f domain.Feature, rowsTotal float64) []Charge {
	rest := f.Spend.Credits - rowsTotal
	if rest <= crumb {
		return nil
	}
	var out []Charge
	if d := min(rest, f.Spend.DecomposeCredits); d > crumb {
		out = append(out, Charge{Role: "decompose", Model: unknownModel, At: f.CreatedAt, Credits: d})
		rest -= d
	}
	if rest > crumb {
		out = append(out, Charge{Role: "unrecorded", Model: unknownModel, At: f.CreatedAt, Credits: rest})
	}
	return out
}

// buckets turns a name→credits map into the largest-first slice every
// display wants, ties broken by name so the order never wobbles.
func buckets(m map[string]float64) []Bucket {
	if len(m) == 0 {
		return nil
	}
	out := make([]Bucket, 0, len(m))
	for k, v := range m {
		out = append(out, Bucket{Name: k, Credits: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Credits != out[j].Credits {
			return out[i].Credits > out[j].Credits
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// clock measures the card's two times — the one an agent was working and
// the one the card merely existed — and the distance between them.
func clock(sess []Session, evs []state.CardEvent, f domain.Feature) Clock {
	var c Clock
	var first, last time.Time
	asks := AskSpans(evs)
	for _, s := range sess {
		if s.Started.IsZero() {
			continue
		}
		if first.IsZero() || s.Started.Before(first) {
			first = s.Started
		}
		if s.Closed {
			// a pass blocked on its own question was not working
			// through it: that stretch is the reader's (AskSpans)
			c.Agent += WorkingTime(s.Started, s.Ended, asks)
			if s.Ended.After(last) {
				last = s.Ended
			}
		}
	}
	if !first.IsZero() && last.After(first) {
		c.Elapsed = last.Sub(first)
	}
	// Waiting is what is left over, and it is floored at zero rather than
	// allowed to go negative: sessions can overlap (a consult alongside a
	// stage), and a card that ran two things at once did not thereby
	// spend negative time waiting.
	if c.Waiting = c.Elapsed - c.Agent; c.Waiting < 0 {
		c.Waiting = 0
	}
	if first.IsZero() {
		return c
	}
	// OnYou is the part of that a person was actually asked for, and Idle
	// the rest. The record of the asking is the decision log, so the
	// split is read from it rather than assumed: whatever is left once
	// the open decisions are accounted for is time nothing ran and
	// nobody had been asked.
	c.OnYou = openDecisionTime(evs, first, last)
	if c.OnYou > c.Waiting {
		// A decision can be open while a session runs — a gate
		// pre-opened as a crossing is attempted, say. That time is the
		// agent's, not the reader's, and the residual is the ceiling on
		// what can be charged to a person. An ask is the exception, and
		// it is already out of Agent above: a session blocked on its own
		// question is not working, so its wait lands in the residual.
		c.OnYou = c.Waiting
	}
	c.Idle = c.Waiting - c.OnYou
	for _, ev := range evs {
		if ev.Kind == state.EventGate && c.ToFirstGate == 0 && ev.At.After(first) {
			c.ToFirstGate = ev.At.Sub(first)
		}
	}
	// Verified is the stamp the card carries, not an event: it is set
	// when verify passes and survives everything after.
	if v := f.VerifiedAt; !v.IsZero() && v.After(first) {
		c.ToVerified = v.Sub(first)
	}
	return c
}

// openDecisionTime is how long the card stood at a decision nobody had
// answered yet, over the card's own life — WaitTime at this horizon.
// One still open at the end of the card's life runs to last: the card
// really was waiting then, and the alternative is to report the longest
// wait on the record as no wait at all.
func openDecisionTime(evs []state.CardEvent, first, last time.Time) time.Duration {
	return WaitTime(evs, first, last)
}

// correlatingID is the decision id a gate or ask event answers, or "" on
// an event that answers none. The two payload shapes carry the id under
// the same key, so one decode serves both.
func correlatingID(ev state.CardEvent) string {
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(ev.Payload), &p) != nil {
		return ""
	}
	return p.ID
}

// hands tallies what the card did: its turns, its tool calls by name, the
// two kinds of delegation worth naming on their own, and gummi's own
// checks.
//
// Tools is left nil when the log holds no agent tool call at all. That is
// not the same as a card that called nothing: three of gummi's six
// backends report no tool outcomes, and until a call is recorded at call
// time they recorded nothing whatsoever. A nil says "this record has no
// tool calls in it"; a reader must not print that as a zero.
func hands(evs []state.CardEvent, baseline []state.CheckResult) Hands {
	var h Hands
	excused := map[string]bool{}
	for _, name := range state.ExcusedChecks(baseline) {
		excused[name] = true
	}
	tools := map[string]*ToolUse{}
	var order []string
	checks := map[string]*CheckRun{}
	var checkOrder []string

	for _, ev := range evs {
		switch ev.Kind {
		case state.EventMessage:
			h.Turns++
		case state.EventTool:
			var p state.ToolPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			// gummi's own verification runs carry no tool name and
			// announce themselves in the label; they are a different kind
			// of thing from an agent reaching for a tool, and counting
			// them together would flatter both.
			if name, ok := checkName(p, ev); ok {
				c := checks[name]
				if c == nil {
					c = &CheckRun{Name: name, Excused: excused[name]}
					checks[name] = c
					checkOrder = append(checkOrder, name)
				}
				c.Runs++
				if ev.Status == state.StatusFail {
					c.Fails++
				}
				continue
			}
			if p.Tool == "" {
				continue // an activity note, not a call
			}
			t := tools[p.Tool]
			if t == nil {
				t = &ToolUse{Name: p.Tool}
				tools[p.Tool] = t
				order = append(order, p.Tool)
			}
			t.Calls++
			h.ToolCalls++
			if ev.Status == state.StatusFail {
				t.Fails++
				h.ToolFails++
			}
			if p.Detail != "" {
				t.Detail = p.Detail
			}
			t.Total += time.Duration(p.MS) * time.Millisecond
		}
	}

	for _, name := range order {
		h.Tools = append(h.Tools, *tools[name])
	}
	sort.SliceStable(h.Tools, func(i, j int) bool { return h.Tools[i].Calls > h.Tools[j].Calls })
	for _, name := range checkOrder {
		h.Checks = append(h.Checks, *checks[name])
	}
	h.Skills = pick(h.Tools, isSkill)
	h.Subagents = pick(h.Tools, isSubagent)
	return h
}

// checkName recovers a gummi check's name from its event, or reports
// that the event is not one. Checks announce themselves as
// "check <name>: <outcome>" and carry no tool name, so the two tests
// together are what tells gummi's own run apart from an agent's call to a
// tool that happens to be called "check".
func checkName(p state.ToolPayload, ev state.CardEvent) (string, bool) {
	if p.Tool != "" || !strings.HasPrefix(p.Label, "check ") {
		return "", false
	}
	name, _, ok := strings.Cut(strings.TrimPrefix(p.Label, "check "), ":")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// isSkill and isSubagent name the two kinds of delegation. Both are
// matched on the tool's name as the backend gave it, case-insensitively,
// because every backend spells its own.
func isSkill(t ToolUse) bool { return strings.EqualFold(t.Name, "skill") }

// isSubagent matches the delegation tools by exact name — Claude Code's
// task/agent, agy's invoke_subagent — never by substring: an MCP tool
// that merely talks about subagents (list_subagents) delegated nothing.
func isSubagent(t ToolUse) bool {
	switch strings.ToLower(t.Name) {
	case "task", "agent", "subagent", "invoke_subagent":
		return true
	}
	return false
}

func pick(all []ToolUse, want func(ToolUse) bool) []ToolUse {
	var out []ToolUse
	for _, t := range all {
		if want(t) {
			out = append(out, t)
		}
	}
	return out
}

// judgment tallies the card's checkpoints by who answered them, its
// verdicts, its round counters and every time it stopped.
func judgment(evs []state.CardEvent, rnds map[domain.RoundKind]int) Judgment {
	j := Judgment{Verdicts: map[string]int{}, Rounds: map[domain.RoundKind]int{}}
	for k, v := range rnds {
		j.Rounds[k] = v
	}
	for _, ev := range evs {
		switch ev.Kind {
		case state.EventGate:
			var p state.GatePayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			count(&j.Gates, firstNonEmpty(p.By, p.Actor))
		case state.EventAsk:
			var p state.AskPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			count(&j.Asks, firstNonEmpty(p.By, p.Actor))
		case state.EventStageExit:
			var p exitPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			if p.Verdict != "" {
				j.Verdicts[p.Verdict]++
			}
		case state.EventPark:
			var p state.ParkPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			// A park recorded because the board process quit is not the
			// card stopping to wait for anyone; it is gummi going away.
			if p.Reason == state.ParkReasonQuit {
				continue
			}
			j.Parks = append(j.Parks, Park{Reason: p.Reason, Detail: p.Detail, At: ev.At})
		}
	}
	return j
}

func count(a *Answered, actor string) {
	a.Total++
	if humanActors[actor] || state.IsPersonActor(actor) {
		a.ByYou++
		return
	}
	a.ByMachine++
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
