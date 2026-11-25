<script>
  import { niceCeil, tipSide } from './chart.js';
  import { formatTotal } from './format.js';

  // DailyBars shows traffic per day as bars, what came in below what went out, with the budget per day as a
  // line over them. days are [{ day: "YYYY-MM-DD", in, out }] in bytes, oldest first, today last (still
  // running), null for a day without readings; limits holds the budget of each day in bytes (null without a
  // budget). A day over its budget gets a red cap. Pointing at a day (or the arrow keys) reads its figures.
  let { days, limits = null, limitLabel = 'Budget', label, height = 220 } = $props();

  const PAD = { top: 12, right: 8, bottom: 26, left: 44 };
  let width = $state(0);
  let hover = $state(-1);
  let keyed = $state(false); // the column was picked by a key, so it stays when the pointer leaves

  const plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
  const plotH = $derived(height - PAD.top - PAD.bottom);
  const totals = $derived(days.map((d) => (d.in || 0) + (d.out || 0)));
  const peak = $derived(Math.max(1, ...totals, ...(limits ?? []).filter(Number.isFinite)));
  const stepV = $derived(niceCeil(peak / 4));
  const top = $derived(stepV * Math.ceil(peak / stepV));
  const yTicks = $derived(Array.from({ length: Math.round(top / stepV) + 1 }, (_, i) => i * stepV));
  const slot = $derived(plotW / Math.max(1, days.length));
  const barW = $derived(Math.max(2, Math.min(18, slot * 0.62)));
  const y = (v) => PAD.top + plotH - (Math.max(0, v) / top) * plotH;
  const cx = (i) => PAD.left + slot * (i + 0.5);
  const over = (i) => Number.isFinite(limits?.[i]) && totals[i] > limits[i];

  const date = (day) => new Date(`${day}T12:00:00`);
  const short = (day) => date(day).toLocaleDateString([], { day: 'numeric', month: 'short' });
  const long = (day) => date(day).toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
  // A label every week, counted back from today so today is always named.
  const every = $derived(Math.max(1, Math.ceil(days.length / Math.max(1, Math.floor(plotW / 64)))));
  const labelled = $derived(days.map((_, i) => (days.length - 1 - i) % every === 0));

  const linePath = $derived.by(() => {
    if (!limits) return '';
    let d = '';
    let open = false;
    limits.forEach((v, i) => {
      if (!Number.isFinite(v)) {
        open = false;
        return;
      }
      const a = (i === 0 ? PAD.left : cx(i) - slot / 2).toFixed(1);
      const b = (i === days.length - 1 ? PAD.left + plotW : cx(i) + slot / 2).toFixed(1);
      d += `${open ? 'L' : 'M'}${a},${y(v).toFixed(1)}L${b},${y(v).toFixed(1)}`;
      open = true;
    });
    return d;
  });

  function figures(i) {
    const d = days[i];
    const limit = limits?.[i];
    if (!Number.isFinite(d.in) && !Number.isFinite(d.out)) {
      return { x: cx(i), when: long(d.day), none: true, rx: '', tx: '', total: '', limit: '', over: false };
    }
    return {
      x: cx(i), when: `${long(d.day)}${i === days.length - 1 ? ' · so far' : ''}`,
      rx: formatTotal(d.in || 0), tx: formatTotal(d.out || 0), total: formatTotal(totals[i]),
      limit: Number.isFinite(limit) ? `${Math.round((totals[i] / limit) * 100)}% of ${formatTotal(limit)}` : '',
      over: over(i),
    };
  }
  const tip = $derived(hover >= 0 && hover < days.length ? figures(hover) : null);
  // The day the keys stand on (today before any), and its words for a screen reader.
  const now = $derived(hover >= 0 ? hover : Math.max(0, days.length - 1));
  const spoken = $derived.by(() => {
    if (!days.length) return '';
    const f = figures(now);
    if (f.none) return `${f.when}: no readings`;
    return `${f.when}: ${f.rx} in, ${f.tx} out, ${f.total} in all${f.limit ? `, ${f.limit}` : ''}${f.over ? ', over budget' : ''}`;
  });
  const side = $derived(tip ? tipSide(tip.x, width) : null);

  // column is a bar from y0 (top) down to y1, its top corners rounded when it is the top of the stack.
  function column(x0, y0, y1, round) {
    const h = y1 - y0;
    if (!(h > 0)) return '';
    const r = round ? Math.min(4, barW / 2, h) : 0;
    const f = (v) => v.toFixed(1);
    return `M${f(x0)},${f(y1)}V${f(y0 + r)}Q${f(x0)},${f(y0)} ${f(x0 + r)},${f(y0)}H${f(x0 + barW - r)}` +
      `Q${f(x0 + barW)},${f(y0)} ${f(x0 + barW)},${f(y0 + r)}V${f(y1)}Z`;
  }

  function axis(v) {
    const gb = v / 1e9;
    if (gb >= 1000) return `${+(gb / 1000).toFixed(1)} TB`;
    return gb >= 10 || gb === 0 ? `${Math.round(gb)}` : `${+gb.toFixed(1)}`;
  }

  function point(event) {
    const box = event.currentTarget.getBoundingClientRect();
    const px = event.clientX - box.left - PAD.left;
    if (px < 0 || px > plotW) {
      hover = -1;
      return;
    }
    keyed = false;
    hover = Math.min(days.length - 1, Math.floor(px / slot));
  }

  function key(event) {
    const n = days.length;
    let i = hover < 0 ? n - 1 : hover;
    if (event.key === 'ArrowLeft') i = Math.max(0, i - 1);
    else if (event.key === 'ArrowRight') i = Math.min(n - 1, i + 1);
    else if (event.key === 'Home') i = 0;
    else if (event.key === 'End') i = n - 1;
    else if (event.key === 'Escape') i = -1;
    else return;
    event.preventDefault();
    keyed = true;
    hover = i;
  }
</script>

<div class="daily">
  <!-- A slider over the days: the arrow keys move between them, and each day is read out. -->
  <div class="plot" bind:clientWidth={width} style:height={`${height}px`} role="slider" aria-roledescription="chart"
    aria-label={label} tabindex="0" aria-orientation="horizontal"
    aria-valuemin={0} aria-valuemax={Math.max(0, days.length - 1)} aria-valuenow={now} aria-valuetext={spoken}
    onpointermove={point} onpointerdown={point} onpointerleave={() => { if (!keyed) hover = -1; }}
    onkeydown={key} onblur={() => { hover = -1; keyed = false; }}>
    {#if width > 0}
      <svg {width} {height} aria-hidden="true">
        {#each yTicks as v (v)}
          <line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={y(v)} y2={y(v)} />
          <text class="label" x={PAD.left - 10} y={y(v)} text-anchor="end" dominant-baseline="middle">{axis(v)}</text>
        {/each}
        {#if hover >= 0}
          <rect class="col" x={cx(hover) - slot / 2} y={PAD.top} width={slot} height={plotH} rx="4" />
        {/if}
        {#each days as d, i (d.day)}
          {@const x0 = cx(i) - barW / 2}
          {@const yIn = y(d.in || 0)}
          {@const yAll = y(totals[i])}
          {#if totals[i] > 0}
            <path class="bar" d={column(x0, yIn, y(0), !(d.out > 0))} style:fill="var(--down)" />
            {#if d.out > 0}<path class="bar" d={column(x0, yAll, yIn, true)} style:fill="var(--up)" />{/if}
            {#if over(i)}<rect class="cap" x={x0} y={yAll - 5} width={barW} height="3" rx="1.5" />{/if}
          {/if}
          {#if labelled[i]}
            <text class="label" x={cx(i)} y={height - 7} text-anchor="middle">{short(d.day)}</text>
          {/if}
        {/each}
        {#if linePath}<path class="limit" d={linePath} />{/if}
      </svg>
      {#if tip}
        <div class="tip" style:left={side.left} style:right={side.right} aria-hidden="true">
          <span class="when">{tip.when}</span>
          {#if tip.none}<span class="note">No readings</span>{:else}
          <span class="row"><i class="ring" style:color="var(--down)"></i><span>In</span><b class="num">{tip.rx}</b></span>
          <span class="row"><i class="ring" style:color="var(--up)"></i><span>Out</span><b class="num">{tip.tx}</b></span>
          <span class="row total"><span></span><span>Total</span><b class="num">{tip.total}</b></span>
          {#if tip.limit}<span class="note" class:fail-text={tip.over}>{tip.limit}{tip.over ? ' · over budget' : ''}</span>{/if}
          {/if}
        </div>
      {/if}
    {/if}
  </div>
  <ul class="keys" aria-label="Key">
    <li><i class="ring" style:color="var(--down)"></i>In</li>
    <li><i class="ring" style:color="var(--up)"></i>Out</li>
    {#if linePath}<li><i class="line-key" aria-hidden="true"></i>{limitLabel}</li>{/if}
    <li class="unit">GB per day</li>
  </ul>
</div>

<style>
  .daily { display: grid; gap: 12px; min-width: 0; }
  .plot { position: relative; width: 100%; min-width: 0; touch-action: pan-y; border-radius: var(--r-sm); }
  .plot:focus-visible { outline-offset: 4px; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--grid); }
  .label { fill: var(--muted); font-size: var(--t-micro); }
  .col { fill: var(--surface2); }
  .bar { fill-opacity: 0.9; }
  .cap { fill: var(--fail); }
  .limit { fill: none; stroke: var(--series-1); stroke-width: 2; stroke-linejoin: round; }
  .line-key { width: 16px; height: 2px; border-radius: 1px; background: var(--series-1); flex: none; }
  .keys { padding-left: 44px; }
  .keys .unit { margin-left: auto; color: var(--muted); }
  @media (max-width: 720px) { .keys { padding-left: 0; } }

  .tip {
    position: absolute; top: 4px; z-index: 2; pointer-events: none; display: grid; gap: 4px; min-width: 160px;
    padding: 9px 12px; border-radius: var(--r); background: var(--surface3); border: 1px solid var(--border2);
    box-shadow: var(--float); font-size: var(--t-small); color: var(--text2);
  }
  .when { color: var(--text); font-weight: 600; white-space: nowrap; }
  .row { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto; gap: 7px; align-items: center; }
  .row b { color: var(--text); font-weight: 600; white-space: nowrap; margin-left: 10px; }
  .row.total { border-top: 1px solid var(--border2); padding-top: 4px; margin-top: 2px; }
  .note { color: var(--muted); white-space: nowrap; }
</style>
