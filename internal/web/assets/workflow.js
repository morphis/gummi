// workflow.js — a session's workflow runs on the page (DESIGN §25). Three
// things live here, each drawn from what the server folded, never decided
// here:
//
// The run's block (§25.9): one per run, inline in the thread where the run
// was proposed (the server places it: among the settled items, or among
// the live conversation's turns), built from the thread's run item
// (threadfold) and redrawn only when its Rev moves. An ended run no longer
// the latest news is folded by the server to one line, which a click
// unfolds whole and the block's ▾ folds again: the person's own, kept per
// card and run until they leave the card. Its head says the recipe, the spec, the spend
// (a run estimates nothing) and the elapsed time, with Pause, Resume and Stop;
// a strip draws the recipe's top-level entries; a row per step that has
// run says what it did, with Open; a step's question is answered under
// its row; the footer lists the files changed and offers Revert run…, and
// Save as recipe… for a recipe the session wrote inline.
//
// The step panel (§25.10): Open shows the step beside the thread, on the
// surface the documents use (a phone's full screen). Its Transcript is the
// step card's own conversation, live; Diff its own diff; Brief the prompt
// as filled in and the artifacts it reads and writes. A step has no
// terminal of its own: it works in the session's tree, whose terminal is
// the session's. A message typed under it goes to the step's agent.
//
// The proposal's card (§25.5): the pinned decision that asks to allow a
// run draws its step tree, the checks it will run and what it costs
// against the session's pool, with a box for feedback.
//
// The live strip (§25.9): while a run is open, one line directly above the
// composer says what it is doing — the step at work and its action, a
// step's question, a spent pool, a pause — with Jump to the run's block
// and what the moment offers (Open, Answer, Top up…, Resume). Its words
// are the run item's Live, folded on the server; the strip is updated in
// place, part by part, and never redrawn whole. A pill at the top of the
// thread says the open run's block is above while it is scrolled out of
// view (an IntersectionObserver), and jumps to it too.

import { $, h, clear, cr, dur, plural, isMobile } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'
import { onEvent } from './events.js?v=__ASSET_V__'
import { hl } from './diff.js?v=__ASSET_V__'
import { runAction } from './actions.js?v=__ASSET_V__'

let ctx = {}

export function initWorkflow (c) {
  ctx = c
  // a step is opened on its session: another card closes it, and the
  // person's folds of its runs are forgotten
  on(['sel'], () => {
    runFolds.clear()
    stepsOpen.clear()
    replies.clear()
    inner.clear()
    picks.clear()
    focusedReply = null
    // what a person typed to a step is kept across cards, but not without end
    while (drafts.size > 50) drafts.delete(drafts.keys().next().value)
    if (state.step) set({ step: null })
  })
  // the step's own conversation moves with its card's events, which name
  // the step's card, not the session
  onEvent('live', (e) => { if (state.step && e.id === state.step) loadTranscript() })
  onEvent('card', (e) => { if (state.step && e.id === state.step) { loadTranscript(); stepDiff = null } })
  on(['step'], () => { stepDiff = null; transcript = null; loadTranscript() })
  // the step's transcript follows its newest line while the reader is at
  // it, as the thread does: scrolling up lets go, coming back down takes
  // hold again. A scroll the page made redrawing the pane is not the
  // reader's (stickStep).
  const pane = $('#pane')
  pane.addEventListener('scroll', () => {
    if (!redrawing && state.tab === 'step' && transcriptBox?.isConnected) stepFollowing = atEnd(pane)
  }, { passive: true })
  // an open run's elapsed time is counted in place, never by a redraw
  setInterval(tickElapsed, 15000)
  // the live strip and the pill follow the thread's open run; the block
  // they point at is placed by the thread's own redraw, read after it
  on(['thread', 'sel', 'card', 'view'], () => { renderStrip(); requestAnimationFrame(watchBlock) })
  document.addEventListener('click', (e) => { if (pickOpen && !e.target.closest?.('.rls-pick, [data-testid="run-live-open"]')) closePick() })
  $('#run-above').addEventListener('click', () => { if (stripRun) jumpToRun(stripRun.n) })
}

// ---------------------------------------------------------------- words

const STATE_WORD = {
  proposed: 'waits on your answer',
  queued: 'queued behind the open run',
  running: 'running',
  paused: 'paused',
  done: 'done',
  partial: 'partial',
  stopped: 'stopped',
  denied: 'not run'
}

const MARK = { done: '✓', failed: '✕', stopped: '–', asking: '?', running: '', waiting: '' }

// elapsed is how long a run has worked, its pauses left out: the time it
// worked before, and the time since it last resumed while it runs. A run
// from before gummi kept its clock reads its whole span.
function elapsed (r) {
  if (!r.started) return ''
  let ms = r.activeMs || 0
  if (r.resumed) ms += Math.max(0, Date.now() - new Date(r.resumed).getTime())
  if (!ms && !r.resumed && r.ended) ms = new Date(r.ended).getTime() - new Date(r.started).getTime()
  return ms > 0 ? dur(ms) : ''
}

// PHASE says what gummi does on a run between its steps.
const PHASE = {
  baseline: 'running the done-when checks before the first step',
  until: 'judging whether the loop is done',
  ending: 'running the done-when checks and writing the report'
}

function tickElapsed () {
  const runs = (state.thread?.items || []).filter(it => it.t === 'run')
  for (const el of document.querySelectorAll('[data-elapsed-run]')) {
    const it = runs.find(x => String(x.run.n) === el.dataset.elapsedRun)
    if (!it || it.run.ended || !it.run.resumed) continue
    const t = elapsed(it.run)
    if (el.textContent !== t) el.textContent = t
  }
}

// ---------------------------------------------------------------- the block

// stepsOpen remembers, per card and run, a person's unfolding of an ended
// run's steps.
const stepsOpen = new Set()
// runFolds is a person's own fold of an ended run, per card and run: true
// folded to its line, false whole. A run they never touched folds as the
// server says (run.folded). It is forgotten when they leave the card.
const runFolds = new Map()

// folded says an ended run is drawn as its one line.
function folded (r) {
  if (!['done', 'partial', 'stopped'].includes(r.state)) return false
  const k = `${state.sel} ${r.n}`
  return runFolds.has(k) ? runFolds.get(k) : !!r.folded
}

// setFold folds run n to its line or unfolds it whole, and has the thread
// draw it again.
function setFold (n, fold) {
  runFolds.set(`${state.sel} ${n}`, fold)
  set({ runFold: (state.runFold || 0) + 1 })
}
// replies are what a person is typing to a step's question, per step card:
// a block redrawn under them keeps the words; focusedReply is the step card
// whose reply has the focus, and where its caret stood, so a redraw gives
// both back. picks are the options ticked on a question that takes several.
const replies = new Map()
let focusedReply = null
const picks = new Map()

// runSig is what the thread caches a run's block by: its revision and the
// page's own state of it.
export function runSig (it) {
  return `${it.run.rev}|${stepsOpen.has(`${state.sel} ${it.run.n}`)}|${folded(it.run)}|${state.card?.stage || ''}`
}

// runBlock draws one run's block.
export function runBlock (it) {
  const r = it.run
  const id = state.sel
  const ended = ['done', 'partial', 'stopped'].includes(r.state)
  const live = ['running', 'paused'].includes(r.state)
  if (ended && folded(r)) return foldedLine(id, r)
  const block = h('section', {
    class: ['rb', `rb-${r.state}`, live && 'rb-on', (flashing.get(`${id} ${r.n}`) || 0) > Date.now() && 'rb-flash'],
    testid: `run-${r.n}`,
    data: { run: String(r.n), state: r.state, rev: r.rev },
    'aria-label': `${r.recipe} run ${r.n}, ${STATE_WORD[r.state] || r.state}`
  }, head(id, r, ended), r.state === 'proposed' ? baselineEl(r.baseline, 'run-baseline') : null,
  r.state === 'proposed' && (r.canSave || r.savedAs) ? h('div', { class: 'rb-foot' }, h('span', { class: 'sp' }), saveEl(id, r)) : null,
  r.state === 'proposed' || r.state === 'denied' ? null : body(id, r, ended))
  return block
}

// foldedLine is an ended run folded to one line (§25.9): its state's glyph
// and the server's line, ▸; a click unfolds the block whole.
function foldedLine (id, r) {
  return h('section', {
    class: ['rb', 'rb-folded', `rb-${r.state}`, (flashing.get(`${id} ${r.n}`) || 0) > Date.now() && 'rb-flash'],
    testid: `run-${r.n}`,
    data: { run: String(r.n), state: r.state, rev: r.rev, folded: 'yes' },
    'aria-label': `${r.recipe} run ${r.n}, ${STATE_WORD[r.state] || r.state}, folded`
  }, h('button', {
    class: 'rb-line', type: 'button', testid: 'run-line', 'aria-expanded': 'false', title: 'Unfold the run',
    onclick: () => setFold(r.n, false)
  },
  h('span', { class: ['rb-mark', `s-${r.state}`], 'aria-hidden': 'true' }, r.mark || '✓'),
  h('span', { class: 'rb-ltext', testid: 'run-line-text' }, r.line || `${r.recipe} · run ${r.n}`),
  h('span', { class: 'rb-fold-mark', 'aria-hidden': 'true' }, '▸')))
}

// runPost does what to run n, saying so in a toast.
async function runPost (id, n, what, said, b) {
  if (b) b.disabled = true
  try {
    await post(cardPath(id, `runs/${n}/${what}`))
    toast(`Run ${n}: ${said}`)
    ctx.refresh(id)
    return true
  } catch (err) {
    if (b) b.disabled = false
    toast(err.data?.error || err.message, { err: true })
    return false
  }
}

// CONFIRM is the acts a person confirms first: they end what cannot be
// carried on again, as the terminal confirms them.
const CONFIRM = {
  stop: (r) => ({ title: `Stop ${r.recipe} · run ${r.n}?`, body: 'Its running steps are interrupted at their next tool call. Nothing is reverted: its changes stay in the tree.', label: 'Stop' }),
  withdraw: (r) => ({ title: `Withdraw ${r.recipe} · run ${r.n}?`, body: 'It was allowed and waits behind the open run. Withdrawn, it never starts; the session may propose again.', label: 'Withdraw' })
}

function head (id, r, ended) {
  const act = (what, label, danger) => h('button', {
    class: ['btn', danger && 'danger'], type: 'button', testid: `run-${what}`,
    onclick: (e) => {
      const b = e.currentTarget
      const ask = CONFIRM[what]?.(r)
      if (!ask) { runPost(id, r.n, what, label.toLowerCase(), b); return }
      openModal({
        title: ask.title,
        testid: `run-${what}-dialog`,
        card: id,
        bodyEl: h('div', { class: 'mbody' }, h('p', null, ask.body)),
        actions: [
          { label: 'Cancel', testid: `run-${what}-cancel` },
          { label: ask.label, danger: true, primary: true, testid: `run-${what}-confirm`, onClick: async () => { if (!await runPost(id, r.n, what, label.toLowerCase(), null)) return false } }
        ]
      })
    }
  }, label)
  const money = cr(r.spent)
  return h('div', { class: 'rb-head', testid: 'run-head' },
    h('span', { class: 'k' }, 'workflow'),
    h('b', { testid: 'run-title' }, `${r.recipe} · run ${r.n}`),
    r.specTitle ? h('span', { class: 'rb-spec', title: `spec ${r.spec}${r.specRev ? `, revision ${r.specRev}` : ''}` }, h('span', { class: 'k' }, 'spec'), ' ', r.specTitle) : null,
    h('span', { class: 'rb-r' },
      h('span', { class: ['rb-state', `s-${r.state}`], testid: 'run-state' }, r.stopping ? 'stopping' : (STATE_WORD[r.state] || r.state)),
      r.state === 'proposed' || r.state === 'denied' ? null : h('span', { class: 'mono', testid: 'run-spent' }, money),
      r.started ? h('span', { class: 'mono rb-el', testid: 'run-elapsed', data: { elapsedRun: String(r.n) } }, elapsed(r)) : null,
      r.canPause ? act('pause', 'Pause') : null,
      r.canResume ? act('resume', 'Resume') : null,
      r.canStop ? act('stop', 'Stop', true) : null,
      r.canWithdraw ? act('withdraw', 'Withdraw', true) : null,
      ended ? h('button', { class: 'link rb-foldbtn', type: 'button', testid: 'run-fold', 'aria-expanded': 'true', title: 'Fold the run to one line', 'aria-label': `Fold run ${r.n} to one line`, onclick: () => setFold(r.n, true) }, '▾') : null),
    r.reason && r.state !== 'done' ? h('span', { class: 'rb-why', testid: 'run-reason' }, r.reason) : null,
    PHASE[r.phase] ? h('span', { class: 'rb-why rb-phase', testid: 'run-phase' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), ` gummi is ${PHASE[r.phase]}`) : null,
    r.alwaysAllowed ? h('span', { class: 'rb-why rb-always', testid: 'run-always' }, 'every run of this session starts without asking · ',
      h('button', { class: 'link', type: 'button', testid: 'run-askagain', onclick: (e) => runPost(id, r.n, 'askagain', 'its session’s runs ask you again', e.currentTarget) }, 'Ask again')) : null)
}

// baselineEl is a run's baseline (§25.5): the done-when checks run before
// its first step, each checked item as it found it; one that already
// passes, or ran no tests, proves nothing about the run and says so.
// Evidence for the person: nothing waits on it.
function baselineEl (b, testid) {
  if (!b) return null
  const k = h('span', { class: 'k' }, 'baseline')
  if (b.running) return h('div', { class: 'rb-base', testid, data: { running: 'yes' } }, k, ' ', h('span', { class: 'hint', testid: 'baseline-running' }, 'running the done-when checks before the run…'))
  if (b.err) return h('div', { class: 'rb-base', testid }, k, ' ', h('span', { class: 'hint' }, `not run: ${b.err}`))
  return h('div', { class: 'rb-base', testid }, k,
    (b.items || []).map(it => h('span', {
      class: ['rb-bi', it.problem ? 'warnc' : 'hint'], testid: `baseline-${it.item}`, data: { verdict: it.verdict },
      title: it.problem ? 'it proves nothing about the run' : null
    }, it.problem ? `! ${it.line}` : it.line)))
}

function body (id, r, ended) {
  const parts = [strip(r)]
  if (r.baseline) parts.push(baselineEl(r.baseline, 'run-baseline'))
  if (r.pending) {
    parts.push(h('div', { class: 'rb-pending', testid: 'run-pending' },
      h('b', null, 'Waits on you'), ' · ', r.pending.question.split('\n')[0],
      h('span', { class: 'hint' }, ' — answer it below the thread')))
  }
  const rows = h('div', { class: 'rb-rows', testid: 'run-rows' }, r.rows.map(row => [stepRow(id, row), row.question ? question(id, row) : null]))
  if (ended) {
    if (r.results?.length) parts.push(results(r))
    const key = `${id} ${r.n}`
    const d = h('details', { class: 'rb-fold', testid: 'run-steps', open: stepsOpen.has(key) },
      h('summary', null, plural(r.rows.length, 'step'), ' · ', r.rows.map(x => x.folded ? `${x.id} ↺ ${x.iter}` : x.id).join(' · ')), rows)
    d.addEventListener('toggle', () => { if (d.open) stepsOpen.add(key); else stepsOpen.delete(key) })
    parts.push(d)
    if (r.commits?.length) {
      parts.push(h('div', { class: 'rb-commits', testid: 'run-commits' }, h('span', { class: 'k' }, 'commits made while it ran'),
        r.commits.map(c => h('span', { class: 'mono' }, c))))
    }
  } else {
    parts.push(rows)
  }
  parts.push(artifactsEl(id, r))
  parts.push(foot(id, r, ended))
  return parts
}

function strip (r) {
  const chip = (e) => {
    const word = e.kind === 'repeat'
      ? `${e.id} ↺ ${e.iter || 0}/${e.max}`
      : e.kind === 'parallel' ? (e.branches || []).map(b => b.id).join(' ∥ ') : e.id
    return h('span', { class: ['rb-chip', `s-${e.state}`], testid: `run-strip-${e.id}`, title: `${e.id}: ${e.state}` },
      word, MARK[e.state] ? h('span', { class: 'm', 'aria-hidden': 'true' }, ' ' + MARK[e.state]) : null)
  }
  return h('div', { class: 'rb-strip', testid: 'run-strip' },
    r.strip.map((e, i) => [i ? h('i', { 'aria-hidden': 'true' }, '→') : null, chip(e)]))
}

function stepRow (id, row) {
  const open = row.card && !row.folded
  const where = row.iter ? `↺ ${row.iter}` : ''
  return h('div', {
    class: ['rb-row', `s-${row.state}`, row.waiting && 'waiting', row.question && 'asking', row.folded && 'folded', row.watch && 'rb-watch', row.pass && !row.role && 'rb-pass'],
    testid: `run-row-${row.key}`,
    data: { card: row.card || null, state: row.state }
  },
  h('span', { class: 'dot', 'aria-label': row.state }),
  h('span', { class: 'at mono', title: row.path }, where),
  h('span', { class: ['wrole', row.role && `r-${row.role}`] }, row.role || row.repeat || ''),
  h('span', { class: 'ti' },
    h('span', { class: 'n' }, row.pass && row.folded ? `${row.id} · ${row.event}` : (row.folded ? row.steps : row.id)),
    row.line ? h('small', { testid: 'run-row-line' }, row.line) : null,
    row.watch && row.watch.url ? h('a', { class: 'rb-pr mono', href: row.watch.url, target: '_blank', rel: 'noopener', testid: 'run-watch-pr' }, row.watch.pr) : null,
    row.pass && row.folded && row.steps ? h('small', { class: 'hint' }, row.steps) : null,
    row.flag ? h('small', { class: 'flag', testid: 'run-row-flag' }, row.flag) : null),
  h('span', { class: 'cost mono', title: [row.model, rowTook(row) && `took ${rowTook(row)}`].filter(Boolean).join(' · ') || null },
    rowTook(row) ? h('small', { class: 'hint', testid: 'run-row-took' }, `${rowTook(row)} `) : null, row.spent ? cr(row.spent) : '–'),
  open
    ? h('button', { class: 'link rb-open', type: 'button', testid: 'run-open', 'aria-label': `Open step ${row.path}`, onclick: () => openStep(row.card) }, 'Open')
    : h('span'))
}

// rowTook is how long an ended step took, '' for one still at work.
function rowTook (row) {
  if (!row.started || !row.ended || row.folded) return ''
  const ms = new Date(row.ended).getTime() - new Date(row.started).getTime()
  return ms > 0 ? dur(ms) : ''
}

function question (id, row) {
  const q = row.question
  const send = async (answer, btn) => {
    if (!String(answer || '').trim()) return
    if (btn) btn.disabled = true
    try {
      // the answer names the question read: refused once the step asks another
      await post(cardPath(id, `steps/${encodeURIComponent(row.card)}/answer`), { answer, call: q.call || '' })
      replies.delete(row.card)
      picks.delete(row.card)
      toast(`Answered ${row.id}: ${answer}`)
      ctx.refresh(id)
    } catch (err) {
      if (btn) btn.disabled = false
      toast(err.data?.error || err.message, { err: true })
    }
  }
  const input = h('input', { type: 'text', class: 'rb-reply', testid: 'run-question-reply', placeholder: 'Reply…', 'aria-label': `Reply to ${row.id} in your own words`, value: replies.get(row.card) || '' })
  const caret = () => { if (focusedReply?.card === row.card) focusedReply.at = input.selectionStart }
  input.addEventListener('input', () => { replies.set(row.card, input.value); caret() })
  input.addEventListener('keyup', caret)
  input.addEventListener('click', caret)
  input.addEventListener('focus', () => { focusedReply = { card: row.card, at: input.selectionStart } })
  // a redraw removes the input without a blur: only the person leaving it
  // lets the focus go
  input.addEventListener('blur', () => { if (focusedReply?.card === row.card) focusedReply = null })
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); send(input.value, null) } })
  if (focusedReply?.card === row.card) {
    const at = focusedReply.at ?? input.value.length
    requestAnimationFrame(() => { if (input.isConnected && document.activeElement !== input) { input.focus(); input.setSelectionRange(at, at) } })
  }
  // a question that takes several answers: each option ticks, and Send
  // picked sends every one ticked, as the decision picker does
  const ticked = picks.get(row.card) || new Set()
  const opts = q.multi
    ? (q.options || []).map((o, i) => h('label', { class: ['rb-pick', o.recommended && 'pri'], title: o.detail || o.label },
        h('input', {
          type: 'checkbox', testid: `run-question-pick-${i}`, checked: ticked.has(o.label),
          onchange: (e) => { if (e.target.checked) ticked.add(o.label); else ticked.delete(o.label); picks.set(row.card, ticked) }
        }), ' ', o.label))
    : (q.options || []).map((o, i) => h('button', {
        class: ['btn', o.recommended && 'pri'], type: 'button', testid: `run-question-option-${i}`, title: o.detail || o.label,
        onclick: (e) => send(o.label, e.currentTarget)
      }, o.label))
  return h('div', { class: 'rb-ask', testid: 'run-question', data: { card: row.card } },
    h('b', null, `${row.id} asks`, q.multi ? h('span', { class: 'hint' }, ' · pick any') : null),
    h('div', { class: 'rb-q' }, q.question),
    h('div', { class: 'rb-opts' }, opts,
      q.multi ? h('button', {
        class: 'btn pri', type: 'button', testid: 'run-question-send-picked',
        onclick: (e) => { const labels = (q.options || []).map(o => o.label).filter(l => ticked.has(l)); if (labels.length) send(labels.join(', '), e.currentTarget); else toast('Tick an answer first') }
      }, 'Send picked') : null),
    h('div', { class: 'rb-replyrow' }, input,
      h('button', { class: 'btn', type: 'button', testid: 'run-question-send', onclick: (e) => send(input.value, e.currentTarget) }, 'Send')))
}

function results (r) {
  const word = { met: '✓', unmet: '✕', unjudged: '–' }
  return h('div', { class: 'rb-results', testid: 'run-results' }, r.results.map(x => h('span', {
    class: ['rb-res', `v-${x.verdict}`], testid: `run-result-${x.item}`, title: [x.checked ? 'its check' : 'judged', x.detail].filter(Boolean).join(': ')
  }, h('span', { 'aria-label': x.verdict }, word[x.verdict] || '·'), ` ${x.item}`,
  x.change ? h('small', { class: 'hint', testid: 'run-result-change' }, ` ${x.change}`) : null)))
}

function foot (id, r, ended) {
  const files = r.files || []
  return h('div', { class: 'rb-foot', testid: 'run-files' },
    files.length
      ? h('span', { title: files.join('\n') }, h('b', null, plural(files.length, 'file')), ended ? ' left changed in the tree' : ' changed in the tree', ' · uncommitted unless committed since')
      : h('span', null, ended ? 'the run left the tree as it found it' : 'nothing changed yet'),
    h('span', { class: 'sp' }),
    files.length && r.started ? h('button', { class: 'link', type: 'button', testid: 'run-diff', onclick: () => openRunDiff(id, r) }, 'diff ▸') : null,
    r.report ? h('button', { class: 'link', type: 'button', testid: 'run-report', onclick: () => openSpec(r.spec) }, 'report ▸') : null,
    r.canForward ? h('button', { class: 'btn', type: 'button', testid: 'run-forward', title: 'Act on the feedback the trust filter kept out, as it stands now; read it on the PR first', onclick: (e) => { const btn = e.currentTarget; openModal({ title: 'Forward kept-out feedback', testid: 'run-forward-confirm', body: h('p', { class: 'aq' }, 'Read it on the PR first. Forwarded, each item the trust filter kept out is acted on as trusted feedback, as it stands now; an edit after this keeps it out again.'), actions: [{ label: 'Cancel', testid: 'run-forward-cancel' }, { label: 'Forward', primary: true, testid: 'run-forward-go', onClick: () => runPost(id, r.n, 'forward', 'forwarded to its watch', btn) }] }) } }, 'Forward kept-out feedback') : null,
    r.canRetry ? h('button', { class: 'btn', type: 'button', testid: 'run-retry', title: 'Run its failed and stopped steps again, keeping every step that finished', onclick: (e) => runPost(id, r.n, 'retry', 'its failed steps run again', e.currentTarget) }, 'Retry failed steps') : null,
    r.canRevert ? h('button', { class: 'btn', type: 'button', testid: 'run-revert', onclick: () => openRevert(id, r.n) }, 'Revert run…') : null,
    saveEl(id, r))
}

// artifactsEl lists what the run's recipe declares beside the tree — a
// report, a design, a review — each read in a dialog once written.
function artifactsEl (id, r) {
  const arts = r.artifacts || []
  if (!arts.length) return null
  return h('div', { class: 'rb-arts', testid: 'run-artifacts' }, h('span', { class: 'k' }, 'artifacts'),
    arts.map(a => a.exists
      ? h('button', { class: 'link mono', type: 'button', testid: `run-artifact-${a.name}`, title: `read ${a.file}`, onclick: () => openArtifact(id, r.n, a) }, a.file)
      : h('span', { class: 'mono hint', testid: `run-artifact-${a.name}`, title: 'not written yet' }, a.file)))
}

async function openArtifact (id, n, a) {
  let t
  try {
    t = await get(cardPath(id, `runs/${n}/artifacts/${encodeURIComponent(a.name)}`))
  } catch (err) {
    toast(err.data?.error || err.message, { err: true })
    return
  }
  openModal({
    title: `Run ${n} · ${t.file}`,
    testid: 'artifact-dialog',
    card: id,
    bodyEl: h('div', { class: 'mbody' },
      h('pre', { class: 'rb-artpre', testid: 'artifact-text', tabindex: '0' }, t.text),
      t.cut ? h('p', { class: 'hint' }, 'Only its first part is shown.') : null),
    actions: [{ label: 'Close', testid: 'artifact-close' }]
  })
}

// openRunDiff shows the run's whole diff: from where it started to the
// tree now.
async function openRunDiff (id, r) {
  let d
  try {
    d = await get(cardPath(id, `runs/${r.n}/diff`))
  } catch (err) {
    toast(err.data?.error || err.message, { err: true })
    return
  }
  const box = h('div', { class: 'stp-diff rb-rundiff', testid: 'run-diff-body' })
  drawDiff(box, d)
  openModal({
    title: `${r.recipe} · run ${r.n}: its diff`,
    testid: 'run-diff-dialog',
    card: id,
    bodyEl: h('div', { class: 'mbody' }, h('p', { class: 'hint' }, 'From where the run started to the tree now, uncommitted work beside it included.'), box),
    actions: [{ label: 'Close', testid: 'run-diff-close' }]
  })
}

// saveEl is a run's recipe as a person keeps it: Save as recipe… on a run
// whose recipe the session wrote inline, or what it was saved as.
function saveEl (id, r) {
  if (r.savedAs) return h('span', { class: 'hint', testid: 'run-saved' }, 'saved as ', h('b', { class: 'mono' }, r.savedAs))
  if (!r.canSave) return null
  return h('button', { class: 'btn', type: 'button', testid: 'run-save', onclick: () => openSave(id, r) }, 'Save as recipe…')
}

// openSave asks the name the run's recipe is saved as, under
// .gummi/workflows/; a name a recipe already has is refused by the server.
function openSave (id, r) {
  const input = h('input', { type: 'text', class: 'mono', testid: 'save-name', 'aria-label': 'Recipe name', placeholder: 'recipe name', value: r.saveName || '', spellcheck: 'false' })
  const err = h('p', { class: 'warnc', testid: 'save-error', hidden: true })
  const save = {
    label: 'Save',
    primary: true,
    testid: 'save-confirm',
    onClick: async () => {
      const name = input.value.trim()
      if (!name) { input.focus(); return false }
      try {
        const done = await post(cardPath(id, `runs/${r.n}/save`), { name })
        toast(`Run ${r.n}'s recipe saved as ${done.name}`)
        ctx.refresh(id)
      } catch (e) {
        err.textContent = e.data?.error || e.message
        err.hidden = false
        return false
      }
    }
  }
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); save.el?.click() } })
  openModal({
    title: `Save run ${r.n}'s recipe`,
    testid: 'save-dialog',
    card: id,
    bodyEl: h('div', { class: 'mbody' },
      h('p', null, 'Kept as a workspace recipe, ', h('span', { class: 'mono' }, '.gummi/workflows/<name>.yaml'), ', as the session wrote it: any session may run it by name.'),
      input, err),
    actions: [{ label: 'Cancel', testid: 'save-cancel' }, save]
  })
  requestAnimationFrame(() => { input.focus(); input.select() })
}

// openSpec shows a spec on the Specs tab: a run's report is written there.
export let openSpec = () => {}
export function setOpenSpec (fn) { openSpec = fn }

// markStep rings the row of the step open beside the thread, in place.
export function markStep (root) {
  for (const el of root.querySelectorAll('.rb-row[data-card]')) el.classList.toggle('sel', el.dataset.card === state.step)
}

// ---------------------------------------------------------------- revert

// openRevert previews and confirms a revert of run n: to where it started,
// or with step to where that step started.
async function openRevert (id, n, step = '') {
  let pre
  const q = step ? `?step=${encodeURIComponent(step)}` : ''
  try {
    pre = await get(cardPath(id, `runs/${n}/revert${q}`))
  } catch (err) {
    toast(err.data?.error || err.message, { err: true })
    return
  }
  const paths = pre.paths || []
  openModal({
    title: step ? `Revert run ${n} to before ${step}?` : `Revert run ${n}?`,
    testid: 'revert-dialog',
    card: id,
    bodyEl: h('div', { class: 'mbody' },
      h('p', null, paths.length
        ? `${plural(paths.length, 'path')} go back to how they were ${step ? `before ${step} started` : `when run ${n} started`}:`
        : step ? 'Nothing differs from where the step started.' : 'Nothing differs from where the run started.'),
      paths.length ? h('ul', { class: 'rb-paths mono', testid: 'revert-paths' }, paths.map(p => h('li', null, p))) : null,
      h('p', { class: 'hint', testid: 'revert-note' }, pre.note)),
    actions: [
      { label: 'Cancel', testid: 'revert-cancel' },
      {
        label: 'Revert',
        danger: true,
        primary: true,
        testid: 'revert-confirm',
        disabled: !paths.length,
        onClick: async () => {
          try {
            // the revert is of what this preview listed: the server refuses it if the tree has moved since
            const done = await post(cardPath(id, `runs/${n}/revert`), { tree: pre.tree, step })
            toast(`Run ${n} reverted: ${plural((done.paths || []).length, 'path')} put back`)
            ctx.refresh(id)
          } catch (err) {
            toast(err.data?.error || err.message, { err: true })
            return false
          }
        }
      }
    ]
  })
}

// ---------------------------------------------------------------- the step panel

let transcript = null // { card, thread } as last read
let stepDiff = null // { card, data } as last read
let transcriptBusy = false
let transcriptAgain = false
const inner = new Map() // step card -> the inner tab shown
const drafts = new Map() // step card -> what is typed to it

export function openStep (card) {
  if (!card) return
  set({ step: card })
  ctx.setTab('step')
}

export const stepTab = {
  name: 'step',
  label: 'Step',
  key: 'g o',
  hidden: () => !state.step,
  fetch: (id) => state.step ? get(cardPath(id, `steps/${encodeURIComponent(state.step)}`)) : Promise.resolve(null),
  empty: (d) => !d,
  render: renderStep
}

async function loadTranscript () {
  const card = state.step
  if (!card) return
  if (transcriptBusy) { transcriptAgain = true; return }
  transcriptBusy = true
  try {
    const t = await get(cardPath(card, 'thread'))
    if (state.step !== card) return
    const was = transcript
    transcript = { card, thread: t }
    if (!was || was.card !== card || JSON.stringify(was.thread) !== JSON.stringify(t)) drawTranscript()
  } catch (err) {
    if (state.step === card && !transcript) { transcript = { card, err }; drawTranscript() }
  } finally {
    transcriptBusy = false
    if (transcriptAgain) { transcriptAgain = false; loadTranscript() }
  }
}

// transcriptBox is the step's conversation, kept across the panel's
// redraws: the panel redraws its pane when the step's head moves, and the
// conversation under the reader is not drawn again for that.
let transcriptBox = null
// stepFollowing says the reader is at the transcript's newest line, and a
// redraw keeps them there; redrawing marks the pane's own redraw, whose
// scroll is not the reader's.
let stepFollowing = true
let redrawing = false
function atEnd (sc) { return sc.scrollHeight - sc.scrollTop - sc.clientHeight < 24 }

// stickStep keeps a following reader at the transcript's newest line once
// the pane has laid out.
function stickStep () {
  requestAnimationFrame(() => {
    const pane = $('#pane')
    if (stepFollowing && state.tab === 'step' && transcriptBox?.isConnected) pane.scrollTop = pane.scrollHeight
    redrawing = false
  })
}
let drawTurns = () => []
let reconcileInto = (box, els) => { clear(box); box.append(...els) }
export function setTranscriptDrawer (draw, reconcile) { drawTurns = draw; reconcileInto = reconcile }

function drawTranscript () {
  if (!transcriptBox) return
  const t = transcript
  if (!t || t.card !== state.step) { reconcileInto(transcriptBox, [h('div', { class: 'empty' }, h('span', { class: 'spinner' }))]); return }
  if (t.err) { reconcileInto(transcriptBox, [h('div', { class: 'empty err' }, h('b', null, 'The step’s conversation did not load'), t.err.message)]); return }
  const els = drawTurns(t.card, t.thread.live)
  reconcileInto(transcriptBox, els.length ? els : [h('div', { class: 'empty', testid: 'step-transcript-empty' }, 'Nothing said yet.')])
  stickStep()
}

function renderStep (pane, entry, pctx) {
  const s = entry.data
  if (!s) { pane.append(h('div', { class: 'empty' }, 'No step open.')); return }
  const tab = inner.get(s.card) || 'transcript'
  const setInner = (t) => { inner.set(s.card, t); pctx.rerender() }
  const crumb = h('nav', { class: 'stp-crumb', testid: 'step-crumb', 'aria-label': 'Where this step is' },
    s.crumb.map((c, i) => [i ? h('span', { 'aria-hidden': 'true' }, ' › ') : null,
      i === 0 ? h('button', { class: 'link', type: 'button', testid: 'step-close', onclick: backToSession }, c)
        : i === s.crumb.length - 1 ? h('b', null, c) : h('span', null, c)]))
  const money = cr(s.spent)
  const head = h('div', { class: 'stp-head', testid: 'step-head' },
    crumb,
    h('div', { class: 'stp-title' },
      h('span', { class: ['wrole', `r-${s.role}`] }, s.role),
      h('b', { testid: 'step-title' }, s.id)),
    h('div', { class: 'stp-meta' },
      s.live ? h('span', { class: 'stp-live', testid: 'step-live' }, 'live') : h('span', { class: ['rb-state', `s-${s.state}`], testid: 'step-state' }, s.state),
      [s.model, s.slot && `slot ${s.slot}`].filter(Boolean).join(' · '),
      h('span', { class: 'mono', testid: 'step-spent' }, money),
      'in the session’s tree'),
    s.reason ? h('div', { class: 'rb-why' }, s.reason) : null,
    s.canRevert ? h('div', { class: 'stp-acts' }, h('button', {
      class: 'btn', type: 'button', testid: 'step-revert', title: 'Put the tree back to where this step started, keeping what the steps before it did',
      onclick: () => openRevert(s.session, s.run, s.path)
    }, 'Revert to before this step…')) : null)
  const tabs = [['transcript', 'Transcript'], ['diff', 'Diff'], ['brief', 'Brief']]
  const bar = h('div', { class: 'seg stp-tabs', role: 'tablist', 'aria-label': 'The step' }, tabs.map(([k, l]) => h('button', {
    class: tab === k && 'on', type: 'button', role: 'tab', 'aria-selected': String(tab === k), testid: `step-tab-${k}`, onclick: () => setInner(k)
  }, l)))
  // a step that asks is answered here too: the strip's Answer opens it
  const ask = s.question ? question(s.session, { card: s.card, id: s.id, question: s.question }) : null
  pane.append(h('div', { class: 'stp', testid: 'step-panel', data: { card: s.card } }, head, ask, bar, innerBody(tab, s), composer(s, pctx)))
}

// backToSession closes the step: the surface it was shown on closes with
// it, and a phone goes back to the thread.
function backToSession () {
  set({ step: null, tab: 'spec', ...(isMobile() ? { view: 'thread' } : { rightHidden: true }) })
}

function innerBody (tab, s) {
  if (tab === 'transcript') {
    if (!transcriptBox || transcriptBox.dataset.card !== s.card) {
      // a step opened lands on its newest line, as a thread does
      transcriptBox = h('div', { class: 'stp-transcript', testid: 'step-transcript', data: { card: s.card } })
      stepFollowing = true
      drawTranscript()
      if (!transcript || transcript.card !== s.card) loadTranscript()
    }
    redrawing = true
    stickStep()
    return transcriptBox
  }
  if (tab === 'diff') {
    const box = h('div', { class: 'stp-diff', testid: 'step-diff' })
    if (stepDiff?.card === s.card) drawDiff(box, stepDiff.data)
    else {
      box.append(h('div', { class: 'empty' }, h('span', { class: 'spinner' })))
      get(cardPath(s.session, `steps/${encodeURIComponent(s.card)}/diff`)).then((d) => {
        stepDiff = { card: s.card, data: d }
        if (state.step === s.card) { clear(box); drawDiff(box, d) }
      }).catch((err) => { clear(box); box.append(h('div', { class: 'empty err' }, err.message)) })
    }
    return box
  }
  return h('div', { class: 'stp-brief', testid: 'step-brief' },
    (s.artifacts || []).length
      ? h('div', { class: 'stp-arts', testid: 'step-artifacts' }, s.artifacts.map(a => h('div', { class: ['stp-art', a.exists && 'there'] },
        h('span', { class: 'k' }, a.writes ? 'writes' : 'reads'), h('b', null, a.name), h('span', { class: 'mono', title: a.path }, a.path),
        a.exists ? null : h('span', { class: 'hint' }, 'not there yet'))))
      : null,
    h('pre', { class: 'stp-prompt', tabindex: '0', 'aria-label': 'The prompt as filled in' }, s.brief || ''),
    s.output ? h('div', { class: 'stp-out' }, h('span', { class: 'k' }, 'its output'), h('pre', { tabindex: '0' }, s.output)) : null)
}

function drawDiff (box, d) {
  if (!d.files?.length) { box.append(h('div', { class: 'empty', testid: 'step-diff-none' }, d.why || 'No change.')); return }
  for (const f of d.files) {
    box.append(h('section', { class: 'file', testid: 'step-diff-file', 'aria-label': f.path, data: { path: f.path } },
      h('div', { class: 'fh' }, h('span', { class: 'p' }, f.path), h('span', { class: 'add' }, `+${f.add}`), h('span', { class: 'del' }, `−${f.del}`)),
      (f.hunks || []).map(hk => h('div', { class: 'hunk' }, h('div', { class: 'hh' }, hk.header),
        (hk.lines || []).map(ln => {
          const code = h('span')
          code.innerHTML = hl(ln.text) || ' ' // hl escapes everything it is given
          return h('div', { class: ['ln', ln.t === '+' ? 'a' : ln.t === '-' ? 'd' : ''] },
            h('span', { class: 'o ro' }, ln.old ? String(ln.old) : ''), h('span', { class: 'n ro' }, ln.new ? String(ln.new) : ''),
            h('span', { class: 'm', 'aria-hidden': 'true' }, ln.t === ' ' ? '' : ln.t), code)
        })))))
  }
}

// composer is the line to the step's agent: a turn while it runs, a
// follow-up once it has ended (the server's words say which).
function composer (s, pctx) {
  const ta = h('textarea', { class: 'stp-input', testid: 'step-input', rows: '1', placeholder: `Message ${s.id}`, 'aria-label': `Message ${s.id}` })
  ta.value = drafts.get(s.card) || ''
  ta.addEventListener('input', () => drafts.set(s.card, ta.value))
  const send = async (btn) => {
    const text = ta.value.trim()
    if (!text) { ta.focus(); return }
    btn.disabled = true
    try {
      await post(cardPath(s.session, `steps/${encodeURIComponent(s.card)}/send`), { text })
      drafts.delete(s.card)
      ta.value = ''
      toast(`Sent to ${s.id}`)
      loadTranscript()
    } catch (err) {
      toast(err.data?.error || err.message, { err: true })
    } finally {
      btn.disabled = false
    }
  }
  const btn = h('button', { class: 'send', type: 'button', testid: 'step-send', onclick: (e) => send(e.currentTarget) }, 'Send')
  ta.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(btn) } })
  // the thread's own composer, its bar saying what enter sends
  return h('div', { class: 'stp-comp', testid: 'step-composer', data: { draft: `step:${s.card}` } },
    h('div', { class: 'composer' }, ta,
      h('div', { class: 'bar' }, h('span', { class: 'enter' }, h('kbd', null, 'enter'), h('b', null, s.says)), btn)))
}

// ---------------------------------------------------------------- the proposal

// proposalEl is a run's proposal (or revised plan) as its decision draws
// it: the step tree with each step's role and the model it runs on, the
// spec it freezes,
// the checks it runs, the warnings, and what the session's pool has left —
// the run spends from it and estimates nothing.
export function proposalEl (p) {
  const left = p.bounded ? `${cr(p.poolLeft)} left in the session` : 'the session has no envelope'
  return h('div', { class: 'wfp', testid: 'run-proposal' },
    h('div', { class: 'wfp-head' },
      h('b', null, p.kind === 'plan' ? `Allow the revised plan · run ${p.run}?` : `Allow ${p.recipe} · run ${p.run}?`),
      h('span', { class: 'hint', testid: 'proposal-tree' }, ['in the session’s tree', p.branch, p.dirty ? 'holds uncommitted work' : 'nothing uncommitted'].filter(Boolean).join(' · '))),
    p.description ? h('div', { class: 'hint' }, p.description) : null,
    h('div', { class: 'wfp-spec', testid: 'proposal-spec' }, h('span', { class: 'k' }, 'spec'), ' ',
      `S${p.spec} ${p.specTitle || ''}`, p.specRev ? ` · revision ${p.specRev}, frozen now` : ''),
    stepTable(p.steps, 'Its steps', 'proposal-step'),
    p.replaces?.length ? h('div', { class: 'wfp-replaces', testid: 'proposal-replaces' }, h('span', { class: 'k' }, 'what it replaces, which would not run'),
      stepTable(p.replaces, 'The steps it replaces', 'proposal-replaced')) : null,
    p.checks?.length ? h('div', { class: 'wfp-checks', testid: 'proposal-checks' }, h('span', { class: 'k' }, 'runs in the tree when it ends'),
      p.checks.map(c => h('div', null, h('b', { class: 'mono' }, c.item), ' ', h('span', { class: 'mono' }, c.cmd)))) : null,
    baselineEl(p.baseline, 'proposal-baseline'),
    p.preflight ? h('div', { class: 'wfp-checks', testid: 'proposal-preflight' }, h('span', { class: 'k' }, 'preflight passed'), ' ',
      h('span', { class: 'mono' }, p.preflight),
      p.preflightOutput ? h('pre', { class: 'hint mono' }, p.preflightOutput) : null) : null,
    h('div', { class: 'wfp-ticks' },
      p.warnings?.length
        ? h('div', { class: 'warnc', testid: 'proposal-warnings' }, p.warnings.map(w => h('div', null, `! ${w}`)))
        : h('span', { class: 'okc' }, '✓ recipe checks pass'),
      h('span', { class: p.bounded && p.poolLeft <= 0 ? 'warnc' : 'okc', testid: 'proposal-money' }, `${left}; the run spends from it`)),
    h('div', { class: 'hint' }, 'Changes stay uncommitted in the tree · a snapshot at every step'))
}

// stepTable is a proposal's step tree: each step with its role, the
// model it runs on and its prompt's gist; a repeat with its bound and its
// condition; a parallel with its join.
function stepTable (steps, label, testid) {
  return h('div', { class: 'wfp-steps', role: 'table', 'aria-label': label }, steps.map(s => h('div', {
    class: ['wfp-step', `k-${s.kind}`], role: 'row', testid: `${testid}-${s.id}`, style: { '--depth': String(s.depth) }
  },
  h('span', { class: 'id mono', role: 'cell' }, s.kind === 'repeat' ? `${s.id} ↺≤${s.max}` : s.kind === 'parallel' ? `${s.id} ∥` : s.id),
  h('span', { class: ['wrole', s.role && `r-${s.role}`], role: 'cell' }, s.role || (s.kind === 'parallel' ? s.join : '')),
  h('span', { class: 'ti', role: 'cell', title: s.prompt || null }, s.kind === 'repeat'
    ? `until ${s.until} · at the max: ${s.onMax}`
    : s.kind === 'parallel' ? `side by side, join ${s.join}` : [s.model, s.prompt].filter(Boolean).join(' · ')))))
}

// ---------------------------------------------------------------- the live strip

// openRunItem is the thread's open run, the one the strip is about.
function openRunItem () {
  const runs = (state.thread?.items || []).filter(it => it.t === 'run' && it.run.live)
  return runs.length ? runs[runs.length - 1].run : null
}

let stripRun = null // the run the strip draws, as last drawn
let stripEl = null // the strip's root, kept while a run is open
const stripParts = new Map() // part name -> { sig, el }
let pickOpen = false

// renderStrip draws the open run's strip into its slot, part by part: a
// part whose words have not moved keeps its node.
function renderStrip () {
  const slot = $('#run-live-slot')
  const r = state.sel ? openRunItem() : null
  stripRun = r
  if (!r) {
    if (stripEl) { stripEl.remove(); stripEl = null; stripParts.clear(); closePick() }
    $('#run-above').hidden = true
    return
  }
  const l = r.live
  if (!stripEl || stripEl.dataset.run !== String(r.n) || stripEl.dataset.card !== state.sel) {
    stripEl?.remove()
    stripParts.clear()
    closePick()
    stripEl = h('div', { class: 'rls', testid: 'run-live', role: 'status', 'aria-live': 'off', data: { run: String(r.n), card: state.sel } })
    slot.append(stripEl)
  }
  stripEl.className = ['rls', `rls-${l.mood}`].join(' ')
  stripEl.dataset.mood = l.mood
  const running = l.mood === 'running'
  const parts = [
    ['dot', l.mood, () => h('span', { class: 'pd', 'aria-hidden': 'true' })],
    ['name', l.name, () => h('b', { class: 'nm', testid: 'run-live-name' }, l.name)],
    ['sp', running ? JSON.stringify(r.strip) : '', () => running ? liveEntries(r.strip) : null],
    ['now', JSON.stringify([l.mood, l.who, l.action, l.question, l.line]), () => liveNow(l)],
    ['m', `${l.mood}|${r.spent}|${r.started}`, () => l.mood === 'asking' || l.mood === 'deciding' ? null
      : h('span', { class: 'm', testid: 'run-live-money' }, cr(r.spent),
        r.started && elapsed(r) ? [' · ', h('span', { data: { elapsedRun: String(r.n) } }, elapsed(r))] : null)],
    ['acts', JSON.stringify([state.sel, r.n, l.mood, l.card, l.steps, l.canTopUp, l.canResume, !!state.card?.actions?.some(a => a.id === 'envelope')]), () => h('span', { class: 'acts' }, liveActs(r, l))]
  ]
  let i = 0
  for (const [name, sig, make] of parts) {
    let p = stripParts.get(name)
    if (!p || p.sig !== sig) {
      const el = make()
      p?.el?.remove()
      p = { sig, el }
      stripParts.set(name, p)
    }
    if (p.el) {
      if (stripEl.children[i] !== p.el) stripEl.insertBefore(p.el, stripEl.children[i] || null)
      i++
    }
  }
}

// liveEntries is the recipe's top-level entries as the strip says them.
function liveEntries (strip) {
  return h('span', { class: 'sp', testid: 'run-live-strip' }, (strip || []).map((e, i) => {
    const word = e.kind === 'repeat'
      ? `${e.id} ↺ ${e.iter || 0}/${e.max}`
      : e.kind === 'parallel' ? (e.branches || []).map(b => b.id).join(' ∥ ') : e.id
    return [i ? h('i', { 'aria-hidden': 'true' }, '›') : null,
      h('span', { class: `s-${e.state}` }, word, MARK[e.state] ? ' ' + MARK[e.state] : '')]
  }))
}

// liveNow is what the run is doing now: its step and that step's action,
// a step's question, or what a run that is not running says.
function liveNow (l) {
  switch (l.mood) {
    case 'running':
      return h('span', { class: 'now', testid: 'run-live-now', title: [l.who, l.action].filter(Boolean).join(' · ') },
        l.who ? [h('b', null, l.who), ' · '] : null, l.action)
    case 'asking':
      return h('span', { class: 'now', testid: 'run-live-now', title: l.question }, h('b', null, `${l.who} asks:`), ' ', l.question)
  }
  return h('span', { class: 'now', testid: 'run-live-now', title: l.line }, l.line)
}

// liveActs is what the strip offers: Jump always, and beside it Open (a
// pick of the live steps when several work side by side), Answer, Top up…
// or Resume.
function liveActs (r, l) {
  const id = state.sel
  const btn = (testid, label, onclick, pri) => h('button', { class: ['bt', pri && 'pri'], type: 'button', testid, onclick }, label)
  const jump = (pri) => btn('run-live-jump', '↑ Jump to run', () => jumpToRun(r.n), pri)
  const steps = l.steps || []
  switch (l.mood) {
    case 'running':
      if (steps.length > 1) {
        return [jump(true), h('span', { class: 'rls-pickwrap' },
          btn('run-live-open', 'Open… ▾', (e) => { e.stopPropagation(); pickOpen ? closePick() : openPick(e.currentTarget, steps) }))]
      }
      return [jump(true), l.card ? btn('run-live-open', `Open ${l.who}`, () => openStep(l.card)) : null]
    case 'asking':
      // the question is answered in one place, under its row in the run's
      // block: Answer goes there and puts the first answer under the
      // keyboard, rather than opening the step as a second place to answer
      return [btn('run-live-answer', 'Answer ↑', () => answerInBlock(r.n), true)]
    case 'budget': {
      // the run's own question is the top-up: Top up… answers it with an
      // amount, read when pressed; with none open, the card's budget is
      // raised instead
      const raise = state.card?.actions?.find(a => a.id === 'envelope')
      const question = () => state.card?.decision?.runKind === 'budget' ? state.card.decision : null
      return [btn('run-live-topup', 'Top up…', () => {
        const d = question()
        const words = d?.options?.find(o => o.words)
        if (words) openTopUp(id, r, d, words)
        else if (raise) runAction(state.card, raise)
        else toast('Raise the session’s budget from its menu')
      }, true), jump(false)]
    }
    case 'paused':
      return [l.canResume ? btn('run-live-resume', 'Resume', async (e) => {
        const b = e.currentTarget
        b.disabled = true
        try {
          await post(cardPath(id, `runs/${r.n}/resume`))
          toast(`Run ${r.n}: resumed`)
          ctx.refresh(id)
        } catch (err) {
          b.disabled = false
          toast(err.data?.error || err.message, { err: true })
        }
      }, true) : null, jump(!l.canResume)]
  }
  return [jump(true)]
}

// openTopUp asks how much to top the session up by, in dollars, and
// answers the run's budget question with it: the run carries on.
function openTopUp (id, r, d, words) {
  const suggest = (d.question.match(/\$(\d+(?:\.\d+)?)/) || [])[1] || ''
  const input = h('input', { type: 'text', inputmode: 'decimal', class: 'mono', testid: 'topup-amount', 'aria-label': 'Dollars to top up by', placeholder: '1.00', value: suggest })
  const err = h('p', { class: 'warnc', testid: 'topup-error', hidden: true })
  const go = {
    label: 'Top up',
    primary: true,
    testid: 'topup-confirm',
    onClick: async () => {
      const v = input.value.trim().replace(/^\$/, '')
      if (!(Number(v) > 0)) { err.textContent = 'Give an amount in dollars, as 2.50'; err.hidden = false; return false }
      try {
        await post(cardPath(id, 'answer'), { ref: d.ref, option: words.id, words: `$${v}`, against: d.against?.token || '' })
        toast(`Topped up by $${Number(v).toFixed(2)}: run ${r.n} carries on`)
        ctx.refresh(id)
      } catch (e) {
        err.textContent = e.data?.error || e.message
        err.hidden = false
        return false
      }
    }
  }
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); go.el?.click() } })
  openModal({
    title: `Top up for ${r.recipe} · run ${r.n}`,
    testid: 'topup-dialog',
    card: id,
    bodyEl: h('div', { class: 'mbody' },
      h('p', null, 'The session’s budget is spent. Raise it by this much, in dollars, and the run carries on where it paused.'),
      input, err),
    actions: [{ label: 'Cancel', testid: 'topup-cancel' }, go]
  })
  requestAnimationFrame(() => { input.focus(); input.select() })
}

// openPick offers the live steps to open, under (or over) the strip.
function openPick (anchor, steps) {
  closePick()
  const menu = h('div', { class: 'rls-pick', testid: 'run-live-pick', role: 'menu' }, steps.map(st => h('button', {
    type: 'button', role: 'menuitem', testid: `run-live-pick-${st.id}`, onclick: () => { closePick(); openStep(st.card) }
  }, h('b', null, st.id), h('span', { class: 'mono' }, st.path))))
  anchor.parentElement.append(menu)
  pickOpen = true
}

function closePick () {
  document.querySelector('.rls-pick')?.remove()
  pickOpen = false
}

// flashing holds, per card and run, until when a block jumped to flashes:
// a block redrawn meanwhile (its run moved) keeps the flash.
const flashing = new Map()

// jumpToRun scrolls the thread to run n's block and flashes it; unfold
// unfolds a run folded to its line whole first (a spec's list of runs).
// answerInBlock brings run n's block on screen and focuses the first
// answer of the question asked under its row.
function answerInBlock (n) {
  jumpToRun(n, { unfold: true })
  setTimeout(() => document.querySelector(`#thread [data-testid="run-${n}"] [data-testid="run-question-option-0"]`)?.focus({ preventScroll: true }), 350)
}

export function jumpToRun (n, { unfold = false } = {}) {
  if (isMobile() && state.view !== 'thread') set({ view: 'thread' })
  if (unfold && runFolds.get(`${state.sel} ${n}`) !== false) setFold(n, false)
  const go = () => {
    const el = document.querySelector(`#thread [data-testid="run-${n}"]`)
    if (!el) return
    const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches
    el.scrollIntoView({ block: 'start', behavior: reduce ? 'auto' : 'smooth' })
    const key = `${state.sel} ${n}`
    flashing.set(key, Date.now() + 1800)
    el.classList.remove('rb-flash')
    void el.offsetWidth // restart the flash when it is already lit
    el.classList.add('rb-flash')
    setTimeout(() => {
      if ((flashing.get(key) || 0) > Date.now()) return // jumped to again since
      flashing.delete(key)
      document.querySelector(`#thread [data-testid="run-${n}"]`)?.classList.remove('rb-flash')
    }, 1850)
  }
  requestAnimationFrame(go)
}

// ---- the pill: the open run's block is above

let watched = null // the block observed
let observer = null

// watchBlock observes the open run's block where the thread placed it: a
// block redrawn when its run moved is a new node, observed in its turn.
function watchBlock () {
  const pill = $('#run-above')
  const r = stripRun
  const el = r ? document.querySelector(`#thread [data-testid="run-${r.n}"]`) : null
  if (el === watched) return
  observer?.disconnect()
  watched = el
  if (!el) { pill.hidden = true; return }
  observer = new IntersectionObserver((entries) => {
    const e = entries[entries.length - 1]
    if (!stripRun || e.target !== watched) return
    const above = !e.isIntersecting && e.boundingClientRect.bottom <= (e.rootBounds?.top ?? 0) + 1
    const text = `↑ ${stripRun.live.name} is above`
    if (pill.textContent !== text) pill.textContent = text
    pill.hidden = !above
  }, { root: $('#thread') })
  observer.observe(el)
}
