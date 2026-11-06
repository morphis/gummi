// spec.js — the Spec tab: the card's document as it stands on its branch,
// with a sections outline, its review notes shown inline where they are
// anchored, a "Comment" on every section that writes a note into the spec
// (POST …/spec/notes), "Request changes" sending the open notes to their
// writer (POST …/spec/changes), and the gummi-checks block drawn as a table with each
// check's last outcome on this branch.
//
// A session's specs (DESIGN §25.3) are optional, one per piece of work,
// and live with the workspace: the tab is Specs, a list of them with each
// one's state, revision, runs and done-when count (GET …/specs), and the
// one chosen beneath it (GET …/spec?spec=<n>), edited whole (PUT, each save
// a numbered revision listed under it, with the workflow runs that froze
// it) and annotated as any spec. "+ New spec" starts another. A spec lists
// its workflow runs, newest first: a row jumps to the run's block in the
// thread, unfolded whole.

import { h, append, clock, plural, clear, cardName, cr } from './dom.js?v=__ASSET_V__'
import { get, post, put, cardPath, uploadAttachment } from './api.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { changesButton } from './actions.js?v=__ASSET_V__'
import { openMenu } from './views.js?v=__ASSET_V__'
import { jumpToRun } from './workflow.js?v=__ASSET_V__'

export const specTab = {
  name: 'spec',
  label: 'Specs',
  key: 'g s',
  fetch,
  empty: (d) => !d || (!d.list?.specs?.length && (!d.spec || d.spec.none)),
  count: (d) => d?.list?.specs?.length || 0,
  // an agent in a home of its own keeps memory, not a spec; a session may
  // keep several (DESIGN §25.3)
  hidden: (c) => !!c?.home,
  render
}

// chosen is the spec a person picked on each card, by its number; the
// word 'newest' picks the one just started.
const chosen = new Map()

// chooseSpec shows spec n of card id the next time the tab reads.
export function chooseSpec (id, n) { chosen.set(id, n) }

// fetch reads a session's specs and the one shown: the one picked, else
// the one a run is open on, else the newest. A card that keeps no list
// (a retired kind's) reads its one document.
async function fetch (id) {
  let list = null
  try {
    list = await get(cardPath(id, 'specs'))
  } catch (err) {
    if (err.status !== 404) throw err
  }
  if (!list) return { spec: await get(cardPath(id, 'spec')) }
  const specs = list.specs || []
  const want = chosen.get(id)
  const newest = specs.length ? specs[specs.length - 1].n : 0
  let n = want === 'newest' ? newest : specs.find(x => x.n === want)?.n
  if (!n) n = specs.find(x => x.state === 'running')?.n || newest
  if (want === 'newest' && n) chosen.set(id, n)
  const spec = await get(cardPath(id, 'spec') + (n ? `?spec=${n}` : ''))
  return { list, n, spec }
}

// specPath is a route of the spec shown.
function specPath (ctx, rest) { return cardPath(ctx.id, rest) + (ctx.specN ? `?spec=${ctx.specN}` : '') }

// keyOf is what a spec's edits and drafts are kept by: the card and the
// spec, so a draft in one spec never comes back in another.
function keyOf (ctx) { return ctx.specN ? `${ctx.id}#${ctx.specN}` : ctx.id }

const STATE_TIP = {
  draft: 'never run',
  running: 'a run on it is open',
  met: 'its last run passed every checked item',
  partial: 'its last run did not pass every checked item'
}

function render (pane, entry, ctx) {
  const d = entry.data
  // a write answers with the spec it wrote; a session's list moves with
  // it (a revision, a new spec), so the tab reads both again
  if (!d?.list?.specs?.length) {
    renderSpec(pane, { data: d?.spec }, { ...ctx, swap: (spec) => ctx.swap(spec && !d?.list ? { ...d, spec } : null) })
    return
  }
  const sctx = { ...ctx, specN: d.n, swap: () => ctx.swap(null) }
  const main = h('div', { class: 'specs-main' })
  pane.append(h('div', { class: 'specs', testid: 'specs' }, specList(d, ctx), main))
  renderSpec(main, { data: d.spec }, sctx)
}

// specList is the session's specs, the one shown marked.
function specList (d, ctx) {
  const pick = (n) => { chosen.set(ctx.id, n); ctx.swap(null) }
  const items = d.list.specs.map(sp => {
    const last = sp.runs?.length ? sp.runs[sp.runs.length - 1] : null
    const meta = [
      `rev ${sp.rev}${sp.frozen ? ` · frozen at ${sp.frozen}` : ''}`,
      last ? `${last.recipe} · run ${last.n}` : 'no run yet',
      sp.doneWhen ? `DW ${sp.met}/${sp.doneWhen}` : 'no done-when yet'
    ]
    return h('button', {
      class: ['specs-it', sp.n === d.n && 'sel'], type: 'button', testid: `specs-item-${sp.n}`,
      'aria-current': sp.n === d.n ? 'true' : null, onclick: () => { if (sp.n !== d.n) pick(sp.n) }
    },
    h('b', null, sp.title),
    h('span', { class: 'm' }, h('span', { class: ['sst', `sst-${sp.state}`], testid: `specs-state-${sp.n}`, title: STATE_TIP[sp.state] || '' }, sp.state), meta.join(' · ')))
  })
  return h('nav', { class: 'specs-list', testid: 'specs-list', 'aria-label': 'The session’s specs' }, items,
    d.list.canStart && !ctx.closed ? newSpec(ctx) : null)
}

// newSpecs is the "+ New spec" form as a person left it, per card: the
// list is drawn again on every change to the session, and an open form
// with a title half-typed must come back from that as it was.
const newSpecs = new Map() // card id -> { open, title }

// newSpec is the list's last row: a title, and the spec is started from
// the template as its first revision.
function newSpec (ctx) {
  const st = newSpecs.get(ctx.id) || { open: false, title: '' }
  const form = h('form', { class: 'specs-new', testid: 'specs-new-form', hidden: !st.open, data: { draft: 'specs-new' } })
  const input = h('input', { type: 'text', testid: 'specs-new-title', placeholder: 'What the spec is about', 'aria-label': 'The new spec’s title', value: st.title })
  input.addEventListener('input', () => newSpecs.set(ctx.id, { open: true, title: input.value }))
  input.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); newSpecs.delete(ctx.id); ctx.rerender() } })
  form.append(input, h('button', { class: 'btn pri', type: 'submit', testid: 'specs-new-start' }, 'Start'))
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    const title = input.value.trim()
    if (!title) { input.focus(); return }
    const btn = form.querySelector('button')
    btn.disabled = true
    try {
      await post(cardPath(ctx.id, 'spec/start'), { title })
      newSpecs.delete(ctx.id)
      chosen.set(ctx.id, 'newest')
      toast(`Spec “${title}” started from the template`)
      ctx.swap(null)
    } catch (err) {
      btn.disabled = false
      toast(err.data?.error || err.message, { err: true })
    }
  })
  const open = h('button', {
    class: 'specs-add', type: 'button', testid: 'specs-new', hidden: st.open,
    onclick: () => { newSpecs.set(ctx.id, { open: true, title: '' }); open.hidden = true; form.hidden = false; input.focus() }
  }, '+ New spec')
  return [open, form]
}

// noteDrafts are the notes being written, per card and spec: section line
// → the draft's text and attachments. The panel redraws the tab whenever
// the card changes (a running card changes often), and a half-written
// note must come back from that redraw as it was.
const noteDrafts = new Map() // keyOf -> Map(line -> { text, attachments })
function draftsOf (id) {
  if (!noteDrafts.has(id)) noteDrafts.set(id, new Map())
  return noteDrafts.get(id)
}

const slug = (name, i) => `sec-${i}-` + String(name).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

// renderSpec draws one spec, or the card's one document.
function renderSpec (pane, entry, ctx) {
  const s = entry.data
  if (s?.session) {
    const ed = editing.get(keyOf(ctx))
    if (s.none) {
      pane.append(h('div', { class: 'empty', testid: 'spec-none' }, h('b', null, 'No spec'), s.why || '',
        ctx.closed ? null : h('button', { class: 'btn pri', type: 'button', testid: 'spec-start-session', onclick: (e) => startSpec(e.currentTarget, ctx) }, 'Start a spec')))
      return
    }
    if (ed && !ctx.closed) { pane.append(editor(s, ed, ctx)); return }
  }
  if (!s || s.none) {
    pane.append(h('div', { class: 'empty', testid: 'spec-none' }, h('b', null, 'No spec'),
      s?.why || (ctx.card?.stage === 'open' ? 'A session carries no document. Its conversation is the context.' : 'The plan stage writes it onto the card’s branch.')))
    return
  }
  const lines = String(s.markdown || '').replace(/\r\n?/g, '\n').split('\n')
  const sections = (s.sections || []).slice().sort((a, b) => a.line - b.line)
  // a note sits in the section holding the line it comments on (its
  // anchor); a note on the whole document (anchor 0) sits at the top
  const notes = foldResolutions((s.notes || []).map(n => ({ ...n, at: n.anchor || n.line })).sort((a, b) => a.at - b.at))
  const checksBlock = () => checksTable(s.checks || [])
  let checksDrawn = false
  const opts = {
    fence: (lang) => {
      if (lang !== 'gummi-checks') return null
      checksDrawn = true
      return checksBlock()
    }
  }
  // the preamble before the first section, without the title heading
  const firstLine = sections.length ? sections[0].line : lines.length + 1
  const pre = lines.slice(0, firstLine - 1).filter(l => !/^#\s/.test(l.trim()) || l.replace(/^#\s+/, '').trim() !== s.title)
  const doc = h('article', { class: 'doc', testid: 'spec-doc' },
    h('div', { class: 'src', testid: 'spec-src' }, h('span', { class: 'at', title: s.path || null }, [s.path, s.rev ? `@ ${String(s.rev).slice(0, 7)}` : null].filter(Boolean).join(' ')),
      s.draft ? h('span', { class: 'draftflag' }, ' · draft, not on the branch yet') : null),
    h('h1', { testid: 'spec-title' }, s.title || ctx.card?.title || ''),
    markdown(pre.join('\n'), opts),
    ctx.closed ? h('p', { class: 'ro', testid: 'spec-closed' }, `${cardName(ctx.id)} ${ctx.card?.landed ? 'has landed' : 'is done'}: its spec reads as it ended, and takes no more notes.`) : null,
    notes.filter(n => !n.anchor).map(n => noteEl(n, ctx)),
    notesIn(notes.filter(n => n.anchor), 1, firstLine - 1, ctx))
  const headings = new Map() // section line -> its h2, for reopening drafts
  const empty = new Set() // the sections nothing is written under yet
  sections.forEach((sec, i) => {
    const end = i + 1 < sections.length ? sections[i + 1].line - 1 : lines.length
    const body = lines.slice(sec.line, end).join('\n')
    if (!body.trim()) empty.add(i)
    // a landed card's spec is the record of what was built: no new notes
    const heading = h('h2', { class: 'sec', id: slug(sec.name, i), testid: `spec-section-${i}` }, sec.name,
      ctx.closed ? null : h('button', { class: 'sc', type: 'button', testid: `spec-comment-${i}`, 'aria-label': `Comment on ${sec.name}`, onclick: (e) => openNote(e.currentTarget.closest('h2'), sec, ctx) }, 'Comment'))
    headings.set(sec.line, { heading, sec })
    append(doc, [heading, markdown(body, { ...opts, headingBase: 3 }), notesIn(notes, sec.line, end, ctx)])
  })
  // the notes being written come back where they were, unfocused (the
  // panel puts the caret back in the one that had it)
  for (const [line, dr] of draftsOf(keyOf(ctx))) {
    const at = headings.get(line)
    if (at && !ctx.closed) openNote(at.heading, at.sec, ctx, dr)
  }
  if (!checksDrawn && s.checks?.length) doc.append(h('h2', { class: 'sec' }, 'Checks'), checksBlock())
  if (s.openComments && ctx.closed) {
    pane.append(h('div', { class: 'pending' }, h('span', { testid: 'spec-pending' },
      `${plural(s.openComments, 'comment')} ${s.openComments === 1 ? 'was' : 'were'} still open when it closed.`)))
  } else if (s.openComments) {
    // the TUI's R: the open notes go now, not on the next pass — to the
    // stage writing what they are about, which may mean sending the card
    // back (asked first)
    pane.append(h('div', { class: 'pending' },
      h('span', { testid: 'spec-pending' }, `${plural(s.openComments, 'comment')} ${s.openComments === 1 ? 'is' : 'are'} still open here.`),
      changesButton(ctx.id, 'spec', [ctx.card?.stage, s.rev, s.openComments].join('|'),
        'Send the open comments to the stage that writes what they are about')))
  }
  // the outline is a menu on the line that says which file this is, which
  // stays in view: the document keeps the surface's whole width, a phone
  // has the outline too, and a section nothing is written under says so
  if (sections.length) {
    doc.querySelector('.src').append(h('button', {
      class: 'toc', type: 'button', testid: 'spec-toc', 'aria-haspopup': 'menu',
      onclick: (e) => openMenu(e.currentTarget, sections.map((sec, i) => ({
        label: sec.name,
        count: empty.has(i) ? 'empty' : undefined,
        testid: `spec-toc-${i}`,
        onClick: () => document.getElementById(slug(sec.name, i))?.scrollIntoView({ block: 'start' })
      })), { testid: 'spec-toc-menu' })
    }, 'Sections ', h('span', { 'aria-hidden': 'true' }, '▾')))
  }
  if (s.session) {
    // a spec with no runs has no runs row: after() would write a null as text
    doc.querySelector('.src').after(...[sessionBar(s, ctx), specRunsEl(s)].filter(Boolean))
    doc.append(revisionsEl(s))
  }
  pane.append(h('div', { class: 'spec', testid: 'spec' }, doc))
}

// ---- a session's spec ----

// editing holds a session spec being edited, per card: the text and the
// revision it was opened at. The panel redraws the tab on every change to
// the card, and a half-made edit must come back from that as it was.
const editing = new Map()

function sessionBar (s, ctx) {
  const items = s.doneWhen || []
  const checked = items.filter(i => i.check || i.judge).length
  return h('div', { class: 'spec-sbar', testid: 'spec-session' },
    h('span', { class: 'mono', testid: 'spec-rev' }, s.revisions?.length ? `revision ${s.revisions[0].rev}` : 'no revision yet'),
    h('span', { class: items.length ? 'okc' : 'nonec', testid: 'spec-ready' }, items.length
      ? `${plural(items.length, 'done-when item')}${checked < items.length ? `, ${items.length - checked} without a check` : ''}`
      : 'no done-when item yet: a workflow run is judged against them'),
    h('span', { class: 'sp' }),
    ctx.closed || !ctx.specN || (ctx.card?.workflows && !ctx.card.workflows.on) ? null : h('button', { class: 'btn', type: 'button', testid: 'spec-propose-run', title: 'Ask the session to propose a workflow run on this spec: it brings the spec up to date and proposes, and you allow it', onclick: (e) => askForRun(e.currentTarget, ctx) }, 'Propose a run'),
    ctx.closed ? null : h('button', { class: 'btn', type: 'button', testid: 'spec-edit', onclick: () => { editing.set(keyOf(ctx), { text: s.markdown || '', rev: s.rev }); ctx.rerender() } }, 'Edit'))
}

// askForRun sends the session the /workflow line for this spec (the
// TUI's w in its spec view): the agent brings the spec up to date and
// proposes the run, which comes back as a decision for the person.
async function askForRun (btn, ctx) {
  btn.disabled = true
  try {
    await post(cardPath(ctx.id, 'send'), { text: `/workflow on spec ${ctx.specN}` })
    toast(`Asked the session to propose a run on spec ${ctx.specN}`)
  } catch (err) {
    toast(err.data?.error || err.message, { err: true })
  } finally {
    btn.disabled = false
  }
}

// specRunsEl is the spec's runs, newest first (DESIGN §25.9): each run's
// glyph, number and recipe, its state, its checked items met, its spend and
// when it ended. A row jumps to the run's block in the thread, unfolded.
function specRunsEl (s) {
  const runs = s.runs || []
  if (!runs.length) return null
  return h('nav', { class: 'spec-runs', testid: 'spec-runs', 'aria-label': 'This spec’s workflow runs' },
    h('span', { class: 'k' }, 'runs'),
    runs.map(r => h('button', {
      class: ['spec-run', `s-${r.state}`], type: 'button', testid: `spec-run-${r.n}`,
      title: `Jump to run ${r.n} in the thread`, onclick: () => jumpToRun(r.n, { unfold: true })
    },
    h('span', { class: ['rb-mark', `s-${r.state}`], 'aria-hidden': 'true' }, r.mark || '·'),
    h('b', null, `run ${r.n}`),
    h('span', null, r.recipe),
    h('span', { class: 'st' }, r.state),
    r.checked ? h('span', { testid: 'spec-run-met' }, `met ${r.met || 0}/${r.checked}`) : null,
    h('span', { class: 'mono' }, cr(r.spent)),
    r.ended ? h('span', { class: 'mono' }, `ended ${clock(r.ended)}`) : null)))
}

function revisionsEl (s) {
  const revs = s.revisions || []
  if (!revs.length) return null
  return h('section', { class: 'spec-revs', testid: 'spec-revisions' }, h('h2', { class: 'sec' }, 'Revisions'),
    revs.map(r => h('div', { class: 'spec-revrow', testid: `spec-revision-${r.rev}` },
      h('b', { class: 'mono' }, `rev ${r.rev}`),
      h('span', null, r.by || 'you'),
      h('span', { class: 'mono' }, r.at ? clock(r.at) : ''),
      (r.runs || []).length ? h('span', { class: 'spec-frozen' }, `frozen for run ${r.runs.join(', ')}`) : null)))
}

function editor (s, ed, ctx) {
  const ta = h('textarea', { class: 'spec-ta', testid: 'spec-editor', 'aria-label': 'The spec, as markdown', spellcheck: 'true' })
  ta.value = ed.text
  ta.addEventListener('input', () => { ed.text = ta.value })
  const stale = ed.rev !== s.rev
  const save = async (btn) => {
    btn.disabled = true
    try {
      const spec = await put(specPath(ctx, 'spec'), { markdown: ta.value, rev: ed.rev })
      editing.delete(keyOf(ctx))
      toast(`Saved as revision ${spec?.revisions?.[0]?.rev ?? ''}`.trim())
      ctx.swap(spec && spec.markdown !== undefined ? spec : null)
    } catch (err) {
      btn.disabled = false
      toast(err.status === 409 ? 'The spec changed since you opened it: cancel, and edit it as it stands now' : (err.data?.error || err.message), { err: true })
    }
  }
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); save(box.querySelector('[data-testid="spec-save"]')) }
    if (e.key === 'Escape') { e.stopPropagation(); editing.delete(keyOf(ctx)); ctx.rerender() }
  })
  const box = h('div', { class: 'spec spec-editing', testid: 'spec', data: { draft: 'spec-edit' } },
    h('div', { class: 'spec-sbar' },
      h('b', null, 'Editing'),
      h('span', { class: 'mono' }, `from revision ${s.revisions?.[0]?.rev ?? ''}`),
      stale ? h('span', { class: 'badc', testid: 'spec-stale' }, 'the spec changed since you opened it') : null,
      h('span', { class: 'sp' }),
      h('button', { class: 'btn', type: 'button', testid: 'spec-cancel', onclick: () => { editing.delete(keyOf(ctx)); ctx.rerender() } }, 'Cancel'),
      h('button', { class: 'btn pri', type: 'button', testid: 'spec-save', onclick: (e) => save(e.currentTarget) }, 'Save')),
    ta)
  return box
}

async function startSpec (btn, ctx) {
  btn.disabled = true
  try {
    const spec = await post(cardPath(ctx.id, 'spec/start'), { title: ctx.card?.title || '' })
    chosen.set(ctx.id, 'newest')
    toast('Spec started from the template')
    ctx.swap(spec && spec.markdown !== undefined ? spec : null)
  } catch (err) {
    btn.disabled = false
    toast(err.data?.error || err.message, { err: true })
  }
}

function notesIn (notes, from, to, ctx) {
  return notes.filter(n => n.anchor && n.at >= from && n.at <= to).map(n => noteEl(n, ctx))
}

// A resolution is written the terminal board's way: a `%% @who: resolved
// — why` line under the thread it closes (internal/spec). In the file that
// is a note of its own; on the page it is the thread's resolved state, so
// it is folded onto the note just above it in the same thread instead of
// reading as a second, new note.
const RESOLVED = /^resolved\s*(?:$|[:—–]|-(?:\s|$))/i
function foldResolutions (notes) {
  const out = []
  for (const n of notes) {
    const prev = out[out.length - 1]
    if (prev && !prev.resolution && prev.anchor === n.anchor && prev.author !== 'gummi' && RESOLVED.test(String(n.text || '').trim())) {
      prev.resolution = n
      continue
    }
    out.push(n)
  }
  return out
}

// noteEl draws one %% note. gummi's own notes are the prompts a template
// leaves for the agents; they read quieter than a person's.
function noteEl (n, ctx) {
  const prompt = n.author === 'gummi'
  // a template's prompt is a hint for whoever fills the section in, not a
  // note anyone wrote: it is shown as one, without the %% marker's syntax
  if (prompt && !n.resolution) return h('div', { class: 'note prompt hint', testid: 'spec-prompt' }, h('span', { class: 'tx' }, n.text))
  const r = n.resolution
  const why = r ? String(r.text || '').trim().replace(RESOLVED, '').trim() : ''
  return h('div', { class: ['note', n.resolved && 'resolved', prompt && 'prompt'], testid: prompt ? 'spec-prompt' : 'spec-note' },
    n.resolved ? h('span', { class: 'rs' }, 'resolved')
      : prompt || ctx.closed ? null
        : h('button', { class: 'rs', type: 'button', testid: 'spec-note-resolve', onclick: () => resolve(n, ctx) }, 'Resolve'),
    h('b', null, `%% @${n.by || n.author}${n.date ? ` (${n.date})` : ''}`),
    h('span', { class: 'tx' }, n.text),
    // The parser (internal/spec) is the one truth about open and closed:
    // an agent's "resolved" under a person's comment is its answer, and
    // only a person's resolution closes a person's comment. So the fold
    // says "answered" while the note is still open — the gate counts it
    // open, and the live Resolve button beside it is the way to close it.
    r ? h('span', { class: 'rsby', testid: 'spec-note-resolution' },
      `${n.resolved ? 'resolved' : 'answered'} by @${r.by || r.author}${r.date ? ` (${r.date})` : ''}${why ? ` — ${why}` : ''}${n.resolved ? '' : ' · still open until you resolve it'}`) : null)
}

function checksTable (checks) {
  const last = checks.map(c => c.last?.at).filter(Boolean).sort().pop()
  return h('div', { class: 'gchecks', testid: 'spec-checks' },
    h('div', { class: 'h' }, h('span', null, 'gummi-checks'), h('span', null, last ? `last verify ${clock(last)}` : 'not run yet')),
    checks.map(c => h('div', { class: 'r', testid: `spec-check-${c.name}` },
      h('span', { class: c.last ? (c.last.ok ? 'okc' : 'badc') : 'nonec', 'aria-label': c.last ? (c.last.ok ? 'passed' : 'failed') : 'not run' }, c.last ? (c.last.ok ? '✓' : '✕') : '·'),
      h('span', null, c.name),
      h('span', { class: 'c', title: c.cmd }, c.cmd),
      h('span', { class: 'at' }, c.excused ? h('span', { class: 'excused', title: c.excusedOn ? `Already failing on ${c.excusedOn.slice(0, 7)}, the commit this card forks from — re-measured when its base moves` : 'Already failing when the branch was cut' }, c.excusedOn ? `excused on ${c.excusedOn.slice(0, 7)}` : 'excused') : c.last ? clock(c.last.at) : ''))))
}

// openNote opens the note box under a section's heading; saved is a
// draft the tab is putting back after a redraw (not focused: the panel
// gives the caret back to whichever box had it).
function openNote (h2, sec, ctx, saved = null) {
  const next = h2.nextElementSibling
  if (next?.classList.contains('draft')) { next.querySelector('textarea')?.focus(); return }
  const drafts = draftsOf(keyOf(ctx))
  // attachments are stored by reference (internal/attachment.Link), so —
  // unlike the composer's turn — every backend can take one: the note
  // form always offers the control, never gated on Composer.Images.
  const dr = saved || { text: '', attachments: [] }
  drafts.set(sec.line, dr)
  const drop = () => { drafts.delete(sec.line); box.remove() }
  // a %% note is one line of the spec: line breaks are written as spaces,
  // which the box says rather than doing it silently
  const ta = h('textarea', { testid: 'spec-note-input', 'aria-label': `Note on ${sec.name}`, 'aria-describedby': `note-hint-${sec.line}`, placeholder: 'Written into the spec as a %% note under your name. The architect answers it on the next pass.' })
  ta.value = dr.text
  ta.addEventListener('input', () => { dr.text = ta.value })
  const chips = h('div', { class: 'chips', testid: 'spec-note-chips', hidden: true })
  const file = h('input', {
    type: 'file', accept: 'image/png,image/jpeg,image/gif,image/webp,image/svg+xml', multiple: true, hidden: true,
    onchange: async (e) => {
      const files = [...e.target.files]
      e.target.value = ''
      for (const f of files) {
        const chip = { id: null, name: f.name || 'image', pending: true, error: null }
        dr.attachments.push(chip)
        renderChips()
        try {
          Object.assign(chip, await uploadAttachment(f), { pending: false })
        } catch (err) {
          chip.pending = false
          chip.error = (err.data && err.data.error) || err.message || 'upload failed'
        }
        renderChips()
      }
    }
  })
  const renderChips = () => {
    clear(chips)
    chips.hidden = dr.attachments.length === 0
    for (const a of dr.attachments) {
      chips.append(h('span', { class: ['chip', a.error && 'err', a.pending && 'pending'] },
        a.pending ? 'Uploading…' : (a.error || a.name),
        h('button', { type: 'button', title: 'Remove', onclick: () => { dr.attachments = dr.attachments.filter((x) => x !== a); renderChips() } }, '×')))
    }
  }
  const box = h('div', { class: 'annot draft', testid: 'spec-note-draft', data: { draft: `note:${sec.line}` } },
    h('div', { class: 'a1' }, h('span', { class: 'who' }, h('b', null, ctx.person || 'you'), ` on ${sec.name}`), ta,
      h('span', { class: 'hint', id: `note-hint-${sec.line}`, testid: 'spec-note-hint' }, 'A note is one line of the spec: line breaks become spaces.'), chips),
    h('div', { class: 'act' },
      h('button', { class: 'attach', type: 'button', testid: 'spec-note-attach', title: 'Attach an image', onclick: () => file.click() }, '📎'),
      file,
      h('button', { class: 'btn', type: 'button', onclick: drop }, 'Cancel'),
      h('button', {
        class: 'btn pri',
        type: 'button',
        testid: 'spec-note-save',
        onclick: async (e) => {
          const text = ta.value.replace(/\s*\n\s*/g, ' ').trim()
          if (!text) { ta.focus(); return }
          if (dr.attachments.some((a) => a.pending)) { toast('Still uploading an image — wait a moment and try again'); return }
          if (dr.attachments.some((a) => a.error)) { toast('Remove the failed attachment before saving', { err: true }); return }
          const btn = e.currentTarget
          btn.disabled = true
          try {
            const body = { line: sec.line, text }
            if (dr.attachments.length) body.attachments = dr.attachments.map((a) => a.id)
            const spec = await post(specPath(ctx, 'spec/notes'), body)
            drafts.delete(sec.line)
            toast('Note written into the spec')
            ctx.swap(spec && spec.markdown !== undefined ? spec : null)
          } catch (err) {
            btn.disabled = false
            toast(err.notBuilt ? 'Spec notes from the web are not available yet' : err.message, { err: !err.notBuilt })
          }
        }
      }, 'Add note')))
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); drop() }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); box.querySelector('[data-testid="spec-note-save"]').click() }
  })
  renderChips()
  h2.after(box)
  if (!saved) ta.focus()
}

async function resolve (n, ctx) {
  try {
    await post(specPath(ctx, 'spec/notes/resolve'), { line: n.line, author: n.author, date: n.date })
    toast('Note resolved')
    ctx.swap(null)
  } catch (err) {
    if (err.status === 409) { toast('The spec changed since you read it; here it is as it stands now'); ctx.swap(null); return }
    toast(err.notBuilt ? 'Resolving notes from the web is not available yet' : err.message, { err: !err.notBuilt })
  }
}

