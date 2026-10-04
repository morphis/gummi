// pair.js — the pairing form a browser without a device cookie sees: the
// six-digit code gummi printed in its terminal, and the name of the person
// pairing (devices paired under one name are one person; a code printed by
// `gummi web pair --name` carries its person, and the server says so if a
// name is missing). A wrong code says how many tries it has left; a new
// code can be asked for, and is printed in the terminal, never sent to the
// page — while one is still live, asking only says so.
//
// Once a device has the board, a browser paired with a code other than the
// one printed when the server started only waits to be let in (DESIGN
// §20.3): showPending is that wait, until a paired page approves it,
// rejects it, or it lapses — and the form, shown again, says which.

import { $, h, clear } from './dom.js?v=__ASSET_V__'
import { get, post } from './api.js?v=__ASSET_V__'

// refusals are what the form says when a wait ended without the board.
const refusals = {
  rejected: 'A device already at the board rejected this browser. If that was a mistake, pair again with a new code.',
  expired: 'Nobody at the board approved this browser in time, so the request lapsed. Pair again with a new code.'
}

export function showPair (session, onPaired) {
  const root = $('#pair')
  clear(root)
  // the name is not marked required: a code printed for a named person
  // carries it, and only the server knows which code this is — it says
  // so, without spending a guess, when a name is missing
  const name = h('input', { id: 'pair-name', testid: 'pair-name', name: 'name', maxlength: '40', autocomplete: 'nickname', 'aria-describedby': 'pair-name-hint' })
  const code = h('input', { id: 'pair-code', testid: 'pair-code', class: 'code', name: 'code', inputmode: 'numeric', autocomplete: 'one-time-code', pattern: '[0-9]*', maxlength: '6', placeholder: '······', spellcheck: 'false', required: true })
  const msg = h('p', { class: 'msg-err', testid: 'pair-error', role: 'alert' })
  const refused = refusals[session.approval]
    ? h('p', { class: 'msg-err', testid: 'pair-refused', role: 'alert' }, refusals[session.approval])
    : null
  const note = h('p', { class: 'msg-ok', testid: 'pair-note', role: 'status' })
  const btn = h('button', { class: 'btn pri', type: 'submit', testid: 'pair-submit' }, 'Pair')
  const form = h('form', { autocomplete: 'off', novalidate: true, testid: 'pair-form' },
    // the hint sits beside the label, not in it: it is the field's
    // description, not part of its name
    h('div', { class: 'field' }, h('label', { for: 'pair-name' }, 'Your name'), name,
      h('span', { class: 'fh', id: 'pair-name-hint', testid: 'pair-name-hint' }, 'Needed unless the code was printed with your name.')),
    h('label', { class: 'field', for: 'pair-code' }, 'Pairing code', code),
    btn, msg)
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    msg.textContent = ''
    note.textContent = ''
    const c = code.value.replace(/\D/g, '')
    if (c.length !== 6) { msg.textContent = 'The code is six digits.'; code.focus(); return }
    btn.disabled = true
    try {
      await post('/api/pair', { code: c, name: name.value.trim() })
      onPaired()
    } catch (err) {
      // the server's sentence already counts the tries left ("wrong
      // pairing code (2 tries left)"); say it only when it did not
      const left = err.data?.remaining
      const counted = /\btr(y|ies) left\b/i.test(err.message)
      msg.textContent = err.message + (left != null && !counted ? ` ${left === 1 ? '1 try' : left + ' tries'} left on this code.` : '')
      if (err.data?.error && /needs a name/.test(err.message)) { name.focus(); return }
      code.select()
    } finally {
      btn.disabled = false
    }
  })
  const again = h('button', {
    class: 'link', type: 'button', testid: 'pair-new',
    onclick: async () => {
      msg.textContent = ''
      try {
        const r = await post('/api/pair/request', {})
        note.textContent = r?.live
          ? 'A code is already showing in the terminal running gummi web. Use that one; a new one can be printed once it is used or expires.'
          : 'A new code is in the terminal running gummi web.'
      } catch (err) {
        msg.textContent = err.message
      }
    }
  }, 'Print a new code in the terminal')
  root.append(h('section', { class: 'pair-card', 'aria-labelledby': 'pair-title' },
    h('div', { class: 'brand' }, h('i', { 'aria-hidden': 'true' }), 'gummi'),
    h('h1', { id: 'pair-title' }, 'Pair this browser'),
    refused,
    h('p', null, session.pairingLive
      ? ['gummi printed a six-digit code in the terminal running ', h('code', { class: 'mono' }, 'gummi web'), '. It is good for a few minutes and dies after three wrong guesses.']
      : ['Run ', h('code', { class: 'mono' }, 'gummi web pair'), ' on the machine hosting the board, or ask for a code below; it is printed in that terminal.']),
    form, note, again))
  root.hidden = false
  name.focus()
}

// showPending is a paired browser's wait to be let in. It hears about
// itself on the event stream (the one event a waiting device is sent) and
// asks again every few seconds besides; once the answer is anything but
// "still waiting", onDecided shows whatever the session now says.
export function showPending (session, onDecided) {
  const root = $('#pair')
  clear(root)
  const started = Date.now()
  const total = session.expiresInSecs || 0
  const count = h('b', { testid: 'pending-expires', class: 'mono' })
  const msg = h('p', { class: 'msg-err', testid: 'pending-error', role: 'alert' })
  let es = null
  let poll = 0
  let tick = 0
  let done = false
  const stop = () => {
    done = true
    clearInterval(poll)
    clearInterval(tick)
    try { es?.close() } catch { /* already closed */ }
  }
  const check = async () => {
    if (done) return
    let s
    try { s = await get('/api/session') } catch { return }
    if (done || s.approval === 'pending') return
    stop()
    onDecided()
  }
  const draw = () => {
    const left = Math.max(0, total - Math.floor((Date.now() - started) / 1000))
    count.textContent = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
    if (left === 0) check()
  }
  const cancel = h('button', {
    class: 'link', type: 'button', testid: 'pending-cancel',
    onclick: async () => {
      msg.textContent = ''
      try { await post('/api/unpair', {}) } catch (err) { msg.textContent = err.message; return }
      stop()
      onDecided()
    }
  }, 'Withdraw this request')
  root.append(h('section', { class: 'pair-card', testid: 'pending', 'aria-labelledby': 'pending-title' },
    h('div', { class: 'brand' }, h('i', { 'aria-hidden': 'true' }), 'gummi'),
    h('h1', { id: 'pending-title' }, 'Waiting for approval'),
    h('p', { testid: 'pending-text' },
      'This browser is paired as ', h('b', null, session.person || 'you'),
      session.device ? [' on ', session.device] : null,
      ', but it cannot see the board until a device already paired with it lets it in. ',
      'The request is showing at the top of the board on every paired device — approve it there.'),
    h('p', { class: 'pending-wait', role: 'status' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }),
      h('span', null, 'Waiting — the request lapses in ', count, ' if nobody answers.')),
    msg, cancel))
  root.hidden = false
  draw()
  tick = setInterval(draw, 1000)
  poll = setInterval(check, 5000)
  es = new EventSource('/api/events')
  es.addEventListener('pairing', check)
  es.onerror = () => { if (es.readyState === EventSource.CLOSED) check() }
}
