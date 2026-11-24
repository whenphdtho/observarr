<script>
  import AreaChart from './AreaChart.svelte';
  import { history, snapshot } from './data.js';
  import { serviceList } from './live.js';
  import { formatMbit, formatPct } from './format.js';
  import { links, sum, topNow } from './traffic.js';

  // HostChart is the host's network throughput (or its CPU) over the last five minutes, from the
  // shared history (GET /api/system/history, then every live reading), with the apps that move the
  // most traffic right now and today's peak.
  let mode = $state('net');

  const mbit = (v) => (v * 8) / 1e6;
  const times = $derived($history.map((h) => new Date(h.at).getTime()));
  const latest = $derived($history[$history.length - 1] ?? null);
  const hasNet = $derived(!!latest?.net?.length);
  const showNet = $derived(hasNet && mode === 'net');
  const rx = $derived(latest ? sum(latest.net, 'rx') : 0);
  const tx = $derived(latest ? sum(latest.net, 'tx') : 0);
  const traffic = $derived($snapshot?.traffic ?? null);
  const top = $derived(topNow($snapshot?.containers ?? [], links($serviceList)));

  function tick(v) {
    return v < 10 && v !== Math.round(v) ? v.toFixed(1) : String(Math.round(v));
  }
</script>

<section class="section" aria-labelledby="host-chart-title">
  <div class="card-head">
    <h2 id="host-chart-title">{showNet ? 'Throughput' : 'Host activity'}</h2>
    {#if hasNet}
      <div class="seg" role="group" aria-label="Chart">
        <button type="button" aria-pressed={mode === 'net'} onclick={() => { mode = 'net'; }}>Network</button>
        <button type="button" aria-pressed={mode === 'cpu'} onclick={() => { mode = 'cpu'; }}>CPU</button>
      </div>
    {/if}
  </div>

  {#if !latest}
    <p class="waiting pad">{$snapshot && !$snapshot.host ?
      'No host figures: they need the host’s /proc (see the System view).' : 'Waiting for the first reading…'}</p>
  {:else if showNet}
    <div class="nums">
      <div class="num-block">
        <span class="legend"><i class="sw down"></i>Download</span>
        <span class="big num">{formatMbit(rx)}<small>Mbit/s</small></span>
      </div>
      <div class="num-block">
        <span class="legend"><i class="sw up"></i>Upload</span>
        <span class="big num">{formatMbit(tx)}<small>Mbit/s</small></span>
      </div>
      {#if traffic}
        <div class="peak">
          <span class="muted">peak today</span>
          <span class="num">↓ {formatMbit(Math.max(traffic.peakRx, rx))} · ↑ {formatMbit(Math.max(traffic.peakTx, tx))}<small>Mbit/s</small></span>
        </div>
      {/if}
    </div>
    {#if top.length}
      {@const text = top.map((t) => `${t.label} ↓${formatMbit(t.rx)} ↑${formatMbit(t.tx)}`).join(', ')}
      <p class="top ellipsis" title={`Top now: ${text}`}>
        <span class="muted">Top now</span>
        {#each top as t (t.label)}
          <span class="item"><b>{t.label}</b> <span class="num">↓{formatMbit(t.rx)} ↑{formatMbit(t.tx)}</span></span>
        {/each}
      </p>
    {/if}
    <AreaChart {times} format={tick}
      series={[{ values: $history.map((h) => mbit(sum(h.net, 'rx'))), color: 'var(--down)' },
        { values: $history.map((h) => mbit(sum(h.net, 'tx'))), color: 'var(--up)' }]}
      label={`Network throughput over the last five minutes in Mbit/s: now ${formatMbit(rx)} down, ${formatMbit(tx)} up`} />
  {:else}
    <div class="nums">
      <div class="num-block">
        <span class="legend"><i class="sw down"></i>CPU</span>
        <span class="big num">{Math.round(Math.max(0, latest.cpuPercent))}<small>%</small></span>
      </div>
      <div class="num-block">
        <span class="legend">Load</span>
        <span class="mid num">{latest.load.map((l) => l.toFixed(2)).join(' · ')}</span>
      </div>
    </div>
    <AreaChart {times} max={100} format={(v) => `${v}%`}
      series={[{ values: $history.map((h) => h.cpuPercent), color: 'var(--down)' }]}
      label={`CPU usage over the last five minutes, now ${formatPct(latest.cpuPercent)}`} />
  {/if}
</section>

<style>
  .card-head { flex-wrap: wrap; }
  .pad { padding: 24px 0 18px; }
  .nums { display: flex; gap: 12px 32px; align-items: flex-end; flex-wrap: wrap; margin-bottom: 10px; min-width: 0; }
  .num-block { display: grid; gap: 4px; min-width: 0; }
  .legend { display: flex; align-items: center; gap: 7px; font-size: var(--t-sm); color: var(--text2); }
  .sw.down { background: var(--down); }
  .sw.up { background: var(--up); }
  .big { font-size: var(--t-2xl); font-weight: 600; letter-spacing: -0.02em; line-height: 1.1; white-space: nowrap; }
  .mid { font-size: var(--t-lg); font-weight: 600; line-height: 1.4; white-space: nowrap; }
  small { font-size: var(--t-base); font-weight: 450; color: var(--muted); margin-left: 5px; letter-spacing: 0; }
  .peak { margin-left: auto; display: grid; justify-items: end; gap: 2px; font-size: var(--t-base); white-space: nowrap; }
  .peak small { font-size: var(--t-sm); margin-left: 4px; }
  .peak .muted { font-size: var(--t-sm); }
  .top { font-size: var(--t-sm); color: var(--text2); margin: 0 0 10px; }
  .top .muted { margin-right: 10px; }
  .top .item + .item { margin-left: 14px; }
  .top b { color: var(--text); font-weight: 550; }
  @container (max-width: 520px) {
    .peak { margin-left: 0; justify-items: start; }
  }
</style>
