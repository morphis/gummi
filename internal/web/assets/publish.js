// publish.js — publishing a card (DESIGN §22): the dialog a person confirms
// before gummi pushes the card's branch or opens, updates, readies or
// drafts its pull request. The page decides nothing: the facts it shows are
// the ones the server resolved (GET /api/cards/{id}/publish?act=), and the
// confirm sends back their fingerprint, so an act the facts moved under is
// refused there rather than run.
//
// Both halves are GitHub's to answer, so neither waits in silence: the
// dialog is up, saying what it reads, before the facts are; and a confirmed
// act turns the plan's steps into a checklist the board marks as it goes
// (the "work" events), the one that failed marked where it stopped.

import { h, clear, plural } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { onWork, hold, waited } from './work.js?v=__ASSET_V__'

const TITLE = { create: 'Open a pull request', push: 'Push to GitHub', update: 'Update the pull request', ready: 'Mark the pull request ready', draft: 'Back to draft' }
const GO = { create: 'Open pull request', push: 'Push', update: 'Push update', ready: 'Mark ready', draft: 'Back to draft' }

// publishLabel is the button an act is offered as.
export function publishLabel (act) { return GO[act] || act }

// refusal is a typed publish error in one sentence, with its fix.
function refusal (e) {
  if (!e) return ''
  return e.fix ? `${e.text} — ${e.fix}` : e.text
}

// openPublish reads the facts for act and shows the confirm. A refusal is
// shown in the dialog, in the server's words, with nothing to confirm.
// baseRepo is the repository the person chose for the PR to open in (a
// fork's own or its parent's); the facts and their fingerprint are that
// repository's, so a choice reads them again. typed carries the title and
// description across that.
export async function openPublish (card, act, { returnTo = '[data-testid="card-actions"]', done, baseRepo = '', typed = null } = {}) {
  const title = `${TITLE[act] || act} · ${card.id}`
  // the read asks GitHub about the repository, the remote branch and the
  // PR: the dialog is up, saying so, for as long as that takes
  let gone = false
  const reading = openModal({
    title,
    testid: 'publish-dialog',
    card: card.id,
    returnTo,
    onClose: () => { gone = true },
    body: h('div', { class: 'aform' },
      h('p', { class: 'aq pubwait', testid: 'publish-reading', role: 'status' }, h('span', { class: 'spinner' }), 'Reading where the branch goes from GitHub…')),
    actions: [{ label: 'Cancel', testid: 'publish-cancel' }]
  })
  let facts
  try {
    facts = await get(cardPath(card.id, 'publish') + `?act=${encodeURIComponent(act)}` + (baseRepo ? `&baseRepo=${encodeURIComponent(baseRepo)}` : ''))
  } catch (err) {
    reading.close()
    toast(err.notBuilt ? 'Publishing from the web is not available on this board' : err.message, { err: !err.notBuilt })
    return
  }
  // closed while it read: the person moved on
  if (gone) return
  const repos = facts.baseRepos || []
  const retarget = (repo, typed) => openPublish(card, act, { returnTo, done, baseRepo: repo, typed })
  if (facts.error && facts.error.code === 'base-unchosen' && repos.length > 1) {
    // a fork nobody has chosen a target for: a question, not a refusal
    openModal({
      title,
      testid: 'publish-dialog',
      card: card.id,
      returnTo,
      body: h('div', { class: 'aform' },
        h('p', { class: 'aq', testid: 'publish-choose' }, `${repos[0]} is a fork. Where should the pull request open?`),
        h('p', { class: 'sub' }, 'Your choice is remembered for this repository once the pull request opens.')),
      actions: [
        { label: 'Cancel', testid: 'publish-cancel' },
        ...repos.map((r, i) => ({ label: r, primary: i === 0, testid: `publish-base-${i}`, onClick: () => { retarget(r, typed) } }))
      ]
    })
    return
  }
  if (facts.error) {
    openModal({
      title,
      testid: 'publish-dialog',
      card: card.id,
      returnTo,
      body: h('div', { class: 'aform' }, h('p', { class: 'aerr', testid: 'publish-refusal', role: 'alert' }, refusal(facts.error))),
      actions: [{ label: 'Close', testid: 'publish-cancel' }]
    })
    return
  }
  const error = h('p', { class: 'aerr', testid: 'publish-error', role: 'alert', hidden: true })
  const body = h('div', { class: 'aform' }, h('p', { class: 'aq', testid: 'publish-summary' }, facts.summary))
  let titleIn = null; let bodyIn = null; let draftIn = null; let hookIn = null
  if (act === 'create') {
    titleIn = h('input', { testid: 'publish-title', value: typed?.title ?? facts.title ?? '', autocomplete: 'off', spellcheck: 'true' })
    bodyIn = h('textarea', { testid: 'publish-body', value: typed?.body ?? facts.body ?? '', rows: 6, spellcheck: 'true' })
    if (repos.length > 1) {
      const baseIn = h('select', {
        testid: 'publish-base',
        onchange: () => { retarget(baseIn.value, { title: titleIn.value, body: bodyIn.value }) }
      }, repos.map(r => h('option', { value: r, selected: r === facts.baseRepo }, r)))
      body.append(h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Opens in'), baseIn))
    }
    draftIn = h('input', { type: 'checkbox', testid: 'publish-draft', checked: !!facts.draft, disabled: !!facts.draftLocked })
    body.append(
      h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Title'), titleIn),
      h('label', { class: 'field' }, h('span', { class: 'fl' }, 'Description'), bodyIn),
      h('label', { class: 'pubcheck' }, draftIn, h('span', null, 'Open as draft',
        facts.draftLocked ? h('span', { class: 'sub', testid: 'publish-draft-why' }, facts.draftWhy) : null)))
  }
  if (facts.hook) {
    hookIn = h('input', { type: 'checkbox', testid: 'publish-hook' })
    body.append(h('label', { class: 'pubcheck warn' }, hookIn,
      h('span', null, 'Run this repository’s pre-push hook with my credential', h('span', { class: 'sub mono' }, facts.hook))))
  }
  body.append(h('details', { class: 'pubfacts', testid: 'publish-details' },
    h('summary', null, 'Details'),
    h('dl', null,
      fact('Branch', `${facts.branch} @ ${(facts.tip || '').slice(0, 7)}${facts.tipSubject ? ' — ' + facts.tipSubject : ''}`),
      fact('Commits', plural(facts.ahead || 0, 'commit') + ` ahead of ${facts.base}`),
      fact('Pushes to', `${facts.remote} → ${facts.pushUrl}${facts.push ? ' (' + facts.push + ')' : ''}`),
      fact('Pull request', facts.pr ? `#${facts.pr.number} · ${String(facts.pr.state || '').toLowerCase()}${facts.pr.draft ? ' · draft' : ''}` : facts.baseRepo ? `${facts.head} → ${facts.baseRepo}:${facts.base}` : ''),
      fact('As', facts.gh)),
    (facts.commands || []).length ? h('pre', { class: 'pubcmds', testid: 'publish-commands' }, facts.commands.join('\n')) : null))
  const steps = stepList(facts.steps || [])
  body.append(steps.el, error)
  const fail = (text) => { clear(error).append(text); error.hidden = false; return false }
  // while the act runs what was confirmed is not edited under it
  const lock = (on) => { for (const el of body.querySelectorAll('input,textarea,select')) el.disabled = on || (el === draftIn && !!facts.draftLocked) }
  openModal({
    title,
    testid: 'publish-dialog',
    card: card.id,
    returnTo,
    body,
    actions: [
      { label: 'Cancel', testid: 'publish-cancel' },
      {
        label: facts.toDraft ? 'Push and return to draft' : GO[act] || 'Confirm',
        primary: true,
        testid: 'publish-confirm',
        onClick: async () => {
          const req = { act, fingerprint: facts.fingerprint, draft: !!facts.toDraft }
          if (baseRepo) req.baseRepo = baseRepo
          if (act === 'create') {
            req.title = titleIn.value.trim()
            req.body = bodyIn.value
            req.draft = draftIn.checked
            if (!req.title) { titleIn.focus(); return fail('A pull request needs a title.') }
          }
          if (hookIn && !hookIn.checked) return fail('Read the pre-push hook and tick the box to let it run.')
          error.hidden = true
          // the board says each step as it starts it, and the one that
          // failed: the checklist is this act's, so the notices leave it be
          steps.start()
          lock(true)
          const release = hold(card.id)
          const off = onWork((c) => { if (c.id === card.id && c.step) steps.at(c.step, !!c.err) })
          const stopped = (text) => { steps.fail(); lock(false); return fail(text) }
          try {
            const res = await post(cardPath(card.id, 'publish'), req)
            // a typed refusal (webapi.PublishError): the step it stopped
            // on stays marked, and the words say what went through before it
            if (res.error) return stopped(refusal(res.error))
            steps.done()
            toast(said(res), { ack: card.id })
            if (res.linkError) toast(res.linkError, { err: true })
            done?.(res)
            return true
          } catch (err) {
            return stopped(err.message)
          } finally {
            off()
            release()
          }
        }
      }
    ]
  })
}

// stepList is the plan's steps as a checklist: hidden until the act starts,
// then each one done, running (with a clock once it has run a while),
// failed or still to come. The board names the step it is on; every step
// before that one is done.
const MARK = { todo: '○', done: '✓', fail: '✕' }
function stepList (steps) {
  const rows = steps.map(s => {
    const mark = h('span', { class: 'mark', 'aria-hidden': 'true' })
    const time = h('span', { class: 'sub', 'aria-hidden': 'true' })
    return { mark, time, li: h('li', { testid: `publish-step-${s.id}` }, mark, h('span', null, s.text), time) }
  })
  const el = h('ol', { class: 'pubsteps', testid: 'publish-steps', 'aria-live': 'polite', hidden: true }, rows.map(r => r.li))
  let on = -1; let since = 0; let timer = 0
  const paint = (failed) => rows.forEach((r, i) => {
    const state = i < on ? 'done' : i > on ? 'todo' : failed ? 'fail' : 'run'
    if (r.li.dataset.state === state) return
    r.li.dataset.state = state
    clear(r.mark).append(state === 'run' ? h('span', { class: 'spinner sm' }) : MARK[state])
    if (state !== 'run') r.time.textContent = ''
  })
  const stop = () => { clearInterval(timer); timer = 0 }
  const go = (i) => {
    on = i; since = Date.now()
    paint(false)
    stop()
    timer = setInterval(() => {
      if (!el.isConnected) return stop()
      const t = waited(since)
      if (rows[on]) rows[on].time.textContent = t ? ` · ${t}` : ''
    }, 1000)
  }
  return {
    el,
    start: () => { if (rows.length) { el.hidden = false; go(0) } },
    // at is the board naming the step it is on, or the one that failed
    at: (id, failed) => {
      const i = steps.findIndex(s => s.id === id)
      if (i < 0 || i < on) return
      if (i > on) go(i)
      if (failed) { stop(); paint(true) }
    },
    fail: () => { stop(); if (on >= 0) paint(true) },
    done: () => { stop(); on = rows.length; paint(false) }
  }
}

function fact (k, v) {
  return [h('dt', null, k), h('dd', { class: 'mono' }, v || '—')]
}

// said is what an act did, in one line.
function said (r) {
  const parts = []
  if (r.pushed) parts.push(`Pushed ${r.pushed.slice(0, 7)}`)
  if (r.act === 'create') parts.push(`opened PR #${r.number}${r.draft ? ' as a draft' : ''}`)
  if (r.act === 'update') parts.push(`updated PR #${r.number}`)
  if (r.act === 'ready') parts.push(`PR #${r.number} is ready for review`)
  if (r.act === 'draft' || r.toDraft) parts.push(`PR #${r.number} is a draft again`)
  const s = parts.join(' and ') || 'Nothing to do'
  return s[0].toUpperCase() + s.slice(1)
}
