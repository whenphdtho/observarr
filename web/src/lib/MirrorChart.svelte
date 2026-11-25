<script>
  import { monotonePath, monotoneBack } from './curve.js';
  import { niceCeil, timeTicks, tickLabel, stampLabel, nearest, runs, tipSide, thin } from './chart.js';
  import { chartShift } from './motion.js';
  import { step as stepOf } from './history.js';
  import { formatMbit } from './format.js';

  // MirrorChart stacks traffic sources over time in Mbit/s as smooth translucent bands: incoming above the
  // zero line, outgoing below it, on one scale, with a crosshair that reads every source at a moment. times
  // are ms since epoch; each layer is { label, color, series: { rx, tx } } with rates in bytes per second, one
  // per time. A moment no layer has a value for (null) is a gap in the history; otherwise a missing value
  // counts as zero. live makes the drawing glide as new readings push the window on. More samples than pixels
  // are merged into their means for drawing.
  let { times: allTimes, layers: allLayers, label, height = 290, live = false } = $props();

  const PAD = { top: 10, right: 8, bottom: 26, left: 44 };
  const MIN_SIDE = 0.18; // the smaller side keeps at least this share of the height, so it stays readable
  let width = $state(0);
  let hover = $state(-1);
  let keyed = $state(false); // the crosshair was put by a key, so it stays when the pointer leaves
  const uid = $props.id();

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const thinned = $derived(thin(allTimes, allLayers.flatMap((l) => [l.series?.rx ?? null, l.series?.tx ?? null]), Math.max(60, Math.floor(plotW))));
  const times = $derived(thinned.times);
  const layers = $derived(allLayers.map((l, k) => ({ ...l, series: l.series ? { rx: thinned.lists[2 * k], tx: thinned.lists[2 * k + 1] } : null })));

  const mbit = (v) => ((Number.isFinite(v) ? Math.max(0, v) : 0) * 8) / 1e6;
  const has = (i) => layers.some((l) => Number.isFinite(l.series?.rx?.[i]) || Number.isFinite(l.series?.tx?.[i]));
  const parts = $derived(runs(times.length, has));

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

  const plotH = $derived(height - PAD.top - PAD.bottom);
  // The zero line: each side's height follows its scale, within MIN_SIDE of the whole.
  const zero = $derived(PAD.top + plotH * Math.min(1 - MIN_SIDE, Math.max(MIN_SIDE, inTop / (inTop + outTop))));
  const t0 = $derived(times[0] ?? 0);
  const span = $derived(Math.max(1, (times[times.length - 1] ?? 0) - t0));
  const sampleStep = $derived(stepOf(times));
  const yTicks = $derived.by(() => {
    const out = [];
    for (let v = step; v <= inTop + 1e-9; v += step) out.push(v);
    for (let v = step; v <= outTop + 1e-9; v += step) out.push(-v);
    return [0, ...out];
  });

  const x = (i) => PAD.left + ((times[i] - t0) / span) * plotW;
  const y = (v) => (v >= 0 ? zero - (v / inTop) * (zero - PAD.top) : zero + (-v / outTop) * (PAD.top + plotH - zero));

  // band is the area between a layer's cumulative line and the one below it (or the zero line) over one run,
  // both edges smooth; edge is its outer line alone.
  function band(top, below, sign, run) {
    const pts = (vals) => run.map((i) => [x(i), y(sign * (vals ? vals[i] : 0))]);
    const upper = pts(top);
    const lower = pts(below);
    if (upper.length < 2) return '';
    const last = lower[lower.length - 1];
    return `${monotonePath(upper)}L${last[0].toFixed(1)},${last[1].toFixed(1)}${monotoneBack(lower)}Z`;
  }
  const bands = $derived(layers.map((_, k) => ({
    rx: parts.map((run) => band(stacks.rx[k], stacks.rx[k - 1], 1, run)).join(''),
    tx: parts.map((run) => band(stacks.tx[k], stacks.tx[k - 1], -1, run)).join(''),
    edgeRx: parts.map((run) => monotonePath(run.map((i) => [x(i), y(stacks.rx[k][i])]))).join(''),
    edgeTx: parts.map((run) => monotonePath(run.map((i) => [x(i), y(-stacks.tx[k][i])]))).join(''),
  })));

  // Thin out the y labels when they would crowd each other.
  const labelEvery = $derived(Math.max(1, Math.ceil(16 / Math.max(1, (step / inTop) * (zero - PAD.top)))));
  const shownTicks = $derived(yTicks.filter((v) => Math.round(Math.abs(v) / step) % labelEvery === 0));
  const xTicks = $derived(timeTicks(t0, t0 + span));

  function axis(v) {
    const a = Math.abs(v);
    return a < 10 && a !== Math.round(a) ? a.toFixed(1) : String(Math.round(a));
  }

  const rate = (v) => (Number.isFinite(v) ? formatMbit(v) : '—');
  const tip = $derived.by(() => {
    if (hover < 0 || hover >= times.length) return null;
    const i = hover;
    const gap = !has(i);
    const rows = gap ? [] : layers
      .map((l) => ({ label: l.label, color: l.color, rx: l.series?.rx?.[i], tx: l.series?.tx?.[i] }))
      .filter((r) => (r.rx || 0) + (r.tx || 0) > 0);
    const sum = (key) => layers.reduce((n, l) => n + (Number.isFinite(l.series?.[key]?.[i]) ? l.series[key][i] : 0), 0);
    return { x: x(i), when: stampLabel(times[i], sampleStep, span), gap, rows, rx: sum('rx'), tx: sum('tx') };
  });
  const side = $derived(tip ? tipSide(tip.x, width) : null);
  // The moment the keys stand on (the latest before any), and its words for a screen reader.
  const now = $derived(hover >= 0 ? hover : Math.max(0, times.length - 1));
  const spoken = $derived.by(() => {
    if (!times.length) return '';
    const when = stampLabel(times[now], sampleStep, span);
    if (!has(now)) return `${when}: no reading`;
    const all = (key) => layers.reduce((n, l) => n + (Number.isFinite(l.series?.[key]?.[now]) ? l.series[key][now] : 0), 0);
    const each = layers.filter((l) => (l.series?.rx?.[now] || 0) + (l.series?.tx?.[now] || 0) > 0)
      .map((l) => `${l.label} ${rate(l.series.rx[now])} in, ${rate(l.series.tx[now])} out`);
    return `${when}: ${rate(all('rx'))} Mbit/s in, ${rate(all('tx'))} Mbit/s out; ${each.join('; ')}`;
  });

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

<div class="mirror">
<!-- A slider over the samples: the arrow keys move the crosshair, and each moment is read out. -->
<div class="chart" bind:clientWidth={width} style:height={`${height}px`} role="slider" aria-roledescription="chart"
  aria-label={label} tabindex={times.length > 1 ? 0 : -1} aria-orientation="horizontal"
  aria-valuemin={0} aria-valuemax={Math.max(0, times.length - 1)} aria-valuenow={now} aria-valuetext={spoken}
  onpointermove={point} onpointerdown={point} onpointerleave={() => { if (!keyed) hover = -1; }}
  onkeydown={key} onblur={() => { hover = -1; keyed = false; }}>
  {#if width > 0 && times.length > 1}
    <svg {width} {height} aria-hidden="true">
      <defs>
        {#each layers as l, k (k)}
          <linearGradient id={`${uid}-in-${k}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style:stop-color={l.color} style:stop-opacity="var(--band-hi)" />
            <stop offset="1" style:stop-color={l.color} style:stop-opacity="var(--band-lo)" />
          </linearGradient>
          <linearGradient id={`${uid}-out-${k}`} x1="0" y1="1" x2="0" y2="0">
            <stop offset="0" style:stop-color={l.color} style:stop-opacity="var(--band-hi)" />
            <stop offset="1" style:stop-color={l.color} style:stop-opacity="var(--band-lo)" />
          </linearGradient>
        {/each}
        <clipPath id={`${uid}-plot`}><rect x={PAD.left} y={0} width={plotW} height={height}></rect></clipPath>
      </defs>
      {#each shownTicks as v (v)}
        <line class="grid" class:zero={v === 0} x1={PAD.left} x2={PAD.left + plotW} y1={y(v)} y2={y(v)} />
        <text class="label" x={PAD.left - 8} y={y(v)} text-anchor="end" dominant-baseline="middle">{axis(v)}</text>
      {/each}
      <g clip-path={`url(#${uid}-plot)`}>
        <g use:chartShift={live ? { t0, span, width: plotW } : null}>
          {#each bands as b, k (k)}
            <path class="band" d={b.rx} fill={`url(#${uid}-in-${k})`} />
            <path class="band" d={b.tx} fill={`url(#${uid}-out-${k})`} />
          {/each}
          {#each bands as b, k (k)}
            <path class="edge" d={b.edgeRx} style:stroke={layers[k].color} />
            <path class="edge" d={b.edgeTx} style:stroke={layers[k].color} />
          {/each}
        </g>
      </g>
      <text class="side" x={PAD.left + 8} y={PAD.top + 12}>↓ In</text>
      <text class="side" x={PAD.left + 8} y={PAD.top + plotH - 6}>↑ Out</text>
      {#each xTicks as t (t)}
        {@const tx = PAD.left + ((t - t0) / span) * plotW}
        {#if tx > PAD.left + 20 && tx < PAD.left + plotW - 20}
          <text class="label" x={tx} y={height - 7} text-anchor="middle">{tickLabel(t, span)}</text>
        {/if}
      {/each}
      {#if tip}
        <line class="cross" x1={tip.x} x2={tip.x} y1={PAD.top} y2={PAD.top + plotH} />
      {/if}
    </svg>
    {#if tip}
      <div class="tip" style:left={side.left} style:right={side.right} aria-hidden="true">
        <span class="when">{tip.when}</span>
        {#if tip.gap}
          <span class="muted">No reading</span>
        {:else}
          <span class="row head"><span>Mbit/s</span><b>↓ in</b><b>↑ out</b></span>
          {#each tip.rows as r (r.label)}
            <span class="row"><span class="who"><i class="ring" style:color={r.color}></i><span class="ellipsis">{r.label}</span></span>
              <b class="num">{rate(r.rx)}</b><b class="num">{rate(r.tx)}</b></span>
          {/each}
          <span class="row total"><span>All</span><b class="num">{rate(tip.rx)}</b><b class="num">{rate(tip.tx)}</b></span>
        {/if}
      </div>
    {/if}
  {:else if width > 0}
    <p class="waiting">Waiting for the first reading…</p>
  {/if}
</div>
</div>

<style>
  .mirror { min-width: 0; }
  .chart { position: relative; width: 100%; min-width: 0; touch-action: pan-y; border-radius: var(--r-sm); }
  .chart:focus-visible { outline-offset: 4px; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--grid); }
  .grid.zero { stroke: var(--field-border); }
  .label { fill: var(--muted); font-size: var(--t-micro); }
  .side { fill: var(--text2); font-size: var(--t-micro); font-weight: 500; paint-order: stroke; stroke: var(--surface); stroke-width: 3px; }
  .edge { fill: none; stroke-width: 1.25; stroke-linejoin: round; }
  .cross { stroke: var(--text2); stroke-width: 1; stroke-dasharray: 3 3; }
  .waiting { display: grid; place-items: center; height: 100%; }

  .tip {
    position: absolute; top: 4px; z-index: 2; pointer-events: none; display: grid; gap: 4px; width: max-content;
    max-width: min(280px, 78%); padding: 9px 12px; border-radius: var(--r); background: var(--tip);
    border: 1px solid var(--tip-edge); box-shadow: var(--float); font-size: var(--t-small); color: var(--text2);
  }
  .when { color: var(--text); font-weight: 600; white-space: nowrap; }
  .row { display: grid; grid-template-columns: minmax(0, 1fr) 6ch 6ch; gap: 8px; align-items: center; }
  .row b { color: var(--text); font-weight: 600; text-align: right; white-space: nowrap; }
  .row.head { color: var(--muted); }
  .row.head b { color: var(--muted); font-weight: 500; }
  .row.total { border-top: 1px solid var(--border2); padding-top: 4px; margin-top: 2px; }
  .who { display: flex; align-items: center; gap: 7px; min-width: 0; }
</style>
