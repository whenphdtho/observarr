<script>
  import { monotonePath } from './curve.js';
  import { niceCeil, timeTicks, clock } from './chart.js';

  // AreaChart draws one or more series over time as smooth lines over a faint flat fill, with y-axis
  // labels and a time axis. times are milliseconds since epoch, one per value;
  // each series is { values, color } with color a CSS colour (e.g. "var(--down)"). max fixes the
  // top of the y axis (e.g. 100 for a percentage); otherwise it is rounded up from the data.
  let { times, series, max = null, format, label, height = 190 } = $props();

  const PAD = { top: 8, right: 6, bottom: 24, left: 52 };
  let width = $state(0);

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  // Steps of a round size up to the top: max, or the data rounded up to a whole step.
  const peak = $derived(Math.max(0, ...series.flatMap((s) => s.values)));
  const step = $derived(niceCeil((max ?? peak) / 5));
  const top = $derived(max ?? step * Math.max(1, Math.ceil(peak / step)));
  const yTicks = $derived(Array.from({ length: Math.round(top / step) + 1 }, (_, i) => i * step));
  const xTicks = $derived(timeTicks(t0, t0 + span));

  const x = (t) => PAD.left + ((t - t0) / span) * plotW;
  const y = (v) => PAD.top + plotH - (Math.min(top, Math.max(0, v)) / top) * plotH;

  // linePath joins the points with a monotone curve (see curve.js): it never runs backwards in
  // time, however unevenly the samples are spaced, and never overshoots past zero or the top.
  function linePath(values) {
    return monotonePath(values.map((v, i) => [x(times[i]), y(v)]));
  }

  function areaPath(line) {
    const base = (PAD.top + plotH).toFixed(1);
    return `${line}L${x(t0 + span).toFixed(1)},${base}L${x(t0).toFixed(1)},${base}Z`;
  }
</script>

<div class="chart" bind:clientWidth={width} style:height={`${height}px`}>
  {#if width > 0 && times.length > 1}
    <svg {width} {height} role="img" aria-label={label}>
      {#each yTicks as v (v)}
        <line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={y(v)} y2={y(v)} />
        <text class="label" x={PAD.left - 10} y={y(v)} text-anchor="end" dominant-baseline="middle">{format(v)}</text>
      {/each}
      {#each xTicks as t (t)}
        {#if x(t) > PAD.left + 16 && x(t) < PAD.left + plotW - 16}
          <text class="label" x={x(t)} y={height - 6} text-anchor="middle">{clock(t)}</text>
        {/if}
      {/each}
      {#each series as s, i (i)}
        {@const line = linePath(s.values)}
        <path class="area" d={areaPath(line)} style:fill={s.color} />
        <path class="line" d={line} style:stroke={s.color} />
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
  .label { fill: var(--muted); font-size: var(--t-xs); }
  /* One clean line per series over a barely-there flat fill. */
  .area { fill-opacity: 0.04; }
  .line { fill: none; stroke-width: 1.5; stroke-linejoin: round; stroke-linecap: round; }
  .waiting { display: grid; place-items: center; height: 100%; }
</style>
