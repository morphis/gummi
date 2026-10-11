// mobile.js — the phone layout (≤ 760px): the cards are the root screen,
// and opening one moves to that card's own screen. The card screen keeps
// its identity on top — a back to the cards (which counts what needs you),
// the card's head, and one row of tabs: its Thread beside its documents
// (Memory or Spec, Changes, Run, and Terminal where served; panel.js draws
// the row). The pinned decision
// follows into the documents as a docked bar (decision.js draws it).

import { $, h, clear, icon, isMobile } from './dom.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { pushLayer } from './back.js?v=__ASSET_V__'
import { write as writeHash } from './router.js?v=__ASSET_V__'

export function initMobile () {
  const bar = $('#mcard')
  bar.append(h('button', { class: 'mback', type: 'button', testid: 'card-back', title: 'Back to the cards', 'aria-label': 'Back to the cards', onclick: () => set({ view: 'cards' }) },
    icon('back'), h('span', { class: 'dotn', id: 'm-needs', testid: 'card-back-needs', 'aria-hidden': 'true' })))
  on(['view'], apply)
  on(['view'], layer)
  on(['view'], address)
  on(['board'], count)
  window.addEventListener('resize', () => { place(); apply(); layer() })
  place()
  apply()
  layer()
}

// place moves the card's head and the panel's tabs into the card screen's
// bar on a phone, so both views of a card show whose they are, and puts
// them back where the desktop draws them when the window widens.
let home = null
function place () {
  const head = $('#head')
  const tabs = $('#tabs')
  home ||= { head: [head.parentNode, head.nextSibling], tabs: [tabs.parentNode, tabs.nextSibling] }
  const bar = $('#mcard')
  if (isMobile()) {
    if (head.parentNode !== bar) bar.append(head, tabs)
  } else if (head.parentNode === bar) {
    home.head[0].insertBefore(head, home.head[1])
    home.tabs[0].insertBefore(tabs, home.tabs[1])
  }
}

// On a phone the cards are the root, and a card's screen sits on them as
// one layer (back.js): back from it returns to the cards, and back from
// the cards leaves the page. Moving between a card's tabs is not a step back.
let viewDone = null
function layer () {
  const up = isMobile() && state.view !== 'cards'
  if (up && !viewDone) {
    viewDone = pushLayer(() => { viewDone = null; set({ view: 'cards' }) })
  } else if (!up && viewDone) {
    const done = viewDone
    viewDone = null
    done()
  }
}

// address keeps the hash on what a card's screen shows: its thread is the
// card alone (#FD-001), a document names its tab (#FD-001/diff) — so a
// reload on the thread opens the thread, not the document seen before it.
function address () {
  if (!isMobile() || !state.sel) return
  if (state.view === 'thread') writeHash(state.sel, null)
  else if (state.view === 'panel') writeHash(state.sel, state.tab)
}

function apply () {
  $('#app').dataset.view = state.view
  if (isMobile() && state.view === 'thread') {
    const sc = $('#thread')
    requestAnimationFrame(() => { sc.scrollTop = sc.scrollHeight })
  }
}

function count () {
  const el = $('#m-needs')
  if (!el) return
  const n = state.board?.counts?.needs || 0
  clear(el).append(String(n))
  el.hidden = !n
  $('#mcard .mback').setAttribute('aria-label', n ? `Back to the cards, ${n} need you` : 'Back to the cards')
}
