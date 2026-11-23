<script>
  import { untrack } from 'svelte';
  import Kpi from './Kpi.svelte';
  import MirrorChart from './MirrorChart.svelte';
  import Icon from './icons/Icon.svelte';
  import { api } from './api.js';
  import { snapshot } from './data.js';
  import { group, layers, sum } from './traffic.js';
  import { formatMbit, formatPart, formatTotal, totalParts } from './format.js';

  // Network shows where the traffic goes: the host's rates and today's totals, the traffic of each
  // app (services with their linked containers, and other containers) over five minutes or a day,
  // and the budget. It loads GET /api/network and, for the five minutes, adds each live reading.
  const LIVE_MS = 5 * 60 * 1000;
  const RELOAD_MS = 60 * 1000;

  let range = $state('5m');
  let view = $state(null);
  let error = $state('');

  async function load() {
    const r = range;
    try {
      const v = await api('GET', `/api/network?range=${r}`);
      if (r === range) {
        v.at = r === '5m' ? (v.times[v.times.length - 1] ?? 0) : 0; // the latest reading it holds
        view = v;
        error = '';
      }
    } catch (err) {
      if (r === range) error = err.message;
    }
  }

  $effect(() => {
    range;
    untrack(load);
    const id = setInterval(load, RELOAD_MS);
    return () => clearInterval(id);
  });

  // Each live reading updates the rates now; for the five minutes it also becomes the newest point,
  // and the oldest falls off.
  $effect(() => {
    const snap = $snapshot;
    untrack(() => {
      if (!snap?.host || !view) return;
      const t = new Date(snap.at).getTime();
      if (!(t > (view.at ?? 0))) return;
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
      const now = new Map(snap.containers.map((c) => [c.name, c]));
      for (const c of view.containers) update(c, now.get(c.name)?.netRxRate ?? 0, now.get(c.name)?.netTxRate ?? 0);
      if (live) view.times = [...view.times.slice(drop), t];
    });
  });

  const host = $derived($snapshot?.host ?? null);
  const nowRx = $derived(host ? sum(host.net, 'rx') : (view?.host.rx ?? 0));
  const nowTx = $derived(host ? sum(host.net, 'tx') : (view?.host.tx ?? 0));
  const summary = $derived($snapshot?.traffic ?? view);
  const today = $derived({ rx: summary?.todayRx ?? 0, tx: summary?.todayTx ?? 0 });
  const budget = $derived(summary?.budget ?? null);
  const sources = $derived(view ? group(view.containers) : []);
  const chart = $derived(view ? layers(sources, view.host, today) : []);
  const top = $derived(sources.find((s) => s.rx + s.tx > 0) ?? null);
  const todayTotal = $derived(today.rx + today.tx);
  const otherRow = $derived(chart[chart.length - 1]);
  const rows = $derived([
    ...sources.map((s) => ({ ...s, color: chart.find((c) => c.key === s.key)?.color ?? '' })),
    ...(otherRow && otherRow.todayRx + otherRow.todayTx + otherRow.rx + otherRow.tx > 0 ? [otherRow] : []),
  ]);
  const used = $derived(budget ? Math.min(1, budget.usedBytes / budget.limitBytes) : 0);
  const usedTone = $derived(!budget ? '' : budget.usedBytes >= budget.limitBytes ? 'fail' : used >= 0.8 ? 'warn' : '');
  const period = $derived(budget?.period === 'month' ? 'monthly' : 'daily');

  function sub(s) {
    if (!s.key) return 'not attributed to an app';
    return s.members.length === 1 && s.members[0] === s.label ? 'container' : s.members.join(', ');
  }

  function rates(s) {
    return `↓ ${formatMbit(s.rx)} · ↑ ${formatMbit(s.tx)}`;
  }

  // The gauge: an arc of 240°, open at the bottom; value is 0–1.
  function arc(value) {
    const r = 70;
    const a0 = (150 * Math.PI) / 180;
    const a1 = a0 + ((240 * Math.PI) / 180) * Math.max(0.0001, value);
    const p = (a) => `${(90 + r * Math.cos(a)).toFixed(2)},${(90 + r * Math.sin(a)).toFixed(2)}`;
    return `M${p(a0)}A${r},${r} 0 ${a1 - a0 > Math.PI ? 1 : 0} 1 ${p(a1)}`;
  }
</script>

<div class="network">
  {#if !host && $snapshot}
    <div class="empty">
      <Icon name="network" size={28} />
      <b>No traffic figures</b>
      <span>Traffic is read from the host's /proc: mount it and set OBSERVARR_HOST_PROC (see the System view).</span>
    </div>
  {:else}
    <div class="kpis">
      <Kpi icon="arrow-down" label="Download" value={formatMbit(nowRx)} unit="Mbit/s"
        sub={`peak ${formatMbit(summary?.peakRx ?? 0)} today`} />
      <Kpi icon="arrow-up" label="Upload" value={formatMbit(nowTx)} unit="Mbit/s"
        sub={`peak ${formatMbit(summary?.peakTx ?? 0)} today`} />
      <Kpi icon="gauge" label="Today" value={totalParts(todayTotal)[0]} unit={totalParts(todayTotal)[1]}
        sub={budget?.period === 'day' ? `limit ${formatTotal(budget.limitBytes)}` :
          `↓ ${formatTotal(today.rx)} · ↑ ${formatTotal(today.tx)}`}
        tone={usedTone} />
      <Kpi icon="activity" label="Top now" value={top ? formatMbit(top.rx + top.tx) : '—'} unit={top ? 'Mbit/s' : ''}
        sub={top ? top.label : 'No app traffic now'} />
    </div>

    <section class="card" aria-labelledby="flow-title">
      <div class="card-head">
        <h2 id="flow-title"><Icon name="arrow-down-up" size={16} />Where the traffic goes</h2>
        <div class="seg" role="group" aria-label="Time range">
          <button type="button" aria-pressed={range === '5m'} onclick={() => { range = '5m'; }}>Live · 5 min</button>
          <button type="button" aria-pressed={range === '24h'} onclick={() => { range = '24h'; }}>24 hours</button>
        </div>
      </div>
      {#if error}
        <p class="error" role="alert">{error}</p>
      {:else if !view}
        <p class="waiting pad">Waiting for the first reading…</p>
      {:else}
        <div class="flow">
          <MirrorChart times={view.times} layers={chart}
            label={`Traffic by app over the ${range === '5m' ? 'last five minutes' : 'last day'}, in Mbit/s: incoming above the line, outgoing below; now ${formatMbit(nowRx)} in, ${formatMbit(nowTx)} out`} />
          <ul class="legend" aria-label="Apps">
            <li class="legend-head" aria-hidden="true"><span>App</span><span>Mbit/s now</span></li>
            {#each chart as s (s.key)}
              <li>
                <i class="sw" style:background={s.color}></i>
                <span class="who">
                  <span class="name ellipsis" title={s.label}>{s.label}</span>
                  <span class="muted ellipsis" title={sub(s)}>{sub(s)}</span>
                </span>
                <span class="fig num">
                  <span title={rates(s)}>{rates(s)}</span>
                  <span class="muted">{formatTotal(s.todayRx + s.todayTx)} today</span>
                </span>
              </li>
            {/each}
          </ul>
        </div>
        <p class="note">
          Mbit/s, stacked: above the line what comes in, below what goes out. Other is the part of the host's
          traffic that no app explains. Apps also count traffic between each other, so a stack can briefly
          exceed the host's total.
        </p>
      {/if}
    </section>

    <div class="pair">
      <section class="card" aria-labelledby="apps-title">
        <div class="card-head">
          <h2 id="apps-title"><Icon name="box" size={16} />By app</h2>
          <span class="meta">now · since midnight</span>
        </div>
        {#if rows.length === 0}
          <p class="waiting">No app traffic yet. Containers with a network of their own are counted.</p>
        {:else}
          <div class="table-wrap">
            <table class="data">
              <caption class="visually-hidden">Traffic by app</caption>
              <thead>
                <tr>
                  <th scope="col">App</th>
                  <th scope="col" class="r"><span aria-hidden="true">↓ </span>now</th>
                  <th scope="col" class="r"><span aria-hidden="true">↑ </span>now</th>
                  <th scope="col" class="r"><span aria-hidden="true">↓ </span>today</th>
                  <th scope="col" class="r"><span aria-hidden="true">↑ </span>today</th>
                </tr>
              </thead>
              <tbody>
                {#each rows as s (s.key)}
                  {@const share = todayTotal > 0 ? Math.min(1, (s.todayRx + s.todayTx) / todayTotal) : 0}
                  <tr>
                    <td class="lead" title={s.label}>
                      <span class="app">
                        {#if s.color}<i class="sw" style:background={s.color}></i>{/if}
                        <span class="ellipsis">{s.label}</span>
                      </span>
                      <span class="share" title={`${Math.round(share * 100)}% of today's traffic`}><i style:width={`${share * 100}%`}></i></span>
                    </td>
                    <td class="r" data-label="↓ now">{formatMbit(s.rx)}</td>
                    <td class="r" data-label="↑ now">{formatMbit(s.tx)}</td>
                    <td class="r muted" data-label="↓ today">{formatTotal(s.todayRx)}</td>
                    <td class="r muted" data-label="↑ today">{formatTotal(s.todayTx)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section class="card" aria-labelledby="budget-title">
        <div class="card-head">
          <h2 id="budget-title"><Icon name="gauge" size={16} />{budget ? `${period === 'daily' ? 'Daily' : 'Monthly'} budget` : 'Today'}</h2>
          {#if budget}<span class="meta">{budget.period === 'day' ? 'since midnight' : 'this month'}</span>{/if}
        </div>
        {#if budget}
          <div class="gauge {usedTone}" role="img"
            aria-label={`${Math.round(used * 100)}% of the ${period} budget used: ${formatTotal(budget.usedBytes)} of ${formatTotal(budget.limitBytes)}`}>
            <svg viewBox="0 0 180 150" aria-hidden="true">
              <path class="track" d={arc(1)} />
              <path class="fill" d={arc(used)} />
            </svg>
            <span class="pct num">{Math.round((budget.usedBytes / budget.limitBytes) * 100)}%</span>
            <span class="muted">of {period} budget</span>
          </div>
          <p class="used num">{formatPart(budget.usedBytes, budget.limitBytes, ' of ')} used</p>
        {:else}
          <dl class="totals">
            <div><dt><span aria-hidden="true">↓ </span>Down</dt><dd class="num">{formatTotal(today.rx)}</dd></div>
            <div><dt><span aria-hidden="true">↑ </span>Up</dt><dd class="num">{formatTotal(today.tx)}</dd></div>
            <div class="sum"><dt>Total</dt><dd class="num">{formatTotal(todayTotal)}</dd></div>
          </dl>
          <p class="waiting hint">Set OBSERVARR_TRAFFIC_BUDGET to follow a daily or monthly budget.</p>
        {/if}
      </section>
    </div>
  {/if}
</div>

<style>
  .network { display: grid; gap: 16px; }
  .card-head { flex-wrap: wrap; }
  .pad { padding: 24px 0; }

  .flow { display: grid; grid-template-columns: minmax(0, 1fr) 260px; gap: 24px; align-items: start; }
  .legend { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; min-width: 0; }
  .legend li { display: flex; align-items: flex-start; gap: 10px; padding: 6px 0; min-width: 0; font-size: 13px; }
  .legend .legend-head { font-size: 12px; color: var(--muted); justify-content: space-between; padding-top: 0; }
  .legend .sw { margin-top: 4px; }
  .who { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .who .name { font-weight: 550; }
  .who .muted, .fig .muted { font-size: 12px; }
  .fig { display: grid; justify-items: end; line-height: 1.35; white-space: nowrap; flex: none; }
  .note { margin-top: 14px; font-size: 12.5px; color: var(--muted); }

  .pair { display: grid; gap: 16px; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); align-items: start; }
  .app { display: flex; align-items: center; gap: 8px; min-width: 0; font-weight: 550; }
  .share { display: block; height: 3px; margin-top: 6px; border-radius: 99px; background: var(--surface3); overflow: hidden; max-width: 220px; }
  .share i { display: block; height: 100%; background: var(--accent); border-radius: 99px; }

  .gauge { position: relative; display: grid; justify-items: center; max-width: 200px; margin: 0 auto; }
  .gauge svg { width: 100%; height: auto; }
  .gauge path { fill: none; stroke-width: 12; stroke-linecap: round; }
  .track { stroke: var(--surface3); }
  .fill { stroke: var(--accent); }
  .gauge.warn .fill { stroke: var(--warn); }
  .gauge.fail .fill { stroke: var(--fail); }
  .gauge .pct { position: absolute; top: 38%; font-size: 28px; font-weight: 600; letter-spacing: -0.02em; }
  .gauge .muted { position: absolute; top: 62%; font-size: 12.5px; }
  .used { text-align: center; font-size: 13px; color: var(--text2); margin-top: -6px; }

  .totals { margin: 0; display: grid; }
  .totals div { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-bottom: 1px solid var(--border); font-size: 13px; }
  .totals dt { color: var(--text2); }
  .totals dd { margin: 0; font-weight: 550; white-space: nowrap; }
  .totals .sum { border-bottom: 0; }
  .totals .sum dd { font-size: 15px; font-weight: 600; }
  .hint { margin-top: 10px; font-size: 12.5px; overflow-wrap: anywhere; }

  @container (max-width: 760px) {
    .flow { grid-template-columns: minmax(0, 1fr); }
  }
  @container (max-width: 759px) {
    .pair { grid-template-columns: minmax(0, 1fr); }
  }
  @container (max-width: 759px) {
    .share { max-width: none; }
  }
</style>
