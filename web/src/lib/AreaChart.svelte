<script module>
  let nextId = 0;
</script>

<script>
  // AreaChart draws one or more series over time as smooth lines with a fading fill, with y-axis
  // labels and a minute-based time axis. times are milliseconds since epoch, one per value;
  // each series is { values, color } with color a CSS colour (e.g. "var(--down)"). max fixes the
  // top of the y axis (e.g. 100 for a percentage); otherwise it is rounded up from the data.
  let { times, series, max = null, format, label, height = 190 } = $props();

  const id = `chart${nextId++}`;
  const PAD = { top: 8, right: 6, bottom: 24, left: 52 };
  let width = $state(0);

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  const top = $derived(max ?? niceCeil(Math.max(0, ...series.flatMap((s) => s.values))));
  const yTicks = $derived([0, 1, 2, 3, 4].map((i) => (top * i) / 4));
  const xTicks = $derived(minuteTicks(t0, t0 + span));

  // niceCeil rounds v up to 1, 2, 2.5 or 5 times a power of ten, so the y labels read cleanly.
  function niceCeil(v) {
    if (!(v > 0)) return 1;
    const mag = 10 ** Math.floor(Math.log10(v));
    return [1, 2, 2.5, 5, 10].map((m) => m * mag).find((n) => n >= v);
  }

  // minuteTicks returns whole minutes inside [from, to], spaced so there are at most six.
  function minuteTicks(from, to) {
    const minute = 60000;
    const step = Math.max(1, Math.ceil((to - from) / minute / 6)) * minute;
    const out = [];
    for (let t = Math.ceil(from / step) * step; t <= to; t += step) out.push(t);
    return out;
  }

  const x = (t) => PAD.left + ((t - t0) / span) * plotW;
  const y = (v) => PAD.top + plotH - (Math.min(top, Math.max(0, v)) / top) * plotH;

  // linePath joins the points with Catmull-Rom curves; control points are clamped to the plot so
  // the curve never dips below zero or above the top.
  function linePath(values) {
    const pts = values.map((v, i) => [x(times[i]), y(v)]);
    if (pts.length < 2) return '';
    const clampY = (v) => Math.min(PAD.top + plotH, Math.max(PAD.top, v));
    let d = `M${pts[0][0].toFixed(1)},${pts[0][1].toFixed(1)}`;
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[i - 1] ?? pts[i];
      const p1 = pts[i];
      const p2 = pts[i + 1];
      const p3 = pts[i + 2] ?? p2;
      const c1 = [p1[0] + (p2[0] - p0[0]) / 6, clampY(p1[1] + (p2[1] - p0[1]) / 6)];
      const c2 = [p2[0] - (p3[0] - p1[0]) / 6, clampY(p2[1] - (p3[1] - p1[1]) / 6)];
      d += `C${c1[0].toFixed(1)},${c1[1].toFixed(1)} ${c2[0].toFixed(1)},${c2[1].toFixed(1)} ${p2[0].toFixed(1)},${p2[1].toFixed(1)}`;
    }
    return d;
  }

  function areaPath(line) {
    const base = (PAD.top + plotH).toFixed(1);
    return `${line}L${x(t0 + span).toFixed(1)},${base}L${x(t0).toFixed(1)},${base}Z`;
  }

  function clock(t) {
    return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
</script>

<div class="chart" bind:clientWidth={width} style:height={`${height}px`}>
  {#if width > 0 && times.length > 1}
    <svg {width} {height} role="img" aria-label={label}>
      <defs>
        {#each series as s, i (i)}
          <linearGradient id={`${id}-${i}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style:stop-color={s.color} stop-opacity="0.3" />
            <stop offset="1" style:stop-color={s.color} stop-opacity="0" />
          </linearGradient>
        {/each}
      </defs>
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
        <path d={areaPath(line)} fill={`url(#${id}-${i})`} />
        <path class="line" d={line} style:stroke={s.color} />
      {/each}
    </svg>
  {:else if width > 0}
    <p class="muted waiting">Collecting readings…</p>
  {/if}
</div>

<style>
  .chart { width: 100%; min-width: 0; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--border); stroke-dasharray: 3 4; }
  .label { fill: var(--muted); font-size: 11.5px; font-variant-numeric: tabular-nums; }
  .line { fill: none; stroke-width: 1.75; stroke-linejoin: round; stroke-linecap: round; }
  .waiting { display: grid; place-items: center; height: 100%; font-size: 13px; }
</style>
