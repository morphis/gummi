// approvals.js — devices asking to join the board. A browser paired while
// another already has the board, with a code `gummi web pair` minted or a
// browser asked for, only waits (DESIGN §20.3): anything running as the
// operator can mint such a code, so a person already at the board lets
// it in or turns it away. Every page at the board shows each request — who
// it says it is, its browser, where it came from, which code it used and
// when — until somebody answers it or it lapses. The answer reaches every
// page, and the waiting one, over the event stream ("pairing").

import { $, h, clear, clock } from './dom.js?v=__ASSET_V__'
import { get, post } from './api.js?v=__ASSET_V__'
import { onEvent } from './events.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'

let list = []
let fetchedAt = 0
let timer = 0
let seq = 0
const busy = new Set()

export function initApprovals () {
  onEvent('pairing', load)
  onEvent('resync', load)
  load()
}

async function load () {
  const mine = ++seq
  let next = []
  try {
    next = (await get('/api/devices/pending'))?.devices || []
  } catch (err) {
    if (!err.notBuilt) console.error(err)
  }
  if (mine !== seq) return // a later load already answered
  list = next
  fetchedAt = Date.now()
  announce()
  render()
}

// announce says a request aloud the first time this page sees it: the
// banner itself redraws with every countdown tick, and a live region
// there would read the whole thing out again each time.
const announced = new Set()
function announce () {
  const fresh = list.filter(d => !announced.has(d.id) && left(d) > 0)
  for (const d of list) announced.add(d.id)
  const el = $('#approvals-live')
  if (!el || !fresh.length) return
  el.textContent = fresh.length === 1
    ? `${fresh[0].person} on ${fresh[0].device} asks to join this board. Approve or reject it at the top of the page.`
    : `${fresh.length} new devices ask to join this board. Approve or reject them at the top of the page.`
}

// left is how many seconds a request has before it lapses.
function left (d) {
  return Math.max(0, (d.expiresInSecs || 0) - Math.floor((Date.now() - fetchedAt) / 1000))
}

function inMinutes (secs) {
  if (secs < 60) return 'under a minute'
  const m = Math.ceil(secs / 60)
  return m === 1 ? '1 minute' : `${m} minutes`
}

function render () {
  const box = $('#approvals')
  if (!box) return
  clearTimeout(timer)
  // keep the focus on the buttons when a countdown redraws them
  const focused = document.activeElement?.closest?.('#approvals [data-testid]')?.dataset.testid
  const focusedIn = document.activeElement?.closest?.('.ap-req')?.dataset.id
  clear(box)
  const live = list.filter(d => left(d) > 0)
  box.hidden = !live.length
  if (!live.length) return
  box.append(h('section', { class: 'approvals', testid: 'approvals', role: 'region', 'aria-labelledby': 'approvals-title' },
    h('h2', { id: 'approvals-title', class: 'ap-title' },
      live.length === 1 ? 'A new device asks to join this board' : `${live.length} new devices ask to join this board`),
    h('p', { class: 'ap-warn' },
      'Approve only a pairing you started yourself. A device you let in can do everything you can here — answer, cross gates, land.'),
    live.map(row)))
  if (focused && focusedIn) box.querySelector(`.ap-req[data-id="${CSS.escape(focusedIn)}"] [data-testid="${CSS.escape(focused)}"]`)?.focus()
  const soonest = Math.min(...live.map(left))
  timer = setTimeout(render, Math.min(15000, soonest * 1000 + 50))
}

function row (d) {
  const doing = busy.has(d.id)
  const fact = (k, v, testid, cls) => v ? [h('dt', null, k), h('dd', { testid, class: cls }, v)] : null
  return h('article', { class: 'ap-req', testid: `approval-${d.id}`, data: { id: d.id }, 'aria-label': `${d.person} on ${d.device}` },
    h('div', { class: 'ap-who' },
      h('span', { class: 'ap-dot', 'aria-hidden': 'true' }),
      h('b', { testid: 'approval-person' }, d.person), ' on ', h('span', { testid: 'approval-device' }, d.device)),
    h('dl', { class: 'ap-facts' },
      fact('From', d.source, 'approval-source', 'mono'),
      fact('Paired', d.via + (d.origin ? ` at ${d.origin}` : ''), 'approval-via'),
      fact('Asked', `${clock(d.requestedAt)} · lapses in ${inMinutes(left(d))}`, 'approval-time'),
      fact('Browser', d.userAgent, 'approval-ua', 'mono ua')),
    h('div', { class: 'ap-actions' },
      h('button', { class: 'btn pri', type: 'button', testid: 'approval-approve', disabled: doing, onclick: () => answer(d, 'approve') }, 'Approve'),
      h('button', { class: 'btn danger', type: 'button', testid: 'approval-reject', disabled: doing, onclick: () => answer(d, 'reject') }, 'Reject')))
}

async function answer (d, how) {
  if (busy.has(d.id)) return
  busy.add(d.id)
  render()
  try {
    await post(`/api/devices/${encodeURIComponent(d.id)}/${how}`, {})
  } catch (err) {
    toast(err.message, { err: true })
  } finally {
    busy.delete(d.id)
    await load()
  }
}
