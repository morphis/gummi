// panel.js — the right panel: the Spec, Diff, Log, PR and Stats tabs beside the
// conversation (never over it), the resizer between them, and hiding it
// with `]`. On a phone the tabs lead the card's screen, with its Thread first. It fetches the open tab for the open card, refetches it when
// the card changes, and hands each tab module an entry { data, fresh, err }
// to draw. A route the server does not answer yet shows a quiet "not
// available yet" instead of an error.

import { $, h, clear, icon, isMobile, storage } from './dom.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { write as writeHash } from './router.js?v=__ASSET_V__'
import { specTab } from './spec.js?v=__ASSET_V__'
import { diffTab } from './diff.js?v=__ASSET_V__'
import { logTab } from './log.js?v=__ASSET_V__'
import { prTab } from './pr.js?v=__ASSET_V__'
import { statsTab } from './stats.js?v=__ASSET_V__'

const TABS = [specTab, diffTab, logTab, prTab, statsTab]
const byName = Object.fromEntries(TABS.map(t => [t.name, t]))

// shown is the tabs the open card has: a tab may hide itself for a kind
// of card it has nothing for (the spec, on a session)
const shown = () => TABS.filter(t => !t.hidden?.(state.card))

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
    if (!state.sel) { set({ diffPending: 0 }); renderTabs(); renderPane(false); return }
    prefetch()
    load(state.tab)
  })
  on(['tab'], () => { load(state.tab); renderTabs(); renderPane(false) })
  on(['cardRev'], () => {
    for (const k of Object.keys(cache)) cache[k].stale = true
    load(state.tab, true)
  })
  on(['card'], () => {
    // the card has just said it has no such tab: move to its diff
    // (without opening the panel the way a person's choice of a tab does)
    if (byName[state.tab]?.hidden?.(state.card)) { set({ tab: 'diff' }); writeHash(state.sel, 'diff') }
    prefetch()
    renderTabs()
  })
  on(['rightHidden'], applyHidden)
  // on a phone the row holds the card's Thread too, and says which is shown
  on(['view'], renderTabs)
  matchMedia('(max-width:760px)').addEventListener('change', renderTabs)
  initResizer()
  applyHidden()
  renderTabs()
}

export function setTab (t) {
  if (!byName[t] || byName[t].hidden?.(state.card)) return
  if (state.rightHidden) { set({ rightHidden: false }); storage.set('rightHidden', false) }
  if (isMobile()) set({ view: 'panel' })
  set({ tab: t })
  writeHash(state.sel, t)
}

export function togglePanel () {
  if (isMobile()) { set({ view: state.view === 'panel' ? 'thread' : 'panel' }); return }
  const hidden = !state.rightHidden
  set({ rightHidden: hidden })
  storage.set('rightHidden', hidden)
}

function applyHidden () {
  $('#app').classList.toggle('no-right', !!state.rightHidden)
}

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
  const hadFocus = box.contains(document.activeElement) && document.activeElement.getAttribute('role') === 'tab'
  clear(box)
  // the tablist holds tabs only; the close button sits beside it
  const list = h('div', { class: 'tablist', role: 'tablist', 'aria-label': 'Documents' })
  list.addEventListener('keydown', tabKeys)
  box.append(list)
  // a phone's card screen has one row of tabs: its thread, then its documents
  const phone = isMobile()
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
    }, 'Thread'))
  }
  for (const t of shown()) {
    const e = cache[t.name]
    const on = state.tab === t.name && (!phone || state.view === 'panel')
    const empty = e && !e.err && e.data && t.empty?.(e.data)
    const n = e?.data && t.count ? t.count(e.data) : 0
    list.append(h('button', {
      class: ['tab', on && 'on', !on && (empty || e?.err) && 'dim'],
      role: 'tab',
      type: 'button',
      id: `tab-${t.name}`,
      tabindex: on ? '0' : '-1',
      data: { tab: t.name },
      'aria-selected': String(on),
      'aria-controls': 'pane',
      testid: `tab-${t.name}`,
      title: `${t.label} (${t.key})`,
      onclick: () => setTab(t.name)
    }, t.label, n ? h('span', { class: ['n', e.fresh && 'hot'] }, e.fresh ? `${n}+` : String(n)) : null))
  }
  if (!phone) box.append(h('span', { class: 'sp' }, h('button', {
    class: 'iconbtn', type: 'button', testid: 'panel-close', title: 'Hide panel (])', 'aria-label': 'Hide panel', onclick: togglePanel
  }, icon('close'))))
  $('#pane').setAttribute('aria-labelledby', `tab-${state.tab}`)
  if (hadFocus) list.querySelector('[aria-selected="true"]')?.focus()
}

// tabKeys moves between the tabs with the arrow keys, Home and End, as a
// tablist does; the one chosen opens.
function tabKeys (e) {
  const names = shown().map(t => t.name)
  const i = names.indexOf(e.target.dataset?.tab)
  if (i < 0) return
  const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: names.length - 1 }[e.key]
  if (to === undefined) return
  e.preventDefault()
  setTab(names[(to + names.length) % names.length])
  $(`#tab-${names[(to + names.length) % names.length]}`)?.focus()
}

function renderPane (keepScroll) {
  const pane = $('#pane')
  const top = pane.scrollTop
  clear(pane)
  pane.dataset.tab = state.tab
  const tab = byName[state.tab]
  const e = entry(state.tab)
  if (!state.sel) return
  if (e.err) {
    pane.append(e.err.notBuilt
      ? h('div', { class: 'empty', testid: 'panel-unavailable' }, h('b', null, `The ${tab.label} tab is not available yet`), 'This board’s server does not serve it yet.')
      : h('div', { class: 'empty err', testid: 'panel-error' }, h('b', null, `The ${tab.label} tab did not load`), e.err.message,
        h('button', { class: 'btn', type: 'button', onclick: () => { e.err = null; e.stale = true; load(state.tab) } }, 'Try again')))
  } else if (!e.data && e.loading) {
    pane.append(h('div', { class: 'empty', testid: 'panel-loading' }, h('span', { class: 'spinner' })))
  } else {
    const tctx = {
      id: state.sel,
      card: state.card,
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
}

// ---- the resizer: the conversation keeps 360px, the panel 320px ----
// (the panel's width counts the resizer, its own 6px left edge)
function initResizer () {
  const r = $('#resizer')
  const app = $('#app')
  const saved = storage.get('rightW', null)
  if (saved) app.style.setProperty('--right-w', saved + 'px')
  let drag = false
  const setW = (w) => {
    const main = $('.main').getBoundingClientRect()
    const rail = $('.rail').getBoundingClientRect().width
    const clamped = Math.round(Math.max(326, Math.min(w, main.width - rail - 360)))
    app.style.setProperty('--right-w', clamped + 'px')
    storage.set('rightW', clamped)
    sync()
  }
  // the separator says how much of the width the panel has
  const sync = () => requestAnimationFrame(() => {
    const main = $('.main').getBoundingClientRect().width
    const w = $('.right').getBoundingClientRect().width
    if (!main) return
    r.setAttribute('aria-valuenow', String(Math.round((w / main) * 100)))
    r.setAttribute('aria-valuetext', `document panel ${Math.round(w)} pixels wide`)
  })
  r.setAttribute('aria-valuemin', '0')
  r.setAttribute('aria-valuemax', '100')
  sync()
  window.addEventListener('resize', sync)
  r.addEventListener('pointerdown', (e) => { drag = true; r.classList.add('drag'); r.setPointerCapture(e.pointerId) })
  r.addEventListener('pointermove', (e) => {
    if (!drag) return
    setW($('.main').getBoundingClientRect().right - e.clientX)
  })
  const stop = () => { drag = false; r.classList.remove('drag') }
  r.addEventListener('pointerup', stop)
  r.addEventListener('pointercancel', stop)
  r.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const w = $('.right').getBoundingClientRect().width
    setW(w + (e.key === 'ArrowLeft' ? 24 : -24))
  })
  r.addEventListener('dblclick', () => { app.style.removeProperty('--right-w'); storage.set('rightW', null); sync() })
}
