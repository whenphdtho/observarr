<script>
  import { niceCeil, timeTicks, clock } from './chart.js';

  // MirrorChart stacks traffic sources over time in Mbit/s: incoming above the zero line, outgoing
  // below it, on one scale. times are ms since epoch; each layer is { color, series: { rx, tx } }
  // with rates in bytes per second, one per time (a missing value counts as zero).
  let { times, layers, label, height = 280 } = $props();

  const PAD = { top: 10, right: 6, bottom: 24, left: 44 };
  const MIN_SIDE = 0.18; // the smaller side keeps at least this share of the height, so it stays readable
  let width = $state(0);

  const mbit = (v) => ((Number.isFinite(v) ? Math.max(0, v) : 0) * 8) / 1e6;

  // Cumulative tops per layer: stacks.rx[k][i] is the top of layer k at time i.
  const stacks = $derived.by(() => {
    const out = { rx: [], tx: [] };
    for (const key of ['rx', 'tx']) {
      let below = times.map(() => 0);
      for (const l of layers) {
        const top = times.map((_, i) => below[i] + mbit(l.series?.[key]?.[i]));
        out[key].push(top);
        below = top;
      }
    }
    return out;
  });
  const peak = (key) => Math.max(0, ...(stacks[key][stacks[key].length - 1] ?? [0]));
  const step = $derived(niceCeil((peak('rx') + peak('tx')) / 5 || 0.2));
  const inTop = $derived(Math.max(step, Math.ceil(peak('rx') / step) * step));
  const outTop = $derived(Math.max(step, Math.ceil(peak('tx') / step) * step));

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  // The zero line: each side's height follows its scale, within MIN_SIDE of the whole.
  const zero = $derived(PAD.top + plotH * Math.min(1 - MIN_SIDE, Math.max(MIN_SIDE, inTop / (inTop + outTop))));
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  const yTicks = $derived.by(() => {
    const out = [];
    for (let v = step; v <= inTop + 1e-9; v += step) out.push(v);
    for (let v = step; v <= outTop + 1e-9; v += step) out.push(-v);
    return [0, ...out];
  });

  const x = (i) => PAD.left + ((times[i] - t0) / span) * plotW;
  const y = (v) => (v >= 0 ? zero - (v / inTop) * (zero - PAD.top) : zero + (-v / outTop) * (PAD.top + plotH - zero));

  // area is the band between two cumulative lines (or the zero line), drawn with straight segments.
  function area(top, below, sign) {
    const pts = (vals) => times.map((_, i) => `${x(i).toFixed(1)},${y(sign * (vals?.[i] ?? 0)).toFixed(1)}`);
    return `M${pts(top).join('L')}L${pts(below).reverse().join('L')}Z`;
  }

  // Thin out the y labels when they would crowd each other.
  const labelEvery = $derived(Math.max(1, Math.ceil(16 / Math.max(1, (step / inTop) * (zero - PAD.top)))));
  const shownTicks = $derived(yTicks.filter((v) => Math.round(Math.abs(v) / step) % labelEvery === 0));
  const xTicks = $derived(timeTicks(t0, t0 + span));

  function tickLabel(v) {
    const a = Math.abs(v);
    return a < 10 && a !== Math.round(a) ? a.toFixed(1) : String(Math.round(a));
  }
</script>

<div class="chart" bind:clientWidth={width} style:height={`${height}px`}>
  {#if width > 0 && times.length > 1}
    <svg {width} {height} role="img" aria-label={label}>
      {#each shownTicks as v (v)}
        <line class="grid" class:zero={v === 0} x1={PAD.left} x2={PAD.left + plotW} y1={y(v)} y2={y(v)} />
        <text class="label" x={PAD.left - 8} y={y(v)} text-anchor="end" dominant-baseline="middle">{tickLabel(v)}</text>
      {/each}
      {#each layers as l, k (k)}
        <path class="band" d={area(stacks.rx[k], stacks.rx[k - 1], 1)} style:fill={l.color} />
        <path class="band" d={area(stacks.tx[k], stacks.tx[k - 1], -1)} style:fill={l.color} />
      {/each}
      <text class="side" x={PAD.left + 8} y={PAD.top + 12}>↓ In</text>
      <text class="side" x={PAD.left + 8} y={PAD.top + plotH - 6}>↑ Out</text>
      {#each xTicks as t (t)}
        {@const tx = PAD.left + ((t - t0) / span) * plotW}
        {#if tx > PAD.left + 20 && tx < PAD.left + plotW - 20}
          <text class="label" x={tx} y={height - 6} text-anchor="middle">{clock(t)}</text>
        {/if}
      {/each}
    </svg>
  {:else if width > 0}
    <p class="waiting">Waiting for the first reading…</p>
  {/if}
</div>

<style>
  .chart { width: 100%; min-width: 0; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--border); }
  .grid.zero { stroke: var(--field-border); }
  .label { fill: var(--muted); font-size: var(--t-xs); }
  .side { fill: var(--text2); font-size: var(--t-xs); font-weight: 550; paint-order: stroke; stroke: var(--bg); stroke-width: 3px; }
  /* A thin line in the page's colour keeps neighbouring bands apart. */
  .band { stroke: var(--bg); stroke-width: 1; stroke-linejoin: round; fill-opacity: 0.85; }
  .waiting { display: grid; place-items: center; height: 100%; }
</style>
