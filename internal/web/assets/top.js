// top.js — the header bar: the workspace, the board's two counts and
// today's spend, the connection state, and who else is looking.

import { $, h, icon, cr, initials, clear } from './dom.js?v=__ASSET_V__'
import { on, state } from './store.js?v=__ASSET_V__'

const VIEWER_TINTS = ['var(--accent)', 'var(--s-verify)', 'var(--s-plan)', 'var(--s-open)', 'var(--s-impl)']

export function initTop ({ nextNeeding, palette, keysHelp, toggleRail }) {
  $('#rail-toggle').append(icon('rail'))
  $('#rail-toggle').addEventListener('click', toggleRail)
  $('#btn-palette').append(icon('search'), h('kbd', null, navigator.platform?.startsWith('Mac') ? '⌘K' : 'Ctrl K'))
  $('#btn-palette').addEventListener('click', palette)
  $('#btn-keys').addEventListener('click', keysHelp)
  $('#p-next').addEventListener('click', nextNeeding)
  on(['board', 'session'], renderCounts)
  on(['conn'], renderConn)
  on(['viewers', 'session'], renderPresence)
  renderConn()
}

function renderCounts () {
  const b = state.board
  $('#ws-repo').textContent = b?.repo || state.session?.repo || ''
  $('#ws-head').textContent = b?.head || ''
  $('#ws-head').hidden = !b?.head
  $('.ws .sep.branch').hidden = !b?.head
  $('#p-needs').textContent = b?.counts?.needs ?? 0
  $('#p-run').textContent = b?.counts?.running ?? 0
  $('#p-running').classList.toggle('quiet', !b?.counts?.running)
  $('#p-today').textContent = cr(b?.today?.spent)
  document.title = b?.counts?.needs ? `(${b.counts.needs}) gummi · ${b.repo}` : `gummi · ${b?.repo || ''}`
}

function renderConn () {
  const pill = $('#p-conn')
  const live = state.conn === 'live'
  pill.dataset.state = state.conn
  pill.classList.toggle('off', state.conn === 'reconnecting')
  clear(pill)
  if (live) {
    pill.append(h('span', { class: 'dot ok' }), h('span', null, 'live'))
  } else {
    pill.append(h('span', { class: 'spinner' }),
      h('span', null, state.conn === 'reconnecting' ? 'reconnecting · answers paused' : 'connecting'))
  }
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
