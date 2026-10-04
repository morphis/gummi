// router.js — the address bar. `#FD-012` opens a card, `#FD-012/diff` opens
// it with a panel tab showing; a push notification links to `/#<id>`. The
// hash is written with replaceState, so walking the rail does not fill the
// back button with every card passed on the way.

import { stepping } from './back.js?v=__ASSET_V__'

export const TABS = ['memory', 'spec', 'diff', 'log', 'pr', 'stats']

export function parse (hash = location.hash) {
  const raw = decodeURIComponent(String(hash || '').replace(/^#/, ''))
  const [id, tab] = raw.split('/')
  return { id: id || null, tab: TABS.includes(tab) ? tab : null }
}

export function write (id, tab) {
  if (!id) return
  const next = '#' + encodeURIComponent(id) + (tab ? '/' + tab : '')
  if (location.hash === next) return
  // keep the entry's own state: back.js numbers every entry it stands on
  try { history.replaceState(history.state, '', next) } catch { location.hash = next }
}

// onRoute follows a hash typed or linked into the address bar. A hash
// changed by stepping back through the page's own layers (back.js) is not
// one: the entry under a layer may still name the card open before it.
export function onRoute (fn, current) {
  window.addEventListener('hashchange', () => {
    if (stepping()) { const c = current?.(); if (c) write(c.id, c.tab); return }
    fn(parse())
  })
}
