// stats.js — the Stats tab: the card's run as cardrun folds it (the same
// fold as the TUI's run tab and `gummi status --stats`): what it cost,
// where it went, how much was rework, where the hours went, what the card
// did with its hands and settled with its judgment, and every pass.
//
// The honesty rules carry over from the terminal: a missing record says
// so in words rather than rendering zero, and a section with nothing to
// say does not render at all.

import { h, append, cr, dur, ctxMeter, plural, stageVar } from './dom.js?v=__ASSET_V__'
import { get, cardPath } from './api.js?v=__ASSET_V__'

export const statsTab = {
  name: 'stats',
  label: 'Stats',
  key: 'g r',
  fetch: (id) => get(cardPath(id, 'stats')),
  empty: (s) => !s || (isSession(s) ? !s.money?.credits : !hasRecord(s)),
  render
}

const isSession = (s) => s?.stage === 'open' || s?.kind === 'freeform'

// hasRecord is the tab's offer rule, the run tab's own: the stage is past
// todo, or there is spend — decomposition books the counter at ingest,
// before any pass exists, and a card that has spent must still be offered
// the tab that answers the spend.
const hasRecord = (s) => (s.stage && s.stage !== 'todo') || (s.money?.credits || 0) > 0 || (s.money?.estimated || 0) > 0

function render (pane, entry, ctx) {
  const s = entry.data
  const ps = s?.sessions || []
  if (isSession(s)) { session(pane, s, ctx); return }
  const m = s.money || {}
  const c = s.clock || {}
  // A card draws when it has passes — or when it has spend and no passes:
  // decomposition spend is booked at ingest, and a spent card must not
  // read "nothing has run". The pass-derived sections below are built
  // from the passes and stay absent rather than printed as zeroes on a
  // card whose record holds the money alone.
  if (!ps.length && !m.credits && !m.estimated) {
    pane.append(h('div', { class: 'empty', testid: 'stats-none' }, h('b', null, 'Nothing has run yet'), 'Passes and what they cost appear here once a stage starts.'))
    return
  }
  const sect = h('div', { class: 'sect', testid: 'stats' })
  if (ps.length) {
    const max = Math.max(...ps.map(p => p.credits || 0), 0.1)
    const env = s.envelope?.credits
    sect.append(
      h('div', { class: 'tiles' },
        tile(cr(m.credits), '', env ? `spent of ${cr(env)} budget` : 'spent', 'stats-spent'),
        tile(String(ps.filter(p => p.credits > 0 || p.turns > 0).length), '', 'agent passes', 'stats-passes'),
        tile(cr(m.rework), '', 'rework', 'stats-rework'),
        tile(dur(c.agentMs), '', 'agent time'),
        tile(dur(c.onYouMs), '', 'waiting on you')),
      h('div', { class: 'tablewrap', tabindex: '0', role: 'region', 'aria-label': 'Passes' }, passesTable(ps, max)))
  }
  append(sect, [
    moneyBars(m),
    redoBlock(ps, m),
    clockSection(c),
    handsSection(s),
    judgmentSection(s),
    envelopeLine(s),
    h('p', { class: 'foot-note' }, 'The same numbers as ', h('span', { class: 'mono' }, `gummi status --stats ${ctx.id}`), '.')])
  pane.append(sect)
}

// ---- the passes table: one row per pass, with the honesty marks ----

function passesTable (ps, max) {
  return h('table', { class: 'passes', testid: 'stats-table' },
    h('thead', null, h('tr', null, h('th', null, 'pass'), h('th', null, 'role'), h('th', null, 'model'), h('th', { class: 'num' }, 'cost'), h('th', { class: 'barc' }, h('span', { class: 'sr-only' }, 'share')), h('th', { class: 'num' }, 'time'), h('th', { class: 'num' }, 'tokens'), h('th', null, 'context'))),
    h('tbody', null, ps.map(p => h('tr', { class: p.redo && 'rework', style: { '--sc': stageVar(p.stage) } },
      h('td', null, [p.stage, p.flavor && p.flavor !== 'work' ? p.flavor : null].filter(Boolean).join(' · ')),
      h('td', null, p.role || '—'),
      h('td', { class: 'mono' }, p.model || '—'),
      h('td', { class: 'num' }, p.credits ? cr(p.credits) : '—'),
      h('td', { class: 'barc' }, h('div', { class: 'b', style: { '--w': ((p.credits || 0) / max * 100) + '%' } })),
      h('td', { class: 'num' }, p.ended ? dur(new Date(p.ended) - new Date(p.started)) : 'running'),
      h('td', { class: 'num' }, tokenText(p.tokens)),
      h('td', null, p.contextLimit ? ctxMeter({ tokens: p.contextPeak, limit: p.contextLimit }, 'ctx-meter') : null)))))
}

// tok shortens a token count the width of a table cell reads: whole
// thousands from 10k up, one decimal below that, exact under a thousand.
const tok = (n) => n >= 1e6 ? (n / 1e6).toFixed(1) + 'M'
  : n >= 1e4 ? Math.round(n / 1e3) + 'k'
  : n >= 1e3 ? (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  : String(n)

// tokenText is a pass's token spend the width of a table cell reads: the
// total, then the components a reader cannot take from the total — how
// much the prompt cache served and how much went out. A pass the backend
// reported no figures for reads as "—", the same dash its credits takes,
// never a zero.
function tokenText (t) {
  const total = t ? (t.input || 0) + (t.cached || 0) + (t.output || 0) : 0
  if (!total) return '—'
  const parts = [tok(total)]
  if (t.cached) parts.push(`${tok(t.cached)} cached`)
  if (t.output) parts.push(`${tok(t.output)} out`)
  return parts.join(' · ')
}

// ---- where the money went ----

// moneyBars is "where it went": by stage (its stage colour), by role and
// by model (one neutral series), drawn only when the fold holds buckets,
// with the estimated mark where it stands and the spend no pass holds
// named rather than left for a reader to find by summing the rows short.
function moneyBars (m) {
  const groups = [
    bucketBars(m.byStage, m.credits, (name) => stageVar(name), 'stats-bars-stage'),
    bucketBars(m.byRole, m.credits, null, 'stats-bars-role'),
    bucketBars(m.byModel, m.credits, null, 'stats-bars-model')
  ].filter(Boolean)
  const notes = [
    m.estimated > 0
      ? h('p', { class: 'foot-note warn' }, `~${cr(m.estimated)} estimated`, ' — not yet settled by the provider')
      : null,
    m.elsewhere > 0
      ? h('p', { class: 'foot-note' }, `${cr(m.elsewhere)} on turns that are not passes`,
        (m.elsewhereBy || []).length ? ' — ' + m.elsewhereBy.map(b => `${b.name} ${cr(b.credits)}`).join(', ') : '')
      : null
  ].filter(Boolean)
  if (!groups.length && !notes.length) return null
  return h('section', { class: 'sblock', testid: 'stats-bars', 'aria-label': 'Where the money went' },
    h('h3', { class: 'shead' }, 'Where it went'),
    groups, notes)
}

function bucketBars (list, total, colorOf, testid) {
  if (!list || !list.length) return null
  const max = Math.max(...list.map(b => b.credits || 0), 0.0001)
  return h('table', { class: 'passes bars', testid },
    h('tbody', null, list.map(b => h('tr', { style: { '--sc': colorOf ? colorOf(b.name) : null } },
      h('th', { scope: 'row' }, b.name || '—'),
      h('td', { class: 'num' }, cr(b.credits)),
      h('td', { class: 'barc' }, h('div', { class: 'b', style: { '--w': ((b.credits || 0) / max * 100) + '%' } })),
      h('td', { class: 'num pct' }, total ? Math.round((b.credits || 0) / total * 100) + '%' : '')))))
}

// ---- the redo ----

// redoBlock is the headline, and it earns its own block: a report that
// makes you count rows to find the expensive mistake has buried its own
// point. Each redone pass is named, and the one that cost more than the
// first pass of the same work is flagged beside it. Absent entirely on a
// card that never did anything twice.
function redoBlock (ps, m) {
  const redone = ps.filter(p => p.redo)
  if (!redone.length) return null
  // The first pass of each redone piece of work, so the comparison the
  // block exists to make is on the page rather than in the reader's head.
  const first = new Map()
  for (const p of ps) {
    const k = redoKey(p)
    if (!first.has(k)) first.set(k, p)
  }
  return h('section', { class: 'sblock', testid: 'stats-redo', 'aria-label': 'The redo' },
    h('h3', { class: 'shead' }, 'The redo'),
    h('ul', { class: 'redolist' }, redone.map(p => {
      const f = first.get(redoKey(p))
      return h('li', null,
        h('span', { class: 'what' }, [p.stage, p.role].filter(Boolean).join(' · ')),
        h('span', { class: 'why' }, p.redoReason || 'repeated'),
        p.turns ? h('span', null, plural(p.turns, 'turn')) : null,
        h('span', null, p.ended ? dur(+new Date(p.ended) - +new Date(p.started)) : 'running'),
        h('span', { class: 'num' }, cr(p.credits) + (p.reconstructed ? ' ~' : '')),
        f && p.credits > f.credits && f.credits > 0 ? h('span', { class: 'dearer' }, '← cost more than the first') : null)
    })),
    h('p', { class: 'foot-note' }, `${cr(m.rework)} of ${cr(m.credits)} was work already done (${Math.round((m.rework || 0) / (m.credits || 1) * 100)}%).`),
    m.corrected > 0 && m.reproved > 0
      ? h('p', { class: 'foot-note' }, `${cr(m.corrected)} corrected after a verdict · ${cr(m.reproved)} re-proved over a new base.`)
      : null)
}

const redoKey = (p) => `${p.stage}\u0000${p.role || ''}\u0000${p.flavor || ''}`

// ---- the clock ----

// clockSection is where the hours went: one part-to-whole bar over agent
// working, waiting on you and nothing running (the fleet's pattern), a
// key of the segments that are there, and the distances the card ran.
// Drawn only when the card has measured life to account for.
function clockSection (c) {
  if (!(c.elapsedMs > 0)) return null
  const parts = [
    { k: 'agent', label: 'agent working', ms: c.agentMs || 0 },
    { k: 'you', label: 'waiting on you', ms: c.onYouMs || 0 },
    { k: 'idle', label: 'nothing running', ms: c.idleMs || 0 }
  ]
  const pct = (ms) => ms / c.elapsedMs * 100
  const segs = parts.filter(p => p.ms > 0)
  const lines = [
    h('p', { class: 'skv' }, 'elapsed ', h('b', null, dur(c.elapsedMs))),
    c.toFirstGateMs > 0 ? h('p', { class: 'skv' }, 'to first gate ', h('b', null, dur(c.toFirstGateMs))) : null,
    c.toVerifiedMs > 0 ? h('p', { class: 'skv' }, 'to verified ', h('b', null, dur(c.toVerifiedMs))) : null
  ].filter(Boolean)
  return h('section', { class: 'sblock', testid: 'stats-clock', 'aria-label': 'Where the hours went' },
    h('h3', { class: 'shead' }, 'The clock', h('span', null, `${dur(c.elapsedMs)} of card time`)),
    h('div', { class: 'stack', role: 'img', 'aria-label': segs.map(p => `${p.label} ${dur(p.ms)}`).join(', ') },
      segs.map(p => h('span', {
        class: ['part', `c-${p.k}`],
        style: { '--w': pct(p.ms).toFixed(3) + '%' },
        title: `${p.label} ${dur(p.ms)} · ${Math.round(pct(p.ms))}%`
      }))),
    h('ul', { class: 'keyrow' }, segs.map(p => h('li', { testid: `stats-clock-${p.k}` },
      h('i', { class: ['sw', `c-${p.k}`], 'aria-hidden': 'true' }),
      h('span', null, p.label),
      h('b', null, dur(p.ms)),
      h('span', { class: 'p' }, `${Math.round(pct(p.ms))}%`)))),
    lines)
}

// ---- its hands ----

// handsSection is what the card did: its turns, the tools it called and
// how that went, what it handed to skills and subagents, and gummi's own
// checks with their excused marking. Where a backend reports no tool
// calls the section says so in the sentence the number would have taken —
// the wire's absent array, never a zero.
function handsSection (s) {
  const hd = s.hands
  if (!hd) return null
  const skills = hd.skills || []
  const subs = hd.subagents || []
  const checks = hd.checks || []
  const any = hd.turns > 0 || hd.toolCalls > 0 || hd.toolFails > 0 || skills.length || subs.length || checks.length
  if (!any) return null
  const body = [h('p', { class: 'skv' }, 'turns ', h('b', null, String(hd.turns)))]
  if (hd.tools == null) {
    body.push(h('p', { class: 'skv faint', testid: 'stats-no-tools' }, 'tools none recorded — this backend reports no tool calls'))
  } else {
    body.push(h('p', { class: 'skv' }, 'tools ', h('b', null, `${hd.toolCalls} ${hd.toolCalls === 1 ? 'call' : 'calls'}, ${hd.toolFails} failed`)))
    if (hd.tools.length) {
      body.push(h('div', { class: 'tablewrap' }, h('table', { class: 'passes', testid: 'stats-tools' },
        h('thead', null, h('tr', null, h('th', null, 'tool'), h('th', { class: 'num' }, 'calls'), h('th', { class: 'num' }, 'failed'), h('th', { class: 'num' }, 'time'))),
        h('tbody', null, hd.tools.map(t => h('tr', null,
          h('td', { class: 'mono' }, t.name || '—'),
          h('td', { class: 'num' }, String(t.calls || 0)),
          h('td', { class: 'num' }, t.fails ? String(t.fails) : '—'),
          h('td', { class: 'num' }, t.totalMs ? dur(t.totalMs) : '—')))))))
    }
  }
  for (const sk of skills) {
    body.push(h('p', { class: 'skv' }, 'skill ', h('b', null, sk.detail || '(not recorded)'), ` ×${sk.calls}`))
  }
  for (const sa of subs) {
    body.push(h('p', { class: 'skv' }, 'subagents ', h('b', null, `${sa.calls} spawned`), sa.detail ? ` — ${sa.detail}` : ' (not recorded)'))
  }
  if (checks.length) {
    body.push(h('div', { class: 'tablewrap' }, h('table', { class: 'passes', testid: 'stats-checks' },
      h('thead', null, h('tr', null, h('th', null, 'check'), h('th', { class: 'num' }, 'runs'), h('th', { class: 'num' }, 'failed'), h('th', null, ''))),
      h('tbody', null, checks.map(ch => h('tr', null,
        h('td', { class: 'mono' }, ch.name || '—'),
        h('td', { class: 'num' }, String(ch.runs || 0)),
        h('td', { class: 'num' }, ch.fails ? String(ch.fails) : '—'),
        h('td', null, ch.excused ? h('span', { class: 'excused' }, 'pre-existing, excused') : null)))))))
  }
  return h('section', { class: 'sblock', testid: 'stats-hands', 'aria-label': 'Its hands' },
    h('h3', { class: 'shead' }, 'Its hands'), body)
}

// ---- its judgment ----

// judgmentSection is what the card decided, and who decided it: the gates
// and asks split by who answered, and every time it stopped and waited.
function judgmentSection (s) {
  const j = s.judgment
  if (!j) return null
  const parks = j.parks || []
  const lines = []
  if (j.gates?.total) lines.push(h('li', { testid: 'stats-gates' }, h('b', null, plural(j.gates.total, 'gate')), ` — ${j.gates.byYou} you, ${j.gates.byMachine} machine`))
  if (j.asks?.total) lines.push(h('li', { testid: 'stats-asks' }, h('b', null, plural(j.asks.total, 'ask')), ` — ${j.asks.byYou} you, ${j.asks.byMachine} autopilot`))
  if (!lines.length && !parks.length) return null
  return h('section', { class: 'sblock', testid: 'stats-judgment', 'aria-label': 'Its judgment' },
    h('h3', { class: 'shead' }, 'Its judgment'),
    lines.length ? h('ul', { class: 'kvl' }, lines) : null,
    parks.map(p => h('p', { class: 'skv' }, 'parked ', h('b', null, p.detail || p.reason))))
}

// ---- the envelope ----

// envelopeLine is the budget against the spend, with the utilization the
// page computes from granted and left — the wire carries no derived
// percentage for it.
function envelopeLine (s) {
  const e = s.envelope
  if (!e || !(e.credits > 0)) return null
  const spent = Math.max(e.credits - e.left, 0)
  return h('p', { class: 'foot-note', testid: 'stats-envelope' },
    `granted ${cr(e.credits)} · spent ${cr(spent)} · ${Math.round(spent / e.credits * 100)}% used`)
}

// ---- a session's run ----

// session draws a session's run: it works in turns, not stage passes, so
// there is no pass table and hands and judgment never render — what it
// spent, against its envelope, on which models, and by stage and role.
function session (pane, s, ctx) {
  const m = s.money || {}
  if (!m.credits) {
    pane.append(h('div', { class: 'empty', testid: 'stats-none' }, h('b', null, 'Nothing spent yet'), 'What the session spends appears here once its agent has taken a turn.'))
    return
  }
  const env = s.envelope?.credits
  const models = (m.byModel || []).filter(b => b.credits > 0)
  const max = Math.max(...models.map(b => b.credits), 0.1)
  const bars = [
    bucketBars((m.byStage || []).filter(b => b.credits > 0), m.credits, (name) => stageVar(name), 'stats-bars-stage'),
    bucketBars((m.byRole || []).filter(b => b.credits > 0), m.credits, null, 'stats-bars-role')
  ].filter(Boolean)
  pane.append(h('div', { class: 'sect', testid: 'stats' },
    h('div', { class: 'tiles' },
      tile(cr(m.credits), '', env ? `spent of ${cr(env)} budget` : 'spent', 'stats-spent'),
      env ? tile(cr(Math.max(s.envelope.left, 0)), '', 'left', 'stats-left') : null,
      tile(String(models.length), '', models.length === 1 ? 'model' : 'models', 'stats-models')),
    models.length
      ? h('div', { class: 'tablewrap', tabindex: '0', role: 'region', 'aria-label': 'Models' }, h('table', { class: 'passes', testid: 'stats-table' },
        h('thead', null, h('tr', null, h('th', null, 'model'), h('th', { class: 'num' }, 'cost'), h('th', { class: 'barc' }, h('span', { class: 'sr-only' }, 'share')))),
        h('tbody', null, models.map(b => h('tr', null,
          h('td', { class: 'mono' }, b.name || '—'),
          h('td', { class: 'num' }, cr(b.credits)),
          h('td', { class: 'barc' }, h('div', { class: 'b', style: { '--w': (b.credits / max * 100) + '%' } })))))))
      : null,
    bars.length
      ? h('section', { class: 'sblock', testid: 'stats-bars', 'aria-label': 'Where the money went' }, h('h3', { class: 'shead' }, 'Where it went'), bars)
      : null,
    envelopeLine(s),
    h('p', { class: 'foot-note' }, 'The same numbers as ', h('span', { class: 'mono' }, `gummi status --stats ${ctx.id}`), '.')))
}

function tile (v, unit, k, testid) {
  return h('div', { class: 'tile', testid }, h('div', { class: 'v' }, v, unit ? h('small', null, ' ' + unit) : null), h('div', { class: 'k' }, k))
}