// session.js — a session from the page's side (DESIGN §19.8). A session is
// a freeform card: one agent in its own worktree, on the model its person
// picked, with no stages. Two things live here.
//
// The draft: "New session" opens no dialog. The conversation column becomes
// an empty session whose repository, base and budget sit in the composer,
// and the first message sent is what creates it (POST /api/cards) and its
// first turn.
//
// The model picker beside Send, on a draft and on an open session: the
// agents this host can run, the models the workspace's profiles already run
// on each, the pairs sessions on the board use, and any id typed in. On a
// session that exists, a pick is the card's "model" action. A card in the
// workflow shows no picker: its stages take their models from its profile.

import { $, h, clear, append, setVars, isMobile } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'

let ctx = {}
let form = null // webapi.Form: the draft's choices and the picker's catalog
let pop = null // the open popover: { el, close }

const BUDGETS = [50, 150, 500, 0]

export function initSession (c) {
  ctx = c
  // opening a card leaves the draft; what was typed stays in the composer
  // only while the draft is what it was typed into
  on(['sel'], () => { if (state.sel && state.sessionDraft) set({ sessionDraft: null }) })
  on(['sessionDraft', 'card', 'sel', 'conn'], render)
  render()
}

async function loadForm (repo = '') {
  try {
    form = await get('/api/form' + (repo ? `?repo=${encodeURIComponent(repo)}` : ''))
  } catch (err) {
    toast(`The model list did not load: ${err.message}`, { err: true })
  }
  return form
}

// newSession opens an empty draft in the conversation column.
export async function newSession () {
  closePop()
  // the draft opens at once, on what the page already knows; the form's
  // defaults fill it when they arrive, without touching what was typed
  const known = form
  const draftOf = (f) => {
    const d = f?.sessions?.default || {}
    return { repo: f?.repos?.[0] || '', base: '', envelope: f?.envelope || 0, backend: d.backend || '', model: d.model || '' }
  }
  set({
    sel: null,
    card: null,
    cardErr: null,
    thread: null,
    live: null,
    gone: null,
    sessionDraft: draftOf(known)
  })
  if (isMobile()) set({ view: 'thread' })
  ctx.clearComposer?.()
  $('#composer-input')?.focus()
  const f = await loadForm()
  if (!known && f && state.sessionDraft) set({ sessionDraft: { ...draftOf(f), ...pickedOnly(state.sessionDraft) } })
}

// pickedOnly is what a person changed in a draft before its defaults came.
function pickedOnly (d) {
  const out = {}
  for (const k of ['repo', 'base', 'backend', 'model']) if (d[k]) out[k] = d[k]
  if (d.envelope) out.envelope = d.envelope
  return out
}

// startSession sends the draft's first message: the card is created with
// it, the page moves onto the card, and its first turn is already running.
// A refusal is thrown back to the composer, which keeps the line.
export async function startSession (text) {
  const d = state.sessionDraft
  const req = { kind: 'freeform', description: text, backend: d.backend, model: d.model, envelope: d.envelope }
  if (d.repo) req.repo = d.repo
  if (d.base) req.base = d.base
  const c = await post('/api/cards', req)
  set({ sessionDraft: null })
  await ctx.refreshBoard?.()
  await ctx.select(c.id)
  toast(`${c.id} started on ${pairLabel(c.session || d)}`)
  return c
}

export function pairLabel (p) {
  return `${p.model || 'the default model'} · ${p.backend || 'default agent'}`
}

// draftHead is the conversation's head while a draft is open.
export function draftHead () {
  const d = state.sessionDraft
  return [
    h('div', { class: 'head-row' },
      h('span', { class: 'kind', testid: 'card-kind' }, 'session'),
      h('h1', { testid: 'card-title' }, 'New session'),
      h('div', { class: 'head-actions' },
        h('button', { class: 'btn', type: 'button', testid: 'draft-cancel', onclick: cancelDraft }, 'Cancel'))),
    h('div', { class: 'subline' },
      h('span', null, d.repo || state.board?.repo || 'this repository', d.base ? [' · from ', h('span', { class: 'mono' }, d.base)] : ' · a new worktree'),
      h('span', null, `budget ${d.envelope || '∞'} cr`))
  ]
}

// draftHero is the empty conversation a draft shows.
export function draftHero () {
  const d = state.sessionDraft
  const where = d.repo || state.board?.repo || 'this repository'
  const starters = ['Fix a failing test', 'Explain how this part of the code works', 'Review this branch before I merge it']
  return h('div', { class: 'draft-hero', testid: 'draft-hero' },
    h('b', null, `What are we working on in ${where}?`),
    h('p', null, 'A session is one agent in its own worktree, with no stages and no gates. It starts when you send the first message. The model is next to Send, and you can switch it later.'),
    h('div', { class: 'starters' }, starters.map(s => h('button', {
      class: 'btn', type: 'button', testid: 'draft-starter', onclick: () => { ctx.restoreComposer?.(s + ' '); $('#composer-input')?.focus() }
    }, s))))
}

function cancelDraft () {
  closePop()
  set({ sessionDraft: null })
  ctx.clearComposer?.()
  const first = state.board?.rows?.[0]?.id
  if (first) ctx.select(first)
}

// sessionPair is what the picker shows: the draft's pick, or the pair an
// open session runs on. null hides the picker.
function sessionPair () {
  if (state.sessionDraft) return state.sessionDraft
  const c = state.card
  if (c && c.session && c.stage === 'open' && c.actions?.some(a => a.id === 'model')) return c.session
  return null
}

function render () {
  const slot = $('#composer-model')
  const row = $('#composer-draft')
  if (!slot || !row) return
  clear(slot)
  clear(row)
  const p = sessionPair()
  if (p) {
    const btn = h('button', {
      class: 'mbtn', type: 'button', testid: 'model-picker-btn',
      title: 'The agent and model this session runs on',
      'aria-haspopup': 'dialog', 'aria-expanded': 'false',
      onclick: () => (pop?.kind === 'model' ? closePop() : openPicker(btn))
    }, h('span', { class: 'mono m' }, p.model || 'default model'), h('span', { class: 'mono a' }, p.backend || 'agent'), h('span', { class: 'caret', 'aria-hidden': 'true' }, '▴'))
    slot.append(btn)
  }
  const d = state.sessionDraft
  row.hidden = !d
  if (!d) return
  const repos = form?.repos || []
  if (repos.length > 1) {
    row.append(h('label', { class: 'dsel' }, h('span', { class: 'sr-only' }, 'Repository'),
      h('select', {
        testid: 'draft-repo',
        onchange: async (e) => {
          await loadForm(e.target.value)
          set({ sessionDraft: { ...state.sessionDraft, repo: e.target.value, base: '' } })
        }
      }, repos.map(r => h('option', { value: r, selected: r === d.repo }, r)))))
  }
  const branches = form?.branches || []
  row.append(h('label', { class: 'dsel' }, h('span', { class: 'lbl' }, 'from'),
    h('select', {
      testid: 'draft-base', 'aria-label': 'Branch the session forks from',
      onchange: (e) => set({ sessionDraft: { ...state.sessionDraft, base: e.target.value } })
    }, h('option', { value: '', selected: !d.base }, 'the default branch'), branches.map(b => h('option', { value: b, selected: b === d.base }, b)))))
  row.append(h('span', { class: 'dnote-s' }, 'new worktree'))
  const bud = h('button', {
    class: 'dsel dbtn', type: 'button', testid: 'draft-budget', 'aria-haspopup': 'dialog',
    onclick: () => (pop?.kind === 'budget' ? closePop() : openBudget(bud))
  }, 'budget ', h('b', null, d.envelope ? `${d.envelope} cr` : 'uncapped'))
  row.append(bud)
}

// ---- popovers ----

function closePop () {
  if (!pop) return
  const p = pop
  pop = null
  p.close()
}

// placeAbove pins a popover's bottom edge just above anchor, its right edge
// to the anchor's (left, for one opened from the left of the composer).
function openPop (kind, anchor, el, { alignLeft = false } = {}) {
  closePop()
  document.body.append(el)
  const r = anchor.getBoundingClientRect()
  // pinned above the anchor, and kept inside the window whichever edge it
  // hangs from: on a phone the picker is wider than the space left of Send
  const w = el.offsetWidth
  const left = alignLeft ? r.left : r.right - w
  setVars(el, {
    bottom: Math.max(8, window.innerHeight - r.top + 6) + 'px',
    left: Math.max(8, Math.min(left, window.innerWidth - w - 8)) + 'px',
    right: null
  })
  anchor.setAttribute('aria-expanded', 'true')
  const outside = (e) => { if (!el.contains(e.target) && !anchor.contains(e.target)) closePop() }
  // every key typed in a popover is the popover's: the page's shortcuts
  // (] hides the panel, [ the rail) would move the layout out from under it
  const keys = (e) => {
    e.stopPropagation()
    if (e.key === 'Escape') { closePop(); anchor.focus() }
  }
  // a resize here is usually the on-screen keyboard, raised by this same
  // popover's own search/budget input grabbing focus below — not the page
  // moving out from under it, so it must not close what focus just opened
  const moved = () => { if (!el.contains(document.activeElement)) closePop() }
  setTimeout(() => document.addEventListener('mousedown', outside), 0)
  el.addEventListener('keydown', keys)
  window.addEventListener('resize', moved)
  pop = {
    kind,
    el,
    close: () => {
      document.removeEventListener('mousedown', outside)
      window.removeEventListener('resize', moved)
      anchor.setAttribute('aria-expanded', 'false')
      el.remove()
    }
  }
}

function openBudget (anchor) {
  const d = state.sessionDraft
  const input = h('input', { class: 'inp', type: 'number', min: '0', step: '10', value: String(d.envelope || 0), testid: 'draft-budget-input', 'aria-label': 'Budget in credits' })
  const apply = (v) => { set({ sessionDraft: { ...state.sessionDraft, envelope: Math.max(0, parseInt(v, 10) || 0) } }); closePop() }
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); apply(input.value) } })
  const el = h('div', { class: 'spop', role: 'dialog', 'aria-label': 'Budget for the new session', testid: 'draft-budget-pop' },
    h('div', { class: 'fl' }, 'Budget for the new session'),
    h('div', { class: 'presets' }, BUDGETS.map(v => h('button', {
      class: ['btn', v === (d.envelope || 0) && 'pri'], type: 'button', testid: `draft-budget-${v}`, onclick: () => apply(v)
    }, v ? `${v} cr` : 'uncapped'))),
    h('div', { class: 'row' }, input, h('button', { class: 'btn', type: 'button', onclick: () => apply(input.value) }, 'Set')),
    h('p', { class: 'fh' }, 'When it runs out the session stops and asks. Raise it from the session’s head and the same conversation carries on.'))
  openPop('budget', anchor, el, { alignLeft: true })
  input.focus()
  input.select()
}

// openModelPicker opens the picker from the card's menu: anchored to the
// model beside Send, which is where it lives.
export function openModelPicker () {
  const btn = document.querySelector('[data-testid=model-picker-btn]')
  if (btn) openPicker(btn)
}

async function openPicker (anchor) {
  if (!form) await loadForm(state.sessionDraft?.repo || '')
  const cat = form?.sessions || { agents: [], recent: [] }
  const cur = sessionPair() || {}
  const search = h('input', { class: 'inp', testid: 'model-search', placeholder: 'Search, or type any model id', 'aria-label': 'Search models', autocomplete: 'off' })
  const list = h('div', { class: 'mlist', role: 'listbox', 'aria-label': 'Models' })
  const foot = h('div', { class: 'mfoot-s' })
  const el = h('div', { class: 'mpick', role: 'dialog', 'aria-label': 'Choose a model', testid: 'model-picker' },
    h('div', { class: 'msearch' }, search), list, foot)

  const pick = (backend, model) => choose(backend, model)
  const rowEl = (backend, model, sub, testid) => h('button', {
    class: ['mrow', backend === cur.backend && model === cur.model && 'on'],
    type: 'button', role: 'option', testid,
    'aria-selected': String(backend === cur.backend && model === cur.model),
    onclick: () => pick(backend, model)
  }, h('span', { class: 'mono m' }, model || 'default model'), h('span', { class: 'sub' }, sub))

  const draw = () => {
    const q = search.value.trim()
    const ql = q.toLowerCase()
    const hit = (s) => !ql || String(s).toLowerCase().includes(ql)
    clear(list)
    const recent = (cat.recent || []).filter(p => hit(p.model) || hit(p.backend))
    if (recent.length) {
      append(list, [h('div', { class: 'mgh' }, 'Recent'), recent.map(p => rowEl(p.backend, p.model, p.backend, `model-recent-${p.backend}-${p.model}`))])
    }
    const missing = []
    const installed = []
    let exact = false
    for (const a of cat.agents || []) {
      if (!a.installed) { missing.push(a.name); continue }
      installed.push(a)
      const models = [...(a.needsModel ? [] : ['']), ...(a.models || [])]
      if (q && models.includes(q)) exact = true
      const shown = models.filter(m => hit(m) || hit(a.name))
      if (!shown.length) continue
      append(list, [h('div', { class: 'mgh' }, a.name, a.hint ? h('span', { class: 'hint' }, a.hint) : null),
        shown.map(m => rowEl(a.name, m, m ? '' : `${a.name} picks`, `model-${a.name}-${m || 'default'}`))])
    }
    // an id nobody suggested is still a model: offered once, on each agent
    // that could run it, below everything the workspace already uses
    const fits = installed.filter(a => { try { return !a.pattern || new RegExp(a.pattern, 'i').test(q) } catch { return true } })
    if (q && !exact && fits.length) {
      append(list, [h('div', { class: 'mgh' }, 'Use this id'),
        fits.map(a => rowEl(a.name, q, `on ${a.name}`, `model-typed-${a.name}`))])
    }
    if (!list.children.length) list.append(h('div', { class: 'mempty' }, 'No agent on this host can run a session.'))
    clear(foot)
    append(foot, [state.sessionDraft
      ? 'Any model an installed agent runs. Profiles do not apply to sessions.'
      : 'Switching keeps the conversation: the next turn runs on the new model with what was said so far.',
    missing.length ? h('span', { class: 'missing' }, ` Not installed here: ${missing.join(', ')}.`) : null])
  }
  search.addEventListener('input', draw)
  search.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      list.querySelector('.mrow')?.click()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      list.querySelector('.mrow')?.focus()
    }
  })
  list.addEventListener('keydown', (e) => {
    const rows = [...list.querySelectorAll('.mrow')]
    const i = rows.indexOf(document.activeElement)
    if (e.key === 'ArrowDown' && i >= 0) { e.preventDefault(); rows[Math.min(rows.length - 1, i + 1)].focus() }
    if (e.key === 'ArrowUp' && i >= 0) { e.preventDefault(); (i === 0 ? search : rows[i - 1]).focus() }
  })
  draw()
  openPop('model', anchor, el)
  search.focus()
}

// choose applies a pick: to the draft, or as the open session's "model"
// action, whose refusal (a turn in flight, an id the agent would refuse)
// is said where the picker was.
async function choose (backend, model) {
  closePop()
  if (state.sessionDraft) {
    set({ sessionDraft: { ...state.sessionDraft, backend, model } })
    return
  }
  const id = state.sel
  if (!id) return
  try {
    const c = await post(cardPath(id, 'actions/model'), { backend, model })
    if (c && state.sel === id) set({ card: c })
    toast(`${id} now runs on ${pairLabel({ backend, model })}`)
    ctx.refresh?.(id)
  } catch (err) {
    const e = err.data || {}
    toast(e.error === 'busy' ? 'The session is mid-turn — switch once this turn ends.' : (e.error || err.message), { err: true })
  }
}

// ---- write a spec ----

// writeSpecButton is the session head's way into the workflow: shown while
// the card's menu offers "writespec" (an open session nobody else drives).
export function writeSpecButton (card) {
  const a = card?.actions?.find(x => x.id === 'writespec')
  if (!a) return null
  return h('button', { class: 'btn hide-s', type: 'button', testid: 'write-spec', title: a.detail, onclick: () => openWriteSpec(card, a) }, 'Write a spec')
}

// openWriteSpec asks for the spec's title, profile and budget, then runs
// the session's "writespec" action and moves the page onto the new card.
export async function openWriteSpec (card, a) {
  if (!form) await loadForm(card.repo || '')
  const title = h('input', { value: a.default || card.title, testid: 'spec-title', autocomplete: 'off' })
  const profile = h('select', { testid: 'spec-profile' },
    (a.choices || []).map((c, i) => h('option', { value: c.value, selected: i === 0 }, c.detail ? `${c.label} — ${c.detail}` : c.label)))
  const budget = h('input', { type: 'number', min: '0', step: '10', value: String(form?.envelope || 0), testid: 'spec-budget' })
  const err = h('p', { class: 'spec-err', role: 'alert', testid: 'spec-error', hidden: true })
  const body = h('div', { class: 'mbody spec-body' },
    h('p', { class: 'spec-about' }, `${card.id} ends here and keeps its branch. A feature continues its work on a branch cut from it: the profile’s architect plans it from this conversation and what the branch already holds, and it lands only once its critique and checks pass.`),
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Title'), title),
    (a.choices || []).length ? h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Profile'), profile, h('span', { class: 'fh' }, 'Every stage takes its agent and model from the profile.')) : null,
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Budget'), budget, h('span', { class: 'fh' }, 'Credits for the whole spec, apart from what the session spent. 0 is uncapped.')),
    err)
  openModal({
    title: 'Write a spec',
    testid: 'write-spec-dialog',
    bodyEl: body,
    card: card.id,
    actions: [
      { label: 'Cancel' },
      {
        label: 'Start the spec',
        primary: true,
        testid: 'spec-start',
        onClick: async () => {
          err.hidden = true
          const req = { message: title.value.trim(), number: Math.max(0, parseInt(budget.value, 10) || 0) }
          if (profile.value) req.profile = profile.value
          if (state.card?.decision?.against?.token) req.against = state.card.decision.against.token
          try {
            const c = await post(cardPath(card.id, 'actions/writespec'), req)
            await ctx.refreshBoard?.()
            if (c?.id) await ctx.select(c.id)
            toast(`${card.id} continues as ${c?.id || 'a spec'}`)
          } catch (e) {
            err.textContent = e.data?.text || e.data?.error || e.message
            err.hidden = false
            return false
          }
        }
      }
    ]
  })
}
