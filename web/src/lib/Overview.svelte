<script>
  import HostChart from './HostChart.svelte';
  import Rail from './Rail.svelte';
  import Icon from './icons/Icon.svelte';
  import { snapshot } from './data.js';
  import { downloading, attention } from './status.js';
  import { formatBytes, formatPct } from './format.js';

  // Overview is the landing view: four key figures, the host chart and what needs attention.
  // services is the (search-filtered) service list; onjump(id) shows a service's card.
  let { services, onjump } = $props();

  const host = $derived($snapshot?.host ?? null);
  const up = $derived(services.filter((s) => s.up).length);
  const checked = $derived(services.filter((s) => s.readings?.health).length);
  const active = $derived(services.reduce((n, s) => n + downloading(s), 0));
  const queued = $derived(services.reduce((n, s) => n + (s.readings?.queue?.data?.total ?? 0), 0));
  const memPct = $derived(host && host.memTotal > 0 ? Math.round((host.memUsed / host.memTotal) * 100) : 0);
  const memParts = $derived(host ? formatBytes(host.memUsed).split(' ') : ['—', '']);
  const issues = $derived(attention(services, $snapshot?.containers ?? []));
  const servicesTone = $derived(checked === 0 ? '' : up === services.length ? 'ok' : up === 0 ? 'fail' : 'warn');
</script>

<div class="overview">
  <div class="kpis">
    <a class="card kpi" href="#/apps">
      <span class="tile-icon {servicesTone}" aria-hidden="true"><Icon name="activity" /></span>
      <span class="kpi-text">
        <span class="v num">{up}<small>of {services.length} up</small></span>
        <span class="s ellipsis">
          {services.length === 0 ? 'No services yet' : up === services.length ? 'All services up' : `${services.length - up} not up`}
        </span>
      </span>
    </a>
    <a class="card kpi" href="#/apps">
      <span class="tile-icon" aria-hidden="true"><Icon name="download" /></span>
      <span class="kpi-text">
        <span class="v num">{active}<small>downloading</small></span>
        <span class="s ellipsis">{queued} in queue{queued === 1 ? '' : 's'}</span>
      </span>
    </a>
    <a class="card kpi" href="#/system">
      <span class="tile-icon" aria-hidden="true"><Icon name="cpu" /></span>
      <span class="kpi-text">
        {#if host}
          <span class="v num">{formatPct(host.cpuPercent)}<small>CPU</small></span>
          <span class="s ellipsis">{host.cpus} core{host.cpus === 1 ? '' : 's'} · load {host.load[0].toFixed(2)}</span>
          <span class="bar" class:warn={host.cpuPercent >= 90}><i style:width={formatPct(Math.min(100, host.cpuPercent))}></i></span>
        {:else}
          <span class="v">—<small>CPU</small></span>
          <span class="s ellipsis">No host figures</span>
        {/if}
      </span>
    </a>
    <a class="card kpi" href="#/system">
      <span class="tile-icon" aria-hidden="true"><Icon name="memory-stick" /></span>
      <span class="kpi-text">
        {#if host}
          <span class="v num">{memParts[0]}<small>{memParts[1]}</small></span>
          <span class="s ellipsis" title={`${formatBytes(host.memUsed)} of ${formatBytes(host.memTotal)} used`}>
            used of {formatBytes(host.memTotal)} · {memPct}%
          </span>
          <span class="bar" class:warn={memPct >= 90}><i style:width={`${memPct}%`}></i></span>
        {:else}
          <span class="v">—<small>memory</small></span>
          <span class="s ellipsis">No host figures</span>
        {/if}
      </span>
    </a>
  </div>

  <HostChart />

  <section class="card" aria-labelledby="attention-title">
    <div class="card-head">
      <h2 id="attention-title"><Icon name="triangle-alert" size={16} />Needs attention</h2>
      {#if issues.length}<span class="chip warn">{issues.length}</span>{/if}
    </div>
    {#if issues.length === 0}
      <div class="all-good">
        <Icon name="circle-check" size={26} />
        <b>All good</b>
        <span>No service down, no warnings, no stuck downloads.</span>
      </div>
    {:else}
      <ul class="issues">
        {#each issues as item (item.key)}
          <li>
            <span class="dot {item.tone}" aria-hidden="true"></span>
            <a href={item.href} class="issue">
              <span class="visually-hidden">{item.tone === 'fail' ? 'Error' : 'Warning'}: </span>
              <span class="title ellipsis" title={item.title}>{item.title}</span>
              {#if item.detail}<span class="detail">{item.detail}</span>{/if}
            </a>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <div class="inline-rail">
    <Rail {services} {onjump} />
  </div>
</div>

<style>
  .overview { display: grid; gap: 16px; }
  .kpis { display: grid; gap: 16px; grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .kpi {
    display: flex; gap: 13px; align-items: center; padding: 15px 16px; color: var(--text); text-decoration: none;
    transition: border-color 140ms var(--ease), background 140ms var(--ease);
  }
  .kpi:hover { border-color: var(--border2); background: var(--surface2); }
  .kpi-text { display: grid; min-width: 0; flex: 1; }
  .v { font-size: 19px; font-weight: 600; letter-spacing: -0.015em; line-height: 1.2; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .v small { font-size: 12.5px; font-weight: 500; color: var(--muted); margin-left: 5px; letter-spacing: 0; }
  .s { font-size: 12.5px; color: var(--muted); }
  .kpi .bar { margin-top: 8px; }

  .all-good { display: grid; justify-items: center; gap: 2px; padding: 18px 12px 12px; text-align: center; color: var(--muted); font-size: 13px; }
  .all-good :global(.icon) { color: var(--ok); margin-bottom: 6px; }
  .all-good b { color: var(--text); font-size: 14.5px; font-weight: 600; }
  .issues { list-style: none; margin: 0; padding: 0; }
  .issues li { display: flex; gap: 11px; align-items: flex-start; padding: 10px 0; border-bottom: 1px solid var(--border); min-width: 0; }
  .issues li:last-child { border-bottom: 0; padding-bottom: 2px; }
  .issues .dot { margin-top: 7px; }
  .issue { display: grid; gap: 2px; min-width: 0; flex: 1; color: var(--text); text-decoration: none; }
  .issue:hover .title { text-decoration: underline; text-underline-offset: 3px; }
  .title { font-weight: 550; font-size: 13.5px; }
  .detail { font-size: 12.5px; color: var(--text2); overflow-wrap: anywhere; }

  .inline-rail { display: none; }
  @media (max-width: 1199px) {
    .inline-rail { display: block; padding: 4px 4px 0; }
  }
  @container (max-width: 820px) {
    .kpis { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  @container (max-width: 400px) {
    .kpis { grid-template-columns: minmax(0, 1fr); gap: 10px; }
  }
  /* Four tiles in a row narrower than about 250px each (a 1280–1536px window with the rail shown):
     the icon goes above the figures, so the captions get the tile's full width instead of being
     cut off. */
  @container (min-width: 821px) and (max-width: 1040px) {
    .kpi { flex-direction: column; align-items: stretch; gap: 10px; padding: 14px 16px; }
    .kpi .tile-icon { width: 32px; height: 32px; }
  }
  /* Phones: a 2×2 grid from 360px up, with the icon above the figures so every value keeps one
     line; a single column below 360px. These come after the container queries so they win. */
  @media (max-width: 720px) and (min-width: 360px) {
    .kpis { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
    .kpi { flex-direction: column; align-items: stretch; gap: 10px; padding: 13px 14px; }
    .kpi .tile-icon { width: 32px; height: 32px; }
    .v small { display: block; margin-left: 0; overflow: hidden; text-overflow: ellipsis; }
    .s { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  }
  @media (max-width: 359px) {
    .kpis { grid-template-columns: minmax(0, 1fr); gap: 10px; }
  }
</style>
