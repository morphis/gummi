// api.js — the page's one door to the server: same-origin JSON fetches that
// throw an ApiError carrying the status and the server's error body
// (internal/webapi.Error), so callers can tell a 409 race from a route that
// is not built yet.
//
// A question the server stopped at — an input a flow needs, a confirmation
// to give, a line that reads as a new card — comes back 202 Accepted with
// that same body (webapi.StatusQuestion): it is ordinary control flow, not a
// failed load, so the browser logs nothing. Here it becomes the very ApiError
// a 409 "needs" / "confirm" / "newcard" used to, status 409 and all, so
// every caller asks it exactly as it always has.

// QUESTIONS are the error words a 202 carries a question with
// (webapi.IsQuestion).
const QUESTIONS = new Set(['needs', 'confirm', 'newcard'])

export class ApiError extends Error {
  constructor (status, data, fallback) {
    super((data && data.error) || fallback || `HTTP ${status}`)
    this.status = status
    this.data = data || {}
  }

  // notBuilt is a route this server does not answer yet (501), or at all:
  // a 404 only when nothing answered it — the server's own "no such card"
  // is a 404 too, but it carries its sentence (webapi.Error), and a card
  // that is not on the board is not a route that is missing.
  get notBuilt () {
    return this.status === 501 || this.status === 405 || (this.status === 404 && !this.data.error)
  }

  // notFound is the server saying what was asked for is not there.
  get notFound () { return this.status === 404 && !!this.data.error }
}

let onUnauthorized = null
export function setUnauthorizedHandler (fn) { onUnauthorized = fn }

// A read that failed on the way (the board unreachable, a 5xx) was
// usually asked for by an event saying something changed; nothing will
// say so again, so the page would keep drawing what it had. onMissed is
// told of each such failure and onRead of each read that went through,
// so the page can fetch again until one does.
let reads = { missed: null, ok: null }
export function setReadHandlers ({ missed, ok }) { reads = { missed, ok } }

export async function api (method, path, body) {
  const init = { method, credentials: 'same-origin', headers: { Accept: 'application/json' } }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const read = method === 'GET'
  let res
  try {
    res = await fetch(path, init)
  } catch (err) {
    if (read) reads.missed?.()
    throw new ApiError(0, null, 'the board cannot be reached')
  }
  if (read) {
    if (res.status >= 500 && res.status !== 501) reads.missed?.()
    else reads.ok?.()
  }
  const text = await res.text()
  let data = null
  if (text) {
    try { data = JSON.parse(text) } catch { data = null }
  }
  if (!res.ok) {
    if (res.status === 401 && onUnauthorized) onUnauthorized()
    throw new ApiError(res.status, data, res.statusText)
  }
  if (res.status === 202 && data && QUESTIONS.has(data.error)) {
    // a question, not a result: the page answers it where it asks
    const q = new ApiError(409, data)
    q.question = true
    throw q
  }
  return data
}

export const get = (path) => api('GET', path)
export const post = (path, body = {}) => api('POST', path, body)
export const del = (path) => api('DELETE', path)
export const put = (path, body = {}) => api('PUT', path, body)
export const patch = (path, body = {}) => api('PATCH', path, body)

// cardPath builds /api/cards/<id>/<rest> with the id escaped.
export function cardPath (id, rest = '') {
  return `/api/cards/${encodeURIComponent(id)}${rest ? '/' + rest : ''}`
}

// uploadAttachment POSTs a File/Blob's raw bytes to /api/attachments,
// carrying its name in X-Filename (display only — the server sniffs the
// media type from the bytes, never trusting a header for it). Answers
// the stored ref ({id, name, mediaType, size}); throws ApiError on the
// server's 413/415 refusals.
export async function uploadAttachment (file) {
  const res = await fetch('/api/attachments', {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': file.type || 'application/octet-stream',
      // percent-encoded: header values must stay ASCII, and a pasted
      // screenshot's name is whatever the OS or clipboard gave it.
      'X-Filename': encodeURIComponent(file.name || 'image')
    },
    body: file
  })
  const text = await res.text()
  let data = null
  if (text) {
    try { data = JSON.parse(text) } catch { data = null }
  }
  if (!res.ok) {
    if (res.status === 401 && onUnauthorized) onUnauthorized()
    throw new ApiError(res.status, data, res.statusText)
  }
  return data
}

// attachmentURL is where GET /api/attachments/{id} serves id's bytes —
// what an <img> thumbnail's src points at.
export function attachmentURL (id) {
  return `/api/attachments/${encodeURIComponent(id)}`
}
