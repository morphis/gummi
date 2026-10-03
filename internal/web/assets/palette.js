// palette.js — ⌘K / Ctrl-K: jump to any card by id or words (cards that need
// you first), or open one of the board's surfaces by name. Also the `?`
// sheet that lists the keys.

import { h, clear, GLYPH, needsWord } from './dom.js?v=__ASSET_V__'
import { rows } from './store.js?v=__ASSET_V__'
import { openOverlay, openView, VIEW_LABELS } from './views.js?v=__ASSET_V__'

export function palette (select) {
  const input = h('input', { testid: 'palette-input', placeholder: 'Jump to a card, or open a view', 'aria-label': 'Jump to a card', autocomplete: 'off', role: 'combobox', 'aria-expanded': 'true', 'aria-controls': 'palette-res' })
  const res = h('div', { class: 'res', id: 'palette-res', role: 'listbox', testid: 'palette-results' })
  const panel = h('div', { class: 'palette', role: 'dialog', 'aria-label': 'Jump to a card', testid: 'palette' },
    input, res,
    h('div', { class: 'foot' }, h('span', null, h('kbd', null, '↑↓'), ' move'), h('span', null, h('kbd', null, 'enter'), ' open'), h('span', null, h('kbd', null, 'esc'), ' close')))
  const ov = openOverlay(panel, { returnTo: '#btn-palette' })
  let hi = 0
  const commands = Object.entries(VIEW_LABELS).filter(([k]) => k !== 'goal').map(([k, label]) => ({ view: k, label }))
  const list = () => {
    const q = input.value.toLowerCase().trim()
    const cards = rows().filter(r => !q || `${r.id} ${r.title}`.toLowerCase().includes(q))
      .sort((a, b) => (b.status === 'needs') - (a.status === 'needs'))
      .map(r => ({ card: r }))
    const cmds = commands.filter(c => q && c.label.toLowerCase().includes(q))
    return [...cards, ...cmds]
  }
  const choose = (it) => {
    ov.close()
    if (it.card) select(it.card.id)
    else openView(it.view)
  }
  const draw = () => {
    const l = list()
    hi = Math.min(hi, Math.max(0, l.length - 1))
    clear(res)
    if (!l.length) { res.append(h('div', { class: 'none' }, 'No card matches.')); return }
    l.forEach((it, i) => {
      const r = it.card
      res.append(h('button', {
        class: i === hi && 'hi', type: 'button', role: 'option', 'aria-selected': String(i === hi),
        testid: r ? `palette-row-${r.id}` : `palette-view-${it.view}`,
        onclick: () => choose(it)
      }, r
        ? [h('span', { class: ['g', `st-${r.stage}`], 'aria-hidden': 'true' }, GLYPH[r.stage] || '·'), h('span', { class: 'id' }, r.id), h('span', { class: 't' }, r.title), h('span', { class: 's' }, r.status === 'needs' ? needsWord(r.needs, r.stage) : r.stage)]
        : [h('span', { class: 'g' }, '→'), h('span', { class: 'id' }, 'view'), h('span', { class: 't' }, it.label), h('span', { class: 's' }, '')]))
    })
    res.querySelector('.hi')?.scrollIntoView({ block: 'nearest' })
  }
  input.addEventListener('input', () => { hi = 0; draw() })
  input.addEventListener('keydown', (e) => {
    const l = list()
    if (e.key === 'ArrowDown') { e.preventDefault(); hi = Math.min(hi + 1, l.length - 1); draw() }
    if (e.key === 'ArrowUp') { e.preventDefault(); hi = Math.max(hi - 1, 0); draw() }
    if (e.key === 'Enter' && l[hi]) { e.preventDefault(); choose(l[hi]) }
  })
  draw()
  input.focus()
}

export function keysHelp () {
  const k = (...keys) => h('span', null, keys.map((x, i) => [i ? ' ' : '', h('kbd', null, x)]))
  const panel = h('div', { class: 'palette', role: 'dialog', 'aria-label': 'Keyboard shortcuts', tabindex: '-1', testid: 'keys-help' },
    h('h2', { class: 'sr-only' }, 'Keyboard shortcuts'),
    h('div', { class: 'keys' },
      h('h3', null, 'Anywhere'),
      k('⌘K'), h('span', null, 'Jump to a card (Ctrl K off a Mac)'),
      k('n'), h('span', null, 'Next card that needs you'),
      k('j', 'k'), h('span', null, 'Next card · previous card'),
      k('/'), h('span', null, 'Write in the composer · ', h('kbd', null, 'esc'), ' leaves it'),
      k('['), h('span', null, 'Collapse or expand the rail'),
      k(']'), h('span', null, 'Show or hide the document panel'),
      h('h3', null, 'Document panel'),
      k('g', 'm'), h('span', null, 'Memory'),
      k('g', 's'), h('span', null, 'Spec'),
      k('g', 'd'), h('span', null, 'Diff'),
      k('g', 'l'), h('span', null, 'Log'),
      k('g', 'p'), h('span', null, 'PR'),
      k('g', 'r'), h('span', null, 'Stats'),
      h('h3', null, 'Open decision'),
      k('1–9'), h('span', null, 'Pick an answer'),
      k('↑', '↓'), h('span', null, 'Move between answers (composer empty)'),
      k('enter'), h('span', null, 'Does what the line under the composer says')),
    h('div', { class: 'foot' }, 'Letter keys work when the composer is not focused. No alt chords: alt+d is the browser’s address bar on Windows and Linux.'))
  openOverlay(panel, { returnTo: '#btn-keys' })
  panel.focus()
}
