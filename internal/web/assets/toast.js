// toast.js — short notices at the bottom of the screen: the server's own
// (the TUI's status band, over SSE) and the page's answers to what a person
// just did. A failure stays a little longer and reads in the error colour.
//
// A notice of several lines keeps its lines (the TUI shows those in its
// band, not as a one-row pill). One that hands the reader a command to run
// — an automatic stack replay's `git push` lines — stays a minute rather
// than 2.6 seconds, since nobody copies a command that fast, and while the
// pointer or focus is on it. It shows its sentence with a Copy for the
// commands and folds the commands themselves behind Show, so it never
// stands over the page it is about (the stacks view prints the same lines
// in full).

import { h } from './dom.js?v=__ASSET_V__'

const MAX = 3

// A refusal the page already shows where it happened (the decision's note)
// is not said a second time in a toast: the server broadcasts the same
// line to every viewer, and the one who answered has it in the note.
const hushed = new Map() // normalised text -> until (ms)
const norm = (t) => String(t || '').trim().replace(/[.\s]+$/, '').toLowerCase()

// hush drops a toast saying text, and one that arrives saying it in the
// next few seconds.
export function hush (text, ms = 15000) {
  const k = norm(text)
  if (!k) return
  hushed.set(k, Date.now() + ms)
  const box = document.getElementById('toasts')
  for (const el of [...(box?.children || [])]) if (norm(el.dataset.text) === k) el.remove()
}

// place stands the notices just above whatever is docked at the bottom —
// the decision and composer, the phone's decision bar — so a
// notice never covers the composer or Send, nor the tabs at the top.
function place (box) {
  const vh = window.visualViewport?.height || window.innerHeight
  const tops = ['.dock', '#mdec']
    .map(s => document.querySelector(s))
    .filter(el => el && el.getClientRects().length)
    .map(el => el.getBoundingClientRect().top)
    .filter(t => t > 0 && t < vh)
  const top = tops.length ? Math.min(...tops) : vh
  box.style.setProperty('--toast-bottom', Math.max(16, Math.round(vh - top + 8)) + 'px')
}

// A decision redrawn taller (or a docked bar opened) under a notice
// already up would leave it standing over the answers: follow what is
// docked while any notice is showing.
let watched = null
function follow (box) {
  if (watched || typeof ResizeObserver === 'undefined') return
  watched = new ResizeObserver(() => { if (box.children.length) place(box) })
  for (const s of ['.dock', '#mdec']) {
    const el = document.querySelector(s)
    if (el) watched.observe(el)
  }
}

function isHushed (text) {
  const k = norm(text)
  const until = hushed.get(k)
  if (until === undefined) return false
  if (Date.now() < until) return true
  hushed.delete(k)
  return false
}

// commandLines are the lines of a notice that are a command to run: the
// indented `git …` lines the TUI's notices put under their sentence.
function commandLines (text) {
  return text.split('\n').map(l => l.trim()).filter(l => /^git\s/.test(l))
}

export function toast (text, opts = {}) {
  const box = document.getElementById('toasts')
  if (!box || !text || isHushed(text)) return
  // the page's own answer and the server's broadcast of the same outcome
  // often say the same sentence: show it once
  if ([...box.children].some(el => el.dataset.text === text)) return
  const multi = text.includes('\n')
  const cmds = multi ? commandLines(text) : []
  const sticky = cmds.length > 0
  const head = sticky ? text.split('\n').filter(l => !/^git\s/.test(l.trim())).join('\n').trim() : text
  const detail = sticky ? h('span', { class: 'toast-cmds', testid: 'toast-cmds', hidden: true }, cmds.join('\n')) : null
  const el = h('div', { class: ['toast', opts.err && 'err', multi && 'multi', sticky && 'sticky'], testid: 'toast', role: opts.err ? 'alert' : null },
    sticky ? h('span', { class: 'toast-text' }, head, detail) : text)
  el.dataset.text = text
  if (sticky) {
    const show = h('button', {
      class: 'toast-btn', type: 'button', testid: 'toast-show', 'aria-expanded': 'false',
      onclick: () => { detail.hidden = !detail.hidden; show.setAttribute('aria-expanded', String(!detail.hidden)); show.textContent = detail.hidden ? 'Show' : 'Hide' }
    }, 'Show')
    const copy = h('button', {
      class: 'toast-btn', type: 'button', testid: 'toast-copy',
      onclick: () => (navigator.clipboard?.writeText(cmds.join('\n')) ?? Promise.reject(new Error('no clipboard')))
        .then(() => { copy.textContent = 'Copied' }, () => { copy.textContent = 'Select to copy' })
    }, cmds.length > 1 ? 'Copy all' : 'Copy')
    const close = h('button', { class: 'toast-btn', type: 'button', testid: 'toast-close', 'aria-label': 'Dismiss', onclick: () => el.remove() }, '×')
    el.append(h('span', { class: 'toast-acts' }, show, copy, close))
  }
  place(box)
  follow(box)
  box.append(el)
  // an overflow evicts the oldest notice that will go on its own first;
  // one waiting for its command to be copied goes only when all are
  while (box.children.length > MAX) {
    const drop = [...box.children].find(c => !c.classList.contains('sticky')) || box.firstChild
    drop.remove()
  }
  if (!sticky) { setTimeout(() => el.remove(), opts.ms || (opts.err ? 5000 : multi ? 8000 : 2600)); return }
  // a notice holding commands goes after a minute, but never from under
  // a pointer or a focus that is on it
  const expire = () => {
    if (el.matches(':hover') || el.contains(document.activeElement)) { setTimeout(expire, 5000); return }
    el.remove()
  }
  setTimeout(expire, opts.ms || 60000)
}
