package ui

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/webapi"
)

// This file keeps the board's in-memory rows, and what a web page was
// last told about them, from drifting behind the store and the
// repository.
//
// The rows are a snapshot (loadRows), reloaded on a handful of engine
// events. That left three kinds of write nobody reloaded for:
//
//   - this process's own store writes made off the event path: the
//     verified stamp a board-driven verify pass makes, the spend a scribe
//     pass books, a goal's done-when results, a check baseline. The
//     foreign probe's data_version only moves for OTHER connections, so a
//     board could say "verify failed" over a store that said verified
//     until it restarted;
//   - two loads racing: a load started before a write can land after one
//     started after it, and put the old row back;
//   - what a page shows that no row holds: a card's busy word, its
//     session's state, its re-entry chip — each of which moved on a
//     message the change hook did not map, so an open page kept a spinner
//     for a run that had ended, or never saw the chip a slow read raised.
//
// And one the store never hears of at all: a commit made in a card's
// worktree, or its spec rewritten, by something other than this board.
// Those are watched for the cards a page has open (watchedRevs), cheaply:
// one rev-parse and one file read per open card per probe.

// freshness is the Shell's bookkeeping for the above. rowsSeq is touched
// from row-load commands; watch is shared with the probe command under
// its own lock; everything else belongs to the Update goroutine.
type freshness struct {
	// rowsSeq numbers row loads as they start; applied is the newest load
	// the rows came from.
	rowsSeq atomic.Uint64
	applied uint64

	// changes is the store's own-write counter (Store.Changes) as it
	// stood when the applied load began.
	changes int64

	// seen is each card's row as a page was last told about it, and
	// stacks the stack annotations: a reload that changes neither tells
	// no page anything.
	seen   map[domain.FeatureID]featureRow
	stacks map[domain.FeatureID]stackRow
	// live is each card's liveKey as last reported.
	live map[domain.FeatureID]string

	// watch is the cards a page has open, with when it last read them;
	// revs what the probe last found for each.
	watchMu sync.Mutex
	watch   map[domain.FeatureID]watched
	revs    map[domain.FeatureID]watchRev
}

// watched is one card a page has open.
type watched struct {
	f  domain.Feature
	at time.Time
}

// watchRev is what a watched card's documents stand at: its branch head
// and its artifact's revision. Empty for what the card does not have.
type watchRev struct {
	head string
	spec string
}

// Watch limits: a card read by a page within watchFor is watched, at most
// watchMax of them — the most recently read — so a board with every card
// open in some forgotten tab still costs a handful of reads per probe.
const (
	watchFor = 30 * time.Minute
	watchMax = 8
)

func newFreshness() *freshness { return &freshness{} }

// fresh returns the Shell's freshness state, making it on first use (a
// scaffold that builds a Shell by hand has none).
func (m *Shell) freshState() *freshness {
	if m.fresh == nil {
		m.fresh = newFreshness()
	}
	return m.fresh
}

// nextRowsSeq numbers a row load as it starts. Called from the load's own
// command, so it touches only the atomic counter; a Shell without the
// state (a hand-built scaffold) numbers nothing, and its loads are never
// judged stale.
func (m *Shell) nextRowsSeq() uint64 {
	if m.fresh == nil {
		return 0
	}
	return m.fresh.rowsSeq.Add(1)
}

// staleRows reports a row load older than the one the rows already came
// from, and records the load it accepts. Two loads can be out at once —
// one a turn started, one a write asked for — and whichever returns last
// used to win, so a load begun before a verified stamp could land after
// the one begun after it and put "verify failed" back.
func (m *Shell) staleRows(msg rowsMsg) bool {
	if msg.seq == 0 || m.fresh == nil {
		return false
	}
	if msg.seq < m.fresh.applied {
		return true
	}
	m.fresh.applied, m.fresh.changes = msg.seq, msg.changes
	return false
}

// emitRowChanges tells the pages what a row load moved: a card change for
// every card whose row differs from the last one reported, and one board
// change when anything did. A reload that found the board as it was says
// nothing — the probe reloads every couple of seconds while a run books
// spend, and a page refetching an unchanged board each time is noise.
func (m *Shell) emitRowChanges(msg rowsMsg) {
	if msg.err != nil || (msg.seq != 0 && m.fresh != nil && msg.seq != m.fresh.applied) {
		// a failed load moved nothing, and a stale one was dropped
		return
	}
	fs := m.freshState()
	moved := !reflect.DeepEqual(fs.stacks, msg.stacks)
	next := make(map[domain.FeatureID]featureRow, len(msg.rows))
	var cards []string
	for _, r := range msg.rows {
		next[r.F.ID] = r
		if old, ok := fs.seen[r.F.ID]; !ok || !reflect.DeepEqual(old, r) {
			cards = append(cards, string(r.F.ID))
		}
	}
	if len(next) != len(fs.seen) || len(cards) > 0 {
		moved = true
	}
	// a card that left the board is said to be gone rather than moved: a
	// page that has it open has nothing to refetch, and asking would only
	// be answered 404
	var gone []string
	for id := range fs.seen {
		if _, ok := next[id]; !ok {
			gone = append(gone, string(id))
		}
	}
	fs.seen, fs.stacks = next, msg.stacks
	if !moved {
		return
	}
	m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	for _, id := range cards {
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: id})
	}
	for _, id := range gone {
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: id, Gone: true})
	}
}

// liveKey is what a card's page shows that its row does not hold, reduced
// to a comparable string: whether it is busy and at what, its session's
// state, a pause asked for, a chip waiting on the reader. Cheap — no
// transcript copy — because it is taken for every card after every
// message a page could care about.
func (m *Shell) liveKey(r featureRow) string {
	id := r.F.ID
	b := make([]byte, 0, 48)
	flag := func(on bool) {
		if on {
			b = append(b, '1')
		} else {
			b = append(b, '0')
		}
	}
	flag(m.baselining[id])
	flag(m.reentryRead != nil && m.reentryRead.id == id)
	b = strconv.AppendInt(b, int64(m.scribing[id]), 10)
	flag(m.consultSending[id] != "")
	flag(m.pausing[id])
	flag(m.chips[id] != nil)
	if m.engine != nil {
		if ff := m.engine.Freeform(id); ff != nil {
			b = append(b, 'f')
			flag(ff.Busy())
		}
	}
	if c := m.consultFor(id); c != nil {
		// a consult answer lands in the log as it settles: the thread
		// has a new turn to draw, and the live block one fewer
		snap := c.Snapshot()
		b = append(b, 'c')
		flag(snap.Busy)
		b = strconv.AppendInt(b, int64(len(snap.Transcript)), 10)
	}
	if s := m.sessionFor(id); s != nil {
		b = append(b, '|')
		b = append(b, s.State()...)
		flag(s.Busy())
		flag(s.Critique)
		b = strconv.AppendUint(b, uint64(reflect.ValueOf(s).Pointer()), 36)
	}
	if r.DrivenAbroad {
		b = append(b, 'x')
		flag(r.Foreign.Busy)
	}
	return string(b)
}

// syncWebLive reports every card whose liveKey moved since it was last
// reported: its live block, its head (the decision a busy card withholds,
// a chip) and the board row's running word all read what moved. It runs
// after every message the Shell handles and every web request, so no
// message has to be on a list for its effect to reach a page — the gap
// that left a spinner on a page for a check that had finished, and a
// re-entry chip raised after its request returned on no page at all.
func (m *Shell) syncWebLive() {
	if m.changeHook == nil {
		return
	}
	fs := m.freshState()
	if fs.live == nil {
		fs.live = map[domain.FeatureID]string{}
	}
	board := false
	for _, r := range m.rows {
		k := m.liveKey(r)
		if was, ok := fs.live[r.F.ID]; ok && was == k {
			continue
		}
		fs.live[r.F.ID] = k
		board = true
		id := string(r.F.ID)
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: id})
		m.EmitChange(webapi.Change{Kind: webapi.ChangeLive, ID: id})
	}
	if board {
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	}
}

// toastDetached says on every open page what the status bar said while a
// detached intent's flow went on: the request that started it has
// answered already, so its outcome can no longer carry the sentence (a
// read that took longer than the request waited, ending in "can't go on
// yet — …"). A notice message reports itself (emitChanges); this is for
// the notice a handler set in passing. A verify result reports itself the
// same way (emitChanges), whoever ran it.
func (m *Shell) toastDetached(inner tea.Msg, before noticeMsg) {
	switch inner.(type) {
	case noticeMsg, verifyResultMsg:
		return
	}
	if m.notice == before || m.notice.text == "" {
		return
	}
	m.EmitChange(webapi.Change{Kind: webapi.ChangeToast, ID: string(m.notice.id), Text: m.notice.text, Err: m.notice.isErr})
}

// watchCard records that a page read card f, so the probe watches its
// branch head and artifact for writes the store never hears of.
func (m *Shell) watchCard(f domain.Feature) {
	fs := m.freshState()
	fs.watchMu.Lock()
	defer fs.watchMu.Unlock()
	if fs.watch == nil {
		fs.watch = map[domain.FeatureID]watched{}
	}
	fs.watch[f.ID] = watched{f: f, at: m.now()}
}

// watchedCards is the cards the probe reads this time: read by a page
// within watchFor, the newest watchMax of them. Safe off the loop.
func (m *Shell) watchedCards(now time.Time) []domain.Feature {
	if m.fresh == nil {
		return nil
	}
	fs := m.fresh
	fs.watchMu.Lock()
	defer fs.watchMu.Unlock()
	list := make([]watched, 0, len(fs.watch))
	for id, w := range fs.watch {
		if now.Sub(w.at) > watchFor {
			delete(fs.watch, id)
			continue
		}
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	if len(list) > watchMax {
		list = list[:watchMax]
	}
	out := make([]domain.Feature, len(list))
	for i, w := range list {
		out[i] = w.f
	}
	return out
}

// watchedRevs reads each watched card's branch head and artifact
// revision. Off the loop; it touches only the Shell's fixed wiring.
func (m *Shell) watchedRevs(ctx context.Context) map[domain.FeatureID]watchRev {
	cards := m.watchedCards(m.now())
	if len(cards) == 0 || m.wt == nil {
		return nil
	}
	out := make(map[domain.FeatureID]watchRev, len(cards))
	for i := range cards {
		f := &cards[i]
		var rev watchRev
		if f.Kind != domain.KindResearch {
			if head, err := m.wt.Head(ctx, f); err == nil {
				rev.head = head
			}
		}
		if path := m.artifactFile(f); path != "" {
			if b, err := os.ReadFile(path); err == nil {
				rev.spec = spec.Rev(b)
			}
		}
		out[f.ID] = rev
	}
	return out
}

// applyWatchedRevs compares what the probe found with what it found last
// time, and reports the cards whose branch or artifact moved: their page
// refetches the head, the thread and the open tab (a card change bumps
// the page's card revision, which is what reloads a spec or diff tab),
// and the rows reload, since a new commit can move what the row says
// (landed, the gate's blockers). A card seen for the first time is only
// recorded: there is nothing it moved from.
func (m *Shell) applyWatchedRevs(revs map[domain.FeatureID]watchRev) bool {
	if len(revs) == 0 {
		return false
	}
	fs := m.freshState()
	if fs.revs == nil {
		fs.revs = map[domain.FeatureID]watchRev{}
	}
	moved := false
	for id, rev := range revs {
		was, ok := fs.revs[id]
		fs.revs[id] = rev
		if !ok {
			// first sighting: nothing it moved from — unless the row read
			// a branch tip at load (a verified card's, featureRow.Head) that
			// is no longer the tip. A commit made before any page watched
			// the card would otherwise leave the verify stop reading
			// "verification passed" over a branch it no longer describes.
			if r, found := m.rowByID(id); !found || r.Head == "" || rev.head == "" || r.Head == rev.head {
				continue
			}
		} else if was == rev {
			continue
		}
		moved = true
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: string(id)})
	}
	return moved
}

// applyStoreChanges takes the probe's own-write counter and reports
// whether this process wrote to the store after the rows were read —
// which is a row reload, paced by the probe's interval however many
// writes a run makes in between. The rows carry the counter as it stood
// when their load began (rowsMsg.changes), so a write an event-driven
// reload already picked up asks for nothing more.
func (m *Shell) applyStoreChanges(changes int64) bool {
	if changes == 0 || m.fresh == nil || m.fresh.applied == 0 {
		// unreadable, or no load has landed yet: the first one reads
		// whatever the store holds
		return false
	}
	return changes > m.fresh.changes
}

// applyForeignMsg is the probe's result, applied: the driven-elsewhere
// badges, and a row reload when anything under the board moved — another
// process's commit, this process's own writes, a drive that started or
// ended, a watched card's branch or artifact.
func (m *Shell) applyForeignMsg(msg foreignMsg) tea.Cmd {
	changed := m.applyForeign(msg.drives)
	if moved := msg.storeVersion != 0 && m.storeVersion != 0 && msg.storeVersion != m.storeVersion; msg.storeVersion != 0 && (moved || m.storeVersion == 0) {
		m.storeVersion = msg.storeVersion
		if moved {
			// another process committed — a card minted from the CLI,
			// a run beside the board that stopped at a gate: read the
			// rows and the open decisions again, so neither face shows
			// the board as it stood before
			m.applyWatchedRevs(msg.revs)
			return tea.Batch(m.loadRows, m.refetchOpenDecisions)
		}
	}
	own := m.applyStoreChanges(msg.changes)
	revs := m.applyWatchedRevs(msg.revs)
	if changed || msg.reload || own || revs {
		// a drive that just started or ended moved the store too, and a
		// long-running one keeps moving it; this process's own writes
		// (a verified stamp, a scribe's spend, a goal's done-when results)
		// move it without any event saying so; reload so the badges are
		// not the only thing on the board that is current.
		return m.loadRows
	}
	return nil
}
