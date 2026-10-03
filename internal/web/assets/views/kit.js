// views/kit.js — the small form pieces the tool surfaces (ingest, bug
// import, doctor, fleet) share: a labelled field, a select
// from plain strings, a segmented control, an inline confirm strip, and
// the one way they show a server's refusal.

import { h, clear } from '../dom.js?v=__ASSET_V__'

// field wraps a control in a label with its words above it.
export function field (label, control, { hint, testid, cls } = {}) {
  return h('label', { class: ['field', cls], testid }, h('span', null, label), control, hint ? h('small', { class: 'hint' }, hint) : null)
}

// choose builds a <select> from strings or {value, label} pairs.
export function choose (options, { value, testid, label, onchange } = {}) {
  return h('select', { testid, 'aria-label': label, onchange },
    options.map(o => {
      const v = typeof o === 'string' ? o : o.value
      const l = typeof o === 'string' ? o : o.label
      return h('option', { value: v, selected: v === value }, l)
    }))
}

// segmented is a row of toggle buttons; onPick gets the chosen value.
export function segmented (options, value, onPick, { testid, label } = {}) {
  const box = h('div', { class: 'vseg', role: 'group', 'aria-label': label, testid })
  const draw = (cur) => {
    clear(box)
    for (const o of options) {
      box.append(h('button', {
        type: 'button',
        class: o.value === cur && 'on',
        'aria-pressed': String(o.value === cur),
        testid: testid ? `${testid}-${o.value}` : null,
        onclick: () => { draw(o.value); onPick(o.value) }
      }, o.label))
    }
  }
  draw(value)
  return box
}

// errorBox shows a refusal as the sentence the server gave, plainly.
export function errorBox (err, testid = 'view-error') {
  const text = err?.data?.text || err?.message || String(err || '')
  return h('div', { class: 'verr', role: 'alert', testid }, text)
}

// confirmStrip is an inline yes/no in place of a nested dialog: a view
// already lives in the page's one modal.
export function confirmStrip ({ question, detail, yes, no = 'Cancel', danger, testid = 'confirm', onYes, onNo }) {
  const busy = { v: false }
  const yesBtn = h('button', {
    type: 'button',
    class: ['btn', 'pri', danger && 'danger'],
    testid: `${testid}-yes`,
    onclick: async () => {
      if (busy.v) return
      busy.v = true
      yesBtn.disabled = true
      try { await onYes() } finally { busy.v = false; yesBtn.disabled = false }
    }
  }, yes)
  const strip = h('div', { class: ['vconfirm', danger && 'danger'], role: 'alertdialog', 'aria-label': question, testid },
    h('div', { class: 'q' }, h('b', null, question), detail ? h('div', { class: 'd' }, detail) : null),
    h('div', { class: 'acts' },
      h('button', { type: 'button', class: 'btn', testid: `${testid}-no`, onclick: onNo }, no),
      yesBtn))
  // a question asked below the fold is brought into view
  requestAnimationFrame(() => { if (strip.isConnected) strip.scrollIntoView({ block: 'nearest' }) })
  return strip
}

// cardLinks lists cards a write made; clicking one opens it.
export function cardLinks (cards, ctx, testid = 'created') {
  return h('ul', { class: 'vcards', testid },
    cards.map(c => h('li', null,
      h('button', {
        type: 'button',
        class: 'link',
        testid: `${testid}-${c.id}`,
        onclick: () => { ctx.close(); ctx.select(c.id) }
      }, c.id),
      h('span', { class: 't' }, c.title || ''),
      c.stage ? h('span', { class: 'st' }, c.stage) : null)))
}

// debounced refetch: one request in flight, one queued behind it.
export function refetcher (load) {
  let running = false
  let again = false
  return async function run () {
    if (running) { again = true; return }
    running = true
    try { await load() } finally {
      running = false
      if (again) { again = false; run() }
    }
  }
}
