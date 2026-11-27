<script>
  import { untrack } from 'svelte';
  import Fold from './Fold.svelte';
  import Num from './Num.svelte';
  import RangeSwitch from './RangeSwitch.svelte';
  import Skeleton from './Skeleton.svelte';
  import TimeChart from './TimeChart.svelte';
  import { api } from './api.js';
  import { snapshot } from './data.js';
  import { serviceList } from './live.js';
  import { RANGES, rangeOf } from './history.js';
  import { recall, remember } from './prefs.js';
  import { formatMbit, formatRate } from './format.js';
  import { advance, byPurpose, sum } from './traffic.js';

  // Traffic is the Overview's chart: what the server sends and receives over a range from five minutes to a year,
  // by what it is for (downloads, streams, cloud) where apps have containers of their own, else in and out. It
  // loads GET /api/network and, for the five minutes, adds each live reading. The range is remembered in this
  // browser. The Network view has each app's share.
  const RELOAD_MS = 60 * 1000;
  // Every range but the live one is drawn with this many points, so one can move into the other.
  const POINTS = 120;

  const stored = recall('range', '24h');
  let range = $state(RANGES.some((r) => r.id === stored) ? stored : '24h');
  let view = $state(null);
  let error = $state('');
  // missing: the server has no history for the range asked for (one without the longer ranges answers 400).
  let missing = $state(false);

  async function load() {
    const r = range;
    try {
      const v = await api('GET', `/api/network?range=${r}`);
      if (r !== range) return;
      v.at = r === '5m' ? (v.times[v.times.length - 1] ?? 0) : 0; // the latest reading it holds
      view = v;
      error = '';
      missing = false;
    } catch (err) {
      if (r !== range) return;
      missing = err.status === 400;
      error = missing ? '' : err.message;
      if (missing) view = null;
    }
  }

  $effect(() => {
    remember('range', range);
    untrack(load);
    const id = setInterval(load, RELOAD_MS);
    return () => clearInterval(id);
  });

  $effect(() => {
    const snap = $snapshot;
    untrack(() => {
      if (snap && view) advance(view, snap);
    });
  });

  const host = $derived($snapshot?.host ?? null);
  const rx = $derived(host ? sum(host.net, 'rx') : 0);
  const tx = $derived(host ? sum(host.net, 'tx') : 0);
  const kinds = $derived(new Map($serviceList.map((s) => [s.name, s.kinds ?? []])));
  const series = $derived(view ? byPurpose(view, kinds) : []);
  const shown = $derived(rangeOf(view?.range ?? range));
  const mbit = (v) => (v * 8) / 1e6;
  const rate = (v) => formatMbit((v * 1e6) / 8);
  const axis = (v) => (v < 10 && v !== Math.round(v) ? v.toFixed(1) : String(Math.round(v)));
</script>

<Fold id="traffic" title="Traffic">
  {#snippet summary()}
    {#if host}
      <span class="num"><b><Num value={mbit(rx)} format={rate} /></b> Mbit/s in · <b><Num value={mbit(tx)} format={rate} /></b> out</span>
    {/if}
  {/snippet}
  {#snippet controls()}
    <!-- Without figures there is no range to pick. -->
    {#if host || !$snapshot}<RangeSwitch bind:value={range} />{/if}
  {/snippet}
  {#if $snapshot && !host}
    <p class="waiting pad">No traffic figures: they need the host’s /proc. The System view says how to set it up.</p>
  {:else if error}
    <p class="error pad" role="alert">{error}</p>
  {:else if missing}
    <p class="waiting pad">This server keeps no traffic history for {rangeOf(range).text} yet.</p>
  {:else if !view}
    <Skeleton kind="chart" height={250} label="Waiting for the first reading…" />
  {:else}
    <!-- While another range loads, the chart keeps the one it has, dimmed. -->
    <div class="drawn settle" class:busy={view.range !== range} aria-busy={view.range !== range}>
      <TimeChart times={view.times} {series} height={230} format={axis} value={(v) => formatRate((v * 1e6) / 8)}
        live={view.range === '5m'} points={view.range === '5m' ? 0 : POINTS} range={view.range} hint
        label={`Traffic over ${shown.text} in Mbit/s: now ${formatMbit(rx)} in, ${formatMbit(tx)} out`} />
    </div>
  {/if}
</Fold>

<style>
  .pad { padding: 20px 0 12px; }
  .drawn { transition: opacity var(--dur-base) var(--ease-out); }
  .busy { opacity: 0.45; }
</style>
