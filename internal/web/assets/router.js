// router.js — the address bar. `#FD-012` opens a card, `#FD-012/diff` opens
// it with a panel tab showing; a push notification links to `/#<id>`. The
// hash is written with replaceState, so walking the rail does not fill the
// back button with every card passed on the way.

import { stepping } from './back.js?v=__ASSET_V__'

export const TABS = ['memory', 'spec', 'diff', 'log', 'pr', 'stats', 'terminal']

export function parse (hash = location.hash) {
  const raw = decodeURIComponent(String(hash || '').replace(/^#/, ''))
  const [id, tab] = raw.split('/')
  // a card id typed by hand in lower case (#fd-003) still names the card
  const norm = /^[a-z]+-\d+$/i.test(id || '') ? id.toUpperCase() : id
  return { id: norm || null, tab: TABS.includes(tab) ? tab : null }
}

// last is the hash the page itself wrote most recently: what the address
// should say again when a step back lands on an entry that still names
// something older.
let last = null

export function write (id, tab) {
  if (!id) return
  put('#' + encodeURIComponent(id) + (tab ? '/' + tab : ''))
}

// restore puts back the hash the page last wrote: a typed one it would
// not follow.
export function restore () { if (last) put(last) }

// clear takes the hash off the address: the page shows no card by name.
export function clear () {
  last = null
  if (!location.hash) return
  try { history.replaceState(history.state, '', location.pathname + location.search) } catch {}
}

function put (next) {
  last = next
  if (location.hash === next) return
  // keep the entry's own state: back.js numbers every entry it stands on
  try { history.replaceState(history.state, '', next) } catch { location.hash = next }
}

// onRoute follows a hash typed or linked into the address bar. A hash
// changed by stepping back through the page's own layers (back.js) is not
// one: the entry under a layer may still name the card open before it.
// The address goes back to what the page last wrote for the open card —
// not to a hash rebuilt from the tab showing now, which a card picked in
// the palette has not settled yet (its decision may still move it).
export function onRoute (fn, current) {
  window.addEventListener('hashchange', () => {
    if (stepping()) {
      const c = current?.()
      if (!c) return
      if (last && parse(last).id === c.id) put(last)
      else write(c.id, c.tab)
      return
    }
    fn(parse())
  })
}
