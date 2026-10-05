// actions.js — a card's menu (Card.actions): the things the TUI offers on a
// card besides answering its decision. An action that needs input (a
// message, a number, a profile, a repository, a mode, cards, or a yes)
// collects it in a dialog first, prefilled with the action's default (the
// drafted landing message, the current budget, the dependencies already
// set); then POST /api/cards/{id}/actions/{action} runs it on the server,
// through the code the TUI's key runs.
//
// The server may still stop on a question the request did not answer (a
// "needs" or "confirm" question — a 202, which api.js throws as the 409 it
// used to be — with the question): the dialog asks it and sends
// again. A yes is never the page's to give on its own: an action that asks
// one (delete, clean, hand off …) is sent bare first, and the question the
// server answers with — its own words, line breaks and all — is what the
// dialog shows; the yes sent back is the token that question came with,
// which answers that question and nothing else. A refusal stays in the
// dialog, in the board's own words.

import { h, clear, isMobile } from './dom.js?v=__ASSET_V__'
import { post, cardPath } from './api.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'
import { toast, hush } from './toast.js?v=__ASSET_V__'
import { state, set, rows } from './store.js?v=__ASSET_V__'
import { openModelPicker, openWriteSpec } from './session.js?v=__ASSET_V__'

const NOUN = { message: 'Message', number: 'Credits', profile: 'Profile', repo: 'Repository', mode: 'Mode', cards: 'Waits for', text: 'Value' }

// GO is the confirm button of an entry whose label is a noun (the menu
// row names what it sets): the button says what pressing it does.
const GO = { envelope: 'Set budget', profile: 'Switch profile', repo: 'Set repository', deps: 'Save dependencies' }

function cap (s) { s = String(s || ''); return s ? s[0].toUpperCase() + s.slice(1) : s }

// goLabel is the dialog's confirm button for an action.
function goLabel (a) { return GO[a.id] || cap(a.label) }

// isLandingEntry: the entries whose message is a landing's (or a squash's)
// — the merge, and "next stage" where it lands (the server words that row
// as the landing and marks it dangerous).
function isLandingEntry (card, a) {
  return a.id === 'merge' || a.id === 'squash' || (a.id === 'advance' && card.stage === 'verify' && !!a.danger)
}

export async function runAction (card, a) {
  // a session's model and its spec have surfaces of their own (session.js):
  // the generic one-field dialog cannot carry an agent and a model, or a
  // profile and a budget
  if (a.needs === 'model') { openModelPicker(); return }
  if (a.needs === 'spec') { openWriteSpec(card, a); return }
  if (!a.needs || a.needs === 'confirm') {
    try {
      await send(card.id, a, {})
    } catch (err) {
      // an action with no input can still stop on a question: ask it
      if (err.status === 409 && (err.data?.error === 'needs' || err.data?.error === 'confirm')) {
        dialog(card, a, { ask: err.data })
      } else {
        toast(err.message, { err: true })
      }
    }
    return
  }
  dialog(card, a)
}

// dialog collects an action's input. ask is the question the server
// answered a first try with: what it stopped on, and what it needs.
function dialog (card, a, { ask = null } = {}) {
  const fields = new Map() // need -> { el, value() }
  const body = h('div', { class: 'aform' })
  const question = h('p', { class: 'aq', testid: 'action-question' }, a.detail ? cap(a.detail) + '.' : `${cap(a.label)} on ${card.id}.`)
  const error = h('p', { class: 'aerr', testid: 'action-error', role: 'alert', hidden: true })
  // the tokens of the questions the server asked and this dialog showed:
  // clicking yes under one sends it back, with any asked before it
  const confirms = []
  let asked = ''
  body.append(question)
  const addField = (need) => {
    if (!need || need === 'confirm' || fields.has(need)) return
    const f = fieldFor(card, a, need)
    fields.set(need, f)
    body.append(f.el)
  }
  addField(a.needs)
  body.append(error)
  const takeAsk = (e) => {
    if (e.error === 'confirm' || e.needs === 'confirm') {
      // the server's question, verbatim; its token is the only yes
      asked = e.confirm || ''
      question.classList.add('asked')
      clear(question).append(sentence(e.text) || `${cap(a.label)} ${card.id}?`)
      go.textContent = `Yes, ${a.label}`
      go.classList.add('danger')
    } else {
      addField(e.needs)
      clear(question).append(sentence(e.text) || question.textContent)
      // a landing stopped to have its drafted message read: it is the
      // field's value now, to read and edit before it lands
      if (e.draft != null && e.needs === 'message') {
        const f = fields.get('message')
        const ta = f?.el.querySelector('textarea')
        if (ta && !ta.value.trim() && e.draft.trim()) {
          ta.value = e.draft
          f.drafted?.(e.draft)
          ta.focus(); ta.setSelectionRange(0, 0); ta.scrollTop = 0
        }
      }
    }
  }
  const m = openModal({
    title: `${cap(a.label)} · ${card.id}`,
    testid: 'action-dialog',
    card: card.id,
    // the head redraws under a dialog: focus goes back to its menu button
    returnTo: '[data-testid="card-actions"]',
    body,
    actions: [
      { label: 'Cancel', testid: 'action-cancel' },
      {
        label: goLabel(a),
        primary: true,
        // a landing is pressed as the line it crosses, as the answer
        // block's landing dialog presses it
        danger: a.danger || a.id === 'merge',
        testid: 'action-confirm',
        onClick: async () => {
          const req = {}
          for (const [need, f] of fields) {
            const v = f.value()
            if (v === undefined) {
              // a field that cannot be sent as it stands says why, here,
              // before the board is asked
              const why = f.problem?.()
              if (why) { clear(error).append(why); error.hidden = false }
              f.focus?.()
              return false
            }
            Object.assign(req, v)
            void need
          }
          if (asked) { confirms.push(asked); asked = '' }
          if (confirms.length) req.confirm = confirms.join(' ')
          error.hidden = true
          go.disabled = true
          // a landing sent with no message waits on a drafting pass, a
          // model call that can take a minute: the dialog says so
          const msg = fields.get('message')
          const drafting = !!msg?.drafting && !req.message
          if (drafting) { msg.drafting(true); go.textContent = 'Drafting…' }
          try {
            await send(card.id, a, req)
            return true
          } catch (err) {
            const e = err.data || {}
            if (err.status === 409 && (e.error === 'needs' || e.error === 'confirm')) {
              takeAsk(e)
            } else {
              // said here, where it was asked; the board's broadcast of
              // the same refusal is not repeated over the dialog as a toast
              hush(err.message)
              clear(error).append(sentence(err.message))
              error.hidden = false
            }
            return false
          } finally {
            go.disabled = false
            if (drafting) {
              msg.drafting(false)
              if (!asked) go.textContent = goLabel(a)
            }
          }
        }
      }
    ]
  })
  const go = m.el.querySelector('[data-testid="action-confirm"]')
  if (ask) takeAsk(ask)
  // the message box opens with the cursor at the start: the default is to
  // be read before it is sent
  const ta = m.el.querySelector('textarea')
  if (ta) { ta.focus(); ta.setSelectionRange(0, 0); ta.scrollTop = 0 }
}

// landMethodSeq gives each landing's method choice its own radio group name.
let landMethodSeq = 0

// landMethodControl is the two-way choice beside a landing message: squash
// (the default, one commit) or a merge commit that keeps the branch's own
// commits. It is shown only where the server offers both methods
// (Action.methods, Option.methods); value() is the method the request names.
export function landMethodControl (methods) {
  const name = 'land-method-' + (++landMethodSeq)
  const words = { squash: 'Squash — one commit', merge: 'Merge commit — keeps the branch’s commits' }
  const radios = methods.map(m => h('input', { type: 'radio', name, value: m, checked: m === 'squash', testid: 'land-method-' + m }))
  const el = h('div', { class: 'land-method', testid: 'land-method', role: 'radiogroup', 'aria-label': 'Landing method' },
    methods.map((m, i) => h('label', { class: 'mopt' }, radios[i], ' ', words[m] || m)))
  return { el, value: () => radios.find(r => r.checked)?.value || 'squash' }
}

function fieldFor (card, a, need) {
  const label = h('span', { class: 'fl' }, NOUN[need] || 'Value')
  const def = a.needs === need ? (a.default || '') : ''
  if (need === 'message') {
    const landing = isLandingEntry(card, a)
    const ta = h('textarea', { id: 'action-input', testid: 'action-input', value: def, rows: landing ? 8 : 4, spellcheck: 'true' })
    const squash = a.id === 'squash'
    // where the message in the box came from, for the hint: a method
    // choice re-words the hint, so the origin is kept to say it again
    let from = def ? 'default' : 'none'
    const control = landing && a.methods?.length > 1 ? landMethodControl(a.methods) : null
    const merging = () => control?.value() === 'merge'
    const hint = landing ? h('span', { class: 'fh', testid: 'action-hint' }, messageHint(squash, from, merging())) : null
    // the affordance the answer block's landing dialog shares (its own
    // box is built there)
    const aff = hint ? draftAffordance(ta, hint) : null
    control?.el.addEventListener('change', () => { if (hint && !hint.classList.contains('busy')) hint.textContent = messageHint(squash, from, merging()) })
    return {
      el: h('label', { class: 'field' }, label, ta, control?.el, hint),
      value: () => (control ? { message: ta.value.trim(), method: control.value() } : { message: ta.value.trim() }),
      focus: () => ta.focus(),
      // the draft the server stopped to have read is in the box now: the
      // hint says so rather than that nothing was drafted, and where it
      // came from — the one stored when verify passed reads as that, not
      // as one gummi drafted just now
      drafted: (draft) => { from = draftOrigin(card, a, draft); if (aff) aff.drafted(messageHint(squash, from, merging())) },
      // while gummi drafts the message the box is not for typing and the
      // hint says what is being waited on; a reply that brought no draft
      // puts the hint back as it was
      drafting: aff ? aff.busy : null
    }
  }
  if (need === 'number') {
    const inp = h('input', { id: 'action-input', testid: 'action-input', type: 'number', min: '0', inputmode: 'numeric', value: def })
    return {
      el: h('label', { class: 'field' }, label, inp, a.id === 'envelope' ? h('span', { class: 'fh' }, '0 means uncapped.') : null),
      // credits are whole and never negative: anything else is said here,
      // not sent for the board to refuse as malformed
      value: () => /^\d+$/.test(inp.value.trim()) && Number.isSafeInteger(Number(inp.value.trim())) ? { number: Number(inp.value.trim()) } : undefined,
      problem: () => inp.value.trim() === '' && !inp.validity.badInput ? '' : 'Enter a whole number of credits, 0 or more.',
      focus: () => inp.focus()
    }
  }
  if (need === 'profile' || need === 'repo' || need === 'mode') {
    let choices = a.choices || []
    if (need === 'mode' && !choices.length) choices = [{ value: 'autopilot', label: 'autopilot', detail: 'gates cross unattended' }, { value: 'attended', label: 'attended', detail: 'a person crosses every gate' }]
    const cur = def || (need === 'profile' ? card.profile : need === 'repo' ? card.repo : '')
    const sel = h('select', { id: 'action-input', testid: 'action-input' },
      choices.map(c => h('option', { value: c.value, selected: c.value === cur }, c.detail ? `${c.label} — ${c.detail}` : c.label)))
    const key = need
    return { el: h('label', { class: 'field' }, label, sel), value: () => ({ [key]: sel.value }), focus: () => sel.focus() }
  }
  if (need === 'cards') return cardsField(card, a, label, def)
  if (a.id === 'prlink') clear(label).append('Pull request')
  const inp = h('input', {
    id: 'action-input', testid: 'action-input', value: def, autocomplete: 'off', spellcheck: 'false',
    placeholder: a.id === 'prlink' ? 'https://github.com/owner/repo/pull/7, or 7' : null
  })
  return { el: h('label', { class: 'field' }, label, inp), value: () => ({ message: inp.value.trim() }), focus: () => inp.focus() }
}

// messageHint is the line under a landing's or a squash's message: where
// the message in the box came from (the verify gate's draft, one drafted
// just now, or none yet) and what it becomes. squash says which: a squash
// collapses the branch where it is — nothing lands — so it never says
// "lands". The answer block's landing dialog words its own box the same
// way (decision.js).
export function messageHint (squash, from, merge = false) {
  let becomes = squash ? 'this is the one commit the branch becomes' : 'this is what lands'
  if (merge) becomes = 'this is the merge commit’s message; the branch’s commits land with it'
  if (from === 'none') return 'Nothing was drafted yet: leave it empty and gummi drafts one for you to read first (this can take a minute), or write it.'
  const where = from === 'drafted' ? 'Drafted by gummi just now.' : squash ? 'Drafted for this branch.' : 'Drafted when verify passed.'
  return `${where} Read it, edit it if you like — ${becomes}.`
}

// draftOrigin says where a draft the board handed back came from, for
// messageHint: the stored one (the entry's default, or the merge entry's
// for the verify gate's landing) when it is exactly that, else one a pass
// drafted just now.
function draftOrigin (card, a, draft) {
  const stored = String(a.default || card.actions?.find(x => x.id === 'merge')?.default || '').trim()
  return stored && String(draft || '').trim() === stored ? 'default' : 'drafted'
}

// draftAffordance is the drafting state of a landing message box, shared
// by the menu's merge dialog and the answer block's landing dialog: busy
// makes the box read-only and the hint say what is being waited on, off
// restores the hint as busy found it, and drafted replaces the hint for a
// reply that brought the draft to read — cancelling that restore, since
// what busy was waiting for has arrived.
export function draftAffordance (ta, hint) {
  let before = null
  return {
    busy: (on) => {
      ta.readOnly = on
      if (on) {
        before = hint.textContent
        hint.classList.add('busy')
        clear(hint).append(h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'gummi is drafting the message — this can take a minute.')
      } else {
        hint.classList.remove('busy')
        if (before !== null) { clear(hint).append(before); before = null }
      }
    },
    drafted: (text) => { before = null; clear(hint).append(text) }
  }
}

// cardsField is a multi-select of the board's other cards, the ones set
// already ticked. Clearing every tick clears the dependencies.
function cardsField (card, a, label, def) {
  const set0 = new Set(String(def).split(',').map(s => s.trim()).filter(Boolean))
  const cands = rows().filter(r => r.id !== card.id && (r.status !== 'done' || set0.has(r.id)))
  const filter = h('input', { class: 'cfilter', testid: 'action-cards-filter', placeholder: 'Filter cards', 'aria-label': 'Filter cards', autocomplete: 'off' })
  const boxes = cands.map(r => {
    const cb = h('input', { type: 'checkbox', value: r.id, checked: set0.has(r.id), testid: `action-card-${r.id}` })
    return { r, cb, el: h('label', { class: 'cpick' }, cb, h('span', { class: 'id' }, r.id), h('span', { class: 't' }, r.title), h('span', { class: 's' }, r.stage)) }
  })
  const list = h('div', { class: 'cpicks', id: 'action-input', testid: 'action-input', role: 'group', 'aria-label': 'Cards' }, boxes.map(b => b.el))
  filter.addEventListener('input', () => {
    const q = filter.value.toLowerCase().trim()
    for (const b of boxes) b.el.hidden = !!q && !`${b.r.id} ${b.r.title}`.toLowerCase().includes(q)
  })
  return {
    el: h('div', { class: 'field' }, label, boxes.length > 6 ? filter : null, boxes.length ? list : h('span', { class: 'fh' }, 'No other card to wait for.')),
    value: () => ({ cards: boxes.filter(b => b.cb.checked).map(b => b.r.id) }),
    focus: () => boxes[0]?.cb.focus()
  }
}

// send runs the action. It throws the ApiError for the caller to place.
async function send (id, a, body) {
  // every action is sent against the stop the page shows, so one meant
  // for it is refused with "moved" rather than run at another; the server
  // refuses one that carries none while a decision is pinned
  const d = state.sel === id ? state.card?.decision : null
  if (d?.against?.token && !body.against) body = { ...body, against: d.against.token }
  const res = await post(cardPath(id, `actions/${encodeURIComponent(a.id)}`), body)
  // what the action changed is read again, documents included
  if (res && res.id === state.sel) set({ card: res, cardRev: (state.cardRev || 0) + 1 })
  if (res?.ok && !res.id) {
    toast(`${cap(a.label)}: ${id} is gone`, { ack: id })
    // the action removed the card this page has open, and it was this
    // page's own doing: on a phone the card's screen covers the cards, so
    // the page goes back the way the screen's back chevron does. A
    // deletion that arrives from elsewhere keeps the page still (the
    // board watcher in selection.js), and so does the desktop, where the
    // cards stay beside the open card.
    if (state.sel === id && isMobile()) set({ view: 'cards' })
  } else {
    toast(`${cap(a.label)} · ${id}`, { ack: id })
  }
  return res
}

function sentence (s) {
  s = String(s || '').trim()
  return s ? s[0].toUpperCase() + s.slice(1) : ''
}

// sentChanges remembers, per card and surface, what "Request changes"
// last sent: the comments stay open until the agent answers them, so the
// tab keeps offering the button — and a second press would send the same
// comments twice. While what the tab shows is what was sent (the same
// open comments at the same revision and stage) the button reads "Sent"
// and stays down; a new comment, a resolved one, a new revision or a
// moved card is something new to send.
const sentChanges = new Map() // `${id}:${what}` -> the signature sent

// changesButton is the spec and diff tabs' "Request changes" button. sig
// names what it would send as the tab shows it.
export function changesButton (id, what, sig, title) {
  const key = `${id}:${what}`
  const sent = sentChanges.get(key) === sig
  return h('button', {
    class: 'btn',
    type: 'button',
    testid: `${what}-request-changes`,
    title: sent ? 'These comments were sent; the button comes back when something changes' : title,
    disabled: sent,
    onclick: (e) => requestChanges(id, what, e.currentTarget, sig)
  }, sent ? 'Sent' : 'Request changes')
}

// requestChanges is the spec and diff tabs' "Request changes" — the TUI's
// R on those surfaces: the card's open comments on what (spec or diff) go
// to the agent that writes them, POST …/{what}/changes. When one of them
// belongs to an earlier stage the board answers with a "confirm" question
// instead — send the card back to that stage? — asked here in its own
// words; the yes is the token it came with, and sends it again. The
// board's answer is its own sentence; the broadcast of the same one shows
// once. Once sent, the button stays down (sentChanges).
async function requestChanges (id, what, btn, sig) {
  const key = `${id}:${what}`
  const sent = () => {
    sentChanges.set(key, sig)
    if (btn.isConnected) { btn.textContent = 'Sent'; btn.disabled = true }
  }
  btn.disabled = true
  btn.textContent = 'Sending…'
  try {
    const res = await post(cardPath(id, `${what}/changes`), {})
    sent()
    toast(res?.text || 'Comments sent')
  } catch (err) {
    btn.disabled = false
    btn.textContent = 'Request changes'
    if (err.status === 409 && err.data?.error === 'confirm') {
      confirmChanges(id, what, err.data, sent)
    } else {
      toast(err.notBuilt ? 'Requesting changes from the web is not available yet' : err.message, { err: !err.notBuilt })
    }
  }
}

// confirmChanges asks the send-back the board stopped on, and sends the
// request again with the yes to it.
function confirmChanges (id, what, ask, sent) {
  const error = h('p', { class: 'aerr', testid: 'changes-error', role: 'alert', hidden: true })
  openModal({
    title: `Request changes · ${id}`,
    testid: 'changes-confirm',
    card: id,
    returnTo: `[data-testid="${what}-request-changes"]`,
    body: [h('p', { class: 'aq asked', testid: 'changes-question' }, sentence(ask.text)), error],
    actions: [
      { label: 'Cancel', testid: 'changes-cancel' },
      {
        label: 'Send back',
        primary: true,
        danger: true,
        testid: 'changes-go',
        onClick: async () => {
          try {
            const res = await post(cardPath(id, `${what}/changes`), { confirm: ask.confirm })
            sent()
            toast(res?.text || 'Sent back')
            return true
          } catch (err) {
            clear(error).append(sentence(err.message))
            error.hidden = false
            return false
          }
        }
      }
    ]
  })
}
