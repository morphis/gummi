// views/fleet.js — Fleet stats: where the whole board's credits and hours
// went over a window, fleetrun's fold (the TUI's stats tab, the same
// numbers). The headline money, the clock (card-hours split into agent
// working, waiting on you and nothing running — the three add up to the
// cards' elapsed time), where the money went by stage and by model, and
// the timeline: one lane per card, its stage sessions drawn to scale,
// its waits on a person hatched, its gates and landing marked.
//
// Colour follows meaning: a stage keeps its stage colour everywhere on
// the page, waiting on you is the warning tone (hatched, so it does not
// rely on hue), and a nominal breakdown (models) is one neutral series.

import { h, append, clear, cr, dur, plural, storage, stageVar } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { segmented, errorBox } from './kit.js?v=__ASSET_V__'

const WINDOWS = [
  { value: '24h', label: '24 hours', ms: 24 * 3600e3 },
  { value: '7d', label: '7 days', ms: 7 * 24 * 3600e3 },
  { value: '30d', label: '30 days', ms: 30 * 24 * 3600e3 },
  { value: 'all', label: 'All time', ms: 0 }
]
const STAGE_ORDER = ['plan', 'implement', 'verify', 'open']
const REFRESH_MS = 30_000

registerView('fleet', {
  title: 'Fleet stats',
  css: 'views/fleet.css',
  mount (body, ctx) {
    const v = { win: storage.get('fleet-window', '7d'), rep: null, err: null, loading: false, alive: true }
    if (!WINDOWS.some(w => w.value === v.win)) v.win = '7d'
    body.classList.add('vfleet')

    const bar = h('div', { class: 'fbar' })
    const out = h('div', { class: 'fout', testid: 'fleet' })
    const tip = h('div', { class: 'ftip', role: 'tooltip', hidden: true, testid: 'fleet-tip' })
    body.append(bar, out, tip)

    async function load () {
      v.loading = true
      out.classList.add('stale')
      drawBar()
      const w = WINDOWS.find(x => x.value === v.win)
      const q = w.ms ? `from=${encodeURIComponent(new Date(Date.now() - w.ms).toISOString().replace(/\.\d+Z$/, 'Z'))}` : 'from=all'
      try {
        v.rep = await ctx.api.get(`/api/fleet?${q}`)
        v.err = null
      } catch (err) { v.err = err }
      v.loading = false
      if (!v.alive) return
      out.classList.remove('stale')
      drawBar()
      draw()
    }

    function drawBar () {
      clear(bar)
      append(bar, [
        segmented(WINDOWS.map(w => ({ value: w.value, label: w.label })), v.win,
          (x) => { v.win = x; storage.set('fleet-window', x); load() }, { testid: 'fleet-window', label: 'Window' }),
        v.rep ? h('span', { class: 'span', testid: 'fleet-span' }, spanText(v.rep, v.win === 'all')) : null,
        h('span', { class: 'grow' }),
        v.loading ? h('span', { class: 'spinner', 'aria-label': 'measuring' }) : null])
    }

    function draw () {
      clear(out)
      if (v.err) return out.append(errorBox(v.err, 'fleet-error'))
      const r = v.rep
      if (!r) return
      const lanes = r.lanes || []
      if (!lanes.length && !r.credits && !r.allTimeCards) {
        return out.append(h('div', { class: 'empty', testid: 'fleet-empty' }, h('b', null, 'Nothing has run on this board yet'), 'Cards show up here once a stage has run.'))
      }
      out.append(headline(r), clockBar(r), h('div', { class: 'fsplit' }, byStage(r), byModel(r)), timeline(r))
      // a narrow screen opens the timeline at its right edge: now
      const wrap = out.querySelector('.tlwrap')
      if (wrap) wrap.scrollLeft = wrap.scrollWidth
    }

    // ---- headline ----
    function headline (r) {
      const reworkPct = r.credits ? Math.round(r.rework / r.credits * 100) : 0
      return h('div', { class: 'ftiles', testid: 'fleet-headline' },
        h('div', { class: 'ftile hero', testid: 'fleet-spent' },
          h('div', { class: 'k' }, 'Spent in this window'),
          h('div', { class: 'v' }, cr(r.credits), h('small', null, ' cr')),
          r.estimated > 0 ? h('div', { class: 's est', testid: 'fleet-estimated' }, `~${cr(r.estimated)} cr estimated — not yet settled by the provider`) : h('div', { class: 's' }, plural(lanesWithSpend(r), 'card') + ' spent'),
          tokenTotal(r.tokens) ? h('div', { class: 's', testid: 'fleet-tokens' }, tokenText(r.tokens)) : null),
        h('div', { class: 'ftile', testid: 'fleet-alltime' },
          h('div', { class: 'k' }, 'All time'),
          h('div', { class: 'v' }, cr(r.allTimeCredits), h('small', null, ' cr')),
          h('div', { class: 's' }, plural(r.allTimeCards || 0, 'card'))),
        h('div', { class: 'ftile', testid: 'fleet-rework' },
          h('div', { class: 'k' }, 'Rework'),
          h('div', { class: 'v' }, cr(r.rework), h('small', null, ` cr · ${reworkPct}%`)),
          h('div', { class: 's' }, `${cr(r.corrected)} corrected · ${cr(r.reproved)} re-proved`)),
        h('div', { class: 'ftile', testid: 'fleet-lanes' },
          h('div', { class: 'k' }, 'Lanes'),
          h('div', { class: 'v' }, String(r.peakLanes || 0), h('small', null, ' at peak')),
          h('div', { class: 's' }, r.running ? `${r.running} running now` : 'none running now'),
          r.busiest ? h('div', { class: 's', testid: 'fleet-busiest' }, `busiest ${when(r.busiest.from)} · ${dur(r.busiest.agentMs)} agent in ${dur(r.busiest.lenMs)}`) : null))
    }

    // ---- the clock: a part-to-whole of the cards' elapsed time ----
    function clockBar (r) {
      const total = r.elapsedMs || (r.agentMs + r.onYouMs + r.idleMs) || 0
      const parts = [
        { k: 'agent', label: 'agent working', ms: r.agentMs || 0 },
        { k: 'you', label: 'waiting on you', ms: r.onYouMs || 0 },
        { k: 'idle', label: 'nothing running', ms: r.idleMs || 0 }
      ]
      const pct = (ms) => total ? ms / total * 100 : 0
      const segs = parts.filter(p => p.ms > 0)
      return h('section', { class: 'fsec', testid: 'fleet-clock', 'aria-label': 'Where the card-hours went' },
        h('h3', null, 'Where the hours went', h('span', null, `${dur(total)} of card time`)),
        total
          ? h('div', { class: 'stack', role: 'img', 'aria-label': parts.map(p => `${p.label} ${dur(p.ms)}`).join(', ') },
            segs.map(p => h('span', {
              class: ['part', `c-${p.k}`],
              style: { '--w': pct(p.ms).toFixed(3) + '%' },
              data: { tip: `${p.label}\n${dur(p.ms)} · ${Math.round(pct(p.ms))}%` }
            })))
          : h('div', { class: 'vnote' }, 'No card time in this window.'),
        h('ul', { class: 'keyrow' }, parts.map(p => h('li', { testid: `fleet-clock-${p.k}` },
          h('i', { class: ['sw', `c-${p.k}`], 'aria-hidden': 'true' }),
          h('span', { class: 'l' }, p.label),
          h('b', null, dur(p.ms)),
          h('span', { class: 'p' }, `${Math.round(pct(p.ms))}%`)))))
    }

    // ---- breakdowns ----
    function bars (list, total, colorOf, testid) {
      const max = Math.max(...list.map(b => b.credits), 0.0001)
      return h('table', { class: 'fbars', testid },
        h('tbody', null, list.map(b => h('tr', { data: { tip: `${b.name}\n${cr(b.credits)} cr · ${total ? Math.round(b.credits / total * 100) : 0}%` } },
          h('th', { scope: 'row', title: b.name }, colorOf ? h('i', { class: 'sw', style: { '--sc': colorOf(b.name) }, 'aria-hidden': 'true' }) : null, b.name),
          h('td', { class: 'bc' }, h('span', { class: 'b', style: { '--w': (b.credits / max * 100).toFixed(2) + '%', '--sc': colorOf ? colorOf(b.name) : null } })),
          h('td', { class: 'n' }, cr(b.credits)),
          h('td', { class: 'p' }, total ? `${Math.round(b.credits / total * 100)}%` : '')))))
    }

    function byStage (r) {
      const list = [...(r.byStage || [])].sort((a, b) => rank(a.name) - rank(b.name))
      return h('section', { class: 'fsec', testid: 'fleet-by-stage', 'aria-label': 'Credits by stage' },
        h('h3', null, 'By stage'),
        list.length ? bars(list, r.credits, stageVar, 'fleet-stage-bars') : h('div', { class: 'vnote' }, 'No spend in this window.'))
    }

    function byModel (r) {
      const list = [...(r.byModel || [])].sort((a, b) => b.credits - a.credits)
      return h('section', { class: 'fsec', testid: 'fleet-by-model', 'aria-label': 'Credits by model' },
        h('h3', null, 'By model'),
        list.length ? bars(list, r.credits, null, 'fleet-model-bars') : h('div', { class: 'vnote' }, 'No spend in this window.'))
    }

    // ---- the timeline ----
    function timeline (r) {
      const lanes = r.lanes || []
      const to = +new Date(r.to)
      let from = +new Date(r.from)
      let zoomed = false
      if (v.win === 'all') {
        // the whole history starts at its first activity (the server's
        // from); leave a little air before it, and an hour when empty
        if (from >= to) from = to - 3600e3
        from -= Math.max((to - from) * 0.02, 60e3)
      } else {
        // a window longer than what ran in it would squeeze every mark
        // into its right edge: the track starts at the first activity
        // instead, with the same air, and never spans under an hour
        const first = firstMark(lanes)
        if (first && first > from) {
          const start = Math.max(from, Math.min(first - Math.max((to - first) * 0.02, 60e3), to - 3600e3))
          zoomed = start > from
          from = start
        }
      }
      const span = Math.max(to - from, 1)
      const x = (t) => Math.max(0, Math.min(100, (+new Date(t) - from) / span * 100))
      const flags = { stages: new Set(), wait: false, gate: false, endings: new Set(), running: false }
      const rows = lanes.map(l => lane(l, x, to, flags))
      if (!rows.length) {
        return h('section', { class: 'fsec', testid: 'fleet-timeline' }, h('h3', null, 'Timeline'), h('div', { class: 'vnote' }, 'Nothing ran in this window.'))
      }
      const ticks = tickTimes(from, to)
      return h('section', { class: 'fsec', testid: 'fleet-timeline', 'aria-label': 'Timeline, one lane per card' },
        h('h3', null, 'Timeline', h('span', null, plural(lanes.length, 'card') + (zoomed ? ` · from ${when(from)}, the first activity in this window` : ''))),
        legend(flags),
        h('div', { class: 'tlwrap', testid: 'fleet-timeline-scroll' },
          h('div', { class: 'tl' },
            h('div', { class: 'grid', 'aria-hidden': 'true' }, ticks.map(t => h('i', { style: { '--x': x(t).toFixed(3) + '%' } }))),
            rows,
            h('div', { class: 'axis', 'aria-hidden': 'true' },
              h('span', { class: 'lab' }),
              h('span', { class: 'ticks' },
                ticks.map(t => h('span', { class: 'tk', style: { '--x': x(t).toFixed(3) + '%' } }, tickLabel(t, span))),
                h('span', { class: 'now' }, new Date(to).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })))))))
    }

    function lane (l, x, to, flags) {
      const marks = []
      const openFrom = l.openWaitFrom ? +new Date(l.openWaitFrom) : 0
      for (const w of l.waits || []) {
        // the wait still open is in waits too, cut at the window's edge;
        // it is drawn once, as the open span below, so end any wait that
        // runs into it where the open one begins
        let stop = w.to ? +new Date(w.to) : to
        if (openFrom && stop >= to - 1000) stop = Math.min(stop, openFrom)
        if (stop <= +new Date(w.from)) continue
        flags.wait = true
        marks.push(span('wait', x(w.from), x(stop), `waiting on you\n${when(w.from)} – ${when(stop)} · ${dur(stop - +new Date(w.from))}`))
      }
      if (l.openWaitFrom) {
        flags.wait = true
        marks.push(span('wait open', x(l.openWaitFrom), 100, `waiting on you since ${when(l.openWaitFrom)}\nstill open`))
      }
      for (const b of l.blocks || []) {
        flags.stages.add(b.stage)
        const open = !b.to
        if (open) flags.running = true
        const el = span(['blk', `st-${b.stage}`, open && 'open'], x(b.from), x(b.to || to),
          `${b.stage}${b.role ? ' · ' + b.role : ''}\n${when(b.from)} – ${open ? 'running' : when(b.to)} · ${dur((b.to ? +new Date(b.to) : to) - +new Date(b.from))}`)
        marks.push(el)
      }
      for (const g of l.gates || []) {
        flags.gate = true
        marks.push(h('span', { class: 'gate', style: { '--x': x(g).toFixed(3) + '%' }, data: { tip: `gate crossed\n${when(g)}` } }))
      }
      const end = ENDINGS[l.ending] || (l.landedAt ? ENDINGS.landed : null)
      if (l.landedAt && end) {
        flags.endings.add(end)
        marks.push(h('span', { class: ['land', end.cls], style: { '--x': x(l.landedAt).toFixed(3) + '%' }, data: { tip: `${end.label}\n${when(l.landedAt)}` } }, end.glyph))
      }
      const summary = [`${l.id} ${l.title}`, `${cr(l.credits)} credits`, l.redo ? `${cr(l.redo)} rework` : null, l.running ? 'running' : null,
        l.openWaitFrom ? `waiting on you since ${when(l.openWaitFrom)}` : null, l.landedAt && end ? `${end.label} ${when(l.landedAt)}` : null,
        tokenTotal(l.tokens) ? tokenText(l.tokens) : null, l.note || null].filter(Boolean).join(', ')
      return h('div', { class: ['lane', l.running && 'running'], testid: `fleet-lane-${l.id}`, tabindex: '0', role: 'group', 'aria-label': summary },
        h('div', { class: 'lab' },
          h('button', { type: 'button', class: 'id', tabindex: '-1', title: `Open ${l.id}`, onclick: () => { ctx.close(); ctx.select(l.id) } }, l.id),
          h('span', { class: 't', title: l.title }, l.title),
          h('span', { class: 'c', title: [tokenTotal(l.tokens) ? tokenText(l.tokens) : '', l.note || ''].filter(Boolean).join(' · ') || null }, cr(l.credits)),
          l.openWaitFrom ? h('span', { class: 'w' }, `on you since ${when(l.openWaitFrom)}`) : l.running ? h('span', { class: 'r' }, 'running') : null),
        h('div', { class: 'track' }, marks))
    }

    function span (cls, x0, x1, tipText) {
      return h('span', { class: cls, style: { '--x': x0.toFixed(3) + '%', '--w': Math.max(0, x1 - x0).toFixed(3) + '%' }, data: { tip: tipText } })
    }

    function legend (f) {
      const stages = STAGE_ORDER.filter(s => f.stages.has(s)).concat([...f.stages].filter(s => !STAGE_ORDER.includes(s)))
      return h('ul', { class: 'keyrow legend', testid: 'fleet-legend' },
        stages.map(s => h('li', null, h('i', { class: ['sw', `st-${s}`], 'aria-hidden': 'true' }), s === 'open' ? 'session' : s)),
        f.running ? h('li', null, h('i', { class: 'sw run', 'aria-hidden': 'true' }), 'still running') : null,
        f.wait ? h('li', null, h('i', { class: 'sw c-you hatch', 'aria-hidden': 'true' }), 'waiting on you') : null,
        f.gate ? h('li', null, h('i', { class: 'dia', 'aria-hidden': 'true' }), 'gate crossed') : null,
        Object.values(ENDINGS).filter(e => f.endings.has(e)).map(e => h('li', null, h('i', { class: ['ck', e.cls], 'aria-hidden': 'true' }, e.glyph), e.label)))
    }

    // ---- the one tooltip: marks carry data-tip; hover or focus shows it ----
    function showTip (el, e) {
      const text = el.dataset.tip
      if (!text) return hideTip()
      const [first, ...rest] = text.split('\n')
      // values lead, the name follows
      tip.replaceChildren(...rest.map(l => h('b', null, l)), h('span', null, first))
      tip.hidden = false
      const box = body.getBoundingClientRect()
      const r = el.getBoundingClientRect()
      const px = e && e.clientX ? e.clientX : r.left + r.width / 2
      const tw = tip.offsetWidth
      const left = Math.max(4, Math.min(px - box.left - tw / 2, box.width - tw - 4))
      tip.style.setProperty('--tx', `${left + body.scrollLeft}px`)
      tip.style.setProperty('--ty', `${r.top - box.top + body.scrollTop - tip.offsetHeight - 6}px`)
    }
    function hideTip () { tip.hidden = true }
    out.addEventListener('pointerover', (e) => { const el = e.target.closest('[data-tip]'); if (el) showTip(el, e); else hideTip() })
    out.addEventListener('pointermove', (e) => { const el = e.target.closest('[data-tip]'); if (el) showTip(el, e) })
    out.addEventListener('pointerleave', hideTip)
    out.addEventListener('focusin', (e) => {
      const lane = e.target.closest('.lane')
      if (lane) { const first = lane.querySelector('[data-tip]'); if (first) showTip(first) }
    })
    out.addEventListener('focusout', hideTip)
    body.addEventListener('scroll', hideTip, { passive: true })

    const timer = setInterval(() => { if (!v.loading && document.visibilityState === 'visible') load() }, REFRESH_MS)
    drawBar()
    load()
    return () => { v.alive = false; clearInterval(timer) }
  }
})

// ENDINGS is how a lane's closing mark reads, by the card's ending: only
// a landing is the green check; a session continued as a spec, or a goal
// card dropped, closed without its work reaching the base.
const ENDINGS = {
  landed: { label: 'landed', glyph: '✔', cls: null },
  handed_off: { label: 'handed off', glyph: '↗', cls: 'off' },
  dropped: { label: 'dropped', glyph: '✕', cls: 'off' }
}

// firstMark is the earliest instant any lane draws, or 0 for none.
function firstMark (lanes) {
  let first = 0
  const see = (t) => { const n = t ? +new Date(t) : 0; if (n && (!first || n < first)) first = n }
  for (const l of lanes) {
    for (const b of l.blocks || []) see(b.from)
    for (const w of l.waits || []) see(w.from)
    for (const g of l.gates || []) see(g)
    see(l.openWaitFrom)
    see(l.landedAt)
  }
  return first
}

function rank (stage) { const i = STAGE_ORDER.indexOf(stage); return i < 0 ? 99 : i }
function lanesWithSpend (r) { return (r.lanes || []).filter(l => l.credits > 0).length }

function when (t) {
  if (!t) return 'now'
  const d = new Date(t)
  const now = new Date()
  const hm = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  if (d.toDateString() === now.toDateString()) return hm
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + hm
}

function spanText (r, all) {
  if (all) return `the whole history, since ${when(r.from)}`
  return `${when(new Date(r.from))} – now`
}

function tokenTotal (t) { return t ? (t.input || 0) + (t.cached || 0) + (t.output || 0) : 0 }
function tokenText (t) {
  const n = tokenTotal(t)
  const fmt = n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? Math.round(n / 1e3) + 'k' : String(n)
  const inside = (t.input || 0) + (t.cached || 0)
  return `${fmt} tokens` + (inside && t.cached ? ` · ${Math.round(t.cached / inside * 100)}% cached` : '')
}

// tickTimes picks round instants across [from, to]: hours for a day,
// days for a week or a month, weeks or months beyond.
const HOUR = 3600e3
const DAY = 24 * HOUR
function tickTimes (from, to) {
  const span = to - from
  const steps = [HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 7 * DAY, 14 * DAY, 30 * DAY, 91 * DAY]
  const step = steps.find(s => span / s <= 8) || 182 * DAY
  const out = []
  const start = new Date(from)
  if (step < DAY) {
    start.setMinutes(0, 0, 0)
    const hs = step / HOUR
    start.setHours(Math.ceil(start.getHours() / hs) * hs)
  } else {
    start.setHours(0, 0, 0, 0)
    start.setDate(start.getDate() + 1)
  }
  for (let t = +start; t < to; t += step) if (t > from) out.push(t)
  return out
}

function tickLabel (t, span) {
  const d = new Date(t)
  if (span <= 2 * DAY) {
    if (d.getHours() === 0) return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}
