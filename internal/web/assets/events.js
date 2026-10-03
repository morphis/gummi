// events.js — the board's event stream (GET /api/events). Events are
// invalidations: "board", "card <id>", "live <id>" name a JSON route to
// refetch; "toast" and "viewers" carry their small values; and "resync"
// says the server cannot tell what this page missed, so it refetches
// everything it shows.
//
// While the browser retries the same EventSource it sends Last-Event-ID
// itself and the server replays what was missed. When the stream is dead
// for good (the server answered with an error, or restarted), a new
// EventSource starts without that id, so the page treats reopening one as
// a resync. The connection state drives the header pill and pauses answers.

import { set, state } from './store.js?v=__ASSET_V__'

// ingest runs report progress on their own kind; a view listens with onEvent
// and a device asking to join is "pairing" (approvals.js)
const KINDS = ['board', 'card', 'live', 'toast', 'viewers', 'ingest', 'pairing']
const listeners = new Map()

let es = null
let handlers = {}
let backoff = 1000
let timer = 0
let fresh = true // the current EventSource was created by us, not retried

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

export function connect (h) {
  handlers = h
  // the first stream is fresh too: anything that moved between the page's
  // first fetches and this connection is caught by the resync it implies
  open(true)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && state.conn !== 'live') reopen()
  })
  window.addEventListener('online', () => { if (state.conn !== 'live') reopen() })
}

function open (again) {
  fresh = again
  es = new EventSource('/api/events')
  es.onopen = () => {
    backoff = 1000
    const wasDown = state.conn !== 'live'
    set({ conn: 'live' })
    // a brand-new stream carries no Last-Event-ID: refetch what we show
    if (fresh) { fresh = false; dispatch('resync', {}) } else if (wasDown) dispatch('reconnected', {})
  }
  es.onerror = () => {
    set({ conn: 'reconnecting' })
    if (es.readyState === EventSource.CLOSED) schedule()
  }
  for (const kind of KINDS) {
    es.addEventListener(kind, (e) => {
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
