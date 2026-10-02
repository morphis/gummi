// thread.js — the card's conversation. Items arrive from the server already
// folded (internal/threadfold) and are upserted by key, so an item that grew
// (a tool group gaining calls, a stage divider getting its verdict) is
// redrawn in place rather than appended twice. Items group under the stage
// divider before them; earlier stages fold into one line each and the
// current one stays open. The live block (streaming text, the tool in
// flight) sits under the items, outside the polite live region, so a screen
// reader hears new items and not every streamed token.

import { $, h, clear, clock, dur, cr, initials, ROLE, decisionWord, decisionColor, plural } from './dom.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { on, state, row } from './store.js?v=__ASSET_V__'
import { draftHero } from './session.js?v=__ASSET_V__'

const nodes = new Map() // item key -> { sig, el }
const groups = new Map() // group key -> { el, body, summary }
const openGroups = new Set() // folded groups a person opened, by key
let shownFor = null

export function initThread () {
  on(['thread', 'sel', 'card', 'sessionDraft'], render)
  on(['live'], () => { if (state.thread && !state.thread.items.length) render() })
  on(['live', 'card', 'board'], renderLive)
  $('#thread-items').addEventListener('toggle', (e) => {
    const d = e.target
    if (!d.classList?.contains('sg') || d.classList.contains('cur')) return
    if (d.open) openGroups.add(d.dataset.key); else openGroups.delete(d.dataset.key)
  }, true)
}

function atBottom (sc) { return sc.scrollHeight - sc.scrollTop - sc.clientHeight < 80 }

function render () {
  const box = $('#thread-items')
  const sc = $('#thread')
  const stick = shownFor !== state.sel || atBottom(sc)
  if (shownFor !== state.sel) {
    clear(box); nodes.clear(); groups.clear()
    shownFor = state.sel
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
  const pinned = state.card?.decision
  if (pinned) {
    const same = (id) => !!id && (id === pinned.ref || id.startsWith(pinned.ref + ':'))
    for (const grp of gs) {
      grp.items = grp.items.filter(it => !(it.t === 'decision' && !it.decision?.answer &&
        (same(it.decision?.id) || it.decision?.question === pinned.question)))
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
    : [plural(count, 'event'), verdict || receipt?.receipt?.text || receipt?.text, st.credits ? `${cr(st.credits)} cr` : null].filter(Boolean)
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
  g.el.open = last || openGroups.has(grp.key)
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

// itemEl draws one thread item; the board agent's transcript reuses it.
export function itemEl (it) {
  switch (it.t) {
    case 'message': return message(it)
    case 'you': return you(it)
    case 'tools': return h('div', { class: 'indent' }, tools(it.tools || []))
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
    return h('div', { class: 'msg gummi' },
      h('div', { class: 'av you', 'aria-hidden': 'true' }, 'g'),
      h('div', null,
        h('div', { class: 'who' }, h('b', null, 'gummi'), h('span', { class: 'mono' }, clock(it.time))),
        h('details', { class: 'body prompt' }, h('summary', null, firstLine(it.text)), md(it.text))))
  }
  if (it.author === 'you') return you(it)
  const role = it.author || it.role || ROLE[it.stage] || 'agent'
  // a consult answer sits where it was asked, and says it steered nothing
  const consult = it.via === 'consult'
  return h('div', { class: ['msg', it.stage && `st-${it.stage}`], testid: consult ? 'thread-consult' : null },
    h('div', { class: 'av agent', 'aria-hidden': 'true' }, avatarFor(role)),
    h('div', null,
      h('div', { class: 'who' }, h('b', null, role),
        consult ? h('span', { class: 'via' }, 'read-only') : null,
        it.flavor && it.flavor !== 'work' ? h('span', { class: 'via' }, it.flavor) : null,
        it.model ? h('span', { class: 'mono' }, it.model) : null,
        h('span', { class: 'mono' }, clock(it.time))),
      h('div', { class: 'body' }, md(it.text))))
}

// you is a line a person typed. It is headed with the name it was sent
// with, and "you" when it came with none (the terminal's own lines) —
// never with the viewer's own name, which would put one person's words
// in another's mouth on a board several people share.
function you (it) {
  const who = it.by || 'you'
  return h('div', { class: 'msg', testid: it.via === 'consult' ? 'thread-consult' : null },
    h('div', { class: 'av you', 'aria-hidden': 'true' }, it.by ? initials(it.by) : 'you'),
    h('div', null,
      h('div', { class: 'who' }, h('b', null, who), h('span', { class: 'mono' }, clock(it.time)), it.via ? h('span', { class: 'via' }, it.via) : null),
      h('div', { class: 'body' }, md(it.text))))
}

export function tools (list) {
  const fails = list.filter(t => t.status === 'fail').length
  const running = list.some(t => t.status === 'running')
  // a Monitor watch (webapi.StatusWatching) never settles on its own the
  // way an ordinary call does — it stays outstanding past the turn that
  // started it going idle — so it earns its own word and a spinner that
  // doesn't spin: there, but not "busy", so a reply stays obviously safe
  // to send.
  const watching = list.some(t => t.status === 'watching')
  return h('details', { class: 'tools', testid: 'tool-group' },
    h('summary', null, plural(list.length, 'tool call'),
      fails ? h('span', { class: 'fails' }, `· ${fails} failed`) : null,
      watching ? h('span', { class: 'watching' }, '· watching') : null,
      (running || watching) ? h('span', { class: ['spinner', !running && 'still'] }) : null),
    h('ol', null, list.map(t => {
      const label = String(t.label || '').replace(/\s+/g, ' ').trim()
      const named = label.startsWith(t.tool + ' ') || label === t.tool
      const rest = named ? label.slice(t.tool.length).trim() : label
      return h('li', { class: t.status || null, title: t.output || null }, t.tool, ' ',
        h('span', null, [rest, t.detail].filter(Boolean).join(' ')),
        t.ms ? h('span', { class: 'ms' }, dur(t.ms)) : null,
        t.status === 'fail' && t.output ? h('pre', { class: 'tool-out' }, t.output) : null)
    })))
}

function receipt (it) {
  const r = it.receipt || { ok: true, text: it.text }
  const by = r.by || it.by
  return h('div', { class: 'receipt', testid: 'receipt' },
    h('span', { class: r.ok ? 'ok' : 'bad', 'aria-label': r.ok ? 'done' : 'sent back' }, r.ok ? '✓' : '↺'),
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
      parts.push(h('div', { class: 'live', testid: 'live-elsewhere' }, h('span', { class: ['spinner', !e.busy && 'still'] }),
        h('span', null, e.note || `Another gummi (pid ${e.pid}) is running ${[e.stage, e.role].filter(Boolean).join(' · ')}; this board watches it.`)))
    } else if (!l && r.elsewhere) {
      parts.push(h('div', { class: 'live', testid: 'live-elsewhere' }, h('span', { class: 'spinner still' }), h('span', null, 'Another gummi is driving this card; this board watches it.')))
    }
    if (l) {
      parts.push(...conversation('stage', l, role, stage))
      if (l.busy || l.state === 'queued') {
        const words = l.state === 'queued' && !l.busy
          ? (r.running?.why || 'queued for a free slot')
          : [`${role} is ${l.verb || 'working'}`, l.tool ? String(l.tool.label || l.tool.tool).replace(/\s+/g, ' ') : null].filter(Boolean).join(' · ')
        parts.push(h('div', { class: 'live', testid: 'live' },
          h('span', { class: ['spinner', !l.busy && 'still'] }),
          h('span', { class: l.busy ? 'shimmer' : null }, words + (pausing ? ' · pauses after this turn' : '')),
          l.spent ? h('span', { class: 'spent' }, `${cr(l.spent)} cr`) : null))
      }
      if (l.err) parts.push(h('div', { class: 'live badc', testid: 'live-error' }, l.err))
      for (const [k, label] of [['consult', 'consult · read-only'], ['freeform', 'session']]) {
        const c = l[k]
        if (!c || !(c.turns?.length || c.streaming || c.busy || c.sending || c.err)) continue
        // headed as the settled turns are: the conversation, then its agent
        parts.push(h('div', { class: ['ev-stage', k === 'freeform' ? 'st-open' : 'st-todo'], testid: `live-${k}-head` }, h('b', null, label),
          k === 'freeform' ? h('span', { class: 'sum' }, c.role || 'implementer') : null))
        parts.push(...conversation(k, c, c.role || (k === 'freeform' ? 'implementer' : 'consult'), k === 'freeform' ? 'open' : null))
        if (c.sending) parts.push(you({ text: c.sending, via: 'sending' }))
        if (c.busy) parts.push(h('div', { class: 'live', testid: `live-${k}` }, h('span', { class: 'spinner' }), h('span', { class: 'shimmer' }, c.verb || 'thinking')))
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

// conversation draws a session's settled turns (tool turns folded into one
// block per run) and the message still streaming.
function conversation (kind, c, role, stage) {
  const out = []
  const turns = c.turns || []
  let run = []
  const flush = (i) => {
    if (!run.length) return
    const sig = run.map(t => `${t.tool?.status}|${t.tool?.label}`).join(';')
    out.push(cached(`${kind}:tools:${i}`, sig, () => h('div', { class: 'indent' }, tools(run.map(t => t.tool)))))
    run = []
  }
  turns.forEach((t, i) => {
    if (t.tool) { run.push(t); return }
    flush(i)
    const sig = `${t.author}|${(t.text || '').length}|${state.card?.files?.url || ''}`
    out.push(cached(`${kind}:${i}`, sig, () => t.author === 'you'
      ? you({ text: t.text, by: t.by })
      : message({ author: t.author === role ? null : t.author, role: t.author === 'gummi' ? null : t.author, stage, text: t.text })))
  })
  flush(turns.length)
  if (c.streaming) {
    out.push(h('div', { class: ['msg live-msg', stage && `st-${stage}`], testid: 'live-streaming' },
      h('div', { class: 'av agent', 'aria-hidden': 'true' }, avatarFor(role)),
      h('div', null, h('div', { class: 'who' }, h('b', null, role), h('span', { class: 'mono' }, 'writing')), h('div', { class: 'body' }, md(c.streaming)))))
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
  d.open = true
  openGroups.add(d.dataset.key)
  d.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' })
}
