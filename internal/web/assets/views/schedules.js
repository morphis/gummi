// views/schedules.js — schedules and heartbeats (DESIGN §19.9): the
// timed triggers for freeform sessions. A mint starts a NEW freeform card
// on a cron cadence with its own envelope (the brake); a heartbeat sends
// a recurring turn into ONE existing session. Every definition is stored
// off and stays off across an edit, so the page's job is to make the
// on/off switch — and the confirm that guards it — impossible to miss.
//
// One form creates and edits. The cadence is answered as the person
// types (a debounced round trip to the preview route — the page parses
// no cron of its own), the backend and model come from the session
// picker's catalog, and the timezone is a shortlist plus free text.

import { h, clear, plural } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { errorBox, confirmStrip, field, segmented, choose } from './kit.js?v=__ASSET_V__'

// the last fire a run-now answered with, so a refresh does not drop it
const results = new Map()

// the timezone shortlist: the zones a schedule is plausibly set in, with
// "local" — the host's zone, stored as no timezone at all — first. Any
// other IANA name types in freely; the server validates it.
const timezones = [
  'local', 'UTC',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Sao_Paulo', 'Europe/London', 'Europe/Paris', 'Europe/Berlin',
  'Europe/Helsinki', 'Europe/Moscow', 'Asia/Dubai', 'Asia/Kolkata',
  'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Australia/Sydney',
  'Pacific/Auckland',
]

// tzStored maps what the timezone field holds to what the store keeps:
// "local" and empty both mean the host's zone, which the store spells as
// no timezone. Any other name travels as typed and is validated there.
function tzStored (raw) {
  const v = raw.trim()
  return !v || v === 'local' ? undefined : v
}

function debounce (fn, ms = 250) {
  let t = 0
  return () => { clearTimeout(t); t = setTimeout(fn, ms) }
}

function errText (err) {
  return err?.data?.text || err?.message || String(err)
}

// fmtFire shows one fire time on the wall clock of the zone it was
// computed in — what the cadence means there, not this browser's local
// reading of the instant.
function fmtFire (iso, tz) {
  const zone = tz && tz !== 'local' ? tz : undefined
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: zone, dateStyle: 'medium', timeStyle: 'short'
    }).format(new Date(iso))
  } catch {
    return new Date(iso).toLocaleString()
  }
}

// when shows a row's next fire as a phrase; the server stores an instant,
// and "in 45m" is what a person acts on.
function when (iso) {
  if (!iso) return null
  const ms = new Date(iso) - Date.now()
  if (ms <= 0) return 'due now'
  const m = Math.round(ms / 60000)
  if (m < 1) return 'in under a minute'
  if (m < 60) return `in ${m}m`
  const hEl = Math.round(m / 60)
  if (hEl < 24) return `in ${hEl}h`
  return `in ${Math.round(hEl / 24)}d`
}

function statusClass (status) {
  if (status === 'ok') return 't-ok'
  if (status === 'skipped-busy' || status === 'paused-exhausted' || status === 'disabled-target-closed') return 't-warn'
  if (status) return 't-err'
  return 'mute'
}

function fireText (fire) {
  if (fire.status === 'ok') {
    return fire.kind === 'mint' ? `minted ${fire.card}` : 'sent its turn'
  }
  if (fire.status === 'skipped-busy') return 'skipped — the session is mid-turn'
  return `${fire.status}${fire.detail ? ' — ' + fire.detail : ''}`
}

registerView('schedules', {
  title: 'Schedules',
  css: 'views/schedules.css',
  mount (body, ctx) {
    const v = { data: null, err: null, formOpen: false, editing: null, catalog: null, busy: false, asking: null, notice: null }
    body.classList.add('sch')

    const list = h('div', { class: 'sch-list', testid: 'schedules-list' })
    const formSlot = h('div')
    const count = h('span', { class: 'sch-count', testid: 'schedules-count' })
    const newBtn = h('button', {
      class: 'btn pri', type: 'button', testid: 'schedules-new',
      onclick: () => { v.formOpen && !v.editing ? closeForm() : openForm(null) }
    }, '+ New schedule')
    body.append(
      h('div', { class: 'sch-bar' }, count, newBtn),
      h('p', { class: 'sch-lede' },
        'A mint starts a new freeform card on a cadence; a heartbeat sends a recurring turn into one session. Every schedule is off until you enable it, and an edit turns it off again.'),
      formSlot, list)

    function closeForm () {
      v.formOpen = false
      v.editing = null
      drawForm()
    }

    async function openForm (row) {
      v.editing = row || null
      v.formOpen = true
      // the catalog is fetched before the first draw, so the form the
      // person starts typing into is the one that stays
      if (!v.catalog) {
        try { v.catalog = await ctx.api.get('/api/schedules/catalog') } catch (err) {
          // the form renders without rows; a pair it cannot offer is
          // refused at save, in the board's own words
        }
      }
      drawForm()
    }

    async function load () {
      try {
        const next = await ctx.api.get('/api/schedules')
        v.data = next
        v.err = null
      } catch (err) {
        if (!v.data) v.err = err
      }
      draw()
    }

    async function act (path, method, body) {
      v.busy = true
      draw()
      try {
        const res = method === 'DELETE'
          ? await ctx.api.del(path)
          : await ctx.api.api(method, path, body)
        v.busy = false
        return res
      } catch (err) {
        v.busy = false
        v.asking = null
        v.notice = errText(err)
        draw()
        throw err
      }
    }

    function drawForm () {
      clear(formSlot)
      newBtn.setAttribute('aria-expanded', String(v.formOpen))
      if (!v.formOpen) return
      formSlot.append(scheduleForm(ctx, {
        row: v.editing,
        catalog: v.catalog,
        done: () => { closeForm(); load() }
      }))
    }

    function draw () {
      clear(list)
      const rows = v.data?.schedules || []
      count.textContent = plural(rows.length, 'schedule')
      if (v.notice) {
        list.append(h('div', { class: 'sch-notice', role: 'status', testid: 'schedules-notice' }, v.notice))
      }
      if (!rows.length) {
        list.append(h('div', { class: 'empty', testid: 'schedules-empty' },
          h('b', null, 'No schedules yet'),
          'Define one below, or with `gummi schedule add`. A cron that mints a card every night, or a heartbeat that checks in hourly — off until you switch it on.'))
        return
      }
      for (const sc of rows) list.append(scheduleRow(ctx, sc, v, openForm, load, draw, act))
      if (v.err) list.append(errorBox(v.err))
    }

    drawForm()
    load()
    const off = ctx.onEvent('board', debounce(load))
    return () => { off() }
  }
})

// ---- one row ----

function scheduleRow (ctx, sc, v, openForm, reload, draw, act) {
  const status = sc.lastStatus
    ? h('span', { class: ['sch-status', statusClass(sc.lastStatus)], testid: `schedule-${sc.id}-status` }, sc.lastStatus)
    : h('span', { class: 'sch-status mute', testid: `schedule-${sc.id}-status` }, 'never fired')
  const next = sc.enabled && sc.nextRun
    ? h('span', { class: 'sch-next', testid: `schedule-${sc.id}-next` }, `next ${when(sc.nextRun)}`)
    : null

  const row = h('div', { class: 'sch-row', testid: `schedule-${sc.id}`, data: { enabled: String(!!sc.enabled) } },
    h('div', { class: 'sch-head' },
      h('b', { class: 'sch-name' }, sc.name),
      h('span', { class: 'sch-kind' }, sc.kind === 'heartbeat' ? `heartbeat → ${sc.target}` : (sc.repo ? `mint · ${sc.repo}` : 'mint')),
      h('span', { class: ['sch-toggle', sc.enabled ? 'on' : 'off'], testid: `schedule-${sc.id}-state` }, sc.enabled ? 'on' : 'off'),
      h('span', { class: 'grow' }),
      next, status),
    h('div', { class: 'sch-line' },
      h('code', { class: 'sch-cron' }, sc.cron),
      sc.timezone ? h('span', { class: 'sch-meta' }, sc.timezone) : null,
      sc.envelope ? h('span', { class: 'sch-meta' }, `${sc.envelope} credits per card`) : null,
      sc.lastCard ? h('span', { class: 'sch-meta' }, `last card ${sc.lastCard}`) : null,
      sc.orphanCard ? h('span', { class: 'sch-meta warn', testid: `schedule-${sc.id}-orphan` }, `retrying ${sc.orphanCard}`) : null),
    sc.lastDetail ? h('p', { class: 'sch-detail' }, sc.lastDetail) : null,
    h('div', { class: 'sch-actions' },
      h('button', {
        class: 'btn', type: 'button', testid: `schedule-${sc.id}-toggle`, disabled: v.busy,
        onclick: () => {
          if (sc.enabled) { v.asking = null; draw(); toggleOff(ctx, sc, reload); return }
          v.asking = { sc, what: 'enable' }
          draw()
        }
      }, sc.enabled ? 'Turn off' : 'Turn on'),
      h('button', {
        class: 'btn', type: 'button', testid: `schedule-${sc.id}-edit`, disabled: v.busy,
        onclick: () => openForm(sc)
      }, 'Edit'),
      h('button', {
        class: 'btn', type: 'button', testid: `schedule-${sc.id}-run`, disabled: v.busy,
        onclick: () => { v.asking = { sc, what: 'run' }; draw() }
      }, 'Run now'),
      h('button', {
        class: 'btn', type: 'button', testid: `schedule-${sc.id}-rm`, disabled: v.busy,
        onclick: () => { v.asking = { sc, what: 'delete' }; draw() }
      }, 'Delete')))

  if (v.asking && v.asking.sc?.id === sc.id) {
    const what = v.asking.what
    const yes = async () => {
      v.asking = null
      try {
        if (what === 'enable') {
          const armed = await act(`/api/schedules/${encodeURIComponent(sc.id)}/enable`, 'POST', {})
          results.set(sc.id, { status: 'ok', text: `enabled — next fire ${when(armed.nextRun) || 'soon'}` })
        } else if (what === 'run') {
          const fire = await act(`/api/schedules/${encodeURIComponent(sc.id)}/run`, 'POST', {})
          results.set(sc.id, { status: fire.status, text: fireText(fire) })
        } else {
          await act(`/api/schedules/${encodeURIComponent(sc.id)}`, 'DELETE')
          results.delete(sc.id)
        }
      } catch (err) { /* act reported it */ }
      reload()
      return false
    }
    row.append(confirmStrip({
      question: what === 'enable'
        ? `Enable ${sc.name || sc.id}?`
        : what === 'run'
          ? `Fire ${sc.name || sc.id} now, once?`
          : `Delete ${sc.name || sc.id}?`,
      detail: what === 'enable'
        ? 'It fires on its cadence from now, spending its card\'s envelope.'
        : what === 'run'
          ? 'It fires once right now, even though the schedule is off. A mint starts a card with its envelope; a heartbeat sends the turn into the session.'
          : 'The row goes; the cards it minted stay on the board.',
      yes: what === 'enable' ? 'Enable' : what === 'run' ? 'Run' : 'Delete',
      danger: what === 'delete',
      testid: `schedule-${sc.id}-confirm`,
      onYes: yes,
      onNo: () => { v.asking = null; draw(); return false }
    }))
  }

  const res = results.get(sc.id)
  if (res) {
    row.append(h('div', { class: ['sch-result', statusClass(res.status)], testid: `schedule-${sc.id}-result` }, res.text))
  }
  return row
}

async function toggleOff (ctx, sc, reload) {
  try {
    await ctx.api.post(`/api/schedules/${encodeURIComponent(sc.id)}/disable`, {})
  } catch (err) {
    // the row shows the refusal on the next draw
  }
  reload()
}

// ---- the form: one of it creates, the same of it edits ----

function scheduleForm (ctx, { row, catalog, done }) {
  const editing = !!row
  const kind = { value: row?.kind || 'mint' }
  const err = h('div', { class: 'sch-form-error', role: 'alert', testid: 'schedule-form-error' })
  const name = h('input', { type: 'text', testid: 'schedule-form-name', placeholder: 'nightly triage', required: true, value: row?.name || '' })
  const every = h('input', { type: 'text', testid: 'schedule-form-every', placeholder: '1h, 15m, @daily', value: editing ? '' : '1h' })
  const cron = h('input', { type: 'text', testid: 'schedule-form-cron', placeholder: '0 5 * * *', value: row?.cron || '' })
  const tz = h('input', {
    type: 'text', testid: 'schedule-form-tz', placeholder: 'local',
    list: 'schedule-tz-list', value: row?.timezone || 'local', autocomplete: 'off'
  })
  const prompt = h('textarea', { rows: 2, testid: 'schedule-form-prompt', placeholder: 'what the session is asked to do' })
  if (row?.prompt) prompt.value = row.prompt
  const target = h('input', { type: 'text', testid: 'schedule-form-target', placeholder: 'FF-001', class: 'mono', value: row?.target || '' })
  const repo = h('input', { type: 'text', testid: 'schedule-form-repo', placeholder: 'workspace default', value: row?.repo || '' })
  const backendPick = { name: row?.backend || '' }
  const backend = choose(
    [{ value: '', label: 'profile default' }],
    { value: backendPick.name, testid: 'schedule-form-backend', label: 'Agent',
      onchange: (e) => {
        backendPick.name = e.target.value
        if (!(agentFor(backendPick.name)?.models || []).includes(model.value.trim())) model.value = ''
        drawCatalog()
        askPreview()
      } })
  const model = h('input', {
    type: 'text', testid: 'schedule-form-model', placeholder: "the agent's default", list: 'schedule-model-list',
    autocomplete: 'off', value: row?.model || ''
  })
  const envelope = h('input', { type: 'number', min: '1', testid: 'schedule-form-envelope', value: String(row?.envelope ?? 100) })
  const tzList = h('datalist', { id: 'schedule-tz-list' }, timezones.map(z => h('option', { value: z })))
  const modelList = h('datalist', { id: 'schedule-model-list' })

  const targetField = field('Heartbeat target (FF card)', target)
  const repoField = field('Repository', repo)
  const backendField = field('Agent', backend, { hint: "optional — the profile's implementer by default" })
  const modelField = field('Model', model, { testid: 'schedule-form-model-field' })
  const envelopeField = field('Envelope per card (credits)', envelope, { hint: 'the brake — what one minted card may spend' })

  // drawCatalog fills the pickers from the session picker's catalog:
  // every agent a session can run on, its installed flag on the row, and
  // the models it names itself (plus the ids this workspace runs) as the
  // model field's suggestions — any other id still types in.
  function drawCatalog () {
    clear(modelList)
    const agents = catalog?.agents || []
    clear(backend)
    backend.append(h('option', { value: '', selected: !backendPick.name }, 'profile default'))
    for (const a of agents) {
      backend.append(h('option', {
        value: a.name,
        selected: a.name === backendPick.name
      }, a.installed ? a.name : `${a.name} — not installed`))
    }
    const agent = agentFor(backendPick.name)
    modelField.querySelector('small.hint')?.remove()
    if (agent) {
      modelField.append(h('small', { class: 'hint' }, agent.hint || (agent.needsModel ? 'this agent needs a model id' : 'optional')))
      if (agent.needsModel && !model.value.trim()) model.placeholder = agent.hint || 'provider/model'
    }
    for (const m of agent?.models || []) modelList.append(h('option', { value: m }))
  }

  function agentFor (name) { return (catalog?.agents || []).find(a => a.name === name) }

  // ---- the cadence preview: the server answers what the store would
  // hold and when it would fire; the page parses no cron of its own.
  const preview = h('div', { class: 'sch-preview', testid: 'schedule-form-preview' })
  let previewSeq = 0
  const askPreview = debounce(async () => {
    const seq = ++previewSeq
    const body = {
      every: cron.value.trim() ? undefined : (every.value.trim() || undefined),
      cron: cron.value.trim() || undefined,
      timezone: tzStored(tz.value)
    }
    if (kind.value === 'mint') {
      body.backend = backendPick.name || undefined
      body.model = model.value.trim() || undefined
    }
    try {
      const res = await ctx.api.post('/api/schedules/preview', body)
      if (seq === previewSeq) drawPreview(res)
    } catch (err) { /* the last answer stays up until the next keystroke */ }
  }, 250)

  function drawPreview (res) {
    clear(preview)
    const zone = tzStored(tz.value) || 'local'
    if (res.error) {
      preview.append(h('div', { class: 'sch-preview-err', testid: 'schedule-form-preview-error' }, res.error))
      return
    }
    preview.append(h('div', { class: 'sch-preview-cron', testid: 'schedule-form-preview-cron' },
      'stored as ', h('code', null, res.cron)))
    if (res.fires?.length) {
      preview.append(h('div', { class: 'sch-preview-fires', testid: 'schedule-form-preview-fires' },
        'next fires: ', res.fires.map((f, i) => h('span', { class: 'sch-fire' }, (i ? ' · ' : '') + fmtFire(f, zone)))))
    }
    if (kind.value === 'mint' && res.envelopeHint) {
      preview.append(h('div', { class: 'sch-preview-hint', testid: 'schedule-form-envelope-hint' }, res.envelopeHint))
    }
  }

  // drawKind shows the fields each kind means: a heartbeat targets one
  // card and has no repository, agent, model or envelope of its own.
  function drawKind () {
    const hb = kind.value === 'heartbeat'
    targetField.style.display = hb ? '' : 'none'
    for (const f of [repoField, backendField, modelField, envelopeField]) {
      f.style.display = hb ? 'none' : ''
    }
    askPreview()
  }

  const form = h('form', {
    class: 'sch-form', testid: 'schedule-form',
    onsubmit: async (ev) => {
      ev.preventDefault()
      clear(err)
      const body = {
        name: name.value.trim(),
        cron: cron.value.trim() || undefined,
        every: cron.value.trim() ? undefined : (every.value.trim() || undefined),
        timezone: tzStored(tz.value),
        prompt: prompt.value.trim()
      }
      if (kind.value === 'heartbeat') {
        body.kind = 'heartbeat'
        body.target = target.value.trim().toUpperCase()
      } else {
        body.kind = 'mint'
        body.repo = repo.value.trim() || undefined
        body.backend = backendPick.name || undefined
        body.model = model.value.trim() || undefined
        body.envelope = Number(envelope.value) || undefined
      }
      try {
        if (editing) {
          await ctx.api.api('PATCH', `/api/schedules/${encodeURIComponent(row.id)}`, body)
        } else {
          await ctx.api.post('/api/schedules', body)
        }
      } catch (e) {
        err.append(errorBox(e, 'schedule-form-error-detail'))
        return
      }
      done()
    }
  },
  h('div', { class: 'sch-form-row' },
    field('Name', name),
    editing
      ? field('Kind', h('span', { class: 'sch-kind-fixed', testid: 'schedule-form-kind-fixed' },
          kind.value === 'heartbeat' ? 'heartbeat' : 'mint'), { hint: 'an edit keeps the kind' })
      : field('Kind', segmented([
          { value: 'mint', label: 'Mint a card' },
          { value: 'heartbeat', label: 'Heartbeat a session' }
        ], kind.value, (picked) => { kind.value = picked; drawKind() }, { testid: 'schedule-form-kind' }))),
  targetField, repoField,
  h('div', { class: 'sch-form-row' },
    field('Cadence — preset', every, { hint: '5m, 15m, 1h, 6h, @daily, @weekly' }),
    field('…or cron', cron, { hint: 'minute hour day month weekday' }),
    field('Timezone', tz, { hint: 'type any IANA zone' }), tzList),
  preview,
  field('Prompt', prompt, { hint: 'the opening turn of each minted card, or the recurring turn of the heartbeat' }),
  backendField, modelField, modelList, envelopeField,
  err,
  h('div', { class: 'sch-form-foot' },
    h('button', { class: 'btn', type: 'button', testid: 'schedule-form-cancel', onclick: done }, 'Cancel'),
    h('button', { class: 'btn pri', type: 'submit', testid: 'schedule-form-submit' }, editing ? 'Save changes' : 'Add schedule')))

  every.addEventListener('input', askPreview)
  cron.addEventListener('input', askPreview)
  tz.addEventListener('input', askPreview)
  model.addEventListener('input', askPreview)

  drawCatalog()
  drawKind()
  return form
}
