// The one place that turns figures into words, so every view says the same thing the same way: rates in Mbit/s,
// sizes in GB and TB, spans of time in two units ("2 min 21 s"), ages in one ("3 days ago"), percentages without
// a space. Nothing here knows an app.
//
// Times of day and dates follow the browser's locale (formatClock, formatTime, the charts' labels); numbers are
// written one way everywhere, with a point for decimals and commas for thousands.
//
// The one exception to Mbit/s: what a disk reads and writes is in MB/s (formatDisk), as every disk tool says it.
// Mbit/s is for what travels over a network: traffic, a download, a stream, a transfer to a remote.

// formatMbit renders a bytes-per-second rate as a number of Mbit/s without the unit: one decimal below 10, whole
// numbers above, "<0.1" for a trickle. A missing or negative rate renders as "—".
export function formatMbit(bytesPerSec) {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec < 0) return '—';
  const v = (bytesPerSec * 8) / 1e6;
  if (v === 0) return '0';
  if (v < 0.05) return '<0.1';
  return v < 9.95 ? v.toFixed(1) : String(Math.round(v));
}

// formatRate renders a bytes-per-second rate with its unit, e.g. "304 Mbit/s": a download, a transfer and the
// host's traffic all read alike.
export function formatRate(bytesPerSec) {
  const v = formatMbit(bytesPerSec);
  return v === '—' ? v : `${v} Mbit/s`;
}

// formatDisk renders a disk's bytes-per-second rate as a number of MB/s without the unit: one decimal below 10,
// whole numbers above, "<0.1" for a trickle. It is for disk reads and writes only; see the rule above.
export function formatDisk(bytesPerSec) {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec < 0) return '—';
  const v = bytesPerSec / 1e6;
  if (v === 0) return '0';
  if (v < 0.05) return '<0.1';
  return v < 9.95 ? v.toFixed(1) : String(Math.round(v));
}

// sizeParts splits a number of bytes into [number, unit] in decimal GB, or TB from 1000 GB: two decimals below 1,
// one below 10, whole numbers above; "<0.01" for a few megabytes.
export function sizeParts(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return ['—', ''];
  let v = bytes / 1e9;
  let unit = 'GB';
  if (v >= 999.5) {
    v /= 1000;
    unit = 'TB';
  }
  if (v === 0) return ['0', unit];
  if (v < 0.005) return ['<0.01', unit];
  return [v < 0.995 ? v.toFixed(2) : v < 9.95 ? v.toFixed(1) : String(Math.round(v)), unit];
}

// formatSize renders a number of bytes as e.g. "144 GB" or "1.3 TB".
export function formatSize(bytes) {
  return sizeParts(bytes).join(' ').trim();
}

// formatPart renders a part of a whole in the whole's unit, named once: "6.4 of 17 GB", "464 of 1,400 GB" (a
// whole under 10 TB stays in GB, so a part of it reads on the same scale), "49 of 60 TB".
export function formatPart(part, whole) {
  if (!Number.isFinite(part) || !Number.isFinite(whole) || part < 0 || whole < 0) return '—';
  const tb = whole >= 10e12;
  const fig = (bytes) => {
    const v = bytes / (tb ? 1e12 : 1e9);
    if (v === 0) return '0';
    if (v < 0.005) return '<0.01';
    return v < 0.995 ? v.toFixed(2) : v < 9.95 ? v.toFixed(1) : Math.round(v).toLocaleString('en-US');
  };
  return `${fig(part)} of ${fig(whole)} ${tb ? 'TB' : 'GB'}`;
}

// formatPercent renders a 0..1 fraction as a whole percentage, clamped to that range.
export function formatPercent(fraction) {
  if (!Number.isFinite(fraction)) return '—';
  return `${Math.round(Math.min(1, Math.max(0, fraction)) * 100)}%`;
}

// formatPct renders an already-percent value (0-100, not a 0-1 fraction: a container's CPU can exceed 100 with
// more than one core busy) as a whole percentage. Negative renders as 0%; a non-finite value as "—".
export function formatPct(pct) {
  if (!Number.isFinite(pct)) return '—';
  return `${Math.round(Math.max(0, pct))}%`;
}

// formatChange renders a relative change (0.12 = 12% more) as "+12%" or "−4%"; under half a percent is "0%", and
// one that is not known "—".
export function formatChange(ratio) {
  if (!Number.isFinite(ratio)) return '—';
  const pct = Math.abs(ratio) * 100;
  if (pct < 0.5) return '0%';
  return `${ratio > 0 ? '+' : '−'}${pct > 999 ? '>999' : Math.round(pct)}%`;
}

// formatCount renders a whole number with thousands separators, and "+" when it is a lower bound.
export function formatCount(n, capped = false) {
  if (!Number.isFinite(n)) return '—';
  return `${n.toLocaleString('en-US')}${capped ? '+' : ''}`;
}

const days = (n) => `${n} day${n === 1 ? '' : 's'}`;

// formatSpan renders a span of seconds in its two largest units: "45 s", "2 min 21 s", "1 h 5 min", "12 days 4 h".
// A unit that is zero is left out ("2 min"). Time left, an uptime and a lockout all read this way.
export function formatSpan(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  const s = Math.round(seconds);
  const pair = (a, b) => (b ? `${a} ${b}` : a);
  if (s < 60) return `${s} s`;
  if (s < 3600) return pair(`${Math.floor(s / 60)} min`, s % 60 ? `${s % 60} s` : '');
  if (s < 86400) return pair(`${Math.floor(s / 3600)} h`, s % 3600 >= 60 ? `${Math.floor((s % 3600) / 60)} min` : '');
  return pair(days(Math.floor(s / 86400)), s % 86400 >= 3600 ? `${Math.floor((s % 86400) / 3600)} h` : '');
}

// formatUptime renders a duration in nanoseconds (as Go's time.Duration marshals to JSON), e.g. "12 days 4 h";
// a missing one renders as "—".
export function formatUptime(ns) {
  return typeof ns === 'number' ? formatSpan(ns / 1e9) : '—';
}

// clockSeconds reads a time left as apps give it, "2:21", "00:02:21" or "1.02:03:04" (days first), into seconds;
// null for anything else.
export function clockSeconds(text) {
  const m = /^(?:(\d+)[.:])?(\d{1,2}):(\d{2}):(\d{2})$/.exec(text ?? '') ?? /^()()(\d{1,2}):(\d{2})$/.exec(text ?? '');
  if (!m) return null;
  return (Number(m[1]) || 0) * 86400 + (Number(m[2]) || 0) * 3600 + Number(m[3]) * 60 + Number(m[4]);
}

// formatLeft renders an app's time left as "2 min 21 s left"; what cannot be read as a clock is shown as given,
// and none or zero as "".
export function formatLeft(text) {
  const s = clockSeconds(text);
  if (s === null) return text || '';
  return s > 0 ? `${formatSpan(s)} left` : '';
}

// formatAge renders how long ago `at` (an ISO timestamp, or anything Date understands) was, relative to nowMs
// (ms since epoch), in one unit: "just now", "12 s ago", "4 min ago", "3 h ago", "3 days ago". The unit is counted
// in whole ones that have passed, never rounded up: 36 hours ago is "1 day ago", 119 seconds "1 min ago". Used
// with the shared ticker, so every widget updates together.
export function formatAge(at, nowMs) {
  if (!at) return '';
  const then = new Date(at).getTime();
  if (!Number.isFinite(then)) return '';
  const sec = Math.max(0, Math.floor((nowMs - then) / 1000));
  if (sec < 1) return 'just now';
  if (sec < 60) return `${sec} s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`;
  return `${days(Math.floor(sec / 86400))} ago`;
}

// formatUntil renders the time left until at (an ISO time) as "in 12 min", or "" if at is unset; a past time
// reads "ended".
export function formatUntil(at, nowMs) {
  if (!at) return '';
  const t = new Date(at).getTime();
  if (!Number.isFinite(t)) return '';
  const left = (t - nowMs) / 1000;
  return left > 0 ? `in ${formatSpan(left)}` : 'ended';
}

// formatClock renders a time (ms since epoch, or anything Date understands) as the time of day, "14:30" or
// "2:30 PM" as the browser's locale has it.
export function formatClock(at) {
  return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// formatTime renders an ISO time as a short local date and time, for a title.
export function formatTime(at) {
  if (!at) return '';
  const d = new Date(at);
  return Number.isFinite(d.getTime()) ? d.toLocaleString() : '';
}

// sentence raises the first letter of an app's status word or message: "queued" reads "Queued".
export function sentence(text) {
  return text ? text[0].toUpperCase() + text.slice(1) : text;
}

// plural is "1 thing" or "n things".
export function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

const YEAR = /^(19|20)\d{2}$/;
const EPISODE = /^S\d{1,2}E\d{1,3}$/i;
const RESOLUTION = /^(\d{3,4}p|4k|uhd)$/i;
const SOURCE = /^(web|web-?dl|webrip|bluray|bdrip|brrip|hdtv|dvdrip|remux|hdrip)$/i;

// prettyRelease turns a release name into a title for reading: "Big.Buck.Bunny.2008.2160p.WEB" becomes
// "Big Buck Bunny (2008) · 2160p", "Caminandes.S01E03.Llamigos.1080p.WEB" becomes "Caminandes S01E03 · 1080p".
// Only a release name is touched: words joined by dots or underscores, without a space, that carry a resolution,
// an episode or a source after the title. A title with a year in it ("Blade Runner 2049"), a date ("01.05.2025")
// or a file name ("backup.2024.tar") is returned as it is; the caller keeps the original in the title attribute.
export function prettyRelease(name) {
  if (!name || /\s/.test(name) || !/[._]/.test(name)) return name ?? '';
  const tokens = name.split(/[._]+/).filter(Boolean);
  // The title ends at the first episode, resolution or source; without any, it is no release name.
  const cut = tokens.findIndex((t, i) => i > 0 && (EPISODE.test(t) || RESOLUTION.test(t) || SOURCE.test(t)));
  if (cut < 0) return name;
  // The year is the last one before that: a title may hold a year of its own.
  let year = -1;
  for (let i = cut - 1; i > 0 && year < 0; i--) if (YEAR.test(tokens[i])) year = i;
  let out = tokens.slice(0, year >= 0 ? year : cut).join(' ');
  if (year >= 0) out += ` (${tokens[year]})`;
  if (EPISODE.test(tokens[cut])) out += ` ${tokens[cut].toUpperCase()}`;
  const res = tokens.slice(cut).find((t) => RESOLUTION.test(t));
  return res ? `${out} · ${res}` : out;
}

// containerStatusClass maps a container's Docker state and health to one of the shared status dot classes
// ("ok" | "warn" | "fail" | "unknown"), so a card and the System table agree on meaning.
export function containerStatusClass(state, health) {
  if (state === '') return 'unknown';
  if (health === 'unhealthy') return 'fail';
  if (state === 'running') return health === 'starting' ? 'warn' : 'ok';
  if (state === 'restarting' || state === 'paused' || state === 'created') return 'warn';
  return 'fail'; // exited, dead, removing, …
}

// splitNotice splits a notice string on `backtick`-delimited spans, so a view can render each as plain text or
// <code> through ordinary text interpolation, without a raw-HTML directive. Returns an array of {kind, text}
// with kind one of "text" or "code".
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
