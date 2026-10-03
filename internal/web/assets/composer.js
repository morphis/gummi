// composer.js — the line under the thread, and the sentence under it that
// says what enter will do. The server classifies every line exactly as the
// TUI would (POST …/composer, asked as the person types, debounced) and the
// page shows what it said. With a decision pinned, a line the server reads
// as an answer goes with the decision: enter answers it, and the sentence
// is the highlighted option (relabelled once words are typed; typing moves
// the highlight to the answer that takes words). Any other line — a
// command, a line for the menu — is sent as a line.
//
// A sent line can come back: "busy" (the agent is mid-turn) puts it back in
// the field with a note, "menu" opens the card's actions, "newcard" opens
// the new-card form seeded with it.
//
// With a session draft open (session.js) there is no card yet: the line is
// the session's first message, and sending it creates the session.

import { h, $, clear } from './dom.js?v=__ASSET_V__'
import { post, cardPath, uploadAttachment } from './api.js?v=__ASSET_V__'
import { on, set, state } from './store.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { answer, openDecision, wordsOption, highlight, enterSays, sentence, togglePick } from './decision.js?v=__ASSET_V__'
import { openActions } from './head.js?v=__ASSET_V__'
import { openView } from './views.js?v=__ASSET_V__'
import { startSession } from './session.js?v=__ASSET_V__'

let classify = true // POST …/composer is answered by this server
let said = null // { id, text, says, route } for the open card
let timer = 0
let sending = false
let ctxRef = {}
let noteText = ''
// attachments pending on the composer: {id, name, mediaType, size,
// pending, error}. id is set once the upload answers; pending/error are
// upload-in-flight state a send must wait out (or refuse to send with).
let attachments = []
// the project commands a half-typed "/word" could become (Composer
// .Completions), as last offered, and which one tab would take
let offered = []
let pick = 0

export function initComposer (ctx) {
  ctxRef = ctx
  const input = $('#composer-input')
  input.addEventListener('input', () => {
    autosize()
    set({ draft: input.value })
    const d = openDecision()
    if (d && input.value.trim()) {
      const w = wordsOption()
      if (w >= 0 && !d.options[state.hi]?.words) highlight(w, 'aim')
    }
    if (noteText) setNote('')
    ask()
    renderSays()
  })
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); submit(); return }
    if (offered.length && !$('#composer-complete').hidden) {
      if (e.key === 'Tab' && !e.shiftKey) { e.preventDefault(); complete(offered[pick]); return }
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        e.preventDefault()
        pick = (pick + (e.key === 'ArrowDown' ? 1 : -1) + offered.length) % offered.length
        renderCompletions()
        return
      }
      if (e.key === 'Escape') { offered = []; renderCompletions(); return }
    }
    if (e.key === 'Escape') { input.blur(); return }
    const d = openDecision()
    if (d && !input.value) {
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        e.preventDefault()
        const n = d.options.length
        highlight((state.hi + (e.key === 'ArrowDown' ? 1 : -1) + n) % n)
      } else if (/^[1-9]$/.test(e.key) && +e.key <= d.options.length) {
        e.preventDefault()
        highlight(+e.key - 1)
      } else if (e.key === ' ' && d.multi && !d.options[state.hi]?.chat) {
        e.preventDefault()
        togglePick(d.options[state.hi].id)
      }
    }
  })
  $('#send').addEventListener('click', submit)
  $('#composer-attach').addEventListener('click', () => $('#composer-file').click())
  $('#composer-file').addEventListener('change', (e) => {
    const files = [...e.target.files]
    e.target.value = ''
    addFiles(files)
  })
  input.addEventListener('paste', (e) => {
    const files = [...(e.clipboardData?.items || [])]
      .filter((it) => it.kind === 'file' && it.type.startsWith('image/'))
      .map((it) => it.getAsFile())
      .filter(Boolean)
    if (files.length) { e.preventDefault(); addFiles(files) }
  })
  input.addEventListener('dragover', (e) => {
    if ([...(e.dataTransfer?.items || [])].some((it) => it.kind === 'file' && it.type.startsWith('image/'))) e.preventDefault()
  })
  input.addEventListener('drop', (e) => {
    const files = [...(e.dataTransfer?.files || [])].filter((f) => f.type.startsWith('image/'))
    if (files.length) { e.preventDefault(); addFiles(files) }
  })
  on(['sel'], () => { clearComposer(); said = null; setNote('') })
  on(['card', 'hi', 'conn', 'picked', 'sessionDraft'], renderSays)
  on(['card'], renderAttachButton)
  ctx.clearComposer = clearComposer
  ctx.restoreComposer = restore
  // a line enter would otherwise have given to an answer that takes no
  // words goes as a line (decision.js answer)
  ctx.submitLine = () => submit({ asLine: true })
  renderSays()
  renderAttachButton()
}

export function clearComposer () {
  const input = $('#composer-input')
  input.value = ''
  set({ draft: '' })
  autosize()
  attachments = []
  renderChips()
  renderSays()
}

// renderAttachButton shows the paperclip only when the card's current
// agent can take images with a turn (Composer.Images) — a control the
// backend would refuse is worse than none, since it invites a line that
// comes straight back.
function renderAttachButton () {
  $('#composer-attach').hidden = !state.card?.composer?.images
}

// addFiles uploads each file (POST /api/attachments) and tracks it as a
// chip from the moment it is picked/pasted/dropped, not once the upload
// answers — so a slow upload still shows something landed.
async function addFiles (files) {
  for (const file of files) {
    const chip = { id: null, name: file.name || 'image', mediaType: file.type, size: file.size, pending: true, error: null }
    attachments.push(chip)
    renderChips()
    try {
      const ref = await uploadAttachment(file)
      Object.assign(chip, ref, { pending: false })
    } catch (err) {
      chip.pending = false
      chip.error = (err.data && err.data.error) || err.message || 'upload failed'
    }
    renderChips()
  }
}

function removeAttachment (chip) {
  attachments = attachments.filter((a) => a !== chip)
  renderChips()
}

function renderChips () {
  const box = $('#composer-chips')
  clear(box)
  box.hidden = attachments.length === 0
  for (const a of attachments) {
    box.append(h('span', { class: ['chip', a.error && 'err', a.pending && 'pending'] },
      a.pending ? 'Uploading…' : (a.error || a.name),
      h('button', { type: 'button', title: 'Remove', onclick: () => removeAttachment(a) }, '×')
    ))
  }
}

// restoreComposer puts text back in the composer to be edited and sent.
export function restoreComposer (text) { restore(text) }

function restore (text) {
  const input = $('#composer-input')
  input.value = text
  set({ draft: text })
  autosize()
  renderSays()
}

function autosize () {
  const i = $('#composer-input')
  i.style.setProperty('height', 'auto')
  i.style.setProperty('height', Math.min(i.scrollHeight, 160) + 'px')
}

// setNote shows a line under the composer about the last send (a line
// handed back, a refusal). It clears once the person types again.
function setNote (text, tone = 'warn') {
  noteText = text
  const el = $('#composer-note')
  if (!el) return
  clear(el)
  el.hidden = !text
  el.className = `cnote ${tone}`
  if (text) el.append(text)
}

// ask has the server classify the line, debounced.
function ask () {
  clearTimeout(timer)
  if (!classify || !state.sel) return
  const id = state.sel
  const text = state.draft
  if (!text.trim()) return
  timer = setTimeout(() => classifyNow(id, text).then(renderSays), 180)
}

async function classifyNow (id, text) {
  if (said && said.id === id && said.text === text) return said
  try {
    const c = await post(cardPath(id, 'composer'), { text })
    if (state.sel === id && state.draft === text) said = { id, text, ...c }
    return { id, text, ...c }
  } catch (err) {
    if (err.notBuilt) classify = false
    return null
  }
}

// current is what the server said about the line in the field, if it has.
function current () {
  if (!state.draft.trim()) return state.card?.composer || null
  return said && said.id === state.sel && said.text === state.draft ? said : null
}

// completing puts the command's "/name " in the field, ready for its
// arguments, and asks the server about the line it now is.
function complete (c) {
  if (!c) return
  const input = $('#composer-input')
  input.value = c.text
  set({ draft: c.text })
  offered = []
  autosize()
  input.focus()
  ask()
  renderSays()
}

// renderCompletions lists the commands a "/word" could become. While the
// server has not yet answered for the line in the field, the last list
// narrows to what the word still matches, so it does not flicker away
// between keystrokes.
function renderCompletions (c) {
  const box = $('#composer-complete')
  const draft = state.draft || ''
  if (c) offered = c.completions || []
  else if (!/^\/\S*$/.test(draft)) offered = []
  else offered = offered.filter((o) => o.text.toLowerCase().startsWith(draft.toLowerCase()))
  if (pick >= offered.length) pick = 0
  clear(box)
  box.hidden = offered.length === 0
  offered.forEach((o, i) => {
    box.append(h('button', {
      type: 'button',
      role: 'option',
      'aria-selected': String(i === pick),
      tabindex: '-1',
      onmousedown: (e) => e.preventDefault(),
      onclick: () => complete(o)
    }, h('b', null, o.text.trim()), o.detail ? h('span', null, o.detail) : null))
  })
}

function renderSays () {
  const says = $('#enter-says')
  const btn = $('#send')
  const box = $('#composer')
  const offline = state.conn !== 'live'
  const d = openDecision()
  const c = current()
  renderCompletions(state.draft.trim() ? c : { completions: [] })
  box.classList.remove('blocked')
  box.dataset.route = c?.route || ''
  btn.disabled = offline || sending || (!state.card && !state.sessionDraft)
  $('#composer-input').placeholder = placeholder()
  if (offline) {
    says.textContent = 'answers and messages wait until the board reconnects'
    return
  }
  if (state.sessionDraft) {
    btn.textContent = 'Send'
    says.textContent = state.draft.trim() ? 'starts the session with this message' : 'type what the session should do'
    return
  }
  // with a decision pinned, a line that answers goes with it — but a
  // slash line never answers (the server refuses a command as an answer),
  // so it keeps the command's own wording even before the server has
  // classified it
  if (d && (!state.draft.trim() || (!c && !state.draft.trim().startsWith('/')) || c.route === 'answer')) {
    says.textContent = enterSays(d)
    btn.textContent = 'Answer'
    return
  }
  btn.textContent = 'Send'
  let line = c?.says || ''
  if (!line) line = state.draft.trim() ? 'sends your message' : 'type a message or a command'
  says.textContent = line
  if (c?.route === 'blocked') { box.classList.add('blocked'); btn.disabled = true }
}

// placeholder names who a line goes to: a session's agent by name, since a
// session is one agent and the person picked it.
function placeholder () {
  if (state.sessionDraft) return 'What should it do? The first message starts the session'
  const s = state.card?.session
  if (s && state.card.stage === 'open') return `Message ${s.backend || 'the agent'}, or type a command`
  return 'Message the agent, or type a command'
}

async function submitDraft (text) {
  if (!text || sending) return
  if (state.conn !== 'live') { toast('Messages wait until the board reconnects'); return }
  sending = true
  renderSays()
  try {
    await startSession(text)
    setNote('')
  } catch (err) {
    setNote(sentence(err.data?.error || err.message), 'err')
  } finally {
    sending = false
    renderSays()
  }
}

async function submit ({ asLine = false } = {}) {
  if (state.sessionDraft) return submitDraft(state.draft.trim())
  const d = openDecision()
  const text = state.draft.trim()
  if (d && !text) { answer(); return }
  if (!text || sending || !state.sel) return
  if (state.conn !== 'live') { toast('Messages wait until the board reconnects'); return }
  if (attachments.some((a) => a.pending)) { setNote('Still uploading an image — wait a moment and send again.', 'info'); return }
  if (attachments.some((a) => a.error)) { setNote('Remove the failed attachment before sending.', 'err'); return }
  const id = state.sel
  if (d && !asLine) {
    // enter was pressed before the line was classified: ask now
    const c = current() || await classifyNow(id, state.draft)
    if (!c) {
      // never guess: a guess here would give the pinned decision's
      // highlighted answer — a stop, a landing — for a line meant as words
      setNote('Could not tell what enter would do with that line — it is still here; try again.', 'err')
      return
    }
    if (c.route === 'answer') { answer(); return }
  }
  sending = true
  renderSays()
  try {
    // sent against the stop the page shows: a line meant for it is not
    // routed at another the card has moved to since (409 "moved")
    const body = { text }
    if (d?.against?.token) body.against = d.against.token
    if (attachments.length) body.attachments = attachments.map((a) => a.id)
    const r = await post(cardPath(id, 'send'), body)
    if (r?.route === 'menu') {
      // the line names something in the card's menu: hand it over — the
      // menu opens on the entries the line names, the composer lets the
      // line go (the TUI's own "/" does the same), and enter again on the
      // focused entry runs it
      if (r.card && state.sel === id) set({ card: r.card })
      if (openActions(text)) clearComposer()
      return
    }
    clearComposer()
    setNote('')
    if (r?.card && state.sel === id) set({ card: r.card })
    const where = { steer: 'Steered the agent', consult: 'Asked a consult session', freeform: 'Sent to the session', goalnote: 'Noted on the goal', verb: 'Ran the command', answer: 'Answered', read: 'Sent — the board reads it to place it' }[r?.route]
    if (where) toast(where)
    ctxRef.refresh?.(id)
  } catch (err) {
    const e = err.data || {}
    if (err.status === 409 && e.error === 'busy') {
      restore(e.text || text)
      setNote('The agent is mid-turn — your line is back here. Send it again when this turn ends.')
    } else if (err.status === 409 && e.error === 'newcard') {
      setNote('That reads as separate work — it is in the new-card form.', 'info')
      openView('newcard', { text: e.text || text })
    } else if (err.status === 409 && e.error === 'moved') {
      setNote(`${id} moved since you read it — your line is still here. Read it again, then send it if it still holds.`)
      ctxRef.refresh?.(id)
    } else if (err.status === 409 && (e.error === 'needs' || e.error === 'confirm')) {
      setNote(sentence(e.text) || err.message, 'info')
    } else if (err.notBuilt && err.status !== 404) {
      toast('Sending from the web is not available yet')
    } else {
      setNote(sentence(err.message), 'err')
    }
  } finally {
    sending = false
    renderSays()
  }
}
