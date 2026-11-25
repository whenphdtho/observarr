// Time ranges of the charts over time, and the sums and changes their figures need.

const MIN = 60 * 1000;
const DAY = 24 * 60 * MIN;

// RANGES are the spans a chart over time can show (the ?range= of GET /api/network and
// GET /api/system/history). label is the switch's option, span the window in ms; text and before name
// the window and the one of equal length before it.
export const RANGES = [
  { id: '5m', label: '5 min', name: 'Last five minutes', span: 5 * MIN, text: 'the last five minutes', before: 'the five minutes before' },
  { id: '24h', label: '24 h', name: 'Last 24 hours', span: DAY, text: 'the last 24 hours', before: 'the 24 hours before' },
  { id: '7d', label: '7 d', name: 'Last 7 days', span: 7 * DAY, text: 'the last 7 days', before: 'the 7 days before' },
  { id: '30d', label: '30 d', name: 'Last 30 days', span: 30 * DAY, text: 'the last 30 days', before: 'the 30 days before' },
  { id: '1y', label: '1 y', name: 'Last year', span: 365 * DAY, text: 'the last year', before: 'the year before' },
];

export function rangeOf(id) {
  return RANGES.find((r) => r.id === id) ?? RANGES[0];
}

// step is the usual distance between neighbouring times (their median), 0 for fewer than two.
export function step(times) {
  if (times.length < 2) return 0;
  const d = [];
  for (let i = 1; i < times.length; i++) d.push(times[i] - times[i - 1]);
  d.sort((a, b) => a - b);
  return d[d.length >> 1];
}

// integrate turns rates per second at times into a total: each value counts for the time since the previous
// one, at most one usual step, so a gap (null) adds nothing and the reading after it is not stretched over it.
export function integrate(times, values) {
  const s = step(times);
  if (!s || !values) return 0;
  let total = 0;
  for (let i = 0; i < times.length; i++) {
    const v = values[i];
    if (!Number.isFinite(v) || v <= 0) continue;
    const dt = i === 0 ? s : Math.min(s, times[i] - times[i - 1]);
    total += (v * dt) / 1000;
  }
  return total;
}

// change is the relative change from before to now (0.12 = 12% more), or null when either is missing or
// before is zero.
export function change(now, before) {
  if (!Number.isFinite(now) || !Number.isFinite(before) || before <= 0) return null;
  return (now - before) / before;
}

// mean averages the finite numbers of a list, or returns null without any.
export function mean(values) {
  let n = 0;
  let sum = 0;
  for (const v of values ?? []) {
    if (Number.isFinite(v)) {
      n++;
      sum += v;
    }
  }
  return n ? sum / n : null;
}

const num = (v) => (Number.isFinite(v) ? v : null);

// hostSeries reads GET /api/system/history into { times, cpu, mem } (ms since epoch, percent, bytes; null
// for a gap), or null when the answer does not cover the range asked for. It takes a list of readings
// ({ at, cpuPercent, memUsed }) or series ({ times } or { start, step } with cpu and memUsed arrays).
export function hostSeries(data, rangeId) {
  if (Array.isArray(data)) {
    const times = data.map((h) => new Date(h.at).getTime());
    // A server without the longer ranges answers any of them with its five minutes: not what was asked.
    const covered = times.length > 1 ? times[times.length - 1] - times[0] : 0;
    if (rangeId !== '5m' && covered < rangeOf(rangeId).span / 8) return null;
    return { times, cpu: data.map((h) => num(h.cpuPercent)), mem: data.map((h) => num(h.memUsed)) };
  }
  if (!data || typeof data !== 'object') return null;
  const cpu = data.cpu ?? data.cpuPercent;
  const mem = data.memUsed ?? data.mem;
  if (!Array.isArray(cpu)) return null;
  let times = data.times;
  if (!Array.isArray(times)) {
    // A numeric start and step are both seconds (Unix time) or both ms; a start string is a date with the step in seconds.
    const seconds = typeof data.start !== 'number' || data.start < 1e12;
    const start = typeof data.start === 'number' ? data.start * (seconds ? 1000 : 1) : new Date(data.start).getTime();
    const stepMs = (data.step ?? 0) * (seconds ? 1000 : 1);
    if (!Number.isFinite(start) || !(stepMs > 0)) return null;
    times = cpu.map((_, i) => start + i * stepMs);
  }
  return { times, cpu: cpu.map(num), mem: Array.isArray(mem) ? mem.map(num) : cpu.map(() => null) };
}
