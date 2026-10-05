// back.js — the browser's back button, and a phone's back gesture, step
// back inside the board before they leave it. Everything that sits on top
// of the page — a dialog or view, the palette, a menu, and on a phone the
// thread or documents over the cards — is a layer with one history entry:
// back closes the top layer, and a layer closed any other way (its ×, esc,
// a tap outside) takes its entry back out, so the history always matches
// what is on screen. With no layer open, back leaves the page as usual.
//
// Every entry the page stands on carries a sequence number in its state, a
// layer's and the plain entries under them alike. Arriving at an entry that
// has one is a step back (or forward): the layers opened after it close. An
// entry without one is new — a link to a card, a notification's URL — and
// is a navigation, not a step.

const stack = [] // open layers, oldest first: [{ seq, close, dead, pushed }]
// Numbers start from the clock, not from 0: a reload keeps the history,
// stamps and all, and a layer opened after it must still number above
// every entry an earlier load of the page left there — or back, arriving
// at one of those, would find nothing newer than it to close.
let seq = Date.now()
let ours = 0 // history.back() calls we made, whose popstate is ours to swallow
// the address of a navigation (a hash typed or linked) made while one of
// our steps back was still under way: that step was queued first, so it
// lands behind the navigation — where exactly, the browser decides — and
// the navigation is made again from there
let overtaken = null
// the address a step back arrived at, for a moment: the hash change it
// raises is the tail of that step, not a navigation — and only that one
let stepped = { hash: null, until: 0 }

const stamp = () => { stepped = { hash: location.hash, until: Date.now() + 500 } }

// mark numbers the entry the page stands on, if nothing has yet.
function mark () {
  if (history.state?.gummiSeq != null) return
  try { history.replaceState({ ...(history.state || {}), gummiSeq: ++seq }, '', location.href) } catch {}
}

function push (l) {
  try { history.pushState({ gummiSeq: l.seq, gummiLayer: true }, '', location.href); l.pushed = true } catch {}
}

// pushLayer records a layer and returns done(), to call when it closes by
// any means other than back. close() runs when back closes it.
export function pushLayer (close) {
  mark()
  const l = { seq: ++seq, close, dead: false, pushed: false }
  // a reload made on a layer stands on that layer's old entry, with nothing
  // open over it: the first layer opened takes the entry over rather than
  // pushing another on top, or back would step through a dead one first
  if (!stack.length && !ours && history.state?.gummiLayer) {
    l.seq = history.state.gummiSeq
    l.pushed = true
    stack.push(l)
    return done
  }
  stack.push(l)
  // a layer closed a moment ago may still be stepping its entry back out
  // (history.back is asynchronous): this one's entry waits for that, or
  // the step back would take this one's instead
  if (!ours) push(l)
  return done
  function done () {
    const i = stack.indexOf(l)
    if (i < 0) return // back closed it
    if (!l.pushed) { stack.splice(i, 1); return }
    if (i === stack.length - 1 && history.state?.gummiSeq === l.seq) {
      stack.pop()
      ours++
      history.back()
    } else {
      // an entry under another layer cannot be taken out from the middle;
      // it stays in the history and is passed over when back reaches it
      l.dead = true
    }
  }
}

// stepping reports whether a hash change is the tail of a step back through
// the page's own entries: the entry under a layer may still name the card
// that was open before it, and following it would be a navigation nobody
// asked for.
export function stepping () {
  return Date.now() < stepped.until && location.hash === stepped.hash
}

let inited = false
export function initBack () {
  mark()
  if (inited) return // boot runs again after pairing; one listener is the whole design
  inited = true
  window.addEventListener('popstate', (e) => {
    // our own step back always arrives on an entry this page numbered; a
    // new entry (a hash typed or linked while that step was still under
    // way) has no number, and is a navigation to follow, not ours to
    // swallow — swallowed, its hash would read as the tail of a step and
    // the card it names would never open
    if (ours && e.state?.gummiSeq != null) {
      ours--
      stamp() // our own step back arrived: its hash is not a navigation
      if (!ours && overtaken != null) {
        // it went past a navigation made meanwhile: make that again
        const to = overtaken
        overtaken = null
        location.hash = to
      }
      if (!ours) for (const l of stack) if (!l.pushed && !l.dead) push(l)
      return
    }
    if (ours) { overtaken = location.hash; mark(); return }
    const to = e.state?.gummiSeq
    if (to == null) { mark(); return } // a new entry: a navigation
    let closed = false
    while (stack.length && stack[stack.length - 1].seq > to) {
      const l = stack.pop()
      if (!l.dead) {
        closed = true
        try { l.close() } catch (err) { console.error(err) }
      }
    }
    if (closed) stamp()
  })
  // a browser that does not raise popstate for a new hash still numbers it
  window.addEventListener('hashchange', mark)
}
