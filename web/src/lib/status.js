import { containerStatusClass } from './format.js';

// Summaries of a service's readings shared by the apps rail, the Overview and the cards. They
// only read the generic shapes (health, queue), never anything app-specific.

function warnings(svc) {
  return svc.readings?.health?.data?.warnings ?? [];
}

function queueItems(svc) {
  return svc.readings?.queue?.data?.items ?? [];
}

export function downloading(svc) {
  return queueItems(svc).filter((it) => it.status === 'downloading').length;
}

// serviceStatus is the service's overall state as a status class: "unknown" before the first
// health check, "fail" when down, "warn" when up with warnings or a stuck queue item, else "ok".
export function serviceStatus(svc) {
  const health = svc.readings?.health;
  if (!health) return 'unknown';
  if (!svc.up) return 'fail';
  if (warnings(svc).some((w) => w.level !== 'info') || queueItems(svc).some((it) => it.problem)) return 'warn';
  return 'ok';
}

// serviceLine is the one-line state shown under a service's name: { text, detail, tone }, where
// detail is the part worth highlighting in the tone's colour.
export function serviceLine(svc) {
  const health = svc.readings?.health;
  if (!health) return { text: 'Checking…', detail: '', tone: '' };
  if (!svc.up) return { text: 'Down', detail: health.error || '', tone: 'fail' };
  const n = downloading(svc);
  if (n > 0) return { text: 'Up', detail: `${n} downloading`, tone: 'ok' };
  const w = warnings(svc).filter((x) => x.level !== 'info').length;
  if (w > 0) return { text: 'Up', detail: `${w} warning${w === 1 ? '' : 's'}`, tone: 'warn' };
  const version = health.data?.version;
  return { text: 'Up', detail: version ? `v${version}` : '', tone: '' };
}

// cardSummary is what a collapsed service card shows: one summary line ({ text, tone }) and one
// lead row — the first queue item, else the first warning, else a placeholder — so every
// collapsed card has the same rows whatever the service reports.
export function cardSummary(svc) {
  const health = svc.readings?.health;
  const queue = svc.readings?.queue;
  const hasQueue = (svc.kinds ?? []).includes('queue');
  const items = queueItems(svc);
  const warns = warnings(svc).filter((w) => w.level !== 'info');

  let summary;
  if (!health) summary = { text: 'Waiting for the first check…', tone: '' };
  else if (!svc.up) summary = { text: health.error || 'Not answering', tone: 'fail' };
  else {
    const parts = [];
    const n = downloading(svc);
    const waiting = items.length - n;
    if (n > 0) parts.push(`${n} downloading`);
    if (waiting > 0) parts.push(`${waiting} queued`);
    if (warns.length > 0) parts.push(`${warns.length} warning${warns.length === 1 ? '' : 's'}`);
    if (parts.length) summary = { text: parts.join(' · '), tone: warns.length || items.some((it) => it.problem) ? 'warn' : '' };
    else summary = { text: hasQueue ? 'No downloads' : 'No warnings', tone: '' };
  }

  let lead;
  if (items.length > 0) lead = { kind: 'item', item: items[0] };
  else if (warns.length > 0) lead = { kind: 'warning', warning: warns[0] };
  else if (hasQueue && queue?.error) lead = { kind: 'note', text: queue.error, tone: 'fail' };
  else if (hasQueue) lead = { kind: 'note', text: 'Queue is empty', tone: '' };
  else if (health?.data) lead = { kind: 'note', text: `Answers in ${health.data.latencyMs} ms`, tone: '' };
  else lead = { kind: 'note', text: 'Nothing to report', tone: '' };
  return { summary, lead };
}

// attention lists what needs a look, worst first: services down, health warnings and errors,
// stuck queue items, and containers that are unhealthy, restarting or dead. Each entry is
// { key, tone, title, detail, href }.
export function attention(services, containers) {
  const out = [];
  for (const svc of services) {
    const health = svc.readings?.health;
    if (health && !svc.up) {
      out.push({ key: `down-${svc.id}`, tone: 'fail', title: `${svc.name} is down`, detail: health.error || '', href: '#/apps' });
    }
    for (const [i, w] of warnings(svc).entries()) {
      if (w.level === 'info') continue;
      out.push({
        key: `warn-${svc.id}-${i}`, tone: w.level === 'error' ? 'fail' : 'warn', title: svc.name,
        detail: w.source ? `${w.source}: ${w.message}` : w.message, href: '#/apps',
      });
    }
    for (const [i, it] of queueItems(svc).entries()) {
      if (!it.problem) continue;
      out.push({ key: `queue-${svc.id}-${i}`, tone: 'warn', title: `${svc.name}: ${it.title}`, detail: it.problem, href: '#/apps' });
    }
  }
  for (const c of containers) {
    const bad = c.health === 'unhealthy' || c.state === 'restarting' || c.state === 'dead';
    if (!bad) continue;
    out.push({
      key: `ctr-${c.id}`, tone: containerStatusClass(c.state, c.health), title: `Container ${c.name}`,
      detail: c.health === 'unhealthy' ? 'unhealthy' : c.state, href: '#/system',
    });
  }
  const rank = { fail: 0, warn: 1 };
  return out.sort((a, b) => (rank[a.tone] ?? 2) - (rank[b.tone] ?? 2));
}

// matches reports whether text contains the search query, ignoring case; an empty query matches.
export function matches(query, ...texts) {
  const q = query.trim().toLowerCase();
  return !q || texts.some((t) => (t ?? '').toLowerCase().includes(q));
}
