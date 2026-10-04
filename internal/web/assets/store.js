// store.js — the page's state in one object, and who wants to hear when a
// part of it changes. set() merges a patch and notifies the subscribers of
// each key it touched, once per key, after the whole patch is applied.
// There is no derived state here: the server's projections are stored as
// they arrive, and modules render from them.

import { storage } from './dom.js?v=__ASSET_V__'

export const state = {
  session: null, // webapi.Session
  board: null, // webapi.Board
  sel: null, // selected card id
  tab: 'spec', // right panel tab
  card: null, // webapi.Card of sel
  cardErr: null,
  thread: null, // { items: Item[], lastSeq, unavailable?, err? }
  live: null, // webapi.Live or null
  conn: 'connecting', // connecting | live | reconnecting
  viewers: [],
  hi: 0, // highlighted decision option
  picked: [], // option ids picked on a multi-pick question
  decNote: null, // { text, tone, testid }: what happened to the last answer
  decConfirm: null, // { question, yes, go }: a confirmation an answer's flow asked for
  showNext: null, // id of the next card needing you, after an answer
  railManual: storage.get('railManual', null), // null: follow the width
  rightHidden: storage.get('rightHidden', false),
  view: 'cards', // phone view: cards | thread | panel — the cards are the root screen
  mdecOpen: false,
  filter: '',
  kind: 'all',
  repo: 'all', // the rail's repo chip: 'all', or a repo's name ('' is the default)
  doneAll: false,
  draft: '' // composer text, so decision code can read it
}

const subs = new Map()

export function on (keys, fn) {
  for (const k of [].concat(keys)) {
    if (!subs.has(k)) subs.set(k, new Set())
    subs.get(k).add(fn)
  }
  return () => { for (const k of [].concat(keys)) subs.get(k)?.delete(fn) }
}

export function set (patch) {
  const fired = new Set()
  Object.assign(state, patch)
  for (const k of Object.keys(patch)) {
    for (const fn of subs.get(k) || []) {
      if (fired.has(fn)) continue
      fired.add(fn)
      try { fn(state) } catch (err) { console.error(err) }
    }
  }
}

// emit re-notifies a key's subscribers without changing it (a nested
// value changed in place).
export function emit (key) { set({ [key]: state[key] }) }

// rows is the board's rows, or none yet.
export function rows () { return state.board?.rows || [] }
export function row (id) { return rows().find(r => r.id === id) || null }
