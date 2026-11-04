// thread.js — the card's conversation. Items arrive from the server already
// folded (internal/threadfold) and are upserted by key, so an item that grew
// (a tool group gaining calls, a stage divider getting its verdict) is
// redrawn in place rather than appended twice. Items group under the stage
// divider before them; earlier stages fold into one line each and the
// current one stays open. A foldable row a person has toggled keeps their
// open state across those redraws: the toggle is remembered per card and
// each builder re-applies it, for the page session only. So does a row a
// person unclamped ("show more", "+N more"). The thread follows its newest
// line only while the reader is at it: scrolling up, or opening a row near
// the bottom, lets go, and coming back down takes hold again. The live block
// (streaming text, the tool in flight) sits under the items, outside the
// polite live region, so a screen reader hears new items and not every
// streamed token.

import { $, h, clear, clock, dur, cr, ROLE, decisionWord, decisionColor, plural, ref, cardName } from './dom.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { on, state, row } from './store.js?v=__ASSET_V__'
import { draftHero, newSession } from './session.js?v=__ASSET_V__'
import { attachmentURL, get, post, cardPath } from './api.js?v=__ASSET_V__'
import { restoreComposer } from './composer.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { runBlock, runSig, markStep, setTranscriptDrawer } from './workflow.js?v=__ASSET_V__'
import { dispatchBlock, dispatchSig } from './dispatch.js?v=__ASSET_V__'
import { select, shows } from './selection.js?v=__ASSET_V__'

const nodes = new Map() // item key -> { sig, el }
const groups = new Map() // group key -> { el, body, summary }
const folds = new Map() // '<card id> <row key>' -> the person's last-set open state
const foldSet = new WeakMap() // details element -> the open state the page last set on it
const unclamped = new Set() // '<card id> <row key>' the person asked to see whole
let shownFor = null
// following: the reader is at the newest line, and a redraw that adds to
// the thread keeps them there. It is what the reader last did that says
// so, not where a redraw finds the scroll: one redraw's growth would
// otherwise read as the reader having scrolled away before the next.
let following = true

export function initThread () {
  on(['thread', 'sel', 'card', 'sessionDraft', 'runFold'], render)
  // a board opened with nothing selected changes none of those: draw the
  // empty column once now
  queueMicrotask(render)
  // a run the live conversation places among its turns leaves the settled
  // items, and one it no longer places comes back to them
  let placed = ''
  on(['live'], () => {
    const now = liveRunKeys().join(',')
    if (state.thread && (now !== placed || !threadItems(state.thread).length)) render()
    placed = now
  })
  // a session's workflow runs are drawn among its turns (runsAt), so the
  // live block is drawn again when the thread brings a run that moved
  on(['live', 'card', 'board', 'thread', 'runFold', 'outgoing'], renderLive)
  on(['step'], () => markStep($('#thread')))
  // a step opened beside the thread is drawn the way a session's
  // conversation is (DESIGN §25.10)
  setTranscriptDrawer(stepTranscript, reconcile)
  const sc = $('#thread')
  const follow = () => { following = atEnd(sc) }
  // a row the person opened or unclamped near the bottom grew under them:
  // they are reading it, and the next redraw must not scroll it away
  $('#thread').addEventListener('toggle', (e) => { if (recordFold(e.target)) follow() }, true)
  sc.addEventListener('click', (e) => { if (e.target.closest?.('button')) follow() })
  // a thread scrolled up off its newest line offers the way back down
  const btn = $('#to-bottom')
  const showBtn = () => { btn.hidden = atBottom(sc) }
  sc.addEventListener('scroll', () => { follow(); showBtn() }, { passive: true })
  new ResizeObserver(showBtn).observe($('.thread-inner'))
  btn.addEventListener('click', () => {
    sc.scrollTo({ top: sc.scrollHeight, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  })
}

// ---- the person's folds ----
// Once a person toggles a foldable row of the thread, the row keeps their
// open state across every later redraw for the rest of the page session:
// rows are rebuilt wholesale when their content changes, and a rebuilt
// <details> starts closed, so each builder re-applies the remembered state.
// The memory is scoped per card and lives only in this page — a reload
// forgets.
function foldKey (key) { return `${state.sel} ${key}` }

// applyFold marks a foldable row with its identity and applies its open
// state: the person's last-set one when they have touched the row, the
// builder's default otherwise. Rows the person never touched keep the
// defaults the page computes today.
function applyFold (el, key, def) {
  if (!key) return
  el.dataset.fold = key
  const open = folds.get(foldKey(key))
  const next = open === undefined ? !!def : open
  foldSet.set(el, next)
  el.open = next
}

// recordFold is the toggle listener's writer, and says whether the toggle
// was the person's. The page sets rows' open state itself wherever it
// renders — a builder re-applying the memory, the running stage's divider
// pinned open — and notes what it set (foldSet): a toggle that matches it is the
// page's own doing, not the person's, and is never recorded as their
// choice.
function recordFold (d) {
  if (d?.tagName !== 'DETAILS') return false
  if (foldSet.get(d) === d.open) return false
  if (!d.dataset.fold) return true
  if (d.classList.contains('sg') && d.classList.contains('cur')) return false // pinned open by rule
  foldSet.delete(d)
  folds.set(foldKey(d.dataset.fold), d.open)
  return true
}

// atBottom is near enough the newest line not to offer the way back down;
// atEnd is at it, which is what following takes.
function atBottom (sc) { return sc.scrollHeight - sc.scrollTop - sc.clientHeight < 80 }
function atEnd (sc) { return sc.scrollHeight - sc.scrollTop - sc.clientHeight < 24 }

// stickToEnd keeps a following reader at the newest line once a redraw
// has laid out.
function stickToEnd (sc) {
  requestAnimationFrame(() => { if (following) sc.scrollTop = sc.scrollHeight })
}

function render () {
  const box = $('#thread-items')
  const sc = $('#thread')
  if (shownFor !== state.sel) {
    clear(box); nodes.clear(); groups.clear()
    shownFor = state.sel
    following = true
    // the log falls quiet while another card's whole thread fills it (not
    // news to announce), and speaks again once that fill has landed
    box.setAttribute('aria-live', 'off')
  } else if (state.thread && box.getAttribute('aria-live') === 'off') {
    const filled = shownFor
    setTimeout(() => { if (shownFor === filled && state.thread) box.setAttribute('aria-live', 'polite') }, 300)
  }
  const t = state.thread
  const items = threadItems(t)
  // with nothing open and no draft, there is nobody to write to: the
  // composer is put away (app.css) and the column offers the first step
  document.getElementById('app')?.toggleAttribute('data-nothing-open', !state.sel && !state.sessionDraft)
  if (!state.sel && state.sessionDraft) { placeholder(box, draftHero()); return }
  if (!state.sel) {
    placeholder(box, h('div', { class: 'empty start', testid: 'thread-none' },
      h('b', null, 'Nothing open'),
      'A session is an agent working on a branch of its own while you talk to it. Start one, or pick a card from the list.',
      h('button', { class: 'btn pri', type: 'button', testid: 'thread-new-session', onclick: () => newSession() }, 'New session')))
    return
  }
  if (!t) { placeholder(box, h('div', { class: 'empty', testid: 'thread-loading' }, h('span', { class: 'spinner' }))); return }
  if (!items.length) {
    if (t.gone) {
      placeholder(box, h('div', { class: 'empty', testid: 'thread-gone' }, h('b', null, `${ref(state.sel)} was deleted`),
        state.draft.trim() ? 'What you were writing is still in the composer. Pick another card from the list.' : 'Pick another card from the list.'))
    } else if (t.unavailable) {
      placeholder(box, h('div', { class: 'empty', testid: 'thread-unavailable' }, h('b', null, 'The thread is not available yet'), 'This board’s server does not serve a card’s conversation yet. The head, the decision and the panel still work.'))
    } else if (t.err) {
      placeholder(box, h('div', { class: 'empty err', testid: 'thread-error' }, h('b', null, 'The thread did not load'), t.err.message))
    } else if (talking(state.live) || items.length !== t.items.length) {
      // a conversation with no stage history (a freeform card, a consult)
      // is the live block's to draw; "nothing has happened" would be wrong
      placeholder(box, null)
    } else {
      const r = state.card || row(state.sel)
      placeholder(box, h('div', { class: 'empty', testid: 'thread-empty' }, h('b', null, `${cardName(state.sel)} has not started`),
        r?.waits?.length ? `It waits on ${r.waits.join(', ')}, which has to land first.` : 'Nothing has happened on this card yet.'))
    }
    return
  }
  box.querySelector(':scope > .empty')?.remove()

  // group items under the stage divider before them
  const gs = []
  let g = { key: '__pre', stage: null, items: [] }
  for (const it of items) {
    if (it.t === 'stage') {
      if (g.stage || g.items.length) gs.push(g)
      g = { key: it.key, stage: it, items: [] }
    } else g.items.push(it)
  }
  gs.push(g)
  // the open decision is pinned above the composer; its raising is not
  // repeated in the thread until it has an answer
  // (a stop's log id is its ref and the moment it was raised,
  // "gate:FD-001:plan:<ns>", and its log line — "stopped early at …" — is
  // not the pinned question, so the ref is matched as a prefix)
  //
  // The log's open decision is the stop the card waits on now (threadfold
  // places only that one), so one raised at the card's current stage is
  // the pinned stop even when the log words it differently ("reached the
  // landing gate" under "verification passed"). A chip pinned over a stop
  // is a question of its own, and leaves the stop's raising where it is.
  const pinned = state.card?.decision
  if (pinned) {
    const same = (id) => !!id && (id === pinned.ref || id.startsWith(pinned.ref + ':'))
    const stop = pinned.kind !== 'confirm' && pinned.kind !== 'ask'
    for (const grp of gs) {
      grp.items = grp.items.filter(it => !(it.t === 'decision' && !it.decision?.answer &&
        (same(it.decision?.id) || it.decision?.question === pinned.question ||
          (stop && it.decision?.kind !== 'ask' && it.stage === state.card.stage))))
    }
  }

  const keepGroups = new Set()
  const keepItems = new Set()
  gs.forEach((grp, gi) => {
    const last = gi === gs.length - 1
    const gnode = groupNode(grp, last)
    keepGroups.add(grp.key)
    place(box, gnode.el, gi)
    grp.items.forEach((it, ii) => {
      // the activity a running stage is still adding to stays open between
      // its calls
      const n = itemNode(it, last && ii === grp.items.length - 1 && !!state.live?.busy)
      keepItems.add(it.key)
      place(gnode.body, n, ii)
    })
    while (gnode.body.children.length > grp.items.length) gnode.body.lastChild.remove()
  })
  while (box.children.length > gs.length) box.lastChild.remove()
  for (const k of [...groups.keys()]) if (!keepGroups.has(k)) groups.delete(k)
  for (const k of [...nodes.keys()]) if (!keepItems.has(k)) nodes.delete(k)
  markStep(box)
  stickToEnd(sc)
}

// threadItems is what the thread's own list draws: every item but a
// workflow run's block the live conversation places among its turns
// (renderLive). A run proposed before the turns it carries stands here,
// where the server's fold put it among the settled items.
function threadItems (t) {
  const live = new Set(liveRunKeys())
  return (t?.items || []).filter(it => it.t !== 'run' || !live.has(it.key))
}

// liveRunKeys are the runs the live conversation places among its turns,
// by their items' keys.
function liveRunKeys () {
  return (state.live?.freeform?.runs || []).map(p => `run:${p.n}`)
}

// runsAt is where the server placed each of the session's runs among the
// live conversation's turns (webapi.Conversation.Runs, threadfold's
// RunPlace): a map of turn index to the blocks that go before it, the
// ones after every turn under the last index. The page places nothing
// itself.
function runsAt (places) {
  const items = new Map((state.thread?.items || []).filter(it => it.t === 'run').map(it => [it.run.n, it]))
  const at = new Map()
  for (const p of places || []) {
    const it = items.get(p.n)
    if (!it) continue
    if (!at.has(p.before)) at.set(p.before, [])
    at.get(p.before).push(cached(`run:${p.n}`, runSig(it), () => runBlock(it)))
  }
  return at
}

function talking (l) {
  if (!l) return false
  if (l.busy || l.streaming || l.turns?.length) return true
  return ['consult', 'freeform'].some(k => l[k] && (l[k].turns?.length || l[k].streaming || l[k].busy || l[k].sending))
}

function placeholder (box, el) {
  clear(box); nodes.clear(); groups.clear()
  if (el) box.append(el)
}

// place puts el at position i of parent, moving it only when it is not there.
function place (parent, el, i) {
  if (parent.children[i] !== el) parent.insertBefore(el, parent.children[i] || null)
}

function groupNode (grp, last) {
  let g = groups.get(grp.key)
  if (!grp.stage) {
    if (!g) {
      const el = h('div', { class: 'sg-body', data: { key: grp.key } })
      g = { el, body: el }
      groups.set(grp.key, g)
    }
    return g
  }
  const st = grp.stage
  if (!g) {
    const summary = h('summary')
    const body = h('div', { class: 'sg-body' })
    const el = h('details', { class: ['sg', `st-${st.stage}`], data: { key: grp.key, stage: st.stage }, testid: `stage-group-${st.stage}` }, summary, body)
    g = { el, body, summary }
    groups.set(grp.key, g)
  }
  const verdict = st.verdict
  const receipt = [...grp.items].reverse().find(i => i.t === 'receipt')
  const count = grp.items.length
  const bits = last
    ? [st.role && st.role !== st.stage ? st.role : (st.role ? '' : ROLE[st.stage] || ''), st.model, st.exited && verdict ? verdict : null].filter(Boolean)
    : [plural(count, 'event'), verdict || receipt?.receipt?.text || receipt?.text, st.credits ? cr(st.credits) : null].filter(Boolean)
  // a freeform card's one conversation is headed as its live block is
  const stageName = st.stage === 'open' ? 'session' : st.stage
  const name = st.flavor && !/^(stage|work)$/.test(st.flavor) ? `${stageName} · ${st.flavor}` : stageName
  // the summary is drawn again only when what it says moved: a summary
  // rebuilt under the pointer or the focus loses both
  const sig = JSON.stringify([name, bits, st.outcome || '', last])
  if (g.sig !== sig) {
    g.sig = sig
    clear(g.summary).append(h('div', { class: 'ev-stage' },
      last ? null : h('span', { class: 'fold', 'aria-hidden': 'true' }, '▸'),
      h('b', null, name),
      st.outcome ? h('span', { class: st.outcome === 'ok' ? 'okc' : 'badc', 'aria-label': st.outcome === 'ok' ? 'passed' : 'failed' }, st.outcome === 'ok' ? '✓' : '✕') : null,
      h('span', { class: 'sum' }, bits.join(' · '))))
    g.summary.setAttribute('aria-label', `${name} stage${last ? '' : ', folded'}: ${bits.join(', ')}`)
  }
  g.el.classList.toggle('cur', last)
  if (last) {
    g.el.open = true // the running stage's divider is pinned open by rule
    foldSet.set(g.el, true)
  } else {
    applyFold(g.el, grp.key, false)
  }
  if (last) g.summary.tabIndex = -1; else g.summary.removeAttribute('tabindex')
  return g
}

function itemNode (it, live = false) {
  live = live && it.t === 'activity'
  // a run's block moves without an event, and with the page's own fold of it
  const sig = it.t === 'run' ? `run|${runSig(it)}` : it.t === 'dispatch' ? `dispatch|${dispatchSig(it)}` : `${it.seq}|${it.t}|${it.decision?.answer || ''}|${state.card?.files?.url || ''}|${live}`
  const have = nodes.get(it.key)
  if (have && have.sig === sig) return have.el
  const el = itemEl(it, live)
  el.dataset.key = it.key
  el.dataset.type = it.t
  if (!el.dataset.testid) el.dataset.testid = 'thread-item'
  if (have) have.el.replaceWith(el)
  nodes.set(it.key, { sig, el })
  return el
}

// itemEl draws one thread item.
export function itemEl (it, live = false) {
  switch (it.t) {
    case 'message': return message(it)
    case 'you': return you(it)
    case 'activity': return activity(it.items || [], it.key, live)
    case 'receipt': return receipt(it)
    case 'verify': return verify(it)
    case 'decision': return decision(it)
    case 'note': return h('div', { class: 'note-ev' }, h('span', null, it.text), h('span', { class: 'when' }, clock(it.time)))
    case 'run': return runBlock(it)
    case 'dispatch': return dispatchBlock(it, (card) => select(card))
    default: return h('div', { class: 'note-ev' }, it.text || it.t)
  }
}

function firstLine (text) {
  const l = String(text || '').split('\n').find(x => x.trim()) || ''
  return l.length > 110 ? l.slice(0, 108) + '…' : l
}

// md renders a message, linking the paths it names under the card's
// worktree to the server's copy (webapi.Files).
function md (text) { return markdown(text, { files: state.card?.files }) }

function avatarFor (role) { return (role || 'ag').slice(0, 2).toUpperCase() }

function message (it) {
  if (it.author === 'gummi') {
    const body = h('details', { class: 'body prompt' }, h('summary', null, firstLine(it.text)), md(it.text))
    applyFold(body, it.key, false)
    return h('div', { class: 'msg gummi' },
      h('div', { class: 'av you', 'aria-hidden': 'true' }, 'g'),
      h('div', null,
        h('div', { class: 'who' }, h('b', null, 'gummi'), h('span', { class: 'mono' }, clock(it.time))),
        body))
  }
  if (it.author === 'you') return you(it)
  const role = it.author || it.role || ROLE[it.stage] || 'agent'
  // a consult answer sits where it was asked, and says it steered nothing;
  // it claims no read-only here, since the log does not record whether
  // the backend that answered could confine it
  const consult = it.via === 'consult'
  return h('div', { class: ['msg reply', it.stage && `st-${it.stage}`], testid: consult ? 'thread-consult' : 'reply' },
    h('div', { class: 'who' }, h('b', null, role),
      consult ? h('span', { class: 'via' }, 'consult') : null,
      it.flavor && it.flavor !== 'work' ? h('span', { class: 'via' }, it.flavor) : null),
    h('div', { class: 'body' }, md(it.text)),
    replyFoot(it))
}

// replyFoot is the line under an agent's reply: what it ran on, when it
// was written and how long the turn took, and a copy of its words.
function replyFoot (it) {
  return h('div', { class: 'foot', testid: 'reply-foot' },
    it.model ? h('span', { class: 'mono', testid: 'reply-model' }, it.model) : null,
    it.ms ? h('span', { class: 'mono', testid: 'reply-took', title: 'how long the turn took' }, dur(it.ms)) : null,
    it.time ? h('span', { class: 'mono' }, clock(it.time)) : null,
    copyButton(it.text, 'Copy reply', 'reply-copy'))
}

// copyButton puts text on the clipboard and says so in place.
function copyButton (text, title, testid) {
  const b = h('button', { type: 'button', class: 'copy', title, 'aria-label': title, testid }, 'copy')
  b.addEventListener('click', async (e) => {
    e.stopPropagation()
    e.preventDefault()
    try {
      await navigator.clipboard.writeText(String(text || ''))
      b.textContent = 'copied'
    } catch { b.textContent = 'copy failed' }
    setTimeout(() => { b.textContent = 'copy' }, 1500)
  })
  return b
}

// you is a line a person typed. It is headed with the name it was sent
// with, and "you" when it came with none (the terminal's own lines) —
// never with the viewer's own name, which would put one person's words
// in another's mouth on a board several people share. A turn a workflow
// run sent (its report, DESIGN §25.7) — or an old crew — is gummi's, never
// the person's: it is drawn on the other side, headed as the workflow.
function you (it) {
  if (it.by === 'review') return reviewSent(it)
  const run = it.by === 'workflow' || it.by === 'crew'
  const who = it.by || 'you'
  const text = String(it.text || '')
  // a long prompt is cut to a few lines, with a way to read it whole —
  // which stays whole when the bubble is drawn again
  const long = text.split('\n').length > 12 || text.length > 900
  const whole = long && !!it.key && unclamped.has(foldKey(it.key))
  const body = h('div', { class: ['body', long && !whole && 'clamped'] }, md(text))
  const more = long
    ? h('button', {
        type: 'button',
        class: 'more',
        testid: 'bubble-more',
        onclick: () => {
          const clamped = body.classList.toggle('clamped')
          more.textContent = clamped ? 'show more' : 'show less'
          if (it.key) unclamped[clamped ? 'delete' : 'add'](foldKey(it.key))
        }
      }, whole ? 'show less' : 'show more')
    : null
  return h('div', { class: ['msg', run ? 'runturn' : 'mine'], testid: run ? 'thread-workflow' : it.via === 'consult' ? 'thread-consult' : 'bubble' },
    h('div', { class: 'bubble' }, body, more, attachmentThumbs(it.attachments)),
    h('div', { class: 'who' },
      it.via ? h('span', { class: 'via' }, it.via) : null,
      h('b', { title: run ? `sent by the session’s ${it.by}` : 'who sent it' }, who),
      it.time ? h('span', { class: 'mono' }, clock(it.time)) : null,
      copyButton(text, 'Copy message', 'bubble-copy'),
      it.rewind ? h('button', { type: 'button', class: 'rewind', testid: 'turn-rewind', title: 'Take the conversation back to before this message and edit it. The branch keeps its commits.', onclick: it.rewind }, 'rewind') : null))
}

// reviewSent is the person's diff comments as their session got them
// (Request changes): the person's act, said as one line — how many
// comments went — with what the agent was sent a click away, since its
// words are gummi's instruction to the agent, not the person's.
function reviewSent (it) {
  const text = String(it.text || '')
  const n = text.split('\n').filter(l => /^- /.test(l)).length
  return h('div', { class: 'msg mine review', testid: 'thread-review' },
    h('details', { class: 'bubble' },
      h('summary', null, `Asked for changes on ${n} diff comment${n === 1 ? '' : 's'}`),
      h('div', { class: 'body' }, md(text))),
    h('div', { class: 'who' },
      h('b', { title: 'sent from the diff' }, 'you'),
      it.time ? h('span', { class: 'mono' }, clock(it.time)) : null))
}

// attachmentThumbs renders a you turn's images as thumbnails linking to
// GET /api/attachments/{id}, the same URL their src loads from — nothing
// here is trusted markup, only img tags built from server-named ids.
function attachmentThumbs (refs) {
  if (!refs || !refs.length) return null
  return h('div', { class: 'thumbs' }, refs.map((r) =>
    h('a', { class: 'thumb', href: attachmentURL(r.id), target: '_blank', rel: 'noopener' },
      h('img', { src: attachmentURL(r.id), alt: r.name || 'attached image', loading: 'lazy' }))))
}

// TOOL_KIND sorts a tool by what it does, for the row's mark and verb.
const TOOL_KIND = [
  [/^(edit|write|str_replace|apply_patch|patch|multiedit|create|notebookedit)/i, 'edit', '✎', 'Edited'],
  [/^(read|view|cat|open)/i, 'read', '◱', 'Read'],
  [/^(grep|glob|search|find|ls|list|rg)/i, 'search', '⌕', 'Searched'],
  [/^(bash|shell|exec|run|command|terminal)/i, 'shell', '›_', 'Ran'],
  [/^(web|fetch|http|browse)/i, 'web', '⇄', 'Fetched'],
  [/^(todo|task)/i, 'task', '☐', 'Updated tasks'],
  [/^(ask|question)/i, 'ask', '?', 'Asked']
]
function toolKind (tool) {
  const t = String(tool || '')
  const k = TOOL_KIND.find(([re]) => re.test(t))
  return k ? { kind: k[1], mark: k[2], verb: k[3] } : { kind: 'other', mark: '◇', verb: t || 'tool' }
}

// toolRow is one call: its mark, a verb, what it touched, how long it took.
// A call with output opens to show it (with a copy); one that touched a
// file says which, so it can be found in the Diff tab.
function toolRow (t, fold) {
  const label = String(t.label || '').replace(/\s+/g, ' ').trim()
  const named = label.startsWith(t.tool + ' ') || label === t.tool
  const rest = named ? label.slice(t.tool.length).trim() : label
  const target = rest === t.detail || rest.includes(t.detail || '\0') ? rest : [rest, t.detail].filter(Boolean).join(' ')
  const k = /^gummi \//.test(t.tool) ? { kind: 'gummi', mark: 'g', verb: t.tool.slice(6) } : toolKind(t.tool)
  const head = [
    t.parent ? h('span', { class: 'sub', title: 'by a subagent' }, '↳') : null,
    h('span', { class: 'mark', 'aria-hidden': 'true' }, k.mark),
    h('b', null, k.kind === 'other' ? t.tool : k.verb),
    h('span', { class: 'target', title: target }, target),
    t.status === 'running' ? h('span', { class: 'spinner sm' }) : null,
    t.ms ? h('span', { class: 'ms' }, dur(t.ms)) : null
  ]
  const cls = ['tool', `k-${k.kind}`, t.status || null, t.parent ? 'by-sub' : null]
  if (!t.output) return h('li', { class: cls, testid: 'tool-row' }, h('div', { class: 'thead' }, ...head))
  const out = String(t.output)
  const cut = out.startsWith('…(truncated)')
  const el = h('details', { class: 'tcall' },
    h('summary', { class: 'thead' }, ...head),
    h('div', { class: 'tout' },
      h('div', { class: 'tbar' }, cut ? h('span', { class: 'cut' }, 'truncated') : null, copyButton(out, 'Copy output', 'tool-copy')),
      h('pre', { class: ['tool-out', t.status === 'fail' && 'fail'], testid: 'tool-output' }, out)))
  // gummi's own answer to a session command (/cost, /context…) is what
  // was asked for: shown, not folded behind its row
  applyFold(el, fold, t.status === 'fail' || /^gummi \//.test(t.tool))
  return h('li', { class: cls, testid: 'tool-row' }, el)
}

// ACTIVITY_SHOWN is how many of an activity's newest steps show before
// the older ones fold into "+N more".
const ACTIVITY_SHOWN = 6

// activity is everything the agent did between two messages — its tool calls
// and thoughts, in order — as a list of steps. It is open while the work is
// going on in it (a call in flight, or live: the run a busy session is still
// adding to) and folds to its summary once that is over: what was done is
// one line, and the reply under it is what a person came to read. The
// folded line ends on the last step, so it says where the work got to.
// fold is the row's identity, what the person's fold is remembered by.
export function activity (items, fold, live = false) {
  const calls = items.flatMap(i => i.tools || [])
  const thoughts = items.filter(i => i.t === 'message').length
  const fails = calls.filter(t => t.status === 'fail').length
  const inFlight = calls.find(t => t.status === 'running')
  // a Monitor watch (webapi.StatusWatching) never settles on its own the
  // way an ordinary call does, so it earns its own word and a spinner that
  // doesn't spin: there, but not "busy", so a reply stays obviously safe
  // to send.
  const watching = calls.some(t => t.status === 'watching')
  const parts = [h('b', null, 'Activity')]
  if (calls.length) parts.push(plural(calls.length, 'tool call'))
  if (fails) parts.push(h('span', { class: 'fails' }, `${fails} failed`))
  if (thoughts) parts.push(plural(thoughts, 'thought'))
  // a call in flight is said once, by the run's working line under the
  // thread (and its own row, spinning): the summary does not repeat it
  if (watching) parts.push(h('span', { class: 'watching' }, 'watching'))
  else if (!inFlight && calls.length) parts.push(h('span', { class: 'last', testid: 'activity-last' }, lastStep(calls[calls.length - 1])))
  // each step's fold is keyed by its place in the run, which only grows
  let n = 0
  const steps = items.flatMap(it => {
    if (it.t === 'tools') return (it.tools || []).map(t => toolRow(t, fold && `${fold}#${n++}`))
    const d = h('details', { class: 'tcall' },
      h('summary', { class: 'thead' }, h('span', { class: 'mark', 'aria-hidden': 'true' }, '∴'), h('b', null, 'Thought'),
        h('i', { class: 'target' }, firstLine(it.text))),
      h('div', { class: 'thought-body' }, md(it.text)))
    applyFold(d, fold && `${fold}#${n++}`, false)
    return [h('li', { class: 'tool thought', testid: 'thought-row' }, d)]
  })
  // the older steps a person asked for stay shown as the run grows
  const all = !!fold && unclamped.has(foldKey(fold))
  const hidden = !all && steps.length > ACTIVITY_SHOWN ? steps.slice(0, steps.length - ACTIVITY_SHOWN) : []
  const list = h('ol', { class: 'steps' }, ...steps.slice(hidden.length))
  if (hidden.length) {
    const more = h('li', { class: 'more-steps' }, h('button', {
      type: 'button',
      testid: 'activity-more',
      onclick: () => { if (fold) unclamped.add(foldKey(fold)); more.replaceWith(...hidden) }
    }, `+${hidden.length} more…`))
    list.prepend(more)
  }
  const el = h('details', { class: 'tools activity', testid: 'activity' },
    h('summary', null, parts.flatMap((p, i) => i ? [' · ', p] : [p])),
    h('div', { class: 'body' }, list))
  applyFold(el, fold, live || watching || !!inFlight)
  return el
}

// lastStep is a call in the words its row uses: the verb and what it
// touched.
function lastStep (t) {
  const label = String(t.label || '').replace(/\s+/g, ' ').trim()
  const rest = label.startsWith(t.tool + ' ') || label === t.tool ? label.slice(t.tool.length).trim() : label
  const k = /^gummi \//.test(t.tool) ? { kind: 'gummi', verb: t.tool.slice(6) } : toolKind(t.tool)
  return [k.kind === 'other' ? t.tool : k.verb, rest || t.detail].filter(Boolean).join(' ')
}

function receipt (it) {
  const r = it.receipt || { ok: true, text: it.text }
  const by = r.by || it.by
  // a park stopped the card where it was: it was neither done nor sent
  // back, and says so with a mark of its own
  const park = r.kind === 'park'
  return h('div', { class: 'receipt', testid: 'receipt' },
    park
      ? h('span', { class: 'park', 'aria-label': 'parked' }, '‖')
      : h('span', { class: r.ok ? 'ok' : 'bad', 'aria-label': r.ok ? 'done' : 'sent back' }, r.ok ? '✓' : '↺'),
    h('span', null, r.text, by && !String(r.text || '').includes(by) ? ` · ${by}` : ''),
    h('span', { class: 'when' }, clock(it.time)))
}

export function checksList (checks) {
  return h('div', { class: 'checks' }, checks.map(k => h('div', { testid: `check-${k.name}` },
    h('span', { class: k.ok ? 'okc' : 'badc', 'aria-label': k.ok ? 'passed' : 'failed' }, k.ok ? '✓' : '✕'),
    h('span', { class: 'n', title: k.name }, k.name),
    h('span', { class: 'c', title: k.cmd || '' }, k.cmd || ''),
    h('span', { class: 'd', title: k.status || null }, k.ms ? dur(k.ms) : (k.ok ? '' : (k.status || ''))),
    !k.ok && k.output ? h('pre', { tabindex: '0', 'aria-label': `${k.name} output` }, k.output) : null)))
}

function verify (it) {
  const checks = it.checks || []
  const fails = checks.filter(k => !k.ok).length
  return h('div', { class: 'msg st-verify', testid: 'verify' },
    h('div', { class: 'av agent', 'aria-hidden': 'true', testid: 'verify-avatar' }, avatarFor('verify')),
    h('div', null,
      h('div', { class: 'who' }, h('b', null, 'verify'), h('span', { class: 'mono' }, clock(it.time)),
        // zero checks is not "all passed": gummi ran nothing (the item's
        // text says why), and the verdict rests on the reviewer's own runs
        checks.length
          ? h('span', { class: fails ? 'badc' : 'okc' }, fails ? `${fails} failed` : 'all passed')
          : h('span', { class: 'badc', testid: 'verify-no-checks' }, 'no checks')),
      it.text ? h('div', { class: 'body' }, md(it.text)) : null,
      checks.length ? checksList(checks) : null))
}

function decision (it) {
  const d = it.decision || { kind: 'gate', question: it.text }
  const stage = it.stage || state.card?.stage
  return h('div', { class: 'ev-dec', style: { '--dc': decisionColor(d, stage) }, testid: 'thread-decision' },
    h('span', { class: 'k' }, decisionWord(d, stage)),
    h('span', null, d.question),
    d.answer ? h('span', { class: 'a' }, '→ ', h('b', null, d.answer), d.by ? ` · ${d.by}` : '') : null)
}

// ---- the live block ----
// What the card's log does not hold yet: the running stage session's
// transcript (turns), the message still streaming, the tool in flight, and
// the card's other conversations (consult, freeform), which never reach the
// log. Every part is kept by key and drawn again only when what it says
// moved, and the block is reconciled in place rather than refilled: a
// streaming delta redraws only the tail, and a board change elsewhere
// redraws nothing — a part that was taken out and put back would lose the
// reader's selection in it and the scroll of its output.
const turnNodes = new Map()
let turnsFor = null
// A live conversation sends only its newest turns and how many came before
// them (omitted). The earlier ones a person asked for are kept here, per
// conversation: from, the first one's number, and turns, the run of them
// up to where the block starts — kept that way as the block slides on,
// cut back when the conversation is (a rewind, a clear), and let go when
// the stage run they belong to is replaced by another.
const earlier = new Map() // kind -> { from, turns, session }
const fetching = new Set() // kinds with an earlier read on the way

function renderLive () {
  const box = $('#thread-live')
  const r = state.card || row(state.sel)
  const l = state.live
  if (turnsFor !== state.sel) { turnNodes.clear(); earlier.clear(); turnsFor = state.sel }
  const parts = []
  // the session's runs the server placed among its turns; the rest stand
  // among the settled items (threadItems)
  let runs = null
  if (r) {
    const pausing = r.running?.pausing
    const role = l?.role || ROLE[l?.stage || r.stage] || 'agent'
    const stage = l?.stage || r.stage
    if (l?.elsewhere) {
      const e = l.elsewhere
      parts.push(cached('elsewhere', JSON.stringify(e), () => h('div', { class: 'live', testid: 'live-elsewhere' }, e.busy ? h('span', { class: 'spinner' }) : idleMark(),
        h('span', null, e.note || `Another gummi (pid ${e.pid}) is running ${[e.stage, e.role].filter(Boolean).join(' · ')}; this board watches it.`))))
    } else if (!l && r.elsewhere) {
      parts.push(cached('elsewhere', '', () => h('div', { class: 'live', testid: 'live-elsewhere' }, idleMark(), h('span', null, 'Another gummi is driving this card; this board watches it.'))))
    }
    if (l) {
      parts.push(...conversation('stage', l, role, stage))
      if (l.busy) {
        const words = [`${role} is ${l.verb || 'working'}`, l.tool ? String(l.tool.label || l.tool.tool).replace(/\s+/g, ' ') : null].filter(Boolean).join(' · ') +
          (pausing ? ' · pauses after this turn' : '')
        parts.push(busyLine('live', 'live', words, l.spent ? cr(l.spent) : null))
      }
      // a failure the pinned decision already states (a failed stage's
      // stop quotes it) is not said a second time under the thread
      if (l.err && !String(state.card?.decision?.question || '').includes(l.err.trim())) parts.push(cached('stage:err', l.err, () => h('div', { class: 'live badc', testid: 'live-error' }, l.err)))
      for (const k of ['consult', 'freeform']) {
        const c = l[k]
        if (!c || !(c.turns?.length || c.streaming || c.busy || c.sending || c.err)) continue
        // a consult says what it may do: read-only, or the engine's notice
        // when the backend it runs on cannot confine it
        const label = k === 'freeform' ? 'session' : c.notice ? 'consult · not confined' : 'consult · read-only'
        // headed as the settled turns are: the conversation, then its agent
        const role = c.role || 'implementer'
        parts.push(cached(`${k}:head`, `${label}|${role}`, () => h('div', { class: ['ev-stage', k === 'freeform' ? 'st-open' : 'st-todo'], testid: `live-${k}-head` }, h('b', null, label),
          k === 'freeform' ? h('span', { class: 'sum' }, role) : null)))
        if (c.notice) parts.push(cached(`${k}:notice`, c.notice, () => h('div', { class: 'live warnc', testid: `live-${k}-notice` }, c.notice)))
        if (k === 'freeform') runs = runsAt(c.runs)
        parts.push(...talk(k, c, {
          role: c.role || (k === 'freeform' ? 'implementer' : 'consult'),
          stage: k === 'freeform' ? 'open' : null,
          runs: k === 'freeform' ? runs : null,
          busy: `live-${k}`,
          own: k === 'freeform'
        }))
      }
    } else if (r.status === 'running') {
      parts.push(busyLine('live', 'run', `${ROLE[r.stage] || 'agent'} is ${r.running?.verb || 'working'}${pausing ? ' · pauses after this turn' : ''}`, null))
    }
  }
  // the line the person just sent, until a live read draws it (or the
  // server says it went elsewhere): one the server already shows on its
  // way, or as its newest line, is not drawn twice
  const o = state.outgoing
  if (o && o.id === state.sel && !shows(l, o.text)) {
    parts.push(cached('outgoing', o.text, () => you({ text: o.text, via: 'sending' })))
  }
  const sc = $('#thread')
  reconcile(box, parts)
  markStep(box)
  stickToEnd(sc)
}

// talk is a conversation as the thread draws it under its head: its
// turns (runs placed among them), the line on its way to it,
// the working line while it works — what it is doing, the call in flight,
// what it has cost and how full it is — and its error. own marks the
// card's own session, whose watches, objective and queued lines are the
// card's to act on.
function talk (key, c, { role, stage, runs = null, busy, own = false }) {
  const parts = conversation(key, c, role, stage, runs)
  if (c.sending) parts.push(cached(`${key}:sending`, c.sending, () => you({ text: c.sending, via: 'sending' })))
  if (own && c.watches?.length) parts.push(cached(`${key}:watches`, JSON.stringify(c.watches), () => watches(c.watches)))
  if (own && c.objective) parts.push(cached(`${key}:objective`, JSON.stringify(c.objective), () => objectiveStrip(state.sel, c.objective)))
  if (own && c.queued?.length) parts.push(cached(`${key}:queued`, JSON.stringify(c.queued), () => queued(state.sel, c.queued)))
  if (c.busy) {
    const words = [c.verb || 'thinking', c.tool ? String(c.tool.label || c.tool.tool).replace(/\s+/g, ' ') : null].filter(Boolean).join(' · ')
    const ctx = c.context?.limit ? `${Math.round(100 * c.context.tokens / c.context.limit)}% context` : null
    parts.push(busyLine(busy, `${key}:busy`, words, [c.spent ? cr(c.spent) : null, ctx].filter(Boolean).join(' · ') || null))
  }
  if (c.err) parts.push(cached(`${key}:err`, c.err, () => h('div', { class: 'live badc' }, c.err)))
  return parts
}

// stepTranscript is a workflow step's own conversation as the step panel
// draws it: the session's conversation drawn by the same hand (talk), so
// the two read the same (DESIGN §25.10).
function stepTranscript (card, l) {
  const c = l?.freeform
  if (!c) return []
  return talk(`step:${card}`, c, { role: c.role || 'step', stage: 'open', busy: 'step-busy' })
}

// reconcile makes parent's children els, in order, moving or taking out
// only the nodes that are not already where they belong.
function reconcile (parent, els) {
  els.forEach((el, i) => place(parent, el, i))
  while (parent.children.length > els.length) parent.lastChild.remove()
}

// busyLine is a run's working line: a spinner, what it is doing, and what
// it has cost. It keeps its node while it is busy, its words changed in
// place, so the spinner does not start over on every delta.
function busyLine (testid, key, words, spent) {
  const el = cached(`${key}:line`, testid, () => h('div', { class: 'live', testid },
    h('span', { class: 'spinner' }), h('span', { class: 'shimmer' }), h('span', { class: 'spent' })))
  const [, w, sp] = el.children
  if (w.textContent !== words) w.textContent = words
  if (sp.textContent !== (spent || '')) sp.textContent = spent || ''
  sp.hidden = !spent
  return el
}

// idleMark stands where a spinner would for a watched run with nothing in
// flight (a parked or waiting card another gummi drives): a still ring
// reads as a spinner that froze.
function idleMark () {
  return h('span', { class: 'idle', 'aria-hidden': 'true' }, '◦')
}

// rewind takes a freeform conversation back to before one of the person's
// messages and puts it in the composer. Only the conversation: the branch
// keeps what was committed since, and the session says so.
async function rewind (id, back) {
  try {
    const r = await post(cardPath(id, 'rewind'), { back })
    restoreComposer(r.text)
    toast('Rewound — edit and send. The branch keeps its commits.')
  } catch (e) { toast(e.message) }
}

// queued is what was said while the agent was mid-turn, waiting to go as
// its next turn: each line can be taken back to edit, or dropped.
function queued (id, lines) {
  const take = async (i, edit) => {
    try {
      const r = await post(cardPath(id, `queue/${i}/take`))
      if (edit) restoreComposer(r.text)
    } catch (e) { toast(e.message) }
  }
  return h('div', { class: 'queued', testid: 'queued' },
    ...lines.map((text, i) => h('div', { class: 'queued-line' },
      h('span', { class: 'via' }, 'queued'), h('span', { class: 'text' }, text),
      h('button', { type: 'button', title: 'Edit', testid: 'queued-edit', onclick: () => take(i, true) }, 'edit'),
      h('button', { type: 'button', title: 'Cancel', testid: 'queued-cancel', onclick: () => take(i, false) }, '×'))))
}

// objectiveStrip is the session's objective (DESIGN §19.11): where it
// stands, what it is, the auditor's last note, and the verbs that act on
// it — each the session's own /objective command, sent as its menu row.
function objectiveStrip (id, o) {
  const act = async verb => {
    try {
      await post(cardPath(id, 'actions/objective'), { message: verb })
    } catch (e) { toast(e.message) }
  }
  const verbs = o.state === 'active' ? ['pause', 'stop', 'clear'] : o.state === 'paused' ? ['resume', 'clear'] : ['clear']
  const head = [o.state, `${o.turns}/${o.cap} turns`, o.auditing ? 'auditing…' : null].filter(Boolean).join(' · ')
  return h('div', { class: ['objective', `obj-${o.state}`], testid: 'objective' },
    h('div', { class: 'objective-head' },
      h('span', { class: 'via' }, 'objective'), h('span', { class: 'obj-state', testid: 'objective-state' }, head),
      ...verbs.map(v => h('button', { type: 'button', testid: `objective-${v}`, onclick: () => act(v) }, v))),
    h('div', { class: 'obj-text' }, o.text),
    o.check ? h('div', { class: 'obj-note' }, `check: ${o.check}`) : null,
    o.note ? h('div', { class: 'obj-note', testid: 'objective-note' }, o.note) : null)
}

// watches are the gummi watches the session has running; each reports
// back to the agent as a turn of its own.
function watches (lines) {
  return h('div', { class: 'queued', testid: 'watches' },
    ...lines.map(text => h('div', { class: 'queued-line watch' },
      h('span', { class: 'via' }, 'watching'), h('span', { class: 'text' }, text))))
}

// activityItems shapes a live run of tool and thinking turns the way the
// server shapes a settled activity item.
function activityItems (run) {
  const items = []
  for (const t of run) {
    if (!t.tool) { items.push({ t: 'message', author: 'thinking', text: t.text }); continue }
    const last = items[items.length - 1]
    if (last?.t === 'tools') last.tools.push(t.tool)
    else items.push({ t: 'tools', tools: [t.tool] })
  }
  return items
}

// conversation draws a session's settled turns (each run of tool calls and
// thoughts folded into one activity row) and the message still streaming.
// earlierFor is the earlier turns kept for conversation kind that run
// straight on into its block, or null. A block that slid past them since
// asks for the turns between, and draws without them until those land.
function earlierFor (kind, c) {
  const e = earlier.get(kind)
  if (!e) return null
  const omitted = c.omitted || 0
  if (e.session !== sessionOf(kind) || omitted <= e.from) { earlier.delete(kind); return null }
  const to = e.from + e.turns.length
  if (to > omitted) e.turns = e.turns.slice(0, omitted - e.from)
  if (to < omitted) { loadEarlier(kind, omitted); return null }
  return e
}

function sessionOf (kind) { return kind === 'stage' ? state.live?.session || '' : '' }

// loadEarlier reads the turns of conversation kind before turn before:
// the window above what is drawn, which a person asked for (asked), or
// the gap a sliding block opened below what is kept, which a redraw asks
// for on its own and so must neither redraw nor speak up.
async function loadEarlier (kind, before, asked = false) {
  if (fetching.has(kind)) return
  fetching.add(kind)
  if (asked) renderLive() // the button says it is loading
  const id = state.sel
  const session = sessionOf(kind)
  const sc = $('#thread')
  let grew = false
  try {
    const r = await get(cardPath(id, `live/earlier?conv=${encodeURIComponent(kind)}&before=${before}`))
    if (state.sel !== id || sessionOf(kind) !== session) return
    const e = earlier.get(kind)
    const to = e ? e.from + e.turns.length : 0
    if (!e) {
      earlier.set(kind, { from: r.from, turns: r.turns || [], session })
      grew = true
    } else if (before === e.from) {
      e.turns = (r.turns || []).concat(e.turns)
      e.from = r.from
      grew = true
    } else if (r.from <= to) {
      e.turns = e.turns.concat((r.turns || []).slice(to - r.from))
    } else {
      earlier.delete(kind) // the block ran too far ahead to stitch: asked for again
    }
  } catch (err) {
    if (state.sel === id && asked) toast(`Earlier turns did not load: ${err.message}`)
    // a gap that would not fill is let go rather than asked for on every redraw
    if (!asked) earlier.delete(kind)
  } finally {
    fetching.delete(kind)
  }
  if (state.sel !== id) return
  // turns put above the reader keep what they were reading where it was
  const below = sc.scrollHeight - sc.scrollTop
  renderLive()
  if (grew && !following) sc.scrollTop = sc.scrollHeight - below
}

function conversation (kind, c, role, stage, inserts = null) {
  const out = []
  const head = earlierFor(kind, c)
  const turns = head ? head.turns.concat(c.turns || []) : (c.turns || [])
  // first is the number of turns[0] in the whole conversation, which keys
  // its nodes: a key that moved with the block's window redrew every turn
  // on every one it slid
  const first = head ? head.from : (c.omitted || 0)
  // inserts are keyed by a turn's place in the live window (c.turns): the
  // earlier turns a person loaded sit in front of it
  const off = head ? head.turns.length : 0
  if (first > 0) {
    const busy = fetching.has(kind)
    out.push(cached(`${kind}:earlier`, `${first}|${busy}`, () => h('div', { class: 'earlier-turns' }, h('button', {
      type: 'button',
      testid: `live-${kind}-earlier`,
      disabled: busy,
      onclick: () => loadEarlier(kind, first, true)
    }, busy ? 'Loading earlier turns…' : `Show earlier turns (${first} more)`))))
  }
  let run = []
  let start = 0
  // live is the run a busy session is still adding to: the last one, with
  // nothing said after it yet
  const flush = (live = false) => {
    if (!run.length) return
    const sig = `${live}:` + run.map(t => t.tool ? `${t.tool.status}|${t.tool.label}|${t.tool.ms || 0}|${(t.tool.output || '').length}` : `k|${(t.text || '').length}`).join(';')
    const key = `${kind}:act:${first + start}`
    out.push(cached(key, sig, () => activity(activityItems(run), key, live)))
    run = []
  }
  turns.forEach((t, i) => {
    if (inserts?.has(i - off)) { flush(); out.push(...inserts.get(i - off)) }
    if (t.tool || t.author === 'thinking') {
      if (!run.length) start = i
      run.push(t)
      return
    }
    flush()
    // a freeform session's own messages can be rewound to, between turns:
    // back counts them from the newest, which is how the server names one
    const back = kind === 'freeform' && t.author === 'you' && !c.busy
      ? turns.slice(i).filter(x => x.author === 'you').length
      : 0
    // a reply's turn took from the person's line before it to the reply
    let ms = 0
    if (t.author !== 'you' && t.time) {
      const asked = turns.slice(0, i).reverse().find(x => x.author === 'you')
      if (asked?.time) ms = Math.max(0, new Date(t.time) - new Date(asked.time))
    }
    // back moves with every line sent after this one and with the busy
    // flag: it is set on the bubble's button rather than drawing it again
    const sig = `${t.author}|${(t.text || '').length}|${(t.attachments || []).length}|${state.card?.files?.url || ''}|${c.model || ''}`
    const mkey = `${kind}:${first + i}`
    const el = cached(mkey, sig, () => t.author === 'you'
      ? you({ text: t.text, by: t.by, time: t.time, attachments: t.attachments, key: mkey, rewind: kind === 'freeform' ? (e) => rewind(state.sel, Number(e.currentTarget.dataset.back)) : null })
      : message({ author: t.author === role ? null : t.author, role: t.author === 'gummi' ? null : t.author, stage, text: t.text, key: mkey, time: t.time, ms, model: t.author === 'gummi' ? null : c.model }))
    const rw = t.author === 'you' ? el.querySelector('.rewind') : null
    if (rw) { rw.hidden = !back; rw.dataset.back = String(back) }
    out.push(el)
  })
  flush(!!c.busy && !c.streaming)
  if (inserts?.has(turns.length - off)) out.push(...inserts.get(turns.length - off))
  if (c.streaming) {
    // while it is still being written, the text ends in a caret: "Done:"
    // halfway through is the start of a sentence, not a verdict
    out.push(h('div', { class: ['msg reply live-msg', stage && `st-${stage}`, c.busy && 'writing'], testid: 'live-streaming' },
      // a message left half-written when its session stopped (paused,
      // parked, failed) says it was cut off, not that it is still coming
      h('div', { class: 'who' }, h('b', null, role), c.busy
        ? h('span', { class: 'mono' }, 'writing')
        : h('span', { class: 'mono badc', testid: 'live-interrupted' }, 'interrupted')), h('div', { class: 'body' }, md(c.streaming))))
  }
  return out
}

function cached (key, sig, make) {
  const have = turnNodes.get(key)
  if (have && have.sig === sig) return have.el
  const el = make()
  turnNodes.set(key, { sig, el })
  return el
}

// jumpToStage opens the last segment of a past stage and scrolls to it.
export function jumpToStage (stage) {
  const all = [...document.querySelectorAll(`#thread-items .sg[data-stage="${CSS.escape(stage)}"]`)]
  const d = all.pop()
  if (!d) return
  foldSet.set(d, true)
  d.open = true
  if (d.dataset.fold) folds.set(foldKey(d.dataset.fold), true)
  d.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' })
}
