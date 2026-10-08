// views/newcard.js — the new-card form: every kind the board makes
// (feature, bug, research and its diagnosis mode, goal, freeform), with
// the choices GET /api/form offers — profiles, the default budget,
// repositories and their branches, the cards a new one can stack on or
// wait for. Submitting is POST /api/cards, which fills in and submits the
// TUI's own card form: its validation is the one that runs, and its
// refusal comes back as a sentence, shown beside the field it is about.
// "Create & autopilot" is that form's second button.

import { h, clear, dollarsInput, parseDollars } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { hush } from '../toast.js?v=__ASSET_V__'

const MAC = /Mac|iPhone|iPad/.test(navigator.platform || '')

// What each kind is for, under the kind picker.
const ABOUT = {
  feature: 'A change: planned, implemented and verified on its own branch.',
  bug: 'A defect: the fix is planned, implemented and verified like a feature.',
  research: 'A question to investigate. It ends in findings, not a branch to land.',
  'research:diagnosis': 'Research that pins down why something fails.',
  goal: 'An outcome a lead agent drives through several cards, within one budget.',
  freeform: 'No stages and no gates: an agent works on a branch while you talk to it. It opens at once.'
}
const BRIEF = {
  feature: ['What and why', 'What should change, and why it matters'],
  bug: ['What happens', 'What goes wrong, and where'],
  research: ['The question', 'What do you want to know?'],
  'research:diagnosis': ['The symptom', 'What fails, and when'],
  goal: ['Done when', 'The outcome, and how you will know it is reached'],
  freeform: ['What to do', 'Say what you want done']
}
const ADOPTS = new Set(['feature', 'bug'])
const STACKS = new Set(['feature', 'bug'])
const WAITS = new Set(['feature', 'bug', 'research', 'research:diagnosis'])

// Which field a refusal is about, read from the sentence the form wrote.
// Adopting goes before the base: its refusals talk about branches too
// ("main is the branch this card would land on; adopting it…", "FD-001
// already has feat/x — one branch, one card").
const FIELD_OF = [
  [/title|first line/i, 'title'],
  [/description/i, 'desc'],
  [/adopt|one branch, one card/i, 'adopt'],
  [/budget|envelope|credits/i, 'envelope'],
  [/profile/i, 'profile'],
  [/severity/i, 'severity'],
  [/repositor/i, 'repo'],
  [/stack/i, 'stack'],
  [/waited on|depend/i, 'after'],
  [/branch/i, 'base']
]

function fieldFor (msg) {
  for (const [re, f] of FIELD_OF) if (re.test(msg)) return f
  return ''
}

registerView('newcard', {
  title: 'New card',
  css: 'views/newcard.css',
  mount (body, ctx) {
    const seed = String(ctx.params?.text || '').trim()
    const [seedTitle, ...seedRest] = seed.split('\n')
    const st = {
      kind: ctx.params?.kind || 'feature',
      form: null,
      repo: '',
      busy: false,
      // attachments are stored by reference, so the description always
      // offers the control — every stage gets at least the file's path.
      attachments: []
    }
    clear(body).append(h('div', { class: 'empty', testid: 'newcard-loading' }, h('span', { class: 'spinner' })))

    const el = {}
    const errs = {}

    const load = async (repo = '') => {
      try {
        st.form = await ctx.api.get('/api/form' + (repo ? `?repo=${encodeURIComponent(repo)}` : ''))
      } catch (err) {
        clear(body).append(h('div', { class: 'empty err', testid: 'newcard-error' }, h('b', null, 'The form did not load'), err.message))
        return false
      }
      return true
    }

    const errEl = (name) => (errs[name] = h('span', { class: 'ferr', testid: `newcard-error-${name}`, role: 'alert', hidden: true }))
    const field = (name, label, control, hint) => h('label', { class: 'field', data: { f: name } },
      h('span', { class: 'fl' }, label), control, hint ? h('span', { class: 'fh' }, hint) : null, errEl(name))

    const draw = () => {
      const f = st.form
      clear(body)
      const kinds = h('div', { class: 'nc-kinds', role: 'radiogroup', 'aria-label': 'Kind', testid: 'newcard-kinds' },
        f.kinds.map(k => h('button', {
          type: 'button', role: 'radio', class: ['nc-kind', k.value === st.kind && 'on'],
          'aria-checked': String(k.value === st.kind), testid: `newcard-kind-${k.value.replace(':', '-')}`,
          onclick: () => { keep(); st.kind = k.value; draw() }
        }, k.label)))
      const [briefLabel, briefPh] = BRIEF[st.kind] || BRIEF.feature
      el.title = h('input', { testid: 'newcard-title', placeholder: 'A short title', autocomplete: 'off', value: st.title ?? seedTitle ?? '' })
      el.desc = h('textarea', { testid: 'newcard-desc', placeholder: briefPh, rows: 5, value: st.desc ?? seedRest.join('\n').trim() })
      const chips = h('div', { class: 'chips', testid: 'newcard-chips', hidden: st.attachments.length === 0 })
      const renderChips = () => {
        clear(chips)
        chips.hidden = st.attachments.length === 0
        for (const a of st.attachments) {
          chips.append(h('span', { class: ['chip', a.error && 'err', a.pending && 'pending'] },
            a.pending ? 'Uploading…' : (a.error || a.name),
            h('button', { type: 'button', title: 'Remove', onclick: () => { st.attachments = st.attachments.filter((x) => x !== a); renderChips() } }, '×')))
        }
      }
      renderChips()
      const file = h('input', {
        type: 'file', testid: 'newcard-file', accept: 'image/png,image/jpeg,image/gif,image/webp', multiple: true, hidden: true,
        onchange: async (e) => {
          const files = [...e.target.files]
          e.target.value = ''
          for (const f of files) {
            const chip = { id: null, name: f.name || 'image', pending: true, error: null }
            st.attachments.push(chip)
            renderChips()
            try {
              Object.assign(chip, await ctx.api.uploadAttachment(f), { pending: false })
            } catch (err) {
              chip.pending = false
              chip.error = (err.data && err.data.error) || err.message || 'upload failed'
            }
            renderChips()
          }
        }
      })
      const attach = h('button', { class: 'attach', type: 'button', testid: 'newcard-attach', title: 'Attach an image', onclick: () => file.click() }, '📎')
      const main = [
        h('div', { class: 'nc-kindrow' }, kinds, h('p', { class: 'nc-about', testid: 'newcard-about' }, ABOUT[st.kind] || '')),
        field('title', 'Title', el.title),
        field('desc', briefLabel, el.desc),
        h('div', { class: 'nc-attach' }, attach, file, chips)
      ]
      if (st.kind === 'bug') {
        el.severity = h('select', { testid: 'newcard-severity' }, f.severities.map(s => h('option', { value: s, selected: s === (st.severity || 'medium') }, s)))
        el.repro = h('textarea', { testid: 'newcard-repro', rows: 2, placeholder: 'Steps to reproduce', value: st.repro || '' })
        el.expected = h('input', { testid: 'newcard-expected', placeholder: 'What should happen', value: st.expected || '' })
        el.actual = h('input', { testid: 'newcard-actual', placeholder: 'What happens instead', value: st.actual || '' })
        main.push(h('div', { class: 'nc-row' }, field('severity', 'Severity', el.severity)),
          field('repro', 'Steps to reproduce', el.repro),
          h('div', { class: 'nc-row two' }, field('expected', 'Expected', el.expected), field('actual', 'Actual', el.actual)))
      }

      const side = []
      el.profile = h('select', { testid: 'newcard-profile' }, f.profiles.map(p => h('option', { value: p, selected: p === (st.profile || f.profiles[0]) }, p)))
      side.push(field('profile', 'Profile', el.profile, 'Which models play each role.'))
      el.envelope = h('input', { testid: 'newcard-envelope', type: 'text', inputmode: 'decimal', value: st.envelope ?? (f.envelope ? dollarsInput(f.envelope) : '') })
      side.push(field('envelope', 'Budget', el.envelope, 'Dollars this card may spend. 0 is uncapped.'))
      if (f.repos?.length) {
        el.repo = h('select', { testid: 'newcard-repo', onchange: async () => { keep(); st.repo = el.repo.value; if (await load(st.repo)) draw() } },
          f.repos.map(r => h('option', { value: r, selected: r === st.repo }, r)))
        side.push(field('repo', 'Repository', el.repo))
      }
      const branches = f.branches || []
      el.base = h('select', { testid: 'newcard-base' }, h('option', { value: '' }, 'the default branch'), branches.map(b => h('option', { value: b, selected: b === st.base }, b)))
      const baseHint = h('span', { class: 'fh', testid: 'newcard-base-hint' })
      side.push(h('label', { class: 'field', data: { f: 'base' } }, h('span', { class: 'fl' }, 'Base branch'), el.base, baseHint, errEl('base')))
      if (ADOPTS.has(st.kind)) {
        el.adopt = h('select', { testid: 'newcard-adopt' })
        side.push(field('adopt', 'Adopt a branch', el.adopt, 'Work on a branch gummi did not cut. It is added to, never rebased or deleted.'))
      }
      if (STACKS.has(st.kind) && f.stackable?.length) {
        el.stack = h('select', { testid: 'newcard-stack' }, h('option', { value: '' }, 'no stack'), f.stackable.map(c => h('option', { value: c.id, selected: c.id === st.stack }, `${c.id} · ${c.title}`)))
        side.push(field('stack', 'Stack onto', el.stack, 'Fork from that card’s branch. It never waits for it.'))
      }
      // a stacked card forks from the card below it, whatever base is
      // picked (cardmint ignores it there): the base says so and steps
      // aside rather than being dropped without a word
      const syncBase = () => {
        const stacked = !!el.stack?.value
        el.base.disabled = stacked
        baseHint.textContent = stacked
          ? `Set by the stack: it forks from ${el.stack.value}’s branch and lands with it.`
          : 'What the branch forks from and lands on.'
        syncAdopt()
      }
      // the adopt list: every branch, but one that would be refused is
      // there only to say why it can't be picked — the server's word for
      // each (another card holds it, nothing of its own to adopt), and
      // the branch the card would land on, which follows the base picked
      const why = new Map((f.adoptable || []).map(a => [a.branch, a]))
      const syncAdopt = () => {
        if (!el.adopt) return
        const picked = el.adopt.value || st.adopt || ''
        const lands = el.base.disabled ? '' : (el.base.value || (!st.repo ? ctx.state?.board?.head || '' : ''))
        clear(el.adopt).append(h('option', { value: '' }, 'no — cut a new branch'),
          ...branches.map(b => {
            // the server measured against the default base: under another
            // base only a card holding the branch still settles it, and the
            // create says the rest
            const a = why.get(b) || {}
            const no = b === lands ? 'the branch it lands on' : (!el.base.value || a.held ? a.why || '' : '')
            return no
              ? h('option', { value: b, disabled: true }, `${b} — ${no}`)
              : h('option', { value: b, selected: b === picked }, b)
          }))
      }
      el.base.addEventListener('change', syncAdopt)
      el.stack?.addEventListener('change', syncBase)
      syncBase()
      if (WAITS.has(st.kind) && f.dependable?.length) {
        const picked = new Set(st.after || [])
        el.after = f.dependable.map(c => ({ c, cb: h('input', { type: 'checkbox', value: c.id, checked: picked.has(c.id), testid: `newcard-after-${c.id}` }) }))
        side.push(h('div', { class: 'field', data: { f: 'after' } }, h('span', { class: 'fl' }, 'Waits for'),
          h('div', { class: 'cpicks', role: 'group', 'aria-label': 'Waits for', testid: 'newcard-after' },
            el.after.map(({ c, cb }) => h('label', { class: 'cpick' }, cb, h('span', { class: 'id' }, c.id), h('span', { class: 't' }, c.title), h('span', { class: 's' }, c.stage)))),
          h('span', { class: 'fh' }, 'It is planned at once; its design is approved only once each ticked card is done.'), errEl('after')))
      } else {
        el.after = null
      }

      const err = h('p', { class: 'aerr', testid: 'newcard-error', role: 'alert', hidden: true })
      errs._ = err
      const create = h('button', { class: 'btn pri', type: 'submit', testid: 'newcard-create' }, st.kind === 'freeform' ? 'Create and open' : 'Create')
      const auto = st.kind === 'freeform' ? null : h('button', { class: 'btn', type: 'button', testid: 'newcard-autopilot', title: 'Create it and let autopilot cross its gates', onclick: () => submit(true) }, 'Create & autopilot')
      const form = h('form', { class: 'nc', testid: 'newcard-form', novalidate: true, onsubmit: (e) => { e.preventDefault(); submit(false) } },
        h('div', { class: 'nc-grid' }, h('div', { class: 'nc-main' }, main), h('div', { class: 'nc-side' }, side)),
        h('div', { class: 'nc-foot' }, err, h('span', { class: 'nc-keys' }, h('kbd', null, MAC ? '⌘' : 'Ctrl'), h('kbd', null, 'enter'), ' creates'),
          h('button', { class: 'btn', type: 'button', testid: 'newcard-cancel', onclick: () => ctx.close() }, 'Cancel'), auto, create))
      form.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submit(false) } })
      // a refusal is about what was sent: editing the field clears it
      const clearField = (e) => {
        const f = e.target.closest?.('.field')
        if (!f || !f.classList.contains('bad')) return
        f.classList.remove('bad')
        const fe = f.querySelector('.ferr')
        if (fe) fe.hidden = true
      }
      form.addEventListener('input', clearField)
      form.addEventListener('change', clearField)
      el.create = create
      el.auto = auto
      body.append(form)
      el.title.focus()
    }

    // keep holds what was typed across a redraw (a kind or repository
    // change redraws the form).
    const keep = () => {
      if (!el.title) return
      st.title = el.title.value
      st.desc = el.desc.value
      st.profile = el.profile?.value
      st.envelope = el.envelope?.value
      st.base = el.base?.disabled ? st.base : el.base?.value
      st.adopt = el.adopt?.value
      st.stack = el.stack?.value
      st.severity = el.severity?.value
      st.repro = el.repro?.value
      st.expected = el.expected?.value
      st.actual = el.actual?.value
      st.after = el.after ? el.after.filter(x => x.cb.checked).map(x => x.c.id) : st.after
    }

    const showErr = (msg) => {
      for (const e of Object.values(errs)) { e.hidden = true; e.closest('.field')?.classList.remove('bad') }
      if (!msg) return
      const name = fieldFor(msg)
      const target = (name && errs[name]) || errs._
      // said here, beside its field: the server's broadcast of the same
      // refusal would stand over the form as a toast as well
      hush(msg)
      clear(target).append(sentence(msg, st.form?.branches || []))
      target.hidden = false
      target.closest('.field')?.classList.add('bad')
      target.closest('.field')?.querySelector('input,textarea,select')?.focus()
    }

    // sentence capitalises a refusal's first word — unless that word is a
    // name (a branch: "main is the branch…", "free-1 has no commits…"),
    // which is shown as it is spelled
    const sentence = (msg, names) => {
      const first = msg.split(/\s/)[0]
      if (!/^[a-z]+$/.test(first) || names.includes(first)) return msg
      return msg[0].toUpperCase() + msg.slice(1)
    }

    const submit = async (autopilot) => {
      if (st.busy) return
      if (st.attachments.some((a) => a.pending)) { showErr('still uploading an image — wait a moment and try again'); return }
      if (st.attachments.some((a) => a.error)) { showErr('remove the failed attachment before creating'); return }
      keep()
      // typed in dollars, sent in credits
      const env = el.envelope.value.trim() === '' ? null : parseDollars(el.envelope.value)
      if (env?.err) { showErr(env.err); return }
      const req = {
        kind: st.kind.startsWith('research') ? 'research' : st.kind,
        diagnosis: st.kind === 'research:diagnosis' || undefined,
        title: el.title.value.trim(),
        description: el.desc.value.trim() || undefined,
        profile: el.profile.value || undefined,
        envelope: env ? env.credits : undefined,
        repo: st.repo || undefined,
        base: (!el.base.disabled && el.base.value) || undefined,
        adopt: el.adopt?.value || undefined,
        stackOn: el.stack?.value || undefined,
        dependsOn: el.after ? el.after.filter(x => x.cb.checked).map(x => x.c.id) : undefined,
        autopilot: autopilot || undefined,
        attachments: st.attachments.length ? st.attachments.map((a) => a.id) : undefined
      }
      if (st.kind === 'bug') {
        Object.assign(req, {
          severity: el.severity.value,
          repro: el.repro.value.trim() || undefined,
          expected: el.expected.value.trim() || undefined,
          actual: el.actual.value.trim() || undefined
        })
      }
      showErr('')
      st.busy = true
      el.create.disabled = true
      if (el.auto) el.auto.disabled = true
      try {
        const card = await ctx.api.post('/api/cards', req)
        ctx.close()
        ctx.toast(`Created ${card.id}${autopilot ? ' on autopilot' : ''}`)
        await ctx.refreshBoard?.()
        ctx.select(card.id)
      } catch (err) {
        showErr(err.data?.text || err.message)
      } finally {
        st.busy = false
        if (el.create.isConnected) {
          el.create.disabled = false
          if (el.auto) el.auto.disabled = false
        }
      }
    }

    load().then(ok => { if (ok) draw() })
  }
})
