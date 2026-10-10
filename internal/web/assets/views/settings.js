// views/settings.js — the workspace's own settings: the name this gummi
// instance goes by, so boards running side by side can be told apart in a
// header and a browser tab (the terminal's settings dialog writes the same
// config key), and the GitHub token and SSH key gummi's own gh and push
// commands use where the machine has none set up, and who git writes this
// workspace's commits as. A stored secret is never sent back: the page is
// told only what names it, and a key can be made on the host so that its
// private half never crosses the network at all.

import { h, clear } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { field, errorBox } from './kit.js?v=__ASSET_V__'

// tip is the "?" beside a label: the explanation a person asks for instead
// of one the form always carries. Hover, focus or a tap shows it; the
// styles do the showing, so it needs no listeners.
let tips = 0
function tip (text, testid) {
  const id = `settings-tip-${++tips}`
  return h('span', { class: 'vtip' },
    h('button', { type: 'button', class: 'vtip-b', 'aria-label': 'What is this?', 'aria-describedby': id, testid }, '?'),
    h('span', { class: 'vtip-t', role: 'tooltip', id }, text))
}

// tipField is kit's field with a tip beside its words. The tip sits
// outside the <label>, which would otherwise name the tip's button rather
// than the control.
function tipField (words, text, control, testid) {
  control.id = control.id || `settings-f-${++tips}`
  return h('div', { class: 'field' },
    h('div', { class: 'vlab' }, h('label', { for: control.id }, words), tip(text, testid)),
    control)
}

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

    async function saveIdentity (name, email) {
      v.saving = true
      v.err = null
      draw()
      try {
        v.cur = await ctx.api.put('/api/settings/identity', { name, email })
        ctx.toast?.(v.cur.identity.name ? `Commits are written as ${v.cur.identity.name}` : 'Git identity cleared')
      } catch (err) { v.err = err }
      v.saving = false
      if (v.alive) draw()
    }

    function drawIdentity () {
      const id = v.cur.identity || {}
      const name = h('input', { type: 'text', testid: 'settings-git-name', value: id.name || '', placeholder: 'Your Name', autocomplete: 'off' })
      const email = h('input', { type: 'email', testid: 'settings-git-email', value: id.email || '', placeholder: 'you@example.com', autocomplete: 'off' })
      return h('section', { class: 'vcreds' },
        h('div', { class: 'vhead' }, h('b', null, 'Git identity'),
          tip('Who commits in this workspace are written as: the ones a card’s agent makes and the ones made when a card lands. Saved to the repository’s own git configuration, not the machine’s. Clear both to use the machine’s.', 'settings-git-tip')),
        h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); saveIdentity(name.value, email.value) } },
          h('div', { class: 'vrow' }, field('Name', name), field('Email', email)),
          h('div', { class: 'vrow' },
            h('button', { type: 'submit', class: 'btn primary', testid: 'settings-git-save', disabled: v.saving }, 'Save'))))
    }

    // secret is one stored credential: what names the one held, a control
    // to replace it, and a button to forget it.
    function secret ({ id, label, held, control, hint, key, saved, forgotten, extra }) {
      const form = h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); if (control.value.trim()) saveCredentials({ [key]: control.value }, saved) } },
        tipField(label, hint, control, `${id}-tip`),
        held ? h('div', { class: 'vnote', testid: `${id}-held` }, held) : null,
        h('div', { class: 'vrow' },
          h('button', { type: 'submit', class: 'btn primary', testid: `${id}-save`, disabled: v.saving }, held ? 'Replace' : 'Save'),
          extra || null,
          held ? h('button', { type: 'button', class: 'btn', testid: `${id}-forget`, disabled: v.saving, onclick: () => saveCredentials({ [key]: '' }, forgotten) }, 'Forget') : null))
      return form
    }

    function drawCredentials () {
      const c = v.cur.credentials || {}
      const token = h('input', { type: 'password', testid: 'settings-token', placeholder: c.tokenSet ? 'Paste a new token to replace it' : 'ghp_… or github_pat_…', autocomplete: 'off', spellcheck: 'false' })
      const key = h('textarea', { class: 'mono', testid: 'settings-sshkey', rows: 4, placeholder: '-----BEGIN OPENSSH PRIVATE KEY-----', autocomplete: 'off', spellcheck: 'false' })
      return h('section', { class: 'vcreds' },
        h('div', { class: 'vhead' }, h('b', null, 'GitHub credentials'),
          tip('Optional. Without them gummi uses whatever gh and git already have on this machine. Both are kept on the host, readable only by the account gummi runs as, and are never shown again.', 'settings-creds-tip')),
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
          hint: 'Offered to git over an ssh-agent only while gummi pushes a card’s branch. Paste one (a key with a passphrase is refused), or generate one here and add its public half to GitHub: the private half then never leaves the host.',
          extra: h('button', { type: 'button', class: 'btn', testid: 'settings-sshkey-generate', disabled: v.saving, onclick: () => saveCredentials({ generateSshKey: true }, 'New SSH key generated') }, c.keySet ? 'Generate a new key' : 'Generate a key'),
          saved: 'SSH key stored',
          forgotten: 'SSH key forgotten'
        }),
        c.keySet
          ? h('div', { class: 'vrow' },
            h('label', { class: 'pubcheck' },
              h('input', { type: 'checkbox', testid: 'settings-sign', checked: !!c.signing, disabled: v.saving, onchange: (e) => saveCredentials({ signCommits: e.target.checked }, e.target.checked ? 'Commits are signed with the stored key' : 'Commits are no longer signed with the stored key') }),
              h('span', null, 'Sign commits with this key')),
            tip('Every commit made from here on in this workspace is signed with the stored key: the ones gummi makes (checkpoints, landings, rebases), the ones a card’s agent makes, and the ones made in a card’s terminal. A session already running signs from its next start; switched off, a running session’s commits are refused until it starts again. For GitHub to show them as verified, add the public half above as a signing key and commit as an email verified on that account.', 'settings-sign-tip'))
          : null)
    }

    function draw () {
      clear(body)
      if (v.err) body.append(errorBox(v.err))
      if (!v.cur) return
      const input = h('input', { type: 'text', testid: 'settings-name', value: v.cur.name, maxlength: v.cur.maxName, placeholder: `e.g. staging, laptop, ${v.cur.repo}`, autocomplete: 'off' })
      const form = h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); save(input.value) } },
        tipField('Instance name', 'Shown in this header, the browser tab and the terminal’s status bar. Leave empty for none.', input, 'settings-name-tip'),
        h('div', { class: 'vrow' },
          h('button', { type: 'submit', class: 'btn primary', testid: 'settings-save', disabled: v.saving }, 'Save')))
      body.append(form, drawIdentity(), drawCredentials())
    }

    load()
    return () => { v.alive = false }
  }
})
