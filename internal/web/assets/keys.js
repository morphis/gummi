// keys.js — the board's keyboard: j/k walk the rail, n opens the next card
// that needs you, g then s/m/d/p/r picks a panel tab, [ and ] fold the rail
// and the panel, / writes, ? lists the keys, ⌘K/Ctrl-K jumps. Digits pick
// an answer and enter gives it. Letter keys never fire while typing, and
// nothing uses alt (alt+d is the address bar on Windows and Linux).

import { state } from './store.js?v=__ASSET_V__'
import { overlayOpen } from './views.js?v=__ASSET_V__'
import { openDecision, highlight, answer } from './decision.js?v=__ASSET_V__'

export function initKeys (a) {
  let gAt = 0
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'k') {
      e.preventDefault()
      if (!overlayOpen()) a.palette()
      return
    }
    if (overlayOpen() || e.metaKey || e.ctrlKey || e.altKey) return
    const t = e.target
    if (/^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.isContentEditable) return
    if (e.key === 'Enter' && /^(BUTTON|A|SUMMARY|LABEL)$/.test(t.tagName)) return
    if (t.getAttribute?.('role') === 'button' && (e.key === 'Enter' || e.key === ' ')) return
    if (gAt && Date.now() - gAt < 1200) {
      gAt = 0
      const tab = { s: 'spec', m: 'memory', d: 'diff', l: 'log', p: 'pr', r: 'stats' }[e.key]
      if (tab) { e.preventDefault(); a.setTab(tab) }
      return
    }
    const d = openDecision()
    switch (e.key) {
      case 'g': gAt = Date.now(); break
      case 'j': e.preventDefault(); a.step(1); break
      case 'k': e.preventDefault(); a.step(-1); break
      case 'n': e.preventDefault(); a.nextNeeding(); break
      case '[': e.preventDefault(); a.toggleRail(); break
      case ']': e.preventDefault(); a.togglePanel(); break
      case '/': e.preventDefault(); a.focusComposer(); break
      case '?': e.preventDefault(); a.keysHelp(); break
      case 'Enter': if (d && state.conn === 'live') { e.preventDefault(); answer() } break
      default:
        if (d && /^[1-9]$/.test(e.key) && +e.key <= d.options.length) { e.preventDefault(); highlight(+e.key - 1) }
    }
  })
}
