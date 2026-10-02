// markdown.js — a small, safe markdown renderer for agent messages and specs.
// It builds DOM nodes, never HTML strings, so nothing in the source can
// become markup: raw HTML shows as text. Supported: paragraphs, headings,
// fenced code (with a hook for custom fences such as gummi-checks),
// indented code, code spans (both keep their spaces as written), bold, italic, lists (nested by indent), block quotes, rules, pipe
// tables and links (http and https only, opened with rel=noopener).
// With opts.files (a card's webapi.Files) a path under the card's worktree,
// as an agent names a file it wrote, links to the server's copy of it.
// Lines starting with `%%` are review notes and prompts; the spec view shows
// notes on their own, so they are dropped here.

import { h } from './dom.js?v=__ASSET_V__'

const FENCE = /^\s{0,3}(`{3,}|~{3,})\s*([\w+-]*)/
const INDENTED = /^(?: {4}| {0,3}\t)/
const HEADING = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/
const RULE = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/
const QUOTE = /^\s{0,3}>\s?/
const ITEM = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/
const TABLE_SEP = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/

// files is the worktree a markdown() call links paths under, for as long
// as the call runs (the renderer is synchronous).
let files = null

// Headings are levelled against the page they land in, not the source:
// an agent's "#### Detail" under a card's h1 would skip two levels, which
// is how a screen reader's heading list loses its way. A fragment's first
// heading is at most opts.headingBase (default 2, under a page's h1; the
// spec passes 3, under its section h2), and none is more than one deeper
// than the heading before it.
export function markdown (src, opts = {}) {
  const frag = h('div', { class: ['md', opts.class] })
  const lines = String(src || '').replace(/\r\n?/g, '\n').split('\n').filter(l => !/^\s*%%/.test(l))
  files = opts.files?.dir && opts.files?.url ? opts.files : null
  try {
    frag.append(...blocks(lines, { ...opts, levels: { last: (opts.headingBase || 2) - 1 } }))
  } finally {
    files = null
  }
  return frag
}

function blocks (lines, opts) {
  const out = []
  let i = 0
  let para = []
  const flush = () => {
    if (para.length) out.push(h('p', null, inline(para.join('\n'))))
    para = []
  }
  while (i < lines.length) {
    const line = lines[i]
    let m
    if (!line.trim()) { flush(); i++; continue }
    if (!para.length && INDENTED.test(line)) {
      // an indented code block (CommonMark §4.4): four columns in, and
      // never one that interrupts a paragraph — an indented line under a
      // paragraph's text continues it. It runs through blank lines to the
      // first line that is less indented; its blank tail is not its own.
      const body = []
      while (i < lines.length && (INDENTED.test(lines[i]) || !lines[i].trim())) body.push(lines[i++].replace(INDENTED, ''))
      while (!body[body.length - 1].trim()) { body.pop(); i-- }
      out.push(h('pre', { tabindex: '0' }, h('code', null, body.join('\n'))))
      continue
    }
    if ((m = FENCE.exec(line))) {
      flush()
      const mark = m[1]
      const lang = m[2]
      const body = []
      i++
      while (i < lines.length && !lines[i].trimStart().startsWith(mark)) body.push(lines[i++])
      i++ // closing fence
      const custom = opts.fence?.(lang, body.join('\n'))
      out.push(custom || h('pre', { tabindex: '0' }, h('code', { class: lang ? `lang-${lang}` : null }, body.join('\n'))))
      continue
    }
    if ((m = HEADING.exec(line))) {
      flush()
      const level = Math.min(m[1].length, opts.levels.last + 1)
      opts.levels.last = level
      out.push(opts.heading?.(level, m[2]) || h('h' + level, null, inline(m[2])))
      i++
      continue
    }
    if (RULE.test(line) && !para.length) { flush(); out.push(h('hr')); i++; continue }
    if (QUOTE.test(line)) {
      flush()
      const body = []
      while (i < lines.length && lines[i].trim() && QUOTE.test(lines[i])) body.push(lines[i++].replace(QUOTE, ''))
      out.push(h('blockquote', null, blocks(body, opts)))
      continue
    }
    if (ITEM.test(line)) {
      flush()
      const body = []
      while (i < lines.length) {
        const l = lines[i]
        if (!l.trim()) {
          // a blank line ends the list unless the next line continues it
          const next = lines[i + 1]
          if (next && (ITEM.test(next) || /^\s{2,}\S/.test(next))) { body.push(l); i++; continue }
          break
        }
        if (ITEM.test(l) || /^\s{2,}\S/.test(l) || body.length) {
          if (!ITEM.test(l) && !/^\s/.test(l) && (HEADING.test(l) || FENCE.test(l))) break
          body.push(l); i++
          continue
        }
        break
      }
      out.push(list(body, opts))
      continue
    }
    if (line.includes('|') && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
      flush()
      const head = cells(line)
      i += 2
      const rows = []
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) rows.push(cells(lines[i++]))
      out.push(h('table', null,
        h('thead', null, h('tr', null, head.map(c => h('th', null, inline(c))))),
        h('tbody', null, rows.map(r => h('tr', null, r.map(c => h('td', null, inline(c))))))))
      continue
    }
    para.push(line.replace(/^\s+/, ''))
    i++
  }
  flush()
  return out
}

function cells (line) {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim())
}

// list renders a run of list lines: items at the smallest indent, and each
// item's deeper lines as its own blocks (which may hold a nested list).
function list (lines, opts) {
  const first = ITEM.exec(lines[0])
  const base = first[1].length
  const ordered = /\d/.test(first[2])
  const el = h(ordered ? 'ol' : 'ul')
  if (ordered) {
    const start = parseInt(first[2], 10)
    if (start !== 1) el.setAttribute('start', String(start))
  }
  let cur = null
  for (const l of lines) {
    const m = ITEM.exec(l)
    if (m && m[1].length <= base + 1) {
      cur = { text: [m[3]], sub: [] }
      el.append(cur.li = h('li'))
      cur.li._md = cur
      continue
    }
    if (!cur) continue
    const deIndented = l.replace(new RegExp(`^\\s{0,${base + 4}}`), '')
    if (!cur.sub.length && !ITEM.test(l) && l.trim()) cur.text.push(l.trim())
    else cur.sub.push(deIndented)
  }
  for (const li of el.children) {
    const it = li._md
    li.append(...inline(it.text.join('\n')))
    const sub = it.sub.filter((l, idx, a) => l.trim() || idx < a.length - 1)
    if (sub.some(l => l.trim())) li.append(...blocks(sub, opts))
    delete li._md
  }
  return el
}

const INLINE = /(`+)([\s\S]*?[^`]|[^`])\1(?!`)|\*\*([\s\S]+?)\*\*|__([\s\S]+?)__|\*([^*\s](?:[^*]*?[^*\s])?)\*|(^|[^\w])_([^_\s](?:[^_]*?[^_\s])?)_(?!\w)|\[([^\]\n]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g

export function inline (text) {
  const out = []
  let last = 0
  text = String(text || '')
  // a fresh regex per call: the recursion below must not share lastIndex
  const re = new RegExp(INLINE.source, 'g')
  let m
  while ((m = re.exec(text))) {
    let start = m.index
    if (m[7] !== undefined) start += m[6].length // keep the char before _em_
    if (start > last) out.push(...paths(text.slice(last, start)))
    // a code span keeps its spaces (app.css) but not its line breaks,
    // which CommonMark reads as spaces
    if (m[1]) out.push(fileLink(m[2].trim(), h('code', null, m[2].replace(/\n/g, ' '))))
    else if (m[3] !== undefined) out.push(h('strong', null, inline(m[3])))
    else if (m[4] !== undefined) out.push(h('strong', null, inline(m[4])))
    else if (m[5] !== undefined) out.push(h('em', null, inline(m[5])))
    else if (m[7] !== undefined) out.push(h('em', null, inline(m[7])))
    else if (m[8] !== undefined) out.push(link(m[9], inline(m[8])))
    else if (m[10] !== undefined) out.push(link(m[10], [m[10]]))
    last = re.lastIndex
  }
  if (last < text.length) out.push(...paths(text.slice(last)))
  return out
}

// paths links each worktree path in plain text, keeping the rest as breaks.
function paths (s) {
  if (!files) return breaks(s)
  const re = new RegExp(files.dir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '/[^\\s<>()\\[\\]`\'"]*[^\\s<>()\\[\\]`\'".,;:!?]', 'g')
  const out = []
  let last = 0
  let m
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(...breaks(s.slice(last, m.index)))
    out.push(fileLink(m[0], m[0]))
    last = re.lastIndex
  }
  if (last < s.length) out.push(...breaks(s.slice(last)))
  return out
}

// fileLink is kids linked to the served copy of p when p is a file under
// the worktree (a trailing :line or :line:col is dropped), else kids.
function fileLink (p, kids) {
  if (!files || !p.startsWith(files.dir + '/')) return kids
  const rel = p.slice(files.dir.length + 1).replace(/(?::\d+){1,2}$/, '')
  if (!rel || /\n/.test(rel)) return kids
  const href = files.url + rel.split('/').map(encodeURIComponent).join('/')
  return h('a', { href, target: '_blank', rel: 'noopener noreferrer', class: 'file' }, kids)
}

// breaks keeps a hard line break (two trailing spaces or a backslash).
function breaks (s) {
  const parts = s.split(/(?: {2,}|\\)\n/)
  const out = []
  parts.forEach((p, i) => { if (i) out.push(h('br')); out.push(p) })
  return out
}

function link (href, kids) {
  if (files && href.startsWith(files.dir + '/')) {
    // a link's destination is URL-encoded (%20 for a space); fileLink
    // takes the path as written on disk
    let p = href
    try { p = decodeURI(href) } catch { p = href }
    const a = fileLink(p, kids)
    if (a !== kids) return a
  }
  let url = null
  try { url = new URL(href, location.href) } catch { url = null }
  if (!url || !/^https?:$/.test(url.protocol) || !/^https?:\/\//i.test(href)) {
    return h('span', null, kids)
  }
  return h('a', { href: url.href, target: '_blank', rel: 'noopener noreferrer' }, kids)
}
