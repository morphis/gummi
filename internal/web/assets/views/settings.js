// views/settings.js — the workspace's own settings: the name this gummi
// instance goes by, so boards running side by side can be told apart in a
// header and a browser tab (the terminal's settings dialog writes the same
// config key), and the GitHub token and SSH key gummi's own gh and push
// commands use where the machine has none set up. A stored secret is never
// sent back: the page is told only what names it.

import { h, clear } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { field, errorBox } from './kit.js?v=__ASSET_V__'

registerView('settings', {
  title: 'Settings',
  css: 'views/settings.css',
  mount (body, ctx) {
    const v = { cur: null, err: null, saving: false, alive: true }
    body.classList.add('vsettings')

    async function load () {
      try { v.cur = await ctx.api.get('/api/settings') } catch (err) { v.err = err }
      if (v.alive) draw()
    }

    async function save (name) {
      v.saving = true
      v.err = null
      draw()
      try {
        v.cur = await ctx.api.put('/api/settings', { name })
        ctx.refreshBoard?.()
        ctx.toast?.(v.cur.name ? `Named “${v.cur.name}”` : 'Name cleared')
      } catch (err) { v.err = err }
      v.saving = false
      if (v.alive) draw()
    }

    async function saveCredentials (req, said) {
      v.saving = true
      v.err = null
      draw()
      try {
        v.cur = await ctx.api.put('/api/settings/credentials', req)
        ctx.refreshBoard?.()
        ctx.toast?.(said)
      } catch (err) { v.err = err }
      v.saving = false
      if (v.alive) draw()
    }

    // secret is one stored credential: what names the one held, a control
    // to replace it, and a button to forget it.
    function secret ({ id, label, held, control, hint, key, saved, forgotten }) {
      const form = h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); if (control.value.trim()) saveCredentials({ [key]: control.value }, saved) } },
        field(label, control, { hint }),
        held ? h('div', { class: 'vnote', testid: `${id}-held` }, held) : null,
        h('div', { class: 'vrow' },
          h('button', { type: 'submit', class: 'btn primary', testid: `${id}-save`, disabled: v.saving }, held ? 'Replace' : 'Save'),
          held ? h('button', { type: 'button', class: 'btn', testid: `${id}-forget`, disabled: v.saving, onclick: () => saveCredentials({ [key]: '' }, forgotten) }, 'Forget') : null))
      return form
    }

    function drawCredentials () {
      const c = v.cur.credentials || {}
      const token = h('input', { type: 'password', testid: 'settings-token', placeholder: c.tokenSet ? 'Paste a new token to replace it' : 'ghp_… or github_pat_…', autocomplete: 'off', spellcheck: 'false' })
      const key = h('textarea', { class: 'mono', testid: 'settings-sshkey', rows: 4, placeholder: '-----BEGIN OPENSSH PRIVATE KEY-----', autocomplete: 'off', spellcheck: 'false' })
      return h('section', { class: 'vcreds' },
        h('div', { class: 'vhead' }, h('b', null, 'GitHub credentials')),
        h('div', { class: 'vnote' }, 'Optional. Without them gummi uses whatever gh and git already have on this machine. Both are kept on the host, readable only by the account gummi runs as, and are never shown again.'),
        secret({
          id: 'settings-token',
          label: 'GitHub token',
          control: token,
          key: 'githubToken',
          held: c.tokenSet ? `A token is stored${c.tokenHint ? `, ending in ${c.tokenHint}` : ''}.` : null,
          hint: 'Set as GH_TOKEN on the gh commands gummi runs: pull requests, issue import, review threads.',
          saved: 'GitHub token stored',
          forgotten: 'GitHub token forgotten'
        }),
        secret({
          id: 'settings-sshkey',
          label: 'SSH private key',
          control: key,
          key: 'sshKey',
          held: c.keySet
            ? h('span', null, `A ${c.keyType} key is stored: `, h('code', { testid: 'settings-sshkey-fp' }, c.keyFingerprint), h('pre', { class: 'pub', testid: 'settings-sshkey-pub' }, c.keyPublic))
            : null,
          hint: 'Offered to git over an ssh-agent only while gummi pushes a card’s branch. A key with a passphrase is refused.',
          saved: 'SSH key stored',
          forgotten: 'SSH key forgotten'
        }))
    }

    function draw () {
      clear(body)
      if (v.err) body.append(errorBox(v.err))
      if (!v.cur) return
      const input = h('input', { type: 'text', testid: 'settings-name', value: v.cur.name, maxlength: v.cur.maxName, placeholder: `e.g. staging, laptop, ${v.cur.repo}`, autocomplete: 'off' })
      const form = h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); save(input.value) } },
        field('Instance name', input, { hint: 'Shown in this header, the browser tab and the terminal’s status bar. Leave empty for none.' }),
        h('div', { class: 'vrow' },
          h('button', { type: 'submit', class: 'btn primary', testid: 'settings-save', disabled: v.saving }, 'Save')))
      body.append(form, drawCredentials())
    }

    load()
    return () => { v.alive = false }
  }
})
