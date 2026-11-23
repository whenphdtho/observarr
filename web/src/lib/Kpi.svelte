<script>
  import Icon from './icons/Icon.svelte';

  // Kpi is one key figure in a .kpis row: an icon, a label, the value with its unit, a caption and
  // optionally a bar (bar: 0–100, what it shows is also in the caption). href makes the tile a link.
  // The value and caption keep one line; their full text is in the title.
  let { icon, label, value, unit = '', sub = '', tone = '', bar = null, barTone = '', href = null } = $props();

  const full = $derived(unit ? `${value} ${unit}` : String(value));
</script>

<svelte:element this={href ? 'a' : 'div'} class="card kpi" class:link={!!href} {href}>
  <span class="tile-icon {tone}" aria-hidden="true"><Icon name={icon} /></span>
  <span class="text">
    <span class="label ellipsis">{label}</span>
    <span class="value num" title={full}>{value}{#if unit}<small>{unit}</small>{/if}</span>
    {#if sub}<span class="sub ellipsis" title={sub}>{sub}</span>{/if}
    {#if bar !== null}
      <span class="bar {barTone}" aria-hidden="true"><i style:width={`${Math.min(100, Math.max(0, bar))}%`}></i></span>
    {/if}
  </span>
</svelte:element>

<style>
  .kpi { display: flex; gap: 13px; align-items: flex-start; padding: 15px 16px; color: var(--text); text-decoration: none; }
  .link { transition: border-color 140ms var(--ease), background 140ms var(--ease); }
  .link:hover { border-color: var(--border2); background: var(--surface2); }
  .text { display: grid; min-width: 0; flex: 1; }
  .label { font-size: 12.5px; color: var(--muted); font-weight: 500; }
  .value {
    font-size: 20px; font-weight: 600; letter-spacing: -0.015em; line-height: 1.3;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .value small { font-size: 12.5px; font-weight: 500; color: var(--muted); margin-left: 4px; letter-spacing: 0; }
  .sub { font-size: 12.5px; color: var(--muted); }
  .bar { margin-top: 8px; }

  /* Four tiles narrower than about 250px each (a 1280–1536px window with the rail shown), and
     phones: the icon moves to the top right corner, so the figures keep the tile's full width. */
  @container (min-width: 821px) and (max-width: 1040px) {
    .kpi { position: relative; }
    .tile-icon { position: absolute; top: 12px; right: 12px; width: 30px; height: 30px; }
    .label { padding-right: 36px; }
  }
  @media (max-width: 720px) {
    .kpi { position: relative; padding: 13px 14px; }
    .tile-icon { position: absolute; top: 11px; right: 11px; width: 30px; height: 30px; }
    .label { padding-right: 36px; }
  }
</style>
