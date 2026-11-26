<script>
  // Kpi is one key figure in a .kpis strip: a label, the value with its unit, a caption and
  // optionally a bar (bar: 0–100, what it shows is also in the caption). tone ('warn' or 'fail')
  // colours the value when it needs attention; anything else leaves it plain. href makes the
  // figure a link. The value and caption keep one line; their full text is in the title.
  // spark (a list of numbers, null for a gap) draws the figure's recent course beside the value, in
  // sparkColor from sparkMin (null: its lowest value) up to sparkMax; change ({ ratio, good, against }, see Delta) adds its change against
  // an earlier period. Either is left out while its data is missing.
  import Icon from './icons/Icon.svelte';
  import Num from './Num.svelte';
  import Spark from './Spark.svelte';
  import Delta from './Delta.svelte';

  // num (a number) with format makes the value glide between readings instead of jumping; value is then only
  // its full text for the title. A value of '—' with a caption of 'Loading…' shows a quiet placeholder.
  let {
    label, value, unit = '', sub = '', tone = '', bar = null, barTone = '', href = null, num = null, format = null,
    spark = null, sparkColor = 'var(--series-2)', sparkMin = 0, sparkMax = null, change = null,
  } = $props();
  const loading = $derived(value === '—' && sub === 'Loading…');
  const hasSpark = $derived(!loading && (spark?.filter(Number.isFinite).length ?? 0) > 1);
  const hasChange = $derived(!loading && change != null && Number.isFinite(change.ratio));

  const full = $derived(unit ? `${value} ${unit}` : String(value));
</script>

<svelte:element this={href ? 'a' : 'div'} class="kpi" class:link={!!href} class:graph={hasSpark} {href}>
  <span class="label">
    <span class="ellipsis">{label}</span>
    {#if href}<span class="chev" aria-hidden="true"><Icon name="chevron-right" size={13} /></span>{/if}
  </span>
  {#if hasChange}<span class="change"><Delta {...change} /></span>{/if}
  <span class="value num {tone === 'warn' || tone === 'fail' ? `${tone}-text` : ''}" title={full}>
    {#if loading}<span class="bone" aria-hidden="true"></span><span class="visually-hidden">Loading…</span>{:else}{#if num !== null && format}<Num value={num} {format} />{:else}{value}{/if}{#if unit}<small>{unit}</small>{/if}{/if}
  </span>
  {#if hasSpark}<span class="spark-slot"><Spark values={spark} color={sparkColor} min={sparkMin} max={sparkMax} height={28} /></span>{/if}
  {#if sub && !loading}<span class="sub ellipsis" title={sub}>{sub}</span>{/if}
  {#if bar !== null}
    <span class="bar {barTone}" aria-hidden="true"><i style:width={`${Math.min(100, Math.max(0, bar))}%`}></i></span>
  {/if}
</svelte:element>

<style>
  .kpi {
    display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-rows: auto auto 1fr; column-gap: 12px; min-width: 0;
    padding: 14px 18px 16px; background: var(--surface); color: var(--text); text-decoration: none;
    container-type: inline-size;
  }
  .kpi > :where(:not(.change):not(.spark-slot)) { grid-column: 1 / -1; }
  .label { grid-row: 1; display: flex; align-items: center; gap: 2px; min-width: 0; font-size: var(--t-small); color: var(--muted); }
  .graph .label, .kpi:has(.change) .label { grid-column: 1; }
  .change { grid-row: 1; grid-column: 2; justify-self: end; align-self: center; }
  .graph .value { grid-column: 1; }
  .spark-slot { grid-row: 2; grid-column: 2; width: 76px; align-self: center; margin-top: 4px; }
  .link { transition: background-color var(--dur-fast) var(--ease-out); }
  .link:hover { background: var(--surface2); }
  .link:focus-visible { outline-offset: -2px; }
  /* A figure that is a link says so at rest: a small chevron after its label. */
  .chev { display: grid; color: var(--muted); transition: color var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out); }
  .link:hover .label { color: var(--text2); }
  .link:hover .chev { color: var(--text); transform: translateX(2px); }
  .value {
    grid-row: 2; font-size: var(--t-figure); font-weight: 600; letter-spacing: var(--track-figure); line-height: 1.35; margin-top: 4px;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .value small { font-size: var(--t-small); font-weight: 400; color: var(--muted); margin-left: 4px; letter-spacing: 0; }
  /* The caption keeps to the foot of the figure, so the captions of a strip line up with or without a line above. */
  .sub { grid-row: 3; align-self: end; font-size: var(--t-small); color: var(--muted); }
  .bar { margin-top: 8px; }
  .bone { display: inline-block; width: 64px; height: 18px; margin: 4px 0; border-radius: 4px; background: var(--surface2); vertical-align: middle; }
  /* A narrow figure puts its line below the value, across the whole width. */
  @container (max-width: 200px) {
    .graph .value { grid-column: 1 / -1; }
    .graph { grid-template-rows: auto auto auto 1fr; }
    .spark-slot { grid-row: 3; grid-column: 1 / -1; width: 100%; margin: 6px 0 2px; }
    .graph .sub { grid-row: 4; }
  }
  @media (max-width: 720px) {
    .kpi { padding: 12px 14px 14px; }
  }
</style>
