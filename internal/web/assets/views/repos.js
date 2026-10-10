// views/repos.js — the repositories this workspace manages (DESIGN §10
// decision 10), each with its local branches sorted by who holds them:
// the base, the cards', the ones a card adopted (held, never deleted),
// the goals', and the ones no card claims. From here a person fetches,
// fast-forwards a base to its upstream, cleans up landed cards (the
// card's own clean action) and deletes a branch nobody holds. The
// repository's remotes are listed and edited here too — add, rename,
// point elsewhere, remove — along with which remote branch a local one
// tracks: writes to the repository's own git config, and nothing more.
// The view never pushes: a branch with unpushed commits shows the line to run.

import { h, clear, plural, icon, GLYPH, stageVar } from '../dom.js?v=__ASSET_V__'
import { registerView, openMenu } from '../views.js?v=__ASSET_V__'
import { cardPath } from '../api.js?v=__ASSET_V__'
import { errorBox, confirmStrip, refetcher, field, choose } from './kit.js?v=__ASSET_V__'

const GROUPS = [
  ['base', 'Base'],
  ['cards', 'Cards'],
  ['held', 'Held'],
  ['goals', 'Goals'],
  ['unowned', 'Unowned']
]
const GROUP_HINT = {
  held: 'adopted by a card — gummi adds commits and never deletes one',
  unowned: 'no card on this board holds these'
}

function debounce (fn, ms = 250) {
  let t = 0
  return () => { clearTimeout(t); t = setTimeout(fn, ms) }
}

function errText (err) {
  if (err?.notBuilt) return 'this board does not serve repositories yet'
  return err?.data?.text || err?.message || String(err)
}

// ago words a past moment loosely: a fetch or a commit, not a clock.
function ago (t) {
  const d = new Date(t)
  if (!t || isNaN(d) || d.getFullYear() < 2000) return ''
  const s = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (s < 90) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  if (s < 86400 * 14) return plural(Math.round(s / 86400), 'day') + ' ago'
  if (s < 86400 * 70) return plural(Math.round(s / 86400 / 7), 'week') + ' ago'
  return plural(Math.round(s / 86400 / 30), 'month') + ' ago'
}

const label = (repo) => repo.name || 'default'
const key = (repo) => repo.name || 'default'

// track words a branch against its upstream.
function track (b) {
  const mark = (tone, title, text) => h('span', { class: ['rp-mark', tone], title, testid: `branch-track-${b.name}` }, text)
  if (!b.upstream) return mark('t-mute', 'tracks no remote branch', 'local only')
  if (b.gone) return mark('t-warn', `${b.upstream} is gone from the remote`, 'upstream gone')
  if (!b.ahead && !b.behind) return mark('t-ok', `level with ${b.upstream}`, 'pushed')
  return mark(b.behind ? 't-warn' : 't-run', `against ${b.upstream}`, [b.ahead && `↑${b.ahead}`, b.behind && `↓${b.behind}`].filter(Boolean).join(' '))
}

// rowMenu is a row's "⋯": everything that can be done to the remote or
// branch on it, so a row reads as what it is rather than as its buttons.
// Nothing when the row has no action.
function rowMenu (what, name, items, { busy, testid } = {}) {
  items = items.filter(Boolean)
  while (items.at(-1) === 'sep') items.pop()
  if (!items.length || items[0] === 'sep') return null
  const btn = h('button', { class: 'iconbtn rp-more', type: 'button', disabled: !!busy, title: `Actions for ${name}`, 'aria-label': `Actions for the ${what} ${name}`, 'aria-haspopup': 'menu', 'aria-expanded': 'false', testid }, icon('more'))
  btn.addEventListener('click', () => openMenu(btn, items, { testid: `${testid}-items` }))
  return btn
}

registerView('repos', {
  title: 'Repositories',
  css: 'views/repos.css',
  mount (body, ctx) {
    body.classList.add('rp')
    const v = { data: null, err: null, loading: true, alive: true, busy: new Map(), ask: null, holds: 0, stale: false }
    const list = h('div', { class: 'rp-list', testid: 'repos-list' })
    const count = h('span', { class: 'rp-count', testid: 'repos-count' })
    const rescan = h('button', { class: 'btn', type: 'button', testid: 'repos-rescan', title: 'Read the repositories and their branches again', onclick: () => load() }, 'Rescan')
    const fetchAll = h('button', { class: 'btn', type: 'button', testid: 'repos-fetch-all', onclick: () => write('*', 'fetching…', '/api/repos/fetch', { all: true }) }, 'Fetch all')
    body.append(
      h('div', { class: 'rp-bar' }, count, rescan, fetchAll),
      h('p', { class: 'rp-lede' }, 'Fetch reads the remotes; fast-forward moves a base up to what was fetched. Remotes, and the remote branch a local one tracks, are edited in the repository’s own git config. Nothing here pushes — a branch with unpushed commits shows the line to run.'),
      list)

    const load = refetcher(async () => {
      try {
        const data = await ctx.api.get('/api/repos')
        v.data = data
        v.err = null
      } catch (err) { v.err = err }
      v.loading = false
      if (!v.alive) return
      // a read that lands while a question is open would redraw it and
      // drop what was typed into it: it is drawn when the question closes
      if (v.ask) { v.stale = true } else draw()
    })

    // a refresh waits while a question is open: redrawing would drop it
    const refresh = debounce(() => { if (v.ask) { v.stale = true } else { load() } })

    async function write (busyKey, word, path, req) {
      if (v.busy.has(busyKey)) return
      v.busy.set(busyKey, word)
      draw()
      try {
        const out = await ctx.api.post(path, req)
        if (out?.text) ctx.toast(out.text)
      } catch (err) { ctx.toast(errText(err), { err: true }) }
      v.busy.delete(busyKey)
      await load()
    }

    // cleanCard sends the card's own clean action. The server asks its
    // question first (a 409 carrying a token); the person has answered it
    // on this page already, so the token goes straight back.
    async function cleanCard (id) {
      const path = cardPath(id, 'actions/clean')
      try {
        await ctx.api.post(path, {})
      } catch (err) {
        if (err.status === 409 && (err.data?.error === 'confirm' || err.data?.needs === 'confirm')) {
          await ctx.api.post(path, { confirm: err.data.confirm || '' })
          return
        }
        throw err
      }
    }

    async function clean (repo, ids) {
      const busyKey = key(repo)
      v.busy.set(busyKey, 'cleaning up…')
      draw()
      const failed = []
      for (const id of ids) {
        try { await cleanCard(id) } catch (err) { failed.push(`${id}: ${errText(err)}`) }
      }
      v.busy.delete(busyKey)
      if (failed.length) ctx.toast(failed.join(' · '), { err: true })
      else ctx.toast(`cleaned up ${ids.join(', ')}`)
      await load()
    }

    const ask = (a) => { v.ask = a; draw() }
    const unask = () => {
      v.ask = null
      draw()
      if (v.stale) { v.stale = false; load() }
    }

    function draw () {
      const data = v.data
      const repos = data?.repos || []
      count.textContent = data ? plural(repos.length, 'repository', 'repositories') + (data.discovered ? ' · discovered' : '') : ''
      fetchAll.disabled = v.busy.has('*') || !repos.some(r => r.remote)
      fetchAll.textContent = v.busy.has('*') ? 'Fetching…' : 'Fetch all'
      fetchAll.hidden = repos.length < 2
      // which repositories a person folded, kept across the redraw
      const folded = new Set([...list.querySelectorAll('details.rp-box:not([open])')].map(d => d.dataset.repo))
      clear(list)
      if (v.err) list.append(errorBox(v.err, 'repos-error'))
      if (!data) {
        if (v.loading) list.append(h('div', { class: 'empty', testid: 'repos-loading' }, h('span', { class: 'spinner' })))
        return
      }
      for (const c of data.ambiguous || []) {
        list.append(h('div', { class: 'rp-clash', role: 'note', testid: `repos-clash-${c.name}` },
          h('b', null, `“${c.name}” names ${plural(c.paths.length, 'checkout')}`),
          h('span', null, `${c.paths.join(', ')} — none of them is offered. Pin the one you mean under `),
          h('code', null, 'repos:'), h('span', null, ' in .gummi/config.yaml.')))
      }
      if (!repos.length) {
        list.append(h('div', { class: 'empty', testid: 'repos-empty' }, h('b', null, 'No repository'), 'This workspace manages no git checkout yet.'))
        return
      }
      for (const repo of repos) list.append(repoBox(repo, !folded.has(key(repo))))
    }

    function repoBox (repo, open) {
      const k = key(repo)
      const busy = v.busy.get(k) || (v.busy.has('*') && repo.remote && v.busy.get('*'))
      const base = (repo.branches || []).find(b => b.group === 'base')
      const cleanable = (repo.branches || []).filter(b => b.card?.clean).map(b => b.card.id)
      const canFF = base && base.upstream && !base.gone && base.behind > 0 && !base.ahead
      const head = h('summary', { class: 'rp-head', testid: `repo-head-${k}` },
        h('span', { class: 'rp-title' },
          h('b', { class: !repo.name && 'default' }, label(repo)),
          h('span', { class: 'rp-path mono' }, repo.path === '.' ? 'the workspace root' : repo.path || '')),
        h('span', { class: 'rp-meta' },
          repo.base ? h('span', { class: 'mono rp-basename', title: 'the branch its main checkout has out' }, repo.base) : h('span', { class: 'rp-mark t-warn' }, 'detached'),
          base ? track(base) : null,
          repo.dirty ? h('span', { class: 'rp-mark t-warn', title: 'the main checkout has uncommitted changes to tracked files', testid: `repo-dirty-${k}` }, 'uncommitted changes') : null,
          h('span', null, plural(repo.cards || 0, 'open card')),
          repo.remote ? h('span', { testid: `repo-fetched-${k}` }, repo.fetched ? `fetched ${ago(repo.fetched)}` : 'never fetched') : h('span', null, 'no remote')))
      const actions = h('div', { class: 'rp-actions' },
        busy ? h('span', { class: 'vbusy' }, h('span', { class: 'spinner' }), busy) : null,
        h('button', { class: 'btn', type: 'button', testid: `repo-fetch-${k}`, disabled: !!busy || !repo.remote, title: repo.remote ? 'git fetch --all --prune' : 'this repository has no remote', onclick: () => write(k, 'fetching…', '/api/repos/fetch', { repo: repo.name }) }, 'Fetch'),
        canFF
          ? h('button', { class: 'btn pri', type: 'button', testid: `repo-ff-${k}`, disabled: !!busy || repo.dirty, title: repo.dirty ? 'commit or stash the main checkout’s changes first' : `move ${base.name} up to ${base.upstream}`, onclick: () => write(k, 'fast-forwarding…', '/api/repos/fastforward', { repo: repo.name }) }, `Fast-forward ${base.name} ↓${base.behind}`)
          : null,
        cleanable.length
          ? h('button', { class: 'btn', type: 'button', testid: `repo-clean-${k}`, disabled: !!busy, onclick: () => ask({ kind: 'clean', repo: k, ids: cleanable }) }, `Clean up ${plural(cleanable.length, 'landed card')}`)
          : null)
      const box = h('details', { class: 'rp-box', open, data: { repo: k }, testid: `repo-${k}` }, head,
        h('div', { class: 'rp-body' },
          actions))
      const bodyEl = box.lastChild
      if (base?.ahead && base?.behind) {
        bodyEl.append(h('p', { class: 'vnote warn', testid: `repo-diverged-${k}` }, `${base.name} and ${base.upstream} have diverged (↑${base.ahead} ↓${base.behind}); that takes a merge or a rebase, which is yours to choose.`))
      }
      if (v.ask?.kind === 'clean' && v.ask.repo === k) {
        const ids = v.ask.ids
        bodyEl.append(confirmStrip({
          question: `Clean up ${plural(ids.length, 'landed card')}?`,
          detail: `Removes the worktree and the branch of ${ids.join(', ')}. Their work is on the base; the cards stay on the board.`,
          yes: 'Clean up',
          testid: 'repo-clean-confirm',
          onYes: async () => { v.ask = null; await clean(repo, ids) },
          onNo: unask
        }))
      }
      if (repo.error) {
        bodyEl.append(h('div', { class: 'verr', role: 'alert', testid: `repo-error-${k}` }, repo.error))
        return box
      }
      bodyEl.append(remotesSection(repo, !!busy))
      for (const [g, title] of GROUPS) {
        const rows = (repo.branches || []).filter(b => b.group === g)
        if (!rows.length) continue
        bodyEl.append(h('section', { class: 'rp-group', testid: `repo-${k}-${g}` },
          h('h4', null, title, g !== 'base' ? h('span', { class: 'n' }, String(rows.length)) : null,
            GROUP_HINT[g] ? h('span', { class: 'hint' }, GROUP_HINT[g]) : null),
          h('ul', { class: 'rp-branches' }, rows.map(b => branchRow(repo, b)))))
      }
      return box
    }

    function branchRow (repo, b) {
      const k = key(repo)
      const asking = v.ask?.kind === 'delete' && v.ask.repo === k && v.ask.branch === b.name
      const c = b.card
      const marks = [
        c ? h('span', { class: 'rp-stage', style: { '--sc': stageVar(c.stage) } }, `${GLYPH[c.stage] || ''} ${c.stage}`.trim()) : null,
        c?.landed ? h('span', { class: 'rp-mark t-ok' }, 'landed') : null,
        b.group !== 'base' && b.aheadBase ? h('span', { class: 'rp-n', title: `${plural(b.aheadBase, 'commit')} ${repo.base || 'the base'} lacks` }, `+${b.aheadBase}`) : null,
        b.group !== 'base' && b.behindBase ? h('span', { class: 'rp-n', title: `${plural(b.behindBase, 'commit')} behind ${repo.base || 'the base'}` }, `behind ${b.behindBase}`) : null,
        b.group === 'unowned' && !b.aheadBase ? h('span', { class: 'rp-mark t-ok', title: `${repo.base || 'the base'} has everything on it` }, 'merged') : null,
        track(b),
        b.forkedBy?.length ? h('span', { class: 'rp-n' }, `base of ${b.forkedBy.join(', ')}`) : null,
        b.at ? h('span', { class: 'rp-age' }, ago(b.at)) : null
      ]
      const act = rowMenu('branch', b.name, [
        repo.remote && { label: b.upstream ? 'Change upstream…' : 'Set upstream…', hint: 'which remote branch it tracks', testid: `branch-upstream-${b.name}`, onClick: () => ask({ kind: 'upstream', repo: k, branch: b.name }) },
        c?.clean && { label: 'Clean up…', hint: 'remove the landed card’s worktree and branch', testid: `branch-clean-${c.id}`, onClick: () => ask({ kind: 'clean', repo: k, ids: [c.id] }) },
        b.delete && { label: 'Delete…', danger: true, testid: `branch-delete-${b.name}`, onClick: () => ask({ kind: 'delete', repo: k, branch: b.name }) }
      ], { busy: v.busy.has(k), testid: `branch-menu-${b.name}` })
      const li = h('li', { class: ['rp-branch', b.group], testid: `branch-${b.name}`, data: { group: b.group } },
        h('div', { class: 'rp-row' },
          h('div', { class: 'rp-main' },
            h('span', { class: 'rp-name mono' }, b.name),
            c ? h('button', { class: 'link rp-card', type: 'button', testid: `branch-card-${c.id}`, title: c.title, onclick: () => { ctx.close(); ctx.select(c.id) } }, c.id) : null,
            c ? h('span', { class: 'rp-t' }, c.title) : h('span', { class: 'rp-t' }, b.subject || '')),
          h('div', { class: 'rp-marks' }, marks),
          h('div', { class: 'rp-act' }, act)))
      if (b.why) li.append(h('div', { class: 'rp-why' }, b.why))
      if (b.push) li.append(pushLine(b.push, ctx))
      if (v.ask?.kind === 'upstream' && v.ask.repo === k && v.ask.branch === b.name) li.append(upstreamForm(repo, b))
      if (asking) li.append(b.delete === 'confirm' ? typedDelete(repo, b) : confirmStrip({
        question: `Delete ${b.name}?`,
        detail: `${repo.base || 'The base'} has everything on it. This deletes the local branch only; nothing is pushed.`,
        yes: 'Delete branch',
        danger: true,
        testid: 'branch-delete-confirm',
        onYes: async () => { v.ask = null; await write(k, 'deleting…', '/api/repos/branches/delete', { repo: repo.name, branch: b.name }) },
        onNo: unask
      }))
      return li
    }

    // remotesSection lists the repository's remotes, each with its own
    // fetch and the three edits, and the form that adds one.
    function remotesSection (repo, busy) {
      const k = key(repo)
      const remotes = repo.remotes || []
      const asking = (kind, name) => v.ask?.kind === kind && v.ask.repo === k && v.ask.remote === name
      const sec = h('section', { class: 'rp-group rp-remotes', testid: `repo-${k}-remotes` },
        h('h4', null, 'Remotes', remotes.length ? h('span', { class: 'n' }, String(remotes.length)) : null,
          !remotes.length ? h('span', { class: 'hint' }, 'none — nothing to fetch from or push to') : null,
          h('button', { class: 'btn rp-add', type: 'button', testid: `remote-add-${k}`, disabled: busy, title: 'Add a remote', 'aria-label': 'Add a remote', onclick: () => ask({ kind: 'remote-add', repo: k }) }, '+')))
      if (remotes.length) {
        sec.append(h('ul', { class: 'rp-branches' }, remotes.map(r => {
          const li = h('li', { class: 'rp-branch', testid: `remote-${r.name}` },
            h('div', { class: 'rp-row rp-remote' },
              h('div', { class: 'rp-main' },
                h('span', { class: 'rp-name mono' }, r.name),
                h('span', { class: 'rp-url mono', testid: `remote-url-${r.name}`, title: r.secret ? 'the URL carries a credential, which is not shown' : null }, r.url)),
              h('div', { class: 'rp-marks' },
                r.secret ? h('span', { class: 'rp-mark t-mute' }, 'credential hidden') : null,
                h('span', { class: 'rp-n' }, r.tracking ? `${plural(r.tracking, 'branch', 'branches')} ${r.tracking === 1 ? 'tracks' : 'track'} it` : 'no branch tracks it')),
              h('div', { class: 'rp-act' }, rowMenu('remote', r.name, [
                { label: 'Fetch', hint: `git fetch --prune ${r.name}`, testid: `remote-fetch-${r.name}`, onClick: () => write(k, `fetching ${r.name}…`, '/api/repos/fetch', { repo: repo.name, remote: r.name }) },
                { label: 'Rename…', testid: `remote-rename-${r.name}`, onClick: () => ask({ kind: 'remote-rename', repo: k, remote: r.name }) },
                { label: 'Change URL…', testid: `remote-seturl-${r.name}`, onClick: () => ask({ kind: 'remote-url', repo: k, remote: r.name }) },
                'sep',
                { label: 'Remove…', danger: true, testid: `remote-remove-${r.name}`, onClick: () => ask({ kind: 'remote-remove', repo: k, remote: r.name }) }
              ], { busy, testid: `remote-menu-${r.name}` }))))
          if (r.pushUrl) li.append(h('div', { class: 'rp-why mono', testid: `remote-pushurl-${r.name}` }, `pushes to ${r.pushUrl}`))
          if (asking('remote-rename', r.name)) {
            li.append(formStrip({
              question: `Rename ${r.name}`,
              detail: 'Branches that track it keep tracking it under the new name.',
              fields: [{ id: 'newName', label: 'New name', value: r.name, mono: true }],
              yes: 'Rename',
              testid: 'remote-rename',
              ready: f => f.newName && f.newName !== r.name,
              onYes: f => write(k, 'renaming…', '/api/repos/remotes/rename', { repo: repo.name, remote: r.name, newName: f.newName })
            }))
          }
          if (asking('remote-url', r.name)) {
            li.append(formStrip({
              question: `Where ${r.name} points`,
              detail: (r.secret ? 'The URL in place carries a credential and is not shown; type the whole new one. ' : '') + (r.pushUrl ? 'Its separate push URL stays as it is.' : ''),
              // a masked URL is not one to edit: saving it back would write the mask
              fields: [{ id: 'url', label: 'URL', value: r.secret ? '' : r.url, mono: true, wide: true }],
              yes: 'Save URL',
              testid: 'remote-seturl',
              ready: f => f.url && f.url !== r.url,
              onYes: f => write(k, 'saving…', '/api/repos/remotes/seturl', { repo: repo.name, remote: r.name, url: f.url })
            }))
          }
          if (asking('remote-remove', r.name)) {
            const losing = (repo.branches || []).filter(b => b.remote === r.name).map(b => b.name)
            li.append(confirmStrip({
              question: `Remove the remote ${r.name}?`,
              detail: (losing.length
                ? `${losing.join(', ')} ${losing.length === 1 ? 'tracks' : 'track'} it and will track nothing afterwards. `
                : 'No local branch tracks it. ') + 'Its fetched copies of the remote’s branches go; no local branch or commit of yours does, and nothing changes on the remote.',
              yes: 'Remove remote',
              danger: true,
              testid: 'remote-remove-confirm',
              onYes: async () => { v.ask = null; await write(k, 'removing…', '/api/repos/remotes/remove', { repo: repo.name, remote: r.name }) },
              onNo: unask
            }))
          }
          return li
        })))
      }
      if (v.ask?.kind === 'remote-add' && v.ask.repo === k) {
        sec.append(formStrip({
          question: 'Add a remote',
          detail: 'Recorded in this repository’s git config. Nothing is fetched until you ask.',
          fields: [
            { id: 'remote', label: 'Name', value: remotes.length ? '' : 'origin', mono: true },
            { id: 'url', label: 'URL', value: '', mono: true, wide: true, placeholder: 'git@host:owner/repo.git' }
          ],
          yes: 'Add remote',
          testid: 'remote-add',
          ready: f => f.remote && f.url,
          onYes: f => write(k, 'adding…', '/api/repos/remotes/add', { repo: repo.name, remote: f.remote, url: f.url })
        }))
      }
      return sec
    }

    // formStrip is confirmStrip with fields: a question answered by
    // typing, in place where it was asked.
    function formStrip ({ question, detail, fields, yes, testid, ready, onYes }) {
      const inputs = fields.map(f => h('input', { type: 'text', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', value: f.value || '', placeholder: f.placeholder, class: f.mono && 'mono', testid: `${testid}-${f.id}` }))
      const values = () => Object.fromEntries(fields.map((f, i) => [f.id, inputs[i].value.trim()]))
      const go = h('button', { class: 'btn pri', type: 'submit', testid: `${testid}-yes` }, yes)
      const check = () => { go.disabled = !ready(values()) }
      const form = h('form', { class: 'vconfirm rp-form', 'aria-label': question, testid },
        h('div', { class: 'q' }, h('b', null, question), detail ? h('div', { class: 'd' }, detail) : null,
          h('div', { class: 'vrow' }, fields.map((f, i) => field(f.label, inputs[i], { cls: !f.wide && 'narrow', testid: null })))),
        h('div', { class: 'acts' }, h('button', { class: 'btn', type: 'button', testid: `${testid}-no`, onclick: unask }, 'Cancel'), go))
      for (const input of inputs) input.addEventListener('input', check)
      form.addEventListener('submit', async (e) => {
        e.preventDefault()
        const f = values()
        if (!ready(f)) return
        v.ask = null
        await onYes(f)
      })
      check()
      requestAnimationFrame(() => { if (form.isConnected) { form.scrollIntoView({ block: 'nearest' }); inputs[0].focus() } })
      return form
    }

    // upstreamForm picks the remote branch a local one tracks, from the
    // ones last fetched, or none.
    function upstreamForm (repo, b) {
      const k = key(repo)
      const known = repo.remoteBranches || []
      const NONE = ''
      const options = [{ value: NONE, label: 'nothing (local only)' }, ...known.map(n => ({ value: n, label: n }))]
      // a gone upstream is still what it tracks, though no longer a choice
      if (b.upstream && !known.includes(b.upstream)) options.push({ value: b.upstream, label: `${b.upstream} (gone)` })
      const same = (repo.remotes || []).map(r => `${r.name}/${b.name}`).find(n => known.includes(n))
      const select = choose(options, { value: b.upstream || same || NONE, label: `What ${b.name} tracks`, testid: 'branch-upstream-pick' })
      const go = h('button', { class: 'btn pri', type: 'submit', testid: 'branch-upstream-yes' }, 'Save')
      const check = () => { go.disabled = select.value === (b.upstream || NONE) }
      select.addEventListener('change', check)
      const unpublished = !known.some(n => n.endsWith('/' + b.name))
      const form = h('form', { class: 'vconfirm rp-form', 'aria-label': `What ${b.name} tracks`, testid: 'branch-upstream' },
        h('div', { class: 'q' }, h('b', null, `What ${b.name} tracks`),
          h('div', { class: 'd' }, 'The remote branch it is compared with, and where a plain push of it goes. The branch itself does not move.' +
            (unpublished ? ` No remote has a ${b.name} as last fetched: one appears here after its first push (git push -u <remote> ${b.name}) and a fetch.` : '')),
          h('div', { class: 'vrow' }, field('Tracks', select, { testid: null }))),
        h('div', { class: 'acts' }, h('button', { class: 'btn', type: 'button', testid: 'branch-upstream-no', onclick: unask }, 'Cancel'), go))
      form.addEventListener('submit', async (e) => {
        e.preventDefault()
        if (go.disabled) return
        v.ask = null
        await write(k, 'saving…', '/api/repos/branches/upstream', { repo: repo.name, branch: b.name, upstream: select.value })
      })
      check()
      requestAnimationFrame(() => { if (form.isConnected) { form.scrollIntoView({ block: 'nearest' }); select.focus() } })
      return form
    }

    // typedDelete asks for the branch's name before deleting one that
    // holds commits the base lacks: they exist nowhere else once it goes.
    function typedDelete (repo, b) {
      const input = h('input', { type: 'text', autocomplete: 'off', spellcheck: 'false', 'aria-label': `Type ${b.name} to delete it`, placeholder: b.name, testid: 'branch-delete-name' })
      const go = h('button', {
        class: 'btn pri danger',
        type: 'button',
        disabled: true,
        testid: 'branch-delete-force',
        onclick: async () => { v.ask = null; await write(key(repo), 'deleting…', '/api/repos/branches/delete', { repo: repo.name, branch: b.name, force: true }) }
      }, 'Delete branch')
      input.addEventListener('input', () => { go.disabled = input.value.trim() !== b.name })
      const lost = b.upstream && !b.gone && !b.ahead
        ? `Its ${plural(b.aheadBase, 'commit')} ${repo.base || 'the base'} lacks ${b.aheadBase === 1 ? 'is' : 'are'} still on ${b.upstream}.`
        : `Its ${plural(b.aheadBase, 'commit')} ${repo.base || 'the base'} lacks will only be reachable through the reflog.`
      const strip = h('div', { class: 'vconfirm danger rp-typed', role: 'alertdialog', 'aria-label': `Delete ${b.name}?`, testid: 'branch-delete-typed' },
        h('div', { class: 'q' }, h('b', null, `Delete ${b.name}?`), h('div', { class: 'd' }, `${lost} Type the branch’s name to delete it.`), input),
        h('div', { class: 'acts' }, h('button', { class: 'btn', type: 'button', testid: 'branch-delete-no', onclick: unask }, 'Cancel'), go))
      requestAnimationFrame(() => { if (input.isConnected) input.focus() })
      return strip
    }

    const off = ctx.onEvent('board', refresh)
    load()
    return () => { v.alive = false; off?.() }
  }
})

function pushLine (line, ctx) {
  const code = h('code', { class: 'rp-cmd', testid: 'branch-push-line', tabindex: '0' }, line)
  return h('div', { class: 'rp-cmdrow' },
    h('span', { class: 'rp-cmdlbl' }, 'unpushed — run'), code,
    h('button', { class: 'btn rp-copy', type: 'button', testid: 'branch-push-copy', onclick: () => copy(line, code, ctx) }, 'Copy'))
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
