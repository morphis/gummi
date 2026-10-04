// pr.js — the PR tab: the linked pull request, read from GitHub and never
// written to (DESIGN §20.5): its state, its review threads (each can jump
// to the file in the diff), top-level comments, and the push command a
// person runs themselves.

import { h, clock, plural } from './dom.js?v=__ASSET_V__'
import { get, post, cardPath } from './api.js?v=__ASSET_V__'
import { markdown } from './markdown.js?v=__ASSET_V__'
import { toast } from './toast.js?v=__ASSET_V__'
import { runAction } from './actions.js?v=__ASSET_V__'
import { reveal } from './diff.js?v=__ASSET_V__'

export const prTab = {
  name: 'pr',
  label: 'PR',
  key: 'g p',
  fetch: (id) => get(cardPath(id, 'pr')),
  empty: (p) => !p || !p.linked,
  count: (p) => (p?.threads || []).filter(t => !t.resolved).length,
  render
}

function render (pane, entry, ctx) {
  const p = entry.data
  if (!p || !p.linked) {
    const link = (ctx.card?.actions || []).find(a => a.id === 'prlink')
    pane.append(h('div', { class: 'empty', testid: 'pr-none' }, h('b', null, 'No pull request linked'),
      p?.error || 'Link one to read its review threads beside the diff.',
      link
        ? h('button', { class: 'btn', type: 'button', testid: 'pr-link', onclick: () => runAction(ctx.card, link) }, 'Link a pull request')
        : null,
      pushBox(p?.pushCommand, ctx)))
    return
  }
  const threads = p.threads || []
  const sect = h('div', { class: 'sect', testid: 'pr' },
    h('div', { class: 'prcard' },
      h('div', { class: 't' }, ctx.card?.title || 'Pull request', ' ', h('span', null, p.ref || '')),
      h('div', { class: 'kv' },
        p.state ? h('span', { class: ['state', /closed/i.test(p.state) && 'closed', /merged/i.test(p.state) && 'merged'], testid: 'pr-state' }, `● ${p.state.toLowerCase()}`) : null,
        h('span', { testid: 'pr-open' }, plural(threads.filter(t => !t.resolved).length, 'open thread')),
        p.url ? h('a', { href: safeUrl(p.url), target: '_blank', rel: 'noopener noreferrer' }, 'open on GitHub') : null),
      p.error ? h('div', { class: 'badc' }, p.error) : null,
      h('div', { class: 'prmeta' },
        p.fetched ? h('span', { testid: 'pr-fetched' }, `read from GitHub ${clock(p.fetched)}`) : null,
        p.headSha ? h('span', { class: 'mono' }, `head ${p.headSha.slice(0, 7)}`) : null,
        h('button', { class: 'btn', type: 'button', testid: 'pr-refresh', onclick: () => refresh(ctx) }, 'Refresh'),
        h('button', { class: 'btn', type: 'button', testid: 'pr-pull', title: 'Bring the open review threads into the diff as comments', onclick: () => pull(ctx) }, 'Pull threads into the diff'))))
  if (threads.length) {
    sect.append(h('p', { class: 'label' }, 'Review threads, read from GitHub'))
    threads.forEach((t, i) => sect.append(h('div', { class: 'rthread', testid: `pr-thread-${i}` },
      h('div', { class: 'rh' }, `${t.path || 'conversation'}${t.line ? ':' + t.line : ''}`,
        t.resolved ? h('span', { class: 'okc' }, 'resolved') : null,
        t.outdated ? h('span', null, 'outdated') : null,
        t.path ? h('button', { class: 'link go', type: 'button', testid: `pr-thread-show-${i}`, onclick: () => { reveal(ctx.id, t.path, t.line); ctx.setTab('diff') } }, 'show in diff') : null),
      (t.notes || []).map(n => note(n)))))
  }
  if (p.comments?.length) {
    sect.append(h('p', { class: 'label' }, 'Comments'))
    sect.append(h('div', { class: 'rthread' }, p.comments.map(n => note(n))))
  }
  sect.append(pushBox(p.pushCommand, ctx))
  pane.append(sect)
}

function note (n) {
  return h('div', { class: 'c1' }, h('div', { class: 'who' }, h('b', null, n.author || 'someone'), n.at ? ` · ${clock(n.at)}` : ''), markdown(n.body))
}

// pushAdvice is what the push box says about when to push, for the card
// as it stands; null when there is nothing to push at all.
function pushAdvice (c, cmd) {
  if (c?.landed) return { text: 'This card has landed: its work is on its base. gummi does not push; there is nothing left that needs pushing.', cmd: null }
  if (c?.scratch || c?.kind === 'research') return { text: 'A research card works in a scratch tree and never gets a branch: there is nothing to push.', cmd: null }
  if (!cmd) return c?.stage === 'todo' ? { text: 'This card has no branch yet: it is cut when the card starts. gummi never pushes it for you.', cmd: null } : null
  const forced = /--force-with-lease/.test(cmd)
  const lead = forced
    ? 'gummi does not push. The branch was rewritten after it was pushed, so the remote needs a force push:'
    : 'gummi does not push.'
  if (forced) return { text: lead, cmd }
  // a session is never verified: it lands on a person's read of its diff
  if (c?.kind === 'freeform' || c?.stage === 'open') return { text: `${lead} A session has no verify; when its diff reads right to you, push it yourself:`, cmd }
  if (c?.adopted) return { text: `${lead} This branch is adopted, so gummi will not rebase it either. When verify passes, push it yourself:`, cmd }
  return { text: `${lead} When the card is verified, push it yourself:`, cmd }
}

function pushBox (command, ctx) {
  const say = pushAdvice(ctx.card, command)
  if (!say) return null
  if (!say.cmd) return h('p', { class: 'push none', testid: 'pr-push' }, say.text)
  const cmd = say.cmd
  return h('div', { class: 'push', testid: 'pr-push' },
    h('span', null, say.text),
    h('div', { class: 'cmd' }, h('span', { testid: 'pr-push-cmd' }, cmd),
      h('button', {
        type: 'button',
        testid: 'pr-push-copy',
        onclick: () => (navigator.clipboard?.writeText(cmd) ?? Promise.reject(new Error('no clipboard'))).then(() => toast('Copied'), () => toast('Select the command to copy it'))
      }, 'Copy')))
}

function safeUrl (u) {
  try { const x = new URL(u); return /^https?:$/.test(x.protocol) ? x.href : '#' } catch { return '#' }
}

async function refresh (ctx) {
  try {
    ctx.swap(await get(cardPath(ctx.id, 'pr') + '?refresh=1'))
  } catch (err) {
    toast(err.message, { err: true })
  }
}

// pull runs on the server and finishes later: a toast and a card change say
// when the threads have landed in the diff.
async function pull (ctx) {
  try {
    await post(cardPath(ctx.id, 'pr/pull'), {})
    toast('Pulling the review threads; the diff shows them when they land')
  } catch (err) {
    toast(err.notBuilt ? 'Pulling threads from the web is not available yet' : err.message, { err: !err.notBuilt })
  }
}
