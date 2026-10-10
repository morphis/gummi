// work.js — what the board is waiting on GitHub for, per card (the "work"
// events, webapi.ChangeWork): a publish's facts being read, a step of the
// act, a PR's checks or its review threads. Whoever started it — this
// page, another, the terminal — the wait is drawn as a notice that stays
// until the work ends, with a clock once it has run a few seconds, and the
// buttons that would start the same work again are held off meanwhile.

import { h } from './dom.js?v=__ASSET_V__'
import { onEvent } from './events.js?v=__ASSET_V__'
import { toastBox } from './toast.js?v=__ASSET_V__'

// longer than any one git or gh call may take (publish.Timeout): a wait
// whose end this page never heard is not drawn forever
const STALE = 150000

const work = new Map() // card id -> { text, step, since, timer }
const heard = new Set()
const held = new Set() // cards whose work something else is drawing
let clock = 0

// working reports whether the board is waiting on GitHub for a card.
export function working (id) { return work.has(id) }

// onWork hears every work event as it arrives; it returns its own off.
export function onWork (fn) {
  heard.add(fn)
  return () => heard.delete(fn)
}

// hold keeps a card's work out of the notices while a dialog draws it
// itself; it returns the release.
export function hold (id) {
  held.add(id)
  draw(id)
  return () => { held.delete(id); draw(id) }
}

// workLock are the props of a button that would start a card's GitHub
// work again: off while some is out.
export function workLock (id) {
  return { data: { workLock: id }, disabled: work.has(id) }
}

// waited is how long something has been out, once that is worth saying.
export function waited (since) {
  const s = Math.floor((Date.now() - since) / 1000)
  if (s < 3) return ''
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`
}

function take (c) {
  if (!c.id) return
  const was = work.get(c.id)
  clearTimeout(was?.timer)
  if (c.done) {
    work.delete(c.id)
  } else {
    const text = c.text || was?.text || ''
    work.set(c.id, {
      text,
      step: c.step || '',
      since: was && was.text === text ? was.since : Date.now(),
      timer: setTimeout(() => { work.delete(c.id); draw(c.id) }, STALE)
    })
  }
  draw(c.id)
  for (const fn of [...heard]) {
    try { fn(c) } catch (err) { console.error(err) }
  }
  if (work.size && !clock) clock = setInterval(tick, 1000)
}

function tick () {
  if (!work.size) { clearInterval(clock); clock = 0; return }
  for (const el of document.querySelectorAll('[data-work]')) {
    const w = work.get(el.dataset.work)
    const t = w ? waited(w.since) : ''
    el.querySelector('.wt').textContent = t ? ` · ${t}` : ''
  }
}

function draw (id) {
  const w = work.get(id)
  for (const b of document.querySelectorAll(`[data-work-lock="${CSS.escape(id)}"]`)) b.disabled = !!w
  const box = toastBox()
  let el = box && [...box.children].find(e => e.dataset.work === id)
  if (!box || !w || held.has(id)) { el?.remove(); return }
  if (!el) {
    el = h('div', { class: 'toast work', testid: 'work-toast', role: 'status', data: { work: id } },
      h('span', { class: 'spinner sm' }), h('span', { class: 'wl' }), h('span', { class: 'wt', 'aria-hidden': 'true' }))
    box.append(el)
  }
  const line = `${id}: ${w.text}…`
  if (el.querySelector('.wl').textContent !== line) el.querySelector('.wl').textContent = line
}

onEvent('work', take)
