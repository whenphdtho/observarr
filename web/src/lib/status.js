import { streamsOf, streamText } from './streams.js';
import { formatCount, plural, sentence } from './format.js';

// Summaries of a service's readings shared by the apps rail, the Overview and the cards. They only read the
// generic shapes (health, queue, streams, …) and the server's attention list, never anything app-specific.

function warnings(svc) {
  return svc.readings?.health?.data?.warnings ?? [];
}

function queueItems(svc) {
  return svc.readings?.queue?.data?.items ?? [];
}

export function downloading(svc) {
  return queueItems(svc).filter((it) => it.status === 'downloading').length;
}

// queuePaused tells whether a downloader has its whole queue paused: a state, not a problem.
export function queuePaused(svc) {
  return !!svc.readings?.queue?.data?.paused;
}

// tone maps a severity of the attention list to a status class: "fail", "warn", or "" for info.
export function tone(severity) {
  return severity === 'critical' ? 'fail' : severity === 'warning' ? 'warn' : '';
}

// worst is the items of the attention list about service id that carry its worst tone, with that tone:
// { tone: "fail" | "warn" | "", items }.
function worst(id, items) {
  const mine = (items ?? []).filter((it) => it.service === id);
  for (const t of ['fail', 'warn']) {
    const hit = mine.filter((it) => tone(it.severity) === t);
    if (hit.length) return { tone: t, items: hit };
  }
  return { tone: '', items: [] };
}

// serviceStatus is the service's overall state as a status class: "unknown" before the first health check (or the
// first attention list), "fail" when it is not reachable, else the worst of what needs attention about it ("fail"
// or "warn"), else "ok". The severities are the server's, the same the Overview, the bell and the alerts use.
export function serviceStatus(svc, items) {
  if (!svc.readings?.health) return 'unknown';
  if (!svc.up) return 'fail';
  if (!items) return 'unknown'; // what it reports is not weighed yet
  return worst(svc.id, items).tone || 'ok';
}

// serviceLine is the one state shown under a service's name, and the colour of its dot: { text, tone }, tone
// one of "fail", "warn", "idle" (waiting or paused: nothing wrong, nothing moving), "ok" or "unknown". What needs
// someone comes first, then what it waits for, then what it does.
export function serviceLine(svc, items) {
  if (!svc.readings?.health) return { text: 'Checking…', tone: 'unknown' };
  if (!svc.up) return { text: 'Not reachable', tone: 'fail' };
  if (!items) return { text: 'Checking…', tone: 'unknown' };
  const w = worst(svc.id, items);
  if (w.tone) {
    // One short problem is said in its own words; several, or a long one, are counted.
    const only = w.items.length === 1 ? w.items[0] : null;
    const stuck = w.items.every((it) => it.rule === 'downloads');
    const text = stuck ? `${plural(w.items.length, 'download')} stuck` :
      only && only.message.length <= 24 ? sentence(only.message) :
        plural(w.items.length, w.tone === 'fail' ? 'problem' : 'warning');
    return { text, tone: w.tone };
  }
  if (queuePaused(svc)) return { text: 'Paused', tone: 'idle' };
  const pending = svc.readings?.requests?.data?.counts?.pending ?? 0;
  if (pending > 0) return { text: `${pending} waiting`, tone: 'idle' };
  const playing = streamsOf(svc);
  if (playing?.total > 0) return { text: `${playing.total} playing`, tone: 'ok' };
  const n = downloading(svc);
  if (n > 0) return { text: `${n} downloading`, tone: 'ok' };
  const blocked = svc.readings?.security?.data;
  if (blocked) return { text: `${formatCount(blocked.total, blocked.capped)} blocked`, tone: 'ok' };
  return { text: 'Fine', tone: 'ok' };
}

// cardSummary is what a collapsed service card shows: one summary line ({ text, tone }) and one lead row (the
// first queue item, else the first stream, else the first warning, else a placeholder), so every collapsed card
// has the same rows whatever the service reports. A paused queue leads its summary with "Paused" and is no
// warning. A service that is not reachable gets a short summary and its error, said once, in the lead row.
// attention is the attention list.
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
    if (queuePaused(svc)) parts.push('Paused');
    if (n > 0) parts.push(`${n} downloading`);
    if (waiting > 0) parts.push(`${waiting} queued`);
    if (streams?.total > 0) parts.push(streamText(streams));
    if (warns.length > 0) parts.push(plural(warns.length, 'warning'));
    if (parts.length) summary = { text: parts.join(' · '), tone: worst(svc.id, attention).tone };
    else summary = { text: hasQueue ? 'No downloads' : hasStreams ? 'Nothing playing' : 'No warnings', tone: '' };
  }

  let lead;
  if (health && !svc.up) lead = { kind: 'note', text: health.error || 'It does not answer', tone: 'fail' };
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
