<script>
  import { untrack } from 'svelte';
  import { monotonePath } from './curve.js';
  import { niceCeil, timeTicks, tickLabel, stampLabel, nearest, runs, tipAt, thin, bucket, shownOff } from './chart.js';
  import { chartShift, dur, ease, curve, ms } from './motion.js';
  import { step as stepOf } from './history.js';

  // TimeChart draws series over time as smooth lines over soft translucent fills, laid over each other, with a
  // faint grid, axis labels, a key whose entries switch a series off and on, and a crosshair with the values under
  // the pointer (or the arrow keys). times are ms since epoch, one per value; each series is { label, values,
  // color } with values that may hold null for a gap, color a CSS colour (e.g. "var(--cpu)") and optionally
  // tip(v), the text of a value in the tooltip. max fixes the top of the y axis (e.g. 100 for a percentage);
  // otherwise it is rounded up from the data. format labels the y axis, value the tooltip (format by default).
  // live makes the drawing glide as new readings push the window on.
  //
  // More samples than pixels are merged into their means for drawing. With points, a range is instead drawn with
  // exactly that many, so that two ranges have a point for a point: when range (any value naming the range shown)
  // changes and the points match, the lines move from the one into the other; otherwise the new drawing fades in.
  let {
    times: allTimes, series: allSeries, max = null, format, value = null, label, height = 210, legend = true, live = false,
    points = 0, range = '', hint = false,
  } = $props();

  const PAD = { top: 10, right: 8, bottom: 26, left: 48 };
  let width = $state(0);
  let hover = $state(-1);
  let keyed = $state(false); // the crosshair was put by a key, so it stays when the pointer leaves
  let off = $state({}); // the series switched off in the key, by label
  let plotEl = $state();
  const uid = $props.id();

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  const lists = $derived(allSeries.map((s) => s.values));
  const thinned = $derived(points > 0 ? bucket(allTimes, lists, points) : thin(allTimes, lists, Math.max(60, Math.floor(plotW))));
  const times = $derived(thinned.times);
  // What is off among the series there are now: a series that left takes its switch with it, and one left alone
  // is shown (see shownOff).
  const hidden = $derived(shownOff(allSeries.map((s) => s.label), off));
  const series = $derived(allSeries.map((s, k) => ({ ...s, values: thinned.lists[k], off: !!hidden[s.label] })));
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  const sampleStep = $derived(stepOf(times));
  // Steps of a round size up to the top: max, or the data rounded up to a whole step. A series switched off
  // keeps its place in the scale, so the others do not jump.
  const peak = $derived(Math.max(0, ...series.flatMap((s) => s.values.filter(Number.isFinite))));
  const yStep = $derived(niceCeil((max ?? peak) / 4));
  const top = $derived(max ?? yStep * Math.max(1, Math.ceil(peak / yStep)));
  const yTicks = $derived(Array.from({ length: Math.round(top / yStep) + 1 }, (_, i) => i * yStep));
  const base = $derived(PAD.top + plotH);

  const x = (t) => PAD.left + ((t - t0) / span) * plotW;
  // The time labels that fit: inside the plot, and far enough from the one before to be read apart.
  const xTicks = $derived.by(() => {
    let last = -Infinity;
    return timeTicks(t0, t0 + span).filter((t) => {
      const at = x(t);
      if (at < PAD.left + 18 || at > PAD.left + plotW - 18 || at - last < 52) return false;
      last = at;
      return true;
    });
  });
  const y = (v) => PAD.top + plotH - (Math.min(top, Math.max(0, v)) / top) * plotH;

  // Where each value belongs, in pixels (null for a gap), and where it is drawn: the same, except while one range
  // moves into the next.
  const target = $derived(series.map((s) => s.values.map((v) => (Number.isFinite(v) ? y(v) : null))));
  let moving = $state.raw(null);
  const alike = (a, b) => !!a && a.length === b.length && a.every((l, k) => l.length === b[k].length);
  const ys = $derived(alike(moving, target) ? moving : target);

  let shownRange;
  let shownY = null;
  let frame = 0;
  $effect(() => {
    const to = target;
    const next = range;
    untrack(() => {
      cancelAnimationFrame(frame);
      const from = moving ?? shownY;
      const changed = shownRange !== undefined && next !== shownRange;
      // A reading that arrives while the lines travel gives them a new place to go, not a jump there.
      const travelling = moving !== null;
      shownRange = next;
      shownY = to;
      const d = ms(dur('slow'));
      const can = d > 0 && plotW > 0 && alike(from, to);
      if (!can || !(changed || travelling)) {
        moving = null;
        if (changed && d) plotEl?.animate([{ opacity: 0 }, { opacity: 1 }], { duration: d, easing: curve('out') });
        return;
      }
      const start = performance.now();
      const eased = ease('out');
      const tick = (now) => {
        const t = Math.min(1, (now - start) / d);
        const k = eased(t);
        // A gap on either side cannot travel: it changes hands half-way.
        moving = t === 1 ? null : to.map((l, s) => l.map((v, i) => {
          const a = from[s][i];
          return v === null || a === null ? (t < 0.5 ? a : v) : a + (v - a) * k;
        }));
        if (t < 1) frame = requestAnimationFrame(tick);
      };
      moving = from;
      frame = requestAnimationFrame(tick);
    });
    return () => cancelAnimationFrame(frame);
  });

  // Each series breaks into runs at its gaps; each run is one line and the fill below it.
  const drawn = $derived(series.map((s, k) => runs(times.length, (i) => ys[k][i] !== null).map((run) => {
    const pts = run.map((i) => [x(times[i]), ys[k][i]]);
    const line = pts.length > 1 ? monotonePath(pts) : '';
    const area = line ? `${line}L${pts[pts.length - 1][0].toFixed(1)},${base}L${pts[0][0].toFixed(1)},${base}Z` : '';
    return { line, area, dot: pts.length === 1 ? pts[0] : null };
  })));

  const fmt = (s, v) => (Number.isFinite(v) ? (s.tip ? s.tip(v) : (value ?? format)(v)) : 'no data');
  const shown = $derived(series.filter((s) => !s.off));
  const tip = $derived(hover >= 0 && hover < times.length ? {
    x: x(times[hover]),
    when: stampLabel(times[hover], sampleStep, span),
    rows: shown.map((s) => ({ label: s.label, color: s.color, text: fmt(s, s.values[hover]), v: s.values[hover] })),
  } : null);
  // The sample the keys stand on (the latest before any), and its words for a screen reader.
  const now = $derived(hover >= 0 ? hover : Math.max(0, times.length - 1));
  const spoken = $derived(times.length ? `${stampLabel(times[now], sampleStep, span)}: ${shown.map((s) => `${s.label} ${fmt(s, s.values[now])}`).join(', ')}` : '');

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
  {#if legend && series.length > 1}
    <div class="keys">
      <div class="toggles" role="group" aria-label="Lines shown">
        {#each series as s (s.label)}
          <button type="button" class="key" aria-pressed={!s.off} title={s.off ? `Show ${s.label}` : `Hide ${s.label}`}
            onclick={() => { off = { ...hidden, [s.label]: !s.off }; }}>
            <i class="ring" style:color={s.color}></i>{s.label}
          </button>
        {/each}
      </div>
      {#if hint}<span class="hint" aria-hidden="true">Move over the chart · click a name to hide it</span>{/if}
    </div>
  {/if}
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
          <text class="label" x={x(t)} y={height - 7} text-anchor="middle">{tickLabel(t, span)}</text>
        {/each}
        <g clip-path={`url(#${uid}-plot)`}>
          <g bind:this={plotEl} use:chartShift={{ t0, span, width: plotW, live, fade: false }}>
            {#each drawn as parts, k (k)}
              <g class="series" class:off={series[k].off}>
                {#each parts as p, j (j)}
                  {#if p.area}<path d={p.area} fill={`url(#${uid}-fill-${k})`} />{/if}
                {/each}
              </g>
            {/each}
            {#each drawn as parts, k (k)}
              <g class="series" class:off={series[k].off}>
                {#each parts as p, j (j)}
                  {#if p.line}<path class="line" d={p.line} style:stroke={series[k].color} />{/if}
                  {#if p.dot}<circle cx={p.dot[0]} cy={p.dot[1]} r="2" style:fill={series[k].color} />{/if}
                {/each}
              </g>
            {/each}
          </g>
        </g>
        {#if tip}
          <line class="cross" x1="0" x2="0" y1={PAD.top} y2={base} style:transform={`translateX(${tip.x}px)`} />
          {#each tip.rows as r (r.label)}
            {#if Number.isFinite(r.v)}
              <circle class="mark" r="4" style:stroke={r.color} style:transform={`translate(${tip.x}px, ${y(r.v)}px)`} />
            {/if}
          {/each}
        {/if}
      </svg>
      {#if tip}
        <div class="tip" style:transform={tipAt(tip.x, width)} aria-hidden="true">
          <span class="when">{tip.when}</span>
          {#each tip.rows as r (r.label)}
            <span class="row"><i class="ring" style:color={r.color}></i><span class="ellipsis">{r.label}</span><b class="num">{r.text}</b></span>
          {:else}
            <span class="row">Every line is hidden</span>
          {/each}
        </div>
      {/if}
    {:else if width > 0}
      <p class="waiting">Waiting for the first reading…</p>
    {/if}
  </div>
</div>

<style>
  .time-chart { display: grid; gap: 8px; min-width: 0; }
  .plot { position: relative; width: 100%; min-width: 0; touch-action: pan-y; border-radius: var(--r-sm); cursor: crosshair; }
  .plot:focus-visible { outline-offset: 4px; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--grid); }
  .grid.zero { stroke: var(--border2); }
  .label { fill: var(--muted); font-size: var(--t-micro); }
  .line { fill: none; stroke-width: 2.2; stroke-linejoin: round; stroke-linecap: round; }
  /* A line switched off in the key fades out and keeps its place in the scale. */
  .series { transition: opacity var(--dur-base) var(--ease-out); }
  .series.off { opacity: 0; }
  .cross { stroke: var(--text2); stroke-width: 1; stroke-opacity: 0.6; }
  .mark { fill: var(--surface); stroke-width: 2.5; }
  /* The crosshair, its marks and the tooltip follow the pointer from sample to sample. */
  .cross, .mark, .tip { transition: transform var(--dur-fast) var(--ease-out); }
  .waiting { display: grid; place-items: center; height: 100%; }

  /* The key: each name is a switch for its line. */
  .keys { display: flex; align-items: center; justify-content: space-between; gap: 4px 16px; flex-wrap: wrap; min-width: 0; }
  .toggles { display: flex; flex-wrap: wrap; gap: 2px 4px; min-width: 0; margin-left: -10px; }
  .key {
    gap: 8px; min-height: 30px; padding: 4px 10px; border: 0; border-radius: var(--r-pill); background: transparent;
    color: var(--text); font-size: var(--t-small); font-weight: 400;
    transition-property: background-color, opacity;
  }
  .key:hover:not(:disabled) { background: var(--hover); border: 0; }
  .key[aria-pressed='false'] { opacity: 0.5; text-decoration: line-through; }
  .hint { font-size: var(--t-micro); color: var(--muted); white-space: nowrap; }
  @media (hover: none), (max-width: 720px) { .hint { display: none; } }

  .tip {
    position: absolute; top: 4px; left: 0; z-index: 2; pointer-events: none; display: grid; gap: 4px; min-width: 170px; max-width: min(260px, 70%);
    padding: 10px 12px; border-radius: var(--r-md); background: var(--tip); border: 1px solid var(--tip-edge);
    box-shadow: var(--float); font-size: var(--t-small); color: var(--text2);
  }
  .when { color: var(--text); font-weight: 600; white-space: nowrap; }
  .row { display: flex; align-items: center; gap: 7px; min-width: 0; }
  .row .ring { width: 8px; height: 8px; border-radius: 2px; }
  .row .ellipsis { flex: 1; }
  .row b { color: var(--text); font-weight: 600; white-space: nowrap; margin-left: 10px; }
</style>
