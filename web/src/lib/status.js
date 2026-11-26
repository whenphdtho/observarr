import { streamsOf, streamText } from './streams.js';

// Summaries of a service's readings shared by the apps rail, the Overview and the cards. They
// only read the generic shapes (health, queue, streams), never anything app-specific.

function warnings(svc) {
  return svc.readings?.health?.data?.warnings ?? [];
}

function queueItems(svc) {
  return svc.readings?.queue?.data?.items ?? [];
}

export function downloading(svc) {
  return queueItems(svc).filter((it) => it.status === 'downloading').length;
}

// tone maps a severity of the attention list to a status class: "fail", "warn", or "" for info.
export function tone(severity) {
  return severity === 'critical' ? 'fail' : severity === 'warning' ? 'warn' : '';
}

// worstOf is the status class of the worst attention item about service id (items is the attention list, or
// null before it arrived): "fail", "warn" or "".
function worstOf(id, items) {
  let worst = '';
  for (const it of items ?? []) {
    if (it.service !== id) continue;
    const t = tone(it.severity);
    if (t === 'fail') return t;
    worst ||= t;
  }
  return worst;
}

// serviceStatus is the service's overall state as a status class: "unknown" before the first health check (or the
// first attention list), "fail" when down, else the worst of what needs attention about it ("fail" or "warn"), else "ok". The severities are the
// server's, the same the Overview, the bell and the alerts use.
export function serviceStatus(svc, items) {
  if (!svc.readings?.health) return 'unknown';
  if (!svc.up) return 'fail';
  if (!items) return 'unknown'; // what it reports is not weighed yet
  return worstOf(svc.id, items) || 'ok';
}

// serviceLine is the one-line state shown under a service's name: { text, detail, tone }, where
// detail is the part worth highlighting in the tone's colour. items is the attention list.
export function serviceLine(svc, items) {
  const health = svc.readings?.health;
  if (!health) return { text: 'Checking…', detail: '', tone: '' };
  if (!svc.up) return { text: 'Down', detail: health.error || '', tone: 'fail' };
  // A warning or a stuck download is why the status dot is coloured, so it is named before the downloads.
  const worst = worstOf(svc.id, items);
  const w = warnings(svc).filter((x) => x.level !== 'info').length;
  if (w > 0) return { text: 'Up', detail: `${w} warning${w === 1 ? '' : 's'}`, tone: worst || 'warn' };
  const stuck = queueItems(svc).filter((it) => it.problem).length;
  if (stuck > 0) return { text: 'Up', detail: `${stuck} download${stuck === 1 ? '' : 's'} stuck`, tone: worst || 'warn' };
  const n = downloading(svc);
  if (n > 0) return { text: 'Up', detail: `${n} downloading`, tone: 'ok' };
  const playing = streamsOf(svc);
  if (playing?.total > 0) return { text: 'Up', detail: `${playing.total} stream${playing.total === 1 ? '' : 's'}`, tone: 'ok' };
  const version = health.data?.version;
  return { text: 'Up', detail: version ? `v${version}` : '', tone: '' };
}

// cardSummary is what a collapsed service card shows: one summary line ({ text, tone }) and one
// lead row — the first queue item, else the first warning, else a placeholder — so every
// collapsed card has the same rows whatever the service reports. A service that is down gets a
// short summary and its error, said once, in the lead row. attention is the attention list.
export function cardSummary(svc, attention) {
  const health = svc.readings?.health;
  const queue = svc.readings?.queue;
  const hasQueue = (svc.kinds ?? []).includes('queue');
  const hasStreams = (svc.kinds ?? []).includes('streams');
  const items = queueItems(svc);
  const streams = streamsOf(svc);
  const warns = warnings(svc).filter((w) => w.level !== 'info');

  let summary;
  if (!health) summary = { text: 'Waiting for the first check…', tone: '' };
  else if (!svc.up) summary = { text: 'Not reachable', tone: 'fail' };
  else {
    const parts = [];
    const n = downloading(svc);
    const waiting = items.length - n;
    if (n > 0) parts.push(`${n} downloading`);
    if (waiting > 0) parts.push(`${waiting} queued`);
    if (streams?.total > 0) parts.push(streamText(streams));
    if (warns.length > 0) parts.push(`${warns.length} warning${warns.length === 1 ? '' : 's'}`);
    if (parts.length) summary = { text: parts.join(' · '), tone: worstOf(svc.id, attention) };
    else summary = { text: hasQueue ? 'No downloads' : hasStreams ? 'Nothing playing' : 'No warnings', tone: '' };
  }

  let lead;
  if (health && !svc.up) lead = { kind: 'note', text: health.error || 'Not answering', tone: 'fail' };
  else if (items.length > 0) lead = { kind: 'item', item: items[0] };
  else if (streams?.streams.length > 0) lead = { kind: 'item', item: streams.streams[0] };
  else if (warns.length > 0) lead = { kind: 'warning', warning: warns[0] };
  else if (hasQueue && queue?.error) lead = { kind: 'note', text: queue.error, tone: 'fail' };
  else if (hasQueue) lead = { kind: 'note', text: 'Queue is empty', tone: '' };
  else if (hasStreams && svc.readings?.streams?.error) lead = { kind: 'note', text: svc.readings.streams.error, tone: 'fail' };
  else if (health?.data) lead = { kind: 'note', text: `Answers in ${health.data.latencyMs} ms`, tone: '' };
  else lead = { kind: 'note', text: 'Nothing to report', tone: '' };
  return { summary, lead };
}

// matches reports whether text contains the search query, ignoring case; an empty query matches.
export function matches(query, ...texts) {
  const q = query.trim().toLowerCase();
  return !q || texts.some((t) => (t ?? '').toLowerCase().includes(q));
}
