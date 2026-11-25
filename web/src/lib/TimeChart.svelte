<script>
  import { monotonePath } from './curve.js';
  import { niceCeil, timeTicks, tickLabel, stampLabel, nearest, runs, tipSide, thin } from './chart.js';
  import { chartShift } from './motion.js';
  import { step as stepOf } from './history.js';

  // TimeChart draws series over time as smooth lines over soft translucent fills, laid over each other, with
  // a faint grid, axis labels, a key with ring markers, and a crosshair with the values under the pointer (or
  // the arrow keys). times are ms since epoch, one per value; each series is { label, values, color } with
  // values that may hold null for a gap, color a CSS colour (e.g. "var(--cpu)") and optionally tip(v), the
  // text of a value in the tooltip. More samples than pixels are merged into their means for drawing. max fixes the top of the y axis (e.g. 100 for a percentage); otherwise
  // it is rounded up from the data. format labels the y axis, value the tooltip (format by default). live
  // makes the drawing glide as new readings push the window on.
  let {
    times: allTimes, series: allSeries, max = null, format, value = null, label, height = 210, legend = true, live = false,
  } = $props();

  const PAD = { top: 10, right: 8, bottom: 26, left: 48 };
  let width = $state(0);
  let hover = $state(-1);
  let keyed = $state(false); // the crosshair was put by a key, so it stays when the pointer leaves
  const uid = $props.id();

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  const thinned = $derived(thin(allTimes, allSeries.map((s) => s.values), Math.max(60, Math.floor(plotW))));
  const times = $derived(thinned.times);
  const series = $derived(allSeries.map((s, k) => ({ ...s, values: thinned.lists[k] })));
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  const sampleStep = $derived(stepOf(times));
  // Steps of a round size up to the top: max, or the data rounded up to a whole step.
  const peak = $derived(Math.max(0, ...series.flatMap((s) => s.values.filter(Number.isFinite))));
  const yStep = $derived(niceCeil((max ?? peak) / 4));
  const top = $derived(max ?? yStep * Math.max(1, Math.ceil(peak / yStep)));
  const yTicks = $derived(Array.from({ length: Math.round(top / yStep) + 1 }, (_, i) => i * yStep));
  const xTicks = $derived(timeTicks(t0, t0 + span));
  const base = $derived(PAD.top + plotH);

  const x = (t) => PAD.left + ((t - t0) / span) * plotW;
  const y = (v) => PAD.top + plotH - (Math.min(top, Math.max(0, v)) / top) * plotH;

  // Each series breaks into runs at its gaps; each run is one line and the fill below it.
  const drawn = $derived(series.map((s) => runs(times.length, (i) => Number.isFinite(s.values[i])).map((run) => {
    const pts = run.map((i) => [x(times[i]), y(s.values[i])]);
    const line = pts.length > 1 ? monotonePath(pts) : '';
    const area = line ? `${line}L${pts[pts.length - 1][0].toFixed(1)},${base}L${pts[0][0].toFixed(1)},${base}Z` : '';
    return { line, area, dot: pts.length === 1 ? pts[0] : null };
  })));

  const fmt = (s, v) => (Number.isFinite(v) ? (s.tip ? s.tip(v) : (value ?? format)(v)) : 'no data');
  const tip = $derived(hover >= 0 && hover < times.length ? {
    x: x(times[hover]),
    when: stampLabel(times[hover], sampleStep, span),
    rows: series.map((s) => ({ label: s.label, color: s.color, text: fmt(s, s.values[hover]), v: s.values[hover] })),
  } : null);
  const side = $derived(tip ? tipSide(tip.x, width) : null);
  // The sample the keys stand on (the latest before any), and its words for a screen reader.
  const now = $derived(hover >= 0 ? hover : Math.max(0, times.length - 1));
  const spoken = $derived(times.length ? `${stampLabel(times[now], sampleStep, span)}: ${series.map((s) => `${s.label} ${fmt(s, s.values[now])}`).join(', ')}` : '');

  function point(event) {
    const box = event.currentTarget.getBoundingClientRect();
    const px = event.clientX - box.left;
    if (px < PAD.left - 4 || px > PAD.left + plotW + 4) {
      hover = -1;
      return;
    }
    keyed = false;
    hover = nearest(times, t0 + ((px - PAD.left) / plotW) * span);
  }

  function key(event) {
    const n = times.length;
    if (!n) return;
    const jump = Math.max(1, Math.round(n / 24));
    let i = hover < 0 ? n - 1 : hover;
    if (event.key === 'ArrowLeft') i = Math.max(0, i - (event.shiftKey ? jump : 1));
    else if (event.key === 'ArrowRight') i = Math.min(n - 1, i + (event.shiftKey ? jump : 1));
    else if (event.key === 'Home') i = 0;
    else if (event.key === 'End') i = n - 1;
    else if (event.key === 'Escape') i = -1;
    else return;
    event.preventDefault();
    keyed = true;
    hover = i;
  }
</script>

<div class="time-chart">
  <!-- A slider over the samples: the arrow keys move the crosshair, and each value is read out. -->
  <div class="plot" bind:clientWidth={width} style:height={`${height}px`} role="slider" aria-roledescription="chart"
    aria-label={label} tabindex={times.length > 1 ? 0 : -1} aria-orientation="horizontal"
    aria-valuemin={0} aria-valuemax={Math.max(0, times.length - 1)} aria-valuenow={now} aria-valuetext={spoken}
    onpointermove={point} onpointerdown={point} onpointerleave={() => { if (!keyed) hover = -1; }}
    onkeydown={key} onblur={() => { hover = -1; keyed = false; }}>
    {#if width > 0 && times.length > 1}
      <svg {width} {height} aria-hidden="true">
        <defs>
          {#each series as s, k (k)}
            <linearGradient id={`${uid}-fill-${k}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0" style:stop-color={s.color} style:stop-opacity="var(--area-hi)" />
              <stop offset="1" style:stop-color={s.color} style:stop-opacity="var(--area-lo)" />
            </linearGradient>
          {/each}
          <clipPath id={`${uid}-plot`}><rect x={PAD.left} y={0} width={plotW} height={height}></rect></clipPath>
        </defs>
        {#each yTicks as v (v)}
          <line class="grid" class:zero={v === 0} x1={PAD.left} x2={PAD.left + plotW} y1={y(v)} y2={y(v)} />
          <text class="label" x={PAD.left - 10} y={y(v)} text-anchor="end" dominant-baseline="middle">{format(v)}</text>
        {/each}
        {#each xTicks as t (t)}
          {#if x(t) > PAD.left + 18 && x(t) < PAD.left + plotW - 18}
            <text class="label" x={x(t)} y={height - 7} text-anchor="middle">{tickLabel(t, span)}</text>
          {/if}
        {/each}
        <g clip-path={`url(#${uid}-plot)`}>
          <g use:chartShift={live ? { t0, span, width: plotW } : null}>
            {#each drawn as parts, k (k)}
              {#each parts as p, j (j)}
                {#if p.area}<path class="area" d={p.area} fill={`url(#${uid}-fill-${k})`} />{/if}
              {/each}
            {/each}
            {#each drawn as parts, k (k)}
              {#each parts as p, j (j)}
                {#if p.line}<path class="line" d={p.line} style:stroke={series[k].color} />{/if}
                {#if p.dot}<circle cx={p.dot[0]} cy={p.dot[1]} r="2" style:fill={series[k].color} />{/if}
              {/each}
            {/each}
          </g>
        </g>
        {#if tip}
          <line class="cross" x1={tip.x} x2={tip.x} y1={PAD.top} y2={base} />
          {#each tip.rows as r, k (k)}
            {#if Number.isFinite(r.v)}
              <circle class="mark" cx={tip.x} cy={y(r.v)} r="4" style:stroke={r.color} />
            {/if}
          {/each}
        {/if}
      </svg>
      {#if tip}
        <div class="tip" style:left={side.left} style:right={side.right} aria-hidden="true">
          <span class="when">{tip.when}</span>
          {#each tip.rows as r, k (k)}
            <span class="row"><i class="ring" style:color={r.color}></i><span class="ellipsis">{r.label}</span><b class="num">{r.text}</b></span>
          {/each}
        </div>
      {/if}
    {:else if width > 0}
      <p class="waiting">Waiting for the first reading…</p>
    {/if}
  </div>
  {#if legend && series.length > 1}
    <ul class="keys" aria-label="Key">
      {#each series as s, k (k)}<li><i class="ring" style:color={s.color}></i>{s.label}</li>{/each}
    </ul>
  {/if}
</div>

<style>
  .time-chart { display: grid; gap: 12px; min-width: 0; }
  .plot { position: relative; width: 100%; min-width: 0; touch-action: pan-y; border-radius: var(--r-sm); }
  .plot:focus-visible { outline-offset: 4px; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--grid); }
  .grid.zero { stroke: var(--border2); }
  .label { fill: var(--muted); font-size: var(--t-micro); }
  .line { fill: none; stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
  .cross { stroke: var(--text2); stroke-width: 1; stroke-dasharray: 3 3; }
  .mark { fill: var(--surface); stroke-width: 2.5; }
  .waiting { display: grid; place-items: center; height: 100%; }
  .keys { padding-left: 48px; }
  @media (max-width: 720px) { .keys { padding-left: 0; } }

  .tip {
    position: absolute; top: 4px; z-index: 2; pointer-events: none; display: grid; gap: 4px; min-width: 150px; max-width: min(260px, 70%);
    padding: 9px 12px; border-radius: var(--r); background: var(--tip); border: 1px solid var(--tip-edge);
    box-shadow: var(--float); font-size: var(--t-small); color: var(--text2);
  }
  .when { color: var(--text); font-weight: 600; white-space: nowrap; }
  .row { display: flex; align-items: center; gap: 7px; min-width: 0; }
  .row .ellipsis { flex: 1; }
  .row b { color: var(--text); font-weight: 600; white-space: nowrap; margin-left: 8px; }
</style>
