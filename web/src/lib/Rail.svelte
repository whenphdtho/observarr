<script>
  import Icon from './icons/Icon.svelte';
  import { adapters, adapterFor, snapshot } from './data.js';
  import { serviceStatus, serviceLine } from './status.js';
  import { formatBytes, formatPct, formatTotal, formatPart } from './format.js';
  import { mountServices, mountSummary } from './storage.js';
  import { securityServices, activeBans, formatCount } from './security.js';
  import { synced } from './live.js';

  // Rail is every service at a glance plus a short system summary and today's traffic. It sits in the right column on
  // wide screens and inside the Overview on narrower ones. onjump(id) shows a service's card.
  let { services, onjump } = $props();
  const uid = $props.id();

  const host = $derived($snapshot?.host ?? null);
  const containers = $derived($snapshot?.containers ?? []);
  const running = $derived(containers.filter((c) => c.state === 'running').length);
  const disk = $derived(host?.disks?.[0] ?? null);
  const memPct = $derived(host && host.memTotal > 0 ? Math.round((host.memUsed / host.memTotal) * 100) : 0);
  const diskLow = $derived(!!disk && disk.avail < (disk.total - disk.free + disk.avail) * 0.1);
  const online = $derived(services.filter((s) => s.up).length);
  const traffic = $derived($snapshot?.traffic ?? null);
  const budget = $derived(traffic?.budget ?? null);
  // One line each for mounts and bans, only once a service fills those shapes.
  const mounts = $derived(mountSummary(mountServices(services)));
  const blockers = $derived(securityServices(services));
  const bans = $derived(activeBans(blockers));
</script>

<section class="rail-section" aria-labelledby={`${uid}-apps`}>
  <h2 class="cap" id={`${uid}-apps`}>
    Apps
    {#if services.length}<em>{online === services.length ? 'all online' : `${online} of ${services.length} online`}</em>{/if}
  </h2>
  {#if !$synced}
    <p class="muted none">Loading…</p>
  {:else if services.length === 0}
    <p class="muted none">No services to show.</p>
  {:else}
    <ul class="apps">
      {#each services as svc (svc.id)}
        {@const status = serviceStatus(svc)}
        {@const line = serviceLine(svc)}
        {@const lineText = [line.text, line.detail].filter(Boolean).join(' · ')}
        <li>
          <button type="button" class="app row-link {status === 'fail' || status === 'warn' ? status : ''}" onclick={() => onjump(svc.id)} aria-label={`${svc.name}: ${lineText}`}>
            <span class="tile-icon" aria-hidden="true">
              <Icon name={adapterFor($adapters, svc.type)?.icon} size={17} />
              <span class="status-dot {status}"></span>
            </span>
            <span class="text">
              <span class="name ellipsis" title={svc.name}>{svc.name}</span>
              <span class="line ellipsis" title={lineText}>
                {line.text}{#if line.detail}{' · '}<span class={line.tone}>{line.detail}</span>{/if}
              </span>
            </span>
            <span class="chev" aria-hidden="true"><Icon name="chevron-right" size={15} /></span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section class="rail-section" aria-labelledby={`${uid}-system`}>
  <h2 class="cap" id={`${uid}-system`}>System</h2>
  <ul class="sys">
    {#if containers.length}
      <li><a class="srow row-link" href="#/system">
        <span class="dot" class:warn={running < containers.length}></span>
        <span class="ellipsis">Containers</span><b class="num" class:warn-text={running < containers.length}>{running}/{containers.length}</b>
      </a></li>
    {/if}
    {#if host}
      <li><a class="srow row-link" href="#/system">
        <span class="dot" class:warn={host.cpuPercent >= 90}></span><span class="ellipsis">Host CPU</span>
        <b class="num" class:warn-text={host.cpuPercent >= 90}>{formatPct(host.cpuPercent)}</b>
      </a></li>
      <li><a class="srow row-link" href="#/system">
        <span class="dot" class:warn={memPct >= 90}></span><span class="ellipsis">Memory used</span>
        <b class="num" class:warn-text={memPct >= 90}>{formatBytes(host.memUsed)}</b>
      </a></li>
      {#if disk}
        <li><a class="srow row-link" href="#/system" title={disk.path}>
          <span class="dot" class:warn={diskLow}></span><span class="ellipsis">Disk free · {disk.path}</span>
          <b class="num" class:warn-text={diskLow}>{formatBytes(disk.avail)}</b>
        </a></li>
      {/if}
    {/if}
    {#if mounts.readings > 0}
      <li><a class="srow row-link" href="#/storage">
        <span class="dot" class:warn={mounts.healthy < mounts.listed}></span><span class="ellipsis">Mounts healthy</span>
        <b class="num" class:warn-text={mounts.healthy < mounts.listed}>{mounts.healthy}/{mounts.listed}{#if mounts.total > mounts.listed}
          <em>+{mounts.total - mounts.listed} not listed</em>{/if}</b>
      </a></li>
    {/if}
    {#if bans.any}
      <li><a class="srow row-link" href="#/security">
        <span class="dot"></span><span class="ellipsis">Active bans</span>
        <b class="num">{formatCount(bans.bans, bans.capped)}</b>
      </a></li>
    {/if}
    {#if !host && !containers.length}
      <li><a class="srow row-link" href="#/system"><span class="dot"></span><span class="ellipsis">No host figures yet</span></a></li>
    {/if}
  </ul>

  {#if traffic}
    {@const total = traffic.todayRx + traffic.todayTx}
    <a class="budget row-link" href="#/network">
      {#if budget}
        {@const pct = Math.round((budget.usedBytes / budget.limitBytes) * 100)}
        <span class="row1">
          <span>{budget.period === 'day' ? 'Traffic today' : 'Traffic this month'}</span>
          <b class="num">{formatPart(budget.usedBytes, budget.limitBytes)}</b>
        </span>
        <span class="bar" class:warn={pct >= 80 && pct < 100} class:fail={pct >= 100}><i style:width={`${Math.min(100, pct)}%`}></i></span>
        <span class="muted small">{pct}% of the {budget.period === 'day' ? 'daily' : 'monthly'} budget</span>
      {:else}
        <span class="row1"><span>Traffic today</span><b class="num">{formatTotal(total)}</b></span>
        <span class="muted small num">↓ {formatTotal(traffic.todayRx)} · ↑ {formatTotal(traffic.todayTx)}</span>
      {/if}
    </a>
  {/if}
</section>

<style>
  .rail-section + .rail-section { margin-top: 24px; }
  .cap {
    display: flex; justify-content: space-between; align-items: baseline; gap: 8px; margin-bottom: 6px;
    font-size: var(--t-base); font-weight: 600;
  }
  .cap em { font-style: normal; color: var(--muted); font-weight: 400; font-size: var(--t-sm); white-space: nowrap; }
  .none { font-size: var(--t-base); }
  ul { list-style: none; margin: 0; padding: 0; }
  .app {
    width: calc(100% + 16px); margin: 0 -8px; padding: 6px 8px; justify-content: flex-start; gap: 12px;
    background: transparent; border: 0; border-radius: var(--r); color: var(--text); font-weight: 400;
    font-size: var(--t-base); text-align: left;
  }
  .app:hover:not(:disabled) { background: var(--surface2); }
  /* A service with a problem keeps a faint wash of its state colour, so it is found without reading. */
  .app.fail { background: var(--fail-wash); }
  .app.warn { background: var(--warn-wash); }
  .app.fail:hover:not(:disabled) { background: rgba(236, 108, 125, 0.14); }
  .app.warn:hover:not(:disabled) { background: rgba(227, 166, 80, 0.13); }
  .app:focus-visible { outline-offset: 0; }
  .apps { display: grid; gap: 2px; }
  .app .tile-icon { width: 32px; height: 32px; }
  .text { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .name { font-weight: 550; }
  .line { font-size: var(--t-sm); color: var(--muted); }
  .line .warn { color: var(--warn); }
  .line .fail { color: var(--fail); }

  .srow {
    display: flex; align-items: center; gap: 10px; padding: 6px 8px; margin: 0 -8px; border-radius: var(--r);
    font-size: var(--t-base); color: var(--text2); text-decoration: none; min-width: 0;
  }
  .srow:hover { color: var(--text); }
  .srow:focus-visible, .budget:focus-visible { outline-offset: 0; }
  .srow .ellipsis { flex: 1; }
  .srow b { color: var(--text); font-weight: 550; white-space: nowrap; }
  .srow b.warn-text { color: var(--warn); }
  .srow b em { font-style: normal; color: var(--muted); font-weight: 400; font-size: var(--t-sm); }

  .budget {
    margin: 10px -8px 0; display: grid; gap: 6px; padding: 8px; border-radius: var(--r);
    color: var(--text2); text-decoration: none; font-size: var(--t-base);
  }
  .budget:hover { color: var(--text); }
  .row1 { display: flex; justify-content: space-between; align-items: baseline; gap: 10px; }
  .row1 b { color: var(--text); font-weight: 550; white-space: nowrap; }
  .small { font-size: var(--t-sm); }
</style>
