<script>
  // Kpi is one key figure in a .kpis strip: a label, the value with its unit, a caption and
  // optionally a bar (bar: 0–100, what it shows is also in the caption). tone ('warn' or 'fail')
  // colours the value when it needs attention; anything else leaves it plain. href makes the
  // figure a link. The value and caption keep one line; their full text is in the title.
  import Icon from './icons/Icon.svelte';

  let { label, value, unit = '', sub = '', tone = '', bar = null, barTone = '', href = null } = $props();

  const full = $derived(unit ? `${value} ${unit}` : String(value));
</script>

<svelte:element this={href ? 'a' : 'div'} class="kpi" class:link={!!href} {href}>
  <span class="label">
    <span class="ellipsis">{label}</span>
    {#if href}<span class="chev" aria-hidden="true"><Icon name="chevron-right" size={13} /></span>{/if}
  </span>
  <span class="value num {tone === 'warn' || tone === 'fail' ? `${tone}-text` : ''}" title={full}>
    {value}{#if unit}<small>{unit}</small>{/if}
  </span>
  {#if sub}<span class="sub ellipsis" title={sub}>{sub}</span>{/if}
  {#if bar !== null}
    <span class="bar {barTone}" aria-hidden="true"><i style:width={`${Math.min(100, Math.max(0, bar))}%`}></i></span>
  {/if}
</svelte:element>

<style>
  .kpi {
    display: grid; align-content: start; min-width: 0; padding: 12px 16px 14px; background: var(--bg);
    color: var(--text); text-decoration: none;
  }
  .link { transition: background 140ms var(--ease); }
  .link:hover { background: var(--surface2); }
  .link:focus-visible { outline-offset: -2px; }
  .label { display: flex; align-items: center; gap: 2px; min-width: 0; font-size: var(--t-sm); color: var(--muted); }
  /* A figure that is a link says so at rest: a small chevron after its label. */
  .chev { display: grid; color: var(--muted); transition: color 140ms var(--ease), transform 140ms var(--ease); }
  .link:hover .label { color: var(--text2); }
  .link:hover .chev { color: var(--text); transform: translateX(2px); }
  .value {
    font-size: var(--t-xl); font-weight: 600; letter-spacing: -0.01em; line-height: 1.35; margin-top: 2px;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .value small { font-size: var(--t-sm); font-weight: 450; color: var(--muted); margin-left: 4px; letter-spacing: 0; }
  .sub { font-size: var(--t-sm); color: var(--muted); }
  .bar { margin-top: 8px; }
  @media (max-width: 720px) {
    .kpi { padding: 10px 12px 12px; }
  }
</style>
