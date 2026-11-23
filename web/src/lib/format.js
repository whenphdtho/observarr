// Small, app-independent formatting helpers shared by every widget.

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

// formatBytes renders n bytes as e.g. "1.5 GiB" (base 1024, one decimal; whole bytes have none).
// A negative or non-finite value (an app can report a nonsensical size) renders as "—".
export function formatBytes(n) {
  if (!Number.isFinite(n) || n < 0) return '—';
  if (n < 1024) return `${Math.round(n)} B`;
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${BYTE_UNITS[unit]}`;
}

// formatPercent renders a 0..1 fraction as a whole percentage, clamped to that range.
export function formatPercent(fraction) {
  if (!Number.isFinite(fraction)) return '—';
  const pct = Math.round(Math.min(1, Math.max(0, fraction)) * 100);
  return `${pct}%`;
}

// formatAge renders how long ago `at` (an ISO timestamp, or anything Date understands) was,
// relative to nowMs (milliseconds since epoch), as e.g. "12 s ago", "4 min ago", "3 h ago".
// Used with a shared ticker so every widget updates together without its own interval.
export function formatAge(at, nowMs) {
  if (!at) return '';
  const then = new Date(at).getTime();
  if (!Number.isFinite(then)) return '';
  const deltaSec = Math.max(0, Math.round((nowMs - then) / 1000));
  if (deltaSec < 1) return 'just now';
  if (deltaSec < 60) return `${deltaSec} s ago`;
  const deltaMin = Math.round(deltaSec / 60);
  if (deltaMin < 60) return `${deltaMin} min ago`;
  const deltaHour = Math.round(deltaMin / 60);
  if (deltaHour < 24) return `${deltaHour} h ago`;
  const deltaDay = Math.round(deltaHour / 24);
  return `${deltaDay} d ago`;
}

// formatRate renders a bytes-per-second figure as e.g. "1.5 MiB/s". A non-finite or negative
// value (no reading yet, or a counter reset) renders as "—".
export function formatRate(bytesPerSec) {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec < 0) return '—';
  return `${formatBytes(bytesPerSec)}/s`;
}

// formatPct renders an already-percent value (0-100, not a 0-1 fraction — a container's CPU can
// exceed 100 with more than one core busy) as a whole percentage. Negative renders as 0%; a
// non-finite value renders as "—".
export function formatPct(pct) {
  if (!Number.isFinite(pct)) return '—';
  return `${Math.round(Math.max(0, pct))}%`;
}

// formatDuration renders a duration in nanoseconds (as Go's time.Duration marshals to JSON) as
// e.g. "3d 4h", "5h 12m", "42m 10s", "8s". A non-finite or negative value renders as "—".
export function formatDuration(ns) {
  if (!Number.isFinite(ns) || ns < 0) return '—';
  let s = Math.floor(ns / 1e9);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

// containerStatusClass maps a container's Docker state and health to one of the shared status dot
// classes ("ok" | "warn" | "fail" | "unknown"), so a card and the System table agree on meaning.
export function containerStatusClass(state, health) {
  if (state === '') return 'unknown';
  if (health === 'unhealthy') return 'fail';
  if (state === 'running') return health === 'starting' ? 'warn' : 'ok';
  if (state === 'restarting' || state === 'paused' || state === 'created') return 'warn';
  return 'fail'; // exited, dead, removing, …
}

// splitNotice splits a notice string on `backtick`-delimited spans, so a view can render each as
// plain text or <code> through ordinary text interpolation, without a raw-HTML directive. Returns
// an array of {kind, text} with kind one of "text" or "code".
export function splitNotice(text) {
  const parts = [];
  const re = /`([^`]+)`/g;
  let last = 0;
  let m;
  while ((m = re.exec(text))) {
    if (m.index > last) parts.push({ kind: 'text', text: text.slice(last, m.index) });
    parts.push({ kind: 'code', text: m[1] });
    last = re.lastIndex;
  }
  if (last < text.length) parts.push({ kind: 'text', text: text.slice(last) });
  return parts;
}
