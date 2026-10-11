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
  tab: 'spec', // the tab that shows, or waits, beside the thread
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
  rightHidden: storage.get('rightHidden', true), // the surface beside the thread starts closed
  view: 'cards', // phone view: cards | thread | panel — the cards are the root screen
  mdecOpen: false,
  filter: '',
  kinds: [], // the rail's kind filter: card id prefixes, none meaning all
  statuses: [], // and its status filter: the rail's groups, none meaning all
  railGroup: storage.get('railGroup', 'status'), // status | repo
  railShow: storage.get('railShow', ['stack', 'spend']), // what a row's second line adds
  repo: 'all', // the rail's repo filter: 'all', or a repo's name ('' is the default)
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

// shownTab is the document on screen beside (on a phone, instead of) the
// thread, or null when none is: what the address names.
export function shownTab () {
  const narrow = matchMedia('(max-width:760px)').matches
  return (narrow ? state.view === 'panel' : !state.rightHidden) ? state.tab : null
}
