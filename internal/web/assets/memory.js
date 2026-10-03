// memory.js — the Memory tab: a freeform card's project memory — the
// workspace's global memory and the card's own plan and dead-ends — the
// documents its session reads at spawn, the card's own two filled as it
// works, shown the way a workflow card's spec is. Plain files under
// .gummi/memory/, never committed; editable by hand on the host.

import { h } from './dom.js?v=__ASSET_V__'
import { get, cardPath } from './api.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'

export const memoryTab = {
  name: 'memory',
  label: 'Memory',
  key: 'g m',
  fetch: (id) => get(cardPath(id, 'memory')),
  empty: (m) => !m || m.none || !(m.global?.text || m.plan?.text || m.deadEnds?.text),
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
  const doc = (tid, title, d, ph) => h('article', { class: 'doc', testid: `memory-${tid}` },
    h('h2', { class: 'sec' }, title, h('span', { class: 'src' }, d?.path || '')),
    d?.text ? markdown(d.text) : h('div', { class: 'empty' }, ph))
  pane.append(h('div', { class: 'spec', testid: 'memory' },
    h('div', { class: 'src', testid: 'memory-src' },
      m.dir ? `${m.dir} — plain files, never committed; editable by hand` : ''),
    doc('global', 'Global memory', m.global,
      'Nothing written yet: every freeform session here reads this file at spawn; it is filled by hand.'),
    doc('plan', 'Plan', m.plan,
      'Nothing written yet: the session keeps its working plan here, current as it goes.'),
    doc('dead-ends', 'Dead ends', m.deadEnds,
      'Nothing written yet: what it tried that failed is recorded here, so no later turn pays for it twice.')))
}
