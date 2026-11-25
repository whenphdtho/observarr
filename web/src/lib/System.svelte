<script>
  import { onMount } from 'svelte';
  import HostHistory from './HostHistory.svelte';
  import Backup from './Backup.svelte';
  import Kpi from './Kpi.svelte';
  import Icon from './icons/Icon.svelte';
  import { snapshot, systemError, hostLines, watchTrend } from './data.js';
  import { matches } from './status.js';
  import { diskParts } from './storage.js';
  import { now } from './ticker.js';
  import {
    formatBytes, formatRate, formatMbit, formatPct, formatDuration, formatAge, splitNotice, containerStatusClass,
  } from './format.js';

  // System shows the host (CPU, memory, swap, load, uptime, disks, network) and every container
  // Observarr can see, from the sampler's Snapshot (system.Snapshot) that Shell loads and the live
  // stream keeps current. query narrows the containers table.
  let { query = '' } = $props();

  onMount(watchTrend);
  const HOUR = 'the hour before';

  const host = $derived($snapshot?.host ?? null);
  const disks = $derived(host?.disks ?? []);
  const net = $derived(host?.net ?? []);
  const containers = $derived(($snapshot?.containers ?? []).filter((c) => matches(query, c.name, c.state)));
  const allContainers = $derived($snapshot?.containers?.length ?? 0);
  const notices = $derived($snapshot?.notices ?? []);
  // dockerNotConfigured: Docker discovery/figures were never set up at all (as opposed to being
  // set up but failing) — the empty container list then says so plainly instead of the generic
  // "No containers.", which reads as if Docker were configured and simply had none running.
  const dockerNotConfigured = $derived(notices.some((n) => n.includes('OBSERVARR_DOCKER_HOST')));

  function barPercent(used, total) {
    return total > 0 ? Math.round(Math.min(1, Math.max(0, used / total)) * 100) : 0;
  }

  // ownNetwork mirrors collect.Container.OwnNetwork: a container sharing the host's network (or
  // another container's) has no network counters of its own to show a rate for.
  function ownNetwork(mode) {
    return mode !== 'host' && !(mode ?? '').startsWith('container:');
  }

  const columns = [
    { key: 'name', label: 'Name' },
    { key: 'state', label: 'State' },
    { key: 'cpu', label: 'CPU %', num: true },
    { key: 'mem', label: 'Memory', num: true },
    { key: 'net', label: 'Network Mbit/s', num: true },
    { key: 'blk', label: 'Block I/O', num: true },
    { key: 'restarts', label: 'Restarts', num: true },
    { key: 'up', label: 'Up since' },
  ];
  const sortValue = {
    name: (c) => c.name.toLowerCase(),
    state: (c) => c.state + (c.health || ''),
    cpu: (c) => c.cpuPercent,
    mem: (c) => c.memBytes,
    net: (c) => c.netRxRate + c.netTxRate,
    blk: (c) => c.blkReadRate + c.blkWriteRate,
    restarts: (c) => c.restartCount,
    up: (c) => (c.startedAt ? new Date(c.startedAt).getTime() : 0),
  };

  let sortKey = $state('name');
  let sortDir = $state('asc');

  function sortBy(key) {
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = key;
      sortDir = 'asc';
    }
  }

  // The mobile sort <select> only picks a column (there is no header button left to click again
  // for direction), so it always sorts ascending.
  function selectSort(key) {
    sortKey = key;
    sortDir = 'asc';
  }

  function ariaSort(key) {
    if (sortKey !== key) return 'none';
    return sortDir === 'asc' ? 'ascending' : 'descending';
  }

  const sortedContainers = $derived.by(() => {
    const get = sortValue[sortKey];
    const list = [...containers];
    list.sort((a, b) => {
      const av = get(a);
      const bv = get(b);
      const cmp = typeof av === 'string' ? av.localeCompare(bv) : av - bv;
      return sortDir === 'asc' ? cmp : -cmp;
    });
    return list;
  });
</script>

<div class="system">
  {#if $systemError && !$snapshot}
    <p class="error" role="alert">{$systemError}</p>
  {/if}

  {#if notices.length > 0}
    <div class="banner" role="status">
      <Icon name="info" />
      <div class="banner-text">
        {#each notices as notice (notice)}
          <p>
            {#each splitNotice(notice) as part, i (i)}
              {#if part.kind === 'code'}<code>{part.text}</code>{:else}{part.text}{/if}
            {/each}
          </p>
        {/each}
        <p>
          How to set this up:
          <a href="https://github.com/whenphdtho/observarr/blob/main/docs/deploy.md" target="_blank" rel="noopener noreferrer">
            deploy guide
          </a>
        </p>
      </div>
    </div>
  {/if}

  {#if host}
    <div class="kpis">
      <Kpi label="CPU" value={formatPct(host.cpuPercent)} num={host.cpuPercent} format={formatPct} sub={`${host.cpus} core${host.cpus === 1 ? '' : 's'}`}
        tone={host.cpuPercent >= 90 ? 'warn' : ''} spark={$hostLines.cpu} sparkColor="var(--cpu)"
        change={$hostLines.cpuChange !== null ? { ratio: $hostLines.cpuChange, good: 'down', against: HOUR } : null} />
      <Kpi label="Memory" value={formatBytes(host.memUsed)}
        sub={`of ${formatBytes(host.memTotal)} · ${barPercent(host.memUsed, host.memTotal)}%`}
        tone={barPercent(host.memUsed, host.memTotal) >= 90 ? 'warn' : ''} spark={$hostLines.mem} sparkColor="var(--mem)" sparkMin={null}
        change={$hostLines.memChange !== null ? { ratio: $hostLines.memChange, good: 'down', against: HOUR } : null} />
      {#if host.swapTotal === 0}
        <Kpi label="Swap" value="—" sub="No swap" />
      {:else}
        <Kpi label="Swap" value={formatBytes(host.swapUsed)}
          sub={`of ${formatBytes(host.swapTotal)} · ${barPercent(host.swapUsed, host.swapTotal)}%`}
          bar={barPercent(host.swapUsed, host.swapTotal)} />
      {/if}
      <Kpi label="Uptime" value={formatDuration(host.uptimeNs)}
        sub={`load ${host.load.map((l) => l.toFixed(2)).join(' · ')}`} />
    </div>

    <HostHistory />

    {#if disks.length > 0 || net.length > 0}
      <div class="pair" class:single={disks.length === 0 || net.length === 0}>
        {#if disks.length > 0}
          <section class="section" aria-labelledby="disks-title">
            <div class="card-head">
              <h2 id="disks-title">Disks</h2>
              <a class="go" href="#/storage">Details in Storage<Icon name="chevron-right" size={14} /></a>
            </div>
            <ul class="rows">
              {#each disks as d (d.path)}
                {@const p = diskParts(d)}
                <li class="disk-row">
                  <span class="ellipsis name" title={d.path}>{d.path}</span>
                  <span class="bar mini" class:warn={p.pct >= 90} aria-hidden="true"><i style:width={`${p.pct}%`}></i></span>
                  <span class="num rate" title={`${formatBytes(p.used)} used, ${formatBytes(p.avail)} free of ${formatBytes(d.total)}`}>
                    {p.pct}% · {formatBytes(p.avail)} free
                  </span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}
        {#if net.length > 0}
          <section class="section" aria-labelledby="net-title">
            <div class="card-head">
              <h2 id="net-title">Interfaces <span class="unit">Mbit/s</span></h2>
              <a class="go" href="#/network">By app in Network<Icon name="chevron-right" size={14} /></a>
            </div>
            <ul class="rows">
              {#each net as n (n.name)}
                <li>
                  <span class="ellipsis name" title={n.name}>{n.name}</span>
                  <span class="num rate" title={`down ${formatMbit(n.rx)} Mbit/s, up ${formatMbit(n.tx)} Mbit/s`}>
                    ↓ {formatMbit(n.rx)} · ↑ {formatMbit(n.tx)}
                  </span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      </div>
    {/if}
  {/if}

  <section class="section containers" aria-labelledby="containers-title">
    <div class="card-head">
      <h2 id="containers-title">Containers</h2>
      {#if allContainers}
        <span class="meta num">{containers.length === allContainers ? allContainers : `${containers.length} of ${allContainers}`}</span>
      {/if}
    </div>
    {#if containers.length === 0}
      <p class="waiting">
        {allContainers ? 'No container matches the search.' : dockerNotConfigured ? 'Docker is not configured.' : 'No containers.'}
      </p>
    {:else}
      <!-- In a narrow layout the header row (and its sort buttons) is hidden outright rather than
           merely off-screen, so it can't be tabbed to while invisible; this select is the narrow
           equivalent, and it stays a real, focusable control. -->
      <div class="mobile-sort">
        <label for="container-sort">Sort by</label>
        <select id="container-sort" value={sortKey} onchange={(e) => selectSort(e.currentTarget.value)}>
          {#each columns as col (col.key)}
            <option value={col.key}>{col.label}</option>
          {/each}
        </select>
      </div>
      <div class="table-wrap">
        <!-- The explicit roles below are redundant by default, but table/thead/tbody/tr collapse to
             block or grid in a narrow layout (to stack rows as blocks); some screen readers then drop
             the element's table semantics unless a matching role is still set explicitly. -->
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <table class="data" role="table">
          <caption class="visually-hidden">Containers</caption>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <thead role="rowgroup">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              {#each columns as col (col.key)}
                <th role="columnheader" scope="col" class={col.num ? 'r' : ''} aria-sort={ariaSort(col.key)}>
                  <button type="button" onclick={() => sortBy(col.key)}>
                    {col.label}
                    <span class="arrow" aria-hidden="true">
                      {#if sortKey === col.key}<Icon name={sortDir === 'asc' ? 'chevron-up' : 'chevron-down'} size={13} />{/if}
                    </span>
                  </button>
                </th>
              {/each}
            </tr>
          </thead>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tbody role="rowgroup">
            {#each sortedContainers as c (c.id)}
              {@const status = containerStatusClass(c.state, c.health)}
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <tr role="row">
                <td role="cell" data-label="Name" class="lead" title={c.name}>
                  <span class="ellipsis">{c.name}</span>
                </td>
                <td role="cell" data-label="State" title={c.state + (c.health ? ` (${c.health})` : '')}>
                  <span class="state-wrap">
                    <span class="dot {status}" aria-hidden="true"></span>
                    <span class="ellipsis">{c.state}{c.health ? ` (${c.health})` : ''}</span>
                  </span>
                </td>
                <td role="cell" data-label="CPU %" class="r">
                  <span class="ellipsis">{c.state === 'running' ? formatPct(c.cpuPercent) : '—'}</span>
                </td>
                <td role="cell" data-label="Memory" class="r"
                  title={c.state === 'running' ?
                    `${formatBytes(c.memBytes)} of ${c.memLimit ? formatBytes(c.memLimit) : 'no limit'}` : ''}>
                  {#if c.state === 'running'}
                    <span class="two">
                      <span>{formatBytes(c.memBytes)}</span>
                      <span class="muted">{c.memLimit ? `of ${formatBytes(c.memLimit)}` : 'no limit'}</span>
                    </span>
                  {:else}
                    —
                  {/if}
                </td>
                <td role="cell" data-label="Network" class="r"
                  title={c.state === 'running' && ownNetwork(c.networkMode) ?
                    `down ${formatMbit(c.netRxRate)} Mbit/s, up ${formatMbit(c.netTxRate)} Mbit/s` : ''}>
                  {#if c.state !== 'running'}
                    —
                  {:else if ownNetwork(c.networkMode)}
                    <span class="two">
                      <span><span aria-hidden="true">↓</span> {formatMbit(c.netRxRate)}</span>
                      <span><span aria-hidden="true">↑</span> {formatMbit(c.netTxRate)}</span>
                    </span>
                  {:else}
                    <span class="muted" title="no network of its own">—</span>
                  {/if}
                </td>
                <td role="cell" data-label="Block I/O" class="r"
                  title={c.state === 'running' ? `read ${formatRate(c.blkReadRate)}, write ${formatRate(c.blkWriteRate)}` : ''}>
                  {#if c.state === 'running'}
                    <span class="two">
                      <span><span class="muted">R</span> {formatRate(c.blkReadRate)}</span>
                      <span><span class="muted">W</span> {formatRate(c.blkWriteRate)}</span>
                    </span>
                  {:else}
                    —
                  {/if}
                </td>
                <td role="cell" data-label="Restarts" class="r">{c.restartCount}</td>
                <td role="cell" data-label="Up since" title={c.startedAt || ''}>
                  <span class="ellipsis">{c.startedAt ? formatAge(c.startedAt, $now) : '—'}</span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <Backup />
</div>

<style>
  .system { display: grid; gap: 20px; }

  .pair { display: grid; gap: 20px; grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); align-items: start; }
  .pair.single { grid-template-columns: minmax(0, 1fr); }

  .unit { font-size: var(--t-small); font-weight: 400; color: var(--muted); }
  .rows { list-style: none; margin: 0; padding: 0; }
  .rows li {
    display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-bottom: 1px solid var(--border);
    font-size: var(--t-body); min-width: 0;
  }
  .rows li:first-child { padding-top: 0; }
  .rows li:last-child { border-bottom: 0; padding-bottom: 0; }
  .rows .name { font-weight: 500; }
  .rate { white-space: nowrap; color: var(--text2); }
  .rows li.disk-row { align-items: center; }
  .disk-row .name { flex: 1 1 auto; min-width: 0; }
  .bar.mini { flex: 0 1 90px; min-width: 32px; }
  .disk-row .rate { flex: none; min-width: 15ch; text-align: right; }
  @container (max-width: 420px) {
    .bar.mini { display: none; }
  }

  .mobile-sort { display: none; }
  .arrow { width: 13px; height: 13px; display: inline-grid; }
  .state-wrap { display: inline-flex; align-items: center; gap: 7px; min-width: 0; max-width: 100%; }
  /* Two figures of one cell (used/limit, down/up, read/write) sit one above the other. */
  .two { display: grid; line-height: 1.4; }

  @container (max-width: 820px) {
    .pair { grid-template-columns: minmax(0, 1fr); }
  }

  /* Narrow: the shared block layout of .data, arranged for a container: a name line and a state
     line; CPU %, Restarts and Up since side by side; Memory, Network and Block I/O each on a line
     of their own, as their figures are too long to share one. */
  @container (max-width: 759px) {
    .mobile-sort { display: flex; align-items: center; gap: 8px; font-size: var(--t-body); margin-bottom: 10px; }
    .mobile-sort label { white-space: nowrap; flex-shrink: 0; color: var(--muted); }
    .mobile-sort select { flex: 1; min-width: 0; }
    tbody tr {
      grid-template-columns: repeat(3, minmax(0, 1fr));
      grid-template-areas: "name name name" "state state state" "cpu restarts up" "mem mem mem" "net net net" "blk blk blk";
    }
    td[data-label='Name'] { grid-area: name; }
    td[data-label='State'] { grid-area: state; padding-bottom: 6px; justify-content: flex-start; color: var(--text2); }
    td[data-label='State']::before { display: none; }
    td[data-label='CPU %'], td[data-label='Restarts'], td[data-label='Up since'] { display: block; }
    td[data-label='CPU %']::before, td[data-label='Restarts']::before, td[data-label='Up since']::before {
      display: block; margin-bottom: 2px;
    }
    td[data-label='CPU %'] { grid-area: cpu; }
    td[data-label='Restarts'] { grid-area: restarts; }
    td[data-label='Up since'] { grid-area: up; }
    td[data-label='Memory'] { grid-area: mem; }
    td[data-label='Network'] { grid-area: net; }
    td[data-label='Block I/O'] { grid-area: blk; }
    td .two { display: flex; gap: 10px; min-width: 0; }
  }
</style>
