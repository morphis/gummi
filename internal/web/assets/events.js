// events.js — the board's event stream (GET /api/events). Events are
// invalidations: "board", "card <id>", "live <id>" name a JSON route to
// refetch; "toast" and "viewers" carry their small values; and "resync"
// says the server cannot tell what this page missed, so it refetches
// everything it shows.
//
// While the browser retries the same EventSource it sends Last-Event-ID
// itself and the server replays what was missed. A stream the page opens
// itself names the id to resume from (?since=): the board read's own
// (webapi.Board.EventID) for the first, the last event seen for one that
// replaces a dead stream. The server replays from there, or sends a
// "resync" when it cannot (the id is too old, or from a server since
// restarted). Only a stream with no id to name is treated as a resync
// from the start. The connection state drives the header pill and pauses
// answers.

import { set, state } from './store.js?v=__ASSET_V__'

// ingest runs report progress on their own kind; a view listens with onEvent
// and a device asking to join is "pairing" (approvals.js); "work" is what
// the board is waiting on GitHub for (work.js)
const KINDS = ['board', 'card', 'live', 'toast', 'viewers', 'ingest', 'pairing', 'work']
const listeners = new Map()

let es = null
let handlers = {}
let backoff = 1000
let timer = 0
let fresh = true // the current EventSource was created by us, not retried
let since = '' // the event id to resume from: what the page has drawn is as of it

// onEvent lets a view (a goal page, ingest) hear one kind of event while
// it is mounted. It returns the unsubscribe function.
export function onEvent (kind, fn) {
  if (!listeners.has(kind)) listeners.set(kind, new Set())
  listeners.get(kind).add(fn)
  return () => listeners.get(kind)?.delete(fn)
}

function dispatch (kind, data) {
  try { handlers[kind]?.(data) } catch (err) { console.error(err) }
  for (const fn of listeners.get(kind) || []) {
    try { fn(data) } catch (err) { console.error(err) }
  }
}

// readAt notes the event id a board read began at, before the stream is
// open: the stream resumes from the earliest, so a change made while any
// first read was under way is still told.
export function readAt (id) {
  if (es || !id) return
  if (!since || Number(id) < Number(since)) since = id
}

// connect opens the stream. resync: what the page drew may predate every
// id readAt was given (a card read before the board's), so the first
// stream refetches everything rather than resuming.
export function connect (h, { resync = false } = {}) {
  handlers = h
  if (resync) since = ''
  // with no id to resume from, the first stream is fresh too: anything
  // that moved between the page's first fetches and this connection is
  // caught by the resync it implies
  open(true)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && state.conn !== 'live') reopen()
  })
  window.addEventListener('online', () => { if (state.conn !== 'live') reopen() })
}

function open (again) {
  fresh = again
  es = new EventSource(since ? `/api/events?since=${encodeURIComponent(since)}` : '/api/events')
  es.onopen = () => {
    backoff = 1000
    const wasDown = state.conn !== 'live'
    set({ conn: 'live' })
    // a brand-new stream with nothing to resume from: refetch what we
    // show (one that names an id is sent a "resync" if it has to)
    const first = fresh
    fresh = false
    if (first && !since) dispatch('resync', {})
    else if (wasDown) dispatch('reconnected', {})
  }
  es.onerror = () => {
    set({ conn: 'reconnecting' })
    if (es.readyState === EventSource.CLOSED) schedule()
  }
  for (const kind of KINDS) {
    es.addEventListener(kind, (e) => {
      if (e.lastEventId) since = e.lastEventId
      let data = {}
      try { data = JSON.parse(e.data || '{}') } catch { /* keep {} */ }
      dispatch(kind, data)
    })
  }
  es.addEventListener('resync', () => dispatch('resync', {}))
}

function schedule () {
  clearTimeout(timer)
  timer = setTimeout(reopen, backoff)
  backoff = Math.min(backoff * 2, 15000)
}

function reopen () {
  clearTimeout(timer)
  try { es?.close() } catch { /* already closed */ }
  open(true)
}

export function close () {
  clearTimeout(timer)
  try { es?.close() } catch { /* already closed */ }
  es = null
}
