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
// agents this host can run, the models each agent says it provides (the
// server asked them live) merged with the ids the workspace's profiles
// run, the pairs sessions on the board use, and any id typed in. On a
// session that exists, a pick is the card's "model" action. A card in the
// workflow shows no picker: its stages take their models from its profile.

import { $, h, clear, append, setVars, isMobile, cr, dollarsInput, parseDollars } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'

let ctx = {}
let form = null // webapi.Form: the draft's choices and the picker's catalog
let formLoad = null // the /api/form fetch in flight, for a send that cannot wait on nothing
let pop = null // the open popover: { el, close }

const BUDGETS = [500, 1500, 5000, 0]

export function initSession (c) {
  ctx = c
  // opening a card leaves the draft; what was typed stays in the composer
  // only while the draft is what it was typed into
  on(['sel'], () => { if (state.sel && state.sessionDraft) set({ sessionDraft: null }) })
  on(['sessionDraft', 'card', 'sel', 'conn'], render)
  render()
}

async function loadForm (repo = '') {
  const mine = (formLoad = (async () => {
    try {
      form = await get('/api/form' + (repo ? `?repo=${encodeURIComponent(repo)}` : ''))
    } catch (err) {
      toast(`The model list did not load: ${err.message}`, { err: true })
    }
    return form
  })())
  try { return await mine } finally { if (formLoad === mine) formLoad = null }
}

// budgetOf reads a budget typed by a person in dollars, 0 for uncapped,
// and answers it in credits. Anything else — empty, negative, finer than a
// cent — is refused with a sentence, never quietly read as 0 (which would
// be no cap at all).
export function budgetOf (raw) {
  if (String(raw ?? '').trim() === '') return { err: 'Say a budget in dollars — 0 is uncapped.' }
  const b = parseDollars(raw)
  return b.err ? { err: b.err } : { n: b.credits }
}

// budgetWord is how a draft says its budget: none picked yet and the
// board's default still on its way says so, rather than "uncapped".
function budgetWord (env) {
  if (env == null) return 'the board default'
  return env ? cr(env) : 'uncapped'
}

// newSession opens an empty draft in the conversation column. cameFrom
// is the card open before it, which Cancel goes back to.
let cameFrom = null
export async function newSession () {
  closePop()
  if (state.sel) cameFrom = state.sel
  // the draft opens at once, on what the page already knows; the form's
  // defaults fill it when they arrive, without touching what was typed
  const known = form
  // envelope null is "the board's default, not here yet": never 0, which
  // would start the session uncapped
  const draftOf = (f) => {
    const d = f?.sessions?.default || {}
    return { repo: f?.repos?.[0] || '', base: '', envelope: f ? (f.envelope || 0) : null, backend: d.backend || '', model: d.model || '', mainCheckout: false }
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
  if (d.envelope != null) out.envelope = d.envelope
  if (d.mainCheckout) out.mainCheckout = true
  return out
}

// startSession sends the draft's first message: the card is created with
// it, the page moves onto the card, and its first turn is already running.
// A refusal is thrown back to the composer, which keeps the line. atts
// are the ids of images already uploaded (the composer's paperclip) —
// they ride the description the card is seeded with, and its first turn
// carries them natively when the agent it starts on can take images.
export async function startSession (text, atts = []) {
  let d = state.sessionDraft
  if (d.envelope == null) {
    // sent before the form came: its default budget is what the session
    // starts on, so wait for it — and refuse rather than start uncapped
    await (formLoad || loadForm(d.repo || ''))
    d = state.sessionDraft || d
    if (d.envelope == null && form) d = { ...d, envelope: form.envelope || 0 }
    if (d.envelope == null) throw new Error('The board’s default budget did not load. Set a budget beside the composer, then send again.')
  }
  const req = { kind: 'freeform', description: text, backend: d.backend, model: d.model, envelope: d.envelope }
  if (d.repo) req.repo = d.repo
  if (d.mainCheckout) req.mainCheckout = true
  else if (d.base) req.base = d.base
  if (atts.length) req.attachments = atts
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
      h('span', null, d.repo || state.board?.repo || 'this repository', d.mainCheckout ? ' · the main checkout' : (d.base ? [' · from ', h('span', { class: 'mono' }, d.base)] : ' · a new worktree')),
      h('span', null, `budget ${budgetWord(d.envelope)}`))
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
  const back = state.board?.rows?.some(r => r.id === cameFrom) ? cameFrom : state.board?.rows?.[0]?.id
  if (back) ctx.select(back)
}

// draftTakesImages is whether the session this draft would start can
// take a turn's images: the agent its pair names, the form's default
// pair answering for one that has not picked (or has not loaded its
// form) — the backend-level half of the live answer a card's composer
// state reports once the session exists. The first message's attachments
// ride the description the card is seeded with, and an agent that cannot
// take them natively still gets each one by its path, so nothing here
// can be refused.
export function draftTakesImages () {
  const d = state.sessionDraft
  if (!d) return false
  const backend = d.backend || form?.sessions?.default?.backend || ''
  const a = (form?.sessions?.agents || []).find((x) => x.name === backend)
  return !!a?.images
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
  row.append(h('button', {
    class: 'dsel dbtn', type: 'button', testid: 'draft-main',
    title: d.mainCheckout ? 'The session works in the main checkout: no branch, no worktree, its changes left uncommitted' : 'The session works in its own branch worktree',
    // the base stays picked across the toggle: the main checkout only sets
    // it aside (startSession sends no base with it), and coming back finds it
    onclick: () => set({ sessionDraft: { ...state.sessionDraft, mainCheckout: !d.mainCheckout } })
  }, d.mainCheckout ? h('b', null, 'the main checkout') : 'own worktree'))
  if (!d.mainCheckout) {
    row.append(h('label', { class: 'dsel' }, h('span', { class: 'lbl' }, 'from'),
      h('select', {
        testid: 'draft-base', 'aria-label': 'Branch the session forks from',
        onchange: (e) => set({ sessionDraft: { ...state.sessionDraft, base: e.target.value } })
      }, h('option', { value: '', selected: !d.base }, 'the default branch'), branches.map(b => h('option', { value: b, selected: b === d.base }, b)))))
    row.append(h('span', { class: 'dnote-s' }, 'new worktree'))
  }
  const bud = h('button', {
    class: 'dsel dbtn', type: 'button', testid: 'draft-budget', 'aria-haspopup': 'dialog',
    onclick: () => (pop?.kind === 'budget' ? closePop() : openBudget(bud))
  }, 'budget ', h('b', null, budgetWord(d.envelope)))
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
  // the composer redraws its buttons on every card and connection change:
  // one replaced while the form loaded is gone from the page, and a
  // popover placed against it would hang off the top of the window
  if (!anchor.isConnected && anchor.dataset.testid) anchor = document.querySelector(`[data-testid="${anchor.dataset.testid}"]`)
  if (!anchor) return
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
  // added a tick later, so the click that opened it does not close it —
  // and only if it is still open then: a popover closed within that tick
  // would leave the listener behind, closing every later one on mousedown
  setTimeout(() => { if (pop?.el === el) document.addEventListener('mousedown', outside) }, 0)
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
  const input = h('input', { class: 'inp', type: 'text', inputmode: 'decimal', value: dollarsInput(d.envelope ?? form?.envelope ?? 0), testid: 'draft-budget-input', 'aria-label': 'Budget in dollars', 'aria-describedby': 'draft-budget-err' })
  const err = h('p', { class: 'ferr', id: 'draft-budget-err', testid: 'draft-budget-error', role: 'alert', hidden: true })
  // a preset is already credits; only what was typed is dollars to read
  const apply = (v) => {
    const b = typeof v === 'number' ? { n: v } : budgetOf(v)
    if (b.err) { err.textContent = b.err; err.hidden = false; input.focus(); return }
    set({ sessionDraft: { ...state.sessionDraft, envelope: b.n } })
    closePop()
  }
  input.addEventListener('input', () => { err.hidden = true })
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); apply(input.value) } })
  const el = h('div', { class: 'spop', role: 'dialog', 'aria-label': 'Budget for the new session', testid: 'draft-budget-pop' },
    h('div', { class: 'fl' }, 'Budget for the new session'),
    h('div', { class: 'presets' }, BUDGETS.map(v => h('button', {
      class: ['btn', v === d.envelope && 'pri'], type: 'button', testid: `draft-budget-${v}`, onclick: () => apply(v)
    }, v ? cr(v) : 'uncapped'))),
    h('div', { class: 'row' }, input, h('button', { class: 'btn', type: 'button', testid: 'draft-budget-set', onclick: () => apply(input.value) }, 'Set')),
    err,
    h('p', { class: 'fh' }, 'When it runs out the session stops and asks. Raise it from the session’s head and the same conversation carries on.'))
  openPop('budget', anchor, el, { alignLeft: true })
  input.focus()
  input.select()
}

// modelShownMax bounds how many of one agent's rows the picker draws
// before pointing at the rest: an agent that enumerates its catalog can
// offer hundreds of ids, and every one of them as a row is exactly what
// the search box was put there to prevent. The rows are the filtered
// list's head; the note names how many wait behind the next character
// typed.
const modelShownMax = 30

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
    type: 'button', role: 'option', testid, data: { model },
    'aria-selected': String(backend === cur.backend && model === cur.model),
    onclick: () => pick(backend, model)
  }, h('span', { class: 'mono m' }, model || 'default model'), h('span', { class: 'sub' }, sub))

  // enter takes what was typed at its word: the row that is exactly that
  // id, else the typed id itself — a suggestion that merely contains it
  // (claude-sonnet-4-6 for "sonnet-4") only once nothing else is left
  const enterRow = () => {
    const q = search.value.trim()
    const rows = [...list.querySelectorAll('.mrow')]
    if (!q) return rows[0]
    return rows.find(r => r.dataset.model === q) || list.querySelector('[data-testid^="model-typed-"]') || rows[0]
  }

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
      const cap = shown.slice(0, modelShownMax)
      append(list, [h('div', { class: 'mgh' }, a.name, a.hint ? h('span', { class: 'hint' }, a.hint) : null),
        cap.map(m => rowEl(a.name, m, m ? '' : `${a.name} picks`, `model-${a.name}-${m || 'default'}`)),
        shown.length > cap.length
          ? h('div', { class: 'mgh more' }, `+${shown.length - cap.length} more — search to narrow`)
          : null])
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
      ? 'Any model an installed agent runs — the ones it names itself, plus the ids this workspace has used. Profiles do not apply to sessions.'
      : 'Switching keeps the conversation: the next turn runs on the new model with what was said so far.',
    missing.length ? h('span', { class: 'missing' }, ` Not installed here: ${missing.join(', ')}.`) : null])
  }
  search.addEventListener('input', draw)
  search.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      enterRow()?.click()
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

// openWriteSpec opens the dialog at once and starts the handoff brief. On a
// live session the start IS the brief turn, gummi asking the session to
// write what the next card's architect will read: it runs in the
// background, the thread's busy marker names it, and the dialog reads it
// again when the card's update says it has landed — so leaving the dialog,
// or the page, loses nothing. The brief is an editable field filled with
// the draft, labeled by where it came from. Nothing mints before the brief
// has landed and the person confirms, and the start button stays off until
// it has.
export async function openWriteSpec (card, a) {
  if (!form) await loadForm(card.repo || '')
  const title = h('input', { value: card.title, testid: 'spec-title', autocomplete: 'off' })
  const brief = h('textarea', { rows: 10, testid: 'spec-brief', disabled: true })
  const note = h('span', { class: 'fh', testid: 'spec-brief-note' }, 'drafting the handoff brief…')
  const profile = h('select', { testid: 'spec-profile' },
    (a.choices || []).map((c, i) => h('option', { value: c.value, selected: i === 0 }, c.detail ? `${c.label} — ${c.detail}` : c.label)))
  const budget = h('input', { type: 'text', inputmode: 'decimal', value: form ? dollarsInput(form.envelope || 0) : '', testid: 'spec-budget' })
  const err = h('p', { class: 'spec-err', role: 'alert', testid: 'spec-error', hidden: true })
  const body = h('div', { class: 'mbody spec-body' },
    h('p', { class: 'spec-about' }, `${card.id} ends here and keeps its branch. A feature continues its work on a branch cut from it: the profile’s architect plans it from this conversation and what the branch already holds, and it lands only once its critique and checks pass.`),
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Title'), title),
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Brief'), brief, note),
    (a.choices || []).length ? h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Profile'), profile, h('span', { class: 'fh' }, 'Every stage takes its agent and model from the profile.')) : null,
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Budget'), budget, h('span', { class: 'fh' }, 'Dollars for the whole spec, apart from what the session spent. 0 is uncapped.')),
    err)
  const startAction = { label: 'Start the spec', primary: true, testid: 'spec-start', disabled: true, onClick: mint }
  // filled: the brief has landed in the field, which is when a mint may
  // read it. Set once and never cleared — a later read cannot unfill it.
  let filled = false
  let reading = false
  // apply takes the brief as the page last read it: drafting keeps the
  // dialog waiting, a brief fills the field, a failed read says why.
  const apply = (d) => {
    if (filled || !d || d.drafting) return
    filled = true
    // the draft's own label: the session's words, or what the conversation
    // alone could lay out — a degraded brief is never mistaken for the
    // session's own words
    brief.value = d.brief || ''
    brief.disabled = false
    if (startAction.el) startAction.el.disabled = false
    note.textContent = d.source === 'assembled'
      ? 'assembled from the conversation — no live session could answer it'
      : 'the session’s own words — edit what the next card’s architect will read'
  }
  const fail = (e) => { note.textContent = e.data?.text || e.data?.error || e.message }
  // read GETs the brief as it stands; it never starts a turn. An update that
  // arrives while a read is in flight sets again, and the read settles by
  // reading once more: the brief can land during the GET, and the update
  // that says so must not be the one dropped.
  let again = false
  const read = async () => {
    if (filled) return
    if (reading) { again = true; return }
    reading = true
    try {
      apply(await get(cardPath(card.id, 'writespec-draft')))
    } catch (e) {
      fail(e)
    } finally {
      reading = false
      if (again) { again = false; read() }
    }
  }
  async function mint () {
    err.hidden = true
    if (!filled) {
      // the brief is still drafting: nothing mints off an empty field
      err.textContent = 'still drafting the handoff brief…'
      err.hidden = false
      return false
    }
    const b = budgetOf(budget.value)
    if (b.err) { err.textContent = b.err; err.hidden = false; budget.focus(); return false }
    const req = { message: title.value.trim(), brief: brief.value, number: b.n }
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
  // the card's update is the signal the brief has landed: read it then. The
  // listener waits for the start to answer without error: a refused start
  // started no brief, so an update that arrives meanwhile must not read an
  // assembled draft into the dialog as though it were the session's own.
  let unwatch = () => {}
  let closed = false
  openModal({
    title: 'Write a spec',
    testid: 'write-spec-dialog',
    bodyEl: body,
    card: card.id,
    onClose: () => { closed = true; unwatch() },
    actions: [{ label: 'Cancel' }, startAction]
  })
  // the dialog is up: start the brief once, then show what it answered
  try {
    apply(await post(cardPath(card.id, 'writespec-draft')))
    if (!closed) {
      unwatch = on(['card'], () => { read() })
      // a brief that landed before the listener attached has no update
      // left to say so: one read covers it
      read()
    }
  } catch (e) {
    fail(e)
  }
}

// ---- delegation ----

// delegateButton opens the session's delegation: the credits it may give
// workflow cards it creates. Off until the person sets a budget — a session
// without one is not offered the card tools at all.
export function delegateButton (card) {
  if (!card?.session || card.stage === 'done') return null
  const d = card.delegation
  const label = d ? `Delegating · ${cr(Math.max(0, d.left))} left` : 'Delegate'
  return h('button', { class: ['btn', 'hide-s', d && 'on'], type: 'button', testid: 'delegate', title: 'Let this session create workflow cards under a budget', onclick: () => openDelegate(card) }, label)
}

export function openDelegate (card) {
  const d = card.delegation || { budget: 0, confirmAll: false }
  const budget = h('input', { type: 'text', inputmode: 'decimal', value: d.budget ? dollarsInput(d.budget) : '', testid: 'delegate-budget' })
  const all = h('input', { type: 'checkbox', testid: 'delegate-all' })
  all.checked = !!d.confirmAll
  const err = h('p', { class: 'spec-err', role: 'alert', testid: 'delegate-error', hidden: true })
  const body = h('div', { class: 'mbody spec-body' },
    h('p', { class: 'spec-about' }, 'The session may then create feature and bug cards that fork from this branch, run the whole workflow, and land back here when it asks. Each card is put to you first.'),
    h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Budget'), budget, h('span', { class: 'fh' }, 'Dollars across every card it creates. 0 turns delegation off.')),
    h('label', { class: 'field' }, all, h('span', null, ' Don’t ask me about each card')),
    err)
  openModal({
    title: 'Delegate to cards',
    testid: 'delegate-dialog',
    bodyEl: body,
    card: card.id,
    actions: [{ label: 'Cancel' }, {
      label: 'Save',
      primary: true,
      testid: 'delegate-save',
      onClick: async () => {
        const b = budget.value.trim() === '' ? { credits: 0 } : parseDollars(budget.value)
        if (b.err) { err.hidden = false; err.textContent = b.err; return false }
        try {
          await post(cardPath(card.id, 'delegation'), { budget: b.credits, confirm_all: all.checked })
          return true
        } catch (e) {
          err.hidden = false
          err.textContent = e.data?.error || e.message
          return false
        }
      }
    }]
  })
}
