// views/ingest.js — Import spec (DESIGN §11): hand the board a document,
// watch the architect decompose it, then review the proposed cards before
// any exist. The board holds one pass at a time, the TUI's own: this page
// shows whatever pass is current — started here, in the terminal, or by a
// research card's decompose re-run.
//
//   form      paste markdown (with a name) or name a workspace file; the
//             profile, repository and envelope the cards are made with
//   running   the pass's milestones and tool calls, streamed as `ingest`
//             events (each names the run; the page refetches it)
//   review    rename, edit the one-liner, drop/undrop, merge into the one
//             above; coverage and the UNMAPPED requirements stay in view,
//             and the approve confirm repeats them as the TUI's does
//   done      the cards the approval minted, each a link to open it

import { h, clear, plural } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { field, choose, segmented, errorBox, confirmStrip, cardLinks, refetcher } from './kit.js?v=__ASSET_V__'

let draft = { how: 'paste', name: '', markdown: '', path: '', profile: '', repo: '', envelope: '' }

registerView('ingest', {
  title: 'Import spec',
  css: 'views/ingest.css',
  mount (body, ctx) {
    const v = { run: null, choices: null, confirm: null, editing: null, err: null, mine: null, alive: true }
    body.classList.add('vingest')

    const load = refetcher(async () => {
      try {
        const run = await ctx.api.get(v.run?.id ? `/api/ingest/${encodeURIComponent(v.run.id)}` : '/api/ingest')
        // a board with no pass answers with an empty run (no id), the same
        // as the 404 an older server sends: the form
        v.run = run?.id ? run : null
        v.err = null
      } catch (err) {
        if (err.status === 404) v.run = null
        else v.err = err
      }
      if (v.alive) draw()
    })

    async function loadChoices () {
      const c = { profiles: [], repos: [], envelope: 0 }
      try {
        const f = await ctx.api.get('/api/form')
        Object.assign(c, { profiles: f.profiles || [], repos: f.repos || [], envelope: f.envelope || 0 })
      } catch {
        // the form's choices are not served: the default repository needs
        // no choosing
      }
      v.choices = c
      if (v.alive) draw(true)
    }

    // a pass nobody here started, and that is over, leaves the form up
    function mode () {
      const r = v.run
      if (!r || !r.state || (r.state !== 'running' && r.state !== 'review' && r.id !== v.mine)) return 'form'
      return r.state
    }

    // draw redraws the surface; the form is left alone while it stays the
    // form, so an event arriving mid-typing does not take the caret away
    let shown = null
    function draw (force) {
      const m = mode()
      if (m === 'form' && shown === 'form' && !force) return
      shown = m
      clear(body)
      const r = v.run
      if (v.err) body.append(errorBox(v.err))
      if (m === 'form') return body.append(form())
      if (r.state === 'running') return body.append(running(r))
      if (r.state === 'review') return body.append(review(r))
      body.append(finished(r))
    }

    // ---- the form ----
    function form () {
      const c = v.choices || { profiles: [], repos: [] }
      const name = h('input', { testid: 'ingest-name', value: draft.name, placeholder: 'checkout-rework', oninput: (e) => { draft.name = e.target.value } })
      const md = h('textarea', { testid: 'ingest-markdown', value: draft.markdown, rows: 12, placeholder: '# The spec\n\n## First piece\n…', oninput: (e) => { draft.markdown = e.target.value } })
      const path = h('input', { testid: 'ingest-path', value: draft.path, placeholder: 'docs/specs/checkout.md', oninput: (e) => { draft.path = e.target.value } })
      const src = h('div', { class: 'isrc' })
      const drawSrc = () => {
        clear(src)
        if (draft.how === 'paste') {
          src.append(field('Name', name, { hint: 'saved under .gummi/ingest as <name>.md' }), field('Markdown', md, { cls: 'md' }))
        } else {
          src.append(field('Path in the workspace', path, { hint: 'relative to the repository root' }))
        }
      }
      drawSrc()
      const profile = c.profiles.length
        ? choose(c.profiles, { value: draft.profile || c.profiles[0], testid: 'ingest-profile', onchange: (e) => { draft.profile = e.target.value } })
        : null
      const repo = c.repos.length > 1
        ? choose([{ value: '', label: 'default' }, ...c.repos], { value: draft.repo, testid: 'ingest-repo', onchange: (e) => { draft.repo = e.target.value } })
        : null
      const env = h('input', { testid: 'ingest-envelope', type: 'number', min: '0', inputmode: 'numeric', value: draft.envelope, placeholder: c.envelope ? String(c.envelope) : 'default', oninput: (e) => { draft.envelope = e.target.value } })
      const start = h('button', { type: 'button', class: 'btn pri', testid: 'ingest-start', onclick: () => begin(start) }, 'Decompose')
      return h('div', { class: 'vstack', testid: 'ingest-form' },
        h('p', { class: 'vnote' }, 'An architect pass reads the document and proposes cards, with a map of which requirement each covers. Nothing is created until you approve.'),
        segmented([{ value: 'paste', label: 'Paste markdown' }, { value: 'path', label: 'Workspace file' }], draft.how,
          (x) => { draft.how = x; drawSrc() }, { testid: 'ingest-source', label: 'Source' }),
        src,
        h('div', { class: 'vrow' },
          profile ? field('Profile', profile) : null,
          repo ? field('Repository', repo) : null,
          field('Envelope per card', env, { cls: 'narrow' })),
        h('div', { class: 'vfoot' }, start))
    }

    async function begin (btn) {
      const req = {}
      if (draft.how === 'paste') {
        if (!draft.markdown.trim()) return ctx.toast('Paste the markdown to decompose', { err: true })
        req.markdown = draft.markdown
        req.name = draft.name.trim() || 'pasted'
      } else {
        if (!draft.path.trim()) return ctx.toast('Name a file in the workspace', { err: true })
        req.path = draft.path.trim()
      }
      const profiles = v.choices?.profiles || []
      if (draft.profile || profiles[0]) req.profile = draft.profile || profiles[0]
      if (draft.repo) req.repo = draft.repo
      if (String(draft.envelope).trim() !== '') req.envelope = Number(draft.envelope)
      btn.disabled = true
      try {
        v.run = await ctx.api.post('/api/ingest', req)
        v.mine = v.run.id
        v.err = null
        draft = { ...draft, markdown: '', name: '', path: '' }
      } catch (err) {
        v.err = err
      }
      btn.disabled = false
      draw(true)
    }

    // ---- running ----
    function running (r) {
      const steps = r.steps || []
      return h('div', { class: 'vstack', testid: 'ingest-running' },
        h('div', { class: 'vhead' }, h('b', null, 'Decomposing'), h('span', { class: 'src' }, r.source || '')),
        steps.length
          ? h('ol', { class: 'vsteps', testid: 'ingest-steps', 'aria-live': 'polite' }, steps.map(s => h('li', { class: s.kind, testid: 'ingest-step' }, s.text)))
          : null,
        h('div', { class: 'vbusy', testid: 'ingest-busy' }, h('span', { class: 'spinner' }), h('span', { class: 'shimmer' }, r.commentary || 'the architect is reading the document')))
    }

    // ---- review ----
    function review (r) {
      const props = r.proposals || []
      const kept = props.filter(p => !p.dropped).length
      const cov = r.coverage
      const unmapped = r.unmapped || []
      const box = h('div', { class: 'vstack', testid: 'ingest-review' },
        h('div', { class: 'vhead' },
          h('b', null, plural(props.length, 'proposal')),
          h('span', { class: 'keep', testid: 'ingest-kept' }, `${kept} kept`),
          h('span', { class: 'src' }, r.source || ''),
          h('span', { class: 'grow' }),
          r.profile ? h('span', null, `profile ${r.profile}`) : null,
          r.envelope ? h('span', null, `${r.envelope} cr each`) : null),
        cov ? coverage(cov, unmapped) : null,
        h('ol', { class: 'props', testid: 'ingest-proposals' }, props.map((p, i) => proposal(r, p, i))))
      if (v.confirm === 'approve') {
        box.append(confirmStrip({
          question: `Create ${plural(kept, 'card')} in todo?`,
          detail: unmapped.length
            ? h('span', null, h('span', { class: 'loud', testid: 'ingest-confirm-unmapped' }, `${unmapped.length} source requirement${unmapped.length === 1 ? '' : 's'} UNMAPPED`), ` · ${r.source || ''}`)
            : r.source,
          yes: 'Create cards',
          testid: 'ingest-confirm',
          onYes: () => approve(r),
          onNo: () => { v.confirm = null; draw() }
        }))
      } else if (v.confirm === 'discard') {
        box.append(confirmStrip({
          question: 'Discard this pass?',
          detail: `${plural(props.length, 'proposal')} and your edits go; the document stays in .gummi/ingest.`,
          yes: 'Discard',
          danger: true,
          testid: 'ingest-discard-confirm',
          onYes: () => discard(r),
          onNo: () => { v.confirm = null; draw() }
        }))
      } else {
        box.append(h('div', { class: 'vfoot' },
          h('button', { type: 'button', class: 'btn danger', testid: 'ingest-discard', onclick: () => { v.confirm = 'discard'; draw() } }, 'Discard'),
          h('span', { class: 'grow' }),
          h('button', { type: 'button', class: 'btn pri', testid: 'ingest-approve', disabled: kept === 0, onclick: () => { v.confirm = 'approve'; draw() } }, kept ? `Approve ${plural(kept, 'card')}` : 'Nothing kept')))
      }
      return box
    }

    function coverage (cov, unmapped) {
      return h('div', { class: ['icov', unmapped.length && 'gap'], testid: 'ingest-coverage' },
        h('div', { class: 'nums' },
          h('span', null, h('b', null, String(cov.mapped)), ' mapped'),
          h('span', null, h('b', null, String(cov.outOfScope)), ' out of scope'),
          h('span', { class: unmapped.length ? 'bad' : null, testid: 'ingest-unmapped-count' }, h('b', null, String(cov.unmapped)), ' unmapped')),
        unmapped.length
          ? h('ul', { class: 'unmapped', testid: 'ingest-unmapped' }, unmapped.map(u => h('li', null, u)))
          : null)
    }

    function proposal (r, p, i) {
      const ed = v.editing
      const titleEditing = ed && ed.i === i && ed.op === 'rename'
      const lineEditing = ed && ed.i === i && ed.op === 'oneLiner'
      const title = titleEditing
        ? editor(p.title, 'rename', i, r)
        : h('span', { class: 't', testid: 'ingest-title' }, p.title)
      const oneLiner = lineEditing
        ? editor(p.oneLiner || '', 'oneLiner', i, r)
        : (p.oneLiner ? h('div', { class: 'ol' }, p.oneLiner) : null)
      const act = (label, op, testid, extra = {}) => h('button', {
        type: 'button', class: 'mini', testid: `${testid}-${i}`, ...extra,
        onclick: () => (op === 'rename' || op === 'oneLiner') ? (v.editing = { i, op }, draw()) : edit(r, { index: i, op })
      }, label)
      return h('li', { class: ['prop', p.dropped && 'dropped'], testid: `ingest-proposal-${i}`, data: { dropped: p.dropped ? 'true' : 'false' } },
        h('span', { class: 'n' }, `${i + 1}.`),
        h('div', { class: 'pm' },
          h('div', { class: 'line' }, h('span', { class: 'kind' }, kindTag(p.kind)), title,
            p.openQuestions?.length ? h('span', { class: 'qs', title: 'open questions' }, `${p.openQuestions.length}?`) : null),
          oneLiner,
          p.sourceRefs?.length ? h('div', { class: 'meta' }, 'from ', p.sourceRefs.join(', ')) : null,
          p.dependsOn?.length ? h('div', { class: 'meta' }, 'needs ', p.dependsOn.join(', ')) : null,
          p.problem || p.openQuestions?.length
            ? h('details', { class: 'more' }, h('summary', null, 'problem and questions'),
              p.problem ? h('p', null, p.problem) : null,
              (p.openQuestions || []).length ? h('ul', null, p.openQuestions.map(q => h('li', null, q))) : null)
            : null),
        h('div', { class: 'acts' },
          p.dropped
            ? act('Keep', 'undrop', 'ingest-undrop')
            : [act('Rename', 'rename', 'ingest-rename'), act('One-liner', 'oneLiner', 'ingest-oneliner'),
                i > 0 ? act('Merge up', 'merge', 'ingest-merge', { title: 'Fold into the proposal above' }) : null,
                act('Drop', 'drop', 'ingest-drop')]))
    }

    function editor (value, op, i, r) {
      const input = h('input', { class: 'ied', testid: `ingest-edit-${op}`, value, 'aria-label': op === 'rename' ? 'Title' : 'One-liner' })
      const save = () => {
        const text = input.value.trim()
        v.editing = null
        if (op === 'rename' && (!text || text === value)) return draw()
        if (op === 'oneLiner' && text === value) return draw()
        edit(r, op === 'rename' ? { index: i, op, title: text } : { index: i, op, oneLiner: text })
      }
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); save() }
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); v.editing = null; draw() }
      })
      requestAnimationFrame(() => { input.focus(); input.select() })
      return h('span', { class: 'iedw' }, input,
        h('button', { type: 'button', class: 'mini', testid: `ingest-edit-save`, onclick: save }, 'Save'))
    }

    async function edit (r, req) {
      try {
        v.run = await ctx.api.post(`/api/ingest/${encodeURIComponent(r.id)}/edit`, req)
        v.err = null
      } catch (err) { v.err = err }
      draw()
    }

    async function approve (r) {
      try {
        v.run = await ctx.api.post(`/api/ingest/${encodeURIComponent(r.id)}/approve`, {})
        v.err = null
      } catch (err) {
        v.err = err
        if (err.data?.id) v.run = err.data
      }
      v.mine = v.run?.id
      v.confirm = null
      ctx.refreshBoard?.()
      draw()
    }

    async function discard (r) {
      try {
        v.run = await ctx.api.post(`/api/ingest/${encodeURIComponent(r.id)}/discard`, {})
        v.err = null
      } catch (err) { v.err = err }
      v.confirm = null
      v.mine = null
      draw()
    }

    // ---- done ----
    function finished (r) {
      const created = r.created || []
      const out = h('div', { class: 'vstack', testid: 'ingest-done', data: { state: r.state } })
      if (r.state === 'failed') out.append(h('div', { class: 'verr', testid: 'ingest-failed' }, r.error || 'the pass failed'))
      if (r.state === 'discarded') out.append(h('p', { class: 'vnote' }, 'The pass was discarded.'))
      if (created.length) {
        out.append(h('div', { class: 'vhead' }, h('b', null, `Created ${plural(created.length, 'card')} in todo`), h('span', { class: 'src' }, r.source || '')),
          cardLinks(created, ctx, 'ingest-created'))
      } else if (r.state === 'materialized') {
        out.append(h('p', { class: 'vnote' }, 'The pass finished with nothing to create.'))
      }
      out.append(h('div', { class: 'vfoot' },
        h('button', { type: 'button', class: 'btn', testid: 'ingest-again', onclick: () => { v.mine = null; v.run = null; v.err = null; draw() } }, 'Import another')))
      return out
    }

    const offs = [
      ctx.onEvent('ingest', (c) => {
        // a newer pass (started anywhere) replaces the one on the page
        if (c.id && v.run?.id !== c.id) v.run = { ...(v.run || {}), id: c.id }
        load()
      }),
      ctx.onEvent('resync', () => load())
    ]
    draw()
    loadChoices()
    load()
    return () => { v.alive = false; offs.forEach(off => off()) }
  }
})

function kindTag (kind) {
  return { feature: 'FD', bug: 'BG', research: 'RS' }[kind] || (kind || 'FD').slice(0, 2).toUpperCase()
}
