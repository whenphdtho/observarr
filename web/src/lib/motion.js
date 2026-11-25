import { prefersReducedMotion } from 'svelte/motion';
import { cubicOut } from 'svelte/easing';

// Motion shared by the views. Every duration goes through ms(), so a reader who asks for reduced motion gets
// none of the movement; colour and state changes still happen, just at once.

export const EASE = cubicOut;
// The CSS easing of the same feel, for Web Animations.
export const EASE_CSS = 'cubic-bezier(0.16, 1, 0.3, 1)';

export function ms(duration) {
  return prefersReducedMotion.current ? 0 : duration;
}

// reveal is an opening: the height grows while the content fades in. A closing is quicker than an opening.
export function reveal(node, { duration = 220, delay = 0 } = {}) {
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
    easing: EASE,
    css: (t) =>
      `overflow: hidden; height: ${t * h}px; padding-top: ${t * pt}px; padding-bottom: ${t * pb}px;` +
      `margin-top: ${t * mt}px; margin-bottom: ${t * mb}px; opacity: ${Math.min(1, t * 1.6)};`,
  };
}

export function conceal(node, { duration = 150 } = {}) {
  return reveal(node, { duration });
}

// arrive is a view or block coming in: a short fade with a few pixels of rise. It has no exit, so two views
// never overlap.
export function arrive(node, { duration = 180, y = 6 } = {}) {
  const d = ms(duration);
  if (d === 0) return { duration: 0 };
  return { duration: d, easing: EASE, css: (t, u) => `opacity: ${t}; transform: translateY(${u * y}px);` };
}

// chartShift is an action for a chart's plot group: when the time window moves on by a sample, the drawing
// starts where it was and glides to its new place, instead of jumping one step to the left.
export function chartShift(node, params) {
  let prev = params;
  return {
    update(next) {
      const p = prev;
      prev = next;
      if (!p || !next || !p.span || ms(1) === 0 || document.hidden) return;
      if (Math.abs(next.span - p.span) > p.span * 0.05 || next.width !== p.width) return;
      const dx = ((next.t0 - p.t0) / next.span) * next.width;
      if (!(dx > 0.2) || dx > 40) return;
      node.animate([{ transform: `translateX(${dx}px)` }, { transform: 'translateX(0)' }], { duration: 700, easing: EASE_CSS });
    },
  };
}
