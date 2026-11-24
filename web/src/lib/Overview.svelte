<script>
  import HostChart from './HostChart.svelte';
  import Rail from './Rail.svelte';
  import Kpi from './Kpi.svelte';
  import Icon from './icons/Icon.svelte';
  import Skeleton from './Skeleton.svelte';
  import { flip } from 'svelte/animate';
  import { reveal, conceal, ms, EASE } from './motion.js';
  import { onMount, untrack } from 'svelte';
  import { api } from './api.js';
  import { snapshot } from './data.js';
  import { synced } from './live.js';
  import { downloading, attention } from './status.js';
  import { mountAttention } from './storage.js';
  import { formatLeft } from './security.js';
  import { formatBytes, formatPct } from './format.js';

  // Overview is the landing view: what needs attention first (or one calm line when nothing does), then the
  // host's throughput and four key figures.
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
  const fails = $derived(issues.filter((i) => i.tone === 'fail').length);
  const warns = $derived(issues.length - fails);
  const tally = $derived([
    fails ? `${fails} error${fails === 1 ? '' : 's'}` : '',
    warns ? `${warns} warning${warns === 1 ? '' : 's'}` : '',
  ].filter(Boolean).join(' · '));
  const VIEW_NAMES = { '#/apps': 'Apps', '#/storage': 'Storage', '#/security': 'Security', '#/system': 'System' };

  // Rows that appear after the first reading are marked fresh for a moment, so a new problem is noticed; the
  // rows already there when the view opens are not.
  let known = null;
  let fresh = $state({});
  $effect(() => {
    if (!$synced) return;
    const keys = issues.map((i) => i.key);
    if (known === null) {
      known = new Set(keys);
      return;
    }
    const added = keys.filter((k) => !known.has(k));
    known = new Set(keys);
    if (added.length === 0) return;
    fresh = { ...untrack(() => fresh), ...Object.fromEntries(added.map((k) => [k, true])) };
    setTimeout(() => {
      fresh = Object.fromEntries(Object.entries(fresh).filter(([k]) => !added.includes(k)));
    }, 1600);
  });

  // openIssue takes an entry about one service straight to its card; the others follow their link.
  function openIssue(event, item) {
    if (item.service == null || item.href !== '#/apps') return;
    event.preventDefault();
    onjump(item.service);
  }
  const servicesTone = $derived(checked === 0 ? '' : up === services.length ? 'ok' : up === 0 ? 'fail' : 'warn');
</script>

<div class="overview">
  {#if !$synced}
    <div class="calm"><Skeleton count={1} label="Waiting for the first reading…" /></div>
  {:else if issues.length === 0}
    <section class="calm" aria-labelledby="attention-title">
      <h2 id="attention-title" class="visually-hidden">Needs attention</h2>
      <p class="all-good">
        <span class="dot ok" aria-hidden="true"></span>
        <span><b>All good.</b> <span class="muted">No service down, no warnings, no stuck downloads.</span></span>
      </p>
    </section>
  {:else}
    <section class="attention" class:has-fail={fails > 0} aria-labelledby="attention-title" in:reveal out:conceal>
      <div class="att-head">
        <h2 id="attention-title">Needs attention</h2>
        <span class="tally num">{tally}</span>
      </div>
      <ul class="issues">
        {#each issues as item (item.key)}
          <li class={item.tone} class:fresh={fresh[item.key]} in:reveal out:conceal
            animate:flip={{ duration: ms(200), easing: EASE }}>
            <a href={item.href} class="issue row-link" onclick={(e) => openIssue(e, item)}>
              <span class="dot {item.tone}" aria-hidden="true"></span>
              <span class="what">
                <span class="visually-hidden">{item.tone === 'fail' ? 'Error' : 'Warning'}: </span>
                <span class="title ellipsis" title={item.title}>{item.title}</span>
                {#if item.detail}<span class="detail">{item.detail}</span>{/if}
              </span>
              <span class="where">
                <span class="where-name"><span class="visually-hidden">, open </span>{VIEW_NAMES[item.href] ?? ''}</span>
                <span class="chev"><Icon name="chevron-right" size={16} /></span>
              </span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

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
      <Kpi href="#/system" label="CPU" value={formatPct(host.cpuPercent)} num={host.cpuPercent} format={formatPct}
        sub={`${host.cpus} core${host.cpus === 1 ? '' : 's'} · load ${host.load[0].toFixed(2)}`}
        bar={Math.min(100, host.cpuPercent)} barTone={host.cpuPercent >= 90 ? 'warn' : ''} />
      <Kpi href="#/system" label="Memory" value={memParts[0]} unit={memParts[1]}
        sub={`of ${formatBytes(host.memTotal)} · ${memPct}%`} bar={memPct} barTone={memPct >= 90 ? 'warn' : ''} />
    {:else}
      <Kpi href="#/system" label="CPU" value="—" sub="No host figures" />
      <Kpi href="#/system" label="Memory" value="—" sub="No host figures" />
    {/if}
  </div>

  <div class="inline-rail">
    <Rail {services} {onjump} />
  </div>
</div>

<style>
  .overview { display: grid; gap: 32px; }

  /* Nothing to look at: one quiet line leads the page. */
  .calm { margin-bottom: -8px; }
  .all-good { display: flex; align-items: center; gap: 10px; font-size: var(--t-base); }
  .all-good b { font-weight: 600; }

  /* Something to look at: the one boxed panel of the page, edged in the worst state's colour. */
  .attention {
    background: var(--surface); border: 1px solid var(--warn-edge); border-radius: var(--r-lg);
    padding: 14px 8px 8px; min-width: 0; box-shadow: var(--lift);
  }
  .attention.has-fail { border-color: var(--fail-edge); }
  .att-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; padding: 0 10px 8px; min-width: 0; }
  .att-head h2 { font-size: var(--t-lg); font-weight: 650; letter-spacing: -0.015em; }
  .tally { font-size: var(--t-sm); color: var(--text2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .issues { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
  .issue {
    display: flex; align-items: flex-start; gap: 11px; padding: 9px 10px; border-radius: var(--r);
    min-width: 0; color: var(--text); text-decoration: none; transition: background 140ms var(--ease);
  }
  .fail .issue { background: var(--fail-wash); }
  /* A problem that just appeared: its row lights once and settles to its usual wash. */
  .fresh .issue { animation: fresh 1.4s var(--ease); }
  .fresh.fail .issue { animation-name: fresh-fail; }
  @keyframes fresh { from { background: rgba(227, 166, 80, 0.2); } }
  @keyframes fresh-fail { from { background: rgba(236, 108, 125, 0.26); } }
  .fail .issue:hover { background: rgba(236, 108, 125, 0.14); }
  .issue:hover .title { text-decoration: underline; text-underline-offset: 3px; }
  .issue:focus-visible { outline-offset: 0; }
  .issue .dot { margin-top: 7px; width: 8px; height: 8px; }
  .what { display: grid; gap: 1px; min-width: 0; flex: 1; }
  .title { font-weight: 550; font-size: var(--t-base); }
  .detail { font-size: var(--t-sm); color: var(--text2); overflow-wrap: anywhere; }
  .where {
    display: flex; align-items: center; gap: 4px; flex: none; align-self: center;
    font-size: var(--t-sm); color: var(--muted); white-space: nowrap;
  }
  .issue:hover .where { color: var(--text2); }

  .inline-rail { display: none; }
  @media (max-width: 1199px) {
    .inline-rail { display: block; padding-top: 18px; border-top: 1px solid var(--border); }
  }
  @container (max-width: 520px) {
    .where-name { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
    .attention { padding: 12px 4px 4px; }
  }
</style>
