// diff.js — the Diff tab: the card's branch against its base, file by file,
// with the review comments anchored in it. Clicking a line number opens a
// comment box (POST …/diff/annotations with the line's raw-diff index);
// comments can be edited, resolved or deleted, and "Request changes" sends the open
// ones to the implementer (POST …/diff/changes) — asking first when that
// sends the card back. "Viewed" ticks are kept per card in
// this browser. When the branch moves while someone reads, the new diff is
// announced with a banner, never swapped in under the reader.

import { h, plural, storage, isMobile } from './dom.js?v=__ASSET_V__'
import { get, post, del, patch, cardPath } from './api.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { state } from './store.js?v=__ASSET_V__'
import { changesButton } from './actions.js?v=__ASSET_V__'

// "Since you last read it": the branch head this browser showed on the
// previous visit to a card's diff is the baseline, and the toggle asks the
// server to mark what changed after it (?since=). The baseline is taken
// once per card per page load, so reading the diff does not move it.
const baseline = new Map() // card id -> rev read on the previous visit
const sinceOn = new Set() // cards showing only what changed since

function noteRead (id, rev) {
  if (!rev) return
  if (!baseline.has(id)) {
    const prev = storage.get(`readRev:${id}`, null)
    baseline.set(id, prev && prev !== rev ? prev : null)
  }
  storage.set(`readRev:${id}`, rev)
}

export const diffTab = {
  name: 'diff',
  label: 'Diff',
  key: 'g d',
  fetch: (id) => {
    const since = sinceOn.has(id) ? baseline.get(id) : null
    return get(cardPath(id, 'diff') + (since ? `?since=${encodeURIComponent(since)}` : ''))
  },
  empty: (d) => !d || !d.files?.length,
  count: (d) => d?.files?.length || 0,
  // hold keeps the diff a person is reading when the branch moved: the
  // panel shows a banner and swaps only when asked
  hold: (old, fresh) => !!old && !!old.files?.length && !!fresh && old.rev !== fresh.rev,
  render
}

// A big diff is not drawn whole: every line is a row of spans, and 6000
// changed lines made ~37k nodes and two seconds of layout before a reader
// saw anything. A file longer than BIG_FILE lines, or any file of more than
// SMALL_FILE lines once BUDGET lines are already on screen, starts folded
// behind a "Show N lines" button. A file with an open comment is always
// drawn (the comment is why someone is here), and one a reader unfolded
// stays unfolded for the page's life.
const BIG_FILE = 400
const SMALL_FILE = 40
const BUDGET = 1500
const unfolded = new Map() // card id -> Set of paths a reader unfolded

function lineCount (f) { return (f.hunks || []).reduce((n, hk) => n + (hk.lines?.length || 0), 0) }

// wrapOn says whether the diff's long lines wrap: what a person last
// chose, else only where the screen is a phone's.
const wrapOn = () => storage.get('diffWrap', null) ?? isMobile()

const viewedKey = (id) => `viewed:${id}`
function viewed (id) { return new Set(storage.get(viewedKey(id), [])) }
function setViewed (id, set) { storage.set(viewedKey(id), [...set]) }

// Comments being written survive the tab's redraws (the panel redraws it
// on every change to the card): new ones by the raw line they are on,
// with that line's text so a diff that moved under one does not put it
// on another line; edits by the comment they change.
const drafts = new Map() // card id -> Map(idx -> { text, line, where })
const edits = new Map() // card id -> Map(annotation id -> text)
function draftsOf (id) { if (!drafts.has(id)) drafts.set(id, new Map()); return drafts.get(id) }
function editsOf (id) { if (!edits.has(id)) edits.set(id, new Map()); return edits.get(id) }

// want is a place another tab asked the diff to show (a PR thread's
// "show in diff"): the file, and the new-side line in it when known.
let want = null
let lit = null // the place last shown, marked until a moment has passed
export function reveal (id, path, line) { want = { id, path, line: line || 0 } }

function render (pane, entry, ctx) {
  pane.classList.toggle('wrap', wrapOn())
  const d = entry.data
  const anns = d?.annotations || []
  const paths = new Set((d?.files || []).map(f => f.path))
  // a comment no line of this diff carries, on a file the diff does not
  // show — a PR thread pulled in on a file the branch never touched, or a
  // comment on a file since deleted or renamed. An open one still holds
  // the gate, so it is drawn above the files where it can be resolved:
  // an orphaned anchor degrades, it never vanishes
  const loose = anns.filter(a => a.idx < 0 && !paths.has(a.file))
  if (!d || !d.files?.length) {
    pane.append(h('div', { class: 'empty', testid: 'diff-none' }, h('b', null, 'No code yet'),
      d?.why || (ctx.card?.stage === 'plan' || ctx.card?.stage === 'todo'
        ? 'The plan stage only writes the spec. Code arrives with implement.'
        : 'This card has no changes against its base.')))
    if (d?.pendingComments) pane.append(pendingBar(d, anns, ctx))
    if (loose.length) pane.append(looseBox(loose, ctx))
    if (want?.id === ctx.id) { toast(`${want.path} is not in this diff`); want = null }
    return
  }
  noteRead(ctx.id, d.rev)
  const since = baseline.get(ctx.id)
  const onlySince = sinceOn.has(ctx.id) && !!d.since
  const files = onlySince ? d.files.filter(f => f.since) : d.files
  const seen = viewed(ctx.id)
  // the totals are of what is listed: all of it, or only what is new
  const add = files.reduce((a, f) => a + (f.add || 0), 0)
  const rem = files.reduce((a, f) => a + (f.del || 0), 0)
  // a file is marked as commented when a comment is drawn in it: one on
  // one of its lines (whatever path the comment was stored under — a
  // deleted file's is /dev/null), or an orphan naming it
  const fileOf = new Map()
  for (const f of d.files) for (const hk of f.hunks || []) for (const ln of hk.lines || []) fileOf.set(ln.idx, f.path)
  const noted = new Set(anns.filter(a => !a.resolved).map(a => a.idx >= 0 ? fileOf.get(a.idx) : a.file).filter(Boolean))
  ctx.placed = new Set() // drafts put back on their lines in this draw

  // the head, the branch-moved banner and the open comments' bar stay in
  // view as the diff scrolls: they are what a reader acts on from anywhere
  const top = h('div', { class: 'dtop' })
  pane.append(top)
  top.append(h('div', { class: 'diffhead', testid: 'diff-head' },
    h('span', null, 'against ', h('span', { class: 'mono' }, d.base || '—')),
    h('span', null, 'at ', h('span', { class: 'mono', testid: 'diff-rev' }, String(d.rev || '').slice(0, 7))),
    h('span', { class: 'add mono', testid: 'diff-add' }, `+${add}`), h('span', { class: 'del mono', testid: 'diff-del' }, `−${rem}`),
    since ? h('span', { class: 'seg', role: 'group', 'aria-label': 'Which changes', testid: 'diff-since' },
      h('button', { type: 'button', class: !sinceOn.has(ctx.id) && 'on', testid: 'diff-since-all', 'aria-pressed': String(!sinceOn.has(ctx.id)), onclick: () => { sinceOn.delete(ctx.id); ctx.swap(null) } }, 'All changes'),
      h('button', { type: 'button', class: sinceOn.has(ctx.id) && 'on', testid: 'diff-since-new', 'aria-pressed': String(sinceOn.has(ctx.id)), onclick: () => { sinceOn.add(ctx.id); ctx.swap(null) } }, `Since ${since.slice(0, 7)}`))
      : null,
    // long lines wrap instead of scrolling each hunk sideways: a phone's
    // default, and a person's choice anywhere
    h('button', { type: 'button', class: ['wraptgl', wrapOn() && 'on'], testid: 'diff-wrap', 'aria-pressed': String(wrapOn()), title: 'Wrap long lines', onclick: () => { storage.set('diffWrap', !wrapOn()); ctx.rerender() } }, 'Wrap')))

  if (entry.fresh) {
    top.append(h('div', { class: 'fresh', testid: 'diff-fresh', role: 'status' },
      h('span', { class: 'spinner' }),
      h('span', null, `The branch moved to ${String(entry.fresh.rev || '').slice(0, 7)} since you opened this diff.`),
      h('button', { class: 'btn', type: 'button', testid: 'diff-fresh-show', onclick: () => ctx.swap(entry.fresh) }, 'Show it')))
  }
  if (d.pendingComments) top.append(pendingBar(d, anns, ctx))
  if (loose.length) pane.append(looseBox(loose, ctx))

  if (onlySince && !files.length) pane.append(h('div', { class: 'empty' }, h('b', null, 'Nothing new'), `No file changed after ${since.slice(0, 7)}.`))
  pane.append(h('div', { class: 'files', testid: 'diff-files' }, files.map((f) => ({ f, i: d.files.indexOf(f) })).map(({ f, i }) => h('button', {
    class: seen.has(f.path) && 'viewed', type: 'button', testid: `diff-file-${i}`, title: f.path,
    onclick: () => {
      pane.querySelector(`[data-testid="diff-unfold-${i}"]`)?.click()
      pane.querySelector(`#f-${i}`)?.scrollIntoView({ block: 'start' })
    }
  },
  h('span', { class: 'p' }, noted.has(f.path) ? h('span', { class: 'dot', 'aria-label': 'has comments' }, '● ') : null, seen.has(f.path) ? '✓ ' : '', f.path,
    f.since && d.since ? h('span', { class: 'since' }, 'new') : null),
  h('span', null, h('span', { class: 'add' }, `+${f.add}`), ' ', h('span', { class: 'del' }, `−${f.del}`))))))

  if (!unfolded.has(ctx.id)) unfolded.set(ctx.id, new Set())
  const open = unfolded.get(ctx.id)
  // a place asked for is drawn, so there is something to scroll to
  const goal = want?.id === ctx.id ? want : null
  if (goal && files.some(f => f.path === goal.path)) open.add(goal.path)
  let drawn = 0
  files.forEach((f) => {
    const i = d.files.indexOf(f)
    const isViewed = seen.has(f.path)
    const n = lineCount(f)
    // a viewed file's lines are hidden (app.css), so they are not drawn
    // either; unticking it redraws the tab
    const folded = !isViewed && !noted.has(f.path) && !open.has(f.path) &&
      (n > BIG_FILE || (n > SMALL_FILE && drawn + n > BUDGET))
    if (!isViewed && !folded) drawn += n
    const hunks = () => (f.hunks || []).map(hk => h('div', { class: 'hunk' },
      h('div', { class: 'hh' }, hk.header),
      (hk.lines || []).map(ln => lineEl(ln, anns, ctx))))
    const fold = folded
      ? h('button', {
        class: 'fold', type: 'button', testid: `diff-unfold-${i}`,
        onclick: (e) => { open.add(f.path); e.currentTarget.replaceWith(...hunks()) }
      }, `Show ${plural(n, 'line')}`, h('span', null, n > BIG_FILE ? ' · a large file, folded to keep the diff quick' : ' · folded to keep the diff quick'))
      : null
    const box = h('input', {
      type: 'checkbox', checked: isViewed, testid: `diff-viewed-${i}`,
      onchange: (e) => {
        const s = viewed(ctx.id)
        if (e.target.checked) s.add(f.path); else s.delete(f.path)
        setViewed(ctx.id, s)
        ctx.rerender()
      }
    })
    const orphans = anns.filter(a => a.file === f.path && a.idx < 0)
    pane.append(h('section', { class: ['file', isViewed && 'viewed'], id: `f-${i}`, testid: `diff-filebox-${i}`, 'aria-label': f.path, data: { path: f.path } },
      h('div', { class: 'fh' }, h('span', { class: 'p', title: f.oldPath ? `${f.oldPath} → ${f.path}` : f.path }, f.oldPath ? `${f.oldPath} → ${f.path}` : f.path),
        f.status && f.status !== 'modified' ? h('span', { class: 'fstatus' }, f.status) : null,
        h('span', { class: 'add' }, `+${f.add}`), h('span', { class: 'del' }, `−${f.del}`),
        h('label', null, box, 'Viewed')),
      f.binary ? h('div', { class: 'binary' }, 'Binary file; not shown.') : null,
      orphans.length ? h('div', { class: 'orphans', testid: 'diff-orphans' }, annotBox(orphans, ctx, 'orphan')) : null,
      isViewed ? null : fold || hunks()))
  })
  // a comment being written on a line this diff no longer has keeps its
  // words, said to be adrift rather than dropped
  const adrift = [...draftsOf(ctx.id)].filter(([idx]) => !ctx.placed.has(idx))
  if (adrift.length && !ctx.closed) pane.querySelector('.files')?.before(...adrift.map(([idx, dr]) => adriftBox(idx, dr, ctx)))
  pane.append(h('div', { class: 'foot-space' }))
  const place = (p) => {
    const sect = [...pane.querySelectorAll('section.file')].find(el => el.dataset.path === p.path)
    return sect && ((p.line && sect.querySelector(`.ln[data-new="${p.line}"]`)) || sect)
  }
  if (goal) {
    want = null
    const row = place(goal)
    if (!row) { toast(`${goal.path} is not in this diff`); return }
    lit = { ...goal, until: Date.now() + 1600 }
    // after the panel has put its scroll back, or that would undo this
    requestAnimationFrame(() => row.scrollIntoView({ block: row.matches('.ln') ? 'center' : 'start' }))
  }
  // the place shown stays marked a moment, through any redraw meanwhile
  if (lit?.id === ctx.id && Date.now() < lit.until) {
    const row = place(lit)
    row?.classList.add('flash')
    setTimeout(() => row?.classList.remove('flash'), lit.until - Date.now())
  }
}

// pendingBar says how many comments are open and offers to send them — on
// a card still in the workflow. A finished one only says they were left.
function pendingBar (d, anns, ctx) {
  const n = d.pendingComments
  if (ctx.closed) {
    return h('div', { class: 'pending' }, h('span', { testid: 'diff-pending' },
      `${plural(n, 'comment')} ${n === 1 ? 'was' : 'were'} still open when it closed.`))
  }
  // they go with an answer only when the open decision has one that
  // carries them (a send-back); otherwise they just wait on the diff
  const carried = state.sel === ctx.id && state.card?.decision?.options?.some(o => o.carriesComments)
  return h('div', { class: 'pending' }, h('span', { testid: 'diff-pending' }, carried
    ? `${plural(n, 'comment')} will go with your next answer.`
    : `${plural(n, 'comment')} on this diff ${n === 1 ? 'is' : 'are'} still open.`),
  changesButton(ctx.id, 'diff', [ctx.card?.stage, d.rev, anns.filter(a => !a.resolved).map(a => a.id).sort((a, b) => a - b)].join('|'),
    'Send the open comments to the implementer, or back to plan with a design note'))
}

// looseBox holds the comments on files this diff does not show.
function looseBox (list, ctx) {
  return h('section', { class: 'file loose', testid: 'diff-other', 'aria-label': 'Comments on files not in this diff' },
    h('div', { class: 'fh' }, h('span', { class: 'p' }, 'Comments on files not in this diff')),
    h('p', { class: 'why' }, 'Written on a file this diff no longer shows, or pulled from a pull request thread on a file the branch never changed. An open one still counts against the gate: resolve or delete it here.'),
    annotBox(list, ctx, 'loose'))
}

function lineEl (ln, anns, ctx) {
  const cls = ln.t === '+' ? 'a' : ln.t === '-' ? 'd' : ''
  const open = (e) => openComment(e.currentTarget.closest('.ln'), ln, ctx)
  // a finished card's diff takes no new comments: its numbers are text
  const num = (v, c) => ctx.closed
    ? h('span', { class: [c, 'ro'] }, v ? String(v) : '')
    : h('span', { class: c, role: 'button', tabindex: '0', 'aria-label': `Comment on line ${ln.new || ln.old || ''}`, onclick: open, onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e) } } }, v ? String(v) : '')
  const code = h('span')
  code.innerHTML = hl(ln.text) || ' ' // hl escapes everything it is given
  const row = h('div', { class: ['ln', cls, ln.since && 'since'], testid: `diff-line-${ln.idx}`, data: { idx: String(ln.idx), new: ln.new ? String(ln.new) : null } },
    num(ln.old, 'o'), num(ln.new, 'n'), h('span', { class: 'm', 'aria-hidden': 'true' }, ln.t === ' ' ? '' : ln.t), code)
  const here = anns.filter(a => a.idx === ln.idx && a.idx >= 0)
  const dr = ctx.closed ? null : draftsOf(ctx.id).get(ln.idx)
  const draft = dr && dr.line === ln.text ? commentBox(ln, ctx, dr) : null
  if (draft) ctx.placed?.add(ln.idx)
  if (!here.length && !draft) return row
  const frag = document.createDocumentFragment()
  frag.append(row)
  if (draft) frag.append(draft)
  if (here.length) frag.append(annotBox(here, ctx, 'line'))
  return frag
}

// annotBox draws comments. mode is where they sit: on their line, as a
// file's orphans (the line they were written on is gone), or loose (the
// file itself is not in this diff).
function annotBox (list, ctx, mode) {
  const editing = editsOf(ctx.id)
  return h('div', { class: 'annot' }, list.map(a => h('div', { class: ['a1', a.resolved && 'resolved'], testid: `annotation-${a.id}` },
    h('span', { class: 'who' }, h('b', null, a.by || (a.source === 'pr' ? 'reviewer' : 'Comment')), a.source === 'pr' ? h('span', { class: 'src' }, 'PR') : null,
      a.resolved ? h('span', null, 'resolved') : null,
      h('span', { class: 'acts' },
        // a pulled PR thread is GitHub's words: it is resolved or
        // deleted here, never rewritten
        a.source !== 'pr' && !ctx.closed && !editing.has(a.id) ? h('button', { type: 'button', testid: `annotation-edit-${a.id}`, onclick: () => startEdit(ctx, a) }, 'Edit') : null,
        h('button', { type: 'button', testid: `annotation-resolve-${a.id}`, onclick: () => act(ctx, a, a.resolved ? 'reopen' : 'resolve') }, a.resolved ? 'Reopen' : 'Resolve'),
        h('button', { type: 'button', testid: `annotation-delete-${a.id}`, onclick: () => act(ctx, a, 'delete') }, 'Delete'))),
    mode === 'loose' ? h('span', { class: 'gone', testid: `annotation-where-${a.id}` }, a.file && a.file !== '/dev/null' ? `on ${a.file}` : 'on a file this branch deleted') : null,
    mode === 'orphan' ? h('span', { class: 'gone', testid: `annotation-gone-${a.id}` }, 'The line this was written on is no longer in the diff.') : null,
    mode !== 'line' && a.excerpt ? h('span', { class: 'ex' }, a.excerpt) : null,
    !ctx.closed && editing.has(a.id) ? editBox(ctx, a) : h('span', { class: 'tx' }, a.comment))))
}

function startEdit (ctx, a) {
  editsOf(ctx.id).set(a.id, a.comment)
  ctx.rerender()
  const ta = [...document.querySelectorAll('[data-draft]')].find(el => el.dataset.draft === `edit:${a.id}`)?.querySelector('textarea')
  ta?.focus()
  ta?.setSelectionRange(ta.value.length, ta.value.length)
}

// editBox rewrites a comment's words in place (PATCH …/annotations/{id});
// where it is anchored stays as it was.
function editBox (ctx, a) {
  const edits = editsOf(ctx.id)
  const ta = h('textarea', { testid: `annotation-edit-input-${a.id}`, 'aria-label': 'Edit the comment' })
  ta.value = edits.get(a.id)
  ta.addEventListener('input', () => edits.set(a.id, ta.value))
  const cancel = () => { edits.delete(a.id); ctx.rerender() }
  const save = async (e) => {
    const comment = ta.value.trim()
    if (!comment) { ta.focus(); return }
    const btn = e.currentTarget
    btn.disabled = true
    try {
      const d = await patch(cardPath(ctx.id, `diff/annotations/${a.id}`), { comment })
      edits.delete(a.id)
      toast('Comment changed')
      ctx.swap(d && d.files ? d : null)
    } catch (err) {
      btn.disabled = false
      toast(err.notBuilt ? 'Editing comments from the web is not available yet' : err.message, { err: !err.notBuilt })
    }
  }
  const box = h('div', { class: 'edit draft', data: { draft: `edit:${a.id}` } }, ta,
    h('div', { class: 'act' },
      h('button', { class: 'btn', type: 'button', onclick: cancel }, 'Cancel'),
      h('button', { class: 'btn pri', type: 'button', testid: `annotation-edit-save-${a.id}`, onclick: save }, 'Save')))
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); cancel() }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); box.querySelector('.pri').click() }
  })
  return box
}

async function act (ctx, a, what) {
  try {
    const path = cardPath(ctx.id, `diff/annotations/${a.id}`)
    const res = what === 'delete' ? await del(path) : await post(path + '/resolve', { resolved: what !== 'reopen' })
    if (what === 'delete') editsOf(ctx.id).delete(a.id)
    toast({ delete: 'Comment deleted', resolve: 'Comment resolved', reopen: 'Comment reopened' }[what])
    ctx.swap(res && res.files ? res : null)
  } catch (err) {
    toast(err.notBuilt ? 'Changing comments from the web is not available yet' : err.message, { err: !err.notBuilt })
  }
}

function openComment (lnEl, ln, ctx) {
  const next = lnEl.nextElementSibling
  if (next?.classList.contains('draft')) { next.querySelector('textarea')?.focus(); return }
  const box = commentBox(ln, ctx, null)
  lnEl.after(box)
  box.querySelector('textarea').focus()
}

// commentBox is the box a new comment on line ln is written in; saved is
// its draft when the tab is drawing it back after a redraw.
function commentBox (ln, ctx, saved) {
  const drafts = draftsOf(ctx.id)
  const dr = saved || { text: '', line: ln.text, where: ln.new || ln.old }
  drafts.set(ln.idx, dr)
  const ta = h('textarea', { testid: 'annotation-input', 'aria-label': `Comment on line ${ln.new || ln.old}`, placeholder: 'Comment on this line. It goes with your next send-back to this card.' })
  ta.value = dr.text
  ta.addEventListener('input', () => { dr.text = ta.value })
  const drop = () => { drafts.delete(ln.idx); box.remove() }
  const save = async (e) => {
    const comment = ta.value.trim()
    if (!comment) { ta.focus(); return }
    const btn = e.currentTarget
    btn.disabled = true
    try {
      const d = await post(cardPath(ctx.id, 'diff/annotations'), { idx: ln.idx, comment, text: ln.text })
      drafts.delete(ln.idx)
      toast(ctx.card?.decision ? 'Comment added. It goes with your next answer.' : 'Comment added')
      ctx.swap(d && d.files ? d : null)
    } catch (err) {
      btn.disabled = false
      if (err.status === 409) { toast('The diff moved under that line; here it is as it stands now. Your comment is still in the box.'); ctx.swap(null); return }
      toast(err.notBuilt ? 'Diff comments from the web are not available yet' : err.message, { err: !err.notBuilt })
    }
  }
  const box = h('div', { class: 'annot draft', testid: 'annotation-draft', data: { draft: `line:${ln.idx}` } },
    h('div', { class: 'a1' }, h('span', { class: 'who' }, h('b', null, ctx.person || 'you'), ` on line ${ln.new || ln.old}`), ta),
    h('div', { class: 'act' },
      h('button', { class: 'btn', type: 'button', onclick: drop }, 'Cancel'),
      h('button', { class: 'btn pri', type: 'button', testid: 'annotation-save', onclick: save }, 'Comment')))
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); drop() }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) box.querySelector('[data-testid="annotation-save"]').click()
  })
  return box
}

// adriftBox is a comment being written whose line the diff no longer has:
// its words are kept to copy onto the line it belongs on now.
function adriftBox (idx, dr, ctx) {
  const ta = h('textarea', { 'aria-label': 'Your unsent comment' })
  ta.value = dr.text
  ta.addEventListener('input', () => { dr.text = ta.value })
  const box = h('div', { class: 'annot draft adrift', testid: 'annotation-adrift', data: { draft: `line:${idx}` } },
    h('div', { class: 'a1' }, h('span', { class: 'who' }, h('b', null, ctx.person || 'you'), ` on line ${dr.where || '?'}`),
      h('span', { class: 'gone' }, 'The branch moved and that line is no longer where it was. Your words are kept here to put on the line they belong to now.'), ta),
    h('div', { class: 'act' }, h('button', { class: 'btn', type: 'button', onclick: () => { draftsOf(ctx.id).delete(idx); box.remove() } }, 'Discard')))
  return box
}

// hl escapes a line of code and tints keywords, strings, comments and calls.
// Only fixed <span class> tags are added after escaping, so no text from the
// diff can become markup.
const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }
function esc (s) { return String(s).replace(/[&<>"]/g, c => ESC[c]) }
export function hl (line) {
  line = String(line ?? '')
  if (/^\s*(\/\/|#(?!include)|--)/.test(line)) return `<span class="tk-c">${esc(line)}</span>`
  // split off strings first so nothing inside them is tinted
  const parts = line.split(/("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`[^`]*`)/)
  let out = ''
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i]
    if (i % 2 === 1) { out += `<span class="tk-s">${esc(p)}</span>`; continue }
    const cm = p.indexOf('//')
    const code = cm >= 0 ? p.slice(0, cm) : p
    const rest = cm >= 0 ? p.slice(cm) : ''
    out += esc(code)
      .replace(/\b(func|return|if|else|for|range|var|const|let|import|package|export|async|await|continue|break|nil|null|true|false|type|struct|interface|def|class|switch|case|default|go|defer|select|chan|map|new|function|from)\b/g, '<span class="tk-k">$1</span>')
      .replace(/\b([A-Za-z_]\w*)(?=\()/g, '<span class="tk-f">$1</span>')
    if (rest) { out += `<span class="tk-c">${esc(rest + parts.slice(i + 1).join(''))}</span>`; break }
  }
  return out
}

