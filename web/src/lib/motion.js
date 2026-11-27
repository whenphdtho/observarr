import { prefersReducedMotion } from 'svelte/motion';

// Motion shared by the views. The durations and curves are the ones app.css defines (--dur-*, --ease-*), read
// from the page: the Web Animations API takes a curve as its CSS text, Svelte's transitions and tweens as a
// function of the same curve. Every duration goes through ms(), so a reader who asks for reduced motion, or a tab
// nobody looks at, gets none of the movement: the change itself still happens, just at once.

let tokens = null;
function token(name) {
  tokens ??= getComputedStyle(document.documentElement);
  return tokens.getPropertyValue(name).trim();
}

// dur gives a duration token in ms: 'fast', 'base', 'slow' or 'glow'.
export function dur(name) {
  const v = token(`--dur-${name}`); // "360ms", or "0.36s" once the stylesheet is minified
  return (parseFloat(v) || 0) * (v.endsWith('ms') ? 1 : 1000);
}

// curve gives an easing token as CSS text, for the Web Animations API: 'out', 'emph' or 'exit'.
export function curve(name) {
  return token(`--ease-${name}`) || 'ease-out';
}

// ease gives the same curve as a function of progress (0..1), for Svelte: the cubic Bézier of the token, solved
// for x by bisection. It is kept once the stylesheet gave it; while the token is still empty, the fallback serves
// but is not kept.
const eases = new Map();
export function ease(name) {
  if (!eases.has(name)) {
    const text = token(`--ease-${name}`);
    const [x1, y1, x2, y2] = (text.match(/-?[\d.]+/g) ?? [0.25, 0.1, 0.25, 1]).map(Number);
    const at = (a, b, t) => 3 * a * t * (1 - t) ** 2 + 3 * b * t * t * (1 - t) + t ** 3;
    const fn = (x) => {
      let lo = 0;
      let hi = 1;
      for (let i = 0; i < 20; i++) {
        const mid = (lo + hi) / 2;
        if (at(x1, x2, mid) < x) lo = mid;
        else hi = mid;
      }
      return at(y1, y2, (lo + hi) / 2);
    };
    if (!text) return fn;
    eases.set(name, fn);
  }
  return eases.get(name);
}

export function ms(duration) {
  return prefersReducedMotion.current || document.hidden ? 0 : duration;
}

// reveal is an opening: the height grows while the content fades in. A closing is quicker than an opening.
export function reveal(node, { duration = dur('base'), delay = 0, easing = ease('out') } = {}) {
  const d = ms(duration);
  if (d === 0) return { duration: 0 };
  const style = getComputedStyle(node);
  const h = node.offsetHeight;
  const pt = parseFloat(style.paddingTop);
  const pb = parseFloat(style.paddingBottom);
  const mt = parseFloat(style.marginTop);
  const mb = parseFloat(style.marginBottom);
  return {
    delay,
    duration: d,
    easing,
    css: (t) =>
      `overflow: hidden; height: ${t * h}px; padding-top: ${t * pt}px; padding-bottom: ${t * pb}px;` +
      `margin-top: ${t * mt}px; margin-bottom: ${t * mb}px; opacity: ${Math.min(1, t * 1.6)};`,
  };
}

export function conceal(node) {
  return reveal(node, { duration: dur('fast'), easing: ease('exit') });
}

// arrive is a view, block or row coming in: a short fade with a few pixels of rise. It has no exit, so two views
// never overlap.
export function arrive(node, { duration = dur('base'), y = 6 } = {}) {
  const d = ms(duration);
  if (d === 0) return { duration: 0 };
  return { duration: d, easing: ease('out'), css: (t, u) => `opacity: ${t}; transform: translateY(${u * y}px);` };
}

// leave is a row going: a quick fade. In a list with animate:flip the row is lifted out of the flow while it
// fades, so the rows below move up at once.
export function leave() {
  return { duration: ms(dur('fast')), easing: ease('exit'), css: (t) => `opacity: ${t};` };
}

// FLIP is the options of animate:flip for a list whose rows move: they travel to their new place.
export const FLIP = { get duration() { return ms(dur('slow')); }, get easing() { return ease('emph'); } };

// keyed pairs each item of a list with a key from key(item) that is unique in the list, for a keyed {#each} over
// items that have no id of their own: a repeat gets a count after it, the next count that no other item has.
export function keyed(list, key) {
  const taken = new Set();
  return list.map((item) => {
    const k = key(item);
    let out = k;
    for (let n = 1; taken.has(out); n++) out = `${k}#${n}`;
    taken.add(out);
    return { key: out, item };
  });
}

// moveAll runs change (which may return a promise, e.g. Svelte's tick) and lets each of nodes travel from where it
// was to where the change puts it, instead of jumping: for a layout that reflows, as a grid of cards when one
// card takes a whole row.
export async function moveAll(nodes, change) {
  const d = ms(dur('slow'));
  const before = d ? [...nodes].map((n) => [n, n.getBoundingClientRect()]) : [];
  await change();
  for (const [n, a] of before) {
    if (!n.isConnected) continue;
    const b = n.getBoundingClientRect();
    const dx = a.left - b.left;
    const dy = a.top - b.top;
    if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
    n.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }], { duration: d, easing: curve('emph') });
  }
}

// smoothHeight is an action for a box whose one child changes height (a form that steps on, a list that grows):
// the box eases to the new height instead of snapping, and clips its child meanwhile. A change of width (the
// window was resized) is taken as it is. The box is a positioned one, so it also clips the rows of a flip list that
// are lifted out of the flow while they leave.
export function smoothHeight(node) {
  const inner = node.firstElementChild;
  if (!inner) return {};
  node.style.position = 'relative';
  let h = inner.offsetHeight;
  let w = inner.offsetWidth;
  const ro = new ResizeObserver(() => {
    const nh = inner.offsetHeight;
    const nw = inner.offsetWidth;
    const d = ms(dur('base'));
    if (d && nw === w && Math.abs(nh - h) > 1) {
      // From where it stands: mid-way through an earlier ease, else at the old height (the box already has the new).
      const running = node.getAnimations();
      const from = running.length ? node.getBoundingClientRect().height : h;
      for (const a of running) a.cancel();
      node.style.overflow = 'hidden';
      const a = node.animate([{ height: `${from}px` }, { height: `${nh}px` }], { duration: d, easing: curve('emph') });
      a.onfinish = () => { node.style.overflow = ''; };
    }
    h = nh;
    w = nw;
  });
  ro.observe(inner);
  return { destroy: () => ro.disconnect() };
}

// flash is an action that lights the element's ground once, in the colour of the new tone ('ok', 'warn' or
// 'fail'), whenever tone moves from one of them to another: a service that goes down or comes back is noticed
// without reading. The first reading (from 'unknown') is not a change.
const TONES = ['ok', 'warn', 'fail'];
export function flash(node, tone) {
  let prev = tone;
  return {
    update(next) {
      const was = prev;
      prev = next;
      if (next === was || !TONES.includes(was) || !TONES.includes(next)) return;
      const d = ms(dur('glow'));
      if (!d) return;
      const colour = token(`--${next}-flash`);
      node.animate([{ backgroundColor: colour }, {}], { duration: d, easing: curve('out') });
    },
  };
}

// chartShift is an action for a chart's plot group, given { t0, span, width, live, fade }. While live, when the
// time window moves on by a sample, the drawing starts where it was and glides to its new place, instead of jumping
// one step to the left. When the span changes (another range was picked) the new drawing fades in over the old
// place, unless fade is false: the chart then moves its own lines from the one range into the other.
export function chartShift(node, params) {
  let prev = params;
  return {
    update(next) {
      const p = prev;
      prev = next;
      if (!p?.span || !next?.span || next.width !== p.width) return;
      const d = ms(dur('slow'));
      if (!d) return;
      if (Math.abs(next.span - p.span) > p.span * 0.05) {
        if (next.fade !== false) node.animate([{ opacity: 0 }, { opacity: 1 }], { duration: d, easing: curve('out') });
        return;
      }
      if (!next.live) return;
      const dx = ((next.t0 - p.t0) / next.span) * next.width;
      if (!(dx > 0.2) || dx > 40) return;
      node.animate([{ transform: `translateX(${dx}px)` }, { transform: 'translateX(0)' }], { duration: d, easing: curve('emph') });
    },
  };
}
