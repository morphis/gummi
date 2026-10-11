// panel.js — the right side: a rail of icons that opens one surface at a
// time beside the conversation (never over it) — Memory, Spec, Changes
// (the diff, the commits and the pull request, behind one switch), Run and
// Terminal. It starts closed; an icon opens its surface, the open one's
// icon or `]` closes it, and each surface has a width of its own, which
// the resizer changes. On a phone the same row leads the card's screen as
// tabs, with its Thread first. It fetches the open tab for the open card,
// refetches it when the card changes, and hands each tab module an entry
// { data, fresh, err } to draw. A route the server does not answer yet shows a quiet "not
// available yet" instead of an error.

import { $, h, clear, icon, isMobile, storage } from './dom.js?v=__ASSET_V__'
import { on, set, state, shownTab } from './store.js?v=__ASSET_V__'
import { write as writeHash } from './router.js?v=__ASSET_V__'
import { rememberTab } from './selection.js?v=__ASSET_V__'
import { memoryTab } from './memory.js?v=__ASSET_V__'
import { specTab } from './spec.js?v=__ASSET_V__'
import { diffTab } from './diff.js?v=__ASSET_V__'
import { logTab } from './log.js?v=__ASSET_V__'
import { prTab } from './pr.js?v=__ASSET_V__'
import { statsTab } from './stats.js?v=__ASSET_V__'
import { terminalTab } from './terminal.js?v=__ASSET_V__'

const TABS = [memoryTab, specTab, diffTab, logTab, prTab, statsTab, terminalTab]
const byName = Object.fromEntries(TABS.map(t => [t.name, t]))

// SURFACES is what the icons open. One with several tabs shows a switch
// between them in its header; `as` is what a tab is called there.
const SURFACES = [
  { name: 'memory', label: 'Memory', icon: 'memory', tabs: [memoryTab] },
  { name: 'spec', label: 'Spec', icon: 'doc', tabs: [specTab] },
  { name: 'changes', label: 'Changes', icon: 'branch', tabs: [diffTab, logTab, prTab], as: { diff: 'Files', log: 'Commits', pr: 'Pull request' } },
  { name: 'run', label: 'Run', icon: 'fleet', tabs: [statsTab] },
  { name: 'terminal', label: 'Terminal', icon: 'terminal', tabs: [terminalTab] }
]
const surfaceOf = (tab) => SURFACES.find(s => s.tabs.some(t => t.name === tab))

// surfaces is what the open card has: a tab may hide itself for a kind of
// card it has nothing for (the spec, on a session), and a surface with no
// tab left has no icon
const surfaces = () => SURFACES.map(s => ({ ...s, tabs: s.tabs.filter(t => !t.hidden?.(state.card)) })).filter(s => s.tabs.length)

// lastIn is the tab a person last had open on a surface: where its icon
// goes back to
const lastIn = {}

let cache = {} // tab name -> { data, fresh, err, loading, stale }
let cacheFor = null
let ctx = {}

export function initPanel (c) {
  ctx = c
  on(['sel'], () => {
    cache = {}
    cacheFor = state.sel
    // no card (a new session's draft): nothing loads, so the pane and its
    // tabs are cleared here or they keep the last card's
    applyHidden()
    if (!state.sel) {
      set({ diffPending: 0 })
      // nor does the address bar keep naming the card that was open: a
      // reload must not reopen it under the draft
      if (location.hash) try { history.replaceState(history.state, '', location.pathname + location.search) } catch {}
      renderTabs()
      renderPane(false)
      return
    }
    prefetch()
    load(state.tab)
  })
  on(['tab'], () => {
    // however the tab came to show (a press, a link, a card's own last
    // one), it is where its surface's icon goes back to
    const s = surfaceOf(state.tab)
    if (s) lastIn[s.name] = state.tab
    load(state.tab); renderTabs(); applyWidth(); renderPane(false)
  })
  on(['cardRev'], () => {
    for (const k of Object.keys(cache)) cache[k].stale = true
    load(state.tab, true)
  })
  on(['card'], () => {
    // the card has just said it has no such tab: move to its diff
    // (without opening the panel the way a person's choice of a tab does).
    // No card yet is the next card's head still in flight, which says
    // nothing: a tab only a session has must survive the switch to one.
    if (state.card && byName[state.tab]?.hidden?.(state.card)) { set({ tab: 'diff' }); writeHash(state.sel, shownTab()) }
    prefetch()
    renderTabs()
    // the tab may have been drawn before the head arrived (its read can
    // answer first): one drawn open for a card that has closed is redrawn
    if (drawnClosed !== null && drawnClosed !== isClosed()) renderPane(true)
  })
  on(['rightHidden'], applyHidden)
  // on a phone the row holds the card's Thread too, and says which is shown
  on(['view'], renderTabs)
  // a window that crossed a phone's width shows a different thing beside
  // (or instead of) the thread: the address follows what shows now
  matchMedia('(max-width:760px)').addEventListener('change', () => { renderTabs(); writeHash(state.sel, shownTab()) })
  initResizer()
  applyHidden()
  renderTabs()
}

export function setTab (t) {
  if (!byName[t] || byName[t].hidden?.(state.card)) return
  // a phone's tab opens its document's screen; only beside the thread is
  // there a surface to open, and to find open again on the next visit
  if (isMobile()) set({ view: 'panel' })
  else if (state.rightHidden) { set({ rightHidden: false }); storage.set('rightHidden', false) }
  set({ tab: t })
  writeHash(state.sel, t)
  rememberTab(state.sel, t)
}

// openSurface is a press of a surface's icon: it opens the surface on the
// tab last seen there, and closes it when it is the one already open. A
// phone's row is plain tabs, which close nothing.
function openSurface (s) {
  const open = surfaceOf(state.tab)?.name === s.name
  if (!isMobile() && open && !state.rightHidden) { togglePanel(); return }
  if (isMobile() && open && state.view === 'panel') return
  const last = s.tabs.find(t => t.name === lastIn[s.name])
  setTab((open ? byName[state.tab] : last || s.tabs[0]).name)
}

export function togglePanel () {
  if (isMobile()) { set({ view: state.view === 'panel' ? 'thread' : 'panel' }); return }
  const hidden = !state.rightHidden
  set({ rightHidden: hidden })
  storage.set('rightHidden', hidden)
  // the address names a tab only while one shows
  writeHash(state.sel, shownTab())
}

function applyHidden () {
  // no card (a new session's draft, an empty board) has no documents and
  // no icons to close one with: nothing stands open beside it
  $('#app').classList.toggle('no-right', !!state.rightHidden || !state.sel)
  renderTabs()
  applyWidth()
}

// applyWidth gives the open surface its width: the one a person dragged
// it to, else the surface's own (app.css).
function applyWidth () {
  const s = surfaceOf(state.tab)
  const right = $('.right')
  right.dataset.surface = s?.name || ''
  const saved = s && storage.get(`rightW.${s.name}`, null)
  if (saved) right.style.setProperty('--right-w', saved + 'px')
  else right.style.removeProperty('--right-w')
  syncResizer()
}

// syncResizer makes the separator say how wide the surface now is
// (initResizer sets it)
let syncResizer = () => {}

// prefetch loads the diff early when the pinned decision can carry its
// comments, so the answer can say how many will go with it.
function prefetch () {
  const d = state.card?.decision
  if (d?.options?.some(o => o.carriesComments) && !cache.diff) load('diff')
}

function entry (name) {
  if (cacheFor !== state.sel) { cache = {}; cacheFor = state.sel }
  return (cache[name] ||= { data: null, fresh: null, err: null, loading: false, stale: true })
}

function load (name, refresh = false) {
  const tab = byName[name]
  const id = state.sel
  if (!tab || !id) return
  const e = entry(name)
  // a refresh asked for while a fetch is out runs again when it lands:
  // the one in flight may have left before what changed
  if (e.loading && refresh) { e.again = true; return }
  if (e.loading || (!e.stale && !refresh)) { if (name === state.tab) renderPane(true); return }
  e.loading = true
  if (name === state.tab && !e.data && !e.err) renderPane(false)
  tab.fetch(id).then((data) => {
    if (state.sel !== id) return
    if (refresh && e.data && tab.hold?.(e.data, data)) e.fresh = data
    else { e.data = data; e.fresh = null }
    e.err = null
    if (name === 'diff') set({ diffPending: e.data?.pendingComments || 0 })
  }).catch((err) => {
    if (state.sel !== id) return
    e.err = err
  }).finally(() => {
    if (state.sel !== id) return
    e.loading = false
    e.stale = false
    renderTabs()
    if (name === state.tab) renderPane(true)
    if (e.again) { e.again = false; load(name, true) }
  })
}

function renderTabs () {
  const box = $('#tabs')
  // the row is drawn anew: focus goes back to the tab it was on, open or
  // closed, not to <body>
  const a = document.activeElement
  const hadFocus = box.contains(a) && a.getAttribute('role') === 'tab' ? (a.dataset.surface || a.dataset.tab) : null
  clear(box)
  // a phone's card screen has one row of tabs: its thread, then its
  // documents. Beside the thread they are a column of icons.
  const phone = isMobile()
  const list = h('div', { class: 'tablist', role: 'tablist', 'aria-label': 'Documents', 'aria-orientation': phone ? 'horizontal' : 'vertical' })
  list.addEventListener('keydown', tabKeys)
  box.append(list)
  if (phone) {
    const on = state.view === 'thread'
    list.append(h('button', {
      class: ['tab', on && 'on'],
      role: 'tab',
      type: 'button',
      id: 'tab-thread',
      tabindex: on ? '0' : '-1',
      data: { tab: 'thread' },
      'aria-selected': String(on),
      testid: 'tab-thread',
      onclick: () => set({ view: 'thread' })
    }, h('span', { class: 'lbl' }, 'Thread')))
  }
  const open = phone ? state.view === 'panel' : !state.rightHidden
  const cur = surfaceOf(state.tab)?.name
  // a session's draft has no documents: the row holds no tabs to pick
  const list2 = state.sel ? surfaces() : []
  list2.forEach((s, i) => {
    const on = open && cur === s.name
    const es = s.tabs.map(t => cache[t.name])
    // dim once its first tab has loaded with nothing to show, and no
    // other of its tabs loaded with something
    const none = (e, j) => e && (e.err || (e.data && s.tabs[j].empty?.(e.data)))
    const dim = none(es[0], 0) && es.every((e, j) => !e || (!e.data && !e.err) || none(e, j))
    const [n, hot] = countOf(s.tabs)
    list.append(h('button', {
      class: ['tab', on && 'on', !on && dim && 'dim'],
      role: 'tab',
      type: 'button',
      id: `tab-${s.tabs[0].name}`,
      tabindex: on || (!phone && !open && i === 0) ? '0' : '-1',
      data: { tab: s.tabs[0].name, surface: s.name },
      'aria-selected': String(on),
      'aria-controls': 'pane',
      testid: `tab-${s.tabs[0].name}`,
      title: phone ? null : `${s.label} (${s.tabs[0].key})${on ? ' — press again to close' : ''}`,
      onclick: () => openSurface(s)
    }, icon(s.icon), h('span', { class: 'lbl' }, s.label), n ? h('span', { class: ['n', hot && 'hot'] }, hot ? `${n}+` : String(n)) : null))
  })
  renderSurfaceHead(list2.find(s => s.name === cur), phone)
  if (state.sel) $('#pane').setAttribute('aria-labelledby', document.getElementById(`tab-${state.tab}`) ? `tab-${state.tab}` : `tab-${surfaceOf(state.tab)?.tabs[0].name}`)
  else $('#pane').removeAttribute('aria-labelledby')
  if (hadFocus) (list.querySelector(`[data-surface="${hadFocus}"]`) || list.querySelector(`[data-tab="${hadFocus}"]`) || list.querySelector('[aria-selected="true"]'))?.focus()
}

// countOf is the figure a row of tabs shows: the first of them that has
// one, and whether newer data waits behind it.
function countOf (tabs) {
  for (const t of tabs) {
    const e = cache[t.name]
    const n = e?.data && t.count ? t.count(e.data) : 0
    if (n) return [n, !!e.fresh]
  }
  return [0, false]
}

// renderSurfaceHead draws the open surface's one header line: its name,
// the switch between its tabs when it has several, the branch the changes
// are on, and the way to close it.
function renderSurfaceHead (s, phone) {
  const box = $('#surface-head')
  const hadFocus = box.contains(document.activeElement) && document.activeElement.getAttribute('role') === 'tab'
  clear(box)
  box.hidden = !s
  if (!s) return
  box.append(h('b', { class: 'st', testid: 'surface-title' }, s.label))
  if (s.tabs.length > 1) {
    const sw = h('div', { class: 'seg', role: 'tablist', 'aria-label': s.label, testid: 'surface-switch' }, s.tabs.map(t => {
      const on = state.tab === t.name
      const e = cache[t.name]
      const [n, hot] = t === s.tabs[0] ? [0, false] : countOf([t])
      return h('button', {
        class: [on && 'on', !on && e && (e.err || (e.data && t.empty?.(e.data))) && 'dim'],
        role: 'tab',
        type: 'button',
        // the first tab's id is its surface's icon's
        id: t === s.tabs[0] ? null : `tab-${t.name}`,
        tabindex: on ? '0' : '-1',
        data: { tab: t.name },
        'aria-selected': String(on),
        'aria-controls': 'pane',
        testid: t === s.tabs[0] ? `${s.name}-${t.name}` : `tab-${t.name}`,
        title: `${t.label} (${t.key})`,
        onclick: () => setTab(t.name)
      }, s.as?.[t.name] || t.label, n ? h('span', { class: ['n', hot && 'hot'] }, hot ? `${n}+` : String(n)) : null)
    }))
    sw.addEventListener('keydown', (e) => {
      const names = s.tabs.map(t => t.name)
      const i = names.indexOf(e.target.dataset?.tab)
      const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: names.length - 1 }[e.key]
      if (i < 0 || to === undefined) return
      e.preventDefault()
      setTab(names[(to + names.length) % names.length])
      box.querySelector('.seg [aria-selected="true"]')?.focus()
    })
    box.append(sw)
  }
  const c = state.card
  if (s.name === 'changes' && c) {
    box.append(h('span', { class: 'on' },
      c.branch ? h('span', { class: 'mono', testid: 'card-branch', title: c.branch }, c.branch) : null,
      c.base && c.branch ? h('span', null, ' onto ', h('span', { class: 'mono' }, c.base)) : null,
      c.adopted ? h('span', null, ' · adopted') : null,
      c.scratch ? h('span', { testid: 'card-scratch' }, 'scratch tree · no branch') : null))
  }
  if (!phone) {
    box.append(h('button', {
      class: 'iconbtn close', type: 'button', testid: 'panel-close', title: 'Close (])', 'aria-label': `Close ${s.label}`,
      // the button goes with its surface: focus moves to the icon that opens it again
      onclick: () => { togglePanel(); $(`#tab-${s.tabs[0].name}`)?.focus() }
    }, icon('close')))
  }
  if (hadFocus) box.querySelector('.seg [aria-selected="true"]')?.focus()
}

// tabKeys moves between the surfaces with the arrow keys, Home and End, as
// a tablist does; the one chosen opens.
function tabKeys (e) {
  const list = surfaces()
  const i = list.findIndex(s => s.name === e.target.dataset?.surface)
  if (i < 0) return
  const to = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: list.length - 1 }[e.key]
  if (to === undefined) return
  e.preventDefault()
  const s = list[(to + list.length) % list.length]
  setTab((s.tabs.find(t => t.name === lastIn[s.name]) || s.tabs[0]).name)
  $(`#tab-${s.tabs[0].name}`)?.focus()
}

// a landed (or otherwise finished) card takes no more review input: its
// notes and comments have no one left to go to
function isClosed () { return state.card?.stage === 'done' || !!state.card?.landed }

// drawnClosed is isClosed() as the pane last drew a tab, null when it drew
// none
let drawnClosed = null

function renderPane (keepScroll) {
  const pane = $('#pane')
  const top = pane.scrollTop
  // a box being written in (a spec note, a diff comment) is redrawn by
  // its tab from the draft it keeps; the caret goes back where it was
  const a = document.activeElement
  const typing = pane.contains(a) && a.closest?.('[data-draft]') ? { key: a.closest('[data-draft]').dataset.draft, from: a.selectionStart, to: a.selectionEnd } : null
  clear(pane)
  drawnClosed = null
  pane.dataset.tab = state.tab
  const tab = byName[state.tab]
  const e = entry(state.tab)
  // a diff's wrapped lines are the diff's alone (diff.js sets it again)
  pane.classList.remove('wrap')
  // no card (a new session's draft): nothing exists yet to have documents
  if (!state.sel) return
  if (e.err) {
    pane.append(e.err.notBuilt
      ? h('div', { class: 'empty', testid: 'panel-unavailable' }, h('b', null, `The ${tab.label} tab is not available yet`), 'This board’s server does not serve it yet.')
      : h('div', { class: 'empty err', testid: 'panel-error' }, h('b', null, `The ${tab.label} tab did not load`), e.err.message,
        h('button', { class: 'btn', type: 'button', onclick: () => { e.err = null; e.stale = true; load(state.tab) } }, 'Try again')))
  } else if (!e.data && e.loading) {
    pane.append(h('div', { class: 'empty', testid: 'panel-loading' }, h('span', { class: 'spinner' })))
  } else {
    drawnClosed = isClosed()
    const tctx = {
      id: state.sel,
      card: state.card,
      closed: drawnClosed,
      person: state.session?.person,
      setTab,
      select: ctx.select,
      rerender: () => renderPane(true),
      // swap shows new data (or refetches, given null) without the banner
      swap: (data) => {
        if (data) { e.data = data; e.fresh = null; if (tab.name === 'diff') set({ diffPending: data.pendingComments || 0 }); renderTabs(); renderPane(true) } else { e.stale = true; e.fresh = null; load(tab.name) }
      }
    }
    try { tab.render(pane, e, tctx) } catch (err) {
      console.error(err)
      pane.append(h('div', { class: 'empty err' }, h('b', null, `The ${tab.label} tab could not be drawn`), String(err.message || err)))
    }
  }
  pane.scrollTop = keepScroll ? top : 0
  if (typing) {
    const ta = [...pane.querySelectorAll('[data-draft]')].find(el => el.dataset.draft === typing.key)?.querySelector('textarea')
    if (ta) {
      ta.focus({ preventScroll: true })
      try { ta.setSelectionRange(typing.from, typing.to) } catch {}
    }
  }
}

// ---- the resizer: the conversation keeps 400px, a surface 326px ----
// (a surface's width counts the resizer, its own 6px left edge, and not
// the icons beside it)
function initResizer () {
  const r = $('#resizer')
  let drag = false
  const setW = (w) => {
    const main = $('.main').getBoundingClientRect()
    const rail = $('.rail').getBoundingClientRect().width
    const icons = $('#tabs').getBoundingClientRect().width
    const clamped = Math.round(Math.max(326, Math.min(w, main.width - rail - icons - 400)))
    storage.set(`rightW.${surfaceOf(state.tab)?.name}`, clamped)
    applyWidth()
    sync()
  }
  // the separator says how much of the width the panel has
  const sync = () => requestAnimationFrame(() => {
    const main = $('.main').getBoundingClientRect().width
    const w = $('#surface').getBoundingClientRect().width
    if (!main) return
    r.setAttribute('aria-valuenow', String(Math.round((w / main) * 100)))
    r.setAttribute('aria-valuetext', w ? `${Math.round(w)} pixels wide` : 'closed')
  })
  r.setAttribute('aria-valuemin', '0')
  r.setAttribute('aria-valuemax', '100')
  syncResizer = sync
  sync()
  window.addEventListener('resize', sync)
  r.addEventListener('pointerdown', (e) => { drag = true; r.classList.add('drag'); r.setPointerCapture(e.pointerId) })
  r.addEventListener('pointermove', (e) => {
    if (!drag) return
    setW($('#surface').getBoundingClientRect().right - e.clientX)
  })
  const stop = () => { drag = false; r.classList.remove('drag') }
  r.addEventListener('pointerup', stop)
  r.addEventListener('pointercancel', stop)
  r.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const w = $('#surface').getBoundingClientRect().width
    setW(w + (e.key === 'ArrowLeft' ? 24 : -24))
  })
  r.addEventListener('dblclick', () => { storage.set(`rightW.${surfaceOf(state.tab)?.name}`, null); applyWidth(); sync() })
}
