// views/settings.js — the workspace's own settings: today the name this
// gummi instance goes by, so boards running side by side can be told apart
// in a header and a browser tab. The terminal's settings dialog writes the
// same config key.

import { h, clear } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { field, errorBox } from './kit.js?v=__ASSET_V__'

registerView('settings', {
  title: 'Settings',
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

    function draw () {
      clear(body)
      if (v.err) body.append(errorBox(v.err))
      if (!v.cur) return
      const input = h('input', { type: 'text', testid: 'settings-name', value: v.cur.name, maxlength: v.cur.maxName, placeholder: `e.g. staging, laptop, ${v.cur.repo}`, autocomplete: 'off' })
      const form = h('form', { class: 'vform', onsubmit: (e) => { e.preventDefault(); save(input.value) } },
        field('Instance name', input, { hint: 'Shown in this header, the browser tab and the terminal’s status bar. Leave empty for none.' }),
        h('div', { class: 'vrow' },
          h('button', { type: 'submit', class: 'btn primary', testid: 'settings-save', disabled: v.saving }, 'Save')))
      body.append(form)
    }

    load()
    return () => { v.alive = false }
  }
})
