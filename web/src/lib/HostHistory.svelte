<script>
  import { untrack } from 'svelte';
  import TimeChart from './TimeChart.svelte';
  import RangeSwitch from './RangeSwitch.svelte';
  import Skeleton from './Skeleton.svelte';
  import { api } from './api.js';
  import { history, snapshot } from './data.js';
  import { rangeOf, hostSeries, mean } from './history.js';
  import { formatBytes, formatPct } from './format.js';

  // HostHistory is the host's CPU and memory over a range, both in percent on one scale (memory of the total),
  // with the memory in bytes in the tooltip. The five minutes come from the shared live history; the longer
  // ranges from GET /api/system/history?range=, reloaded every minute. While another range loads, the chart keeps
  // the one it has, dimmed, and the new one fades in over it: no empty frame between the two.
  const RELOAD_MS = 60 * 1000;

  let range = $state('5m');
  let loaded = $state(null);
  // drawn is the range the chart shows: range itself once its data is in.
  let drawn = $state('5m');
  let missing = $state(false);
  let error = $state('');

  async function load() {
    const r = range;
    try {
      const data = hostSeries(await api('GET', `/api/system/history?range=${r}`), r);
      if (r !== range) return;
      missing = !data;
      loaded = data;
      drawn = r;
      error = '';
    } catch (err) {
      if (r !== range) return;
      missing = err.status === 400;
      error = missing ? '' : err.message;
    }
  }

  $effect(() => {
    missing = false;
    error = '';
    if (range === '5m') {
      drawn = '5m';
      return;
    }
    untrack(load);
    const id = setInterval(load, RELOAD_MS);
    return () => clearInterval(id);
  });

  const memTotal = $derived($snapshot?.host?.memTotal ?? 0);
  const live = $derived(hostSeries($history, '5m'));
  const data = $derived(drawn === '5m' ? live : loaded);
  const busy = $derived(drawn !== range);
  const memPct = $derived(data && memTotal > 0 ? data.mem.map((v) => (Number.isFinite(v) ? (v / memTotal) * 100 : null)) : []);
  const avgCpu = $derived(data ? mean(data.cpu) : null);
  const avgMem = $derived(data ? mean(data.mem) : null);
  const shown = $derived(rangeOf(drawn));
</script>

<section class="section" aria-labelledby="host-history-title">
  <div class="card-head">
    <h2 id="host-history-title">CPU and memory</h2>
    <RangeSwitch bind:value={range} />
  </div>
  {#if error}
    <p class="error" role="alert">{error}</p>
  {:else if missing}
    <p class="waiting pad">This server keeps no host history for {rangeOf(range).text} yet.</p>
  {:else if !data || data.times.length === 0}
    <Skeleton kind="chart" height={230} label="Waiting for the first reading…" />
  {:else}
    <div class="drawn settle" class:busy aria-busy={busy}>
    {#if avgCpu !== null}
      <p class="avg">
        <span class="muted">Average over {shown.text}</span>
        <span><i class="ring" style:color="var(--cpu)"></i>CPU <b class="num">{formatPct(avgCpu)}</b></span>
        {#if avgMem !== null}<span><i class="ring" style:color="var(--mem)"></i>Memory <b class="num">{formatBytes(avgMem)}</b></span>{/if}
      </p>
    {/if}
    <TimeChart times={data.times} max={100} format={(v) => `${v}%`} value={formatPct} live={drawn === '5m'} legend={false}
      series={[
        { label: 'CPU', values: data.cpu, color: 'var(--cpu)' },
        ...(memTotal > 0 ? [{ label: 'Memory', values: memPct, color: 'var(--mem)',
          tip: (v) => `${formatBytes((v / 100) * memTotal)} · ${formatPct(v)}` }] : []),
      ]}
      label={`CPU${memTotal > 0 ? ' and memory' : ''} use over ${shown.text}, in percent${avgCpu !== null ? `; CPU ${formatPct(avgCpu)} on average` : ''}`} />
    </div>
  {/if}
</section>

<style>
  .card-head { flex-wrap: wrap; }
  .pad { padding: 24px 0; }
  .drawn { transition: opacity var(--dur-base) var(--ease-out); }
  .busy { opacity: 0.45; }
  .avg { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 18px; margin: -6px 0 14px; font-size: var(--t-body); }
  .avg > span { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; color: var(--text2); }
  .avg b { font-weight: 600; color: var(--text); }
</style>
