// top.js — the header bar: the workspace, the board's two counts (each
// jumps to the next such card) and today's spend, the connection state
// while it is anything but live, and who else is looking.

import { $, h, icon, cr, initials, clear } from './dom.js?v=__ASSET_V__'
import { on, state } from './store.js?v=__ASSET_V__'

const VIEWER_TINTS = ['var(--accent)', 'var(--s-verify)', 'var(--s-plan)', 'var(--s-open)', 'var(--s-impl)']

export function initTop ({ nextNeeding, nextRunning, palette, keysHelp, toggleRail }) {
  $('#rail-toggle').append(icon('rail'))
  $('#rail-toggle').addEventListener('click', toggleRail)
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || '')
  $('#btn-palette').append(icon('search'), h('kbd', null, mac ? '⌘K' : 'Ctrl K'))
  if (!mac) $('#btn-palette').title = 'Jump to a card, or open a view (Ctrl K)'
  $('#btn-palette').addEventListener('click', palette)
  $('#btn-keys').addEventListener('click', keysHelp)
  $('#p-next').addEventListener('click', nextNeeding)
  $('#p-running').addEventListener('click', nextRunning)
  on(['board', 'session'], renderCounts)
  on(['conn'], renderConn)
  on(['viewers', 'session'], renderPresence)
  renderConn()
}

function renderCounts () {
  const b = state.board
  $('#ws-repo').textContent = b?.repo || state.session?.repo || ''
  const name = b?.name || ''
  $('#ws-name').textContent = name
  $('#ws-name').hidden = !name
  $('#ws-head').textContent = b?.head || ''
  $('#ws-head').hidden = !b?.head
  $('.ws .sep.branch').hidden = !b?.head
  $('#p-needs').textContent = b?.counts?.needs ?? 0
  $('#p-run').textContent = b?.counts?.running ?? 0
  $('#p-running').classList.toggle('quiet', !b?.counts?.running)
  $('#p-today').textContent = cr(b?.today?.spent)
  // a named instance leads the tab title, ahead of "gummi": a tab strip
  // truncates long titles, and several boards open side by side are told
  // apart by the name only if it survives that truncation
  const title = [b?.name, 'gummi', b?.repo].filter(Boolean).join(' · ')
  document.title = b?.counts?.needs ? `(${b.counts.needs}) ${title}` : title
}

function renderConn () {
  const pill = $('#p-conn')
  const live = state.conn === 'live'
  pill.dataset.state = state.conn
  pill.classList.toggle('off', state.conn === 'reconnecting')
  clear(pill)
  const words = live ? 'live' : state.conn === 'reconnecting' ? 'reconnecting · answers paused' : 'connecting'
  // the words fold away on a narrow window, where the dot alone is shown:
  // they stay its name and its tooltip
  pill.title = `Connection: ${words}`
  pill.setAttribute('aria-label', `Connection: ${words}`)
  pill.append(live ? h('span', { class: 'dot ok' }) : h('span', { class: 'spinner' }), h('span', { class: 'ct' }, words))
}

function renderPresence () {
  const box = $('#presence')
  clear(box)
  const vs = state.viewers || []
  const me = state.session?.deviceId
  const names = vs.map(v => `${v.deviceId === me ? 'you' : v.person} (${v.device})`)
  box.title = vs.length ? `Viewing now: ${names.join(', ')}` : ''
  box.setAttribute('aria-label', box.title || 'Nobody else is viewing')
  const people = []
  for (const v of vs) if (!people.some(p => p.person === v.person)) people.push(v)
  people.slice(0, 4).forEach((v, i) => {
    box.append(h('span', { testid: 'viewer', title: `${v.person} · ${v.device}`, style: { '--vc': VIEWER_TINTS[hash(v.person) % VIEWER_TINTS.length] } }, initials(v.person)))
  })
  if (people.length > 4) box.append(h('span', { class: 'more' }, `+${people.length - 4}`))
}

function hash (s) {
  let n = 0
  for (const c of String(s)) n = (n * 31 + c.charCodeAt(0)) >>> 0
  return n
}
