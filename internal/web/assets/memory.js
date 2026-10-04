// memory.js — the Memory tab: a freeform card's project memory — the
// workspace's global memory and the card's own memory and dead-ends — the
// documents its session reads at spawn, the card's own two filled as it
// works, shown the way a workflow card's spec is. Each document folds to
// a header row — title, path, a one-line hint — so a phone sees where one
// document ends and the next begins instead of one long scroll. The
// card's own memory is open by default; the other two follow the
// viewport, and every open/closed choice persists in this browser.
// Plain files under .gummi/memory/, never committed; editable by hand
// on the host.

import { h, isMobile, storage } from './dom.js?v=__ASSET_V__'
import { get, cardPath } from './api.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'

export const memoryTab = {
  name: 'memory',
  label: 'Memory',
  key: 'g m',
  fetch: (id) => get(cardPath(id, 'memory')),
  empty: (m) => !m || m.none || !(m.global?.text || m.memory?.text || m.deadEnds?.text),
  // a workflow card's documents are its stages'; memory belongs to a
  // session (the mirror of the spec tab's hiding on one)
  hidden: (c) => !(c?.kind === 'freeform' || c?.stage === 'open'),
  render
}

function render (pane, entry, ctx) {
  const m = entry.data
  if (!m || m.none) {
    pane.append(h('div', { class: 'empty', testid: 'memory-none' }, h('b', null, 'No memory'),
      m?.why || 'Memory belongs to a freeform session.'))
    return
  }
  // one column: the documents stack, with none of the spec's outline
  pane.append(h('div', { class: 'memory', testid: 'memory' },
    h('div', { class: 'src', testid: 'memory-src' },
      m.dir ? `${m.dir} — plain files, never committed; editable by hand` : ''),
    doc('global', 'Global memory', m.global, !isMobile(),
      'Nothing written yet: every freeform session here reads this file at spawn; it is filled by hand.'),
    doc('memory', 'Memory', m.memory, true,
      'Nothing written yet: the session keeps its working notes here, current as it goes.'),
    doc('dead-ends', 'Dead ends', m.deadEnds, !isMobile(),
      'Nothing written yet: what it tried that failed is recorded here, so no later turn pays for it twice.')))
}

// hint is a document's own first line — what the header row shows of a
// closed fold. Markdown heading markers are stripped so the hint reads
// as a line, not as markup.
function hint (text) {
  const first = (text || '').split('\n').map((l) => l.trim()).find(Boolean) || ''
  return first.replace(/^#+\s*/, '')
}

// doc builds one memory document as a fold. The default open/closed
// state comes from the viewport at render; a reader's choice is stored
// per document and per browser and wins over the default from then on.
// The choice is recorded from the summary's click — the fold's toggle
// runs as that click's default action, so el.open still holds the
// pre-toggle state and the write lands synchronously, before any
// navigation can beat the async toggle event to it.
function doc (tid, title, d, defOpen, ph) {
  const saved = storage.get(`memory-open:${tid}`, null)
  const el = h('details', { class: ['doc', 'memdoc'], testid: `memory-${tid}`, open: saved == null ? defOpen : saved },
    h('summary', null,
      h('span', { class: 'mt' }, title),
      d?.text ? h('span', { class: 'mh' }, hint(d.text)) : null,
      h('span', { class: 'src' }, d?.path || ''),
      h('span', { class: 'chev', 'aria-hidden': 'true' }, '▸')),
    d?.text ? markdown(d.text) : h('div', { class: 'empty' }, ph))
  el.addEventListener('click', (e) => {
    if (e.target.closest('summary')) storage.set(`memory-open:${tid}`, !el.open)
  })
  return el
}