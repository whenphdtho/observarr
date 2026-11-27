// The one place that turns figures into words, so every view says the same thing the same way: rates in Mbit/s,
// sizes in GB and TB, spans of time in two units ("2 min 21 s"), ages in one ("3 days ago"), percentages without
// a space. Nothing here knows an app.

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

// formatPart renders a part of a whole, e.g. "144 of 1400 GB", naming the unit once when both share it.
export function formatPart(part, whole) {
  const [p, pu] = sizeParts(part);
  const [w, wu] = sizeParts(whole);
  return `${p}${pu === wu ? '' : ` ${pu}`} of ${w} ${wu}`;
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

// formatChange renders a relative change (0.12 = 12% more) as "+12%" or "−4%"; under half a percent is "0%".
export function formatChange(ratio) {
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

// formatUptime renders a duration in nanoseconds (as Go's time.Duration marshals to JSON), e.g. "12 days 4 h".
export function formatUptime(ns) {
  return formatSpan(ns / 1e9);
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
// (ms since epoch), in one unit: "just now", "12 s ago", "4 min ago", "3 h ago", "3 days ago". Used with the
// shared ticker, so every widget updates together.
export function formatAge(at, nowMs) {
  if (!at) return '';
  const then = new Date(at).getTime();
  if (!Number.isFinite(then)) return '';
  const sec = Math.max(0, Math.round((nowMs - then) / 1000));
  if (sec < 1) return 'just now';
  if (sec < 60) return `${sec} s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min} min ago`;
  const hours = Math.round(min / 60);
  if (hours < 24) return `${hours} h ago`;
  return `${days(Math.round(hours / 24))} ago`;
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

// prettyRelease turns a release name into a title for reading: "Big.Buck.Bunny.2008.2160p.WEB" becomes
// "Big Buck Bunny (2008) · 2160p", "Caminandes.S01E03.Llamigos.1080p.WEB" becomes "Caminandes S01E03 · 1080p".
// A name without a year, an episode or a resolution after its title is not a release name and is returned as it
// is; the caller keeps the original in the title attribute.
export function prettyRelease(name) {
  const tokens = (name ?? '').split(/[._\s]+/).filter(Boolean);
  const cut = tokens.findIndex((t, i) => i > 0 && (EPISODE.test(t) || RESOLUTION.test(t)));
  // The year is the last one before the episode or resolution: a title may hold a year of its own.
  const head = cut < 0 ? tokens : tokens.slice(0, cut);
  let year = -1;
  for (let i = head.length - 1; i > 0 && year < 0; i--) if (YEAR.test(head[i])) year = i;
  const end = year >= 0 ? year : cut;
  if (end <= 0) return name ?? '';
  let out = tokens.slice(0, end).join(' ');
  if (year >= 0) out += ` (${tokens[year]})`;
  if (cut >= 0 && EPISODE.test(tokens[cut])) out += ` ${tokens[cut].toUpperCase()}`;
  const res = tokens.slice(Math.max(cut, 0)).find((t) => RESOLUTION.test(t));
  return res ? `${out} · ${res}` : out;
}

// stripSource drops an app's check name from the front of its message for display, "IndexerSearchCheck: No
// indexers…" becoming "No indexers…": the name of a check means nothing to a reader. Only a single word in
// CamelCase counts as one; the full text stays available to whoever opens the details.
export function stripSource(message) {
  return (message ?? '').replace(/^[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+:\s+/, '');
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
