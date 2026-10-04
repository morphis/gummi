// head.js — the open card's head: kind, id and title, its menu, the stage
// strip (a past stage jumps to that stage in the thread), branch, waits,
// and the spend bar against its envelope.

import { $, h, icon, clear, append, kindTag, STAGES, cr, ctxMeter, isMobile } from './dom.js?v=__ASSET_V__'
import { on, state, row } from './store.js?v=__ASSET_V__'
import { openMenu, openView } from './views.js?v=__ASSET_V__'
import { runAction } from './actions.js?v=__ASSET_V__'
import { draftHead, writeSpecButton } from './session.js?v=__ASSET_V__'

let ctx = {}

export function initHead (c) {
  ctx = c
  on(['card', 'sel', 'board', 'cardErr', 'rightHidden', 'view', 'sessionDraft'], render)
  if (typeof ResizeObserver !== 'undefined') new ResizeObserver(fit).observe($('#head'))
}

// TITLE_KEEPS is how much of a title (px) the head keeps on screen before its
// buttons give way: below it, they fold one by one into the card's menu
// ("⋯", which offers every one of them too), the least needed first.
const TITLE_KEEPS = 240

// fit folds the head's buttons until the title has its room — or no
// button is left to fold. Each button names its rank (data-fold, lowest
// folds first); a button folded is still the card's menu entry.
function fit () {
  const el = $('#head')
  const t = el?.querySelector('.head-row h1')
  if (!t) return
  const btns = [...el.querySelectorAll('.head-actions [data-fold]')].sort((a, b) => a.dataset.fold - b.dataset.fold)
  for (const b of btns) b.classList.remove('folded')
  // a phone's head already keeps only what a thumb needs (the others are
  // hide-s): what is left there stays
  if (isMobile()) return
  for (const b of btns) {
    if (t.clientWidth >= Math.min(t.scrollWidth, TITLE_KEEPS)) break
    b.classList.add('folded')
  }
}

function render () {
  const el = $('#head')
  clear(el)
  if (!state.sel && state.sessionDraft) {
    append(el, draftHead())
    return
  }
  const c = state.card || row(state.sel)
  if (!c) {
    el.append(h('div', { class: 'head-row' }, h('h1', { testid: 'card-title' }, !state.sel ? 'No card open' : state.gone === state.sel ? `${state.sel} · deleted` : state.sel)))
    return
  }
  const actions = state.card?.actions || []
  // pause earns the head only while there is a run to pause: on a settled
  // session the same entry is "park", which changes nothing on screen
  // and stays in the menu
  const prominent = actions.find(a => a.id === 'resume' || (a.id === 'pause' && (c.status === 'running' || !!c.running)))
  const menuBtn = actions.length
    ? h('button', { class: 'iconbtn', id: 'card-actions', testid: 'card-actions', title: 'Card actions', 'aria-label': 'Card actions', 'aria-haspopup': 'menu', 'aria-expanded': 'false', type: 'button' }, icon('more'))
    : null
  menuBtn?.addEventListener('click', () => openActions())
  const panelOpen = !state.rightHidden
  append(el, [
    h('div', { class: 'head-row' },
      h('span', { class: 'kind', testid: 'card-kind' }, kindTag(c)),
      h('span', { class: 'cid', testid: 'card-id' }, c.id),
      h('h1', { testid: 'card-title', title: c.title }, c.title),
      h('div', { class: 'head-actions' },
        foldAt(1, commitButton(c, actions)),
        foldAt(3, landButton(c, actions)),
        foldAt(0, writeSpecButton(state.card)),
        prominent ? h('button', { class: ['btn', c.running?.pausing && 'on'], testid: `action-btn-${prominent.id}`, type: 'button', title: prominent.detail || prominent.label, data: { fold: '2' }, onclick: () => runAction(state.card, prominent) }, prominent.label) : null,
        menuBtn,
        h('button', { class: ['iconbtn', panelOpen && 'on'], testid: 'toggle-panel', title: 'Show or hide the document panel (])', 'aria-label': 'Toggle document panel', 'aria-pressed': String(panelOpen), type: 'button', onclick: ctx.togglePanel }, icon('panel')))),
    h('div', { class: 'subline' },
      stages(c),
      c.branch ? h('span', { class: 'mono', testid: 'card-branch' }, c.branch) : null,
      c.adopted ? h('span', null, 'adopted branch') : null,
      c.base && c.branch ? h('span', null, 'onto ', h('span', { class: 'mono' }, c.base)) : null,
      c.scratch ? h('span', { testid: 'card-scratch' }, 'scratch tree · no branch') : null,
      c.elsewhere ? h('span', null, 'driven by another gummi') : null,
      c.waits?.length ? h('span', null, 'waits on ', c.waits.map((w, i) => [i ? ', ' : '', h('button', { class: 'link', type: 'button', onclick: () => ctx.select(w) }, w)])) : null,
      c.kind === 'goal' ? h('button', { class: 'link', type: 'button', testid: 'card-goal', title: 'Open the goal page: its budget, done-when, cards and log', onclick: () => openView('goal', { id: c.id }) }, 'goal page') : null,
      c.goal ? h('button', { class: 'link', type: 'button', testid: 'card-goal', title: c.goal.title ? `Open the goal: ${c.goal.title}` : 'Open the goal', onclick: () => openView('goal', { id: c.goal.id }) }, 'goal ', h('span', { class: 'mono' }, c.goal.id)) : null,
      c.stack ? h('button', { class: ['badge stack', c.stack.stale && 'stale'], type: 'button', testid: 'card-stack', title: `Open the stack ${c.stack.name || c.stack.id}`, onclick: () => openView('stacks', { id: c.stack.id }) }, `stack ${c.stack.pos + 1} of ${c.stack.of}`) : null,
      spend(c),
      ctxMeter(c.context, 'card-context')),
    state.cardErr && !state.card ? h('div', { class: 'subline badc', testid: 'card-error' }, state.cardErr.message) : null])
  fit()
}

// foldAt ranks a head button for fit: the lower, the sooner it folds.
function foldAt (rank, btn) {
  if (btn) btn.dataset.fold = String(rank)
  return btn
}

// landButton is an open session's landing, in its head: a session pins no
// decision while it is idle (DESIGN §19.8), so its way onto the base is
// here, as its merge action — which shows the drafted message first.
function landButton (c, actions) {
  if (!c.session || c.stage !== 'open') return null
  const merge = actions.find(a => a.id === 'merge')
  if (!merge) return null
  return h('button', { class: 'btn pri hide-s', type: 'button', testid: 'session-land', title: merge.detail || 'Land this session’s branch', onclick: () => runAction(state.card, merge) }, 'Land…')
}

// commitButton commits an open session's worktree with the person's own
// message. Between turns gummi leaves a session's work uncommitted (only
// Land sweeps what is left into a final checkpoint), so this is shown
// whenever the worktree holds something to commit (the server lists the
// action only then).
function commitButton (c, actions) {
  if (!c.session || c.stage !== 'open') return null
  const commit = actions.find(a => a.id === 'commit')
  if (!commit) return null
  return h('button', { class: 'btn hide-s', type: 'button', testid: 'session-commit', title: commit.detail || 'Commit the worktree', onclick: () => runAction(state.card, commit) }, 'Commit…')
}

// openActions drops the card's menu from the head's "⋯". With a line (a
// composer line the server routed to the menu, "/rebase"), the entries
// that line names come first. It answers true when the menu opened — a
// line routed here has nowhere to go when it did not, and stays in the
// composer instead.
export function openActions (line = '') {
  const btn = document.getElementById('card-actions')
  const actions = state.card?.actions || []
  if (!btn || !actions.length) return false
  const word = String(line).trim().replace(/^\//, '').split(/\s+/)[0].toLowerCase()
  const hit = (a) => word && (a.id.toLowerCase().startsWith(word) || a.label.toLowerCase().startsWith(word))
  const list = word ? [...actions.filter(hit), ...actions.filter(a => !hit(a))] : actions
  // no key hints: the letters are the terminal's, and most of them mean
  // something else on this page
  const items = list.map(a => ({
    label: a.label, danger: a.danger, testid: `action-${a.id}`, hint: a.detail, onClick: () => runAction(state.card, a)
  }))
  openMenu(btn, items, { testid: 'card-actions-menu' })
  return true
}

function stages (c) {
  // an open session has no stages at all: ticking the workflow's would
  // claim a plan, an implement and a verify it never had, so it shows no
  // badge while open. Once closed it still isn't done in the workflow's
  // sense, but the card needs some word for what happened to it.
  if (c.stage === 'open') return null
  if (c.kind === 'freeform') {
    return h('span', { class: 'stages', testid: 'card-stages' }, h('button', { class: 'cur st-open', type: 'button' }, '◆ session · closed'))
  }
  const idx = STAGES.indexOf(c.stage)
  return h('span', { class: 'stages', testid: 'card-stages' }, STAGES.map((s, i) => [
    h('button', {
      class: [i < idx && 'past', i === idx && 'cur', `st-${s}`],
      type: 'button',
      data: { stage: s },
      testid: `stage-${s}`,
      'aria-current': i === idx ? 'step' : null,
      title: i < idx && i > 0 ? `Show the ${s} stage in the thread` : null,
      onclick: i < idx && i > 0 ? () => ctx.jumpToStage(s) : null
    }, i < idx ? '✓ ' : '', s),
    i < STAGES.length - 1 ? h('em', { 'aria-hidden': 'true' }, '›') : null
  ]))
}

function spend (c) {
  const env = c.envelope || 0
  const pct = env ? Math.min(100, (c.spend / env) * 100) : 0
  // on a session the budget is one click away, since running out of it is
  // how a session stops: the card's own envelope action raises it
  const raise = c.session && state.card?.actions?.find(a => a.id === 'envelope')
  if (raise) {
    return h('button', { class: ['spend', 'link-spend', env && c.spend > env && 'over'], type: 'button', testid: 'card-spend', title: `${cr(c.spend)} of a ${env || '∞'} credit budget — change it`, onclick: () => runAction(state.card, raise) },
      h('span', { class: 'bar' }, h('i', { style: { '--pct': pct + '%' } })),
      h('span', { class: 'mono' }, `${cr(c.spend)} / ${env || '∞'} cr`))
  }
  return h('span', { class: ['spend', env && c.spend > env && 'over'], testid: 'card-spend', title: env ? `${cr(c.spend)} of a ${env} credit envelope` : 'No envelope' },
    h('span', { class: 'bar' }, h('i', { style: { '--pct': pct + '%' } })),
    h('span', { class: 'mono' }, `${cr(c.spend)} / ${env || '∞'} cr`))
}
