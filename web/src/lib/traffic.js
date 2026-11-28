// Network traffic by source. A source is a service with the containers linked to it, or a
// container no service is linked to. Rates are bytes per second, totals bytes.

// PALETTE colours the sources with the most traffic, in this order: one hue in three strengths. Every other
// source counts towards "Other" (OTHER), which is the host's traffic no coloured source explains. The colours
// are the data tokens of app.css.
export const PALETTE = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)'];
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

// advance takes a live reading (the system snapshot) into view, a GET /api/network answer: the rates now of the
// host and of each container, and, for the five minutes, the reading as the newest point while the oldest falls
// off. view.at is the latest reading it holds; an older or equal one changes nothing.
const LIVE_MS = 5 * 60 * 1000;
export function advance(view, snap) {
  const t = new Date(snap.at).getTime();
  if (!snap.host || !(t > (view.at ?? 0))) return;
  view.at = t;
  const live = view.range === '5m';
  const drop = live ? view.times.filter((x) => x < t - LIVE_MS).length : 0;
  const update = (flow, rx, tx) => {
    flow.rx = rx;
    flow.tx = tx;
    if (!live || (!flow.series && !(rx || tx))) return;
    flow.series ??= { rx: view.times.map(() => 0), tx: view.times.map(() => 0) };
    flow.series.rx = [...flow.series.rx.slice(drop), rx];
    flow.series.tx = [...flow.series.tx.slice(drop), tx];
  };
  update(view.host, sum(snap.host.net, 'rx'), sum(snap.host.net, 'tx'));
  const now = new Map((snap.containers ?? []).map((c) => [c.name, c]));
  for (const c of view.containers) update(c, now.get(c.name)?.netRxRate ?? 0, now.get(c.name)?.netTxRate ?? 0);
  if (live) view.times = [...view.times.slice(drop), t];
}

// What an app's traffic is for, read from the shapes it fills, first match first: the Overview's chart draws
// these instead of each app, so it stays three lines however many apps there are.
const PURPOSES = [
  { shape: 'queue', label: 'Downloads', color: 'var(--series-2)' },
  { shape: 'streams', label: 'Streams', color: 'var(--series-1)' },
  { shape: 'mounts', label: 'Cloud', color: 'var(--series-3)' },
];

// byPurpose turns a GET /api/network answer into the series of the Overview's chart, in Mbit/s, in and out
// together: one per purpose that an app with a linked container serves, and "Other" for the rest of the host's
// traffic. kindsOf maps a service's name to the shapes it fills. Without any such app it is the host's traffic
// in and out. Each is { label, color, values }, values null for a gap.
export function byPurpose(view, kindsOf) {
  const mbit = (v) => (Number.isFinite(v) ? (v * 8) / 1e6 : null);
  const host = view.host.series ?? { rx: [], tx: [] };
  const total = host.rx.map((v, i) => add(v, host.tx[i]));
  const found = PURPOSES.map((p) => ({ ...p, values: null }));
  for (const c of view.containers) {
    const kinds = kindsOf.get(c.service) ?? [];
    const p = found.find((f) => kinds.includes(f.shape));
    if (!p || !c.series) continue;
    const both = c.series.rx.map((v, i) => add(v, c.series.tx[i]));
    p.values = p.values ? p.values.map((v, i) => add(v, both[i])) : both;
  }
  const shown = found.filter((p) => p.values);
  if (shown.length === 0) {
    return [
      { label: 'In', color: 'var(--series-1)', values: host.rx.map(mbit) },
      { label: 'Out', color: 'var(--series-3)', values: host.tx.map(mbit) },
    ];
  }
  const rest = total.map((v, i) => (Number.isFinite(v) ? Math.max(0, v - shown.reduce((n, p) => n + (p.values[i] || 0), 0)) : null));
  return [
    ...shown.map((p) => ({ label: p.label, color: p.color, values: p.values.map(mbit) })),
    ...(rest.some((v) => v > 0) ? [{ label: 'Other', color: OTHER, values: rest.map(mbit) }] : []),
  ];
}
