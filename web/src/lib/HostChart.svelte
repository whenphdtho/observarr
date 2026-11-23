<script>
  import AreaChart from './AreaChart.svelte';
  import Icon from './icons/Icon.svelte';
  import { history } from './data.js';
  import { formatPct, formatRate } from './format.js';

  // HostChart is the host's CPU or network throughput over the last five minutes, from the shared
  // history (GET /api/system/history, then every live reading).
  let mode = $state('cpu');

  const times = $derived($history.map((h) => new Date(h.at).getTime()));
  const latest = $derived($history[$history.length - 1] ?? null);
  const rx = $derived($history.map((h) => sum(h.net, 'rx')));
  const tx = $derived($history.map((h) => sum(h.net, 'tx')));
  const hasNet = $derived(!!latest?.net?.length);

  function sum(list, key) {
    return (list ?? []).reduce((n, x) => n + (Number.isFinite(x[key]) ? x[key] : 0), 0);
  }

  // split turns "5.1 MiB/s" into ["5.1", "MiB/s"] so the number and its unit can be sized apart.
  function split(text) {
    const i = text.indexOf(' ');
    return i < 0 ? [text, ''] : [text.slice(0, i), text.slice(i + 1)];
  }

  function compactRate(v) {
    return formatRate(v).replace('.0 ', ' ');
  }
</script>

<section class="card host-chart" aria-labelledby="host-chart-title">
  <div class="card-head">
    <h2 id="host-chart-title"><Icon name="activity" size={16} />Host activity</h2>
    {#if hasNet}
      <div class="seg" role="group" aria-label="Chart">
        <button type="button" aria-pressed={mode === 'cpu'} onclick={() => { mode = 'cpu'; }}>CPU</button>
        <button type="button" aria-pressed={mode === 'net'} onclick={() => { mode = 'net'; }}>Network</button>
      </div>
    {/if}
  </div>

  {#if !latest}
    <p class="muted none">No host readings yet. Host figures need the host's /proc; see the System view.</p>
  {:else}
    {#if mode === 'net' && hasNet}
      {@const [rxNum, rxUnit] = split(formatRate(sum(latest.net, 'rx')))}
      {@const [txNum, txUnit] = split(formatRate(sum(latest.net, 'tx')))}
      <div class="nums">
        <div class="num-block">
          <span class="legend"><i class="swatch down"></i>Download</span>
          <span class="big num">{rxNum}<small>{rxUnit}</small></span>
        </div>
        <div class="num-block">
          <span class="legend"><i class="swatch up"></i>Upload</span>
          <span class="big num">{txNum}<small>{txUnit}</small></span>
        </div>
      </div>
      <AreaChart {times} format={compactRate}
        series={[{ values: rx, color: 'var(--down)' }, { values: tx, color: 'var(--up)' }]}
        label={`Network throughput over the last five minutes: now ${formatRate(sum(latest.net, 'rx'))} down, ${formatRate(sum(latest.net, 'tx'))} up`} />
    {:else}
      <div class="nums">
        <div class="num-block">
          <span class="legend"><i class="swatch down"></i>CPU</span>
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
  {/if}
</section>

<style>
  .host-chart { padding-bottom: 8px; }
  .card-head { flex-wrap: wrap; }
  .none { padding: 24px 0 18px; font-size: 13px; }
  .seg {
    display: inline-flex; gap: 2px; padding: 3px; border-radius: var(--r-sm);
    background: var(--surface); border: 1px solid var(--border);
  }
  .seg button {
    padding: 4px 12px; border-radius: 7px; font-weight: 500; color: var(--text2);
    background: transparent; border: 0;
  }
  .seg button:hover:not(:disabled) { background: transparent; color: var(--text); }
  .seg button[aria-pressed='true'] { background: var(--surface3); color: var(--text); }
  .nums { display: flex; gap: 32px; align-items: flex-end; flex-wrap: wrap; margin-bottom: 12px; min-width: 0; }
  .num-block { display: grid; gap: 6px; min-width: 0; }
  .legend { display: flex; align-items: center; gap: 7px; font-size: 12.5px; color: var(--text2); font-weight: 500; }
  .swatch { width: 8px; height: 8px; border-radius: 2px; }
  .swatch.down { background: var(--down); }
  .swatch.up { background: var(--up); }
  .big { font-size: 32px; font-weight: 600; letter-spacing: -0.025em; line-height: 1; white-space: nowrap; }
  .mid { font-size: 17px; font-weight: 600; line-height: 1.4; white-space: nowrap; }
  small { font-size: 13px; font-weight: 500; color: var(--muted); margin-left: 5px; letter-spacing: 0; }
</style>
