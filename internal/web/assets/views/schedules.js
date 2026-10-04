// views/schedules.js — schedules and heartbeats (DESIGN §19.9): the
// timed triggers for freeform sessions. A mint starts a NEW freeform card
// on a cron cadence with its own envelope (the brake); a heartbeat sends
// a recurring turn into ONE existing session. Every definition is stored
// off and stays off across an edit, so the page's job is to make the
// on/off switch — and the confirm that guards it — impossible to miss.

import { h, clear, plural } from '../dom.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { errorBox, confirmStrip, field, segmented } from './kit.js?v=__ASSET_V__'

// the last fire a run-now answered with, so a refresh does not drop it
const results = new Map()

function debounce (fn, ms = 250) {
  let t = 0
  return () => { clearTimeout(t); t = setTimeout(fn, ms) }
}

function errText (err) {
  return err?.data?.text || err?.message || String(err)
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
    const v = { data: null, err: null, formOpen: false, busy: false, asking: null, notice: null }
    body.classList.add('sch')

    const list = h('div', { class: 'sch-list', testid: 'schedules-list' })
    const formSlot = h('div')
    const count = h('span', { class: 'sch-count', testid: 'schedules-count' })
    const newBtn = h('button', {
      class: 'btn pri', type: 'button', testid: 'schedules-new',
      onclick: () => { v.formOpen = !v.formOpen; drawForm() }
    }, '+ New schedule')
    body.append(
      h('div', { class: 'sch-bar' }, count, newBtn),
      h('p', { class: 'sch-lede' },
        'A mint starts a new freeform card on a cadence; a heartbeat sends a recurring turn into one session. Every schedule is off until you enable it, and an edit turns it off again.'),
      formSlot, list)

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
      formSlot.append(newScheduleForm(ctx, {
        done: () => { v.formOpen = false; drawForm(); load() }
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
      for (const sc of rows) list.append(scheduleRow(ctx, sc, v, load, draw, act))
      if (v.err) list.append(errorBox(v.err))
    }

    drawForm()
    load()
    const off = ctx.onEvent('board', debounce(load))
    return () => { off() }
  }
})

// ---- one row ----

function scheduleRow (ctx, sc, v, reload, draw, act) {
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

// ---- the add form ----

function newScheduleForm (ctx, { done }) {
  const kind = { value: 'mint' }
  const err = h('div', { class: 'sch-form-error', role: 'alert', testid: 'schedule-form-error' })
  const name = h('input', { type: 'text', testid: 'schedule-form-name', placeholder: 'nightly triage', required: true })
  const every = h('input', { type: 'text', testid: 'schedule-form-every', placeholder: '1h, 15m, @daily', value: '1h' })
  const cron = h('input', { type: 'text', testid: 'schedule-form-cron', placeholder: '0 5 * * *' })
  const tz = h('input', { type: 'text', testid: 'schedule-form-tz', placeholder: 'local' })
  const prompt = h('textarea', { rows: 2, testid: 'schedule-form-prompt', placeholder: 'what the session is asked to do' })
  const target = h('input', { type: 'text', testid: 'schedule-form-target', placeholder: 'FF-001', class: 'mono' })
  const repo = h('input', { type: 'text', testid: 'schedule-form-repo', placeholder: 'workspace default' })
  const backend = h('input', { type: 'text', testid: 'schedule-form-backend', placeholder: 'backend' })
  const model = h('input', { type: 'text', testid: 'schedule-form-model', placeholder: 'model' })
  const envelope = h('input', { type: 'number', min: '1', testid: 'schedule-form-envelope', value: '100' })

  const targetField = field('Heartbeat target (FF card)', target)
  const repoField = field('Repository', repo)
  const backendField = field('Backend', backend, { hint: 'optional — the profile\'s implementer by default' })
  const modelField = field('Model', model, { hint: 'optional' })
  const envelopeField = field('Envelope per card (credits)', envelope, { hint: 'the brake — what one minted card may spend' })

  // drawKind shows the fields each kind means: a heartbeat targets one
  // card and has no repository, backend, model or envelope of its own.
  function drawKind () {
    targetField.style.display = kind.value === 'heartbeat' ? '' : 'none'
    repoField.style.display = kind.value === 'heartbeat' ? 'none' : ''
    backendField.style.display = kind.value === 'heartbeat' ? 'none' : ''
    modelField.style.display = kind.value === 'heartbeat' ? 'none' : ''
    envelopeField.style.display = kind.value === 'heartbeat' ? 'none' : ''
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
        timezone: tz.value.trim() || undefined,
        prompt: prompt.value.trim()
      }
      if (kind.value === 'heartbeat') {
        body.kind = 'heartbeat'
        body.target = target.value.trim().toUpperCase()
      } else {
        body.kind = 'mint'
        body.repo = repo.value.trim() || undefined
        body.backend = backend.value.trim() || undefined
        body.model = model.value.trim() || undefined
        body.envelope = Number(envelope.value) || undefined
      }
      try {
        await ctx.api.post('/api/schedules', body)
      } catch (e) {
        err.append(errorBox(e, 'schedule-form-error-detail'))
        return
      }
      done()
    }
  },
  h('div', { class: 'sch-form-row' },
    field('Name', name),
    field('Kind', segmented([
      { value: 'mint', label: 'Mint a card' },
      { value: 'heartbeat', label: 'Heartbeat a session' }
    ], kind.value, (picked) => { kind.value = picked; drawKind() }, { testid: 'schedule-form-kind' }))),
  targetField, repoField,
  h('div', { class: 'sch-form-row' },
    field('Cadence — preset', every, { hint: '5m, 15m, 1h, 6h, @daily, @weekly' }),
    field('…or cron', cron, { hint: 'minute hour day month weekday' }),
    field('Timezone', tz)),
  field('Prompt', prompt, { hint: 'the opening turn of each minted card, or the recurring turn of the heartbeat' }),
  backendField, modelField, envelopeField,
  err,
  h('div', { class: 'sch-form-foot' },
    h('button', { class: 'btn', type: 'button', testid: 'schedule-form-cancel', onclick: done }, 'Cancel'),
    h('button', { class: 'btn pri', type: 'submit', testid: 'schedule-form-submit' }, 'Add schedule')))

  drawKind()
  return form
}
