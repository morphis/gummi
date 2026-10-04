// views.js — the page's overlays, and a registry other surfaces plug into.
//
//   registerView(name, { title, wide, css, mount(body, ctx), unmount() })
//     adds a surface (new card, goal, stacks, ingest, bugs, doctor, fleet)
//     that the rail foot, the palette and the keys can open by
//     name. mount may return a cleanup function. ctx carries what a view
//     needs from the page: { params, close, api, select, toast, state,
//     onStore, onEvent, openModal }.
//   openView(name, params)   opens one; an unregistered name says so quietly.
//   openModal({...})         a dialog with a title, a body and buttons.
//   openMenu(anchor, items)  a small popup menu under or over a button.

import { h, icon } from './dom.js?v=__ASSET_V__'
import { pushLayer } from './back.js?v=__ASSET_V__'
import { on, state } from './store.js?v=__ASSET_V__'

const views = new Map()
let ctxFactory = () => ({})
let current = null // { scrim, close }

export const VIEW_LABELS = {
  newcard: 'New card',
  fleet: 'Fleet stats',
  goals: 'Goals',
  goal: 'Goal',
  stacks: 'Stacks',
  schedules: 'Schedules',
  ingest: 'Import spec',
  bugs: 'Import bugs',
  doctor: 'Doctor'
}

export function registerView (name, def) { views.set(name, def) }

// A view may name its own stylesheet (a path under assets/, e.g.
// 'views/goal.css'); it is linked once, the first time the view opens, so
// surfaces keep their styles beside their code instead of in app.css.
const linked = new Set()
function linkCSS (href) {
  if (!href || linked.has(href)) return
  linked.add(href)
  document.head.append(h('link', { rel: 'stylesheet', href: `/assets/${href}?v=__ASSET_V__` }))
}
export function hasView (name) { return views.has(name) }
export function setViewContext (fn) { ctxFactory = fn }
export function overlayOpen () { return !!current }

export function openView (name, params = {}) {
  const def = views.get(name)
  const title = def?.title || VIEW_LABELS[name] || name
  if (!def) {
    return openModal({
      title,
      testid: `view-${name}`,
      body: h('div', { class: 'empty', testid: 'not-available' },
        h('b', null, `${title} is not available yet`),
        'This board’s web face does not offer it yet. The terminal board has it.')
    })
  }
  linkCSS(def.css)
  const body = h('div', { class: 'mbody', testid: `view-${name}-body` })
  let cleanup = null
  const m = openModal({
    title,
    wide: def.wide !== false,
    testid: `view-${name}`,
    bodyEl: body,
    onClose: () => { try { cleanup?.(); def.unmount?.() } catch (err) { console.error(err) } }
  })
  const ctx = { ...ctxFactory(), params, close: m.close }
  try {
    const r = def.mount(body, ctx)
    if (typeof r === 'function') cleanup = r
    else if (r && typeof r.then === 'function') r.then(fn => { if (typeof fn === 'function') cleanup = fn }).catch(err => console.error(err))
  } catch (err) {
    console.error(err)
    body.append(h('div', { class: 'empty err' }, h('b', null, `${title} did not open`), String(err.message || err)))
  }
  return m
}

// openerFor is where focus goes back to when an overlay closes: whatever
// had it when the overlay opened. An overlay a shortcut opened with nothing
// focused has no such element — focus would fall to <body>, the top of the
// page for a keyboard — so it goes back to returnTo (a selector or an
// element: the control that opens the overlay) when that is on screen.
function openerFor (returnTo) {
  const a = document.activeElement
  // the board's own root holds focus only as a keyboard's start (app.js)
  if (a && a !== document.body && a.id !== 'app') return a
  return shown(returnTo)
}

// shown resolves returnTo to the first matching element on screen.
function shown (returnTo) {
  const els = typeof returnTo === 'string' ? [...document.querySelectorAll(returnTo)] : [returnTo]
  return els.find(el => el?.getClientRects().length) || null
}

// focusBack gives focus back on close: to the opener, or — when a redraw
// replaced it while the overlay was up — to returnTo's element now.
function focusBack (opener, returnTo) {
  const el = opener && document.contains(opener) ? opener : returnTo ? shown(returnTo) : null
  el?.focus?.()
}

// openModal shows one dialog over the page. body is a Node (or bodyEl a
// ready container); actions are [{label, primary, danger, testid, onClick}].
// An action whose onClick returns false keeps the dialog open. card, when
// given, is the card the dialog acts on: opening another card (a link, a
// hash typed in) closes it, so its keys never land on a card nobody is
// looking at.
export function openModal ({ title, body, bodyEl, actions = [], wide = false, testid = 'modal', onClose, role = 'dialog', returnTo, card }) {
  const mbody = bodyEl || h('div', { class: 'mbody' }, body)
  let close = null
  const foot = actions.length
    ? h('div', { class: 'mfoot' }, actions.map(a => h('button', {
      class: ['btn', a.primary && 'pri', a.danger && 'danger'],
      testid: a.testid,
      type: 'button',
      onclick: async () => {
        const keep = await a.onClick?.()
        if (keep !== false) close()
      }
    }, a.label)))
    : null
  const box = h('section', { class: ['modal', wide && 'wide'], role, 'aria-modal': 'true', 'aria-label': title, testid, tabindex: '-1' },
    // a div, not a header: inside a dialog a <header> is read as the
    // page's banner, and the page already has one (.top)
    h('div', { class: 'mhead' },
      h('h2', null, title),
      h('button', { class: 'iconbtn', type: 'button', 'aria-label': 'Close', title: 'Close (esc)', testid: 'modal-close', onclick: () => close() }, icon('close'))),
    mbody, foot)
  let off = null
  close = layer(box, { onClose: () => { off?.(); onClose?.() }, returnTo })
  if (card) off = on(['sel'], () => { if (state.sel !== card) close() })
  const first = box.querySelector('input,textarea,select,[autofocus]') || box
  first.focus()
  return { el: box, body: mbody, close }
}

// openOverlay shows a bare panel (the palette, the keys sheet) in a scrim.
export function openOverlay (panel, { onClose, returnTo } = {}) {
  return { close: layer(panel, { onClose, returnTo }) }
}

// layer puts box over the page in a scrim, as the one overlay open, and
// returns its close. While it is up it is the page: everything behind it
// is inert (neither focus nor a screen reader reaches it), focus stays in
// it — Tab wraps, and a control a redraw took away hands focus to its
// redrawn self or to the box, never to <body> — and escape closes it
// wherever focus is. Closing gives focus back to whatever opened it.
function layer (box, { onClose, returnTo }) {
  closeOverlay()
  const opener = openerFor(returnTo)
  if (!box.hasAttribute('tabindex')) box.tabIndex = -1 // focus's last resort
  const scrim = h('div', { class: 'scrim', testid: 'scrim' }, box)
  let layerDone = null
  let lastTid = null
  const behind = []
  const kept = new MutationObserver(() => {
    if (current?.scrim !== scrim || holdsFocus(scrim)) return
    const again = lastTid && box.querySelector(`[data-testid="${CSS.escape(lastTid)}"]`)
    const to = again && again.getClientRects().length && !again.disabled ? again : box
    to.focus({ preventScroll: true })
  })
  const close = () => {
    if (!current || current.scrim !== scrim) return
    current = null
    kept.disconnect()
    scrim.remove()
    for (const el of behind) el.inert = false
    layerDone?.()
    try { onClose?.() } catch (err) { console.error(err) }
    focusBack(opener, returnTo)
  }
  scrim.addEventListener('mousedown', (e) => { if (e.target === scrim) close() })
  scrim.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); close() }
    if (e.key === 'Tab') wrapTab(e, box)
  })
  scrim.addEventListener('focusin', (e) => { lastTid = e.target?.dataset?.testid || null })
  for (const el of document.body.children) {
    if (OVER.has(el.id) || el.tagName === 'SCRIPT' || el.inert) continue
    el.inert = true
    behind.push(el)
  }
  document.body.append(scrim)
  kept.observe(box, { childList: true, subtree: true })
  current = { scrim, box, close }
  layerDone = pushLayer(close)
  return close
}

// OVER are the page's parts that stand above any overlay and stay live
// under it: the notices, and a device asking to join — a request that may
// lapse while a dialog is up must still be answerable.
const OVER = new Set(['toasts', 'approvals', 'approvals-live'])

// holdsFocus: focus is inside the overlay, or on something that sits over
// it (a menu it opened, a notice, a device asking to join).
function holdsFocus (scrim) {
  const a = document.activeElement
  return !!a && a !== document.body && (scrim.contains(a) || !!a.closest?.('.menu, #toasts, #approvals'))
}

// wrapTab keeps Tab inside the box: past the last control to the first,
// and back.
function wrapTab (e, box) {
  const els = [...box.querySelectorAll('a[href],button,input,select,textarea,summary,[tabindex]:not([tabindex="-1"])')]
    .filter(el => !el.disabled && el.getClientRects().length)
  if (!els.length) { e.preventDefault(); box.focus(); return }
  const first = els[0]
  const last = els[els.length - 1]
  const a = document.activeElement
  if (e.shiftKey && (a === first || a === box)) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && a === last) {
    e.preventDefault()
    first.focus()
  }
}

// Escape closes the overlay even when focus is on nothing (a redraw took
// the focused control away): the scrim's own listener hears only keys
// that start inside it. A menu over the overlay goes first.
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || e.defaultPrevented) return
  // a menu closes on escape wherever focus went (a click on the page
  // behind it, a redraw) — not only while focus is on one of its items
  if (menuOpen) {
    e.preventDefault()
    const { anchor } = menuOpen
    closeMenu()
    if (document.contains(anchor)) anchor.focus()
    return
  }
  if (!current) return
  e.preventDefault()
  current.close()
})

// Focus that wanders outside an open overlay is brought back into it.
document.addEventListener('focusin', () => {
  if (current && !holdsFocus(current.scrim)) current.box.focus({ preventScroll: true })
})

export function closeOverlay () { current?.close() }

// openMenu drops a menu from anchor's container. items are
// [{label, onClick, danger, key, testid}] or 'sep'.
let menuOpen = null
export function openMenu (anchor, items, { up = false, testid = 'menu' } = {}) {
  closeMenu()
  const menu = h('div', { class: ['menu', up ? 'up' : 'down'], role: 'menu', testid },
    items.map(it => it === 'sep'
      ? h('div', { class: 'sep', role: 'separator' })
      : h('button', {
        role: 'menuitem',
        type: 'button',
        class: it.danger && 'danger',
        testid: it.testid,
        title: it.hint || null,
        // focus goes back to the menu's button first, so a dialog the item
        // opens returns there when it closes rather than to <body> — the
        // item that had focus is gone with the menu
        onclick: () => { closeMenu(); if (document.contains(anchor)) anchor.focus(); it.onClick?.() }
      }, it.icon ? icon(it.icon) : null, it.label, it.key ? h('kbd', { class: 'kh' }, it.key) : null)))
  // on the body, placed against the anchor: the surface that drew the
  // anchor may redraw (the card head does on every change) without taking
  // an open menu with it, and a long menu scrolls inside the window
  document.body.append(menu)
  place(menu, anchor, up)
  anchor.setAttribute('aria-expanded', 'true')
  const outside = (e) => { if (!menu.contains(e.target) && e.target !== anchor && !anchor.contains(e.target)) closeMenu() }
  const keys = (e) => {
    const btns = [...menu.querySelectorAll('button')]
    const i = btns.indexOf(document.activeElement)
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeMenu(); anchor.focus() }
    if (e.key === 'ArrowDown') { e.preventDefault(); btns[(i + 1) % btns.length]?.focus() }
    if (e.key === 'ArrowUp') { e.preventDefault(); btns[(i - 1 + btns.length) % btns.length]?.focus() }
    // Tab stays in the menu while it is open, as the arrows do: focus that
    // left it for the page behind would leave the menu standing with
    // nothing to close it from
    if (e.key === 'Tab') { e.preventDefault(); btns[(i + (e.shiftKey ? -1 : 1) + btns.length) % btns.length]?.focus() }
  }
  setTimeout(() => { if (menuOpen?.menu === menu) document.addEventListener('mousedown', outside) }, 0)
  // a menu placed against its anchor is wrong after a real resize (a
  // rotation, a wider window) — but only its own resize: a listener left
  // behind by a menu the back button closed would close the next one
  const resized = () => { if (menuOpen?.menu === menu) closeMenu() }
  window.addEventListener('resize', resized)
  menu.addEventListener('keydown', keys)
  menuOpen = { menu, anchor, outside, resized, done: pushLayer(() => closeMenu()) }
  menu.querySelector('button')?.focus()
  return menu
}

function place (menu, anchor, up) {
  const r = anchor.getBoundingClientRect()
  const host = anchor.parentElement?.getBoundingClientRect() || r
  const vw = document.documentElement.clientWidth
  const vh = window.innerHeight
  if (up) {
    menu.style.setProperty('left', Math.max(8, host.left + 8) + 'px')
    menu.style.setProperty('bottom', Math.max(8, vh - r.top + 4) + 'px')
    menu.style.setProperty('max-height', Math.max(120, r.top - 12) + 'px')
  } else {
    menu.style.setProperty('right', Math.max(8, vw - r.right) + 'px')
    menu.style.setProperty('top', (r.bottom + 4) + 'px')
    menu.style.setProperty('max-height', Math.max(120, vh - r.bottom - 12) + 'px')
  }
}

export function closeMenu () {
  if (!menuOpen) return
  document.removeEventListener('mousedown', menuOpen.outside)
  window.removeEventListener('resize', menuOpen.resized)
  menuOpen.anchor.setAttribute('aria-expanded', 'false')
  menuOpen.menu.remove()
  const done = menuOpen.done
  menuOpen = null
  done?.()
}
