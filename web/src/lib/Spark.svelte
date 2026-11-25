<script>
  import { monotonePath } from './curve.js';
  import { runs } from './chart.js';

  // Spark is a small line of a figure's recent course under a soft fill, without axes: the figure itself and
  // its change are given as text beside it, so it is hidden from screen readers. values are evenly spaced,
  // null for a gap; min and max fix the scale (by default from zero to the highest value; min null starts it
  // at the lowest value, for a figure that never nears zero).
  let { values, color = 'var(--series-2)', min = 0, max = null, height = 30 } = $props();

  let width = $state(0);
  const uid = $props.id();
  const finite = $derived(values.filter(Number.isFinite));
  const low = $derived(min ?? Math.min(...finite));
  const high = $derived(max ?? Math.max(...finite));
  // A flat line sits in the middle rather than on an edge.
  const flat = $derived(!(high - low > 1e-9));
  const lo = $derived(flat ? low - 1 : low);
  const top = $derived(flat ? low + 1 : high);
  const x = (i) => 1 + (i / Math.max(1, values.length - 1)) * (width - 2);
  const y = (v) => 2 + (height - 4) * (1 - (Math.min(top, Math.max(lo, v)) - lo) / (top - lo));
  const parts = $derived(runs(values.length, (i) => Number.isFinite(values[i])).filter((r) => r.length > 1).map((run) => {
    const pts = run.map((i) => [x(i), y(values[i])]);
    const line = monotonePath(pts);
    return { line, area: `${line}L${pts[pts.length - 1][0].toFixed(1)},${height}L${pts[0][0].toFixed(1)},${height}Z` };
  }));
</script>

<span class="spark" bind:clientWidth={width} style:height={`${height}px`} aria-hidden="true">
  {#if width > 0 && finite.length > 1}
    <svg {width} {height}>
      <defs>
        <linearGradient id={`${uid}-g`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" style:stop-color={color} style:stop-opacity="var(--spark-hi)" />
          <stop offset="1" style:stop-color={color} stop-opacity="0" />
        </linearGradient>
      </defs>
      {#each parts as p, i (i)}
        <path d={p.area} fill={`url(#${uid}-g)`} />
        <path class="line" d={p.line} style:stroke={color} />
      {/each}
    </svg>
  {/if}
</span>

<style>
  .spark { display: block; width: 100%; min-width: 0; }
  svg { display: block; overflow: visible; }
  .line { fill: none; stroke-width: 1.6; stroke-linejoin: round; stroke-linecap: round; }
</style>
