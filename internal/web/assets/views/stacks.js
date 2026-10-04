// views/stacks.js — stacks (DESIGN §18): chains of cards in one repository
// whose branches fork from one another. A position is topology, never
// scheduling, so nothing here holds a card back; what the page shows is
// what each card forks from, which are sitting on commits that moved, which
// must land first, and — after a restack — the replays it made and the
// `git push --force-with-lease` lines they need. gummi never pushes.

import { h, clear, plural, GLYPH, kindTag } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'

// the last restack answer per stack, so a refresh does not drop the push
// lines a person has not copied yet
const results = new Map()
// automatic replays a person dismissed, by stack id and replay time
const dismissed = new Set()

function debounce (fn, ms = 250) {
  let t = 0
  return () => { clearTimeout(t); t = setTimeout(fn, ms) }
}

function errText (err) {
  if (err?.notBuilt) return 'this board does not serve stacks yet'
  return err?.message || String(err)
}

// stackable: the cards that could carry a fork, as the new-card form's
// stack row offers them (GET /api/form: not research, which has no branch,
// not a goal or a goal's card, which share the goal's branch, and not a
// session that is closed or works in the main checkout), less the ones
// already in a stack. Until the form's list arrives the board's rows
// stand in, by the same rule as far as a row tells it. The server has the
// last word.
let formStackable = null
function stackable (ctx) {
  const rows = ctx.state.board?.rows || []
  const inStack = new Set(rows.filter(r => r.stack).map(r => r.id))
  if (formStackable) {
    const byId = new Map(rows.map(r => [r.id, r]))
    return formStackable.filter(c => !inStack.has(c.id)).map(c => ({ ...byId.get(c.id), ...c }))
  }
  return rows.filter(r =>
    !['RS', 'GL'].includes(kindTag(r)) && r.kind !== 'research' && r.kind !== 'goal' &&
    !r.goal && !r.stack && !r.landed && !(r.kind === 'freeform' && r.stage === 'done'))
}

registerView('stacks', {
  title: 'Stacks',
  css: 'views/stacks.css',
  mount (body, ctx) {
    body.classList.add('sk')
    ctx.api.get('/api/form').then(f => { formStackable = f?.stackable || null }).catch(() => {})
    let data = null
    let focus = ctx.params?.id || null
    let formOpen = false
    let holds = 0 // inline forms open: refreshes wait for them
    let stale = false
    let scrolledTo = null

    const list = h('div', { class: 'sk-list', testid: 'stacks-list' })
    const formSlot = h('div')
    const count = h('span', { class: 'sk-count', testid: 'stacks-count' })
    const newBtn = h('button', { class: 'btn pri', type: 'button', testid: 'stacks-new', onclick: () => { formOpen = !formOpen; renderForm() } }, '+ New stack')
    body.append(
      h('div', { class: 'sk-bar' }, count, newBtn),
      h('p', { class: 'sk-lede' }, 'Each card forks from the one listed before it; the first forks from its base. A position never holds work back — it only orders landing.'),
      formSlot, list)

    const hooks = {
      hold () { holds++ },
      release () { holds = Math.max(0, holds - 1); if (!holds && stale) { stale = false; load() } },
      // force redraws even when the server's answer is unchanged: a restack
      // that found nothing to move (a conflict, a wait) leaves the data as
      // it was, yet its button and result panel still have to be redrawn
      reload: (force) => { if (force) drawn = ''; return load() },
      focus (id) { focus = id }
    }

    function renderForm () {
      clear(formSlot)
      newBtn.setAttribute('aria-expanded', String(formOpen))
      if (!formOpen) return
      formSlot.append(newStackForm(ctx, {
        done: () => { formOpen = false; renderForm() },
        made: (id) => { formOpen = false; renderForm(); focus = id; load() }
      }))
    }

    function render () {
      clear(list)
      const stacks = data?.stacks || []
      count.textContent = plural(stacks.length, 'stack')
      if (!stacks.length) {
        list.append(h('div', { class: 'empty', testid: 'stacks-empty' },
          h('b', null, 'No stacks yet'),
          'Slice one piece of work into several reviewable branches: start a stack from the card at its bottom, then put cards on top of it.'))
        return
      }
      for (const st of stacks) list.append(stackBox(st, ctx, hooks, st.id === focus))
      // bring a newly focused stack into view once, not on every refresh:
      // a page that scrolls itself moves the button under the pointer
      if (focus && focus !== scrolledTo) {
        scrolledTo = focus
        const el = list.querySelector('.sk-box.focus')
        el?.scrollIntoView?.({ block: 'nearest' })
      }
    }

    // what the list was last drawn from, so a refresh that brings nothing
    // new leaves the page — and whatever a person is reaching for — alone
    let drawn = ''
    async function load () {
      if (holds) { stale = true; return }
      try {
        const next = await ctx.api.get('/api/stacks')
        // a form may have opened while the list was on its way: drawing now
        // would take it from under the person, so wait for them instead
        if (holds) { data = next; stale = true; return }
        // the rows draw the members' board state too (stage, needs you)
        const rows = (ctx.state.board?.rows || []).map(r => [r.id, r.stage, r.status, r.needs?.kind])
        const key = JSON.stringify([next, rows])
        data = next
        if (key === drawn && list.childElementCount) return
        drawn = key
        render()
      } catch (err) {
        clear(list)
        list.append(h('div', { class: 'empty err', testid: 'stacks-error' }, h('b', null, 'Stacks did not load'), errText(err)))
      }
    }

    renderForm()
    load()
    const off = ctx.onEvent('board', debounce(load))
    return () => off()
  }
})

// ---- a new stack, from the card at its bottom ----

function newStackForm (ctx, { done, made }) {
  const cands = stackable(ctx)
  const pre = cands.some(r => r.id === ctx.state.sel) ? ctx.state.sel : ''
  const card = h('select', { testid: 'stack-form-card', id: 'stack-form-card' },
    h('option', { value: '' }, cands.length ? 'Choose the bottom card…' : 'No card can start a stack'),
    cands.map(r => h('option', { value: r.id, selected: r.id === pre }, `${r.id} · ${r.title}`)))
  const name = h('input', { testid: 'stack-form-name', id: 'stack-form-name', placeholder: 'named after the bottom card' })
  const err = h('div', { class: 'sk-err', role: 'alert', testid: 'stack-form-error', hidden: true })
  const submit = h('button', { class: 'btn pri', type: 'submit', testid: 'stack-form-submit' }, 'Start stack')
  const form = h('form', { class: 'sk-form', testid: 'stack-form' },
    h('div', { class: 'sk-form-row' },
      h('label', { class: 'field grow' }, 'Bottom card', card),
      h('label', { class: 'field' }, 'Name', name)),
    err,
    h('div', { class: 'sk-form-foot' },
      h('button', { class: 'btn', type: 'button', testid: 'stack-form-cancel', onclick: done }, 'Cancel'),
      submit))
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    if (!card.value) { err.textContent = 'Choose the card at the bottom of the stack.'; err.hidden = false; return }
    submit.disabled = true
    try {
      const res = await ctx.api.post('/api/stacks', { card: card.value, name: name.value.trim() || undefined })
      ctx.toast(res?.text || 'stack created')
      made(res?.id)
    } catch (ex) {
      err.textContent = errText(ex)
      err.hidden = false
      submit.disabled = false
    }
  })
  setTimeout(() => card.focus(), 0)
  return form
}

// ---- one stack ----

function stackBox (st, ctx, hooks, focused) {
  const members = [...(st.members || [])].sort((a, b) => a.pos - b.pos)
  const path = `/api/stacks/${encodeURIComponent(st.id)}`
  const slot = h('div', { class: 'sk-slot' })
  const rows = (ctx.state.board?.rows || [])
  const rowOf = (id) => rows.find(r => r.id === id)

  // an inline form under the header; one at a time per stack
  let formEl = null
  const closeForm = () => { if (formEl) { formEl.remove(); formEl = null; hooks.release() } }
  const openForm = (el) => { closeForm(); formEl = el; slot.prepend(el); hooks.hold(); el.querySelector('input,select,button.pri')?.focus() }

  const write = async (fn, after) => {
    try {
      const res = await fn()
      if (res?.text) ctx.toast(res.text)
      after?.(res)
      closeForm()
      ctx.refreshBoard?.()
      hooks.focus(st.id)
      hooks.reload()
    } catch (ex) {
      ctx.toast(errText(ex), { err: true })
      return ex
    }
  }

  const restack = async (btn) => {
    btn.disabled = true
    btn.classList.add('busy')
    try {
      const res = await ctx.api.post(`${path}/restack`, {})
      results.set(st.id, res)
      ctx.toast(res.conflict ? `${res.conflict.card} hit conflicts` : res.replayed?.length ? `replayed ${res.replayed.join(', ')}` : res.waiting ? `waiting: ${res.waiting}` : 'nothing to replay')
      ctx.refreshBoard?.()
      hooks.focus(st.id)
      hooks.reload(true)
    } catch (ex) {
      ctx.toast(errText(ex), { err: true })
      btn.disabled = false
      btn.classList.remove('busy')
    }
  }

  const addForm = () => {
    const inStack = new Set(members.map(m => m.id))
    const cands = stackable(ctx).filter(r => !inStack.has(r.id))
    const card = h('select', { testid: 'stack-add-card' },
      h('option', { value: '' }, cands.length ? 'Choose a card…' : 'No card can join a stack'),
      cands.map(r => h('option', { value: r.id }, `${r.id} · ${r.title}`)))
    const pos = h('select', { testid: 'stack-add-pos' },
      h('option', { value: '' }, 'on top'),
      members.slice(0, -1).map(m => h('option', { value: String(m.pos + 1) }, `above ${m.id}`)),
      members.length ? h('option', { value: '0' }, 'at the bottom') : null)
    const f = h('form', { class: 'sk-inline', testid: 'stack-add-form' },
      h('label', { class: 'field grow' }, 'Card', card),
      h('label', { class: 'field' }, 'Where', pos),
      h('div', { class: 'sk-inline-foot' },
        h('button', { class: 'btn', type: 'button', onclick: closeForm }, 'Cancel'),
        h('button', { class: 'btn pri', type: 'submit', testid: 'stack-add-submit' }, 'Add to stack')))
    f.addEventListener('submit', (e) => {
      e.preventDefault()
      if (!card.value) { card.focus(); return }
      const body = { card: card.value }
      if (pos.value !== '') body.pos = Number(pos.value)
      write(() => ctx.api.post(`${path}/cards`, body))
    })
    openForm(f)
  }

  const renameForm = () => {
    const inp = h('input', { testid: 'stack-rename-input', value: st.name || st.id })
    const f = h('form', { class: 'sk-inline', testid: 'stack-rename-form' },
      h('label', { class: 'field grow' }, 'Name', inp),
      h('div', { class: 'sk-inline-foot' },
        h('button', { class: 'btn', type: 'button', onclick: closeForm }, 'Cancel'),
        h('button', { class: 'btn pri', type: 'submit', testid: 'stack-rename-save' }, 'Rename')))
    f.addEventListener('submit', (e) => {
      e.preventDefault()
      if (!inp.value.trim()) return
      write(() => ctx.api.post(`${path}/rename`, { name: inp.value.trim() }))
    })
    openForm(f)
    inp.select()
  }

  const confirmRow = (text, label, testid, run) => {
    const f = h('div', { class: 'sk-inline sk-confirm', testid: `${testid}-dialog`, role: 'group' },
      h('span', { class: 'grow' }, text),
      h('div', { class: 'sk-inline-foot' },
        h('button', { class: 'btn', type: 'button', testid: `${testid}-cancel`, onclick: closeForm }, 'Cancel'),
        h('button', { class: 'btn pri danger', type: 'button', testid: `${testid}-confirm`, onclick: run }, label)))
    f.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); closeForm() } })
    openForm(f)
  }

  const move = (m, to) => write(() => ctx.api.post(`${path}/move`, { card: m.id, pos: to }))
  const remove = (m) => confirmRow(
    [`Take ${m.id} out of the stack?`, h('span', { class: 'sk-sub' }, members.some(x => x.pos > m.pos) ? (members.filter(x => x.pos > m.pos).length === 1 ? ' The card above it is replayed onto its new base.' : ' The cards above it are replayed onto their new base.') : (members.some(x => x.pos < m.pos) ? ' Its branch and work stay; it just stops forking from the card below.' : ' Its branch and work stay; it just leaves the stack.'))],
    'Remove', 'stack-remove',
    () => write(() => ctx.api.del(`${path}/cards/${encodeURIComponent(m.id)}`)))
  const del = () => confirmRow(`Delete the empty stack ${st.name || st.id}?`, 'Delete', 'stack-delete', () => write(() => ctx.api.del(path)))

  const restackBtn = h('button', { class: 'btn', type: 'button', testid: 'stack-restack', title: 'Replay every card onto its current base now', onclick: (e) => restack(e.currentTarget) }, 'Restack')
  const header = h('div', { class: 'sk-head' },
    h('div', { class: 'sk-title' },
      h('b', { testid: 'stack-name' }, st.name || st.id),
      st.name && st.name !== st.id ? h('span', { class: 'sk-id' }, st.id) : null,
      h('span', { class: 'sk-meta' }, plural(members.length, 'card'), st.repo ? ` · ${st.repo}` : '')),
    h('div', { class: 'sk-actions' },
      restackBtn,
      h('button', { class: 'btn', type: 'button', testid: 'stack-add', onclick: addForm }, '+ Add card'),
      h('button', { class: 'btn', type: 'button', testid: 'stack-rename', onclick: renameForm }, 'Rename'),
      members.length ? null : h('button', { class: 'btn danger', type: 'button', testid: 'stack-delete', onclick: del }, 'Delete')))

  const base = st.base || ''
  const chain = h('ol', { class: 'sk-chain', testid: 'stack-chain' },
    h('li', { class: 'sk-base', testid: 'stack-base' },
      h('span', { class: 'sk-node', 'aria-hidden': 'true' }),
      h('span', null, 'forks from ', base ? h('span', { class: 'mono' }, base) : 'the branch the repository has checked out')),
    members.map((m, i) => memberRow(m, i, members, rowOf(m.id), {
      open: () => { ctx.select(m.id); ctx.close() },
      // a closed card (landed, handed off) keeps its place
      up: i > 0 && !isClosed(m) ? () => move(m, members[i - 1].pos) : null,
      down: i < members.length - 1 && !isClosed(m) ? () => move(m, members[i + 1].pos) : null,
      remove: () => remove(m)
    })))

  return h('section', { class: ['sk-box', focused && 'focus'], testid: `stack-${st.id}`, 'aria-label': `stack ${st.name || st.id}` },
    header, slot, chain, resultPanel(shownResult(st), ctx, () => {
      if (st.replayedAt) dismissed.add(`${st.id}@${st.replayedAt}`)
      results.delete(st.id)
      hooks.reload()
    }))
}

function memberRow (m, i, members, row, act) {
  const marks = []
  if (m.handedOff) marks.push(h('span', { class: 'sk-mark t-mute', testid: 'marker-handedoff', title: 'Closed without landing; its branch is kept for you to push' }, 'handed off'))
  if (m.landed) marks.push(h('span', { class: 'sk-mark t-ok', testid: 'marker-landed' }, 'landed'))
  if (m.stale) marks.push(h('span', { class: 'sk-mark t-warn', testid: 'marker-stale', title: 'It sits on commits that have since moved; the next tick replays it' }, 'stale'))
  if (m.running) marks.push(h('span', { class: 'sk-mark t-run', testid: 'marker-running', title: 'A session is working on it; a replay waits' }, 'running'))
  if (m.dirty) marks.push(h('span', { class: 'sk-mark t-warn', testid: 'marker-dirty', title: 'Its worktree has uncommitted changes; a replay waits' }, 'uncommitted changes'))
  if (!m.tree && !m.landed && !m.handedOff) {
    // an adopted branch exists already; only its worktree waits for the start
    marks.push(m.adopted
      ? h('span', { class: 'sk-mark t-mute', testid: 'marker-notree', title: 'Its adopted branch exists; the worktree on it is made when the card starts' }, 'no worktree yet')
      : h('span', { class: 'sk-mark t-mute', testid: 'marker-notree', title: 'Its branch is cut when it starts' }, 'no branch yet'))
  }
  if (row?.status === 'needs') marks.push(h('span', { class: 'sk-mark t-run', testid: 'marker-needs' }, 'needs you'))
  const below = m.below || ''
  return h('li', {
    class: ['sk-member', `st-${m.stage}`, m.landed && 'landed'],
    testid: `stack-member-${m.id}`,
    data: { pos: String(m.pos), stale: m.stale ? '1' : null, landed: m.landed ? '1' : null, running: m.running ? '1' : null, dirty: m.dirty ? '1' : null }
  },
  h('span', { class: 'sk-node', 'aria-hidden': 'true' }, String(m.pos + 1)),
  h('div', { class: 'sk-body' },
    h('button', { class: 'sk-open', type: 'button', testid: `stack-open-${m.id}`, title: `Open ${m.id}`, onclick: act.open },
      h('span', { class: 'sk-glyph', 'aria-hidden': 'true' }, GLYPH[m.stage] || '·'),
      h('span', { class: 'sk-id' }, m.id),
      h('span', { class: 'sk-t' }, m.title || m.id)),
    h('div', { class: 'sk-line' },
      m.branch ? h('span', { class: 'mono sk-branch', testid: 'member-branch' }, m.branch) : null,
      h('span', { class: 'sk-stage' }, m.stage),
      below ? h('span', null, 'on ', h('span', { class: 'mono' }, below)) : null,
      m.blocker ? h('span', { class: 'sk-blocker', testid: 'member-blocker', title: 'Its branch holds the commits of every card beneath it, so it lands after them' }, `lands after ${m.blocker}`) : null,
      marks)),
  h('div', { class: 'sk-ctl' },
    h('button', { class: 'iconbtn', type: 'button', testid: `stack-up-${m.id}`, disabled: !act.up, title: 'Move toward the base', 'aria-label': `Move ${m.id} toward the base`, onclick: act.up }, '↑'),
    h('button', { class: 'iconbtn', type: 'button', testid: `stack-down-${m.id}`, disabled: !act.down, title: 'Move away from the base', 'aria-label': `Move ${m.id} away from the base`, onclick: act.down }, '↓'),
    h('button', { class: 'iconbtn sk-rm', type: 'button', testid: `stack-remove-${m.id}`, title: 'Take out of the stack', 'aria-label': `Take ${m.id} out of the stack`, onclick: act.remove }, '×')))
}

// isClosed is a card whose work has left gummi: it holds its place in the chain.
const isClosed = (m) => !!m.landed || !!m.handedOff || m.stage === 'done'

// ---- what a restack did ----

// shownResult picks what the stack's result panel shows: this page's last
// restack answer, unless it found nothing to do while a replay walk — the
// board's own, most often, which got there first — still has push lines
// to show; then that walk.
function shownResult (st) {
  // a replay of cards that have all left the stack since is nothing to
  // show any more: its cards are no longer here to push
  if (!(st.members || []).length) return null
  const members = new Set(st.members.map(m => m.id))
  let r = results.get(st.id)
  if (r && (r.replayed || []).length && !r.replayed.some(id => members.has(id))) r = null
  const idle = r && !(r.replayed || []).length && !r.conflict && !r.waiting
  return (idle && lastReplay(st)) || r || lastReplay(st)
}

// lastReplay is the stack's latest replay walk as the server keeps it —
// the board's own automatic replays included — shaped like a restack
// answer, so a page opened after the replay still shows its push lines.
function lastReplay (st) {
  if (!(st.push || []).length || dismissed.has(`${st.id}@${st.replayedAt}`)) return null
  const members = new Set((st.members || []).map(m => m.id))
  if (!(st.replayed || []).some(id => members.has(id))) return null
  return { replayed: st.replayed || [], stack: st, auto: true, at: st.replayedAt }
}

function resultPanel (res, ctx, dismiss) {
  if (!res) return null
  const replayed = res.replayed || []
  const push = res.stack?.push || []
  return h('div', { class: 'sk-result', testid: 'stack-result' },
    h('div', { class: 'sk-result-head' },
      h('b', null, res.auto ? `Replayed ${res.at ? new Date(res.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }) : ''}`.trim() : 'Restack'),
      h('button', { class: 'iconbtn', type: 'button', 'aria-label': 'Dismiss', title: 'Dismiss', testid: 'stack-result-close', onclick: dismiss }, '×')),
    res.conflict
      ? h('div', { class: 'sk-note t-err', testid: 'stack-conflict' },
        h('b', null, `${res.conflict.card} hit conflicts`), ' — its branch is untouched and the cards below it are already correct. Resolve them on the card to carry on.',
        (res.conflict.files || []).length ? h('ul', { class: 'sk-files' }, res.conflict.files.map(f => h('li', { class: 'mono' }, f))) : null)
      : null,
    res.waiting ? h('div', { class: 'sk-note t-warn', testid: 'stack-waiting' }, h('b', null, 'Waiting'), ' — ', res.waiting) : null,
    replayed.length
      ? h('div', { class: 'sk-note t-ok', testid: 'stack-replayed' }, h('b', null, 'Replayed'), ' ', replayed.join(', '), replayed.length === 1 ? ' onto its current base.' : ' onto their current base.')
      : (!res.conflict && !res.waiting ? h('div', { class: 'sk-note t-mute', testid: 'stack-replayed' }, 'Nothing to replay — every branch already sits on the one below it.') : null),
    push.length
      ? h('div', { class: 'sk-push', testid: 'stack-push' },
        h('span', { class: 'sk-sub' }, 'gummi never pushes. These branches moved; push them yourself:'),
        push.map(line => pushLine(line, ctx)),
        push.length > 1 ? h('button', { class: 'link sk-copyall', type: 'button', testid: 'stack-push-copy-all', onclick: (e) => copy(push.join('\n'), e.currentTarget.parentElement, ctx) }, 'Copy all') : null)
      : null)
}

function pushLine (line, ctx) {
  const code = h('code', { class: 'sk-cmd', testid: 'stack-push-line', tabindex: '0' }, line)
  return h('div', { class: 'sk-cmdrow' }, code,
    h('button', { class: 'btn sk-copy', type: 'button', testid: 'stack-push-copy', onclick: () => copy(line, code, ctx) }, 'Copy'))
}

// copy puts text on the clipboard; where the page may not (an insecure
// origin, a refused permission), it selects the text for ⌘C/Ctrl+C instead.
async function copy (text, el, ctx) {
  try {
    if (!navigator.clipboard?.writeText) throw new Error('no clipboard')
    await navigator.clipboard.writeText(text)
    ctx.toast('copied')
  } catch {
    try {
      const range = document.createRange()
      range.selectNodeContents(el)
      const sel = window.getSelection()
      sel.removeAllRanges()
      sel.addRange(range)
      ctx.toast('selected — press Ctrl+C to copy')
    } catch { ctx.toast('copy it by hand', { err: true }) }
  }
}
