// terminal.js — the Terminal tab: a shell in the card's worktree, on the
// machine hosting the board. It is offered only when the server was
// started with --terminal (the card's head says so).
//
// The shell is the card's, not this page's: it keeps running when the tab
// is left or the phone sleeps, and coming back replays the tail of what it
// printed. So the page holds one terminal at a time, for the open card,
// and drops it (the socket, not the shell) when the tab or the card
// changes.
//
// xterm.js is vendored (assets/vendor, scripts/vendor-xterm.sh) and loaded
// the first time the tab opens. The page's CSP allows no inline style, so
// the vendored copy writes its generated rules into an <xterm-style>
// element instead of a <style>: defined here, it keeps them in a
// constructed stylesheet, which the policy does not restrict.
//
// A phone's keyboard has no esc, tab, ctrl or arrows, so on a touch screen
// a bar of them sits under the shell (keyBar). ctrl and alt are sticky:
// pressed, they hold for the next key, from the bar or the keyboard.

import { h, isMobile } from './dom.js?v=__ASSET_V__'
import { on, state } from './store.js?v=__ASSET_V__'
import { get, cardPath } from './api.js?v=__ASSET_V__'
import { isDark } from './theme.js?v=__ASSET_V__'

export const terminalTab = {
  name: 'terminal',
  label: 'Terminal',
  key: 'g t',
  hidden: (card) => !card?.terminal,
  // nothing to read: the shell is a socket the tab opens when it is drawn
  fetch: () => Promise.resolve({}),
  empty: () => false,
  render
}

class XtermStyle extends HTMLElement {
  #sheet = new CSSStyleSheet()
  get textContent () { return '' }
  set textContent (css) { this.#sheet.replaceSync(css) }
  connectedCallback () { document.adoptedStyleSheets = [...document.adoptedStyleSheets, this.#sheet] }
  disconnectedCallback () { document.adoptedStyleSheets = document.adoptedStyleSheets.filter(s => s !== this.#sheet) }
}
customElements.define('xterm-style', XtermStyle)

let libs = null
function loadLibs () {
  if (!libs) {
    document.head.append(h('link', { rel: 'stylesheet', href: '/assets/vendor/xterm.css?v=__ASSET_V__' }))
    libs = Promise.all([
      import('./vendor/xterm.js?v=__ASSET_V__'),
      import('./vendor/xterm-addon-fit.js?v=__ASSET_V__'),
      document.fonts?.load?.('13px "Geist Mono"').catch(() => {})
    ]).catch((err) => { libs = null; throw err })
  }
  return libs
}

// the sixteen colours in each theme; the ground and the type are the page's
const PALETTE = {
  dark: {
    black: '#2A2834', red: '#FF6B7F', green: '#79E06A', yellow: '#F2D24B', blue: '#7FB2FF', magenta: '#C7A0FF', cyan: '#6FD6D0', white: '#B9B5C9',
    brightBlack: '#6E6B7C', brightRed: '#FF8FA0', brightGreen: '#9CEB90', brightYellow: '#F7E07F', brightBlue: '#A6CAFF', brightMagenta: '#D9BEFF', brightCyan: '#97E6E1', brightWhite: '#ECEAF4'
  },
  light: {
    black: '#1D1B26', red: '#B02C3F', green: '#1A6B36', yellow: '#805500', blue: '#2D63C7', magenta: '#8A3FCB', cyan: '#0F6F78', white: '#625E70',
    brightBlack: '#4A4759', brightRed: '#C8384E', brightGreen: '#23924A', brightYellow: '#A87F00', brightBlue: '#1C7FC4', brightMagenta: '#B0306A', brightCyan: '#1890A0', brightWhite: '#1D1B26'
  }
}

function theme () {
  const css = getComputedStyle(document.documentElement)
  const v = (name) => css.getPropertyValue(name).trim()
  return {
    ...PALETTE[isDark() ? 'dark' : 'light'],
    background: v('--bg'),
    foreground: v('--fg'),
    cursor: v('--accent'),
    cursorAccent: v('--bg'),
    selectionBackground: v('--band')
  }
}

// cur is the one terminal the page holds: { id, root, screen, note, term,
// fit, ws, gone, focus, exited, opened, tries, timer }
let cur = null
let rerender = null

function drop () {
  const t = cur
  cur = null
  if (!t) return
  t.gone = true
  clearTimeout(t.timer)
  clearTimeout(t.unsure)
  t.sizes?.disconnect()
  t.ws?.close()
  t.term?.dispose()
  t.root.remove()
}

on(['sel', 'tab'], () => { if (cur && (state.tab !== 'terminal' || state.sel !== cur.id)) drop() })
// drawn before the card's head arrived: draw again now that it says
on(['card'], () => { if (state.tab === 'terminal' && !cur && state.card) rerender?.() })
// the page's theme is the terminal's
new MutationObserver(() => { if (cur?.term) cur.term.options.theme = theme() })
  .observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
matchMedia('(prefers-color-scheme: dark)').addEventListener?.('change', () => { if (cur?.term) cur.term.options.theme = theme() })
// the pane is redrawn whenever its card changes, which takes the terminal
// out of the page and puts it back: the keyboard stays with it unless the
// person put it somewhere else in between
document.addEventListener('focusin', (e) => { if (cur) cur.focus = cur.root.contains(e.target) })

function render (pane, entry, ctx) {
  rerender = ctx.rerender
  if (!ctx.card?.terminal) {
    drop()
    if (ctx.card) pane.append(h('div', { class: 'empty', testid: 'terminal-off' }, h('b', null, 'No terminal here'), 'This card has no worktree on the machine hosting the board.'))
    else pane.append(h('div', { class: 'empty' }, h('span', { class: 'spinner' })))
    return
  }
  if (!cur || cur.id !== ctx.id) {
    drop()
    cur = start(ctx.id)
  }
  pane.append(cur.root)
  if (cur.focus) cur.term?.focus()
}

function start (id) {
  const screen = h('div', { class: 'term-screen', testid: 'terminal-screen' })
  const note = h('div', { class: 'term-note', testid: 'terminal-note', role: 'status', hidden: true })
  const t = { id, screen, note, tries: 0, ctrl: false, alt: false }
  // the way out of a shell that no longer answers its keys; asked twice,
  // since whatever it was running goes with it
  const end = h('button', {
    class: 'term-end',
    type: 'button',
    testid: 'terminal-end',
    title: 'End this shell and whatever is running in it',
    onclick: () => {
      if (t.ws?.readyState !== WebSocket.OPEN) return
      if (!end.dataset.sure) {
        end.dataset.sure = '1'
        end.textContent = 'End it?'
        t.unsure = setTimeout(() => { delete end.dataset.sure; end.textContent = 'End shell' }, 4000)
        return
      }
      clearTimeout(t.unsure)
      delete end.dataset.sure
      end.textContent = 'End shell'
      t.ended = true
      t.ws.send(JSON.stringify({ end: true }))
    }
  }, 'End shell')
  end.addEventListener('pointerdown', (e) => e.preventDefault())
  end.addEventListener('mousedown', (e) => e.preventDefault())
  t.root = h('div', { class: 'term', testid: 'terminal' }, h('div', { class: 'term-main' }, screen, end, note), touch() ? keyBar(t) : null)
  say(t, h('span', { class: 'spinner' }), 'Opening the terminal…')
  loadLibs().then(([{ Terminal }, { FitAddon }]) => {
    if (t.gone) return
    t.term = new Terminal({
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--mono').trim() || 'monospace',
      fontSize: 13,
      cursorBlink: true,
      scrollback: 5000,
      theme: theme()
    })
    t.fit = new FitAddon()
    t.term.loadAddon(t.fit)
    t.term.open(t.screen)
    const enc = new TextEncoder()
    t.term.onData((d) => send(t, enc.encode(held(t, d))))
    t.term.onBinary((d) => send(t, Uint8Array.from(d, c => c.charCodeAt(0))))
    t.term.onResize(({ cols, rows }) => { if (t.ws?.readyState === WebSocket.OPEN) t.ws.send(JSON.stringify({ cols, rows })) })
    // the panel is resized by its edge, the window, and a phone's keyboard
    t.sizes = new ResizeObserver(() => { if (t.root.isConnected && t.screen.clientWidth) try { t.fit.fit() } catch {} })
    t.sizes.observe(t.screen)
    try { t.fit.fit() } catch {}
    connect(t)
    if (t.root.isConnected) { t.term.focus(); t.focus = true }
  }).catch((err) => {
    if (t.gone) return
    console.error(err)
    say(t, h('b', null, 'The terminal could not be loaded'), String(err.message || err))
  })
  return t
}

// touch says the screen is one a finger types on: it gets the key bar
const touch = () => isMobile() || matchMedia('(pointer: coarse)').matches

// KEYS is the bar, in order. seq is what a key sends: a string, or for the
// keys a modifier changes the code of, [final byte, prefix without one].
const KEYS = [
  { id: 'esc', label: 'esc', seq: '\x1b' },
  { id: 'tab', label: 'tab', seq: '\t' },
  { id: 'ctrl', label: 'ctrl', mod: 'ctrl' },
  { id: 'alt', label: 'alt', mod: 'alt' },
  { id: 'left', label: '←', name: 'Left', csi: 'D' },
  { id: 'down', label: '↓', name: 'Down', csi: 'B' },
  { id: 'up', label: '↑', name: 'Up', csi: 'A' },
  { id: 'right', label: '→', name: 'Right', csi: 'C' },
  { id: 'home', label: 'home', csi: 'H' },
  { id: 'end', label: 'end', csi: 'F' },
  { id: 'pgup', label: 'pgup', tilde: 5 },
  { id: 'pgdn', label: 'pgdn', tilde: 6 },
  { id: 'dash', label: '-', seq: '-' },
  { id: 'slash', label: '/', seq: '/' },
  { id: 'pipe', label: '|', seq: '|' },
  { id: 'tilde', label: '~', seq: '~' }
]

function keyBar (t) {
  const bar = h('div', { class: 'term-keys', testid: 'terminal-keys', role: 'toolbar', 'aria-label': 'Terminal keys' })
  for (const k of KEYS) {
    const b = h('button', {
      class: 'term-key',
      type: 'button',
      tabIndex: -1,
      testid: `terminal-key-${k.id}`,
      'aria-label': k.name || k.label,
      onclick: () => {
        if (k.mod) { t[k.mod] = !t[k.mod]; paintMods(t) } else send(t, new TextEncoder().encode(keySeq(t, k)))
        // the keyboard stays up if it was: the bar is part of typing
        if (t.focus) t.term?.focus()
      }
    }, k.label)
    if (k.mod) { b.dataset.mod = k.mod; b.setAttribute('aria-pressed', 'false') }
    // a press must not take the focus (and with it the keyboard) from the shell
    b.addEventListener('pointerdown', (e) => e.preventDefault())
    b.addEventListener('mousedown', (e) => e.preventDefault())
    bar.append(b)
  }
  t.keys = bar
  return bar
}

function paintMods (t) {
  for (const b of t.keys?.querySelectorAll('[data-mod]') || []) b.setAttribute('aria-pressed', String(!!t[b.dataset.mod]))
}

// mods takes the held modifiers, releasing them: xterm's number for them
// (1 + alt 2 + ctrl 4), 1 when none is held.
function mods (t) {
  const m = 1 + (t.alt ? 2 : 0) + (t.ctrl ? 4 : 0)
  if (m > 1) { t.ctrl = t.alt = false; paintMods(t) }
  return m
}

// keySeq is what a bar key sends, under the held modifiers and, for the
// arrows, the mode a full-screen program put the cursor keys in.
function keySeq (t, k) {
  if (k.seq) return held(t, k.seq)
  const m = mods(t)
  if (k.tilde) return m > 1 ? `\x1b[${k.tilde};${m}~` : `\x1b[${k.tilde}~`
  if (m > 1) return `\x1b[1;${m}${k.csi}`
  return (t.term?.modes.applicationCursorKeysMode ? '\x1bO' : '\x1b[') + k.csi
}

// held applies the bar's ctrl and alt to one typed key, releasing them:
// ctrl takes a letter to its control code, alt sends esc before it. More
// than one character is a paste or a key's own sequence, and passes as is.
function held (t, d) {
  if (!(t.ctrl || t.alt) || [...d].length !== 1) return d
  const m = mods(t)
  if (m & 4) {
    const c = d.toUpperCase().charCodeAt(0)
    if (c >= 0x40 && c <= 0x5f) d = String.fromCharCode(c - 0x40)
    else if (d === ' ') d = '\x00'
    else if (d === '?') d = '\x7f'
  }
  return m & 2 ? '\x1b' + d : d
}

function send (t, bytes) {
  if (t.ws?.readyState === WebSocket.OPEN) t.ws.send(bytes)
}

function connect (t) {
  if (t.gone) return
  const u = new URL(`/api/cards/${encodeURIComponent(t.id)}/term`, location.href)
  u.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  u.searchParams.set('cols', t.term.cols)
  u.searchParams.set('rows', t.term.rows)
  const ws = t.ws = new WebSocket(u)
  ws.binaryType = 'arraybuffer'
  t.exited = null
  t.ended = false
  t.opened = false
  ws.onopen = () => {
    t.opened = true
    t.tries = 0
    // what follows is the shell's output from its scrollback on
    t.term.reset()
    t.note.hidden = true
    t.root.dataset.state = 'open'
    ws.send(JSON.stringify({ cols: t.term.cols, rows: t.term.rows }))
  }
  ws.onmessage = (e) => {
    if (typeof e.data !== 'string') { t.term.write(new Uint8Array(e.data)); return }
    try { t.exited = JSON.parse(e.data).exit ?? 0 } catch {}
  }
  ws.onclose = () => {
    if (t.gone || t.ws !== ws) return
    t.root.dataset.state = 'closed'
    const again = (label) => h('button', { class: 'btn', type: 'button', testid: 'terminal-again', onclick: () => { t.tries = 0; say(t, h('span', { class: 'spinner' }), 'Opening the terminal…'); connect(t); t.focus = true; t.term.focus() } }, label)
    if (t.exited !== null) {
      say(t, h('b', null, t.ended ? 'The shell was ended' : t.exited ? `The shell exited (${t.exited})` : 'The shell exited'), again('New shell'))
    } else if (!t.opened) {
      // a refused handshake says nothing a page may read, so the server
      // is asked in words: its refusal is final, its silence is not
      get(cardPath(t.id, 'term')).then(() => null, (err) => err).then((err) => {
        if (t.gone || t.ws !== ws) return
        if (err?.status && err.status < 500) say(t, h('b', null, 'The terminal did not open'), err.message, again('Try again'))
        else retry()
      })
    } else retry()
    function retry () {
      if (t.tries >= 6) {
        say(t, h('b', null, 'The terminal did not open'), 'The board is out of reach.', again('Try again'))
        return
      }
      say(t, h('span', { class: 'spinner' }), 'Reconnecting…')
      t.timer = setTimeout(() => connect(t), Math.min(500 * 2 ** t.tries++, 8000))
    }
  }
}

function say (t, ...parts) {
  t.note.replaceChildren(...parts.map(p => typeof p === 'string' ? h('span', null, p) : p))
  t.note.hidden = false
}
