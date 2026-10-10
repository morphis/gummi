// publish.js — publishing a card (DESIGN §22): the dialog a person confirms
// before gummi pushes the card's branch or opens, updates, readies or
// drafts its pull request. The page decides nothing: the facts it shows are
// the ones the server resolved (GET /api/cards/{id}/publish?act=), and the
// confirm sends back their fingerprint, so an act the facts moved under is
// refused there rather than run.

import { h, clear, plural } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { openModal } from './views.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'

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
export async function openPublish (card, act, { returnTo = '[data-testid="card-actions"]', done } = {}) {
  let facts
  try {
    facts = await get(cardPath(card.id, 'publish') + `?act=${encodeURIComponent(act)}`)
  } catch (err) {
    toast(err.notBuilt ? 'Publishing from the web is not available on this board' : err.message, { err: !err.notBuilt })
    return
  }
  const title = `${TITLE[act] || act} · ${card.id}`
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
    titleIn = h('input', { testid: 'publish-title', value: facts.title || '', autocomplete: 'off', spellcheck: 'true' })
    bodyIn = h('textarea', { testid: 'publish-body', value: facts.body || '', rows: 6, spellcheck: 'true' })
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
      fact('Pull request', facts.pr ? `#${facts.pr.number} · ${String(facts.pr.state || '').toLowerCase()}${facts.pr.draft ? ' · draft' : ''}` : `${facts.head} → ${facts.baseRepo}:${facts.base}`),
      fact('As', facts.gh)),
    (facts.commands || []).length ? h('pre', { class: 'pubcmds', testid: 'publish-commands' }, facts.commands.join('\n')) : null),
  error)
  const fail = (text) => { clear(error).append(text); error.hidden = false; return false }
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
          if (act === 'create') {
            req.title = titleIn.value.trim()
            req.body = bodyIn.value
            req.draft = draftIn.checked
            if (!req.title) { titleIn.focus(); return fail('A pull request needs a title.') }
          }
          if (hookIn && !hookIn.checked) return fail('Read the pre-push hook and tick the box to let it run.')
          error.hidden = true
          try {
            const res = await post(cardPath(card.id, 'publish'), req)
            // a typed refusal (webapi.PublishError): nothing was published
            if (res.error) return fail(refusal(res.error))
            toast(said(res), { ack: card.id })
            if (res.linkError) toast(res.linkError, { err: true })
            done?.(res)
            return true
          } catch (err) {
            return fail(err.message)
          }
        }
      }
    ]
  })
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
