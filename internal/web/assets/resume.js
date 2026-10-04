// resume.js — the quit-resume question. When the board's last host quit
// with cards mid-stage (on autopilot or started by hand), it stopped them
// and marked where; the
// next host holds the question (Board.resume) until a person answers:
// pick them all back up, choose some, or not now — which leaves every one
// parked where it stopped. Nothing restarts without that answer
// (POST /api/board/resume).

import { $, h, clear, plural } from './dom.js?v=__ASSET_V__'
import { post } from './api.js?v=__ASSET_V__'
import { on, state } from './store.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'

let choosing = false
// the cards unticked under Choose…: the banner redraws on every board
// event, and a box ticked again by a redraw would resume a card its
// person had left out
const unticked = new Set()
let busy = false
let reload = () => {}
let selectCard = () => {}

export function initResume ({ loadBoard, select }) {
  reload = loadBoard
  selectCard = select
  on(['board'], render)
  render()
}

function render () {
  const box = $('#resume')
  if (!box) return
  // a redraw keeps the focus where it was (a box being ticked, a button)
  const focused = box.contains(document.activeElement) ? document.activeElement.dataset?.testid : null
  clear(box)
  const offer = state.board?.resume
  if (!offer?.cards?.length) { choosing = false; unticked.clear(); return }
  const cards = offer.cards
  const picks = cards.map(c => ({
    c,
    cb: h('input', {
      type: 'checkbox', checked: !unticked.has(c.id), testid: `resume-card-${c.id}`, 'aria-label': `Resume ${c.id}`,
      onchange: (e) => { if (e.target.checked) unticked.delete(c.id); else unticked.add(c.id) }
    })
  }))
  const names = cards.map((c, i) => [i ? ', ' : '', h('button', { class: 'link mono', type: 'button', title: c.title, onclick: () => selectCard(c.id) }, c.id)])
  box.append(h('section', { class: ['resume-banner', choosing && 'open'], testid: 'resume-banner', role: 'region', 'aria-label': 'Resume stopped cards' },
    h('div', { class: 'rb-row' },
      h('span', { class: 'rb-dot', 'aria-hidden': 'true' }),
      h('span', { class: 'rb-text', testid: 'resume-text' },
        // the server measures since against the moment this board started,
        // not against now: "closed 3s ago" would stand, unchanging, for as
        // long as the banner did, so it is said as the gap it is
        `When the board last quit${quitGap(offer.since)}, ${plural(cards.length, 'card')} ${cards.length === 1 ? 'was' : 'were'} mid-stage: `, names,
        '. ', cards.length === 1 ? 'It is' : 'They are', ' parked where ', cards.length === 1 ? 'it' : 'they', ' stopped.'),
      h('span', { class: 'rb-actions' },
        h('button', { class: 'btn pri', type: 'button', testid: 'resume-all', disabled: busy, onclick: () => answer({ cards: cards.map(c => c.id) }) }, cards.length === 1 ? 'Resume it' : 'Resume all'),
        cards.length > 1 ? h('button', { class: ['btn', choosing && 'on'], type: 'button', testid: 'resume-choose', 'aria-expanded': String(choosing), onclick: () => { choosing = !choosing; render() } }, 'Choose…') : null,
        h('button', { class: 'btn', type: 'button', testid: 'resume-none', disabled: busy, onclick: () => answer({ none: true }) }, 'Not now'))),
    choosing
      ? h('div', { class: 'rb-pick' },
        picks.map(({ c, cb }) => h('label', { class: 'cpick' }, cb, h('span', { class: 'id' }, c.id), h('span', { class: 't' }, c.title), h('span', { class: 's' }, ''))),
        h('div', { class: 'rb-pick-go' }, h('button', {
          class: 'btn pri', type: 'button', testid: 'resume-picked', disabled: busy,
          onclick: () => {
            const ids = picks.filter(p => p.cb.checked).map(p => p.c.id)
            answer(ids.length ? { cards: ids } : { none: true })
          }
        }, 'Resume the ticked ones')))
      : null))
  if (focused) box.querySelector(`[data-testid="${CSS.escape(focused)}"]`)?.focus()
}

// quitGap is how long before this board started the last one quit, from
// the server's "3m ago" — nothing when that was a moment.
function quitGap (since) {
  const gap = String(since || '').replace(/\s*ago$/, '').trim()
  if (!gap || /^0s$/.test(gap)) return ''
  return ` (${gap} before this one started)`
}

async function answer (body) {
  if (busy) return
  busy = true
  render()
  try {
    await post('/api/board/resume', body)
    choosing = false
    unticked.clear()
    toast(body.none ? 'Left them parked — resume any from its card' : `Resuming ${body.cards.join(', ')}`)
    await reload()
  } catch (err) {
    toast(err.message, { err: true })
    await reload()
  } finally {
    busy = false
    render()
  }
}
