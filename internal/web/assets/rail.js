// rail.js — the card rail: the board's rows grouped by status (in the
// server's order within a group), a filter and kind chips, a compact form
// below 1280px or on `[`, and the foot that opens the board's other
// surfaces (new session, new card, board agent, fleet stats, and the More
// menu).

import { $, h, icon, clear, kindTag, repoSlot, needsColor, needsWord, GLYPH, STAGES, cr, isMobile, storage } from './dom.js?v=__ASSET_V__'
import { on, set, state, rows } from './store.js?v=__ASSET_V__'
import { openView, openMenu } from './views.js?v=__ASSET_V__'
import { openPush } from './push.js?v=__ASSET_V__'

const GROUPS = [
  ['needs', 'Needs you'],
  ['running', 'Running'],
  ['paused', 'Paused'],
  ['idle', 'Open'],
  ['todo', 'Backlog'],
  ['done', 'Done']
]
const DONE_SHOWN = 3

let selectCard = () => {}

export function initRail ({ select, unpair, newSession }) {
  selectCard = select
  $('#filter-icon').replaceWith(icon('search'))
  $('#filter').addEventListener('input', (e) => set({ filter: e.target.value.toLowerCase().trim() }))
  $('#filter').addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.target.value = ''; set({ filter: '' }); e.target.blur() }
    if (e.key === 'Enter') { const first = visibleIds()[0]; if (first) selectCard(first) }
  })
  $('#cards').addEventListener('click', (e) => {
    if (e.target.closest('.more-done')) { set({ doneAll: !state.doneAll }); return }
    const r = e.target.closest('.row')
    if (r) selectCard(r.dataset.id)
  })
  $('#kinds').addEventListener('click', (e) => {
    const b = e.target.closest('.chip')
    if (b) set({ kind: b.dataset.k })
  })
  $('#repos').addEventListener('click', (e) => {
    const b = e.target.closest('.chip')
    if (b) set({ repo: b.dataset.r })
  })
  renderFoot(unpair, newSession)
  on(['board', 'sel', 'filter', 'kind', 'repo', 'doneAll'], renderRail)
  on(['railManual'], applyRail)
  window.addEventListener('resize', applyRail)
  applyRail()
}

export function toggleRail () {
  if (isMobile()) return
  const compact = document.getElementById('app').classList.contains('rail-compact')
  set({ railManual: !compact })
  storage.set('railManual', !compact)
}

function applyRail () {
  const compact = isMobile() ? false : (state.railManual ?? window.innerWidth < 1280)
  const app = document.getElementById('app')
  app.classList.toggle('rail-compact', compact)
  const t = $('#rail-toggle')
  t.classList.toggle('on', !compact)
  t.setAttribute('aria-pressed', String(!compact))
}

function matches (r, repo) {
  if (state.kind !== 'all' && kindTag(r) !== state.kind) return false
  if (repo !== 'all' && (r.repo || '') !== repo) return false
  if (!state.filter) return true
  return `${r.id} ${r.title} ${r.stage} ${r.goal?.title || ''}`.toLowerCase().includes(state.filter)
}

// visibleIds is the rail's rows in on-screen order: what j and k walk.
export function visibleIds () {
  return [...document.querySelectorAll('#cards .row')].map(r => r.dataset.id)
}

function renderRail () {
  const all = rows()
  const tags = ['all', ...new Set(all.map(kindTag))]
  const kinds = $('#kinds')
  clear(kinds)
  for (const k of tags) {
    kinds.append(h('button', { class: ['chip', state.kind === k && 'on'], data: { k }, testid: `rail-kind-${k}`, 'aria-pressed': String(state.kind === k), type: 'button' }, k))
  }
  // the repo chips exist only where the board spans repos (the default
  // counts as one of them); a board that does not has nothing to tell apart
  const repos = [...new Set(all.map(r => r.repo || ''))].sort()
  const spread = repos.length > 1
  renderRepos(repos, spread)
  const vis = all.filter(r => matches(r, spread ? state.repo : 'all'))
  const box = $('#cards')
  const top = box.scrollTop
  clear(box)
  if (!state.board) {
    box.append(h('div', { class: 'empty' }, h('span', { class: 'spinner' })))
    return
  }
  for (const [k, label] of GROUPS) {
    let list = vis.filter(r => r.status === k)
    if (!list.length) continue
    const total = list.length
    const fold = k === 'done' && !state.doneAll && !state.filter && total > DONE_SHOWN
    if (fold) list = list.slice(0, DONE_SHOWN)
    box.append(h('section', { class: ['group', k], testid: `rail-group-${k}`, 'aria-label': label },
      h('h3', null, label, h('span', null, String(total))),
      list.map(r => rowEl(r, spread)),
      k === 'done' && total > DONE_SHOWN && !state.filter
        ? h('button', { class: 'more-done', testid: 'rail-more-done', type: 'button' }, state.doneAll ? 'Show fewer' : `Show all ${total}`)
        : null))
  }
  if (!box.children.length) {
    box.append(h('div', { class: 'empty', testid: 'rail-empty' }, all.length ? 'No cards match.' : h('span', null, h('b', null, 'No cards yet'), h('br'), 'Start with New session, or New card for work that walks the stages.')))
  }
  box.scrollTop = top
  box.querySelector('.row.sel')?.scrollIntoView?.({ block: 'nearest' })
}

function renderRepos (repos, spread) {
  const box = $('#repos')
  box.hidden = !spread
  clear(box)
  if (!spread) return
  for (const k of ['all', ...repos]) {
    box.append(h('button', { class: ['chip', state.repo === k && 'on'], data: { r: k }, testid: `rail-repo-${k || 'default'}`, 'aria-pressed': String(state.repo === k), type: 'button' }, k || 'default'))
  }
}

function rowEl (r, spread) {
  const sel = r.id === state.sel
  const dc = needsColor(r.needs, r.stage)
  const badge = r.status === 'needs'
    ? h('span', { class: 'badge needs' }, needsWord(r.needs, r.stage))
    : r.status === 'running'
      ? h('span', { class: ['badge run', r.running?.verb !== 'queued' && 'shimmer'], title: r.running?.why || null }, r.running?.pausing ? 'pausing' : r.running?.verb === 'queued' ? 'queued' : (r.running?.autopilot || r.autopilot) ? 'autopilot' : (r.running?.verb || 'working'))
      : r.status === 'watching' ? h('span', { class: 'badge watch' }, 'watching')
        : r.status === 'paused' ? h('span', { class: 'badge paused' }, 'paused') : null
  const title = [r.id, r.title, r.needs?.question, r.running?.why].filter(Boolean).join(' · ')
  return h('button', {
    class: ['row', sel && 'sel', r.status === 'needs' && 'needs'],
    style: { '--dc': dc },
    data: { id: r.id, status: r.status },
    testid: `rail-row-${r.id}`,
    'aria-current': sel ? 'true' : null,
    title,
    type: 'button'
  },
  h('span', { class: ['g', `st-${r.stage}`], 'aria-hidden': 'true' }, GLYPH[r.stage] || '·'),
  h('span', { class: 't' }, r.title),
  h('span', { class: 'id' }, r.id),
  h('span', { class: 'meta' },
    spread && r.repo ? h('span', { class: 'repo', style: { '--rc': `var(--r${repoSlot(r.repo)})` }, title: `repository ${r.repo}`, testid: `rail-row-repo-${r.id}` }, r.repo) : null,
    r.stage === 'open' || r.kind === 'freeform' ? h('span', { class: 'ff' }, 'session') : strip(r.stage),
    badge,
    r.waits?.length ? h('span', { class: 'waits' }, `waits on ${r.waits.join(', ')}`) : null,
    r.stack ? h('span', { class: ['badge stack', r.stack.stale && 'stale'], title: r.stack.name }, `stack ${r.stack.pos + 1} of ${r.stack.of}`) : null,
    r.elsewhere ? h('span', { class: 'badge else', title: 'Another gummi is driving this card' }, 'elsewhere') : null,
    h('span', { class: 'meter' }, `${r.spend ? cr(r.spend) : '—'}/${r.envelope || '∞'}`)))
}

function strip (stage) {
  const idx = STAGES.indexOf(stage)
  return h('span', { class: 'strip', 'aria-hidden': 'true' }, STAGES.slice(1).map((s, i) => {
    const done = i + 1 < idx
    const now = i + 1 === idx
    return h('i', { class: [`st-${s}`, done && 'past', now && 'now'] })
  }))
}

function renderFoot (unpair, newSession) {
  const foot = $('#rail-foot')
  const more = h('button', { testid: 'rail-more', title: 'More', 'aria-haspopup': 'menu', 'aria-expanded': 'false', type: 'button' }, icon('more'), h('span', { class: 'lbl' }, 'More'))
  more.addEventListener('click', () => {
    const item = (name, label, ic) => ({ label, icon: ic, testid: `menu-${name}`, onClick: () => openView(name) })
    const items = [
      item('goals', 'Goals', 'goal'),
      item('stacks', 'Stacks', 'stack'),
      item('ingest', 'Import spec', 'import'),
      item('bugs', 'Import bugs', 'bug'),
      item('doctor', 'Doctor', 'doctor')
    ]
    items.push('sep', { label: 'Notifications on this device', icon: 'bell', testid: 'menu-push', onClick: openPush })
    if (!state.session?.openAccess) items.push({ label: 'Unpair this browser', icon: 'unpair', danger: true, testid: 'menu-unpair', onClick: unpair })
    openMenu(more, items, { up: true, testid: 'rail-more-menu' })
  })
  foot.append(
    h('button', { class: 'newcard newsession', testid: 'rail-new-session', title: 'New session: one agent in its own worktree, no stages', type: 'button', onclick: () => newSession?.() }, h('span', { class: 'plus', 'aria-hidden': 'true' }, '◆'), h('span', { class: 'lbl' }, 'New session')),
    h('button', { class: 'newcard', testid: 'rail-new', title: 'New card', type: 'button', onclick: () => openView('newcard') }, h('span', { class: 'plus', 'aria-hidden': 'true' }, '+'), h('span', { class: 'lbl' }, 'New card')),
    h('button', { testid: 'rail-agent', title: 'Board agent', type: 'button', onclick: () => openView('agent') }, icon('agent'), h('span', { class: 'lbl' }, 'Board agent'), h('span', { class: 'sub', id: 'agent-sub' })),
    h('button', { testid: 'rail-fleet', title: 'Fleet stats', type: 'button', onclick: () => openView('fleet') }, icon('fleet'), h('span', { class: 'lbl' }, 'Fleet stats'), h('span', { class: 'sub' }, '7 days')),
    more)
}
