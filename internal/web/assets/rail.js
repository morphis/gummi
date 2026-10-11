// rail.js — the card rail: the board's rows grouped by status (in the
// server's order within a group) or by repository, a filter box and one
// menu for the rest (grouping, kind, status, repository, what a row
// shows), a compact form below 1280px or on `[`, and the foot that opens
// the board's other surfaces (new session, new card, fleet stats, More).

import { $, h, icon, clear, kindTag, repoSlot, needsWord, STAGES, cr, isMobile, storage } from './dom.js?v=__ASSET_V__'
import { on, set, state, rows } from './store.js?v=__ASSET_V__'
import { openView, openMenu } from './views.js?v=__ASSET_V__'
import { openPush } from './push.js?v=__ASSET_V__'

const GROUPS = [
  ['needs', 'Needs you'],
  ['running', 'Running'],
  ['watching', 'Watching'],
  ['paused', 'Paused'],
  ['idle', 'Open'],
  ['todo', 'Backlog'],
  ['done', 'Done']
]
const DONE_SHOWN = 3
const KINDS = { FD: 'Feature', BG: 'Bug', RS: 'Research', GL: 'Goal', FF: 'Session' }
// SHOWS is what a row's second line may add to its status word
const SHOWS = [['stage', 'Stage'], ['stack', 'Stack position'], ['spend', 'Spend']]
// NEAR is how much of its budget a card has spent before the row says so
const NEAR = 0.8

let selectCard = () => {}

export function initRail ({ select, unpair, newSession }) {
  selectCard = select
  $('#filter-icon').replaceWith(icon('search'))
  $('#filter-menu').append(icon('filter'))
  $('#filter-menu').addEventListener('click', openFilters)
  $('#filter').addEventListener('input', (e) => set({ filter: e.target.value.toLowerCase().trim() }))
  $('#filter').addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.target.value = ''; set({ filter: '' }); e.target.blur() }
    if (e.key === 'Enter') { const first = visibleIds()[0]; if (first) selectCard(first) }
  })
  const cards = $('#cards')
  cards.addEventListener('click', (e) => {
    if (e.target.closest('.more-done')) { set({ doneAll: !state.doneAll }); return }
    if (e.target.closest('.clear-filters')) { clearFilters(); return }
    const r = e.target.closest('.row')
    if (r) { hideHover(); selectCard(r.dataset.id) }
  })
  // a row's facts wait behind a pause of the pointer on it; a touch
  // screen has no hover, and its card screen shows them instead
  cards.addEventListener('pointerover', (e) => { if (e.pointerType === 'mouse') armHover(e.target.closest('.row')) })
  cards.addEventListener('pointerleave', hideHover)
  cards.addEventListener('scroll', hideHover, { passive: true })
  // nor does it stand over what a key just opened (the palette, a dialog)
  document.addEventListener('keydown', hideHover)
  $('#filter-tokens').addEventListener('click', (e) => {
    const b = e.target.closest('button')
    if (!b) return
    if (b.dataset.clear) clearFilters()
    else untoken(b.dataset.t, b.dataset.v)
    $('#filter').focus()
  })
  renderFoot(unpair, newSession)
  on(['board', 'sel', 'filter', 'kinds', 'statuses', 'repo', 'doneAll', 'railGroup', 'railShow'], renderRail)
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
  hideHover()
  app.classList.toggle('rail-compact', compact)
  const t = $('#rail-toggle')
  t.classList.toggle('on', !compact)
  t.setAttribute('aria-pressed', String(!compact))
}

// quality is the one thing a row's dot says: what the card is waiting on,
// never which stage it is in.
function quality (r) {
  if (r.status === 'needs') {
    if (r.needs?.color === 'err') return 'failed'
    return r.needs?.color === 'ok' && r.stage === 'verify' ? 'ready' : 'needs'
  }
  if (r.status === 'idle') return r.stage === 'open' || r.kind === 'freeform' ? 'session' : 'idle'
  return r.status
}

// word is the row's status in words, the second line's lead.
function word (r) {
  if (r.status === 'needs') return needsWord(r.needs, r.stage)
  if (r.status === 'running') return r.running?.pausing ? 'pausing' : (r.running?.autopilot || r.autopilot) ? 'autopilot' : (r.running?.verb || 'working')
  if (r.status === 'watching') return 'watching'
  if (r.status === 'paused') return 'paused'
  const session = r.stage === 'open' || r.kind === 'freeform'
  if (r.status === 'done') return [session && 'session', r.landed && 'landed'].filter(Boolean).join(' · ')
  return session ? 'session' : ''
}

const spread = () => new Set(rows().map(r => r.repo || '')).size > 1

function matches (r, repo) {
  if (state.kinds.length && !state.kinds.includes(kindTag(r))) return false
  if (state.statuses.length && !state.statuses.includes(r.status)) return false
  if (repo !== 'all' && (r.repo || '') !== repo) return false
  if (!state.filter) return true
  const group = GROUPS.find(g => g[0] === r.status)?.[1] || ''
  return `${r.id} ${r.title} ${r.stage} ${word(r)} ${group} ${KINDS[kindTag(r)] || ''} ${r.repo || ''} ${r.pr || ''} ${r.stack?.name || ''} ${r.goal?.title || ''}`.toLowerCase().includes(state.filter)
}

// visibleIds is the rail's rows in on-screen order: what j and k walk.
export function visibleIds () {
  return [...document.querySelectorAll('#cards .row')].map(r => r.dataset.id)
}

function clearFilters () {
  $('#filter').value = ''
  set({ filter: '', kinds: [], statuses: [], repo: 'all' })
  setGroup('status')
}

function untoken (t, v) {
  if (t === 'kind') set({ kinds: state.kinds.filter(k => k !== v) })
  if (t === 'status') set({ statuses: state.statuses.filter(k => k !== v) })
  if (t === 'repo') set({ repo: 'all' })
  if (t === 'group') setGroup('status')
}

function setGroup (g) { set({ railGroup: g }); storage.set('railGroup', g) }

const flip = (list, v) => list.includes(v) ? list.filter(x => x !== v) : [...list, v]

// openFilters drops the one menu that holds what the chips used to: how
// the rows are grouped, which kinds, statuses and repository show (each
// with how many cards it has), and what a row's second line adds.
function openFilters () {
  const all = rows()
  const n = (fn) => all.filter(fn).length
  const repos = [...new Set(all.map(r => r.repo || ''))].sort()
  // the repositories exist only where the board spans them (the default
  // counts as one): a board that does not has nothing to tell apart
  const wide = repos.length > 1
  const items = [
    ...(wide
      ? [{ head: 'Group by' },
          { label: 'Status', radio: true, testid: 'rail-group-by-status', checked: () => state.railGroup !== 'repo', onClick: () => setGroup('status') },
          { label: 'Repository', radio: true, testid: 'rail-group-by-repo', checked: () => state.railGroup === 'repo', onClick: () => setGroup('repo') }]
      : []),
    ...(all.length ? [{ head: 'Kind' }] : []),
    ...[...new Set(all.map(kindTag))].map(k => ({
      label: KINDS[k] || k, count: n(r => kindTag(r) === k), testid: `rail-kind-${k}`, checked: () => state.kinds.includes(k), onClick: () => set({ kinds: flip(state.kinds, k) })
    })),
    ...(all.length ? [{ head: 'Status' }] : []),
    ...GROUPS.filter(([k]) => n(r => r.status === k)).map(([k, label]) => ({
      label, count: n(r => r.status === k), testid: `rail-status-${k}`, checked: () => state.statuses.includes(k), onClick: () => set({ statuses: flip(state.statuses, k) })
    }))
  ]
  if (wide) {
    items.push({ head: 'Repository' }, ...repos.map(k => ({
      label: k || 'default', radio: true, count: n(r => (r.repo || '') === k), testid: `rail-repo-${k || 'default'}`, checked: () => state.repo === k, onClick: () => set({ repo: state.repo === k ? 'all' : k })
    })))
  }
  items.push({ head: 'A row shows' }, ...SHOWS.map(([k, label]) => ({
    label, testid: `rail-show-${k}`, checked: () => state.railShow.includes(k), onClick: () => { const next = flip(state.railShow, k); set({ railShow: next }); storage.set('railShow', next) }
  })))
  items.push('sep', { label: 'Clear filters', testid: 'rail-clear', onClick: clearFilters })
  openMenu($('#filter-menu'), items, { testid: 'rail-filter-menu' })
}

// a repository filter left from a board that has since narrowed to one
// repository filters nothing, and is not counted
const filtersOn = () => state.kinds.length + state.statuses.length + (spread() && state.repo !== 'all' ? 1 : 0)

// renderTokens names every filter that applies under the box, each with
// its own way off — a filter is never on without saying so.
function renderTokens (wide) {
  const box = $('#filter-tokens')
  clear(box)
  const tok = (t, v, label) => h('span', { class: 'tok', testid: `rail-token-${t}-${v || 'default'}` }, label,
    h('button', { type: 'button', data: { t, v }, 'aria-label': `Remove the filter ${label}`, title: 'Remove this filter' }, '×'))
  const toks = [
    ...state.kinds.map(k => tok('kind', k, (KINDS[k] || k).toLowerCase())),
    ...state.statuses.map(k => tok('status', k, (GROUPS.find(g => g[0] === k)?.[1] || k).toLowerCase())),
    wide && state.repo !== 'all' ? tok('repo', state.repo, state.repo || 'default') : null,
    wide && state.railGroup === 'repo' ? tok('group', 'repo', 'by repository') : null
  ].filter(Boolean)
  if (toks.length > 1) toks.push(h('button', { class: 'tok clear', type: 'button', data: { clear: '1' }, testid: 'rail-tokens-clear' }, 'clear'))
  box.append(...toks)
  box.hidden = !toks.length
  const n = filtersOn()
  const btn = $('#filter-menu')
  btn.classList.toggle('on', n > 0)
  btn.querySelector('.n')?.remove()
  if (n) btn.append(h('span', { class: 'n', 'aria-hidden': 'true' }, String(n)))
  btn.setAttribute('aria-label', n ? `Group, filter and row display — ${n} filter${n === 1 ? '' : 's'} on` : 'Group, filter and row display')
}

function renderRail () {
  const all = rows()
  const wide = spread()
  renderTokens(wide)
  const vis = all.filter(r => matches(r, wide ? state.repo : 'all'))
  const box = $('#cards')
  const top = box.scrollTop
  // the rows are drawn anew on every change, the one a keyboard just
  // activated included: focus goes back to that row (or the fold button),
  // not to <body> and the top of the page
  const a = document.activeElement
  const had = box.contains(a) ? (a.classList.contains('more-done') ? { more: true } : { id: a.dataset?.id }) : null
  const hovered = hoverFor?.dataset.id
  clear(box)
  if (!state.board) {
    box.append(h('div', { class: 'empty' }, h('span', { class: 'spinner' })))
    return
  }
  // the compact rail has no room for the filter box and its menu: a filter
  // still applying says so at its top, and opens the full rail to change it
  const filtered = [state.filter && `“${state.filter}”`, ...state.kinds, ...state.statuses, wide && state.repo !== 'all' && (state.repo || 'default')].filter(Boolean)
  if (filtered.length) {
    box.append(h('button', {
      class: 'rail-filtered', type: 'button', testid: 'rail-filtered',
      title: `Filtered: ${filtered.join(' · ')} — show the filters`,
      'aria-label': `Cards filtered by ${filtered.join(', ')}. Show the filters`,
      onclick: () => { set({ railManual: false }); storage.set('railManual', false) }
    }, icon('search'), h('span', null, state.kinds[0] || '…')))
  }
  if (state.railGroup === 'repo' && wide) {
    for (const repo of [...new Set(all.map(r => r.repo || ''))].sort()) {
      const list = vis.filter(r => (r.repo || '') === repo)
      if (!list.length) continue
      box.append(h('section', { class: 'group', testid: `rail-repo-group-${repo || 'default'}`, 'aria-label': repo || 'default' },
        h('h3', null, repo || 'default', h('span', null, String(list.length))),
        list.map(r => rowEl(r, false))))
    }
  } else {
    for (const [k, label] of GROUPS) {
      let list = vis.filter(r => r.status === k)
      if (!list.length) continue
      const total = list.length
      const fold = k === 'done' && !state.doneAll && !state.filter && total > DONE_SHOWN
      if (fold) {
        // a folded card that is open (from the palette, a link) still shows,
        // highlighted, under the ones the fold keeps: j and k walk on from it
        const open = list.slice(DONE_SHOWN).find(r => r.id === state.sel)
        list = list.slice(0, DONE_SHOWN)
        if (open) list.push(open)
      }
      const more = state.doneAll ? 'Show fewer' : `Show all ${total}`
      box.append(h('section', { class: ['group', k], testid: `rail-group-${k}`, 'aria-label': label },
        h('h3', null, label, h('span', null, String(total))),
        list.map(r => rowEl(r, wide)),
        k === 'done' && total > DONE_SHOWN && !state.filter
          ? h('button', { class: 'more-done', testid: 'rail-more-done', type: 'button', 'aria-label': more, title: more },
            h('span', { class: 'lbl' }, more), h('span', { class: 'cmp', 'aria-hidden': 'true' }, state.doneAll ? '−' : `+${total - DONE_SHOWN}`))
          : null))
    }
  }
  if (!box.querySelector('.row')) {
    box.append(h('div', { class: 'empty', testid: 'rail-empty' }, all.length
      ? h('span', null, 'No cards match.', h('br'), h('button', { class: 'link clear-filters', type: 'button', testid: 'rail-empty-clear' }, 'Clear filters'))
      : h('span', null, h('b', null, 'No cards yet'), h('br'), 'Start with New session, or New card for work that walks the stages.')))
  }
  // a board that changed under a resting pointer keeps that row's card up
  const still = hovered && box.querySelector(`.row[data-id="${CSS.escape(hovered)}"]`)
  if (still) hoverFor = still
  else hideHover()
  box.scrollTop = top
  box.querySelector('.row.sel')?.scrollIntoView?.({ block: 'nearest' })
  if (had) {
    const back = had.more ? box.querySelector('.more-done') : had.id && box.querySelector(`.row[data-id="${CSS.escape(had.id)}"]`)
    back?.focus({ preventScroll: true })
  }
}

// near says a card has spent most of its budget: the one time a row
// names the budget at all.
const near = (r) => !!r.envelope && r.spend / r.envelope >= NEAR

function rowEl (r, wide) {
  const sel = r.id === state.sel
  const q = quality(r)
  const w = word(r)
  const show = state.railShow
  // a card between its stages (or with its stage asked for) names it: the
  // word of a card that needs you or is running already does
  const staged = STAGES.includes(r.stage) && r.stage !== 'todo' && r.stage !== 'done' && (show.includes('stage') || !w)
  const sep = () => h('i', { 'aria-hidden': 'true' }, '·')
  const bits = [
    wide && r.repo ? h('span', { class: 'repo', style: { '--rc': `var(--r${repoSlot(r.repo)})` }, title: `repository ${r.repo}`, testid: `rail-row-repo-${r.id}` }, r.repo) : null,
    w ? h('span', { class: ['word', `q-${q}`, r.status === 'running' && 'shimmer'], testid: `rail-row-word-${r.id}` }, w) : null,
    staged ? h('span', { class: 'stage' }, r.stage) : null,
    r.severity && r.status === 'todo' ? h('span', null, r.severity) : null,
    r.objective ? h('span', { class: ['badge obj', `obj-${r.objective}`], title: `objective ${r.objective}`, testid: `rail-row-objective-${r.id}` }, `◆ ${r.objective}`) : null,
    r.waits?.length ? h('span', { class: 'waits' }, `waits on ${r.waits.join(', ')}`) : null,
    r.stack && show.includes('stack') ? h('span', { class: ['stackpos', r.stack.stale && 'stale'], title: r.stack.stale ? `${r.stack.name} — its base has moved` : r.stack.name }, `stack ${r.stack.pos + 1}/${r.stack.of}`) : null,
    r.elsewhere ? h('span', { class: 'else', title: 'Another gummi is driving this card' }, 'elsewhere') : null
  ].filter(Boolean)
  // the same figure as the card's head. The budget joins it only once
  // the card is near it; a card that spent nothing says nothing
  const money = near(r)
    ? h('span', { class: 'spend near', testid: `rail-row-spend-${r.id}` }, `${cr(r.spend)} of ${cr(r.envelope)}`)
    : show.includes('spend') && r.spend ? h('span', { class: 'spend', testid: `rail-row-spend-${r.id}` }, cr(r.spend)) : null
  return h('button', {
    class: ['row', sel && 'sel', r.status === 'needs' && 'needs'],
    data: { id: r.id, status: r.status, q },
    testid: `rail-row-${r.id}`,
    'aria-current': sel ? 'true' : null,
    type: 'button'
  },
  h('span', { class: ['dot', `q-${q}`], 'aria-hidden': 'true' }, q === 'done' ? '✔' : ''),
  h('span', { class: 't' }, r.title),
  h('span', { class: 'id' }, r.id),
  h('span', { class: 'meta' }, h('span', { class: 'l' }, bits.flatMap((b, i) => i ? [sep(), b] : [b])), money))
}

// ---- the hover card: what the row no longer spells out ----
let hoverT = 0
let hoverFor = null

function armHover (el) {
  if (el === hoverFor) return
  hideHover()
  if (!el) return
  hoverFor = el
  // by id: the board may redraw the row before the pause is over
  hoverT = setTimeout(() => showHover(el.dataset.id), 500)
}

function hideHover () {
  clearTimeout(hoverT)
  hoverFor = null
  document.getElementById('rowcard')?.remove()
}

function showHover (id) {
  const r = rows().find(x => x.id === id)
  const el = hoverFor
  if (!r || !el || el.dataset.id !== id || !document.contains(el)) return
  const pct = r.envelope ? Math.min(100, (r.spend / r.envelope) * 100) : 0
  const fact = (k, v) => v ? [h('dt', null, k), h('dd', null, v)] : []
  // told to the eye only: a screen reader has the row and, opened, the card's head
  const card = h('div', { class: 'rowcard', id: 'rowcard', testid: 'rail-row-card', 'aria-hidden': 'true' },
    h('b', null, r.title),
    h('dl', null,
      fact('Card', `${r.id} · ${(KINDS[kindTag(r)] || r.kind || '').toLowerCase()}`),
      fact('Status', [word(r), STAGES.includes(r.stage) ? r.stage : ''].filter(Boolean).join(' · ')),
      fact('Asks', r.needs?.question),
      fact('Doing', r.running?.why),
      fact('Repository', r.repo),
      fact('Stack', r.stack ? `${r.stack.name || 'stack'} · ${r.stack.pos + 1} of ${r.stack.of}${r.stack.stale ? ' · base moved' : ''}` : ''),
      fact('Goal', r.goal ? `${r.goal.id}${r.goal.title ? ' · ' + r.goal.title : ''}` : ''),
      fact('Waits on', r.waits?.join(', ')),
      fact('Pull request', r.pr),
      fact('Profile', r.profile),
      fact('Budget', `${cr(r.spend)} of ${r.envelope ? cr(r.envelope) : 'no cap'}`)),
    r.envelope ? h('span', { class: ['bar', near(r) && 'near'] }, h('i', { style: { '--pct': pct + '%' } })) : null)
  document.body.append(card)
  const b = el.getBoundingClientRect()
  card.style.setProperty('left', (b.right + 8) + 'px')
  card.style.setProperty('top', Math.max(8, Math.min(b.top, window.innerHeight - card.offsetHeight - 8)) + 'px')
}

function renderFoot (unpair, newSession) {
  const foot = $('#rail-foot')
  const item = (name, label, ic) => ({ label, icon: ic, testid: `menu-${name}`, onClick: () => openView(name) })
  const more = h('button', { class: 'iconbtn', testid: 'rail-more', title: 'More', 'aria-label': 'More', 'aria-haspopup': 'menu', 'aria-expanded': 'false', type: 'button' }, icon('more'))
  more.addEventListener('click', () => {
    const items = [
      item('goals', 'Goals', 'goal'),
      item('stacks', 'Stacks', 'stack'),
      item('repos', 'Repositories', 'repo'),
      item('schedules', 'Schedules', 'clock'),
      'sep',
      item('ingest', 'Import spec', 'import'),
      item('bugs', 'Import bugs', 'bug'),
      'sep',
      item('doctor', 'Doctor', 'doctor'),
      item('settings', 'Settings', 'settings')
    ]
    items.push({ label: 'Notifications on this device', icon: 'bell', testid: 'menu-push', onClick: openPush })
    if (!state.session?.openAccess) items.push('sep', { label: 'Unpair this browser', icon: 'unpair', danger: true, testid: 'menu-unpair', onClick: unpair })
    openMenu(more, items, { up: true, testid: 'rail-more-menu' })
  })
  // what else can be started, behind the one button's arrow
  const other = h('button', { class: 'split-more', testid: 'rail-new-menu', title: 'New card, or import', 'aria-label': 'New card, or import', 'aria-haspopup': 'menu', 'aria-expanded': 'false', type: 'button' }, icon('chevron'))
  other.addEventListener('click', () => openMenu(other, [
    { label: 'New card', icon: 'plus', testid: 'rail-new', hint: 'Work that walks the stages: plan, implement, verify', onClick: () => openView('newcard') },
    { ...item('ingest', 'Import spec', 'import'), testid: 'rail-new-ingest' },
    { ...item('bugs', 'Import bugs', 'bug'), testid: 'rail-new-bugs' }
  ], { up: true, testid: 'rail-new-menu-list' }))
  foot.append(
    h('div', { class: 'split' },
      h('button', { class: 'newsession', testid: 'rail-new-session', title: 'New session: one agent in its own worktree, no stages', type: 'button', onclick: () => newSession?.() }, h('span', { class: 'plus', 'aria-hidden': 'true' }, '◆'), h('span', { class: 'lbl' }, 'New session')),
      other),
    h('button', { class: 'iconbtn', testid: 'rail-fleet', title: 'Fleet stats (last 7 days)', 'aria-label': 'Fleet stats', type: 'button', onclick: () => openView('fleet') }, icon('fleet')),
    more)
}
