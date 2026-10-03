// views/agent.js — the board agent (DESIGN §16): the conversation the TUI
// hosts in its agent tab, bound to the board rather than a card, acting on
// it through the board-level tools. The page reads the same session the
// tab draws (GET /api/agent) and refetches it on every `agent` event; it
// sends through the tab's composer (POST /api/agent/send, where `/clear`
// starts over), stops a turn (interrupt), and switches profile or model
// (POST /api/agent/profile) — asking first, as the TUI does, when a
// conversation would be lost: the server answers a "confirm" question with
// the question, and the page repeats the request with confirm once you agree.

import { h, append, clear, cr, storage, $, ctxMeter } from '../dom.js?v=__ASSET_V__'
import { markdown } from '../markdown.js?v=__ASSET_V__'
import { registerView } from '../views.js?v=__ASSET_V__'
import { onEvent } from '../events.js?v=__ASSET_V__'
import { get } from '../api.js?v=__ASSET_V__'
import { itemEl } from '../thread.js?v=__ASSET_V__'
import { field, choose, errorBox, confirmStrip, refetcher } from './kit.js?v=__ASSET_V__'

// the rail's "Board agent" button says whether it is busy
function railSub (a) {
  const sub = $('#agent-sub')
  if (!sub) return
  sub.textContent = !a ? '' : a.live?.busy ? 'working' : a.opening ? 'opening' : a.open ? 'idle' : ''
}
let railFetch = null
onEvent('agent', () => {
  if (mounted) return // the open view refetches and updates it
  clearTimeout(railFetch)
  railFetch = setTimeout(() => get('/api/agent').then(railSub, () => {}), 250)
})
let mounted = false

registerView('agent', {
  title: 'Board agent',
  css: 'views/agent.css',
  mount (body, ctx) {
    mounted = true
    const v = { a: null, err: null, confirm: null, pending: null, alive: true }
    body.classList.add('vagent')
    body.parentElement?.classList.add('agent-modal')

    const head = h('div', { class: 'ahead', testid: 'agent-head' })
    const scroller = h('div', { class: 'ascroll', testid: 'agent-transcript' })
    const items = h('div', { class: 'aitems', 'aria-live': 'polite' })
    const live = h('div', { class: 'alive', testid: 'agent-live' })
    const inner = h('div', { class: 'ainner' }, items, live)
    scroller.append(inner)
    const notice = h('div', { class: 'anotice' })
    const input = h('textarea', {
      testid: 'agent-input',
      rows: 1,
      placeholder: 'Ask the board agent — it can read and act on every card',
      'aria-label': 'Message the board agent',
      value: storage.get('agent-draft', '')
    })
    const sendBtn = h('button', { type: 'button', class: 'btn pri', testid: 'agent-send', onclick: () => send() }, 'Send')
    const stopBtn = h('button', { type: 'button', class: 'btn', testid: 'agent-interrupt', hidden: true, onclick: () => interrupt() }, 'Stop')
    const composer = h('div', { class: 'acomposer', testid: 'agent-composer' }, input,
      h('div', { class: 'arow' },
        h('span', { class: 'hint' }, h('kbd', null, 'enter'), ' sends · ', h('kbd', null, 'shift enter'), ' new line · ', h('span', { class: 'mono' }, '/clear'), ' starts over'),
        stopBtn, sendBtn))
    body.append(head, scroller, notice, composer)

    const grow = () => { input.style.setProperty('height', 'auto'); input.style.setProperty('height', Math.min(input.scrollHeight, 200) + 'px') }
    input.addEventListener('input', () => { storage.set('agent-draft', input.value); grow() })
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send() }
      if (e.key === 'Escape' && v.a?.live?.busy) { e.preventDefault(); e.stopPropagation(); interrupt() }
    })

    const load = refetcher(async () => {
      try {
        v.a = await ctx.api.get('/api/agent')
        v.err = null
      } catch (err) { v.err = err }
      railSub(v.a)
      if (v.alive) draw()
    })

    const nodes = new Map()
    function draw () {
      const a = v.a
      drawHead(a)
      clear(notice)
      if (v.err) notice.append(errorBox(v.err, 'agent-error'))
      if (a?.err) notice.append(h('div', { class: 'verr', testid: 'agent-open-error' }, a.err))
      if (v.confirm) notice.append(confirmStrip({ ...v.confirm, testid: 'agent-confirm' }))
      if (!a) return
      if (!a.open) {
        drawOpener(a)
        composer.hidden = true
        return
      }
      composer.hidden = false
      const stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80
      const want = []
      for (const it of a.items || []) {
        // keys restart with a new conversation: the time and text tell them apart
        const sig = `${it.seq}|${it.time}|${(it.text || '').length}|${(it.tools || []).concat((it.items || []).flatMap(c => c.tools || [])).map(t => t.status).join('')}`
        let n = nodes.get(it.key)
        if (!n || n.sig !== sig) {
          const view = it.t === 'message' ? { ...it, author: null, role: 'agent' } : it
          const el = itemEl(view)
          el.dataset.key = it.key
          el.dataset.type = it.t
          el.dataset.testid = 'agent-item'
          n = { sig, el }
          nodes.set(it.key, n)
        }
        want.push(n.el)
      }
      if (!want.length && !a.live?.busy && !v.pending) {
        want.push(h('div', { class: 'empty', testid: 'agent-empty' }, h('b', null, 'Ask about the board'),
          'The agent sees every card and can act through the board’s tools: “what needs me first?”, “start FD-012 on autopilot”, “summarise what landed today”.'))
      }
      items.replaceChildren(...want)
      drawLive(a)
      stopBtn.hidden = !a.live?.busy
      if (stick) requestAnimationFrame(() => { scroller.scrollTop = scroller.scrollHeight })
    }

    function drawLive (a) {
      const l = a.live
      const parts = []
      if (v.pending) parts.push(itemEl({ t: 'you', text: v.pending, via: 'sending' }))
      if (l?.streaming) {
        parts.push(h('div', { class: 'msg live-msg', testid: 'agent-streaming' },
          h('div', { class: 'av agent', 'aria-hidden': 'true' }, 'AG'),
          h('div', null, h('div', { class: 'who' }, h('b', null, 'agent'), h('span', { class: 'mono' }, 'writing')), h('div', { class: 'body' }, markdown(l.streaming)))))
      }
      if (l?.busy) {
        const tool = l.tool ? String(l.tool.label || l.tool.tool).replace(/\s+/g, ' ') : null
        parts.push(h('div', { class: 'live', testid: 'agent-busy' },
          h('span', { class: 'spinner' }),
          h('span', { class: 'shimmer' }, ['agent is ' + (l.verb || (tool ? 'working' : 'thinking')), tool].filter(Boolean).join(' · ')),
          l.spent ? h('span', { class: 'spent' }, `${cr(l.spent)} cr`) : null))
      }
      live.replaceChildren(...parts)
    }

    function drawHead (a) {
      clear(head)
      if (!a) {
        head.append(h('span', { class: 'vbusy' }, h('span', { class: 'spinner' }), 'reaching the board…'))
        return
      }
      if (!a.open) {
        head.append(h('span', { class: 'astate' }, a.opening ? 'opening…' : 'not open'))
        return
      }
      const profiles = (a.profiles || []).map(p => p.name)
      const pick = choose(profiles.length ? profiles : [a.profile || 'default'], {
        value: a.profile,
        testid: 'agent-profile',
        label: 'Profile',
        onchange: (e) => switchTo({ profile: e.target.value }, () => { e.target.value = a.profile })
      })
      const model = h('input', { testid: 'agent-model', list: 'agent-models', value: a.model || '', placeholder: 'model', 'aria-label': 'Model', spellcheck: 'false' })
      const models = h('datalist', { id: 'agent-models' }, (a.models || []).map(m => h('option', { value: m.model }, (m.uses || []).join(', '))))
      model.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); useModel() } })
      const useModel = () => {
        const m = model.value.trim()
        if (!m || m === a.model) return
        switchTo({ model: m }, () => { model.value = a.model || '' })
      }
      const ctxm = ctxMeter(a.context, 'agent-context')
      append(head, [
        h('label', { class: 'hf' }, h('span', null, 'profile'), pick),
        h('span', { class: 'hf model' }, h('span', null, 'model'), model, models,
          h('button', { type: 'button', class: 'btn', testid: 'agent-model-use', onclick: useModel }, 'Use')),
        h('span', { class: 'grow' }),
        a.backend ? h('span', { class: 'mono be', title: 'backend' }, a.backend) : null,
        ctxm,
        a.live?.spent ? h('span', { class: 'mono spent', testid: 'agent-spent' }, `${cr(a.live.spent)} cr`) : null])
    }

    function drawOpener (a) {
      items.replaceChildren()
      live.replaceChildren()
      if (a.opening) {
        items.append(h('div', { class: 'empty', testid: 'agent-opening' }, h('span', { class: 'spinner' }), h('b', null, 'Opening the board session…')))
        return
      }
      const profiles = (a.profiles || []).map(p => p.name)
      const last = storage.get('agent-open', {})
      const pick = choose(profiles, { value: last.profile || profiles[0], testid: 'agent-open-profile' })
      const model = h('input', { testid: 'agent-open-model', list: 'agent-open-models', value: last.model || '', placeholder: 'the profile’s own', spellcheck: 'false' })
      const opts = h('datalist', { id: 'agent-open-models' }, (a.models || []).map(m => h('option', { value: m.model }, (m.uses || []).join(', '))))
      const go = h('button', {
        type: 'button',
        class: 'btn pri',
        testid: 'agent-open',
        onclick: async () => {
          go.disabled = true
          const req = {}
          if (pick.value) req.profile = pick.value
          if (model.value.trim()) req.model = model.value.trim()
          storage.set('agent-open', req)
          try { await ctx.api.post('/api/agent/open', req); v.err = null } catch (err) { v.err = err }
          go.disabled = false
          load()
        }
      }, 'Open the board agent')
      items.append(h('div', { class: 'aopen', testid: 'agent-opener' },
        h('p', null, 'A conversation with an agent that sees the whole board and acts on it with the board’s own tools — the same session as the terminal’s agent tab.'),
        h('div', { class: 'vrow' },
          profiles.length ? field('Profile', pick) : null,
          field('Model', h('span', { class: 'mf' }, model, opts), { hint: 'leave empty for the profile’s model' })),
        h('div', { class: 'vfoot' }, go)))
    }

    async function switchTo (req, revert) {
      const doit = async (confirm) => {
        try {
          // confirm is the token the server's question came with
          await ctx.api.post('/api/agent/profile', confirm ? { ...req, confirm } : req)
          v.err = null
          v.confirm = null
          nodes.clear()
        } catch (err) {
          if (err.status === 409 && err.data?.error === 'confirm') {
            v.confirm = {
              question: err.data.text || 'Start a fresh conversation?',
              detail: 'The current conversation ends; a fresh one starts under the new choice.',
              yes: 'Switch',
              no: 'Keep this one',
              onYes: () => doit(err.data.confirm || ''),
              onNo: () => { v.confirm = null; revert?.(); draw() }
            }
          } else {
            v.err = err
            v.confirm = null
            revert?.()
          }
        }
        draw()
        load()
      }
      await doit(false)
    }

    async function send () {
      const text = input.value.trim()
      if (!text) return
      input.value = ''
      storage.set('agent-draft', '')
      grow()
      v.pending = text
      draw()
      try {
        await ctx.api.post('/api/agent/send', { text })
        v.err = null
        if (/^\/clear\s*$/i.test(text)) nodes.clear()
      } catch (err) {
        if (err.status === 409 && err.data?.error === 'busy') {
          input.value = err.data.text || text
          ctx.toast('The agent is mid-turn — stop it, or send when it is done', { err: true })
        } else {
          input.value = text
          v.err = err
        }
        storage.set('agent-draft', input.value)
        grow()
      }
      v.pending = null
      await load()
      input.focus()
    }

    async function interrupt () {
      try { await ctx.api.post('/api/agent/interrupt', {}) } catch (err) { v.err = err; draw() }
      load()
    }

    const offs = [onEvent('agent', () => load()), onEvent('resync', () => load())]
    draw()
    load().then(() => { grow(); input.focus() })
    return () => { v.alive = false; mounted = false; offs.forEach(off => off()) }
  }
})
