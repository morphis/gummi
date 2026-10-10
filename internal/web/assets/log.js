// log.js — the Log tab: the card's own commits, oldest first, and the one
// thing that can be done to them here: reword a commit, squash it into
// the one before, or — where commits are signed — sign the ones that are
// not. All of it leaves the branch's content exactly as it was, so
// what verify saw and the comments on the diff stay true; the server
// refuses anything else, and refuses to rewrite while an agent is working.
//
// The edits are a draft the page holds. Each change asks the server what
// the branch would look like (a dry run that moves nothing), and only
// Apply rewrites it. gummi never pushes: a rewrite of commits the remote
// already has ends with the command a person runs themselves.

import { h, clock, plural } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'

export const logTab = {
  name: 'log',
  label: 'Log',
  key: 'g l',
  fetch: (id) => get(cardPath(id, 'log')),
  empty: (d) => !d || !d.commits?.length,
  render
}

// per card: the draft edits, what is expanded, the last dry run and the
// force push a finished rewrite left for the person
const drafts = new Map()
const pushes = new Map()
const patches = new Map() // sha -> the commit's patch, kept while the page is open

function draftFor (id, head) {
  let d = drafts.get(id)
  // the branch moved since the draft was made: its commits are not these
  if (!d || d.head !== head) {
    d = { head, squash: new Set(), msg: new Map(), sign: false, typing: new Map(), editing: null, open: new Set(), preview: null, seq: 0, confirming: false }
    drafts.set(id, d)
  }
  return d
}

const fullMessage = (c) => c.body ? `${c.subject}\n\n${c.body}` : c.subject

// groupsOf turns the draft into the request: contiguous runs, the first
// commit of each run carrying the message when it was changed or when the
// run has more than one commit to say something about.
function groupsOf (commits, dr) {
  const groups = []
  commits.forEach((c, i) => {
    if (i > 0 && dr.squash.has(c.sha)) { groups[groups.length - 1].commits.push(c.sha); return }
    groups.push({ commits: [c.sha], lead: c })
  })
  return groups.map(g => {
    const own = dr.msg.get(g.lead.sha)
    const msg = own !== undefined ? own : (g.commits.length > 1 ? fullMessage(g.lead) : '')
    return msg.trim() ? { commits: g.commits, message: msg.trim() } : { commits: g.commits }
  })
}

const dirty = (dr) => dr.squash.size > 0 || dr.msg.size > 0 || dr.sign

// planOf is the request a draft makes: its groups, and whether the
// unsigned commits are to be made again, signed.
const planOf = (d, dr, groups) => dr.sign ? { head: d.head, groups, sign: true } : { head: d.head, groups }

// keyOf names the draft a dry run answered, so an answer to an earlier
// one is not read as this one's.
const keyOf = (dr, groups) => JSON.stringify([groups, dr.sign])

function render (pane, entry, ctx) {
  const d = entry.data
  if (!d || !d.commits?.length) {
    pane.append(h('div', { class: 'empty', testid: 'log-none' }, h('b', null, 'No commits yet'),
      d?.why || 'Commits appear here once the card has made some.'))
    return
  }
  const dr = draftFor(ctx.id, d.head)
  const can = d.rewritable
  const groups = groupsOf(d.commits, dr)
  const heads = new Set(groups.map(g => g.commits[0]))
  const sect = h('div', { class: 'sect log', testid: 'log' })
  sect.append(h('div', { class: 'loghead', testid: 'log-head' },
    h('span', null, h('b', null, plural(d.commits.length, 'commit')), ` ahead of ${d.base || 'its base'}`),
    !can && d.why ? h('span', { class: 'why', testid: 'log-why' }, d.why) : null))
  if (pushes.has(ctx.id)) sect.append(pushBox(pushes.get(ctx.id), ctx))

  const list = h('ol', { class: 'commits' })
  d.commits.forEach((c, i) => {
    const folded = i > 0 && dr.squash.has(c.sha)
    list.append(commitRow(c, i, { d, dr, ctx, can, folded, lead: heads.has(c.sha) }))
  })
  sect.append(list)
  if (can) sect.append(planBar(d, dr, ctx, groups))
  pane.append(sect)
  if (can && dirty(dr)) dryRun(d, dr, ctx, groups)
}

function commitRow (c, i, { d, dr, ctx, can, folded, lead }) {
  const editing = dr.editing === c.sha
  const reworded = dr.msg.has(c.sha)
  const row = h('li', { class: ['commit', folded && 'folded', c.checkpoint && 'cp', reworded && 'edited'], testid: `log-commit-${i}` },
    h('div', { class: 'ch' },
      h('span', { class: 'sha mono' }, c.short),
      h('span', { class: 'subj' }, reworded && lead ? (dr.msg.get(c.sha).split('\n')[0] || c.subject) : c.subject),
      c.checkpoint ? h('span', { class: 'tag', title: 'gummi committed this itself between turns' }, 'checkpoint') : null,
      c.pushed ? h('span', { class: 'tag pushed', title: 'The remote already has this commit; rewriting it needs a force push' }, 'pushed') : null,
      c.signed
        ? h('span', { class: 'tag', title: 'This commit carries a signature' }, 'signed')
        : dr.sign ? h('span', { class: 'tag tosign', title: 'Apply makes this commit again, signed' }, 'to sign') : null,
      c.warning ? h('span', { class: 'tag warn', title: `The message carries “${c.warning}” — a rewrite refuses it, and landing scrubs it` }, 'attribution') : null),
    h('div', { class: 'cm' },
      h('span', null, c.author),
      h('span', null, clock(c.at)),
      h('span', null, plural(c.files, 'file')),
      h('span', null, h('span', { class: 'add' }, `+${c.add}`), ' ', h('span', { class: 'del' }, `−${c.del}`)),
      folded ? h('span', { class: 'into' }, `squashed into ${leadShort(d, dr, i)}`) : null),
    h('div', { class: 'ca' },
      h('button', { class: 'link', type: 'button', testid: `log-show-${i}`, 'aria-expanded': String(dr.open.has(c.sha)), onclick: () => toggleOpen(c, dr, ctx) }, dr.open.has(c.sha) ? 'Hide changes' : 'Show changes'),
      can && lead ? h('button', { class: 'link', type: 'button', testid: `log-reword-${i}`, onclick: () => { dr.editing = editing ? null : c.sha; dr.typing.delete(c.sha); ctx.rerender() } }, editing ? 'Cancel' : 'Reword') : null,
      can && i > 0 ? h('button', { class: ['link', folded && 'on'], type: 'button', testid: `log-squash-${i}`, 'aria-pressed': String(folded), title: 'Fold this commit into the one before it', onclick: () => { folded ? dr.squash.delete(c.sha) : dr.squash.add(c.sha); dr.confirming = false; ctx.rerender() } }, folded ? 'Unsquash' : 'Squash into previous') : null))
  if (editing) row.append(editor(c, i, dr, ctx, d))
  if (dr.open.has(c.sha)) row.append(patchView(c, ctx))
  return row
}

// leadShort names the commit a folded one lands in: the nearest one
// before it that is not itself folded.
function leadShort (d, dr, i) {
  for (let j = i - 1; j >= 0; j--) if (j === 0 || !dr.squash.has(d.commits[j].sha)) return d.commits[j].short
  return d.commits[0].short
}

function editor (c, i, dr, ctx, d) {
  const group = groupOf(d.commits, dr, c)
  const ta = h('textarea', { class: 'msg', rows: '6', spellcheck: 'true', testid: `log-editor-${i}`, 'aria-label': group > 1 ? 'Message for the combined commit' : 'Commit message' })
  // what is being typed outlives the tab's redraws (every change to the
  // card redraws it); it becomes the draft's only on "Use this message"
  ta.value = dr.typing.has(c.sha) ? dr.typing.get(c.sha) : dr.msg.has(c.sha) ? dr.msg.get(c.sha) : fullMessage(c)
  ta.addEventListener('input', () => dr.typing.set(c.sha, ta.value))
  return h('div', { class: 'editor', data: { draft: `msg:${c.sha}` } },
    group > 1 ? h('p', { class: 'hint' }, `This is the message of the ${group} commits squashed together.`) : null,
    ta,
    h('div', { class: 'row' },
      h('button', { class: 'btn pri', type: 'button', testid: `log-editor-save-${i}`, onclick: () => {
        const v = ta.value.trim()
        if (!v) { toast('A commit needs a message', { err: true }); return }
        if (v === fullMessage(c).trim() && group === 1) dr.msg.delete(c.sha); else dr.msg.set(c.sha, v)
        dr.typing.delete(c.sha)
        dr.editing = null
        dr.confirming = false
        ctx.rerender()
      } }, 'Use this message'),
      h('button', { class: 'btn', type: 'button', onclick: () => { dr.editing = null; dr.typing.delete(c.sha); ctx.rerender() } }, 'Cancel')))
}

// groupOf is how many commits the group led by c holds.
function groupOf (commits, dr, c) {
  const i = commits.findIndex(x => x.sha === c.sha)
  let n = 1
  for (let j = i + 1; j < commits.length && dr.squash.has(commits[j].sha); j++) n++
  return n
}

async function toggleOpen (c, dr, ctx) {
  if (dr.open.has(c.sha)) { dr.open.delete(c.sha); ctx.rerender(); return }
  dr.open.add(c.sha)
  ctx.rerender()
  if (patches.has(c.sha)) return
  try {
    patches.set(c.sha, await get(cardPath(ctx.id, `log/${encodeURIComponent(c.short)}`)))
  } catch (err) {
    patches.set(c.sha, { error: err.message })
  }
  ctx.rerender()
}

// A commit's patch folds its large files the way the Diff tab does: a
// file longer than BIG_FILE lines, or any once BUDGET lines are drawn,
// waits behind a "Show N lines" button, and stays open once opened.
const BIG_FILE = 400
const SMALL_FILE = 40
const BUDGET = 1500
const unfoldedFiles = new Set() // `${sha}:${path}` a reader opened

function patchView (c, ctx) {
  const p = patches.get(c.sha)
  if (!p) return h('div', { class: 'patch' }, h('span', { class: 'spinner' }))
  if (p.error) return h('div', { class: 'patch badc' }, p.error)
  if (!p.files?.length) return h('div', { class: 'patch hint' }, 'This commit changes no files.')
  let drawn = 0
  return h('div', { class: 'patch cdiff', testid: 'log-patch' }, p.files.map((f, i) => {
    const n = (f.hunks || []).reduce((a, hk) => a + (hk.lines?.length || 0), 0)
    const key = `${c.sha}:${f.path}`
    const folded = !unfoldedFiles.has(key) && (n > BIG_FILE || (n > SMALL_FILE && drawn + n > BUDGET))
    if (!folded) drawn += n
    const hunks = () => (f.hunks || []).map(hk => h('div', { class: 'hunk' }, h('div', { class: 'hh' }, hk.header),
      hk.lines.map(l => h('div', { class: ['ln', l.t === '+' ? 'a' : l.t === '-' ? 'd' : ''] },
        h('span', { class: 'o' }, l.old ? String(l.old) : ''), h('span', { class: 'n' }, l.new ? String(l.new) : ''),
        h('span', { class: 'm', 'aria-hidden': 'true' }, l.t === ' ' ? '' : l.t), h('span', null, l.text || ' ')))))
    return h('section', { class: 'file' },
      h('div', { class: 'fh' }, h('span', { class: 'p' }, f.oldPath ? `${f.oldPath} → ${f.path}` : f.path),
        f.status && f.status !== 'modified' ? h('span', { class: 'fstatus' }, f.status) : null,
        h('span', { class: 'add' }, `+${f.add}`), h('span', { class: 'del' }, `−${f.del}`)),
      f.binary ? h('div', { class: 'binary' }, 'Binary file; not shown.') : null,
      folded
        ? h('button', {
          class: 'fold', type: 'button', testid: `log-unfold-${i}`,
          onclick: (e) => { unfoldedFiles.add(key); e.currentTarget.replaceWith(...hunks()) }
        }, `Show ${plural(n, 'line')}`, h('span', null, n > BIG_FILE ? ' · a large file, folded to keep the log quick' : ' · folded to keep the log quick'))
        : hunks())
  }))
}

function planBar (d, dr, ctx, groups) {
  // a dry run of an earlier draft says nothing about this one
  const p = dr.preview?.for === keyOf(dr, groups) ? dr.preview : null
  const changed = dirty(dr)
  const line = !changed
    ? `Reword a commit, ${d.signable ? 'squash it into the one before, or sign the ones that are not signed' : 'or squash it into the one before'}. The content of the branch never changes.`
    : p?.error ? p.error
      : p ? (p.noop ? 'No change.' : `${plural(d.commits.length, 'commit')} → ${plural(p.commits.length, 'commit')} · content unchanged${dr.sign ? ` · ${plural(p.changed, 'commit')} made again, signed` : ''}`)
        : 'Checking…'
  return h('div', { class: ['plan', p?.error && 'bad'], testid: 'log-plan', 'aria-live': 'polite' },
    h('span', { class: 'pl', testid: 'log-plan-line' }, line),
    dr.confirming && p?.pushed ? h('div', { class: 'force', testid: 'log-confirm' },
      h('span', null, 'The remote already has commits this replaces. gummi will not push; afterwards you run:'),
      h('div', { class: 'cmd' }, h('span', { testid: 'log-confirm-cmd' }, p.pushCommand || d.pushCommand || ''), copyButton(p.pushCommand || d.pushCommand || '', 'log-confirm-copy'))) : null,
    h('div', { class: 'row' },
      d.signable ? h('button', { class: ['btn', dr.sign && 'on'], type: 'button', testid: 'log-sign', 'aria-pressed': String(dr.sign), title: 'Make every commit from the first unsigned one up again, signed. Messages and content stay as they are; the commits get new SHAs', onclick: () => { dr.sign = !dr.sign; dr.confirming = false; ctx.rerender() } }, dr.sign ? 'Don’t sign' : 'Sign all commits') : null,
      h('button', { class: 'btn', type: 'button', testid: 'log-reset', disabled: !changed, onclick: () => { drafts.delete(ctx.id); ctx.rerender() } }, 'Reset'),
      h('button', { class: 'btn pri', type: 'button', testid: 'log-apply', disabled: !changed || !p || !!p.error || p.noop, onclick: () => apply(d, dr, ctx, groups, p) },
        dr.confirming ? 'Rewrite anyway' : (p?.pushed ? 'Rewrite (needs a force push)' : 'Apply'))))
}

async function dryRun (d, dr, ctx, groups) {
  const seq = ++dr.seq
  const key = keyOf(dr, groups)
  if (dr.preview?.for === key) return
  try {
    const p = await post(cardPath(ctx.id, 'log/plan'), planOf(d, dr, groups))
    if (dr.seq !== seq) return
    dr.preview = { ...p, for: key }
  } catch (err) {
    if (dr.seq !== seq) return
    dr.preview = { error: err.message, for: key }
  }
  ctx.rerender()
}

async function apply (d, dr, ctx, groups, p) {
  if (p.pushed && !dr.confirming) { dr.confirming = true; ctx.rerender(); return }
  try {
    const res = await post(cardPath(ctx.id, 'log/rewrite'), { ...planOf(d, dr, groups), acknowledgePushed: !!p.pushed })
    drafts.delete(ctx.id)
    if (res.pushCommand) pushes.set(ctx.id, res.pushCommand); else pushes.delete(ctx.id)
    toast('History rewritten; the branch’s content is unchanged')
    ctx.swap(res.log)
  } catch (err) {
    toast(err.message, { err: true })
    drafts.delete(ctx.id)
    ctx.swap(null)
  }
}

function pushBox (cmd, ctx) {
  return h('div', { class: 'push', testid: 'log-push' },
    h('span', null, 'The remote still has the old commits. To replace them, run this, or push from the PR tab where publishing is set up:'),
    h('div', { class: 'cmd' }, h('span', { testid: 'log-push-cmd' }, cmd),
      copyButton(cmd, 'log-push-copy'),
      h('button', { type: 'button', onclick: () => { pushes.delete(ctx.id); ctx.rerender() } }, 'Dismiss')))
}

function copyButton (cmd, testid) {
  return h('button', {
    type: 'button',
    testid,
    onclick: () => (navigator.clipboard?.writeText(cmd) ?? Promise.reject(new Error('no clipboard'))).then(() => toast('Copied'), () => toast('Select the command to copy it'))
  }, 'Copy')
}
