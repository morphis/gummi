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
      repoFilter: 'all',
      items: [],
      repos: [],
      providers: [],
      candidates: [],
      selected: new Set(),
      editor: null,
      confirmDelete: null,
      err: null,
      scanning: false,
      busy: false,
      alive: true
    }
    body.classList.add('vplugins')

    async function refresh () {
      try {
        const data = await ctx.api.get('/api/plugins')
        v.items = data.items || []
        v.repos = data.repos || []
        v.providers = data.providers || []
        v.err = null
      } catch (err) { v.err = err }
      if (v.alive) draw()
    }

    function scopePicker (global = false, picked = []) {
      const all = h('input', { type: 'checkbox', checked: global, testid: 'plugins-scope-global' })
      const repoInputs = v.repos.map(name => [name, h('input', {
        type: 'checkbox',
        checked: picked.includes(name),
        disabled: global,
        testid: `plugins-scope-${name}`
      })])
      all.addEventListener('change', () => { for (const [, input] of repoInputs) input.disabled = all.checked })
      const node = h('fieldset', { class: 'pscope', testid: 'plugins-scope' },
        h('legend', null, 'Availability'),
        h('label', { class: 'pcheck' }, all, h('span', null, 'All managed repositories')),
        repoInputs.length
          ? h('div', { class: 'prepos' }, repoInputs.map(([name, input]) =>
            h('label', { class: 'pcheck' }, input, h('span', null, name === 'default' ? 'Default repository' : name))))
          : h('small', { class: 'pquiet' }, 'No managed repositories are configured.'))
      return {
        node,
        value: () => ({
          global: all.checked,
          repos: repoInputs.filter(([, input]) => input.checked).map(([name]) => name)
        })
      }
    }

    function kindTitle () { return v.tab === 'skills' ? 'skill' : 'agent' }
    function kindAPI () { return v.tab === 'skills' ? 'skill' : 'agent' }

    function beginCreate () {
      v.editor = { id: '', name: '', content: v.tab === 'skills' ? SKILL_STARTER : AGENT_STARTER, global: false, repos: [] }
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

    async function saveEditor (name, content, scope, button) {
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
        const req = { name: itemName, content: itemContent, ...scope.value() }
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
      const scope = scopePicker(item.global, item.repos || [])
      const save = h('button', {
        class: 'btn pri', type: 'button', testid: 'plugins-save',
        disabled: v.busy,
        onclick: () => saveEditor(name, content, scope, save)
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
        scope.node,
        h('div', { class: 'pactions' },
          h('button', { class: 'btn', type: 'button', testid: 'plugins-cancel-edit', onclick: () => { v.editor = null; draw() } }, 'Cancel'),
          save))
    }

    function scopeSummary (item) {
      if (item.global) return 'All managed repositories'
      return (item.repos || []).map(x => x === 'default' ? 'default repo' : x).join(', ')
    }

    function itemCard (item) {
      const titleOnly = h('strong', null, item.name)
      const tooltip = item.description || scopeSummary(item) || item.sourcePath || ''
      const title = tooltip ? h('abbr', { title: tooltip }, titleOnly) : titleOnly
      return h('article', { class: 'pitem', testid: `plugins-item-${item.id}` },
        h('div', { class: 'pitemmain' },
          h('div', { class: 'pitemtitle' }, title, item.linked ? h('span', { class: 'ptag' }, 'linked') : null)),
        v.tab === 'skills' ? providerBadges(item) : null,
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

    function providerBadges (item) {
      const applied = enabledForRepo(item)
      return h('div', { class: 'pproviders', 'aria-label': 'Backend skill availability' },
        v.providers.map(p => {
          const available = p.skillDirs && applied
          return h('span', {
            class: ['pprovider', available ? 'supported' : 'unsupported'],
            title: p.skillDetail,
            testid: `plugins-provider-${item.id}-${p.name}`
          }, h('i', { 'aria-hidden': 'true' }, available ? '✓' : '—'), p.name)
        }))
    }

    function enabledForRepo (item, repo = v.repoFilter) {
      return repo === 'all' || item.global || (item.repos || []).includes(repo)
    }

    function effectiveItems () {
      const kind = v.tab === 'skills' ? 'skill' : 'agent'
      return v.items.filter(item => item.kind === kind && enabledForRepo(item))
    }

    function effectiveCandidates () {
      const kind = v.tab === 'skills' ? 'skill' : 'agent'
      return v.candidates.filter(candidate =>
        candidate.kind === kind && (v.repoFilter === 'all' || candidate.repo === v.repoFilter))
    }

    function selectedScope () {
      return v.importScope?.value() || { global: false, repos: [] }
    }

    async function scan () {
      v.scanning = true
      draw()
      try {
        const data = await ctx.api.post('/api/plugins/discover', {})
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
      const scope = selectedScope()
      if (!scope.global && scope.repos.length === 0) {
        ctx.toast('Choose all repositories or at least one repository', { err: true })
        return
      }
      v.busy = true
      draw()
      try {
        await ctx.api.post('/api/plugins/import', { sources, ...scope })
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
      const candidates = effectiveCandidates()
      const sourceRepos = [{ value: '', label: 'Workspace-relative path' }, ...v.repos.map(name => ({ value: name, label: name }))]
      const repo = choose(sourceRepos, { value: '', testid: 'plugins-import-repo' })
      const path = h('input', { placeholder: 'git/griffin/.github/skills/review/SKILL.md', testid: 'plugins-import-path' })
      const scope = scopePicker(false, [])
      v.importScope = scope
      const pathImport = h('button', {
        class: 'btn', type: 'button', testid: 'plugins-import-path-button',
        disabled: v.busy,
        onclick: async () => {
          if (!path.value.trim()) { path.focus(); return }
          try {
            v.scanning = true
            draw()
            const data = await ctx.api.post('/api/plugins/discover', { path: path.value.trim(), repo: repo.value })
            v.candidates = data.candidates || []
            v.selected.clear()
            v.err = null
          } catch (err) { v.err = err }
          v.scanning = false
          draw()
        }
      }, 'Scan path')
      const rows = candidates.map(c => {
        const key = candidateKey(c)
        return h('label', { class: 'pcandidate', testid: `plugins-candidate-${c.kind}-${c.repo}-${c.name}` },
          h('input', { type: 'checkbox', checked: v.selected.has(key), onchange: (e) => {
            if (e.target.checked) v.selected.add(key)
            else v.selected.delete(key)
          } }),
          h('span', { class: 'pcandname' }, c.name),
          h('span', { class: 'pcandrepo' }, c.repo),
          h('code', { class: 'pcandpath' }, c.path))
      })
      return h('section', { class: 'pimport', testid: 'plugins-import' },
        h('div', { class: 'psectionhead' },
          h('h3', null, `Add ${kindTitle()}s`),
          h('button', { class: 'btn', type: 'button', testid: 'plugins-discover', disabled: v.scanning || v.busy, onclick: scan }, v.scanning ? 'Scanning…' : 'Scan repositories')),
        h('p', { class: 'pquiet' }, v.tab === 'skills'
          ? 'Discovered skills are shown with their repository and path. Import links them into .gummi; editing later creates a private copy.'
          : 'Discovered agent Markdown files are shown with their repository and path, then copied into .gummi.'),
        v.candidates.length ? h('div', { class: 'pcandidates', testid: 'plugins-candidates' },
          candidates.length ? rows : h('div', { class: 'pempty' }, `No ${kindTitle()} definitions found in the scan.`),
          h('div', { class: 'pimportactions' },
            h('button', { class: 'btn pri', type: 'button', testid: 'plugins-import-selected', disabled: v.busy || !candidates.some(c => v.selected.has(candidateKey(c))),
              onclick: () => importSources(candidates.filter(c => v.selected.has(candidateKey(c)))) }, 'Import selected')))
          : null,
        h('div', { class: 'pexplicit' },
          h('h4', null, 'Import a path'),
          h('div', { class: 'pexplicitrow' }, repo, path, pathImport)),
        scope.node)
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

    function draw () {
      clear(body)
      const active = effectiveItems()
      const repoChoices = [{ value: 'all', label: 'All repositories' }, ...v.repos.map(name => ({ value: name, label: name === 'default' ? 'Default repository' : name }))]
      const repoSelect = choose(repoChoices, {
        value: v.repoFilter,
        testid: 'plugins-repo-filter',
        onchange: (e) => { v.repoFilter = e.target.value; v.selected.clear(); draw() }
      })
      body.append(h('div', { class: 'pintro' },
        h('p', null, 'Keep agent and skill definitions in this workspace’s .gummi library, then make them available globally or only to selected repositories. The library is local and is not added to a product repository.'),
        field('Show items enabled for', repoSelect)))
      body.append(h('nav', { class: 'ptabs', role: 'tablist', 'aria-label': 'Agent plugin management' },
        ...[['agents', 'Manage agents'], ['skills', 'Manage skills']].map(([id, label]) =>
          h('button', {
            type: 'button', role: 'tab', class: v.tab === id && 'on',
            'aria-selected': String(v.tab === id), testid: `plugins-tab-${id}`,
            onclick: () => { v.tab = id; v.editor = null; draw() }
          }, label))))
      if (v.err) body.append(errorBox(v.err))
      if (v.busy && !v.editor) body.append(h('div', { class: 'pbusy' }, h('span', { class: 'spinner' }), 'Working…'))
      if (v.editor) {
        body.append(editorForm())
      } else {
        body.append(h('div', { class: 'psectionhead plisthead' },
          h('h3', null, `${v.tab === 'skills' ? 'Skills' : 'Agent definitions'} (${active.length})`),
          h('button', { type: 'button', class: 'btn pri', testid: 'plugins-new', disabled: v.busy, onclick: beginCreate }, `New ${kindTitle()}`)))
        if (!active.length) body.append(h('div', { class: 'pempty' }, `No ${v.tab} enabled for this view yet. Create one, import a path, or scan repositories.`))
        else body.append(h('div', { class: 'pitems', testid: 'plugins-items' }, active.map(itemCard)))
        if (v.tab === 'skills') {
          body.append(h('section', { class: 'pavailability', testid: 'plugins-availability' },
            h('h3', null, 'Backend skill availability'),
            h('p', { class: 'pquiet' }, 'This reflects gummi’s adapter capabilities. Enabled skills are passed to new sessions only; a running session keeps the skills it started with.'),
            h('div', { class: 'pproviders' }, v.providers.map(p =>
              h('span', { class: ['pprovider', p.skillDirs ? 'supported' : 'unsupported'], testid: `plugins-backend-${p.name}`, title: p.skillDetail },
                h('i', { 'aria-hidden': 'true' }, p.skillDirs ? '✓' : '—'), p.name))),
            h('p', { class: 'pquiet' }, 'Current support: Copilot, Claude, and opencode accept forwarded skill directories. Codex, pi, and headless do not.')))
        } else {
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
