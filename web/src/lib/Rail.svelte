<script>
  import { flip } from 'svelte/animate';
  import AppIcon from './AppIcon.svelte';
  import Skeleton from './Skeleton.svelte';
  import { adapters, adapterFor, snapshot } from './data.js';
  import { serviceLine } from './status.js';
  import { formatPct, formatPart, formatSize } from './format.js';
  import { diskParts, percentOf } from './storage.js';
  import { synced, attention } from './live.js';
  import { recall, remember } from './prefs.js';
  import { arrive, leave, flash, smoothHeight, FLIP } from './motion.js';

  // Rail is the apps column: by default only the apps that need someone, worst first, and on request every app,
  // with what each is doing; below them the server in four lines. It sits in the right column on wide screens and
  // inside the Overview on narrower ones, and never folds away. Whether it shows all apps is remembered in this
  // browser. onjump(id) shows an app's card.
  let { services, onjump } = $props();
  const uid = $props.id();

  let all = $state(recall('apps.all', false));
  function toggle() {
    all = !all;
    remember('apps.all', all);
  }

  const ORDER = { fail: 0, warn: 1, idle: 2, unknown: 2, ok: 3 };
  const apps = $derived(services.map((svc) => ({ svc, ...serviceLine(svc, $attention) }))
    .sort((a, b) => ORDER[a.tone] - ORDER[b.tone] || a.svc.name.localeCompare(b.svc.name)));
  const needy = $derived(apps.filter((a) => a.tone === 'fail' || a.tone === 'warn'));
  const list = $derived(all ? apps : needy);

  const host = $derived($snapshot?.host ?? null);
  const containers = $derived($snapshot?.containers ?? []);
  const running = $derived(containers.filter((c) => c.state === 'running').length);
  const traffic = $derived($snapshot?.traffic ?? null);
  // The fullest disk is the one worth a line.
  const disk = $derived((host?.disks ?? []).map((d) => ({ path: d.path, ...diskParts(d) })).sort((a, b) => b.pct - a.pct)[0] ?? null);
  const memPct = $derived(host ? percentOf(host.memUsed, host.memTotal) : 0);
</script>

<section class="rail-section" aria-labelledby={`${uid}-apps`}>
  <h2 id={`${uid}-apps`}>{$synced && services.length === 0 ? 'Apps' : all ? 'All apps' : 'Need you'}</h2>
  {#if !$synced}
    <Skeleton kind="rows" count={3} label="Loading the apps…" />
  {:else if services.length === 0}
    <p class="none">None here yet.</p>
  {:else}
    <div use:smoothHeight>
      <div>
        {#if list.length === 0}
          <p class="none settle"><span class="dot ok" aria-hidden="true"></span>No app needs you.</p>
        {/if}
        <ul class="apps">
          {#each list as { svc, text, tone } (svc.id)}
            <li in:arrive out:leave animate:flip={FLIP}>
              <button type="button" class="app row-link" onclick={() => onjump(svc.id)} title={`${svc.name}: ${text}`} use:flash={tone}>
                <span class="tile-icon" aria-hidden="true">
                  <AppIcon adapter={adapterFor($adapters, svc.type)} size={19} />
                  <span class="status-dot {tone}"></span>
                </span>
                <span class="text">
                  <span class="name ellipsis">{svc.name}</span>
                  <span class="line ellipsis {tone}">{text}</span>
                </span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    </div>
    {#if needy.length < apps.length}
      <button type="button" class="more" aria-expanded={all} onclick={toggle}>
        {all ? 'Show only apps that need you' : `Show all ${apps.length} apps`}
      </button>
    {/if}
  {/if}
</section>

{#if host || containers.length || traffic}
  <section class="rail-section" aria-labelledby={`${uid}-server`}>
    <h2 id={`${uid}-server`}>Server</h2>
    <ul class="server">
      {#if host}
        <li><a class="fact row-link" href="#/system">
          <span class="label">Processor</span><b class="num" class:warn-text={host.cpuPercent >= 90}>{formatPct(host.cpuPercent)}</b>
          <span class="bar" class:warn={host.cpuPercent >= 90} aria-hidden="true"><i style:width={`${Math.min(100, host.cpuPercent)}%`}></i></span>
        </a></li>
        <li><a class="fact row-link" href="#/system">
          <span class="label">Memory</span><b class="num" class:warn-text={memPct >= 90}>{formatPart(host.memUsed, host.memTotal)}</b>
          <span class="bar" class:warn={memPct >= 90} aria-hidden="true"><i style:width={`${memPct}%`}></i></span>
        </a></li>
        {#if disk}
          <li><a class="fact row-link" href="#/storage" title={`${disk.path}: ${formatSize(disk.avail)} free`}>
            <span class="label ellipsis">Disk {disk.path}</span><b class="num" class:warn-text={disk.pct >= 90}>{disk.pct}%</b>
            <span class="bar" class:warn={disk.pct >= 90} aria-hidden="true"><i style:width={`${disk.pct}%`}></i></span>
          </a></li>
        {/if}
      {/if}
      {#if containers.length}
        <li><a class="fact row-link" href="#/system">
          <span class="label">Containers</span><b class="num" class:warn-text={running < containers.length}>{running} of {containers.length}</b>
          <span class="bar" class:warn={running < containers.length} aria-hidden="true"><i style:width={`${percentOf(running, containers.length)}%`}></i></span>
        </a></li>
      {/if}
      {#if traffic}
        <li><a class="fact row-link" href="#/network">
          <span class="label">Traffic today</span><b class="num">{formatSize(traffic.todayRx + traffic.todayTx)}</b>
        </a></li>
      {/if}
    </ul>
  </section>
{/if}

<style>
  .rail-section + .rail-section { margin-top: 26px; }
  h2 { margin: 0 6px 10px; font-size: 16px; }
  ul { list-style: none; margin: 0; padding: 0; }
  .none { display: flex; align-items: center; gap: 9px; padding: 8px 6px; font-size: var(--t-body); color: var(--muted); }
  .apps { display: grid; gap: 4px; }
  .app {
    width: 100%; padding: 7px 6px; justify-content: flex-start; gap: 12px; min-height: 0;
    background: transparent; border: 0; border-radius: var(--r-md); color: var(--text); font-weight: 400; text-align: left;
  }
  .app:hover:not(:disabled) { background: var(--hover); }
  .app:focus-visible { outline-offset: 0; }
  .app .tile-icon { border-radius: 11px; }
  .text { display: grid; min-width: 0; flex: 1; line-height: 1.4; }
  .name { font-weight: 600; }
  .line { font-size: var(--t-small); color: var(--muted); }
  .line.warn { color: var(--warn-ink); }
  .line.fail { color: var(--fail-ink); }
  .more {
    margin-top: 8px; width: 100%; justify-content: flex-start; padding: 8px; border: 0; border-radius: var(--r);
    background: transparent; color: var(--accent-text); font-weight: 500;
  }
  .more:hover:not(:disabled) { background: var(--hover); border: 0; color: var(--accent-text-hi); }

  .server { display: grid; gap: 2px; }
  .fact {
    display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 6px 12px; align-items: baseline; padding: 7px 6px 9px;
    border-radius: var(--r); color: var(--text2); text-decoration: none; font-size: var(--t-body);
  }
  .fact b { color: var(--text); font-weight: 600; white-space: nowrap; }
  .fact b.warn-text { color: var(--warn-ink); }
  .fact .bar { grid-column: 1 / -1; }
  .fact:focus-visible { outline-offset: 0; }
</style>
