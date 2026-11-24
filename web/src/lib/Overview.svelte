<script>
  import HostChart from './HostChart.svelte';
  import Rail from './Rail.svelte';
  import Kpi from './Kpi.svelte';
  import { onMount } from 'svelte';
  import { api } from './api.js';
  import { snapshot } from './data.js';
  import { synced } from './live.js';
  import { downloading, attention } from './status.js';
  import { mountAttention } from './storage.js';
  import { formatLeft } from './security.js';
  import { formatBytes, formatPct } from './format.js';

  // Overview is the landing view: the host's throughput, four key figures and what needs attention.
  // services is the (search-filtered) service list; onjump(id) shows a service's card.
  let { services, onjump } = $props();

  const host = $derived($snapshot?.host ?? null);
  const up = $derived(services.filter((s) => s.up).length);
  const checked = $derived(services.filter((s) => s.readings?.health).length);
  const active = $derived(services.reduce((n, s) => n + downloading(s), 0));
  const queued = $derived(services.reduce((n, s) => n + (s.readings?.queue?.data?.total ?? 0), 0));
  const memPct = $derived(host && host.memTotal > 0 ? Math.round((host.memUsed / host.memTotal) * 100) : 0);
  const memParts = $derived(host ? formatBytes(host.memUsed).split(' ') : ['—', '']);
  // The server-wide sign-in limit is the one security state worth a line here; it is read every minute while the
  // Overview is open (a small answer from memory), and a failed read just leaves it out.
  let lockedSec = $state(0);
  async function loadLock() {
    try {
      lockedSec = (await api('GET', '/api/security/lock')).serverLockedSec;
    } catch {
      lockedSec = 0;
    }
  }
  onMount(() => {
    loadLock();
    const id = setInterval(loadLock, 60 * 1000);
    return () => clearInterval(id);
  });
  const lockIssue = $derived(lockedSec > 0 ? [{
    key: 'server-lock', tone: 'warn', title: 'Sign-ins limited', href: '#/security',
    detail: `Many addresses failed to sign in; for ${formatLeft(lockedSec)} only addresses that signed in before may try`,
  }] : []);
  const issues = $derived(
    [...attention(services, $snapshot?.containers ?? []), ...mountAttention(services), ...lockIssue]
      .sort((a, b) => (a.tone === 'fail' ? 0 : 1) - (b.tone === 'fail' ? 0 : 1)),
  );
  const servicesTone = $derived(checked === 0 ? '' : up === services.length ? 'ok' : up === 0 ? 'fail' : 'warn');
</script>

<div class="overview">
  <HostChart />

  <div class="kpis">
    {#if $synced}
      <Kpi href="#/apps" tone={servicesTone} label="Services" value={up} unit={`of ${services.length} up`}
        sub={services.length === 0 ? 'No services yet' : up === services.length ? 'All services up' : `${services.length - up} not up`} />
      <Kpi href="#/apps" label="Downloads" value={active} unit="active"
        sub={`${queued} in queue${queued === 1 ? '' : 's'}`} />
    {:else}
      <!-- Until the first snapshot an empty list means "not known yet": no counts, no all-clear. -->
      <Kpi href="#/apps" label="Services" value="—" sub="Loading…" />
      <Kpi href="#/apps" label="Downloads" value="—" sub="Loading…" />
    {/if}
    {#if host}
      <Kpi href="#/system" label="CPU" value={formatPct(host.cpuPercent)}
        sub={`${host.cpus} core${host.cpus === 1 ? '' : 's'} · load ${host.load[0].toFixed(2)}`}
        bar={Math.min(100, host.cpuPercent)} barTone={host.cpuPercent >= 90 ? 'warn' : ''} />
      <Kpi href="#/system" label="Memory" value={memParts[0]} unit={memParts[1]}
        sub={`of ${formatBytes(host.memTotal)} · ${memPct}%`} bar={memPct} barTone={memPct >= 90 ? 'warn' : ''} />
    {:else}
      <Kpi href="#/system" label="CPU" value="—" sub="No host figures" />
      <Kpi href="#/system" label="Memory" value="—" sub="No host figures" />
    {/if}
  </div>

  <section class="section" aria-labelledby="attention-title">
    <div class="card-head">
      <h2 id="attention-title">Needs attention</h2>
      {#if $synced && issues.length}<span class="meta num">{issues.length}</span>{/if}
    </div>
    {#if !$synced}
      <p class="waiting">Waiting for the first reading…</p>
    {:else if issues.length === 0}
      <p class="all-good">
        <span class="dot ok" aria-hidden="true"></span>
        <span><b>All good.</b> <span class="muted">No service down, no warnings, no stuck downloads.</span></span>
      </p>
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
  .overview { display: grid; gap: 28px; }
  .all-good { display: flex; align-items: center; gap: 10px; font-size: var(--t-base); }
  .all-good b { font-weight: 600; }
  .issues { list-style: none; margin: 0; padding: 0; }
  .issues li { display: flex; gap: 11px; align-items: flex-start; padding: 9px 0; border-bottom: 1px solid var(--border); min-width: 0; }
  .issues li:first-child { padding-top: 0; }
  .issues li:last-child { border-bottom: 0; padding-bottom: 0; }
  .issues .dot { margin-top: 7px; }
  .issue { display: grid; gap: 1px; min-width: 0; flex: 1; color: var(--text); text-decoration: none; }
  .issue:hover .title { text-decoration: underline; }
  .title { font-weight: 550; font-size: var(--t-base); }
  .detail { font-size: var(--t-sm); color: var(--text2); overflow-wrap: anywhere; }

  .inline-rail { display: none; }
  @media (max-width: 1199px) {
    .inline-rail { display: block; padding-top: 14px; border-top: 1px solid var(--border); }
  }
</style>
