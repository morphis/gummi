// spec.js — the Spec tab: the card's document as it stands on its branch,
// with a sections outline, its review notes shown inline where they are
// anchored, a "Comment" on every section that writes a note into the spec
// (POST …/spec/notes), "Request changes" sending the open notes to their
// writer (POST …/spec/changes), and the gummi-checks block drawn as a table with each
// check's last outcome on this branch.

import { h, append, clock, plural, clear } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath, uploadAttachment } from './api.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { changesButton } from './actions.js?v=__ASSET_V__'

export const specTab = {
  name: 'spec',
  label: 'Spec',
  key: 'g s',
  fetch: (id) => get(cardPath(id, 'spec')),
  empty: (s) => !s || s.none,
  // a session has no spec and never will: its work is its diff
  hidden: (c) => c?.stage === 'open' || c?.kind === 'freeform',
  render
}

const slug = (name, i) => `sec-${i}-` + String(name).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

function render (pane, entry, ctx) {
  const s = entry.data
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
    h('div', { class: 'src', testid: 'spec-src' }, [s.path, s.rev ? `@ ${String(s.rev).slice(0, 7)}` : null].filter(Boolean).join(' '),
      s.draft ? h('span', { class: 'draftflag' }, ' · draft, not on the branch yet') : null),
    h('h1', { testid: 'spec-title' }, s.title || ctx.card?.title || ''),
    markdown(pre.join('\n'), opts),
    notes.filter(n => !n.anchor).map(n => noteEl(n, ctx)),
    notesIn(notes.filter(n => n.anchor), 1, firstLine - 1, ctx))
  sections.forEach((sec, i) => {
    const end = i + 1 < sections.length ? sections[i + 1].line - 1 : lines.length
    const body = lines.slice(sec.line, end).join('\n')
    const heading = h('h2', { class: 'sec', id: slug(sec.name, i), testid: `spec-section-${i}` }, sec.name,
      h('button', { class: 'sc', type: 'button', testid: `spec-comment-${i}`, 'aria-label': `Comment on ${sec.name}`, onclick: (e) => openNote(e.currentTarget.closest('h2'), sec, ctx) }, 'Comment'))
    append(doc, [heading, markdown(body, { ...opts, headingBase: 3 }), notesIn(notes, sec.line, end, ctx)])
  })
  if (!checksDrawn && s.checks?.length) doc.append(h('h2', { class: 'sec' }, 'Checks'), checksBlock())
  if (s.openComments) {
    // the TUI's R: the open notes go now, not on the next pass — to the
    // stage writing what they are about, which may mean sending the card
    // back (asked first)
    pane.append(h('div', { class: 'pending' },
      h('span', { testid: 'spec-pending' }, `${plural(s.openComments, 'comment')} ${s.openComments === 1 ? 'is' : 'are'} still open here.`),
      changesButton(ctx.id, 'spec', [ctx.card?.stage, s.rev, s.openComments].join('|'),
        'Send the open comments to the stage that writes what they are about')))
  }
  pane.append(h('div', { class: 'spec', testid: 'spec' },
    h('nav', { class: 'toc', 'aria-label': 'Sections', testid: 'spec-toc' }, h('h2', null, 'Sections'),
      sections.map((sec, i) => h('a', {
        href: '#' + slug(sec.name, i),
        testid: `spec-toc-${i}`,
        onclick: (e) => { e.preventDefault(); document.getElementById(slug(sec.name, i))?.scrollIntoView({ block: 'start' }) }
      }, sec.name))),
    doc))
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
  const r = n.resolution
  const why = r ? String(r.text || '').trim().replace(RESOLVED, '').trim() : ''
  return h('div', { class: ['note', n.resolved && 'resolved', prompt && 'prompt'], testid: prompt ? 'spec-prompt' : 'spec-note' },
    n.resolved ? h('span', { class: 'rs' }, 'resolved')
      : prompt ? null
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

function openNote (h2, sec, ctx) {
  const next = h2.nextElementSibling
  if (next?.classList.contains('draft')) { next.querySelector('textarea')?.focus(); return }
  // attachments are stored by reference (internal/attachment.Link), so —
  // unlike the composer's turn — every backend can take one: the note
  // form always offers the control, never gated on Composer.Images.
  let attachments = []
  const ta = h('textarea', { testid: 'spec-note-input', 'aria-label': `Note on ${sec.name}`, placeholder: 'Written into the spec as a %% note under your name. The architect answers it on the next pass.' })
  const chips = h('div', { class: 'chips', testid: 'spec-note-chips', hidden: true })
  const file = h('input', {
    type: 'file', accept: 'image/png,image/jpeg,image/gif,image/webp', multiple: true, hidden: true,
    onchange: async (e) => {
      const files = [...e.target.files]
      e.target.value = ''
      for (const f of files) {
        const chip = { id: null, name: f.name || 'image', pending: true, error: null }
        attachments.push(chip)
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
    chips.hidden = attachments.length === 0
    for (const a of attachments) {
      chips.append(h('span', { class: ['chip', a.error && 'err', a.pending && 'pending'] },
        a.pending ? 'Uploading…' : (a.error || a.name),
        h('button', { type: 'button', title: 'Remove', onclick: () => { attachments = attachments.filter((x) => x !== a); renderChips() } }, '×')))
    }
  }
  const box = h('div', { class: 'annot draft', testid: 'spec-note-draft' },
    h('div', { class: 'a1' }, h('span', { class: 'who' }, h('b', null, ctx.person || 'you'), ` on ${sec.name}`), ta, chips),
    h('div', { class: 'act' },
      h('button', { class: 'attach', type: 'button', testid: 'spec-note-attach', title: 'Attach an image', onclick: () => file.click() }, '📎'),
      file,
      h('button', { class: 'btn', type: 'button', onclick: () => box.remove() }, 'Cancel'),
      h('button', {
        class: 'btn pri',
        type: 'button',
        testid: 'spec-note-save',
        onclick: async (e) => {
          const text = ta.value.trim()
          if (!text) { ta.focus(); return }
          if (attachments.some((a) => a.pending)) { toast('Still uploading an image — wait a moment and try again'); return }
          if (attachments.some((a) => a.error)) { toast('Remove the failed attachment before saving', { err: true }); return }
          const btn = e.currentTarget
          btn.disabled = true
          try {
            const body = { line: sec.line, text }
            if (attachments.length) body.attachments = attachments.map((a) => a.id)
            const spec = await post(cardPath(ctx.id, 'spec/notes'), body)
            toast('Note written into the spec')
            ctx.swap(spec && spec.markdown !== undefined ? spec : null)
          } catch (err) {
            btn.disabled = false
            toast(err.notBuilt ? 'Spec notes from the web are not available yet' : err.message, { err: !err.notBuilt })
          }
        }
      }, 'Add note')))
  ta.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); box.remove() } })
  h2.after(box)
  ta.focus()
}

async function resolve (n, ctx) {
  try {
    await post(cardPath(ctx.id, 'spec/notes/resolve'), { line: n.line, author: n.author, date: n.date })
    toast('Note resolved')
    ctx.swap(null)
  } catch (err) {
    if (err.status === 409) { toast('The spec changed since you read it; here it is as it stands now'); ctx.swap(null); return }
    toast(err.notBuilt ? 'Resolving notes from the web is not available yet' : err.message, { err: !err.notBuilt })
  }
}

