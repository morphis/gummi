// views/agentplugins.js — workspace-local agent definitions and skills.
import { h, clear } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { errorBox, field, choose, confirmStrip } from './kit.js?v=__ASSET_V__'

const SKILL_STARTER = '---\nname: new-skill\ndescription: Describe when this skill should be used.\n---\n\n# Skill instructions\n'
const AGENT_STARTER = '---\nname: new-agent\ndescription: Describe this agent and when to use it.\n---\n\n# Agent instructions\n'

function slug (value) {
  return value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

registerView('agentplugins', {
  title: 'Agent Plugins',
  css: 'views/agentplugins.css',
  wide: true,
  mount (body, ctx) {
    const v = {
      tab: 'agents',
      items: [],
      providers: [],
      candidates: [],
      selected: new Set(),
      editor: null,
      confirmDelete: null,
      err: null,
      scanning: false,
      busy: false,
      // UI filters for the candidate list
      candidateFilter: '',
      candidateRepo: '',
      alive: true
    }
    body.classList.add('vplugins')

    async function refresh () {
      try {
        const data = await ctx.api.get('/api/plugins')
        v.items = data.items || []
        v.providers = data.providers || []
        v.err = null
      } catch (err) { v.err = err }
      if (v.alive) draw()
    }

    function kindTitle () { return v.tab === 'skills' ? 'skill' : 'agent' }
    function kindAPI () { return v.tab === 'skills' ? 'skill' : 'agent' }

    function beginCreate () {
      v.editor = { id: '', name: '', content: v.tab === 'skills' ? SKILL_STARTER : AGENT_STARTER }
      draw()
    }

    async function beginEdit (item) {
      v.busy = true
      draw()
      try {
        const detail = await ctx.api.get(`/api/plugins/${encodeURIComponent(item.id)}`)
        v.editor = { ...detail.item, content: detail.content }
        v.err = null
      } catch (err) { v.err = err }
      v.busy = false
      draw()
    }

    async function saveEditor (name, content, button) {
      if (!name.value.trim()) { name.focus(); return }
      button.disabled = true
      v.busy = true
      draw()
      try {
        const itemName = name.value.trim()
        const starterName = kindAPI() === 'skill' ? 'new-skill' : 'new-agent'
        const defaultFrontmatter = `---\nname: ${starterName}\n`
        const itemContent = !v.editor.id && content.value.startsWith(defaultFrontmatter)
          ? content.value.replace(defaultFrontmatter, `---\nname: ${slug(itemName)}\n`)
          : content.value
        const req = { name: itemName, content: itemContent }
        if (v.editor.id) {
          await ctx.api.api('PUT', `/api/plugins/${encodeURIComponent(v.editor.id)}`, req)
        } else {
          await ctx.api.post('/api/plugins', { ...req, kind: kindAPI() })
        }
        v.editor = null
        v.err = null
        v.busy = false
        await refresh()
        ctx.toast('Saved to the workspace plugin library')
      } catch (err) {
        v.err = err
        v.busy = false
        draw()
      }
    }

    function editorForm () {
      const item = v.editor
      const name = h('input', {
        value: item.name || '',
        placeholder: v.tab === 'skills' ? 'review-guidelines' : 'code-reviewer',
        maxlength: '80',
        testid: 'plugins-editor-name'
      })
      const content = h('textarea', {
        rows: 15,
        spellcheck: false,
        value: item.content,
        testid: 'plugins-editor-content'
      })
      const save = h('button', {
        class: 'btn pri', type: 'button', testid: 'plugins-save',
        disabled: v.busy,
        onclick: () => saveEditor(name, content, save)
      }, item.id ? 'Save changes' : `Create ${kindTitle()}`)
      return h('section', { class: 'peditor', testid: 'plugins-editor' },
        h('div', { class: 'psectionhead' },
          h('h3', null, item.id ? `Edit ${kindTitle()}` : `New ${kindTitle()}`),
          item.linked ? h('span', { class: 'plinked' }, 'Linked source · saving makes a workspace copy') : null),
        field('Name', name),
        field(v.tab === 'skills' ? 'SKILL.md' : 'Agent Markdown', content, {
          hint: v.tab === 'skills'
            ? 'Supporting files in an imported skill are preserved when you edit its instructions.'
            : 'Use a provider-compatible *.agent.md or AGENTS.md definition.'
        }),
        h('div', { class: 'pactions' },
          h('button', { class: 'btn', type: 'button', testid: 'plugins-cancel-edit', onclick: () => { v.editor = null; draw() } }, 'Cancel'),
          save))
    }

    function itemCard (item) {
      const titleOnly = h('strong', null, item.name)
      const tooltip = item.description || item.sourcePath || ''
      const title = tooltip ? h('abbr', { title: tooltip }, titleOnly) : titleOnly
      return h('article', { class: 'pitem', testid: `plugins-item-${item.id}` },
        h('div', { class: 'pitemmain' },
          h('div', { class: 'pitemtitle' }, title, item.linked ? h('span', { class: 'ptag' }, 'linked') : null)),
        v.tab === 'skills' ? providerBadges() : null,
        h('div', { class: 'pitemactions' },
          h('button', { class: 'btn', type: 'button', testid: `plugins-edit-${item.id}`, disabled: v.busy, onclick: () => beginEdit(item) }, 'Edit'),
          h('button', { class: 'btn', type: 'button', testid: `plugins-export-${item.id}`, onclick: () => exportItem(item) }, 'Export'),
          h('button', { class: 'btn danger', type: 'button', testid: `plugins-delete-${item.id}`, onclick: () => { v.confirmDelete = item; draw() } }, 'Remove')),
        v.confirmDelete?.id === item.id ? confirmStrip({
          question: `Remove ${item.name} from the workspace library?`,
          detail: 'This removes the local entry or link, not the source file in its repository.',
          yes: 'Remove',
          danger: true,
          testid: `plugins-confirm-delete-${item.id}`,
          onYes: () => removeItem(item),
          onNo: () => { v.confirmDelete = null; draw() }
        }) : null)
    }

    function providerBadges () {
      return h('div', { class: 'pproviders' },
        v.providers.map(p =>
          h('span', {
            class: ['pprovider', p.skillDirs ? 'supported' : 'unsupported'],
            title: p.skillDetail,
            testid: `plugins-provider-${p.name}`
          }, h('i', { 'aria-hidden': 'true' }, p.skillDirs ? '✓' : '—'), p.name)))
    }

    function itemsForTab () {
      const kind = v.tab === 'skills' ? 'skill' : 'agent'
      return v.items.filter(item => item.kind === kind)
    }

    function candidatesForTab () {
      const kind = v.tab === 'skills' ? 'skill' : 'agent'
      return v.candidates.filter(candidate => candidate.kind === kind)
    }

    async function scan () {
      v.scanning = true
      draw()
      try {
        const data = await ctx.api.post('/api/plugins/discover', { kind: kindAPI() })
        v.candidates = data.candidates || []
        v.selected.clear()
        v.err = null
      } catch (err) { v.err = err }
      v.scanning = false
      draw()
    }

    function candidateKey (c) { return `${c.repo}:${c.path}:${c.kind}` }

    async function importSources (sources) {
      if (!sources.length) { ctx.toast('Select at least one item to import', { err: true }); return }
      v.busy = true
      draw()
      try {
        await ctx.api.post('/api/plugins/import', { sources })
        v.candidates = []
        v.selected.clear()
        v.err = null
        v.busy = false
        await refresh()
        ctx.toast(`Imported ${sources.length} ${sources.length === 1 ? 'item' : 'items'} into the workspace`)
      } catch (err) {
        v.err = err
        v.busy = false
        draw()
      }
    }

    function importPanel () {
      const candidates = candidatesForTab()
      // repo picker for targeted path scans
      const sourceRepos = [{ value: '', label: 'Workspace-relative path' }, ...Array.from(new Set(v.candidates.map(c => c.repo))).map(name => ({ value: name, label: name }))]
      const repo = choose(sourceRepos, { value: '', testid: 'plugins-import-repo' })
      const path = h('input', { placeholder: 'git/anbox-agent-plugins/**', testid: 'plugins-import-path' })
      const pathImport = h('button', {
        class: 'btn', type: 'button', testid: 'plugins-import-path-button',
        disabled: v.busy,
        onclick: async () => {
          if (!path.value.trim()) { path.focus(); return }
          try {
            v.scanning = true
            draw()
            const data = await ctx.api.post('/api/plugins/discover', { path: path.value.trim(), repo: repo.value, kind: kindAPI() })
            v.candidates = data.candidates || []
            v.selected.clear()
            v.err = null
          } catch (err) { v.err = err }
          v.scanning = false
          draw()
        }
      }, 'Scan path')
      // apply repo and text filters to candidates
      const visibleCandidates = candidates.filter(c => {
      if (v.candidateRepo && v.candidateRepo !== '' && c.repo !== v.candidateRepo) return false
      if (v.candidateFilter && v.candidateFilter.trim() !== '') {
        const q = v.candidateFilter.trim().toLowerCase()
        return c.name.toLowerCase().includes(q) || c.path.toLowerCase().includes(q) || c.repo.toLowerCase().includes(q) || (c.description || '').toLowerCase().includes(q)
      }
      return true
      })
      const rows = visibleCandidates.map(c => {
        const key = candidateKey(c)
        return h('label', { class: 'pcandidate', testid: `plugins-candidate-${c.kind}-${c.repo}-${c.name}` },
          h('input', {
            type: 'checkbox',
            checked: v.selected.has(key),
            onchange: (e) => {
              if (e.target.checked) v.selected.add(key)
              else v.selected.delete(key)
              // re-render so the Import-selected button's disabled state
              // reflects the selection immediately, instead of staying
              // stuck until some unrelated redraw (e.g. a tab switch).
              draw()
            }
          }),
          h('span', { class: 'pcandname' }, c.name),
          h('span', { class: 'pcandrepo' }, c.repo),
          h('code', { class: 'pcandpath' }, c.path))
      })
      return h('section', { class: 'pimport', testid: 'plugins-import' },
        h('div', { class: 'psectionhead' },
          h('h3', null, `Add ${kindTitle()}s`),
          h('div', { style: 'display:flex;gap:8px;align-items:center' },
          h('input', { type: 'search', placeholder: 'Filter candidates', value: v.candidateFilter, testid: 'plugins-candidate-filter', oninput: (e) => { v.candidateFilter = e.target.value; draw() } }),
          choose([{ value: '', label: 'All repos' }, ...Array.from(new Set(v.candidates.map(c => c.repo))).map(name => ({ value: name, label: name }))], { value: v.candidateRepo, testid: 'plugins-candidate-repo', onchange: (e) => { v.candidateRepo = e.target.value; draw() } }),
          h('button', { class: 'btn', type: 'button', testid: 'plugins-discover', disabled: v.scanning || v.busy, onclick: scan }, v.scanning ? 'Scanning…' : 'Scan repositories'))),
        h('p', { class: 'pquiet' }, v.tab === 'skills'
          ? 'Discovered skills are shown with their repository and path. Import links them into .gummi; editing later creates a private copy.'
          : 'Discovered agent Markdown files are shown with their repository and path, then copied into .gummi.'),
        v.candidates.length ? h('div', { class: 'pcandidates', testid: 'plugins-candidates' },
          visibleCandidates.length ? rows : h('div', { class: 'pempty' }, `No ${kindTitle()} definitions found in the scan.`),
          h('div', { class: 'pimportactions' },
          h('button', { class: 'btn pri', type: 'button', testid: 'plugins-import-selected', disabled: v.busy || visibleCandidates.length === 0 || !Array.from(v.selected).some(k => visibleCandidates.some(c => candidateKey(c) === k)),
            onclick: () => importSources(visibleCandidates.filter(c => v.selected.has(candidateKey(c)))) }, 'Import selected')))
          : null,
        h('div', { class: 'pexplicit' },
          h('h4', null, 'Import a path'),
          h('div', { class: 'pexplicitrow' }, repo, path, pathImport)))
    }

    async function exportItem (item) {
      try {
        const res = await fetch(`/api/plugins/${encodeURIComponent(item.id)}/export`, { credentials: 'same-origin' })
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new Error(data.error || `Export failed (${res.status})`)
        }
        const blob = await res.blob()
        const url = URL.createObjectURL(blob)
        const a = h('a', { href: url, download: `${item.id}.zip` })
        document.body.append(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(url)
      } catch (err) {
        v.err = err
        draw()
      }
    }

    async function removeItem (item) {
      try {
        await ctx.api.del(`/api/plugins/${encodeURIComponent(item.id)}`)
        v.confirmDelete = null
        await refresh()
        ctx.toast(`Removed ${item.name}`)
      } catch (err) { v.err = err; draw() }
    }

    function switchTab (id) {
      v.tab = id
      v.editor = null
      // candidates and their selection are tab-scoped: a scan started on
      // one tab must not leave the other tab showing stale results.
      v.candidates = []
      v.selected.clear()
      draw()
    }

    function draw () {
      clear(body)
      const active = itemsForTab()
      body.append(h('div', { class: 'pintro' },
        h('p', null, 'Keep agent and skill definitions in this workspace’s .gummi library. They are always available to every card and freeform session — the library is local and is not added to a product repository.')))
      body.append(h('nav', { class: 'ptabs', role: 'tablist', 'aria-label': 'Agent plugin management' },
        ...[['agents', 'Manage agents'], ['skills', 'Manage skills']].map(([id, label]) =>
          h('button', {
            type: 'button', role: 'tab', class: v.tab === id && 'on',
            'aria-selected': String(v.tab === id), testid: `plugins-tab-${id}`,
            onclick: () => switchTab(id)
          }, label))))
      if (v.err) body.append(errorBox(v.err))
      if (v.busy && !v.editor) body.append(h('div', { class: 'pbusy' }, h('span', { class: 'spinner' }), 'Working…'))
      if (v.editor) {
        body.append(editorForm())
      } else {
        body.append(h('div', { class: 'psectionhead plisthead' },
          h('h3', null, `${v.tab === 'skills' ? 'Skills' : 'Agent definitions'} (${active.length})`),
          h('button', { type: 'button', class: 'btn pri', testid: 'plugins-new', disabled: v.busy, onclick: beginCreate }, `New ${kindTitle()}`)))
        if (!active.length) body.append(h('div', { class: 'pempty' }, `No ${v.tab} yet. Create one, import a path, or scan repositories.`))
        else body.append(h('div', { class: 'pitems', testid: 'plugins-items' }, active.map(itemCard)))
        if (v.tab === 'agents') {
          body.append(h('p', { class: 'pquiet' }, 'Agent definitions use *.agent.md / AGENTS.md and are managed and exported here. Gummi currently forwards skills to card sessions; it does not load custom agent definitions into stage sessions.'))
        }

        body.append(importPanel())
      }
      if (v.busy && v.editor) body.querySelectorAll('input,textarea,button').forEach(el => { el.disabled = true })
    }

    refresh()
    return () => { v.alive = false }
  }
})
