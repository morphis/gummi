// dom.js — how this page builds elements, and the few words and glyphs every
// module shares. Elements come from h(), never from HTML strings, so server
// text is always text. Styles are set through element.style.setProperty
// (allowed by the CSP) and never as a style="" attribute (refused by it).

const PROPS = new Set(['value', 'checked', 'disabled', 'hidden', 'open', 'selected', 'tabIndex', 'htmlFor', 'indeterminate'])

export function h (tag, props, ...kids) {
  const el = document.createElement(tag)
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue
      if (k === 'class') el.className = Array.isArray(v) ? v.filter(Boolean).join(' ') : v
      else if (k === 'style') setVars(el, v)
      else if (k === 'data') for (const [d, dv] of Object.entries(v)) { if (dv != null) el.dataset[d] = dv }
      else if (k === 'testid') el.dataset.testid = v
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v)
      else if (PROPS.has(k)) el[k] = v
      else el.setAttribute(k, v === true ? '' : String(v))
    }
  }
  append(el, kids)
  return el
}

export function append (el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false || k === '') continue
    el.append(k instanceof Node ? k : document.createTextNode(String(k)))
  }
  return el
}

// setVars sets style properties (custom properties included) one by one.
export function setVars (el, vars) {
  for (const [p, val] of Object.entries(vars)) {
    if (val == null) el.style.removeProperty(p)
    else el.style.setProperty(p, String(val))
  }
}

export function clear (el) {
  while (el.firstChild) el.firstChild.remove()
  return el
}

export function $ (sel, root = document) { return root.querySelector(sel) }
export function $$ (sel, root = document) { return [...root.querySelectorAll(sel)] }

// ---- icons: fixed markup, never built from data ----
const ICONS = {
  rail: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>',
  panel: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M14 4v16"/>',
  search: '<circle cx="11" cy="11" r="6"/><path d="M20 20l-4-4"/>',
  moon: '<path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  fleet: '<path d="M4 19V9M10 19V5M16 19v-7M22 19H2"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  back: '<path d="M15 5l-7 7 7 7"/>',
  cards: '<rect x="4" y="4" width="16" height="4" rx="1.5"/><rect x="4" y="10" width="16" height="4" rx="1.5"/><rect x="4" y="16" width="16" height="4" rx="1.5"/>',
  thread: '<path d="M4 5h16v11H9l-5 4z"/>',
  doc: '<path d="M7 4h7l4 4v12H7z"/><path d="M14 4v4h4"/>',
  goal: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="4"/><circle cx="12" cy="12" r=".5"/>',
  stack: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
  import: '<path d="M12 4v11M7 10l5 5 5-5M5 20h14"/>',
  bug: '<rect x="8" y="7" width="8" height="12" rx="4"/><path d="M12 7V4M4 12h4M16 12h4M5 7l3 2M19 7l-3 2M5 18l3-2M19 18l-3-2"/>',
  doctor: '<path d="M12 21s-7-4.5-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 11c0 5.5-7 10-7 10z"/>',
  bell: '<path d="M6 16V11a6 6 0 0 1 12 0v5l2 2H4z"/><path d="M10 21h4"/>',
  unpair: '<path d="M15 7h3a4 4 0 0 1 0 8h-3M9 17H6a4 4 0 0 1 0-8h3M4 4l16 16"/>'
}

export function icon (name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('viewBox', '0 0 24 24')
  svg.setAttribute('class', 'i')
  svg.setAttribute('aria-hidden', 'true')
  svg.innerHTML = ICONS[name] || ''
  return svg
}

// ---- shared words ----
export const STAGES = ['todo', 'plan', 'implement', 'verify', 'done']
export const GLYPH = { todo: '○', plan: '●', implement: '●', verify: '◐', done: '✔', open: '◆' }
export const ROLE = { plan: 'architect', implement: 'implementer', verify: 'verify', open: 'session' }

// kindTag is the rail's short kind: the card id's prefix (FD, BG, RS, FF, GL).
export function kindTag (row) {
  const m = /^([A-Z]+)-/.exec(row.id || '')
  return m ? m[1] : (row.kind || '').slice(0, 2).toUpperCase()
}

// REPO_SLOTS is the repo palette's size: theme.RepoSlots on the server.
export const REPO_SLOTS = 6

// repoSlot maps a repository name to its palette slot (--r0 … --r5). It is
// theme.RepoSlot's FNV-1a hash, so the web rail and the TUI agree on a
// repo's color.
export function repoSlot (name) {
  let x = 0x811c9dc5
  for (const b of new TextEncoder().encode(name)) x = Math.imul(x ^ b, 0x01000193)
  return (x >>> 0) % REPO_SLOTS
}

// needsColor maps a needs-you entry to the colour token it is tinted with.
export function needsColor (needs, stage) {
  if (!needs) return 'var(--accent)'
  switch (needs.color) {
    case 'err': return 'var(--err)'
    case 'warn': return 'var(--warn)'
    case 'info': return 'var(--accent)'
    // a clean gate is not a warning: it takes its stage's hue
    case 'ok': return stageVar(stage)
    default: return stageVar(stage)
  }
}

export function stageVar (stage) {
  return { todo: 'var(--s-todo)', plan: 'var(--s-plan)', implement: 'var(--s-impl)', verify: 'var(--s-verify)', done: 'var(--s-done)', open: 'var(--s-open)' }[stage] || 'var(--accent)'
}

export function needsWord (needs, stage) {
  if (!needs) return ''
  // the server says the word; the kind is only a fallback for an older one
  if (needs.word) return needs.word
  switch (needs.kind) {
    case 'gate': return stage === 'plan' ? 'design gate' : `${stage} gate`
    case 'question': return 'question'
    case 'failure': return stage === 'verify' ? 'verify failed' : 'failed'
    case 'budget': return 'budget'
    default: return needs.kind
  }
}

export function decisionWord (d, stage) {
  // the server says the word (a verify decision may be a pass or a
  // failure, an idle card may be working); the kind is only a fallback
  if (d.word) return d.word
  switch (d.kind) {
    case 'gate': return stage === 'plan' ? 'design gate' : `${stage} gate`
    case 'ask': return 'question'
    case 'verify': return 'verify failed'
    case 'conflict': return 'conflict'
    case 'budget': return 'envelope spent'
    case 'idle': return 'idle'
    case 'confirm': return 'confirm'
    default: return d.kind
  }
}

export function decisionColor (d, stage) {
  if (d.tone) return needsColor({ color: d.tone }, stage)
  switch (d.kind) {
    case 'gate': return stageVar(stage)
    case 'verify': case 'conflict': return 'var(--err)'
    case 'budget': case 'confirm': return 'var(--warn)'
    case 'idle': return 'var(--fg3)'
    default: return 'var(--accent)'
  }
}

// ---- formatting ----
export function cr (n) { return (Number(n) || 0).toFixed(1) }

// ctxMeter renders a session's context-window occupancy: a green/yellow/red
// bar (the same thresholds as the budget nudges, DESIGN §5.1) with a
// breakdown on hover — tokens used, the limit, and what is left. null while
// the backend has reported no limit, which every surface that places this
// takes as "nothing to show".
export function ctxMeter (ctx, testid = 'ctx-meter') {
  if (!ctx || !ctx.limit) return null
  const pct = Math.min(100, ctx.tokens / ctx.limit * 100)
  const tier = pct >= 95 ? 'err' : pct >= 80 ? 'warn' : 'ok'
  const left = Math.max(0, ctx.limit - ctx.tokens)
  const title = [
    `${ctx.tokens.toLocaleString()} of ${ctx.limit.toLocaleString()} tokens used (${Math.round(pct)}%)`,
    `${left.toLocaleString()} tokens left`
  ].join('\n')
  return h('span', { class: ['ctxm', tier], testid, title },
    h('span', { class: 'bar' }, h('i', { style: { '--pct': pct.toFixed(1) + '%' } })),
    `${Math.round(pct)}% context`)
}

export function clock (t) {
  if (!t) return ''
  const d = new Date(t)
  if (isNaN(d) || d.getFullYear() < 2000) return ''
  const now = new Date()
  const hm = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  if (d.toDateString() === now.toDateString()) return hm
  const y = new Date(now); y.setDate(now.getDate() - 1)
  if (d.toDateString() === y.toDateString()) return `yesterday ${hm}`
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + hm
}

export function dur (ms) {
  ms = Number(ms) || 0
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m`
  const hr = Math.floor(m / 60)
  return `${hr}h ${m % 60}m`
}

export function initials (name) {
  const parts = String(name || '?').trim().split(/\s+/)
  return ((parts[0] || '?')[0] + (parts.length > 1 ? parts[parts.length - 1][0] : (parts[0][1] || ''))).toUpperCase()
}

export function plural (n, one, many = one + 's') { return `${n} ${n === 1 ? one : many}` }

export function isMobile () { return matchMedia('(max-width:760px)').matches }

// storage wraps localStorage: a private window or blocked storage must
// never break the page, only forget.
export const storage = {
  get (k, d = null) { try { const v = localStorage.getItem('gummi-web:' + k); return v == null ? d : JSON.parse(v) } catch { return d } },
  set (k, v) { try { localStorage.setItem('gummi-web:' + k, JSON.stringify(v)) } catch { /* forget */ } }
}
