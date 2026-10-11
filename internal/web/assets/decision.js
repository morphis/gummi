// decision.js — the card's pinned decision: its question, the revision it
// was raised against, and numbered answers (options are the server's,
// regenerated on every read; the page never invents one). Answering sends
// the decision's ref, the option id (several, comma-separated, for a
// question that takes more than one), any words, and the against token.
//
// The server can refuse an answer with a 409 that says why, or stop at a
// question (a 202, which api.js throws the same way), and each gets its
// own treatment rather than a bare error:
//
//   answered  someone else got there first: say who, and what they said
//   moved     the card moved under the page: say so, and show it as it is
//   confirm   the flow stops at a question the TUI would ask (y/n): ask it
//             here — the server's words — and send the same answer again with
//             the token the question came with
//   needs     the answer needs words it did not carry: say what, and put
//             the reader in the composer
//   newcard   the words read as separate work: open the new-card form
//   busy      the agent is mid-turn: the words go back in the composer
//
// Also here: the "also needs you" chip shown after an answer, and the
// phone's docked decision bar.

import { $, h, clear, decisionWord, decisionColor, needsColor, isMobile, plural } from './dom.js?v=__ASSET_V__'
import { post, cardPath } from './api.js?v=__ASSET_V__'
import { on, set, state, rows, row } from './store.js?v=__ASSET_V__'
import { toast, hush } from './toast.js?v=__ASSET_V__'
import { openView, openModal } from './views.js?v=__ASSET_V__'
import { runAction, messageHint, draftAffordance, landMethodControl, landMessageBox } from './actions.js?v=__ASSET_V__'

let ctx = {}
let answering = false
// landing, while one is open: the dialog a landing answer opened, which
// that answer's replies are routed into (see refused) instead of notes it
// would cover. Opened by the press before its request, it is on screen
// while the board drafts, the way the menu's merge entry already behaves.
let landing = null
// landingDismissed: the person closed the landing dialog while the answer
// that opened it was still out. Its reply, arriving late, is said beside
// the decision — a dialog the person walked away from does not come back.
let landingDismissed = false

// A decision that has just appeared is not a bare enter's to answer yet: a
// second press meant for the answer before it (a double enter, a key held
// a moment too long) would otherwise give the new question its first
// answer unread. A person's own choice on it (a click, an arrow, a digit)
// or words typed for it are read, and answer at once.
const SETTLE_MS = 800
let shownAt = 0
// replacedAt: when a press's own answer last replaced the decision on the
// open card (its reply, or the stop that came a moment after it). A press
// arriving that soon is the second click of a double click, aimed at the
// answer before it. A decision that arrived any other way — the card just
// opened, a chip raised by a line sent — answers at once.
let replacedAt = 0
let pressed = { busy: false, done: 0 }
// held: the decision changed under words typed for the one before it.
// The next enter only says so; the one after sends them to the new one.
let held = false
// explained: a refusal just said why the decision changed under this
// page's answer (answered first, moved). The change itself, arriving a
// moment later, keeps that note rather than replacing it.
let explained = null
const EXPLAINED_MS = 10000

export function initDecision (c) {
  ctx = c
  on(['card', 'hi', 'conn', 'diffPending', 'sel', 'picked', 'decNote', 'decConfirm'], renderDecision)
  on(['showNext', 'board', 'sel'], renderNext)
  on(['card', 'hi', 'conn', 'view', 'mdecOpen', 'sel', 'diffPending', 'picked', 'decNote', 'decConfirm', 'showNext', 'board'], renderMdec)
  // a note is about the card it was said on
  on(['sel'], () => { explained = null; set({ decNote: null, decConfirm: null, picked: [], hiUser: false }) })
  // a different decision (or none) drops what was picked for the last one,
  // and the highlight goes back to its first answer as the TUI's cursor
  // does: a row chosen for one question is never enter's answer to the next.
  // A decision is a different one when its ref changes, and also when its
  // against token does: a gate keeps its ref while another viewer's answer
  // ("stop here" parks it) swaps its answer set and revision under the
  // page, and the token names both
  let ref = null
  let token = null
  let refCard = null
  on(['card'], () => {
    const d = openDecision()
    const r = d?.ref || null
    const t = d?.against?.token || null
    if (r !== ref || (r !== null && t !== token)) {
      // a decision that changed under an answer the person had chosen is
      // said, not silently swapped: what they chose was for the old one
      const sameCard = refCard === state.sel
      const same = ref !== null && sameCard && !answering
      const chose = same && state.hiUser
      // words typed for the old decision are aimed at whichever answer
      // takes words in the new one: a reply to a question someone else
      // answered would go as a rework of the plan, say, unannounced
      const wrote = same && !chose && !!state.draft.trim()
      replacedAt = ref !== null && sameCard && (pressed.busy || Date.now() - pressed.done < SETTLE_MS) ? Date.now() : 0
      ref = r
      token = t
      refCard = state.sel
      shownAt = Date.now()
      // a refusal that already said what happened (who answered first, or
      // that the card moved) is not covered by a second, vaguer note
      const told = !!explained && explained.card === state.sel && Date.now() - explained.at < EXPLAINED_MS
      // the row the person chose is not moved onto another answer of the
      // new set (the same place in a new list is a different answer):
      // nothing is highlighted until they choose again, so enter cannot
      // give an answer they never picked
      set({ picked: [], decConfirm: null, hi: chose || told ? -1 : 0, hiUser: false })
      if (chose && !told) note(`${state.sel} moved while you were choosing — read it again, then answer.`, { tone: 'warn', testid: 'decision-moved' })
      else if (wrote && !told) note(`${state.sel} moved while you were writing — read it again; your words are still here.`, { tone: 'warn', testid: 'decision-moved' })
      // kept across the card's next moves (it may run a while before its
      // next decision), dropped with the card
      held = wrote || (held && sameCard)
    }
  })
  on(['draft'], () => { if (!state.draft.trim()) held = false })
  window.addEventListener('resize', renderMdec)
}

export function openDecision () { return state.card?.decision || null }

// highlight moves the highlight. by is 'user' for a person's own choice
// (a click, an arrow, a digit) and anything else for the page's aim (the
// row that takes the words being typed); only a person's own choice lets
// enter or a press answer a decision that has just appeared.
export function highlight (i, by = 'user') {
  const d = openDecision()
  if (!d || i < 0 || i >= d.options.length) return
  set({ hi: i, hiUser: by === 'user' || (state.hiUser && state.hi === i) })
}

// wordsOption is the first answer that takes a note, or -1.
export function wordsOption () {
  const d = openDecision()
  return d ? d.options.findIndex(o => o.words) : -1
}

// note shows a line above the decision (or in its place, once the decision
// has gone): what happened to the last answer.
export function note (text, { tone = 'info', testid = 'decision-note' } = {}) {
  if (text) hush(text)
  set({ decNote: text ? { text, tone, testid } : null })
}

// pickable: a multi-pick question's own options (never its chat row).
function pickable (d, o) { return d.multi && !o.chat }

function optionButtons (d, compact) {
  const offline = state.conn !== 'live'
  const pending = state.diffPending || 0
  const picked = state.picked || []
  return h('div', { class: 'opts', role: 'group', 'aria-label': d.multi ? 'Answers — pick any' : 'Answers' }, d.options.map((o, i) => h('button', {
    class: ['opt', i === state.hi && 'hi', o.chat && 'chat', o.danger && 'danger', pickable(d, o) && 'pick', picked.includes(o.id) && 'picked'],
    type: 'button',
    data: { i: String(i) },
    testid: `${compact ? 'mdec' : 'decision'}-option-${o.id}`,
    disabled: offline || answering,
    'aria-pressed': String(pickable(d, o) ? picked.includes(o.id) : i === state.hi),
    title: o.detail || o.label,
    onclick: () => onOption(i, compact)
  },
  h('span', { class: 'k', 'aria-hidden': 'true' }, pickable(d, o) ? (picked.includes(o.id) ? '✓' : String(i + 1)) : String(i + 1)),
  h('span', { class: 'l' }, o.label),
  h('span', { class: 'd' }, o.carriesComments && pending
    ? h('span', { class: 'carry', testid: 'decision-carry' }, `+ ${plural(pending, 'diff comment')}`)
    : (o.detail || (o.words ? 'type below' : ''))))))
}

// SURFACES are answers that only open something: the page has each of
// them itself, so pressing one opens it here instead of asking the board
// to open the terminal's.
const SURFACES = {
  spec: () => ctx.setTab('spec'),
  diff: () => ctx.setTab('diff'),
  goalpage: () => openView('goal', { id: state.sel }),
  // the card's own menu entry, which asks for the profile the way the
  // menu does (a failed stage offers "change profile")
  profile: () => {
    const a = (state.card?.actions || []).find(x => x.id === 'profile')
    if (a) runAction(state.card, a)
  }
}

// isLanding mirrors the board's rule for which answers are landings
// (webintents.go): the verify gate's "advance", and the merge answer a
// stopped or handed-off card's set carries. That rule has no research
// carve-out; the research card's "mark done" at the same gate is out
// because the flow never asks it for a message — a research card lands
// nothing (msgs.go advanceLands), so its advance settles without a
// landing stop and no dialog is wanted. Such an answer stops on the
// landing message, and its dialog opens for it on the press.
//
// A card linked to a pull request is out too: its verify gate's advance
// is "merge the PR", which lands on GitHub, and the board refuses a local
// landing for it (merge.go) before any message is asked for — so the
// answer goes as any other, and the refusal is said beside the decision
// rather than over a message box nobody can use.
function isLanding (o) {
  if (state.card?.kind === 'research' || state.card?.pr) return false
  return (o.id === 'advance' && state.card?.stage === 'verify') || o.id === 'merge'
}

function onOption (i, compact) {
  const d = openDecision()
  const o = d.options[i]
  if (SURFACES[o.id]) {
    set({ hi: i })
    if (isMobile() && o.id !== 'goalpage' && o.id !== 'profile') set({ view: 'panel' })
    SURFACES[o.id]()
    return
  }
  if (pickable(d, o)) {
    togglePick(o.id)
    set({ hi: i, hiUser: true })
    return
  }
  // a press on an answer gives it, as its key does in the terminal. What
  // asks first is the server's to ask — a landing's message, a yes for
  // what spends or cannot be undone, the words a row is nothing without —
  // so the page adds no press of its own. A note written first goes with
  // the answer that takes one; the chat row with none asks for it.
  //
  // The one exception: a decision that has just replaced another under
  // the pointer (the second click of a double click, meant for the answer
  // before it) is chosen, not answered unread.
  const chose = state.hi === i && state.hiUser
  set({ hi: i, hiUser: true })
  if (!chose && Date.now() - replacedAt < SETTLE_MS) {
    note(`${state.sel} has a new decision — read it, then ${compact ? 'tap' : 'press'} “${o.label}” again.`, { tone: 'warn', testid: 'decision-fresh' })
    return
  }
  pressed = { busy: true, done: 0 }
  Promise.resolve(answer({ picked: true })).finally(() => { pressed = { busy: false, done: Date.now() } })
}

export function togglePick (id) {
  const picked = (state.picked || []).slice()
  const at = picked.indexOf(id)
  if (at >= 0) picked.splice(at, 1)
  else picked.push(id)
  set({ picked })
}

function noteEl (compact) {
  const n = state.decNote
  if (!n) return null
  return h('div', { class: ['dnote', n.tone], testid: compact ? 'mdec-note' : n.testid, role: n.tone === 'err' ? 'alert' : 'status' },
    h('span', null, n.text),
    h('button', { class: 'link', type: 'button', 'aria-label': 'Dismiss', onclick: () => set({ decNote: null }) }, 'dismiss'))
}

// confirmEl is the question an answer's flow stopped on (the TUI's y/n),
// drawn beside the decision rather than inside it: the decision is capped
// and clips what overflows it, and a confirmation nobody can reach is a
// flow nobody can finish (on a phone it sat under the composer).
function confirmEl (compact) {
  const c = state.decConfirm
  if (!c) return null
  const tid = compact ? 'mdec-confirm' : 'decision-confirm'
  return h('div', { class: 'dconfirm', testid: tid, role: 'alertdialog', 'aria-label': 'Confirm', 'aria-describedby': `${tid}-q`, tabindex: '-1' },
    h('div', { class: 'cq', id: `${tid}-q`, testid: `${tid}-question` }, c.question),
    h('div', { class: 'cb' },
      h('button', { class: 'btn', type: 'button', testid: `${tid}-no`, onclick: () => set({ decConfirm: null }) }, 'Cancel'),
      h('button', { class: ['btn', 'pri', c.danger && 'danger'], type: 'button', testid: `${tid}-yes`, disabled: answering, onclick: () => { const go = c.go; set({ decConfirm: null }); go() } }, c.yes || 'Yes, go ahead')))
}

// revealConfirm brings a confirmation that just appeared into view and
// puts focus on it, so a keyboard or a screen reader meets the question
// before anything else (Tab then reaches Cancel and the answer).
let reveal = false
function revealConfirm () {
  reveal = false
  const el = [...document.querySelectorAll('.dconfirm')].find(e => e.getClientRects().length)
  if (!el) return
  el.scrollIntoView({ block: 'nearest' })
  el.focus({ preventScroll: true })
}

// keepFocus redraws a box without dropping the focus a control inside it
// had: the same control (by test id) takes it back once redrawn.
function keepFocus (box, draw) {
  const a = document.activeElement
  const tid = a && a !== box && box.contains(a) ? a.dataset.testid : null
  draw()
  if (!tid) return
  const again = box.querySelector(`[data-testid="${CSS.escape(tid)}"]`)
  if (again && !again.disabled) again.focus({ preventScroll: true })
}

// jumpFor is the link beside the decision to the tab it is about. A
// research card never gets a branch of its own: its work is the document,
// so there is no diff to send anyone to.
function jumpFor (d, card) {
  const research = card?.kind === 'research'
  if (d.anchor === 'spec' || (d.anchor === 'diff' && research)) return ['spec', research ? 'read the document' : 'read the spec']
  if (d.anchor === 'diff') return ['diff', 'see the diff']
  return null
}

// clipHint fades the foot of a question longer than the space the
// decision gives it, so a line cut off mid-sentence reads as "scroll for
// more" rather than as the end of the question; the fade goes once the
// rest is scrolled into view. The question's box changes height with the
// window and the dock, so the one on screen is watched (qResize).
function clipHint (q) {
  q.classList.toggle('clip', q.scrollHeight - q.scrollTop - q.clientHeight > 2)
}
const qResize = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(es => { for (const e of es) clipHint(e.target) })

let shownHi = -1
function renderDecision () {
  const box = $('#decision')
  keepFocus(box, () => drawDecision(box))
}

function drawDecision (box) {
  // the answers scroll inside a capped decision: keep where the list was
  // and where the question was scrolled to, and bring the highlighted
  // answer into view when it moves
  const was = box.querySelector('.decision > .opts')?.scrollTop || 0
  const wasQ = box.querySelector('.decision > .q')?.scrollTop || 0
  clear(box)
  box.append(noteEl(false) || '')
  const d = openDecision()
  if (!d) return
  const stage = state.card.stage
  const offline = state.conn !== 'live'
  const jump = jumpFor(d, state.card)
  box.append(h('section', {
    class: ['decision', offline && 'paused', answering && 'sending'],
    style: { '--dc': decisionColor(d, stage) },
    'aria-label': 'Open decision',
    'aria-busy': answering ? 'true' : null,
    testid: 'decision',
    data: { kind: d.kind, ref: d.ref }
  },
  h('header', null, decisionWord(d, stage),
    h('span', { class: 'more' },
      state.card.decisionsMore ? h('span', { testid: 'decision-more' }, `${state.card.decisionsMore} more after this`) : null,
      // what the answer is given against, as one token: the sentence
      // around it is its tooltip and what a screen reader hears
      h('span', { class: ['against', offline && 'off'], testid: 'decision-against' }, offline
        ? 'Reconnecting. Answers wait until the board is back.'
        : [h('span', { class: 'sr-only' }, 'You are answering against '),
            h('span', { class: 'mono', title: 'What you are answering against: the card as you read it. An answer to a card that has moved since is refused.' }, d.against?.label || d.against?.token || 'the card as shown'),
            d.multi ? h('span', null, isMobile() ? ' · pick any, then Answer' : ' · pick any, then enter') : '']),
      jump ? h('button', { class: 'link', type: 'button', testid: 'decision-jump', onclick: () => ctx.setTab(jump[0]) }, jump[1]) : null)),
  h('div', { class: 'q', testid: 'decision-question' }, d.question),
  optionButtons(d, false)),
  confirmEl(false) || '')
  const opts = box.querySelector('.decision > .opts')
  if (opts) opts.scrollTop = was
  const q = box.querySelector('.decision > .q')
  if (q) {
    q.scrollTop = wasQ
    clipHint(q)
    q.addEventListener('scroll', () => clipHint(q), { passive: true })
    qResize?.disconnect()
    qResize?.observe(q)
  }
  if (state.hi !== shownHi) {
    shownHi = state.hi
    opts?.querySelector('.opt.hi')?.scrollIntoView({ block: 'nearest' })
  }
}

// nextRow is the card the "also needs you" chip offers, while it still
// does.
function nextRow () {
  const nx = state.showNext && row(state.showNext)
  return nx && nx.status === 'needs' && nx.id !== state.sel ? nx : null
}

// needsLine is what the chip says the next card waits on, worded the way
// the rail heads it: a question is quoted (its "asks:" prefix, which only
// some of the board's stops carry, dropped — the word already says it is
// one), and any other stop is named by its word and the card's title, not
// the stop's raw park line, which can be a run's CLI wording.
function needsLine (nx) {
  const n = nx.needs || {}
  if (n.kind === 'question' && n.question) return `${n.word || 'question'}: ${n.question.replace(/^asks:\s*/i, '')}`
  return [n.word, nx.title].filter(Boolean).join(' · ')
}

function nextChip (nx) {
  return h('button', {
    class: 'nextup', type: 'button', testid: 'nextup',
    style: { '--dc': needsColor(nx.needs, nx.stage) },
    onclick: () => ctx.select(nx.id)
  }, h('span', { class: 'dotc' }), h('span', { class: 'nl' }, h('b', null, nx.id), ` also needs you · ${needsLine(nx)}`),
  h('span', { class: 'go' }, 'Open ', h('kbd', null, 'n')))
}

function renderNext () {
  const box = $('#nextup')
  clear(box)
  const nx = nextRow()
  if (nx) box.append(nextChip(nx))
}

function renderMdec () {
  const box = $('#mdec')
  keepFocus(box, () => drawMdec(box))
}

function drawMdec (box) {
  const d = openDecision()
  // the bar follows the card into its documents only: over the cards it
  // would pin one card's decision under a list of every card that has one.
  // It also carries the next card waiting after an answer — the chip in
  // the dock sits with the composer, out of sight from a document tab —
  // and, with no decision left, what the answer left to say
  const nx = state.view !== 'panel' ? null : nextRow()
  const show = isMobile() && (!!d || !!state.decNote || !!nx) && state.view === 'panel'
  box.hidden = !show
  clear(box)
  if (!show) return
  if (!d) {
    if (nx) box.style.setProperty('--dc', needsColor(nx.needs, nx.stage))
    box.classList.remove('open')
    box.append(noteEl(true) || '', nx ? h('div', { class: 'mnext' }, nextChip(nx)) : '')
    return
  }
  box.style.setProperty('--dc', decisionColor(d, state.card.stage))
  box.classList.toggle('open', state.mdecOpen)
  box.append(h('button', { class: 'sum', type: 'button', testid: 'mdec-toggle', 'aria-expanded': String(state.mdecOpen), onclick: () => set({ mdecOpen: !state.mdecOpen }) },
    h('span', { class: 'k' }, decisionWord(d, state.card.stage)),
    h('span', { class: 'q' }, d.question),
    h('span', { class: 'chev', 'aria-hidden': 'true' }, '▴')))
  if (state.mdecOpen || state.decNote || state.decConfirm) {
    box.append(noteEl(true) || '')
    if (state.mdecOpen) {
      box.append(optionButtons(d, true))
      // a question that takes several answers is sent by a press of its
      // own: tapping an answer only ticks it, and the bar has no enter
      if (d.multi) {
        const n = (state.picked || []).length
        box.append(h('button', { class: 'btn pri msend', type: 'button', testid: 'mdec-send', disabled: !n || answering || state.conn !== 'live', onclick: () => answer() },
          n ? `Answer with ${plural(n, 'choice')}` : 'Tick one or more, then answer'))
      }
    }
    box.append(confirmEl(true) || '')
  }
  if (nx) box.append(h('div', { class: 'mnext' }, nextChip(nx)))
}

// chosen is what enter would answer: the picked options of a multi-pick
// question, else the highlighted one — or null when nothing is
// highlighted (the decision changed under the person's choice), never a
// stand-in for it.
function chosen (d) {
  const o = d.options[state.hi] || null
  const picked = (state.picked || []).filter(id => d.options.some(x => x.id === id && !x.chat))
  if (d.multi && picked.length && !(o?.chat && state.draft.trim())) {
    const labels = d.options.filter(x => picked.includes(x.id)).map(x => x.label)
    return { id: d.options.filter(x => picked.includes(x.id)).map(x => x.id).join(','), label: labels.join(', '), words: false }
  }
  return o
}

// NONE is what enter says, and does, with nothing highlighted.
export const NONE = 'pick an answer first'

// enterSays is what the composer's enter line reads with a decision pinned.
export function enterSays (d) {
  const o = chosen(d)
  const text = state.draft.trim()
  if (text && !o?.words) {
    const w = wordsOption()
    if (w >= 0) return d.options[w].relabel || d.options[w].label
  }
  if (!o) return NONE
  return text && o.words ? (o.relabel || o.label) : o.label
}

// answer sends the chosen option, with the composer's words when the
// option takes them. Typed words are never dropped: with a line in the
// composer, enter gives it to the answer that takes words (DESIGN §6.3,
// enter sends the line), and when none does the line goes as a line —
// only a person's own press on an answer (picked) gives that answer and
// leaves the line where it is.
export async function answer ({ picked = false } = {}) {
  const d = openDecision()
  if (!d || answering) return
  if (state.conn !== 'live') { toast('Answers wait until the board reconnects'); return }
  if (held && !picked && state.draft.trim()) {
    held = false
    note(`${state.sel} moved while you were writing — enter again sends your words as “${enterSays(d)}”.`, { tone: 'warn', testid: 'decision-moved' })
    return
  }
  held = false
  if (!picked && !state.hiUser && !state.draft.trim() && Date.now() - shownAt < SETTLE_MS) {
    note(`${state.sel} has a new decision — read it, then press enter again.`, { tone: 'warn', testid: 'decision-fresh' })
    return
  }
  let o = chosen(d)
  const text = state.draft.trim()
  if (text && !o?.words && !picked && !(d.multi && state.picked?.length)) {
    const w = wordsOption()
    if (w < 0) { ctx.submitLine?.(); return }
    o = d.options[w]
  }
  if (!o) {
    note(`Nothing is highlighted on ${state.sel}'s decision — ${isMobile() ? 'tap an answer' : 'pick one with ↑↓ or its number, then press enter'}.`, { testid: 'decision-pick' })
    return
  }
  if (SURFACES[o.id] && !text) {
    if (isMobile() && o.id !== 'goalpage' && o.id !== 'profile') set({ view: 'panel' })
    SURFACES[o.id]()
    return
  }
  if (o.chat && !text) {
    if (isMobile()) set({ view: 'thread' })
    $('#composer-input').focus()
    note('Type your answer in the composer, then press enter.', { testid: 'decision-needs' })
    return
  }
  const label = text && o.words ? (o.relabel || o.label) : o.label
  const body = {
    ref: d.ref,
    option: o.id,
    words: o.words && text ? text : undefined,
    against: d.against?.token || ''
  }
  // A landing pressed bare opens its dialog on the press, before the
  // request — the menu's merge entry already behaves so — and shows its
  // drafting state while the answer waits on the live draft pass behind
  // it. Words typed for the option are the person's own message; a landing
  // on those goes straight to the board.
  if (isLanding(o) && !body.words) {
    landingDismissed = false
    const dlg = openLanding(state.sel, d, o, '')
    dlg.wait(true)
    try {
      await send(state.sel, body, label, false, o.danger)
    } finally {
      dlg.wait(false)
      // the person dismissed the dialog mid-flight: its close could not
      // hand the focus back, the answer's own buttons being disabled
      // (the request was still out). They are theirs again now.
      if (dlg.left) refocusAnswer(o.id)
    }
    // a reply the dialog kept — the draft to read, a failure to retry
    // from — holds it open; anything else (the card moved on without one)
    // closes it
    if (!dlg.kept) dlg.close()
    return
  }
  return send(state.sel, body, label, !!(o.words && text), o.danger)
}

// refocusAnswer puts the focus back on the answer a dismissed dialog was
// opened from — the first such answer on screen that can take it.
function refocusAnswer (id) {
  const back = [...document.querySelectorAll(`[data-testid$="-option-${CSS.escape(id)}"]`)]
    .find(el => el.getClientRects().length && !el.disabled)
  back?.focus()
}

async function send (id, body, label, tookWords, danger) {
  answering = true
  set({ decNote: null, decConfirm: null })
  try {
    const card = await post(cardPath(id, 'answer'), body)
    if (tookWords) ctx.clearComposer()
    const next = rows().find(r => r.status === 'needs' && r.id !== id)
    if (state.sel === id) set({ card: card && card.id ? card : state.card, hi: 0, picked: [], mdecOpen: false, showNext: next ? next.id : null })
    // "waits on …" answers nothing: the board says what the card waits
    // for in a toast of its own, and "Answered" would claim it moved
    if (body.option !== 'wait') toast(`Answered: ${label}`)
    ctx.refresh(id)
  } catch (err) {
    refused(id, err, body, label, tookWords, danger)
  } finally {
    answering = false
    renderDecision()
    renderMdec()
    if (reveal) revealConfirm()
  }
}

function refused (id, err, body, label, tookWords, danger) {
  if (err.notBuilt && err.status !== 404) {
    landing?.close()
    toast('Answering from the web is not available yet')
    return
  }
  if (err.status !== 409) {
    if (landing) { landing.failed(err.message); return }
    note(err.message, { tone: 'err', testid: 'decision-error' })
    return
  }
  const e = err.data || {}
  switch (e.error) {
    case 'answered':
      // the dialog was about a card that is no longer the one it was:
      // close it, and say what happened where the decision is
      landing?.close()
      unchoose(id)
      note(`Answered by ${e.by || 'someone else'}${e.receipt ? ` — ${e.by && e.receipt.startsWith(e.by + ' ') ? e.receipt.slice(e.by.length + 1) : e.receipt}` : ''}. Here is the card as it stands now.`, { tone: 'warn', testid: 'decision-answered' })
      ctx.refresh(id)
      return
    case 'moved':
      landing?.close()
      unchoose(id)
      note(`${id} moved since you read it${e.text ? ` (${e.text.replace(/^the card moved since you read it\s*[—-]\s*/, '')})` : ''}. Read it again, then answer if it still holds.`, { tone: 'warn', testid: 'decision-moved' })
      ctx.refresh(id)
      return
    case 'confirm':
      landing?.close()
      landingDismissed = false // the close was the flow's, not the person's
      // the server's question, verbatim; the yes sent back is its token
      set({ decConfirm: { question: sentence(e.text) || `${label}?`, yes: `Yes, ${label}`, danger, go: () => send(id, { ...body, confirm: [body.confirm, e.confirm].filter(Boolean).join(' ') }, label, tookWords, danger) } })
      reveal = true
      return
    case 'needs':
      if (e.needs === 'decision') {
        // which decision to reverse is picked on the goal's page, which
        // lists them with the lead's reasons
        landing?.close()
        note(sentence(e.text) || 'Pick the decision on the goal’s page.', { testid: 'decision-needs' })
        openView('goal', { id })
        return
      }
      if (e.needs === 'message' && e.draft !== undefined && e.draft !== null) {
        // a landing stopped to have its message read: into the dialog the
        // press opened when one is up (a second one on top of it would
        // stack two of the same), else it opens on the draft — unless the
        // person dismissed that dialog while the answer was still out: a
        // dialog they walked away from does not come back, and the note
        // says where the landing stands instead
        if (landing) { landing.fill(e.draft, sentence(e.text)); return }
        if (landingDismissed) { note(sentence(e.text) || 'This answer needs more from you.', { tone: 'info', testid: 'decision-needs' }); return }
        const d = openDecision()
        const o = d?.options.find(x => x.id === body.option)
        if (d && o) { openLanding(id, d, o, e.draft, sentence(e.text)); return }
      }
      if (e.needs === 'message') {
        if (landing) { landing.asked(sentence(e.text)); return }
        const w = wordsOption()
        if (w >= 0) set({ hi: w })
        if (isMobile()) set({ view: 'thread' })
        $('#composer-input').focus()
      } else {
        landing?.close()
      }
      note(sentence(e.text) || 'This answer needs more from you.', { tone: 'info', testid: 'decision-needs' })
      return
    case 'newcard':
      landing?.close()
      note('That reads as separate work — start it as its own card.', { testid: 'decision-newcard' })
      openView('newcard', { text: e.text })
      return
    case 'busy':
      landing?.close()
      if (e.text) ctx.restoreComposer?.(e.text)
      note('The agent is mid-turn. Your words are back in the composer; send them when this turn ends.', { tone: 'warn', testid: 'decision-busy' })
      return
  }
  // A refusal with no reason of its own — the give-up on a draft pass, or
  // one of the landing's own — reads in the dialog while it is open: a
  // note behind it is a note nobody sees, and the dialog is the way to
  // retry (send bare again) or land on a message written here.
  if (landing) { landing.failed(err.message); ctx.refresh(id); return }
  note(pageWords(err.message), { tone: 'err', testid: 'decision-error' })
  ctx.refresh(id)
}

// pageWords points a refusal's terminal command at the page's own way to
// do the same, where the card's menu has it: a PR-linked card's landing
// refusal names `gummi pr unlink`, which is the menu's "unlink PR" here.
function pageWords (text) {
  const unlink = state.card?.actions?.find(a => a.id === 'prunlink')
  if (!unlink) return text
  return String(text).replace(/\(?`gummi pr unlink [^`]+` to land it locally instead\)?/, `(“${unlink.label}” in the card’s menu lands it locally instead)`)
}

// unchoose drops the highlight after an answer the board refused because
// the decision changed under it: the row pressed was for the old one, and
// whatever now sits in its place is not the person's choice. The change
// arriving after this keeps the refusal's note (explained).
function unchoose (id) {
  explained = { card: id, at: Date.now() }
  if (state.sel === id) set({ hi: -1, hiUser: false, picked: [] })
}

// sentence capitalises the server's lower-case line for a page.
export function sentence (s) {
  s = String(s || '').trim()
  return s ? s[0].toUpperCase() + s.slice(1) : ''
}

// draftWhere says where the message in the box came from, for the hint:
// the verify gate's stored draft when the box holds exactly it, else one
// a live pass just brought back, else none.
function draftWhere (msg) {
  msg = String(msg || '').trim()
  if (!msg) return 'none'
  const stored = String(state.card?.actions?.find(x => x.id === 'merge')?.default || '').trim()
  return msg === stored ? 'default' : 'drafted'
}

// openLanding is the landing dialog a landing answer opens. A press opens
// it before the request, empty: the hint offers the leave-it-empty path —
// sent bare again, the landing stops on its message once more, the stored
// draft right away and a fresh pass after a give-up — and the drafting
// state covers the wait. An answer that stopped to have its message read
// opens it on the draft. It lands only on its own button, as the TUI's
// landing dialog does: a branch never lands on a message nobody read.
function openLanding (id, d, o, draft, question = '') {
  const input = h('textarea', { class: 'lmsg', rows: '8', testid: 'landing-message', 'aria-label': 'Landing message' })
  input.value = draft || ''
  const q = h('p', { class: 'aq', testid: 'landing-question' }, question || 'Read the landing message, then land.')
  // squash or a merge commit keeps the branch's commits: offered where the
  // option says both are (Option.methods). A merge takes git's own message,
  // so its box and hint are hidden and nothing is read first
  const control = o.methods?.length > 1 ? landMethodControl(o.methods) : null
  const merging = () => control?.value() === 'merge'
  const hint = h('span', { class: 'fh', testid: 'landing-hint' }, messageHint(false, draftWhere(draft)))
  const aff = draftAffordance(input, hint)
  if (control) {
    const mergeQ = 'A merge commit lands with git’s own message, so there is nothing to read first.'
    control.el.addEventListener('change', () => {
      q.textContent = merging() ? mergeQ : (question || 'Read the landing message, then land.')
    })
    landMessageBox(control, input, hint)
  }
  const err = h('p', { class: 'aerr', testid: 'landing-error', role: 'alert', hidden: true })
  // kept: the last reply was routed into the dialog rather than past it,
  // so the send that is out must not close it (a draft to read, a failure
  // to retry from)
  const dlg = { kept: false }
  const m = openModal({
    title: `${sentence(o.label)} · ${id}`,
    testid: 'landing-dialog',
    card: id,
    // the answer that opened it is redrawn while it is up
    returnTo: `[data-testid$="-option-${CSS.escape(o.id)}"]`,
    onClose: () => { dlg.left = true; landing = null; landingDismissed = true },
    body: [q, h('label', { class: 'field' }, control?.el, input, hint), err],
    actions: [
      { label: 'Cancel', testid: 'landing-cancel' },
      {
        label: sentence(o.label),
        primary: true,
        danger: true,
        testid: 'landing-confirm',
        onClick: async () => {
          // a merge commit sends no words: the box is hidden for it, and
          // the server lands it with git's own message at once
          const merge = !!control && merging()
          const words = merge ? '' : input.value.trim()
          const b = { ref: d.ref, option: o.id, against: d.against?.token || '' }
          if (words) b.words = words
          if (control) b.method = control.value()
          dlg.kept = false
          go.disabled = true
          // sent bare, a squash waits on its drafting pass again: the
          // button and the hint say so, the way the menu's merge entry does
          const wait = !words && !merge
          if (wait) { aff.busy(true); go.textContent = 'Drafting…' }
          try {
            await send(id, b, o.label, false, true)
          } finally {
            if (wait) aff.busy(false)
            go.disabled = false
            go.textContent = sentence(o.label)
            if (dlg.left) refocusAnswer(o.id)
          }
          // a reply the dialog kept holds it open; a landing that went
          // through, or a question it handed back to the decision, closes it
          return !dlg.kept
        }
      }
    ]
  })
  const go = m.el.querySelector('[data-testid="landing-confirm"]')
  // wait shows the drafting state while the answer the press sent is out,
  // and ends it however the reply landed
  dlg.wait = (on) => {
    if (on) { aff.busy(true); go.textContent = 'Drafting…'; go.disabled = true }
    else { aff.busy(false); go.disabled = false; go.textContent = sentence(o.label) }
  }
  dlg.fill = (draft, text) => {
    dlg.kept = true
    clear(q).append(text || 'Read the landing message, then land.')
    err.hidden = true
    // the box is filled only while it is empty: the person's own words
    // stand, as the TUI's dialog never overwrites typed ones
    if (!input.value.trim() && draft.trim()) {
      input.value = draft
      aff.drafted(messageHint(false, draftWhere(draft)))
      input.focus(); input.setSelectionRange(0, 0); input.scrollTop = 0
    }
  }
  dlg.asked = (text) => {
    dlg.kept = true
    clear(q).append(text || 'This answer needs more from you.')
    err.hidden = true
  }
  dlg.failed = (text) => {
    dlg.kept = true
    clear(err).append(sentence(text) || text)
    err.hidden = false
  }
  dlg.close = () => m.close()
  landing = dlg
  input.focus()
  input.setSelectionRange(0, 0)
  input.scrollTop = 0
  return dlg
}
