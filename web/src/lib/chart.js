// Axis helpers shared by the charts.

// niceCeil rounds v up to 1, 2, 2.5 or 5 times a power of ten, so axis labels read cleanly.
export function niceCeil(v) {
  if (!(v > 0)) return 1;
  const mag = 10 ** Math.floor(Math.log10(v));
  return [1, 2, 2.5, 5, 10].map((m) => m * mag).find((n) => n >= v);
}

const MINUTE = 60000;
const STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 180, 240, 360, 720].map((m) => m * MINUTE);

// timeTicks returns round local times inside [from, to] (ms since epoch), at most six of them.
export function timeTicks(from, to) {
  const step = STEPS.find((s) => (to - from) / s <= 6) ?? STEPS[STEPS.length - 1];
  const offset = new Date(from).getTimezoneOffset() * MINUTE; // whole local hours, not UTC ones
  const out = [];
  for (let t = Math.ceil((from - offset) / step) * step + offset; t <= to; t += step) out.push(t);
  return out;
}

export function clock(t) {
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
