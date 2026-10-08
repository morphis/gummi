// thread.js — the card's conversation. Items arrive from the server already
// folded (internal/threadfold) and are upserted by key, so an item that grew
// (a tool group gaining calls, a stage divider getting its verdict) is
// redrawn in place rather than appended twice. Items group under the stage
// divider before them; earlier stages fold into one line each and the
// current one stays open. A foldable row a person has toggled keeps their
// open state across those redraws: the toggle is remembered per card and
// each builder re-applies it, for the page session only. The live block
// (streaming text, the tool in flight) sits under the items, outside the
// polite live region, so a screen reader hears new items and not every
// streamed token.

import { $, h, clear, clock, dur, cr, ROLE, decisionWord, decisionColor, plural } from './dom.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { on, state, row } from './store.js?v=__ASSET_V__'
import { draftHero } from './session.js?v=__ASSET_V__'
import { attachmentURL, post, cardPath } from './api.js?v=__ASSET_V__'
import { restoreComposer } from './composer.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'

const nodes = new Map() // item key -> { sig, el }
const groups = new Map() // group key -> { el, body, summary }
const folds = new Map() // '<card id> <row key>' -> the person's last-set open state
const foldSet = new WeakMap() // details element -> the open state the page last set on it
let shownFor = null

export function initThread () {
  on(['thread', 'sel', 'card', 'sessionDraft'], render)
  on(['live'], () => { if (state.thread && !state.thread.items.length) render() })
  on(['live', 'card', 'board'], renderLive)
  $('#thread').addEventListener('toggle', (e) => recordFold(e.target), true)
  // a thread scrolled up off its newest line offers the way back down
  const sc = $('#thread')
  const btn = $('#to-bottom')
  const showBtn = () => { btn.hidden = atBottom(sc) }
  sc.addEventListener('scroll', showBtn, { passive: true })
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

// recordFold is the toggle listener's writer. The page sets rows' open
// state itself wherever it renders — a builder re-applying the memory, the
// running stage's divider pinned open, a task list open while anything is
// left — and notes what it set (foldSet): a toggle that matches it is the
// page's own doing, not the person's, and is never recorded as their
// choice.
function recordFold (d) {
  if (!d?.dataset?.fold) return
  if (d.classList.contains('sg') && d.classList.contains('cur')) return // pinned open by rule
  if (foldSet.get(d) === d.open) return
  foldSet.delete(d)
  folds.set(foldKey(d.dataset.fold), d.open)
}

function atBottom (sc) { return sc.scrollHeight - sc.scrollTop - sc.clientHeight < 80 }

function render () {
  const box = $('#thread-items')
  const sc = $('#thread')
  const stick = shownFor !== state.sel || atBottom(sc)
  if (shownFor !== state.sel) {
    clear(box); nodes.clear(); groups.clear()
    shownFor = state.sel
    // the log falls quiet while another card's whole thread fills it (not
    // news to announce), and speaks again once that fill has landed
    box.setAttribute('aria-live', 'off')
  } else if (state.thread && box.getAttribute('aria-live') === 'off') {
    const filled = shownFor
    setTimeout(() => { if (shownFor === filled && state.thread) box.setAttribute('aria-live', 'polite') }, 300)
  }
  const t = state.thread
  if (!state.sel && state.sessionDraft) { placeholder(box, draftHero()); return }
  if (!state.sel) {
    placeholder(box, h('div', { class: 'empty', testid: 'thread-none' }, h('b', null, 'Nothing open'), 'Start a session, or pick a card from the list.'))
    return
  }
  if (!t) { placeholder(box, h('div', { class: 'empty', testid: 'thread-loading' }, h('span', { class: 'spinner' }))); return }
  if (!t.items.length) {
    if (t.gone) {
      placeholder(box, h('div', { class: 'empty', testid: 'thread-gone' }, h('b', null, `${state.sel} was deleted`),
        state.draft.trim() ? 'What you were writing is still in the composer. Pick another card from the list.' : 'Pick another card from the list.'))
    } else if (t.unavailable) {
      placeholder(box, h('div', { class: 'empty', testid: 'thread-unavailable' }, h('b', null, 'The thread is not available yet'), 'This board’s server does not serve a card’s conversation yet. The head, the decision and the panel still work.'))
    } else if (t.err) {
      placeholder(box, h('div', { class: 'empty err', testid: 'thread-error' }, h('b', null, 'The thread did not load'), t.err.message))
    } else if (talking(state.live)) {
      // a conversation with no stage history (a freeform card, a consult)
      // is the live block's to draw; "nothing has happened" would be wrong
      placeholder(box, null)
    } else {
      const r = state.card || row(state.sel)
      placeholder(box, h('div', { class: 'empty', testid: 'thread-empty' }, h('b', null, `${state.sel} has not started`),
        r?.waits?.length ? `It waits on ${r.waits.join(', ')}, which has to land first.` : 'Nothing has happened on this card yet.'))
    }
    return
  }
  box.querySelector(':scope > .empty')?.remove()

  // group items under the stage divider before them
  const gs = []
  let g = { key: '__pre', stage: null, items: [] }
  for (const it of t.items) {
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
      const n = itemNode(it)
      keepItems.add(it.key)
      place(gnode.body, n, ii)
    })
    while (gnode.body.children.length > grp.items.length) gnode.body.lastChild.remove()
  })
  while (box.children.length > gs.length) box.lastChild.remove()
  for (const k of [...groups.keys()]) if (!keepGroups.has(k)) groups.delete(k)
  for (const k of [...nodes.keys()]) if (!keepItems.has(k)) nodes.delete(k)
  if (stick) requestAnimationFrame(() => { sc.scrollTop = sc.scrollHeight })
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
  clear(g.summary).append(h('div', { class: 'ev-stage' },
    last ? null : h('span', { class: 'fold', 'aria-hidden': 'true' }, '▸'),
    h('b', null, name),
    st.outcome ? h('span', { class: st.outcome === 'ok' ? 'okc' : 'badc', 'aria-label': st.outcome === 'ok' ? 'passed' : 'failed' }, st.outcome === 'ok' ? '✓' : '✕') : null,
    h('span', { class: 'sum' }, bits.join(' · '))))
  g.summary.setAttribute('aria-label', `${name} stage${last ? '' : ', folded'}: ${bits.join(', ')}`)
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

function itemNode (it) {
  const sig = `${it.seq}|${it.t}|${it.decision?.answer || ''}|${state.card?.files?.url || ''}`
  const have = nodes.get(it.key)
  if (have && have.sig === sig) return have.el
  const el = itemEl(it)
  el.dataset.key = it.key
  el.dataset.type = it.t
  if (!el.dataset.testid) el.dataset.testid = 'thread-item'
  if (have) have.el.replaceWith(el)
  nodes.set(it.key, { sig, el })
  return el
}

// itemEl draws one thread item.
export function itemEl (it) {
  switch (it.t) {
    case 'message': return message(it)
    case 'you': return you(it)
    case 'activity': return activity(it.items || [], it.key)
    case 'receipt': return receipt(it)
    case 'verify': return verify(it)
    case 'decision': return decision(it)
    case 'note': return h('div', { class: 'note-ev' }, h('span', null, it.text), h('span', { class: 'when' }, clock(it.time)))
    case 'stretch': return h('div', { class: ['stretch', it.edge], testid: 'stretch', title: it.reason || null },
      h('b', null, it.label || 'autopilot'), [it.tally, it.how].filter(Boolean).join(' · ') || null)
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
// in another's mouth on a board several people share.
function you (it) {
  const who = it.by || 'you'
  const text = String(it.text || '')
  // a long prompt is cut to a few lines, with a way to read it whole
  const long = text.split('\n').length > 12 || text.length > 900
  const body = h('div', { class: ['body', long && 'clamped'] }, md(text))
  const more = long
    ? h('button', { type: 'button', class: 'more', testid: 'bubble-more', onclick: () => { more.textContent = body.classList.toggle('clamped') ? 'show more' : 'show less' } }, 'show more')
    : null
  return h('div', { class: 'msg mine', testid: it.via === 'consult' ? 'thread-consult' : 'bubble' },
    h('div', { class: 'bubble' }, body, more, attachmentThumbs(it.attachments)),
    h('div', { class: 'who' },
      it.via ? h('span', { class: 'via' }, it.via) : null,
      h('b', { title: 'who sent it' }, who),
      it.time ? h('span', { class: 'mono' }, clock(it.time)) : null,
      copyButton(text, 'Copy message', 'bubble-copy'),
      it.rewind ? h('button', { type: 'button', class: 'rewind', testid: 'turn-rewind', title: 'Take the conversation back to before this message and edit it. The branch keeps its commits.', onclick: it.rewind }, 'rewind') : null))
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
    h('span', { class: 'mark', 'aria-hidden': 'true' }, k.mark),
    h('b', null, k.kind === 'other' ? t.tool : k.verb),
    h('span', { class: 'target', title: target }, target),
    t.status === 'running' ? h('span', { class: 'spinner sm' }) : null,
    t.ms ? h('span', { class: 'ms' }, dur(t.ms)) : null
  ]
  const cls = ['tool', `k-${k.kind}`, t.status || null]
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
// and thoughts, in order — as a list of steps, open by default, its summary
// saying what it came to. fold is the row's identity, what the person's
// fold is remembered by.
export function activity (items, fold) {
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
  if (watching) parts.push(h('span', { class: 'watching' }, 'watching'))
  else if (inFlight) parts.push(h('span', { class: 'shimmer' }, String(inFlight.label || inFlight.tool).replace(/\s+/g, ' ').trim()))
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
  const hidden = steps.length > ACTIVITY_SHOWN ? steps.slice(0, steps.length - ACTIVITY_SHOWN) : []
  const list = h('ol', { class: 'steps' }, ...steps.slice(hidden.length))
  if (hidden.length) {
    const more = h('li', { class: 'more-steps' }, h('button', { type: 'button', testid: 'activity-more', onclick: () => more.replaceWith(...hidden) }, `+${hidden.length} more…`))
    list.prepend(more)
  }
  const el = h('details', { class: 'tools activity', testid: 'activity' },
    h('summary', null, parts.flatMap((p, i) => i ? [' · ', p] : [p])),
    h('div', { class: 'body' }, list))
  applyFold(el, fold, true)
  return el
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
// log. Settled turns keep their nodes between refetches, so a streaming
// delta redraws only the tail.
const turnNodes = new Map()
let turnsFor = null

function renderLive () {
  const box = $('#thread-live')
  const r = state.card || row(state.sel)
  const l = state.live
  if (turnsFor !== state.sel) { turnNodes.clear(); turnsFor = state.sel }
  const parts = []
  if (r) {
    const pausing = r.running?.pausing
    const role = l?.role || ROLE[l?.stage || r.stage] || 'agent'
    const stage = l?.stage || r.stage
    if (l?.elsewhere) {
      const e = l.elsewhere
      parts.push(h('div', { class: 'live', testid: 'live-elsewhere' }, e.busy ? h('span', { class: 'spinner' }) : idleMark(),
        h('span', null, e.note || `Another gummi (pid ${e.pid}) is running ${[e.stage, e.role].filter(Boolean).join(' · ')}; this board watches it.`)))
    } else if (!l && r.elsewhere) {
      parts.push(h('div', { class: 'live', testid: 'live-elsewhere' }, idleMark(), h('span', null, 'Another gummi is driving this card; this board watches it.')))
    }
    if (l) {
      parts.push(...conversation('stage', l, role, stage))
      if (l.busy) {
        const words = [`${role} is ${l.verb || 'working'}`, l.tool ? String(l.tool.label || l.tool.tool).replace(/\s+/g, ' ') : null].filter(Boolean).join(' · ')
        parts.push(h('div', { class: 'live', testid: 'live' },
          h('span', { class: 'spinner' }),
          h('span', { class: 'shimmer' }, words + (pausing ? ' · pauses after this turn' : '')),
          l.spent ? h('span', { class: 'spent' }, cr(l.spent)) : null))
      }
      // a failure the pinned decision already states (a failed stage's
      // stop quotes it) is not said a second time under the thread
      if (l.err && !String(state.card?.decision?.question || '').includes(l.err.trim())) parts.push(h('div', { class: 'live badc', testid: 'live-error' }, l.err))
      for (const k of ['consult', 'freeform']) {
        const c = l[k]
        if (!c || !(c.turns?.length || c.streaming || c.busy || c.sending || c.err)) continue
        // a consult says what it may do: read-only, or the engine's notice
        // when the backend it runs on cannot confine it
        const label = k === 'freeform' ? 'session' : c.notice ? 'consult · not confined' : 'consult · read-only'
        // headed as the settled turns are: the conversation, then its agent
        parts.push(h('div', { class: ['ev-stage', k === 'freeform' ? 'st-open' : 'st-todo'], testid: `live-${k}-head` }, h('b', null, label),
          k === 'freeform' ? h('span', { class: 'sum' }, c.role || 'implementer') : null))
        if (c.notice) parts.push(h('div', { class: 'live warnc', testid: `live-${k}-notice` }, c.notice))
        parts.push(...conversation(k, c, c.role || (k === 'freeform' ? 'implementer' : 'consult'), k === 'freeform' ? 'open' : null))
        if (c.sending) parts.push(you({ text: c.sending, via: 'sending' }))
        if (c.tasks?.length) parts.push(tasks(c.tasks, k))
        if (k === 'freeform' && c.watches?.length) parts.push(watches(c.watches))
        if (k === 'freeform' && c.queued?.length) parts.push(queued(state.sel, c.queued))
        if (c.busy) {
          // the same line a stage run shows: what it is doing, the call in
          // flight, and what the session has cost and how full it is
          const words = [c.verb || 'thinking', c.tool ? String(c.tool.label || c.tool.tool).replace(/\s+/g, ' ') : null].filter(Boolean).join(' · ')
          const ctx = c.context?.limit ? `${Math.round(100 * c.context.tokens / c.context.limit)}% context` : null
          parts.push(h('div', { class: 'live', testid: `live-${k}` }, h('span', { class: 'spinner' }), h('span', { class: 'shimmer' }, words),
            c.spent || ctx ? h('span', { class: 'spent' }, [c.spent ? cr(c.spent) : null, ctx].filter(Boolean).join(' · ')) : null))
        }
        if (c.err) parts.push(h('div', { class: 'live badc' }, c.err))
      }
    } else if (r.status === 'running') {
      parts.push(h('div', { class: 'live', testid: 'live' }, h('span', { class: 'spinner' }), h('span', { class: 'shimmer' }, `${ROLE[r.stage] || 'agent'} is ${r.running?.verb || 'working'}${pausing ? ' · pauses after this turn' : ''}`)))
    }
  }
  const sc = $('#thread')
  const stick = atBottom(sc)
  box.replaceChildren(...parts)
  if (stick) requestAnimationFrame(() => { sc.scrollTop = sc.scrollHeight })
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

// watches are the gummi watches the session has running; each reports
// back to the agent as a turn of its own.
function watches (lines) {
  return h('div', { class: 'queued', testid: 'watches' },
    ...lines.map(text => h('div', { class: 'queued-line watch' },
      h('span', { class: 'via' }, 'watching'), h('span', { class: 'text' }, text))))
}

// tasks is the agent's own checklist, pinned under the conversation: open
// while anything is left, folded to its count once all of it is done. Its
// fold is remembered by the conversation's kind — tasks render only for a
// consult and a freeform session, one of each per card.
const TASK_MARK = { completed: '✓', in_progress: '▸', pending: '○' }
function tasks (list, kind) {
  const done = list.filter(t => t.status === 'completed').length
  const el = h('details', { class: 'tools tasks', testid: 'tasks' },
    h('summary', null, `tasks ${done}/${list.length}`),
    h('ul', { class: 'body' }, ...list.map(t =>
      h('li', { class: ['task', t.status] }, `${TASK_MARK[t.status] || '○'} ${t.text}`))))
  applyFold(el, kind, done < list.length)
  return el
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
function conversation (kind, c, role, stage) {
  const out = []
  const turns = c.turns || []
  let run = []
  let start = 0
  const flush = () => {
    if (!run.length) return
    const sig = run.map(t => t.tool ? `${t.tool.status}|${t.tool.label}|${t.tool.ms || 0}|${(t.tool.output || '').length}` : `k|${(t.text || '').length}`).join(';')
    const key = `${kind}:act:${start}`
    out.push(cached(key, sig, () => activity(activityItems(run), key)))
    run = []
  }
  turns.forEach((t, i) => {
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
    const sig = `${t.author}|${(t.text || '').length}|${state.card?.files?.url || ''}|${back}|${c.model || ''}`
    const mkey = `${kind}:${i}`
    out.push(cached(mkey, sig, () => t.author === 'you'
      ? you({ text: t.text, by: t.by, time: t.time, rewind: back ? () => rewind(state.sel, back) : null })
      : message({ author: t.author === role ? null : t.author, role: t.author === 'gummi' ? null : t.author, stage, text: t.text, key: mkey, time: t.time, ms, model: t.author === 'gummi' ? null : c.model })))
  })
  flush()
  if (c.streaming) {
    out.push(h('div', { class: ['msg reply live-msg', stage && `st-${stage}`], testid: 'live-streaming' },
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
