// Network traffic by source. A source is a service with the containers linked to it, or a
// container no service is linked to. Rates are bytes per second, totals bytes.

// PALETTE colours the sources with the most traffic today, in this order; every other source
// counts towards "Other" (OTHER), which is the host's traffic no coloured source explains. The
// colours are the data tokens of app.css.
export const PALETTE = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)', 'var(--series-4)', 'var(--series-5)'];
export const OTHER = 'var(--series-other)';

// add sums two rates of one moment; a moment neither has (null, a gap in the history) stays a gap.
export function add(a, b) {
  const fa = Number.isFinite(a);
  const fb = Number.isFinite(b);
  if (!fa && !fb) return null;
  return (fa ? a : 0) + (fb ? b : 0);
}

// sum adds the numbers of a list of objects under key.
export function sum(list, key) {
  return (list ?? []).reduce((n, x) => n + (Number.isFinite(x[key]) ? x[key] : 0), 0);
}

// links maps each linked container's name to its service's name (the first by name).
export function links(services) {
  const out = new Map();
  for (const s of services) if (s.container && !out.has(s.container.name)) out.set(s.container.name, s.name);
  return out;
}

// group merges containers (GET /api/network's containers) into sources, most traffic today first:
// { key, label, linked, members, rx, tx, todayRx, todayTx, series: { rx, tx } | null }; linked: grouped by service.
export function group(containers) {
  const byKey = new Map();
  for (const c of containers) {
    const key = c.service || c.name;
    let g = byKey.get(key);
    if (!g) {
      g = { key, label: key, linked: false, members: [], rx: 0, tx: 0, todayRx: 0, todayTx: 0, series: null };
      byKey.set(key, g);
    }
    g.linked ||= Boolean(c.service); // grouped under a service, not only a container's name
    g.members.push(c.name);
    g.rx += c.rx;
    g.tx += c.tx;
    g.todayRx += c.todayRx;
    g.todayTx += c.todayTx;
    if (c.series) {
      g.series = g.series
        ? { rx: g.series.rx.map((v, i) => add(v, c.series.rx[i])), tx: g.series.tx.map((v, i) => add(v, c.series.tx[i])) }
        : { rx: [...c.series.rx], tx: [...c.series.tx] };
    }
  }
  return [...byKey.values()].sort((a, b) =>
    b.todayRx + b.todayTx - (a.todayRx + a.todayTx) || b.rx + b.tx - (a.rx + a.tx) || a.label.localeCompare(b.label));
}

// layers returns the coloured sources and "Other" for the chart and its legend. Other is the
// host's traffic minus the coloured sources, never below zero: app traffic between containers
// can make the sources add up to more than the host's. A gap in the host's series stays a gap.
export function layers(sources, host, today) {
  const shown = sources.slice(0, PALETTE.length).map((s, i) => ({ ...s, color: PALETTE[i] }));
  const hostSeries = host.series ?? { rx: [], tx: [] };
  const rest = (key) => hostSeries[key].map((v, i) =>
    (Number.isFinite(v) ? Math.max(0, v - shown.reduce((n, s) => n + (s.series?.[key][i] || 0), 0)) : null));
  const other = {
    key: '', label: 'Other', members: [], color: OTHER,
    rx: Math.max(0, host.rx - sum(shown, 'rx')), tx: Math.max(0, host.tx - sum(shown, 'tx')),
    todayRx: Math.max(0, today.rx - sum(shown, 'todayRx')), todayTx: Math.max(0, today.tx - sum(shown, 'todayTx')),
    series: { rx: rest('rx'), tx: rest('tx') },
  };
  return [...shown, other];
}

// topNow returns the n sources with the most traffic right now from the live snapshot's containers,
// grouped like group(): [{ label, rx, tx }].
export function topNow(containers, linked, n = 3) {
  const byKey = new Map();
  for (const c of containers) {
    if (c.state !== 'running' || !(c.netRxRate + c.netTxRate > 0)) continue;
    const key = linked.get(c.name) || c.name;
    const g = byKey.get(key) ?? { label: key, rx: 0, tx: 0 };
    g.rx += c.netRxRate;
    g.tx += c.netTxRate;
    byKey.set(key, g);
  }
  return [...byKey.values()].sort((a, b) => b.rx + b.tx - (a.rx + a.tx)).slice(0, n);
}
