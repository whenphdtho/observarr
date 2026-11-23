<script>
  import { onMount, untrack } from 'svelte';
  import { api } from './api.js';
  import { systemSnapshot, reconnecting } from './live.js';
  import { now } from './ticker.js';
  import {
    formatBytes, formatRate, formatPct, formatDuration, formatAge, splitNotice, containerStatusClass,
  } from './format.js';

  // System shows the host (CPU, memory, swap, load, uptime, disks, network) and every container
  // Observarr can see, from the sampler's Snapshot (system.Snapshot). The initial paint comes from
  // a GET of /api/system and /api/system/history; after that it tracks the live "system" SSE event
  // (Shell keeps that stream open), which also keeps the sparkline's history current.
  const HISTORY_WINDOW_MS = 5 * 60 * 1000;
  const SPARK_W = 120;
  const SPARK_H = 28;

  let initialSnapshot = $state(null);
  let initialError = $state('');
  let history = $state([]); // system.HostView[], oldest first, covering the last 5 minutes

  const snapshot = $derived($systemSnapshot ?? initialSnapshot);
  const host = $derived(snapshot?.host ?? null);
  const disks = $derived(host?.disks ?? []);
  const net = $derived(host?.net ?? []);
  const containers = $derived(snapshot?.containers ?? []);
  const notices = $derived(snapshot?.notices ?? []);
  // dockerNotConfigured: Docker discovery/figures were never set up at all (as opposed to being
  // set up but failing) — the empty container list then says so plainly instead of the generic
  // "No containers.", which reads as if Docker were configured and simply had none running.
  const dockerNotConfigured = $derived(notices.some((n) => n.includes('OBSERVARR_DOCKER_HOST')));

  onMount(() => {
    load();
  });

  async function load() {
    try {
      const [snap, hist] = await Promise.all([api('GET', '/api/system'), api('GET', '/api/system/history')]);
      initialSnapshot = snap;
      history = hist ?? [];
    } catch (err) {
      initialError = err.message;
    }
  }

  // Each live snapshot's host reading is appended once (by its "at" timestamp), trimmed to the
  // same 5-minute window the server's own history endpoint covers.
  $effect(() => {
    const h = host;
    if (!h) return;
    untrack(() => {
      const last = history[history.length - 1];
      if (last && last.at === h.at) return;
      // Trimmed against the newest sample's own timestamp, not wall-clock time: the two can drift
      // (a suspended tab, a slow clock) and Date.now() would then trim good history too eagerly.
      const cutoff = new Date(h.at).getTime() - HISTORY_WINDOW_MS;
      history = [...history.filter((x) => new Date(x.at).getTime() >= cutoff), h];
    });
  });

  // sparklinePoints lays out CPU% history on a time-based x axis (samples can arrive faster than
  // every 2 s while a live stream is open, and slower otherwise), y clamped to 0-100%.
  function sparklinePoints(hist) {
    if (hist.length < 2) return '';
    const times = hist.map((x) => new Date(x.at).getTime());
    const t0 = times[0];
    const span = Math.max(1, times[times.length - 1] - t0);
    return hist
      .map((x, i) => {
        const px = ((times[i] - t0) / span) * SPARK_W;
        const py = SPARK_H - (Math.min(100, Math.max(0, x.cpuPercent)) / 100) * SPARK_H;
        return `${px.toFixed(1)},${py.toFixed(1)}`;
      })
      .join(' ');
  }

  function barPercent(used, total) {
    return total > 0 ? Math.round(Math.min(1, Math.max(0, used / total)) * 100) : 0;
  }

  // diskUsed and diskPercent follow df's own convention: used space is total minus free (not
  // "avail", which a filesystem can reserve below), and the percentage is used over used+avail
  // (not over total), since avail is what's actually left for new writes.
  function diskUsed(d) {
    return d.total - d.free;
  }

  function diskPercent(d) {
    const used = diskUsed(d);
    return barPercent(used, used + d.avail);
  }

  // ownNetwork mirrors collect.Container.OwnNetwork: a container sharing the host's network (or
  // another container's) has no network counters of its own to show a rate for.
  function ownNetwork(mode) {
    return mode !== 'host' && !(mode ?? '').startsWith('container:');
  }

  const columns = [
    { key: 'name', label: 'Name' },
    { key: 'state', label: 'State' },
    { key: 'cpu', label: 'CPU %' },
    { key: 'mem', label: 'Memory' },
    { key: 'net', label: 'Network' },
    { key: 'blk', label: 'Block I/O' },
    { key: 'restarts', label: 'Restarts' },
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

<section class="system">
  <h2>System</h2>

  {#if initialError && !snapshot}
    <p class="error" role="alert">{initialError}</p>
  {/if}

  {#if $reconnecting}
    <p class="muted" role="status">Reconnecting…</p>
  {/if}

  {#if notices.length > 0}
    <div class="card notices" role="status">
      {#each notices as notice (notice)}
        <p class="notice muted">
          {#each splitNotice(notice) as part, i (i)}
            {#if part.kind === 'code'}<code>{part.text}</code>{:else}{part.text}{/if}
          {/each}
        </p>
      {/each}
      <p class="notice muted">
        How to set this up:
        <a href="https://github.com/whenphdtho/observarr/blob/main/docs/deploy.md" target="_blank" rel="noopener noreferrer">
          deploy guide
        </a>
      </p>
    </div>
  {/if}

  {#if host}
    <div class="tiles">
      <div class="card tile">
        <h3>CPU</h3>
        <p class="value">{formatPct(host.cpuPercent)}</p>
        <svg class="sparkline" viewBox={`0 0 ${SPARK_W} ${SPARK_H}`} preserveAspectRatio="none" role="img"
          aria-label={`CPU usage over the last ${Math.round(HISTORY_WINDOW_MS / 60000)} minutes`}>
          <polyline points={sparklinePoints(history)} />
        </svg>
        <p class="muted hint">{host.cpus} CPU{host.cpus === 1 ? '' : 's'}</p>
      </div>

      <div class="card tile">
        <h3>Memory</h3>
        <p class="value">{formatBytes(host.memUsed)} / {formatBytes(host.memTotal)}</p>
        <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100"
          aria-valuenow={barPercent(host.memUsed, host.memTotal)}
          aria-label={`Memory: ${formatBytes(host.memUsed)} of ${formatBytes(host.memTotal)} used`}>
          <div class="bar-fill" style={`width:${barPercent(host.memUsed, host.memTotal)}%`}></div>
        </div>
      </div>

      <div class="card tile">
        <h3>Swap</h3>
        {#if host.swapTotal === 0}
          <p class="muted">No swap.</p>
        {:else}
          <p class="value">{formatBytes(host.swapUsed)} / {formatBytes(host.swapTotal)}</p>
          <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100"
            aria-valuenow={barPercent(host.swapUsed, host.swapTotal)}
            aria-label={`Swap: ${formatBytes(host.swapUsed)} of ${formatBytes(host.swapTotal)} used`}>
            <div class="bar-fill" style={`width:${barPercent(host.swapUsed, host.swapTotal)}%`}></div>
          </div>
        {/if}
      </div>

      <div class="card tile">
        <h3>Load</h3>
        <p class="value ellipsis load-value" title="1 / 5 / 15 minute load average">
          {host.load[0].toFixed(2)} · {host.load[1].toFixed(2)} · {host.load[2].toFixed(2)}
        </p>
        <p class="muted hint">1 · 5 · 15 min</p>
      </div>

      <div class="card tile">
        <h3>Uptime</h3>
        <p class="value">{formatDuration(host.uptimeNs)}</p>
      </div>

      {#if disks.length > 0}
        <div class="card tile disks">
          <h3>Disks</h3>
          {#each disks as d (d.path)}
            <div class="meter-row">
              <span class="ellipsis label" title={d.path}>{d.path}</span>
              <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100"
                aria-valuenow={diskPercent(d)}
                aria-label={`${d.path}: ${formatBytes(diskUsed(d))} of ${formatBytes(d.total)} used`}>
                <div class="bar-fill" style={`width:${diskPercent(d)}%`}></div>
              </div>
              <span class="muted figures ellipsis" title={`${formatBytes(diskUsed(d))} of ${formatBytes(d.total)}`}>
                {formatBytes(diskUsed(d))} / {formatBytes(d.total)}
              </span>
            </div>
          {/each}
        </div>
      {/if}

      {#if net.length > 0}
        <div class="card tile net">
          <h3>Network</h3>
          {#each net as n (n.name)}
            <div class="meter-row">
              <span class="ellipsis label" title={n.name}>{n.name}</span>
              <span class="muted figures ellipsis" title={`down ${formatRate(n.rx)}, up ${formatRate(n.tx)}`}>
                ↓ {formatRate(n.rx)} ↑ {formatRate(n.tx)}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <h3 class="containers-heading">Containers</h3>
  {#if containers.length === 0}
    <p class="muted">{dockerNotConfigured ? 'Docker is not configured.' : 'No containers.'}</p>
  {:else}
    <!-- Below 700px the header row (and its sort buttons) is hidden outright rather than merely
         off-screen, so it can't be tabbed to while invisible; this select is the mobile
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
           display: block under 700px (to stack rows as cards); some screen readers then drop the
           element's table semantics unless a matching role is still set explicitly. -->
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <table role="table">
        <caption class="visually-hidden">Containers</caption>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            {#each columns as col (col.key)}
              <th role="columnheader" scope="col" aria-sort={ariaSort(col.key)}>
                <button type="button" onclick={() => sortBy(col.key)}>
                  {col.label}
                  <span class="arrow" aria-hidden="true">
                    {sortKey === col.key ? (sortDir === 'asc' ? '▲' : '▼') : ''}
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
              <td role="cell" data-label="Name" title={c.name}>
                <span class="ellipsis">{c.name}</span>
              </td>
              <td role="cell" data-label="State" title={c.state + (c.health ? ` (${c.health})` : '')}>
                <span class="state-wrap">
                  <span class="dot" class:ok={status === 'ok'} class:warn={status === 'warn'}
                    class:fail={status === 'fail'} aria-hidden="true"></span>
                  <span class="ellipsis">{c.state}{c.health ? ` (${c.health})` : ''}</span>
                </span>
              </td>
              <td role="cell" data-label="CPU %">
                <span class="ellipsis">{c.state === 'running' ? formatPct(c.cpuPercent) : '—'}</span>
              </td>
              <td role="cell" data-label="Memory"
                title={c.state === 'running' ?
                  `${formatBytes(c.memBytes)} of ${c.memLimit ? formatBytes(c.memLimit) : 'no limit'}` : ''}>
                <span class="ellipsis">
                  {#if c.state === 'running'}
                    {formatBytes(c.memBytes)} / {c.memLimit ? formatBytes(c.memLimit) : 'no limit'}
                  {:else}
                    —
                  {/if}
                </span>
              </td>
              <td role="cell" data-label="Network">
                <span class="ellipsis">
                  {#if c.state !== 'running'}
                    —
                  {:else if ownNetwork(c.networkMode)}
                    <span title={`down ${formatRate(c.netRxRate)}, up ${formatRate(c.netTxRate)}`}>
                      ↓ {formatRate(c.netRxRate)} ↑ {formatRate(c.netTxRate)}
                    </span>
                  {:else}
                    <span class="muted" title="no network of its own">—</span>
                  {/if}
                </span>
              </td>
              <td role="cell" data-label="Block I/O"
                title={c.state === 'running' ? `read ${formatRate(c.blkReadRate)}, write ${formatRate(c.blkWriteRate)}` : ''}>
                <span class="ellipsis">
                  {#if c.state === 'running'}
                    R {formatRate(c.blkReadRate)} W {formatRate(c.blkWriteRate)}
                  {:else}
                    —
                  {/if}
                </span>
              </td>
              <td role="cell" data-label="Restarts">{c.restartCount}</td>
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

<style>
  .system { display: grid; gap: 16px; }
  .system h2 { margin: 0; font-size: 18px; }
  .notices { display: grid; gap: 6px; }
  .notice { margin: 0; }
  .notice code {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.95em;
    background: var(--bg); padding: 1px 5px; border-radius: 4px;
  }
  .notice a { color: var(--accent); }

  .tiles { display: grid; gap: 16px; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); }
  .tile { display: grid; gap: 8px; align-content: start; min-width: 0; padding: 16px; }
  .tile h3 { margin: 0; font-size: 13px; color: var(--muted); font-weight: 600; text-transform: uppercase; }
  .value { margin: 0; font-size: 20px; font-weight: 600; }
  .load-value { font-size: 17px; }
  .hint { margin: 0; font-size: 12px; }
  .sparkline { width: 100%; max-width: 160px; height: 28px; display: block; }
  .sparkline polyline { fill: none; stroke: var(--accent); stroke-width: 1.5; }

  .bar {
    /* --field-border, not --border: the track needs the >=3:1 contrast against --surface that
       --border (meant for quiet dividers) does not reliably clear. */
    height: 6px; border-radius: 4px; background: var(--field-border); overflow: hidden;
  }
  .bar-fill { height: 100%; background: var(--accent); }

  .disks, .net { gap: 10px; }
  .meter-row { display: grid; grid-template-columns: minmax(0, auto) minmax(40px, 1fr) minmax(0, auto); gap: 8px; align-items: center; }
  .meter-row .label { font-size: 13px; }
  .meter-row .figures { font-size: 12px; text-align: right; }

  /* The disk's used/total figures need more room than a third grid column can spare without
     clipping, so they sit on their own line under the bar instead. */
  .disks .meter-row {
    grid-template-columns: minmax(0, auto) minmax(40px, 1fr);
    grid-template-areas: "label bar" "figures figures";
    row-gap: 2px;
  }
  .disks .meter-row .label { grid-area: label; }
  .disks .meter-row .bar { grid-area: bar; }
  .disks .meter-row .figures { grid-area: figures; }

  /* The interface name sits on its own line above the rates, so the rates always get the tile's
     full width instead of being squeezed into a shared row (mirrors the disks tile's layout). */
  .net .meter-row { grid-template-columns: minmax(0, 1fr); row-gap: 2px; }
  .net .meter-row .figures { text-align: left; }

  .containers-heading { margin: 0; font-size: 16px; }
  .ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .visually-hidden {
    position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap;
  }
  .mobile-sort { display: none; }

  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--border); min-width: 0; }
  th { white-space: nowrap; }
  th button {
    all: unset; cursor: pointer; display: inline-flex; align-items: center; gap: 4px;
    font: inherit; color: inherit;
  }
  th button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: 4px; }
  .arrow { font-size: 10px; width: 1em; display: inline-block; }
  .state-wrap { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
  td .dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: var(--muted); }
  td .dot.ok { background: var(--ok); }
  td .dot.warn { background: var(--warn); }
  td .dot.fail { background: var(--fail); }

  @media (max-width: 699px) {
    .mobile-sort { display: flex; align-items: center; gap: 8px; font-size: 13px; margin-bottom: 8px; }
    .mobile-sort label { white-space: nowrap; flex-shrink: 0; }
    .mobile-sort select { font: inherit; flex: 1; min-width: 0; }
    table, tbody, tr, th, td { display: block; width: 100%; }
    /* display: none, not just visually hidden: the sort buttons it holds must not be reachable by
       keyboard either. The select above does the same job and stays visible and focusable. */
    thead { display: none; }

    /* Each row becomes a compact card: a name + state line; then CPU %, Restarts and Up since (all
       short values) side by side; then Memory, Network and Block I/O each on their own full-width
       line, since their figures are too long to share a row without truncating. */
    tbody tr {
      display: grid; grid-template-columns: repeat(3, 1fr); column-gap: 10px; row-gap: 4px;
      grid-template-areas:
        "name name state"
        "cpu restarts up"
        "mem mem mem"
        "net net net"
        "blk blk blk";
      margin-bottom: 12px; border: 1px solid var(--border); border-radius: 10px; padding: 8px 10px;
    }
    td {
      display: flex; justify-content: space-between; align-items: center; gap: 8px; border-bottom: 0;
      padding: 4px 0; min-width: 0;
    }
    td::before { content: attr(data-label); color: var(--muted); font-size: 12px; flex-shrink: 0; }

    td[data-label='Name'] { grid-area: name; font-weight: 600; padding-bottom: 6px; }
    td[data-label='State'] { grid-area: state; padding-bottom: 6px; }
    td[data-label='Name']::before, td[data-label='State']::before { display: none; }

    /* CPU %, Restarts and Up since are short values: the label stacks above the value instead of
       pairing label:value inline, so three fit comfortably side by side. */
    td[data-label='CPU %'], td[data-label='Restarts'], td[data-label='Up since'] { display: block; }
    td[data-label='CPU %']::before, td[data-label='Restarts']::before, td[data-label='Up since']::before {
      display: block; margin-bottom: 2px;
    }
    td[data-label='CPU %'] { grid-area: cpu; }
    td[data-label='Restarts'] { grid-area: restarts; }
    td[data-label='Up since'] { grid-area: up; }

    /* Memory, Network and Block I/O need their full figures legible, so each gets the card's full
       width as an ordinary label/value line. */
    td[data-label='Memory'] { grid-area: mem; }
    td[data-label='Network'] { grid-area: net; }
    td[data-label='Block I/O'] { grid-area: blk; }
  }
</style>
