// selection.js — the open card and what the page holds about it: the head
// (GET /api/cards/{id}), the thread (upserted by key from ?after=<seq>) and
// the live moment (GET …/live). It reacts to the event stream: a "card"
// event refetches the head and the thread's newer items, a "live" event the
// live block. It never decides anything about the card; it only fetches.

import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { on, set, state, rows, shownTab } from './store.js?v=__ASSET_V__'
import { write as writeHash } from './router.js?v=__ASSET_V__'
import { isMobile } from './dom.js?v=__ASSET_V__'
import { readAt } from './events.js?v=__ASSET_V__'
import { cached, keep, drop } from './threadcache.js?v=__ASSET_V__'

export async function loadBoard () {
  try {
    const b = await get('/api/board')
    readAt(b.eventId)
    // most events move one card, or nothing this read can see: an answer
    // the page already holds (bar the stream id it was read at) is not
    // drawn again, so the rail keeps its scroll, hover and focus
    if (state.board && same({ ...b, eventId: '' }, { ...state.board, eventId: '' })) return
    const patch = { board: b }
    if (Array.isArray(b.viewers) && b.viewers.length) patch.viewers = b.viewers
    set(patch)
    // the open card's head came from its own route; when the board says
    // its row moved (a stage, waits, the budget), read the head again
    const r = state.sel && b.rows?.find(x => x.id === state.sel)
    if (r && state.card && rowMoved(r, state.card)) loadCard(state.sel)
  } catch (err) {
    if (!state.board) set({ board: { repo: state.session?.repo || '', rows: [], counts: { needs: 0, running: 0 }, today: { spent: 0 }, viewers: [] } })
  }
}

// same says two answers of a route are the same answer. Every event is an
// invalidation, and most leave what a route answers as it was: a
// subscriber told about an equal answer redraws for nothing, and every
// redraw costs the reader a fold, a scroll or a selection.
export function same (a, b) {
  if (a === b) return true
  if (!a || !b) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

const HEAD_FIELDS = ['stage', 'status', 'envelope', 'landed', 'profile', 'repo', 'elsewhere']

function rowMoved (r, c) {
  return HEAD_FIELDS.some(k => (r[k] ?? null) !== (c[k] ?? null)) ||
    JSON.stringify(r.waits || []) !== JSON.stringify(c.waits || []) ||
    JSON.stringify(r.stack || null) !== JSON.stringify(c.stack || null)
}

// select opens a card. tab, when given, wins; then the tab a person last
// picked on this card, on this page; otherwise the panel follows the
// card's pinned decision (the spec for a design gate, the diff for a
// failed verify), and stays where it was when there is none. Whichever
// it lands on is written into the address, so a reload opens the same —
// unless named is false: a card the page picked by itself on a phone's
// cards list, which a reload must not read as a deep link.
let picks = 0
const picked = new Map() // card id -> the tab a person chose on it

// rememberTab records a person's own choice of a tab on a card (the
// panel's setTab), which a later visit to the card returns to.
export function rememberTab (id, tab) { if (id && tab) picked.set(id, tab) }

export async function select (id, { tab = null, view = true, named = true } = {}) {
  if (!id) return
  const mine = ++picks
  const changed = id !== state.sel
  if (changed) {
    // a card opened before draws its thread as this browser last saw it,
    // marked cached, until the read below replaces it (threadcache.js)
    const was = cached(id)
    const thread = was ? { ...was.thread, cached: true } : null
    set({ sel: id, card: null, cardErr: null, thread, live: was?.live || null, outgoing: null, hi: 0, showNext: null, mdecOpen: false, decAll: false, gone: null })
  }
  // on a phone a link that names a tab opens that document, not the
  // thread; a remembered tab only says which document waits behind it
  if (view && isMobile()) set({ view: tab ? 'panel' : 'thread' })
  // a link that names a tab opens the surface it is on; a remembered tab
  // only says which one waits behind the icon
  if (tab && !isMobile() && state.rightHidden) set({ rightHidden: false })
  const linked = !!tab
  if (!tab && changed && picked.has(id)) tab = picked.get(id)
  if (tab && tab !== state.tab) set({ tab })
  // the address names the tab showing, so a reload opens it again — but
  // not under a phone's thread, where naming one would open the document
  // nor with the surface closed, where a reload would open it
  if (named) writeHash(id, linked ? tab : shownTab())
  const tabBefore = state.tab
  if (changed) focus()
  // the head, the thread and the live block are read side by side: none
  // needs another's answer, and on a slow link each wait is a round trip
  const head = loadCard(id)
  if (changed) {
    loadThread(id, true)
    loadLive(id)
  }
  const card = await head
  // the panel follows the decision only when nothing chose a tab while the
  // card loaded: a later select (a link naming its tab) or a person's own
  // click wins over a guess made from a head that was still in flight
  if (changed && !tab && mine === picks && state.tab === tabBefore && card?.decision && card.decision.anchor !== 'thread' && state.sel === id) {
    set({ tab: card.decision.anchor })
    if (named) writeHash(id, shownTab())
  }
}

export async function loadCard (id = state.sel) {
  if (!id || state.gone === id) return null
  try {
    const c = await get(cardPath(id))
    if (state.sel !== id || state.gone === id) return c
    if (!state.cardErr && same(c, state.card)) return state.card
    const patch = { card: c, cardErr: null }
    const n = c.decision?.options?.length || 0
    if (state.hi >= n) patch.hi = 0
    set(patch)
    return c
  } catch (err) {
    // a read in flight when the card left the board fails for that reason:
    // the card is said to be gone (initSelection), not that it did not load
    if (state.sel === id && state.gone !== id) set({ cardErr: err })
    return null
  }
}

export async function loadThread (id = state.sel, full = false) {
  if (!id || state.gone === id) return
  // a cached thread is no base to page from: it is read whole
  const after = !full && state.thread?.items && !state.thread.cached ? state.thread.lastSeq || 0 : 0
  try {
    const t = await get(cardPath(id, 'thread') + (after ? `?after=${after}` : ''))
    if (state.sel !== id || state.gone === id) return
    const items = after ? upsert(withoutGone(state.thread.items, t.items || []), t.items || []) : (t.items || [])
    const lastSeq = Math.max(t.lastSeq || 0, after)
    const was = state.thread
    // a run's block comes on every page (it moves without an event of its
    // own), so a page that brought only what is held moves nothing
    const moved = !was || was.cached || was.err || was.gone || was.lastSeq !== lastSeq || !same(items, was.items)
    if (moved) set({ thread: { items, lastSeq } })
    if (t.live !== undefined && !liveRoute) setLive(liveOrNull(t.live))
  } catch (err) {
    // a read in flight when the card was deleted comes back not found:
    // the thread already says the card is gone, and keeps saying so
    if (state.sel !== id || state.gone === id) return
    set({ thread: { items: state.thread?.items || [], lastSeq: state.thread?.lastSeq || 0, unavailable: err.notBuilt, err } })
  }
}

// withoutGone drops the held run and dispatch items a paged read did not
// bring: those come on every page (threadfold.Since), so one missing is
// gone. A run a /clear or /handoff left behind is still sent, folded.
function withoutGone (items, fresh) {
  const keys = new Set(fresh.map(it => it.key))
  return items.filter(it => (it.t !== 'run' && it.t !== 'dispatch') || keys.has(it.key))
}

// upsert merges newer items into the list by key: an item that grew
// replaces its old self in place; a new one is appended. An item that
// supersedes others (a receipt answering a decision drawn while it
// waited) takes them out: the server stops sending an item that is gone,
// and this is the only word of it a paged read carries.
export function upsert (items, fresh) {
  const gone = new Set(fresh.flatMap(it => it.supersedes || []))
  const out = gone.size ? items.filter(it => !gone.has(it.key)) : items.slice()
  const at = new Map(out.map((it, i) => [it.key, i]))
  for (const it of fresh) {
    if (at.has(it.key)) out[at.get(it.key)] = it
    else { at.set(it.key, out.length); out.push(it) }
  }
  return out
}

// The live route may not exist on an older server; the thread's own live
// field stands in for it then.
let liveRoute = true
let liveBusy = false
let liveAgain = false
let liveAsks = 0 // live reads started, for which one an outgoing line waits on
let pushedAt = 0 // liveAsks when the stream last brought the live block
export async function loadLive (id = state.sel) {
  if (!id || !liveRoute) { if (!liveRoute) loadThread(id); return }
  if (liveBusy) { liveAgain = true; return }
  liveBusy = true
  const n = ++liveAsks
  try {
    const l = await get(cardPath(id, 'live'))
    // a read asked before the stream brought a block is older than it
    if (n <= pushedAt) return
    if (state.sel === id && state.gone !== id) {
      const patch = {}
      const live = liveOrNull(l)
      if (!same(live, state.live)) patch.live = live
      // a read asked after the line was taken draws the line itself (or
      // nothing, for a line that was no message): the page's stand-in goes
      // in the same redraw, so the two never show side by side
      const o = state.outgoing
      if (o && o.id === id && ((o.ack && n >= o.ack) || (n >= o.from && shows(live, o.text)))) patch.outgoing = null
      if (Object.keys(patch).length) set(patch)
    }
  } catch (err) {
    if (err.notBuilt) liveRoute = false
  } finally {
    liveBusy = false
    if (liveAgain) { liveAgain = false; loadLive() }
  }
}

// The event stream's name (its hello) and the card this page told it it
// shows: a live change about that card comes with its live block (pushLive)
// rather than as word to fetch it. A server without the route leaves
// every live change bare, and the page fetches as it always did.
let stream = ''
let focused = ''
let focusRoute = true
export function setStream (name) {
  stream = name || ''
  focused = ''
  focus()
}

function focus () {
  if (!stream || !focusRoute || focused === (state.sel || '')) return
  const card = state.sel || ''
  focused = card
  post('/api/events/focus', { stream, card }).catch((err) => {
    if (err.notBuilt) focusRoute = false
    else if (focused === card) focused = ''
  })
}

// pushLive draws the live block a "live" event brought for the open card:
// the read it stands for is done, and a read still on its way, asked
// before it, is older and is let go (loadLive).
export function pushLive (id, l) {
  if (state.sel !== id || state.gone === id) return
  pushedAt = liveAsks
  const patch = {}
  const live = liveOrNull(l)
  if (!same(live, state.live)) patch.live = live
  const o = state.outgoing
  if (o && o.id === id && shows(live, o.text)) patch.outgoing = null
  if (Object.keys(patch).length) set(patch)
}

// outgoing is a line on its way to the open card's session, drawn under
// the thread from the moment it is sent rather than a round trip later.
// sendOut puts it there; sendTaken says the server took it — as a message
// to the agent (keep), so the next live read stands in for it, or as
// something else, which the live block will never show — and sendFailed
// takes it back off. A line whose live read never lands goes after a while.
let outgoingTimer = 0
export function sendOut (id, text) {
  clearTimeout(outgoingTimer)
  set({ outgoing: { id, text, from: liveAsks + 1, ack: 0 } })
}

// shows says the live block holds text as a line on its way or as its
// conversation's newest line from the person — the server's own drawing
// of what the page was standing in for.
export function shows (l, text) {
  return ['consult', 'freeform'].some(k => {
    const c = l?.[k]
    if (!c) return false
    if (c.sending === text) return true
    const mine = (c.turns || []).filter(t => t.author === 'you')
    return mine.length > 0 && String(mine[mine.length - 1].text || '').trim() === text
  })
}

export function sendTaken (keep) {
  const o = state.outgoing
  if (!o) return
  if (!keep || !liveRoute) { set({ outgoing: null }); return }
  set({ outgoing: { ...o, ack: liveAsks + 1 } })
  outgoingTimer = setTimeout(() => { if (state.outgoing?.text === o.text) set({ outgoing: null }) }, 10000)
}

export function sendFailed () {
  clearTimeout(outgoingTimer)
  if (state.outgoing) set({ outgoing: null })
}

// setLive stores the live block unless it is the one already held: a
// "live" event is sent for a delta of any of the card's conversations, and
// often for none the read can see.
function setLive (l) {
  if (!same(l, state.live)) set({ live: l })
}

// liveOrNull is the server's live block, or null when it says nothing is
// live: an answer with no session, no busy word, no transcript and no
// conversation is the end of whatever the page was drawing, and must
// clear it rather than leave the last spinner standing.
export function liveOrNull (l) {
  if (!l) return null
  const talk = (c) => c && (c.busy || c.sending || c.streaming || c.err || c.turns?.length)
  const any = l.busy || l.state || l.streaming || l.tool || l.err || l.turns?.length ||
    talk(l.consult) || talk(l.freeform) || l.elsewhere
  return any ? l : null
}

// refresh is what a "card" event for the open card does. The live block is
// read again too: a card that moved (a run ended, a pause taken, a check
// finished) has usually ended what the block was drawing, and the page must
// not keep a spinner the server no longer reports.
export function refresh (id = state.sel) {
  if (!id || id !== state.sel) return
  loadCard(id)
  loadThread(id)
  loadLive(id)
  set({ cardRev: (state.cardRev || 0) + 1 })
}

export function refreshAll () {
  loadBoard()
  if (state.sel) {
    loadCard(state.sel)
    loadThread(state.sel, true)
    loadLive(state.sel)
    set({ cardRev: (state.cardRev || 0) + 1 })
  }
}

// nextNeeding opens the next card that needs a person, after the open one.
export function nextNeeding (toast) {
  const list = rows().filter(r => r.status === 'needs')
  if (!list.length) { toast?.('Nothing needs you'); return }
  const i = list.findIndex(r => r.id === state.sel)
  select(list[(i + 1) % list.length].id)
}

// nextRunning opens the next card an agent is working on, after the open one.
export function nextRunning (toast) {
  const list = rows().filter(r => r.status === 'running')
  if (!list.length) { toast?.('Nothing is running'); return }
  const i = list.findIndex(r => r.id === state.sel)
  select(list[(i + 1) % list.length].id)
}

// step walks the rail: +1 is the next visible row, -1 the previous one.
export function step (dir, ids) {
  if (!ids.length) return
  const i = ids.indexOf(state.sel)
  const n = i < 0 ? (dir > 0 ? 0 : ids.length - 1) : (i + dir + ids.length) % ids.length
  select(ids[n], { view: false })
}

export function initSelection () {
  // a card that left the board (deleted, here or on another device) is
  // said to be gone where it was open. The page used to move itself onto
  // another card: a line half-typed for this one was dropped, and enter
  // then answered that other card's decision — a landing, say — which
  // its reader had never looked at. Only a card the board has held is
  // gone: one made a moment ago is opened beside the board's read of it,
  // and an older read still on its way does not hold it yet.
  const seen = new Set()
  on(['thread', 'live'], () => keep(state.sel, state.thread, state.live))
  on('board', () => {
    for (const r of rows()) seen.add(r.id)
    if (state.sel && seen.has(state.sel) && state.board && !rows().some(r => r.id === state.sel) && state.card) {
      drop(state.sel)
      set({ card: null, live: null, gone: state.sel, thread: { items: [], lastSeq: 0, gone: true } })
    }
  })
}
