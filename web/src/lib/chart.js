// Axis and hover helpers shared by the charts.

// niceCeil rounds v up to 1, 2, 2.5 or 5 times a power of ten, so axis labels read cleanly.
export function niceCeil(v) {
  if (!(v > 0)) return 1;
  const mag = 10 ** Math.floor(Math.log10(v));
  return [1, 2, 2.5, 5, 10].map((m) => m * mag).find((n) => n >= v);
}

const MINUTE = 60000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 180, 240, 360, 720].map((m) => m * MINUTE);
const DAY_STEPS = [1, 2, 7, 14].map((d) => d * DAY);

// timeTicks returns round local times inside [from, to] (ms since epoch), at most about six of them:
// whole minutes or hours for a day or less, local midnights for weeks, first days of months for longer.
export function timeTicks(from, to) {
  const span = to - from;
  const out = [];
  if (span <= 2 * DAY) {
    const step = STEPS.find((s) => span / s <= 6) ?? STEPS[STEPS.length - 1];
    const offset = new Date(from).getTimezoneOffset() * MINUTE; // whole local hours, not UTC ones
    for (let t = Math.ceil((from - offset) / step) * step + offset; t <= to; t += step) out.push(t);
    return out;
  }
  if (span <= 70 * DAY) {
    const step = DAY_STEPS.find((s) => span / s <= 6) ?? DAY_STEPS[DAY_STEPS.length - 1];
    const d = new Date(from);
    d.setHours(0, 0, 0, 0);
    if (d.getTime() < from) d.setDate(d.getDate() + 1);
    // Weekly ticks fall on Mondays.
    if (step >= 7 * DAY) while (d.getDay() !== 1) d.setDate(d.getDate() + 1);
    for (; d.getTime() <= to; d.setDate(d.getDate() + step / DAY)) out.push(d.getTime());
    return out;
  }
  const months = [1, 2, 3, 6].find((m) => span / (m * 30 * DAY) <= 6) ?? 12;
  const d = new Date(from);
  d.setHours(0, 0, 0, 0);
  d.setDate(1);
  if (d.getTime() < from) d.setMonth(d.getMonth() + 1);
  while (d.getMonth() % months !== 0) d.setMonth(d.getMonth() + 1);
  for (; d.getTime() <= to; d.setMonth(d.getMonth() + months)) out.push(d.getTime());
  return out;
}

export function clock(t) {
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// tickLabel names an axis tick for a chart spanning span ms: a time of day up to two days, a day and
// month up to ten weeks, a month beyond.
export function tickLabel(t, span) {
  if (span <= 2 * DAY) return clock(t);
  if (span <= 70 * DAY) return new Date(t).toLocaleDateString([], { day: 'numeric', month: 'short' });
  return new Date(t).toLocaleDateString([], { month: 'short' });
}

// stampLabel names one sample in a tooltip, as precisely as its step (ms) allows.
export function stampLabel(t, step, span) {
  const d = new Date(t);
  if (step >= DAY) return d.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
  if (step >= HOUR || span > DAY) {
    return `${d.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' })}, ${clock(t)}`;
  }
  return step < MINUTE ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : clock(t);
}

// nearest returns the index of the time in times (ascending) closest to t, or -1 for none.
export function nearest(times, t) {
  if (!times.length) return -1;
  let lo = 0;
  let hi = times.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (times[mid] < t) lo = mid;
    else hi = mid;
  }
  return Math.abs(times[lo] - t) <= Math.abs(times[hi] - t) ? lo : hi;
}

// runs splits the indexes 0..n-1 into runs where defined(i) holds, so a line breaks at a gap
// instead of bridging it. A lone defined point makes a run of one.
export function runs(n, defined) {
  const out = [];
  let cur = null;
  for (let i = 0; i < n; i++) {
    if (defined(i)) {
      if (!cur) {
        cur = [];
        out.push(cur);
      }
      cur.push(i);
    } else {
      cur = null;
    }
  }
  return out;
}

// thin merges runs of neighbouring samples so that at most max remain, for drawing more samples than there
// are pixels: each group keeps its last time and the mean of its values in each list (null when it has none,
// so a gap stays a gap). times are ascending; lists are arrays of values per time (or null).
export function thin(times, lists, max) {
  const n = times.length;
  if (n <= max || max < 2) return { times, lists };
  const k = Math.ceil(n / max);
  const outTimes = [];
  const out = lists.map(() => []);
  // Groups end at the newest sample, so a group boundary does not move as older samples drop off.
  for (let j = n; j > 0; j -= k) {
    const i = Math.max(0, j - k);
    outTimes.push(times[j - 1]);
    lists.forEach((l, li) => {
      let s = 0;
      let c = 0;
      for (let q = i; q < j; q++) {
        const v = l?.[q];
        if (Number.isFinite(v)) {
          s += v;
          c++;
        }
      }
      out[li].push(l ? (c ? s / c : null) : null);
    });
  }
  return { times: outTimes.reverse(), lists: out.map((l) => l.reverse()) };
}

// tipSide places a tooltip beside a crosshair at x in a chart width wide: to the right in the left half,
// to the left in the right half. It returns the CSS left/right pair.
export function tipSide(x, width) {
  return x < width / 2 ? { left: `${Math.round(x + 12)}px`, right: 'auto' } : { left: 'auto', right: `${Math.round(width - x + 12)}px` };
}
