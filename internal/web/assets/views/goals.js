// views/goals.js — goals (DESIGN §17): the list of every goal on the board,
// the form that mints one, and one goal's page — its state, its budget
// ledger, what "done" means and how far it got, the cards it is running,
// the decisions its lead made for review, its notebook and the lead's log,
// and the goal's own verbs. Everything shown is the server's: the ledger
// is engine.GoalReport's budget tree as it is, never recomputed here.

import { h, append, clear, cr, clock, plural, GLYPH, dollarsInput, parseDollars } from '../dom.js?v=__ASSET_V__'
import { registerView, openView } from '../views.js?v=__ASSET_V__'
import { markdown } from '../markdown.js?v=__ASSET_V__'

// ---- shared words ----

// a goal's one word (webapi.GoalSummary.State) and the tone it reads in
const TONE = {
  todo: 'mute',
  'agreeing the plan': 'plan',
  running: 'run',
  'wrapping up': 'warn',
  'ready for you': 'ok',
  done: 'done'
}
function stateBadge (state, testid) {
  return h('span', { class: ['gl-state', `t-${TONE[state] || 'mute'}`], testid }, state || 'unknown')
}

// a done-when item's mark (engine.DoneWhen* statuses)
function dwMark (status) {
  switch (status) {
    case 'met': return { g: '✓', cls: 'ok', word: 'met' }
    case 'not met': return { g: '✕', cls: 'err', word: 'not met' }
    case 'waiting on budget': case 'waiting on an environment': return { g: '◷', cls: 'warn', word: status }
    default: return { g: '?', cls: 'mute', word: status || 'not checked' }
  }
}

// a goal card's state (engine.GoalReportCard.State)
function cardGlyph (state) {
  switch (state) {
    case 'landed': return { g: '✓', cls: 'ok' }
    case 'running': case 'verified': return { g: '◐', cls: 'run' }
    case 'dropped': return { g: '⊘', cls: 'mute' }
    case 'stuck': case 'exhausted': case 'blocked': return { g: '!', cls: 'warn' }
    default: return { g: '○', cls: 'mute' }
  }
}

const num = (n) => cr(n)

function section (title, testid, ...kids) {
  return h('section', { class: 'gl-sec', testid }, h('h3', null, title), ...kids)
}

function debounce (fn, ms = 250) {
  let t = 0
  return () => { clearTimeout(t); t = setTimeout(fn, ms) }
}

function errText (err) {
  if (err?.notBuilt) return 'this board does not serve goals yet'
  return err?.message || String(err)
}

// ---- goals: the list ----

registerView('goals', {
  title: 'Goals',
  css: 'views/goals.css',
  mount (body, ctx) {
    body.classList.add('gl')
    let data = null
    let formOpen = !!ctx.params?.create
    const list = h('div', { class: 'gl-list', testid: 'goals-list' })
    const formSlot = h('div', { testid: 'goal-form-slot' })
    const newBtn = h('button', { class: 'btn pri', type: 'button', testid: 'goals-new', onclick: () => { formOpen = !formOpen; renderForm() } }, '+ New goal')
    const count = h('span', { class: 'gl-count', testid: 'goals-count' })
    body.append(h('div', { class: 'gl-bar' }, count, newBtn), formSlot, list)

    function renderForm () {
      clear(formSlot)
      newBtn.setAttribute('aria-expanded', String(formOpen))
      if (formOpen) formSlot.append(goalForm(ctx, data?.goals || [], () => { formOpen = false; renderForm() }))
    }

    function render () {
      clear(list)
      const goals = data?.goals || []
      count.textContent = plural(goals.length, 'goal')
      if (!goals.length) {
        list.append(h('div', { class: 'empty', testid: 'goals-empty' },
          h('b', null, 'No goals yet'),
          'A goal is an outcome and a budget: agree what done means, and gummi runs the cards it takes on one branch.'))
        return
      }
      for (const g of goals) list.append(goalRow(g, () => openView('goal', { id: g.id })))
    }

    async function load () {
      try {
        data = await ctx.api.get('/api/goals')
        render()
      } catch (err) {
        clear(list)
        list.append(h('div', { class: 'empty err', testid: 'goals-error' }, h('b', null, 'Goals did not load'), errText(err)))
      }
    }

    renderForm()
    load()
    const offBoard = ctx.onEvent('board', debounce(load))
    return () => offBoard()
  }
})

function goalRow (g, open) {
  const env = g.envelope || 0
  const pct = env ? Math.min(100, (g.spent / env) * 100) : 0
  return h('button', { class: ['gl-row', g.status === 'needs' && 'needs'], type: 'button', testid: `goal-row-${g.id}`, data: { state: g.state }, onclick: open },
    h('span', { class: 'gl-row-main' },
      h('span', { class: 'gl-row-top' },
        h('span', { class: 'gl-id' }, g.id),
        h('span', { class: 'gl-title' }, g.title),
        stateBadge(g.state)),
      h('span', { class: 'gl-row-meta' },
        h('span', { testid: 'goal-row-met' }, h('b', null, `${g.met || 0}/${g.doneWhen || 0}`), ' done-when met'),
        h('span', { testid: 'goal-row-landed' }, h('b', null, `${g.landed || 0}/${g.cards || 0}`), ' cards landed'),
        g.partial ? h('span', { class: 'gl-partial' }, `partial: ${g.partial}`) : null,
        g.status === 'needs' && g.needs?.question ? h('span', { class: 'gl-needs' }, g.needs.question) : null)),
    h('span', { class: 'gl-row-spend', testid: 'goal-row-spent', title: env ? `${cr(g.spent)} of a ${cr(env)} budget` : `${cr(g.spent)}, uncapped` },
      h('span', { class: 'mono' }, `${cr(g.spent)} / ${env ? num(env) : '∞'}`),
      h('span', { class: 'gl-mini' }, h('i', { style: { '--pct': pct + '%' } }))))
}

// ---- the create form ----

function goalForm (ctx, goals, done) {
  const desc = h('textarea', { id: 'goal-desc', testid: 'goal-form-desc', rows: 3, placeholder: 'The outcome, in a sentence or two: what is true when this goal is done.' })
  const budget = h('input', { id: 'goal-budget', testid: 'goal-form-budget', type: 'text', inputmode: 'decimal', placeholder: 'board default' })
  const profile = h('select', { id: 'goal-profile', testid: 'goal-form-profile' }, h('option', { value: '' }, 'the board’s default'))
  const after = h('select', { id: 'goal-after', testid: 'goal-form-after' },
    h('option', { value: '' }, 'nothing — a goal of its own'),
    goals.map(g => h('option', { value: g.id }, `${g.id} · ${g.title}`)))
  const refs = h('textarea', { id: 'goal-refs', testid: 'goal-form-refs', rows: 2, placeholder: 'docs/design.md\nspecs/table.md' })
  const auto = h('input', { id: 'goal-auto', testid: 'goal-form-autopilot', type: 'checkbox' })
  const err = h('div', { class: 'gl-err', testid: 'goal-form-error', role: 'alert', hidden: true })
  const submit = h('button', { class: 'btn pri', type: 'submit', testid: 'goal-form-submit' }, 'Create goal')

  // the form's choices, when the board serves them
  ctx.api.get('/api/form').then(f => {
    for (const p of f?.profiles || []) profile.append(h('option', { value: p }, p))
    if (f?.envelope) budget.placeholder = `board default (${cr(f.envelope)})`
  }).catch(() => {})

  const form = h('form', { class: 'gl-form', testid: 'goal-form' },
    h('label', { class: 'field wide' }, 'Objective', desc),
    h('div', { class: 'gl-form-row' },
      h('label', { class: 'field' }, 'Budget, dollars', budget),
      h('label', { class: 'field' }, 'Profile', profile),
      h('label', { class: 'field' }, 'Continues', after)),
    h('label', { class: 'field wide' }, h('span', null, 'References ', h('span', { class: 'gl-hint' }, '— paths in the workspace, one per line; pinned at the plan gate')), refs),
    h('label', { class: 'gl-check' }, auto, h('span', null, h('b', null, 'Start at once on autopilot'), h('span', { class: 'gl-hint' }, ' — the plan conversation begins now and its gate crosses by itself'))),
    err,
    h('div', { class: 'gl-form-foot' },
      h('button', { class: 'btn', type: 'button', testid: 'goal-form-cancel', onclick: done }, 'Cancel'),
      submit))

  form.addEventListener('input', () => { err.hidden = true })
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    err.hidden = true
    const description = desc.value.trim()
    if (!description) { err.textContent = 'Describe the objective — a goal is created from it.'; err.hidden = false; desc.focus(); return }
    const body = { description }
    if (budget.value.trim() !== '') {
      const b = parseDollars(budget.value)
      if (b.err || !b.credits) { err.textContent = b.err || 'A goal’s budget is more than $0.'; err.hidden = false; budget.focus(); return }
      body.envelope = b.credits
    }
    if (profile.value) body.profile = profile.value
    if (after.value) body.after = after.value
    const paths = refs.value.split(/[\n,]+/).map(s => s.trim()).filter(Boolean)
    if (paths.length) body.references = paths
    if (auto.checked) body.autopilot = true
    submit.disabled = true
    try {
      const res = await ctx.api.post('/api/goals', body)
      ctx.toast(res?.text || 'goal created')
      ctx.refreshBoard?.()
      if (res?.id) openView('goal', { id: res.id })
      else done()
    } catch (ex) {
      err.textContent = errText(ex)
      err.hidden = false
      submit.disabled = false
    }
  })
  setTimeout(() => desc.focus(), 0)
  return form
}

// ---- goal: one goal's page ----

registerView('goal', {
  title: 'Goal',
  css: 'views/goals.css',
  mount (body, ctx) {
    body.classList.add('gl', 'gl-page')
    const id = ctx.params?.id
    let g = null
    let busy = false // an action panel is open: hold refreshes until it closes
    let stale = false
    let cardIds = new Set()
    let gone = false

    async function load () {
      if (gone) return
      if (busy) { stale = true; return }
      try {
        g = await ctx.api.get(`/api/goals/${encodeURIComponent(id)}`)
        cardIds = new Set([...(g.cards || []).map(c => c.id), ...(g.report?.cards || []).map(c => c.id)])
        render()
      } catch (err) {
        clear(body)
        body.append(h('div', { class: 'empty err', testid: 'goal-error' }, h('b', null, `${id} did not load`), errText(err)))
      }
    }
    const reload = debounce(load)

    function setBusy (on) {
      busy = on
      if (!on && stale) { stale = false; load() }
    }

    function render () {
      const r = g.report || {}
      const scroll = body.scrollTop
      clear(body)
      append(body, [
        head(g, r, ctx),
        attention(g, r, ctx),
        actionBar(g, ctx, { setBusy, reload: load }),
        ledger(r.budget || {}),
        h('div', { class: 'gl-cols' },
          h('div', { class: 'gl-col' },
            doneWhen(r),
            cards(g, r, ctx),
            decisions(g, r, ctx, { setBusy, reload: load }),
            found(r)),
          h('div', { class: 'gl-col' },
            notebook(g.notebook || {}),
            tryIt(r),
            leadLog(g.log || [])))])
      body.scrollTop = scroll
    }

    load()
    const offs = [
      ctx.onEvent('board', reload),
      ctx.onEvent('card', (c) => {
        // the goal itself was deleted: there is nothing left to fetch
        if (c?.id === id && c.gone) { gone = true; clear(body); body.append(h('div', { class: 'empty', testid: 'goal-gone' }, h('b', null, `${id} was deleted`))); return }
        if (!gone && (c?.id === id || cardIds.has(c?.id))) reload()
      })
    ]
    return () => offs.forEach(off => off())
  }
})

function head (g, r, ctx) {
  const [met, total] = [(r.done_when || []).filter(d => d.status === 'met').length, (r.done_when || []).length]
  return h('div', { class: 'gl-head', testid: 'goal-head' },
    h('div', { class: 'gl-head-row' },
      h('button', { class: 'iconbtn gl-back', type: 'button', testid: 'goal-back', title: 'All goals', 'aria-label': 'All goals', onclick: () => openView('goals') }, '‹'),
      h('span', { class: 'kind' }, 'GL'),
      h('span', { class: 'gl-id', testid: 'goal-id' }, r.id || g.id),
      h('h3', { class: 'gl-name', testid: 'goal-title', title: r.title }, r.title),
      stateBadge(g.state, 'goal-state')),
    h('div', { class: 'gl-sub' },
      h('span', { testid: 'goal-met' }, h('b', null, `${met} of ${total}`), ' done-when met'),
      h('span', null, plural(r.lanes || 0, 'lane')),
      r.after ? h('span', null, 'continues ', h('button', { class: 'link', type: 'button', testid: 'goal-after', onclick: () => openView('goal', { id: r.after }) }, r.after)) : null,
      (r.repos || []).length === 1 && r.repos[0].branch ? h('span', { class: 'mono' }, r.repos[0].branch) : null,
      h('button', { class: 'link', type: 'button', testid: 'goal-open-card', onclick: () => { ctx.select(r.id || g.id); ctx.close() } }, 'open its card')),
    r.partial ? h('div', { class: 'gl-partial-line', testid: 'goal-partial' }, h('b', null, 'Partial'), ' — ', r.partial) : null)
}

// attention: what the goal is waiting on a person for, in its own words
function attention (g, r, ctx) {
  const lines = []
  if (r.stage === 'plan') {
    lines.push(h('div', { class: 'gl-callout t-plan', testid: 'goal-plan' },
      h('span', null, 'The plan is agreed on the goal’s own card and approved at its gate — the goal runs itself after that.'),
      h('button', { class: 'btn', type: 'button', testid: 'goal-plan-open', onclick: () => { ctx.select(r.id); ctx.close() } }, `Open ${r.id}`)))
  }
  const nb = r.needs_budget || {}
  if (nb.card) lines.push(h('div', { class: 'gl-callout t-warn', testid: 'goal-needs-budget' }, h('b', null, 'Waiting on budget'), ` — ${nb.card} needs ${nb.needs ? cr(nb.needs) : '?'}. ${nb.reason || ''}`))
  const ns = r.needs_substrate || {}
  if (ns.experiment) lines.push(h('div', { class: 'gl-callout t-warn', testid: 'goal-needs-substrate' }, h('b', null, 'Waiting on substrate'), ` — ${ns.experiment}: ${ns.reason || ''}`))
  const no = r.needs_owner || {}
  if (no.question) {
    lines.push(h('div', { class: 'gl-callout t-run', testid: 'goal-needs-owner' },
      h('b', null, `A question for you${no.item ? ` on ${no.item}` : ''}`), ' — ', no.question,
      no.proposal ? h('div', { class: 'gl-callout-sub' }, 'The lead proposes: ', no.proposal) : null,
      h('div', { class: 'gl-callout-sub' }, 'Anything you say to the goal answers it — a note, a send-back, a reversal.')))
  }
  if (r.waiting_on) lines.push(h('div', { class: 'gl-callout t-warn', testid: 'goal-waiting-on' }, h('b', null, 'Waiting on an environment'), ' — ', r.waiting_on))
  if ((r.unread_notes || []).length) {
    lines.push(h('div', { class: 'gl-callout t-mute', testid: 'goal-unread' }, h('b', null, `${plural(r.unread_notes.length, 'note')} not read`), ' — they reached the goal after its last lead turn: ', r.unread_notes.map(n => n.detail).join(' · ')))
  }
  return lines.length ? h('div', { class: 'gl-attn' }, lines) : null
}

// ---- the goal's verbs ----

function actionBar (g, ctx, hooks) {
  const acts = g.actions || []
  const slot = h('div', { class: 'gl-panel-slot', testid: 'goal-action-slot' })
  const bar = h('div', { class: 'gl-actions', testid: 'goal-actions' },
    acts.length ? null : h('span', { class: 'gl-hint' }, g.state === 'done' ? 'This goal is done.' : 'Nothing to do on this goal from here right now.'),
    acts.map(a => h('button', {
      class: ['btn', a.danger && 'danger', (a.id === 'land' || a.id === 'topup') && 'pri'],
      type: 'button',
      testid: `goal-action-${a.id}`,
      title: a.detail || a.label,
      'aria-expanded': 'false',
      onclick: (e) => openPanel(a, g, ctx, slot, hooks, {}, e.currentTarget)
    }, a.label)))
  return h('div', { class: 'gl-actwrap' }, bar, slot)
}

let openedPanel = null

// openPanel collects what a verb needs, inline where it was asked for, and
// runs it only on its own confirm button: the page's confirmation, not a
// browser dialog.
function openPanel (a, g, ctx, host, hooks, preset = {}, opener = null) {
  closePanel()
  const r = g.report || {}
  const fields = h('div', { class: 'gl-panel-fields' })
  const err = h('div', { class: 'gl-err', role: 'alert', testid: 'goal-action-error', hidden: true })
  let collect = () => ({})
  let focus = null

  switch (a.needs) {
    case 'message': {
      const ta = h('textarea', {
        testid: 'goal-action-input',
        rows: a.id === 'land' ? 5 : 3,
        value: a.default || '',
        placeholder: a.id === 'note' ? 'A line for the lead — it reads it on its next turn.' : a.id === 'sendback' ? 'What to change — the lead reads it and reworks its cards.' : ''
      })
      fields.append(h('label', { class: 'field' }, a.id === 'land' ? 'Merge message' : a.id === 'note' ? 'Note' : 'Notes for the lead', ta))
      focus = ta
      collect = () => {
        const text = ta.value.trim()
        if (!text && a.id !== 'land' && a.id !== 'sendback') return 'Say something first.'
        return { text }
      }
      break
    }
    case 'number': {
      const cur = Number(a.default) || r.budget?.envelope || 0
      const inp = h('input', { testid: 'goal-action-input', type: 'text', inputmode: 'decimal', value: cur ? dollarsInput(cur + Math.max(100, Math.round(cur * 0.25))) : '' })
      fields.append(h('label', { class: 'field' }, h('span', null, 'New budget, dollars ', h('span', { class: 'gl-hint' }, `— now ${num(cur)}; a goal’s budget is only ever raised`)), inp))
      focus = inp
      collect = () => {
        if (!inp.value.trim()) return 'Name the new budget.'
        const b = parseDollars(inp.value)
        if (b.err) return b.err
        if (b.credits <= cur) return `A goal’s budget is only raised — above ${num(cur)}.`
        return { envelope: b.credits }
      }
      break
    }
    case 'substrate': {
      const s = r.budget?.substrate || {}
      const runs = h('input', { testid: 'goal-action-runs', type: 'number', min: '0', inputmode: 'numeric', value: String(s.runs || '') })
      const mins = h('input', { testid: 'goal-action-minutes', type: 'number', min: '0', inputmode: 'numeric', value: String(s.minutes || '') })
      fields.append(h('div', { class: 'gl-form-row' }, h('label', { class: 'field' }, 'Runs', runs), h('label', { class: 'field' }, 'Minutes', mins)))
      focus = runs
      collect = () => {
        const out = {}
        if (runs.value !== '') out.runs = Math.round(Number(runs.value))
        if (mins.value !== '') out.minutes = Math.round(Number(mins.value))
        if (!out.runs && !out.minutes) return 'Name the runs or the minutes to raise the substrate budget to.'
        return out
      }
      break
    }
    case 'decision': {
      const ds = r.decisions || []
      if (!ds.length) {
        fields.append(h('p', { class: 'gl-hint' }, 'The lead has made no decisions for review on this goal yet.'))
        collect = () => 'There is no decision to reverse.'
        break
      }
      const sel = h('select', { testid: 'goal-action-ref' }, ds.map(d => h('option', { value: d.ref, selected: d.ref === preset.ref }, `${d.ref} · ${d.detail}`)))
      const why = h('textarea', { testid: 'goal-action-why', rows: 2, placeholder: 'Why the other way — the lead reads it.' })
      fields.append(h('label', { class: 'field' }, 'Decision', sel), h('label', { class: 'field' }, 'Why', why))
      focus = preset.ref ? why : sel
      collect = () => ({ ref: sel.value, why: why.value.trim() })
      break
    }
    default:
      break
  }

  const confirm = h('button', { class: ['btn', 'pri', a.danger && 'danger'], type: 'button', testid: 'goal-action-confirm' }, confirmLabel(a))
  const cancel = h('button', { class: 'btn', type: 'button', testid: 'goal-action-cancel', onclick: () => closePanel() }, 'Cancel')
  const panel = h('div', { class: ['gl-panel', a.danger && 'danger'], testid: `goal-panel-${a.id}`, role: 'group', 'aria-label': a.label },
    h('div', { class: 'gl-panel-head' }, h('b', null, cap(a.label)), a.detail ? h('span', null, a.detail) : null),
    fields, err,
    h('div', { class: 'gl-panel-foot' }, cancel, confirm))

  confirm.addEventListener('click', async () => {
    const body = collect()
    if (typeof body === 'string') { err.textContent = body; err.hidden = false; return }
    err.hidden = true
    confirm.disabled = true
    try {
      const res = await ctx.api.post(`/api/goals/${encodeURIComponent(g.report?.id || g.id)}/actions/${encodeURIComponent(a.id)}`, body)
      ctx.toast(res?.text || `${a.label}: done`)
      closePanel()
      hooks.reload()
    } catch (ex) {
      err.textContent = errText(ex)
      err.hidden = false
      confirm.disabled = false
    }
  })
  panel.addEventListener('input', () => { err.hidden = true })
  panel.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); closePanel() }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); confirm.click() }
  })

  host.append(panel)
  opener?.setAttribute('aria-expanded', 'true')
  hooks.setBusy(true)
  openedPanel = { panel, opener, hooks }
  ;(focus || confirm).focus()
}

function closePanel () {
  if (!openedPanel) return
  const { panel, opener, hooks } = openedPanel
  openedPanel = null
  panel.remove()
  opener?.setAttribute('aria-expanded', 'false')
  if (opener && document.contains(opener)) opener.focus()
  hooks.setBusy(false)
}

function confirmLabel (a) {
  switch (a.id) {
    case 'note': return 'Send note'
    case 'budget': return 'Raise budget'
    case 'substrate': return 'Raise substrate'
    case 'stop': return 'Stop the goal'
    case 'sendback': return 'Send back'
    case 'reverse': return 'Reverse it'
    case 'land': return 'Land on main'
    case 'abandon': return 'Abandon the goal'
    case 'topup': return 'Top up and carry on'
    default: return cap(a.label)
  }
}

function cap (s) { return s ? s[0].toUpperCase() + s.slice(1) : s }

// ---- the budget ledger (DESIGN §17.3) ----

function ledger (b) {
  const env = Number(b.envelope) || 0
  const own = Math.max(0, Number(b.goal_spend) || 0)
  const held = Math.max(0, Number(b.held_by_cards) || 0)
  const cardSpent = Math.max(0, Number(b.card_spend) || 0)
  const reserve = Math.max(0, Number(b.reserve) || 0)
  const left = Number(b.left_to_give) || 0
  const scale = Math.max(env, own + held + reserve + Math.max(0, left)) || 1
  const pct = (v) => `${(v / scale) * 100}%`
  const heldSpentPct = held ? Math.min(100, (cardSpent / held) * 100) : 0
  const seg = (k, v, title, inner) => v > 0
    ? h('span', { class: ['gl-seg', `k-${k}`], testid: `ledger-seg-${k}`, title, style: { '--w': pct(v) } }, inner)
    : null
  const item = (k, label, value, note, testid) => h('div', { class: ['gl-leg', `k-${k}`], testid },
    h('i', { 'aria-hidden': 'true' }),
    h('span', { class: 'gl-leg-l' }, label),
    h('span', { class: 'gl-leg-v mono' }, value),
    note ? h('span', { class: 'gl-leg-n' }, note) : null)
  const s = b.substrate
  return section('Budget', 'goal-ledger',
    h('div', { class: 'gl-ledger-top' },
      h('span', null, h('b', { class: 'mono', testid: 'ledger-envelope' }, num(env)), ' budget'),
      h('span', { class: 'gl-hint' }, h('span', { class: 'mono', testid: 'ledger-total' }, cr(b.total_spend)), ' spent in all')),
    h('div', { class: ['gl-bar-track', left < 0 && 'over'], role: 'img', 'aria-label': `goal spend ${cr(own)}, held by cards ${num(held)}, reserve ${num(reserve)}, left to give ${num(left)}, of ${num(env)}` },
      seg('own', own, `goal spend ${cr(own)}`),
      seg('held', held, `held by cards ${num(held)} (${cr(cardSpent)} spent)`, h('span', { class: 'gl-seg-spent', style: { '--w': heldSpentPct + '%' } })),
      seg('reserve', reserve, `reserve ${num(reserve)}`),
      seg('left', Math.max(0, left), `left to give ${num(left)}`)),
    h('div', { class: 'gl-legend' },
      item('own', 'goal spend', cr(own), 'its lead, review and verify', 'ledger-goal-spend'),
      item('held', 'held by cards', num(held), 'a live card’s envelope, a finished card’s spend', 'ledger-held'),
      item('spent', 'card spend', cr(cardSpent), 'spent of what they hold', 'ledger-card-spend'),
      item('reserve', 'reserve', num(reserve), 'for its own review and verify', 'ledger-reserve'),
      item('left', 'left to give', num(left), left < 0 ? 'short: waits for a raise' : 'for raises and new cards', 'ledger-left')),
    s ? h('div', { class: 'gl-substrate', testid: 'ledger-substrate' },
      h('b', null, 'Substrate'),
      h('span', null, h('span', { class: 'mono' }, `${s.runs_spent}/${s.runs || '∞'}`), ' runs'),
      h('span', null, h('span', { class: 'mono' }, `${Math.round(s.minutes_spent || 0)}/${s.minutes || '∞'}`), ' minutes'),
      h('span', { class: 'gl-hint' }, `${plural(s.reserve_runs || 0, 'run')} held back for its proof`)) : null)
}

// ---- done when ----

function doneWhen (r) {
  const items = r.done_when || []
  return section('Done when', 'goal-done-when',
    items.length
      ? h('ol', { class: 'gl-dw' }, items.map(d => {
        const m = dwMark(d.status)
        return h('li', { class: `t-${m.cls}`, testid: `done-when-${d.id}`, data: { status: d.status } },
          h('span', { class: 'gl-mark', title: m.word, 'aria-label': m.word }, m.g),
          h('span', { class: 'gl-dw-body' },
            h('span', null, h('span', { class: 'gl-id' }, d.id), ' ', d.says),
            h('span', { class: 'gl-dw-how' }, d.evidence ? `${m.word}: ${d.evidence}` : d.how)))
      }))
      : h('p', { class: 'gl-none' }, 'Nothing agreed yet — the plan conversation writes it.'))
}

// ---- the goal's cards ----

function cards (g, r, ctx) {
  const rows = new Map((g.cards || []).map(c => [c.id, c]))
  const rep = r.cards || []
  const list = rep.length ? rep : (g.cards || []).map(c => ({ id: c.id, title: c.title, stage: c.stage, spent: c.spend, envelope: c.envelope }))
  const open = (id) => { ctx.select(id); ctx.close() }
  return section(`Cards${list.length ? ` · ${list.length}` : ''}`, 'goal-cards',
    list.length
      ? h('div', { class: 'gl-cards' }, list.map(c => {
        const row = rows.get(c.id)
        const gl = cardGlyph(c.state)
        return h('button', { class: 'gl-card', type: 'button', testid: `goal-card-${c.id}`, data: { state: c.state || '' }, title: `Open ${c.id} on the board`, onclick: () => open(c.id) },
          h('span', { class: ['gl-mark', `t-${gl.cls}`], 'aria-hidden': 'true' }, gl.g),
          h('span', { class: 'gl-card-main' },
            h('span', { class: 'gl-card-top' },
              h('span', { class: 'gl-id' }, c.id),
              h('span', { class: 'gl-title' }, c.title),
              h('span', { class: `st-${c.stage}`, 'aria-hidden': 'true' }, h('span', { class: 'gl-stage' }, GLYPH[c.stage] || '·', ' ', c.stage))),
            h('span', { class: 'gl-card-meta' },
              c.state ? h('span', { class: `t-${gl.cls}` }, c.state) : null,
              row?.status === 'needs' && row.needs?.question ? h('span', { class: 'gl-needs' }, row.needs.question) : null,
              (c.serves || []).length ? h('span', null, 'serves ', c.serves.map(s => h('span', { class: 'gl-chip' }, s))) : null,
              c.repo && (r.repos || []).length > 1 ? h('span', { class: 'mono' }, c.repo) : null,
              c.commit ? h('span', { class: 'mono' }, `landed as ${c.commit.slice(0, 7)}`) : null),
            c.subject ? h('span', { class: 'gl-card-sub' }, c.subject) : null,
            c.reason ? h('span', { class: 'gl-card-sub' }, c.reason) : null),
          h('span', { class: 'gl-card-spend mono' }, `${cr(c.spent)}/${c.envelope ? cr(c.envelope) : '∞'}`))
      }))
      : h('p', { class: 'gl-none' }, 'None yet — crossing the plan gate mints them.'))
}

// ---- decisions for review, declined findings, found along the way ----

function decisions (g, r, ctx, hooks) {
  const ds = r.decisions || []
  if (!ds.length) return null
  const reverse = (g.actions || []).find(a => a.id === 'reverse')
  return section('Decisions for review', 'goal-decisions',
    h('ul', { class: 'gl-decs' }, ds.map(d => {
      const slot = h('div', { class: 'gl-panel-slot' })
      return h('li', { testid: `goal-decision-${d.ref}` },
        h('div', { class: 'gl-dec-row' },
          h('span', { class: 'gl-dec-body' },
            h('span', null, h('span', { class: 'gl-id' }, d.ref), ' ', d.detail),
            d.alternative ? h('span', { class: 'gl-dec-alt' }, 'not: ', d.alternative) : null,
            d.card || d.item ? h('span', { class: 'gl-dec-alt' }, [d.card, d.item].filter(Boolean).join(' · ')) : null),
          reverse ? h('button', { class: 'btn', type: 'button', testid: `goal-reverse-${d.ref}`, 'aria-expanded': 'false', onclick: (e) => openPanel(reverse, g, ctx, slot, hooks, { ref: d.ref }, e.currentTarget) }, 'Reverse…') : null),
        slot)
    })))
}

function found (r) {
  const declined = r.declined_findings || []
  const along = r.found_along_the_way || []
  return [
    declined.length
      ? section('Declined findings', 'goal-declined', h('ul', { class: 'gl-plain' }, declined.map(d => h('li', null, d.card ? h('span', { class: 'gl-id' }, d.card, ' ') : null, d.finding || '', d.detail ? h('span', { class: 'gl-dec-alt' }, d.detail) : null))))
      : null,
    along.length
      ? section('Found along the way', 'goal-found', h('ul', { class: 'gl-plain' }, along.map(d => h('li', null, d.card ? h('span', { class: 'gl-id' }, d.card, ' ') : null, d.detail))))
      : null
  ]
}

// ---- the notebook (§17.10) ----

function notebook (nb) {
  const refs = nb.references || []
  const fs = nb.findings || []
  return section('Notebook', 'goal-notebook',
    h('div', { class: 'gl-nb-h' }, 'References'),
    refs.length
      ? h('ul', { class: 'gl-refs' }, refs.map(ref => h('li', { testid: 'goal-reference' },
        h('span', { class: 'mono' }, ref.name),
        ref.changed ? h('span', { class: 'gl-flag t-warn' }, 'changed since the plan') : null,
        ref.missing ? h('span', { class: 'gl-flag t-err' }, 'missing') : null)))
      : h('p', { class: 'gl-none' }, 'No reference documents.'),
    h('div', { class: 'gl-nb-h' }, 'Findings'),
    fs.length
      ? h('ul', { class: 'gl-findings' }, fs.map(f => h('li', { testid: `goal-finding-${f.ref}` },
        h('span', null, h('span', { class: 'gl-id' }, f.ref), ' ', f.claim, f.status ? h('span', { class: 'gl-flag' }, f.status) : null),
        f.evidence || f.card ? h('span', { class: 'gl-dec-alt' }, [f.card, f.evidence].filter(Boolean).join(' · ')) : null)))
      : h('p', { class: 'gl-none' }, 'Nothing found yet.'))
}

function tryIt (r) {
  if (!r.try_it) return null
  // the goal doc's section is markdown, as the spec tab reads it
  return section('Try it', 'goal-try-it', h('div', { class: 'md gl-try' }, markdown(r.try_it)))
}

// ---- the lead's log, newest first ----

const LOG_SHOWN = 30

function leadLog (log) {
  if (!log.length) return section('Lead’s log', 'goal-log', h('p', { class: 'gl-none' }, 'Nothing yet.'))
  const newest = [...log].reverse()
  const list = h('ol', { class: 'gl-log' })
  const draw = (all) => {
    clear(list)
    for (const en of all ? newest : newest.slice(0, LOG_SHOWN)) {
      const first = en.action === 'checks' ? '' : String(en.detail || '').split('\n')[0]
      list.append(h('li', { testid: 'goal-log-entry', data: { action: en.action } },
        h('span', { class: 'gl-log-t mono' }, clock(en.at)),
        h('span', { class: 'gl-log-a' }, en.action),
        en.card ? h('span', { class: 'gl-id' }, en.card) : null,
        en.ref ? h('span', { class: 'gl-id' }, en.ref) : null,
        first ? h('span', { class: 'gl-log-d' }, first) : null,
        en.by ? h('span', { class: 'gl-log-by' }, en.by) : null))
    }
  }
  draw(false)
  const more = newest.length > LOG_SHOWN
    ? h('button', { class: 'link gl-more', type: 'button', testid: 'goal-log-more', onclick: (e) => { draw(true); e.currentTarget.remove() } }, `Show all ${newest.length}`)
    : null
  return section('Lead’s log', 'goal-log', list, more)
}

