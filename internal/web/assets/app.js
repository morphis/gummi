// app.js — the page's entry. It asks who this browser is (GET /api/session)
// and shows either the pairing form or the board; for the board it wires
// the modules together, loads the rows and the card the address names, and
// opens the event stream that keeps them current.

import { $, isMobile } from './dom.js?v=__ASSET_V__'
import { get, post, setUnauthorizedHandler, setReadHandlers } from './api.js?v=__ASSET_V__'
import { set, state, rows, on } from './store.js?v=__ASSET_V__'
import { connect, close as closeEvents } from './events.js?v=__ASSET_V__'
import { parse, onRoute } from './router.js?v=__ASSET_V__'
import { initTheme } from './theme.js?v=__ASSET_V__'
import { initViewport } from './viewport.js?v=__ASSET_V__'
import { initBack } from './back.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { setViewContext, openModal, closeOverlay } from './views.js?v=__ASSET_V__'
import { onEvent } from './events.js?v=__ASSET_V__'
import * as api from './api.js?v=__ASSET_V__'
import { initTop } from './top.js?v=__ASSET_V__'
import { initRail, toggleRail, visibleIds } from './rail.js?v=__ASSET_V__'
import { initHead } from './head.js?v=__ASSET_V__'
import { initThread, jumpToStage } from './thread.js?v=__ASSET_V__'
import { initDecision } from './decision.js?v=__ASSET_V__'
import { initComposer } from './composer.js?v=__ASSET_V__'
import { initSession, newSession } from './session.js?v=__ASSET_V__'
import { initPanel, setTab, togglePanel } from './panel.js?v=__ASSET_V__'
import { initMobile } from './mobile.js?v=__ASSET_V__'
import { initKeys } from './keys.js?v=__ASSET_V__'
import { palette, keysHelp } from './palette.js?v=__ASSET_V__'
import { showPair, showPending } from './pair.js?v=__ASSET_V__'
import { initApprovals } from './approvals.js?v=__ASSET_V__'
import { h } from './dom.js?v=__ASSET_V__'
import {
  initSelection, loadBoard, select, refresh, refreshAll, loadLive, nextNeeding, step
} from './selection.js?v=__ASSET_V__'
import { initResume } from './resume.js?v=__ASSET_V__'
import { registerWorker } from './push.js?v=__ASSET_V__'
import './views/index.js?v=__ASSET_V__'

let started = false

async function boot () {
  initTheme()
  initViewport()
  initBack()
  let session
  try {
    session = await get('/api/session')
  } catch (err) {
    $('#boot').textContent = `The board did not answer: ${err.message}`
    return
  }
  set({ session })
  $('#boot').hidden = true
  if (session.approval === 'pending') {
    // paired, but not let in yet: the wait, until a paired page answers
    $('#app').hidden = true
    showPending(session, () => { $('#pair').hidden = true; boot() })
    return
  }
  if (!session.authed && !session.openAccess) {
    $('#app').hidden = true
    showPair(session, () => { $('#pair').hidden = true; boot() })
    return
  }
  $('#pair').hidden = true
  $('#app').hidden = false
  startBoard()
}

function focusComposer () {
  if (isMobile()) set({ view: 'thread' })
  $('#composer-input').focus()
}

async function unpair () {
  openModal({
    title: 'Unpair this browser',
    testid: 'unpair-dialog',
    body: h('p', null, 'This browser forgets its device token. Pairing again needs a new code from the terminal running gummi web.'),
    actions: [
      { label: 'Cancel' },
      {
        label: 'Unpair',
        danger: true,
        primary: true,
        testid: 'unpair-confirm',
        onClick: async () => {
          try { await post('/api/unpair', {}) } catch (err) { toast(err.message, { err: true }); return false }
          closeEvents()
          location.hash = ''
          location.reload()
        }
      }
    ]
  })
}

async function startBoard () {
  if (started) return
  started = true
  const ctx = {
    select: (id, o) => select(id, o),
    setTab,
    togglePanel,
    jumpToStage,
    refresh,
    refreshBoard: () => loadBoard(),
    clearComposer: () => {}
  }
  setViewContext(() => ({
    api,
    select: ctx.select,
    toast,
    state,
    onStore: on,
    onEvent,
    openModal,
    refreshBoard: loadBoard
  }))
  initTop({ nextNeeding: () => nextNeeding(toast), palette: () => palette(ctx.select), keysHelp, toggleRail })
  initRail({ select: ctx.select, unpair, newSession })
  initHead(ctx)
  initThread()
  initDecision(ctx)
  initComposer(ctx)
  initSession(ctx)
  initPanel(ctx)
  initMobile()
  initSelection()
  initResume({ loadBoard, select: ctx.select })
  initApprovals()
  registerWorker()
  initKeys({
    palette: () => palette(ctx.select),
    keysHelp,
    step: (d) => step(d, visibleIds()),
    nextNeeding: () => nextNeeding(toast),
    setTab,
    toggleRail,
    togglePanel,
    focusComposer
  })
  setUnauthorizedHandler(() => { closeEvents(); closeOverlay(); location.reload() })
  // a read that failed is asked again, everything the page shows, until
  // one goes through: the event that asked for it will not come twice
  let again = 0
  let wait = 1000
  let missedAt = 0
  setReadHandlers({
    missed: () => {
      missedAt = Date.now()
      if (again) return
      again = setTimeout(() => { again = 0; refreshAll() }, wait)
      wait = Math.min(wait * 2, 15000)
    },
    // the backoff starts over once reads have gone through for a while
    ok: () => { if (!again && Date.now() - missedAt > 20000) wait = 1000 }
  })

  let routed = false
  onRoute(({ id, tab }) => {
    routed = true
    if (id && (id !== state.sel || (tab && tab !== state.tab))) select(id, { tab })
  }, () => ({ id: state.sel, tab: state.tab }))
  await loadBoard()
  if (routed && state.sel) return connectEvents()
  const route = parse()
  if (route.tab) set({ tab: route.tab })
  const first = (route.id && rows().some(r => r.id === route.id) && route.id) ||
    rows().find(r => r.status === 'needs')?.id || rows()[0]?.id
  if (route.id && first !== route.id) toast(`${route.id} is not on this board`)
  const tab = route.id === first ? route.tab : null
  // a phone opens on the cards; only a route that names the card actually
  // shown (a deep link) moves to its screen — a plain open, and a deep
  // link naming an id that is not on this board, stay there
  const view = !!(route.id && first === route.id)
  if (first) await select(first, { tab, view })

  connectEvents()
}

function connectEvents () {
  connect({
    board: () => loadBoard(),
    // a card that is gone has nothing to refetch: the board change that
    // comes with it moves the page off it (initSelection)
    card: (c) => { if (c.id === state.sel && !c.gone) refresh(c.id) },
    live: (c) => { if (c.id === state.sel) loadLive(c.id) },
    toast: (c) => toast(c.text, { err: c.err }),
    viewers: (c) => set({ viewers: c.viewers || [] }),
    resync: () => refreshAll()
  })
}

boot()
